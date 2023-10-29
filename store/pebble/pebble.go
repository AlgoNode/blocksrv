package pebble

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"
)

const (
	keyLastRound = "lastRound"
)

// Client is the database client
type Client struct {
	logger    *zap.SugaredLogger
	db        *pebble.DB
	b         *bulletin
	lastRound atomic.Uint64
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

	db, err := pebble.Open(path, opts)

	if err != nil {
		return nil, err
	}

	return db, nil
}

// New returns a new database client
func New(cfg *koanf.Koanf) (*Client, error) {

	logger := zap.S().With("package", "store.pebble")
	path := cfg.String("pebble.path")
	db, err := pebbleInit(path, logger)
	if err != nil {
		return nil, err
	}

	c := &Client{
		logger: logger,
		db:     db,
	}

	key := []byte(keyLastRound)
	bVal, closer, err := db.Get(key)
	last := uint64(0)
	if err != nil {
		last = 0
	} else {
		last = binary.LittleEndian.Uint64(bVal)
		defer closer.Close()
	}
	if last == 0 {
		last = c.findLast()
		c.saveLastRnd(last)
	}

	c.b = makeBulletin(last)
	c.lastRound.Store(last)
	logger.Infof("Initialized PebbleDB store:%s with lastRound:%d", path, last)

	return c, nil
}

func (c *Client) saveLastRnd(round uint64) error {
	buf := make([]byte, 8)
	key := []byte(keyLastRound)
	binary.LittleEndian.PutUint64(buf, round)
	return c.db.Set(key, buf, pebble.Sync)
}

func (c *Client) existsRnd(round uint64) bool {
	key := []byte(fmt.Sprintf("dblock-%d", round))
	_, closer, err := c.db.Get(key)
	if err != nil {
		return false
	}
	closer.Close()
	c.logger.Infof("exists %d", round)
	return true
}

func (c *Client) findLast() uint64 {
	n := uint64(100_000_000)
	l := uint64(0)
	h := n - 1
	c.logger.Infof("looking for last round %d..%d", l, h)
	for h > l {
		mid := (h + l) >> 1
		if c.existsRnd(mid) {
			l = mid
		} else {
			h = mid - 1
		}
		if h-l == 1 {
			if c.existsRnd(h) {
				return h
			}
			return l
		}
	}
	return l
}

func (c *Client) GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error) {
	if round > c.GetLedgerLastBlock() {
		if _, err := c.WaitLedgerBlock(ctx, round); err != nil {
			return nil, nil, err
		}
	}
	key := []byte(fmt.Sprintf("dblock-%d", round))
	return c.db.Get(key)
}

func (c *Client) PutLedgerBlockData(context context.Context, round uint64, bData []byte) error {
	key := []byte(fmt.Sprintf("dblock-%d", round))
	if err := c.db.Set(key, bData, &pebble.WriteOptions{Sync: true}); err != nil {
		return err
	}
	go func() {
		if c.updateLedgerLastBlock(round) {
			c.logger.With("round", strconv.Itoa(int(round))).Info("New block")
		}
	}()
	return nil
}

func (c *Client) GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error) {
	key := []byte("genesis")
	return c.db.Get(key)
}

func (c *Client) PutLedgerGenesis(context context.Context, gData []byte) error {
	key := []byte("genesis")
	if err := c.db.Set(key, gData, &pebble.WriteOptions{Sync: true}); err != nil {
		return err
	}
	return nil
}
