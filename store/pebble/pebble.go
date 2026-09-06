// Package pebble is the local PebbleDB storage backend.
package pebble

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/blockcodec"
	"github.com/algonode/blocksrv/store/rounds"
)

const (
	keyLastRound = "lastRound"
	keyGenesis   = "genesis"
	metaPrefix   = "meta-"
	probeLimit   = 100_000
)

// Client is the database client
type Client struct {
	logger  *zap.SugaredLogger
	db      *pebble.DB
	rt      *rounds.Tracker
	genesis atomic.Pointer[store.GenesisInfo]

	lastMu    sync.Mutex
	persisted uint64 // lastRound value known to be on disk
}

func pebbleInit(path string, logger *zap.SugaredLogger) (*pebble.DB, error) {
	opts := &pebble.Options{
		Logger:                   logger,
		MaxOpenFiles:             1000,
		MaxConcurrentCompactions: func() int { return runtime.NumCPU() },
		Levels:                   make([]pebble.LevelOptions, 7),
	}
	opts.Experimental.ReadSamplingMultiplier = -1

	for i := 0; i < len(opts.Levels); i++ {
		l := &opts.Levels[i]
		l.BlockSize = 32 << 10       // 32 KB
		l.IndexBlockSize = 256 << 10 // 256 KB
		l.FilterPolicy = bloom.FilterPolicy(10)
		l.FilterType = pebble.TableFilter
		if i > 0 {
			l.Compression = pebble.ZstdCompression
			l.TargetFileSize = opts.Levels[i-1].TargetFileSize * 2
		}
		l.EnsureDefaults()
	}
	opts.Levels[6].FilterPolicy = nil

	return pebble.Open(path, opts)
}

// New opens the database and resolves the last stored round.
func New(cfg *koanf.Koanf) (*Client, error) {
	logger := zap.S().With("package", "store.pebble")
	path := cfg.String("pebble.path")
	db, err := pebbleInit(path, logger)
	if err != nil {
		return nil, err
	}
	c := &Client{logger: logger, db: db}

	persisted, err := c.loadLastRnd()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	last := persisted
	if last == 0 {
		last = rounds.FindLast(c.existsRnd, rounds.MaxRound)
	}
	last = rounds.ProbeForward(c.existsRnd, last, probeLimit)
	c.persisted = persisted
	c.rt = rounds.New(last)
	if err := c.persistLast(last); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := c.refreshGenesisInfo(); err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Warnw("stored genesis is unreadable", "error", err)
	}
	logger.Infof("Initialized PebbleDB store:%s with lastRound:%d GH:%s", path, last, c.GetLedgerGenesisB64())
	return c, nil
}

func blockKey(round uint64) []byte { return []byte(fmt.Sprintf("dblock-%d", round)) }
func metaKey(key string) []byte    { return []byte(metaPrefix + key) }

func mapErr(err error) error {
	if errors.Is(err, pebble.ErrNotFound) {
		return store.ErrNotFound
	}
	return err
}

func (c *Client) loadLastRnd() (uint64, error) {
	val, closer, err := c.db.Get([]byte(keyLastRound))
	if errors.Is(err, pebble.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(val) != 8 {
		return 0, fmt.Errorf("corrupt %s value (%d bytes)", keyLastRound, len(val))
	}
	return binary.LittleEndian.Uint64(val), nil
}

// persistLast writes round as lastRound unless a higher value is already on disk.
func (c *Client) persistLast(round uint64) error {
	c.lastMu.Lock()
	defer c.lastMu.Unlock()
	if round <= c.persisted {
		return nil
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, round)
	if err := c.db.Set([]byte(keyLastRound), buf, pebble.Sync); err != nil {
		return err
	}
	c.persisted = round
	return nil
}

func (c *Client) existsRnd(round uint64) bool {
	ok, err := c.HasLedgerBlock(context.Background(), round)
	if err != nil {
		c.logger.Warnw("probe failed", "round", round, "error", err)
	}
	return ok
}

// GetLedgerStateDelta returns the raw BlockData blob, long-polling for future rounds.
func (c *Client) GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error) {
	if round > c.rt.Last() {
		if _, err := c.rt.Wait(ctx, round); err != nil {
			return nil, nil, err
		}
	}
	val, closer, err := c.db.Get(blockKey(round))
	return val, closer, mapErr(err)
}

// PutLedgerBlockData stores the raw BlockData blob for round.
func (c *Client) PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error {
	if err := c.db.Set(blockKey(round), bData, pebble.Sync); err != nil {
		return err
	}
	if c.rt.Advance(round) {
		c.logger.With("round", round).Info("New block")
		go func() {
			if err := c.persistLast(c.rt.Last()); err != nil {
				c.logger.Errorw("persist lastRound", "error", err)
			}
		}()
	}
	return nil
}

// HasLedgerBlock reports whether round is stored.
func (c *Client) HasLedgerBlock(ctx context.Context, round uint64) (bool, error) {
	_, closer, err := c.db.Get(blockKey(round))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close()
	return true, nil
}

func (c *Client) GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error) {
	val, closer, err := c.db.Get([]byte(keyGenesis))
	return val, closer, mapErr(err)
}

func (c *Client) PutLedgerGenesis(ctx context.Context, gData []byte) error {
	info, err := blockcodec.GenesisInfo(gData)
	if err != nil {
		return err
	}
	if cur := c.genesis.Load(); cur != nil && cur.Hash != info.Hash {
		return fmt.Errorf("%w: store already holds genesis %s", store.ErrConflict, cur.ID)
	}
	if err := c.db.Set([]byte(keyGenesis), gData, pebble.Sync); err != nil {
		return err
	}
	c.genesis.Store(&info)
	return nil
}

func (c *Client) refreshGenesisInfo() error {
	blob, closer, err := c.GetLedgerGenesis(context.Background())
	if err != nil {
		return err
	}
	defer closer.Close()
	info, err := blockcodec.GenesisInfo(blob)
	if err != nil {
		return err
	}
	c.genesis.Store(&info)
	return nil
}

// GetLedgerGenesisInfo returns the stored genesis identity, if any.
func (c *Client) GetLedgerGenesisInfo() (store.GenesisInfo, bool) {
	if g := c.genesis.Load(); g != nil {
		return *g, true
	}
	return store.GenesisInfo{}, false
}

// GetLedgerGenesisB64 returns the base64 genesis hash or "" when no genesis is stored.
func (c *Client) GetLedgerGenesisB64() string {
	info, _ := c.GetLedgerGenesisInfo()
	return info.Hash
}

func (c *Client) GetLedgerLastBlock() uint64 { return c.rt.Last() }

func (c *Client) WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error) {
	return c.rt.Wait(ctx, round)
}

// GetLedgerMeta returns a copy of a small meta value.
func (c *Client) GetLedgerMeta(ctx context.Context, key string) ([]byte, error) {
	val, closer, err := c.db.Get(metaKey(key))
	if err != nil {
		return nil, mapErr(err)
	}
	defer closer.Close()
	return bytes.Clone(val), nil
}

func (c *Client) PutLedgerMeta(ctx context.Context, key string, val []byte) error {
	return c.db.Set(metaKey(key), val, pebble.Sync)
}

// Close flushes and closes the database.
func (c *Client) Close() error {
	return c.db.Close()
}
