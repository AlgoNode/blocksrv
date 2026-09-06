package seaweed

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/rounds"
)

// latestDoc is the content of latest.json, the pointer direct S3 clients poll.
type latestDoc struct {
	LastRound   uint64 `json:"last-round"`
	GenesisID   string `json:"genesis-id,omitempty"`
	GenesisHash string `json:"genesis-hash,omitempty"`
	Updated     string `json:"updated,omitempty"`
}

// latestWriter persists latest.json from a single goroutine. Kicks coalesce,
// writes are rate limited to one per minInterval (except the final flush on
// stop), every write carries the tracker's current maximum, and the stored
// round never regresses even when block PUTs complete out of order.
type latestWriter struct {
	logger      *zap.SugaredLogger
	store       objectStore
	key         string
	rt          *rounds.Tracker
	genesis     func() (store.GenesisInfo, bool)
	minInterval time.Duration
	lastWrite   time.Time

	kickCh   chan struct{}
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	mu        sync.Mutex
	persisted latestDoc
	writes    atomic.Uint64
}

func newLatestWriter(logger *zap.SugaredLogger, st objectStore, key string, rt *rounds.Tracker,
	genesis func() (store.GenesisInfo, bool), persisted latestDoc, minInterval time.Duration) *latestWriter {
	lw := &latestWriter{
		logger:      logger,
		store:       st,
		key:         key,
		rt:          rt,
		genesis:     genesis,
		minInterval: minInterval,
		kickCh:      make(chan struct{}, 1),
		stopCh:      make(chan struct{}),
		done:        make(chan struct{}),
		persisted:   persisted,
	}
	go lw.run()
	return lw
}

// kick asks the writer to persist the current state; it never blocks.
func (lw *latestWriter) kick() {
	select {
	case lw.kickCh <- struct{}{}:
	default:
	}
}

func (lw *latestWriter) run() {
	defer close(lw.done)
	final := func() {
		if _, err := lw.flush(); err != nil {
			lw.logger.Warnw("final latest.json write failed", "error", err)
		}
	}
	backoff := time.Second
	for {
		select {
		case <-lw.kickCh:
		case <-lw.stopCh:
			final()
			return
		}
		// Rate limit: kicks arriving meanwhile coalesce into the next write.
		if wait := lw.minInterval - time.Since(lw.lastWrite); wait > 0 {
			select {
			case <-time.After(wait):
			case <-lw.stopCh:
				final()
				return
			}
		}
		for {
			wrote, err := lw.flush()
			if err == nil {
				if wrote {
					lw.lastWrite = time.Now()
				}
				backoff = time.Second
				break
			}
			lw.logger.Warnw("latest.json write failed, retrying", "error", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-lw.stopCh:
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

// flush writes latest.json if the round or genesis changed since the last
// successful write and reports whether it wrote.
func (lw *latestWriter) flush() (bool, error) {
	doc := latestDoc{LastRound: lw.rt.Last()}
	if gi, ok := lw.genesis(); ok {
		doc.GenesisID = gi.ID
		doc.GenesisHash = gi.Hash
	}
	lw.mu.Lock()
	cur := lw.persisted
	lw.mu.Unlock()
	if doc.LastRound < cur.LastRound {
		doc.LastRound = cur.LastRound
	}
	if doc.LastRound == cur.LastRound && doc.GenesisID == cur.GenesisID {
		return false, nil
	}
	doc.Updated = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	if err := lw.store.Put(context.Background(), lw.key, payload, objectMeta{
		ContentType:  ContentTypeJSON,
		CacheControl: CacheNoCache,
	}); err != nil {
		return false, err
	}
	lw.mu.Lock()
	lw.persisted = doc
	lw.mu.Unlock()
	lw.writes.Add(1)
	return true, nil
}

func (lw *latestWriter) current() latestDoc {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.persisted
}

// stop performs a final flush and waits for the goroutine, at most wait.
func (lw *latestWriter) stop(wait time.Duration) {
	lw.stopOnce.Do(func() { close(lw.stopCh) })
	select {
	case <-lw.done:
	case <-time.After(wait):
		lw.logger.Warn("latest.json writer did not stop in time")
	}
}
