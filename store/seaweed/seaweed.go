// Package seaweed is the SeaweedFS storage backend. It talks to the S3
// gateway and lays objects out so the gateway can serve them to clients
// directly (see layout.go).
package seaweed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/blockcodec"
	"github.com/algonode/blocksrv/store/rounds"
)

const (
	probeLimit    = 100_000
	closeWaitTime = 15 * time.Second
)

// Config is the SeaweedFS S3 backend configuration.
type Config struct {
	Endpoint   string // host:port, no scheme
	UseSSL     bool
	AccessKey  string
	SecretKey  string
	Region     string
	Bucket     string
	Prefix     string
	Timeout    time.Duration // per S3 call
	MaxRetries int
}

// ConfigFromKoanf reads the seaweed.* keys.
func ConfigFromKoanf(k *koanf.Koanf) Config {
	return Config{
		Endpoint:   k.String("seaweed.endpoint"),
		UseSSL:     k.Bool("seaweed.use_ssl"),
		AccessKey:  k.String("seaweed.access_key"),
		SecretKey:  k.String("seaweed.secret_key"),
		Region:     k.String("seaweed.region"),
		Bucket:     k.String("seaweed.bucket"),
		Prefix:     k.String("seaweed.prefix"),
		Timeout:    k.Duration("seaweed.timeout"),
		MaxRetries: k.Int("seaweed.max_retries"),
	}
}

type genesisState struct {
	blob []byte
	info store.GenesisInfo
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// Client is the SeaweedFS backed Ledger.
type Client struct {
	logger *zap.SugaredLogger
	cfg    Config
	store  objectStore
	lay    layout
	rt     *rounds.Tracker
	latest *latestWriter

	genesis   atomic.Pointer[genesisState]
	genesisMu sync.Mutex
}

// New builds the backend from the seaweed.* config keys.
func New(k *koanf.Koanf) (*Client, error) {
	return NewWithConfig(ConfigFromKoanf(k))
}

// NewWithConfig connects to the S3 gateway, ensures the bucket exists and
// resolves the last stored round.
func NewWithConfig(cfg Config) (*Client, error) {
	logger := zap.S().With("package", "store.seaweed")
	if cfg.Endpoint == "" {
		return nil, errors.New("seaweed.endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("seaweed.bucket is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}

	var creds *credentials.Credentials
	if cfg.AccessKey == "" && cfg.SecretKey == "" {
		creds = credentials.NewStatic("", "", "", credentials.SignatureAnonymous)
	} else {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		ForceAttemptHTTP2:     true,
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        creds,
		Secure:       cfg.UseSSL,
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
		Transport:    transport,
		MaxRetries:   cfg.MaxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("seaweed client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q at %s: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("bucket %q does not exist and could not be created (the identity needs the Admin action, or create it manually): %w", cfg.Bucket, err)
		}
		logger.Infow("created bucket", "bucket", cfg.Bucket)
	}
	return newFromStore(logger, &minioStore{mc: mc, bucket: cfg.Bucket, timeout: cfg.Timeout}, cfg)
}

func newFromStore(logger *zap.SugaredLogger, st objectStore, cfg Config) (*Client, error) {
	c := &Client{
		logger: logger,
		cfg:    cfg,
		store:  st,
		lay:    newLayout(cfg.Prefix),
	}
	ctx := context.Background()
	if err := c.loadGenesis(ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("load genesis: %w", err)
	}
	last, doc, err := c.resolveLast(ctx)
	if err != nil {
		return nil, err
	}
	c.rt = rounds.New(last)
	c.latest = newLatestWriter(logger, st, c.lay.latestKey(), c.rt, c.GetLedgerGenesisInfo, doc)
	c.latest.kick() // no-op unless the pointer or genesis info is stale
	logger.Infof("Initialized SeaweedFS store bucket:%s prefix:%q with lastRound:%d GH:%s",
		cfg.Bucket, c.lay.prefix, last, c.GetLedgerGenesisB64())
	return c, nil
}

// resolveLast reads latest.json, falls back to a binary search over the
// blockdata keys, and probes forward to heal a pointer that fell behind.
func (c *Client) resolveLast(ctx context.Context) (uint64, latestDoc, error) {
	var doc latestDoc
	key := c.lay.latestKey()
	blob, err := c.getAll(ctx, key)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(blob, &doc); jerr != nil {
			c.logger.Warnw("latest pointer unreadable, rescanning", "key", key, "error", jerr)
			doc = latestDoc{}
		}
	case errors.Is(err, store.ErrNotFound):
	default:
		return 0, doc, fmt.Errorf("read %s: %w", key, err)
	}

	var probeErr error
	exists := func(r uint64) bool {
		if probeErr != nil {
			return false
		}
		ok, err := c.exists(ctx, r)
		if err != nil {
			probeErr = err
			return false
		}
		return ok
	}
	last := doc.LastRound
	if last == 0 {
		c.logger.Infow("no last round pointer, scanning bucket", "key", key)
		last = rounds.FindLast(exists, rounds.MaxRound)
	}
	last = rounds.ProbeForward(exists, last, probeLimit)
	if probeErr != nil {
		return 0, doc, fmt.Errorf("probe rounds: %w", probeErr)
	}
	return last, doc, nil
}

func (c *Client) exists(ctx context.Context, round uint64) (bool, error) {
	key, err := c.lay.blockDataKey(round)
	if err != nil {
		return false, err
	}
	return c.store.Exists(ctx, key)
}

func (c *Client) getAll(ctx context.Context, key string) ([]byte, error) {
	body, info, err := c.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var buf bytes.Buffer
	if info.Size > 0 {
		buf.Grow(int(info.Size))
	}
	if _, err := buf.ReadFrom(body); err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return buf.Bytes(), nil
}

// GetLedgerStateDelta returns the raw BlockData blob, long-polling for future rounds.
func (c *Client) GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error) {
	if round > c.rt.Last() {
		if _, err := c.rt.Wait(ctx, round); err != nil {
			return nil, nil, err
		}
	}
	key, err := c.lay.blockDataKey(round)
	if err != nil {
		return nil, nil, err
	}
	blob, err := c.getAll(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	return blob, nopCloser{}, nil
}

// PutLedgerBlockData stores the raw BlockData blob as a directly servable object.
func (c *Client) PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error {
	key, err := c.lay.blockDataKey(round)
	if err != nil {
		return err
	}
	// A client that gives up (the exporter has a 5s timeout) must not abort a
	// nearly finished upload; the store applies its own per-call timeout.
	err = c.store.Put(context.WithoutCancel(ctx), key, bData, objectMeta{
		ContentType:  ContentTypeMsgpack,
		CacheControl: CacheImmutable,
		UserMeta:     map[string]string{MetaRound: strconv.FormatUint(round, 10)},
	})
	if err != nil {
		return err
	}
	if c.rt.Advance(round) {
		c.latest.kick()
		c.logger.With("round", round).Info("New block")
	}
	return nil
}

// HasLedgerBlock reports whether round is stored.
func (c *Client) HasLedgerBlock(ctx context.Context, round uint64) (bool, error) {
	return c.exists(ctx, round)
}

func (c *Client) loadGenesis(ctx context.Context) error {
	blob, err := c.getAll(ctx, c.lay.genesisKey())
	if err != nil {
		return err
	}
	info, err := blockcodec.GenesisInfo(blob)
	if err != nil {
		return fmt.Errorf("stored genesis undecodable: %w", err)
	}
	c.genesis.Store(&genesisState{blob: blob, info: info})
	return nil
}

func (c *Client) GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error) {
	if g := c.genesis.Load(); g != nil {
		return g.blob, nopCloser{}, nil
	}
	if err := c.loadGenesis(ctx); err != nil {
		return nil, nil, err
	}
	g := c.genesis.Load()
	if g == nil {
		return nil, nil, store.ErrNotFound
	}
	return g.blob, nopCloser{}, nil
}

// PutLedgerGenesis stores the genesis. A genesis with a different hash than
// the stored one is refused, so an exporter pointed at the wrong network
// cannot poison the bucket.
func (c *Client) PutLedgerGenesis(ctx context.Context, gData []byte) error {
	info, err := blockcodec.GenesisInfo(gData)
	if err != nil {
		return err
	}
	c.genesisMu.Lock()
	defer c.genesisMu.Unlock()
	cur := c.genesis.Load()
	if cur == nil {
		if err := c.loadGenesis(ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		cur = c.genesis.Load()
	}
	if cur != nil {
		if cur.info.Hash != info.Hash {
			return fmt.Errorf("%w: bucket already holds genesis %s", store.ErrConflict, cur.info.ID)
		}
		if bytes.Equal(cur.blob, gData) {
			return nil
		}
	}
	err = c.store.Put(context.WithoutCancel(ctx), c.lay.genesisKey(), gData, objectMeta{
		ContentType:  ContentTypeMsgpack,
		CacheControl: CacheImmutable,
	})
	if err != nil {
		return err
	}
	c.genesis.Store(&genesisState{blob: gData, info: info})
	c.latest.kick()
	return nil
}

// GetLedgerGenesisInfo returns the stored genesis identity, if any.
func (c *Client) GetLedgerGenesisInfo() (store.GenesisInfo, bool) {
	if g := c.genesis.Load(); g != nil {
		return g.info, true
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

func (c *Client) GetLedgerMeta(ctx context.Context, key string) ([]byte, error) {
	k, err := c.lay.metaKey(key)
	if err != nil {
		return nil, err
	}
	return c.getAll(ctx, k)
}

func (c *Client) PutLedgerMeta(ctx context.Context, key string, val []byte) error {
	k, err := c.lay.metaKey(key)
	if err != nil {
		return err
	}
	return c.store.Put(context.WithoutCancel(ctx), k, val, objectMeta{
		ContentType:  ContentTypeBinary,
		CacheControl: CacheNoCache,
	})
}

// Close flushes latest.json and stops the writer.
func (c *Client) Close() error {
	c.latest.stop(closeWaitTime)
	return nil
}
