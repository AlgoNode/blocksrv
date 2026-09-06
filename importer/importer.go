// Package importer mirrors blocks from another blocksrv instance into the
// local store, e.g. to migrate from a pebble deployment to a SeaweedFS one.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/gorestapi"
	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/blockcodec"
	"github.com/algonode/blocksrv/store/rounds"
)

// MetaKey is the ledger meta key holding the import progress.
const MetaKey = "import"

// Config controls the importer.
type Config struct {
	From    string        // base URL of the source blocksrv; empty disables the importer
	Start   uint64        // first round to import
	End     uint64        // last round to import; 0 = the source's current last round
	Follow  bool          // keep mirroring new rounds once caught up
	Workers int           // parallel fetches
	Timeout time.Duration // per source request

	// Tunables with sensible defaults; exposed for tests.
	PollInterval time.Duration // how often to re-read the source's last round when caught up
	PersistEvery time.Duration // how often to persist progress
	ProbeTimeout time.Duration // per HEAD probe when the source has no /v2/status
	MaxBackoff   time.Duration
}

// ConfigFromKoanf reads the import.* keys.
func ConfigFromKoanf(k *koanf.Koanf) Config {
	return Config{
		From:    strings.TrimRight(strings.TrimSpace(k.String("import.from")), "/"),
		Start:   uint64(k.Int64("import.start")),
		End:     uint64(k.Int64("import.end")),
		Follow:  k.Bool("import.follow"),
		Workers: k.Int("import.workers"),
		Timeout: k.Duration("import.timeout"),
	}
}

// Enabled reports whether a source is configured.
func (c Config) Enabled() bool { return c.From != "" }

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.Timeout <= 0 {
		c.Timeout = 60 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.PersistEvery <= 0 {
		c.PersistEvery = 5 * time.Second
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = 3 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	return c
}

// progress is what gets persisted in the ledger meta.
type progress struct {
	From    string `json:"from"`
	Next    uint64 `json:"next"` // every round below next is done
	Updated string `json:"updated"`
}

// Importer copies rounds from a remote blocksrv into a Ledger.
type Importer struct {
	cfg    Config
	ledger gorestapi.Ledger
	logger *zap.SugaredLogger
	hc     *http.Client

	mu            sync.Mutex
	done          map[uint64]struct{}
	next          uint64 // lowest round not yet completed
	persistedNext uint64
	imported      uint64
	skipped       uint64
}

// New validates the config and builds an importer.
func New(cfg Config, ledger gorestapi.Ledger) (*Importer, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled() {
		return nil, errors.New("import.from is empty")
	}
	if !strings.HasPrefix(cfg.From, "http://") && !strings.HasPrefix(cfg.From, "https://") {
		return nil, fmt.Errorf("import.from must be an http(s) URL, got %q", cfg.From)
	}
	if cfg.End != 0 && cfg.End < cfg.Start {
		return nil, fmt.Errorf("import.end (%d) is below import.start (%d)", cfg.End, cfg.Start)
	}
	return &Importer{
		cfg:    cfg,
		ledger: ledger,
		logger: zap.S().With("package", "importer"),
		hc: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:        cfg.Workers * 2,
				MaxIdleConnsPerHost: cfg.Workers * 2,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		done: make(map[uint64]struct{}),
	}, nil
}

// Run imports until the configured end (or forever when following) or until
// ctx is done. Progress is persisted periodically and on return.
func (im *Importer) Run(ctx context.Context) error {
	if err := im.importGenesis(ctx); err != nil {
		return err
	}

	start := im.cfg.Start
	if p, ok := im.loadProgress(ctx); ok && p.From == im.cfg.From && p.Next > start {
		im.logger.Infow("resuming import", "next", p.Next, "persisted", p.Updated)
		start = p.Next
	}
	im.mu.Lock()
	im.next = start
	im.persistedNext = start
	im.mu.Unlock()

	end := im.cfg.End
	if end == 0 {
		var err error
		if end, err = im.remoteLastRetry(ctx); err != nil {
			return err
		}
	}
	im.logger.Infow("import starting", "from", im.cfg.From, "start", start, "end", end,
		"follow", im.cfg.Follow, "workers", im.cfg.Workers)

	work := make(chan uint64)
	var wg sync.WaitGroup
	for i := 0; i < im.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range work {
				if err := im.importRound(ctx, r); err != nil {
					return // only on ctx cancellation
				}
				im.markDone(r)
			}
		}()
	}
	finish := func() {
		close(work)
		wg.Wait()
		im.persist(context.WithoutCancel(ctx), true)
	}

	lastPersist := time.Now()
	for r := start; ; r++ {
		for r > end {
			if !im.cfg.Follow {
				finish()
				im.logger.Infow("import finished", "end", end, "imported", im.imported, "skipped", im.skipped)
				return nil
			}
			newEnd, err := im.remoteLast(ctx)
			if err != nil {
				if ctx.Err() != nil {
					finish()
					return ctx.Err()
				}
				im.logger.Warnw("source status unavailable", "error", err)
			} else if newEnd > end {
				end = newEnd
				break
			}
			im.persist(ctx, false)
			select {
			case <-ctx.Done():
				finish()
				return ctx.Err()
			case <-time.After(im.cfg.PollInterval):
			}
		}
		select {
		case work <- r:
		case <-ctx.Done():
			finish()
			return ctx.Err()
		}
		if time.Since(lastPersist) >= im.cfg.PersistEvery {
			im.persist(ctx, false)
			lastPersist = time.Now()
		}
	}
}

func (im *Importer) importGenesis(ctx context.Context) error {
	url := im.cfg.From + "/n2/conduit/genesis"
	for backoff := time.Second; ; {
		body, status, err := im.fetch(ctx, url)
		switch {
		case err == nil && status == http.StatusOK:
			if _, derr := blockcodec.GenesisInfo(body); derr != nil {
				return fmt.Errorf("source genesis undecodable: %w", derr)
			}
			perr := im.ledger.PutLedgerGenesis(ctx, body)
			if perr == nil {
				return nil
			}
			if errors.Is(perr, store.ErrConflict) {
				return fmt.Errorf("source genesis does not match the local store: %w", perr)
			}
			err = perr
		case err == nil && (status == http.StatusNotFound || status == http.StatusBadRequest):
			im.logger.Warnw("source has no genesis, continuing without it", "status", status)
			return nil
		case err == nil:
			err = fmt.Errorf("unexpected status %d", status)
		}
		im.logger.Warnw("genesis import failed, retrying", "error", err, "backoff", backoff)
		if !im.sleep(ctx, backoff) {
			return ctx.Err()
		}
		backoff = im.nextBackoff(backoff)
	}
}

// importRound copies one round unless it is already stored. It retries
// transient failures until ctx is done, which is the only error it returns.
func (im *Importer) importRound(ctx context.Context, round uint64) error {
	url := fmt.Sprintf("%s/n2/conduit/blockdata/%d", im.cfg.From, round)
	badBlobs := 0
	for backoff := time.Second; ; {
		has, err := im.ledger.HasLedgerBlock(ctx, round)
		if err == nil && has {
			im.mu.Lock()
			im.skipped++
			im.mu.Unlock()
			return nil
		}
		if err == nil {
			var status int
			var body []byte
			body, status, err = im.fetch(ctx, url)
			switch {
			case err == nil && status == http.StatusOK:
				bd, derr := blockcodec.DecodeBlockData(body)
				if derr != nil || bd.Round() != round {
					badBlobs++
					if badBlobs >= 3 {
						im.logger.Errorw("source returned an invalid blob repeatedly, skipping round",
							"round", round, "error", derr)
						return nil
					}
					err = fmt.Errorf("invalid blob for round %d: %v", round, derr)
					break
				}
				if err = im.ledger.PutLedgerBlockData(ctx, round, body); err == nil {
					im.mu.Lock()
					im.imported++
					im.mu.Unlock()
					return nil
				}
			case err == nil && (status == http.StatusNotFound || status == http.StatusBadRequest):
				im.logger.Warnw("round missing on source, skipping", "round", round, "status", status)
				return nil
			case err == nil:
				err = fmt.Errorf("unexpected status %d", status)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		im.logger.Warnw("import round failed, retrying", "round", round, "error", err, "backoff", backoff)
		if !im.sleep(ctx, backoff) {
			return ctx.Err()
		}
		backoff = im.nextBackoff(backoff)
	}
}

func (im *Importer) markDone(round uint64) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.done[round] = struct{}{}
	for {
		if _, ok := im.done[im.next]; !ok {
			return
		}
		delete(im.done, im.next)
		im.next++
	}
}

// Next returns the lowest round not yet completed.
func (im *Importer) Next() uint64 {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.next
}

func (im *Importer) loadProgress(ctx context.Context) (progress, bool) {
	blob, err := im.ledger.GetLedgerMeta(ctx, MetaKey)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			im.logger.Warnw("import progress unreadable, starting over", "error", err)
		}
		return progress{}, false
	}
	var p progress
	if err := json.Unmarshal(blob, &p); err != nil {
		im.logger.Warnw("import progress corrupt, starting over", "error", err)
		return progress{}, false
	}
	return p, true
}

func (im *Importer) persist(ctx context.Context, final bool) {
	im.mu.Lock()
	next := im.next
	changed := next != im.persistedNext
	imported, skipped := im.imported, im.skipped
	im.mu.Unlock()
	if !changed && !final {
		return
	}
	p := progress{From: im.cfg.From, Next: next, Updated: time.Now().UTC().Format(time.RFC3339)}
	blob, _ := json.Marshal(p)
	if err := im.ledger.PutLedgerMeta(ctx, MetaKey, blob); err != nil {
		im.logger.Warnw("persist import progress failed", "error", err)
		return
	}
	im.mu.Lock()
	im.persistedNext = next
	im.mu.Unlock()
	if changed {
		im.logger.Infow("import progress", "next", next, "imported", imported, "skipped", skipped)
	}
}

// remoteLast asks the source for its last round via /v2/status, falling back
// to probing rounds on sources that predate that endpoint. A future round
// long-polls on the source, so a probe timeout counts as absent.
func (im *Importer) remoteLast(ctx context.Context) (uint64, error) {
	body, status, err := im.fetch(ctx, im.cfg.From+"/v2/status")
	if err != nil {
		return 0, err
	}
	if status == http.StatusOK {
		var st struct {
			LastRound uint64 `json:"last-round"`
		}
		if err := json.Unmarshal(body, &st); err == nil {
			return st.LastRound, nil
		}
	}
	var probeErr error
	exists := func(r uint64) bool {
		if probeErr != nil {
			return false
		}
		pctx, cancel := context.WithTimeout(ctx, im.cfg.ProbeTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(pctx, http.MethodHead,
			fmt.Sprintf("%s/n2/conduit/blockdata/%d", im.cfg.From, r), nil)
		if err != nil {
			probeErr = err
			return false
		}
		resp, err := im.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				probeErr = ctx.Err()
			}
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	last := rounds.FindLast(exists, rounds.MaxRound)
	if probeErr != nil {
		return 0, probeErr
	}
	return last, nil
}

func (im *Importer) remoteLastRetry(ctx context.Context) (uint64, error) {
	for backoff := time.Second; ; {
		last, err := im.remoteLast(ctx)
		if err == nil {
			return last, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		im.logger.Warnw("source last round unavailable, retrying", "error", err, "backoff", backoff)
		if !im.sleep(ctx, backoff) {
			return 0, ctx.Err()
		}
		backoff = im.nextBackoff(backoff)
	}
}

// fetch returns the body for a 200 response, or the status for any other
// response, or a transport error.
func (im *Importer) fetch(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := im.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

func (im *Importer) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (im *Importer) nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > im.cfg.MaxBackoff {
		d = im.cfg.MaxBackoff
	}
	return d
}
