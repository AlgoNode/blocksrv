package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/blockcodec"
	"github.com/algonode/blocksrv/store/rounds"
)

func genesisBlob(network string) []byte {
	return msgpack.Encode(types.Genesis{SchemaID: "v1", Network: network})
}

func blockBlob(round uint64) []byte {
	return msgpack.Encode(data.BlockData{BlockHeader: types.BlockHeader{Round: types.Round(round)}})
}

// fakeLedger is an in-memory gorestapi.Ledger.
type fakeLedger struct {
	mu      sync.Mutex
	blocks  map[uint64][]byte
	genesis []byte
	meta    map[string][]byte
	rt      *rounds.Tracker
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{blocks: map[uint64][]byte{}, meta: map[string][]byte{}, rt: rounds.New(0)}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func (f *fakeLedger) GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blocks[round]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return b, nopCloser{}, nil
}

func (f *fakeLedger) PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error {
	f.mu.Lock()
	f.blocks[round] = bytes.Clone(bData)
	f.mu.Unlock()
	f.rt.Advance(round)
	return nil
}

func (f *fakeLedger) HasLedgerBlock(ctx context.Context, round uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blocks[round]
	return ok, nil
}

func (f *fakeLedger) GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.genesis == nil {
		return nil, nil, store.ErrNotFound
	}
	return f.genesis, nopCloser{}, nil
}

func (f *fakeLedger) PutLedgerGenesis(ctx context.Context, gData []byte) error {
	info, err := blockcodec.GenesisInfo(gData)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.genesis != nil {
		cur, _ := blockcodec.GenesisInfo(f.genesis)
		if cur.Hash != info.Hash {
			return store.ErrConflict
		}
	}
	f.genesis = bytes.Clone(gData)
	return nil
}

func (f *fakeLedger) GetLedgerGenesisInfo() (store.GenesisInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.genesis == nil {
		return store.GenesisInfo{}, false
	}
	gi, _ := blockcodec.GenesisInfo(f.genesis)
	return gi, true
}

func (f *fakeLedger) GetLedgerLastBlock() uint64 { return f.rt.Last() }

func (f *fakeLedger) WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error) {
	return f.rt.Wait(ctx, round)
}

func (f *fakeLedger) GetLedgerMeta(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.meta[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return bytes.Clone(v), nil
}

func (f *fakeLedger) PutLedgerMeta(ctx context.Context, key string, val []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meta[key] = bytes.Clone(val)
	return nil
}

func (f *fakeLedger) Close() error { return nil }

func (f *fakeLedger) has(round uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blocks[round]
	return ok
}

func (f *fakeLedger) progressNext(t *testing.T) uint64 {
	t.Helper()
	v, err := f.GetLedgerMeta(context.Background(), MetaKey)
	if err != nil {
		t.Fatalf("no progress persisted: %v", err)
	}
	var p progress
	if err := json.Unmarshal(v, &p); err != nil {
		t.Fatal(err)
	}
	return p.Next
}

// fakeSource emulates the HTTP surface of a blocksrv instance.
type fakeSource struct {
	mu       sync.Mutex
	genesis  []byte
	blocks   map[uint64][]byte
	last     uint64
	noStatus bool // emulate an old blocksrv without /v2/status
	requests map[uint64]int
	srv      *httptest.Server
}

func newFakeSource(network string, last uint64) *fakeSource {
	s := &fakeSource{genesis: genesisBlob(network), blocks: map[uint64][]byte{}, last: last, requests: map[uint64]int{}}
	for r := uint64(0); r <= last; r++ {
		s.blocks[r] = blockBlob(r)
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *fakeSource) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.URL.Path == "/n2/conduit/genesis":
		w.Header().Set("Content-Type", "application/msgpack")
		_, _ = w.Write(s.genesis)
	case r.URL.Path == "/v2/status":
		if s.noStatus {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]uint64{"last-round": s.last})
	case strings.HasPrefix(r.URL.Path, "/n2/conduit/blockdata/"):
		round, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/n2/conduit/blockdata/"), 10, 64)
		if err != nil {
			http.Error(w, "bad round", http.StatusBadRequest)
			return
		}
		if round > s.last {
			// a real blocksrv long-polls for future rounds
			s.mu.Unlock()
			<-r.Context().Done()
			s.mu.Lock()
			return
		}
		s.requests[round]++
		b, ok := s.blocks[round]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/msgpack")
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func (s *fakeSource) extend(last uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for r := s.last + 1; r <= last; r++ {
		s.blocks[r] = blockBlob(r)
	}
	s.last = last
}

func (s *fakeSource) requested(round uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[round]
}

func testConfig(src *fakeSource) Config {
	return Config{
		From:         src.srv.URL,
		Workers:      4,
		Timeout:      5 * time.Second,
		PollInterval: 20 * time.Millisecond,
		PersistEvery: 10 * time.Millisecond,
		ProbeTimeout: 50 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestImportsRangeAndSkipsExisting(t *testing.T) {
	src := newFakeSource("net-a", 9)
	defer src.srv.Close()
	ledger := newFakeLedger()
	existing := []byte("already here")
	_ = ledger.PutLedgerBlockData(context.Background(), 3, existing)

	cfg := testConfig(src)
	cfg.Follow = false
	im, err := New(cfg, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := im.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for r := uint64(0); r <= 9; r++ {
		if !ledger.has(r) {
			t.Fatalf("round %d not imported", r)
		}
	}
	if b, _, _ := ledger.GetLedgerStateDelta(context.Background(), 3); !bytes.Equal(b, existing) {
		t.Fatal("existing round was overwritten")
	}
	if src.requested(3) != 0 {
		t.Fatal("existing round was fetched from the source")
	}
	if !bytes.Equal(ledger.genesis, src.genesis) {
		t.Fatal("genesis not imported")
	}
	if next := ledger.progressNext(t); next != 10 {
		t.Fatalf("progress next = %d, want 10", next)
	}
	if ledger.GetLedgerLastBlock() != 9 {
		t.Fatalf("ledger last = %d", ledger.GetLedgerLastBlock())
	}
}

func TestResumesFromPersistedProgress(t *testing.T) {
	src := newFakeSource("net-a", 9)
	defer src.srv.Close()
	ledger := newFakeLedger()
	p, _ := json.Marshal(progress{From: src.srv.URL, Next: 5})
	_ = ledger.PutLedgerMeta(context.Background(), MetaKey, p)

	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for r := uint64(0); r < 5; r++ {
		if src.requested(r) != 0 || ledger.has(r) {
			t.Fatalf("round %d was imported despite persisted progress", r)
		}
	}
	for r := uint64(5); r <= 9; r++ {
		if !ledger.has(r) {
			t.Fatalf("round %d not imported", r)
		}
	}
}

func TestProgressFromAnotherSourceIsIgnored(t *testing.T) {
	src := newFakeSource("net-a", 3)
	defer src.srv.Close()
	ledger := newFakeLedger()
	p, _ := json.Marshal(progress{From: "http://elsewhere", Next: 2})
	_ = ledger.PutLedgerMeta(context.Background(), MetaKey, p)
	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ledger.has(0) || !ledger.has(1) {
		t.Fatal("progress from a different source must be ignored")
	}
}

func TestFollowsSourceTail(t *testing.T) {
	src := newFakeSource("net-a", 2)
	defer src.srv.Close()
	ledger := newFakeLedger()
	cfg := testConfig(src)
	cfg.Follow = true
	im, _ := New(cfg, ledger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- im.Run(ctx) }()

	waitFor(t, "initial rounds", func() bool { return ledger.has(2) })
	src.extend(6)
	waitFor(t, "tail rounds", func() bool { return ledger.has(6) })
	// rounds written locally by the exporter must be skipped, not re-fetched
	_ = ledger.PutLedgerBlockData(ctx, 7, []byte("from exporter"))
	src.extend(8)
	waitFor(t, "round 8", func() bool { return ledger.has(8) })
	if src.requested(7) != 0 {
		t.Fatal("locally present round was fetched from the source")
	}
	waitFor(t, "progress", func() bool {
		v, err := ledger.GetLedgerMeta(ctx, MetaKey)
		if err != nil {
			return false
		}
		var p progress
		_ = json.Unmarshal(v, &p)
		return p.Next == 9
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestFallbackProbeWhenSourceHasNoStatus(t *testing.T) {
	src := newFakeSource("net-a", 41)
	src.noStatus = true
	defer src.srv.Close()
	ledger := newFakeLedger()
	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ledger.GetLedgerLastBlock() != 41 || !ledger.has(41) {
		t.Fatalf("ledger last = %d, want 41", ledger.GetLedgerLastBlock())
	}
}

func TestSkipsRoundMissingOnSource(t *testing.T) {
	src := newFakeSource("net-a", 5)
	defer src.srv.Close()
	src.mu.Lock()
	delete(src.blocks, 3)
	src.mu.Unlock()
	ledger := newFakeLedger()
	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ledger.has(3) {
		t.Fatal("phantom round imported")
	}
	for _, r := range []uint64{0, 1, 2, 4, 5} {
		if !ledger.has(r) {
			t.Fatalf("round %d not imported", r)
		}
	}
	if next := ledger.progressNext(t); next != 6 {
		t.Fatalf("progress next = %d, want 6", next)
	}
}

func TestExplicitRange(t *testing.T) {
	src := newFakeSource("net-a", 20)
	defer src.srv.Close()
	ledger := newFakeLedger()
	cfg := testConfig(src)
	cfg.Follow = false
	cfg.Start, cfg.End = 5, 7
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for r := uint64(0); r <= 20; r++ {
		want := r >= 5 && r <= 7
		if ledger.has(r) != want {
			t.Fatalf("round %d present=%v, want %v", r, ledger.has(r), want)
		}
	}
}

func TestGenesisConflictAborts(t *testing.T) {
	src := newFakeSource("net-b", 3)
	defer src.srv.Close()
	ledger := newFakeLedger()
	_ = ledger.PutLedgerGenesis(context.Background(), genesisBlob("net-a"))
	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	err := im.Run(context.Background())
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Run = %v, want ErrConflict", err)
	}
	if ledger.has(0) {
		t.Fatal("blocks imported despite genesis conflict")
	}
}

func TestRetriesTransientSourceErrors(t *testing.T) {
	var flaky sync.Mutex
	failures := 0
	src := newFakeSource("net-a", 3)
	defer src.srv.Close()
	inner := src.srv.Config.Handler
	src.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flaky.Lock()
		if strings.HasSuffix(r.URL.Path, "/2") && failures < 2 {
			failures++
			flaky.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		flaky.Unlock()
		inner.ServeHTTP(w, r)
	})
	ledger := newFakeLedger()
	cfg := testConfig(src)
	cfg.Follow = false
	im, _ := New(cfg, ledger)
	if err := im.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ledger.has(2) {
		t.Fatal("round 2 not imported after transient failures")
	}
	if failures != 2 {
		t.Fatalf("failures = %d", failures)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{}, newFakeLedger()); err == nil {
		t.Fatal("empty From accepted")
	}
	if _, err := New(Config{From: "old-blocksrv:8989"}, newFakeLedger()); err == nil {
		t.Fatal("From without scheme accepted")
	}
	if _, err := New(Config{From: "http://x", Start: 5, End: 3}, newFakeLedger()); err == nil {
		t.Fatal("End below Start accepted")
	}
	if _, err := New(Config{From: fmt.Sprintf("http://x:%d/", 1)}, newFakeLedger()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
