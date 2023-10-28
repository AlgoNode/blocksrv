package pebble

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
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
	key := []byte(keyLastRound)
	bVal, closer, err := db.Get(key)
	if err != nil {
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, 0)
		db.Set(key, buf, &pebble.WriteOptions{Sync: true})
		bVal = buf
	} else {
		defer closer.Close()
	}
	last := binary.LittleEndian.Uint64(bVal)

	c := &Client{
		logger: logger,
		db:     db,
	}
	c.lastRound.Store(last)
	logger.Infof("Initialized PebbleDB store:%s with lastRound:%d", path, last)

	return c, nil
}

func (c *Client) GetLedgerStateDelta(context context.Context, round uint64) ([]byte, io.Closer, error) {
	key := []byte(fmt.Sprintf("dblock-%d", round))
	return c.db.Get(key)
}
