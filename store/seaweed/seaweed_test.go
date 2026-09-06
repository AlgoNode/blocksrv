package seaweed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"
	"go.uber.org/zap"

	"github.com/algonode/blocksrv/store"
)

// memStore is an in-memory objectStore for tests.
type memStore struct {
	mu       sync.Mutex
	objects  map[string]memObject
	puts     int
	putDelay time.Duration
	failPut  func(key string) error
}

type memObject struct {
	payload []byte
	meta    objectMeta
}

func newMemStore() *memStore { return &memStore{objects: make(map[string]memObject)} }

func (m *memStore) Put(ctx context.Context, key string, payload []byte, meta objectMeta) error {
	if m.putDelay > 0 {
		time.Sleep(m.putDelay)
	}
	if m.failPut != nil {
		if err := m.failPut(key); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{payload: bytes.Clone(payload), meta: meta}
	m.puts++
	return nil
}

func (m *memStore) get(key string) (memObject, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	return o, ok
}

func (m *memStore) Get(ctx context.Context, key string) (io.ReadCloser, objectInfo, error) {
	o, ok := m.get(key)
	if !ok {
		return nil, objectInfo{}, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.payload)), objectInfo{Size: int64(len(o.payload)), ContentType: o.meta.ContentType}, nil
}

func (m *memStore) Stat(ctx context.Context, key string) (objectInfo, error) {
	o, ok := m.get(key)
	if !ok {
		return objectInfo{}, store.ErrNotFound
	}
	return objectInfo{Size: int64(len(o.payload)), ContentType: o.meta.ContentType}, nil
}

func (m *memStore) Exists(ctx context.Context, key string) (bool, error) {
	_, ok := m.get(key)
	return ok, nil
}

func (m *memStore) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}

func testLogger() *zap.SugaredLogger { return zap.NewNop().Sugar() }

func genesisBlob(network string) []byte {
	return msgpack.Encode(types.Genesis{SchemaID: "v1", Network: network})
}

func blockBlob(round uint64) []byte {
	return msgpack.Encode(data.BlockData{BlockHeader: types.BlockHeader{Round: types.Round(round)}})
}

func newTestClient(t *testing.T, st objectStore) *Client {
	t.Helper()
	c, err := newFromStore(testLogger(), st, Config{Bucket: "test", Timeout: time.Second})
	if err != nil {
		t.Fatalf("newFromStore: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readLatest(t *testing.T, st *memStore, key string) (latestDoc, bool) {
	t.Helper()
	o, ok := st.get(key)
	if !ok {
		return latestDoc{}, false
	}
	var doc latestDoc
	if err := json.Unmarshal(o.payload, &doc); err != nil {
		t.Fatalf("latest.json unreadable: %v", err)
	}
	return doc, true
}

func TestPutGetRoundTrip(t *testing.T) {
	st := newMemStore()
	c := newTestClient(t, st)
	ctx := context.Background()

	if err := c.PutLedgerGenesis(ctx, genesisBlob("blocksrv-test")); err != nil {
		t.Fatalf("PutLedgerGenesis: %v", err)
	}
	for r := uint64(0); r <= 3; r++ {
		if err := c.PutLedgerBlockData(ctx, r, blockBlob(r)); err != nil {
			t.Fatalf("PutLedgerBlockData(%d): %v", r, err)
		}
	}
	if got := c.GetLedgerLastBlock(); got != 3 {
		t.Fatalf("last = %d, want 3", got)
	}
	blob, closer, err := c.GetLedgerStateDelta(ctx, 2)
	if err != nil || !bytes.Equal(blob, blockBlob(2)) {
		t.Fatalf("GetLedgerStateDelta(2) = %v, %v", blob, err)
	}
	_ = closer.Close()
	if has, err := c.HasLedgerBlock(ctx, 3); err != nil || !has {
		t.Fatalf("HasLedgerBlock(3) = %v, %v", has, err)
	}
	if has, err := c.HasLedgerBlock(ctx, 4); err != nil || has {
		t.Fatalf("HasLedgerBlock(4) = %v, %v", has, err)
	}

	o, ok := st.get("blockdata/000/000/000/000000000003.msgp")
	if !ok {
		t.Fatal("object stored under the wrong key")
	}
	if o.meta.ContentType != ContentTypeMsgpack || o.meta.CacheControl != CacheImmutable || o.meta.UserMeta[MetaRound] != "3" {
		t.Fatalf("object metadata = %+v", o.meta)
	}

	gi, ok := c.GetLedgerGenesisInfo()
	if !ok || gi.ID != "blocksrv-test-v1" || gi.Hash == "" {
		t.Fatalf("genesis info = %+v, %v", gi, ok)
	}
	waitFor(t, "latest.json", func() bool {
		doc, ok := readLatest(t, st, "latest.json")
		return ok && doc.LastRound == 3 && doc.GenesisID == "blocksrv-test-v1"
	})
	o, _ = st.get("latest.json")
	if o.meta.CacheControl != CacheNoCache || o.meta.ContentType != ContentTypeJSON {
		t.Fatalf("latest.json metadata = %+v", o.meta)
	}
}

func TestMissingAndFutureRounds(t *testing.T) {
	st := newMemStore()
	c := newTestClient(t, st)
	ctx := context.Background()
	for _, r := range []uint64{0, 1, 3} {
		if err := c.PutLedgerBlockData(ctx, r, blockBlob(r)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.GetLedgerStateDelta(ctx, 2); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing round error = %v, want ErrNotFound", err)
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, _, err := c.GetLedgerStateDelta(tctx, 9); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("future round error = %v, want DeadlineExceeded", err)
	}
}

func TestPutFailureDoesNotAdvance(t *testing.T) {
	st := newMemStore()
	c := newTestClient(t, st)
	ctx := context.Background()
	if err := c.PutLedgerBlockData(ctx, 0, blockBlob(0)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("gateway down")
	st.failPut = func(key string) error {
		if key == "blockdata/000/000/000/000000000001.msgp" {
			return boom
		}
		return nil
	}
	if err := c.PutLedgerBlockData(ctx, 1, blockBlob(1)); !errors.Is(err, boom) {
		t.Fatalf("expected put failure, got %v", err)
	}
	if c.GetLedgerLastBlock() != 0 {
		t.Fatalf("last advanced despite failure: %d", c.GetLedgerLastBlock())
	}
	st.failPut = nil
	if err := c.PutLedgerBlockData(ctx, 1, blockBlob(1)); err != nil {
		t.Fatal(err)
	}
	if c.GetLedgerLastBlock() != 1 {
		t.Fatalf("last = %d after retry, want 1", c.GetLedgerLastBlock())
	}
}

func seed(t *testing.T, st *memStore, from, to uint64) {
	t.Helper()
	l := newLayout("")
	for r := from; r <= to; r++ {
		k, _ := l.blockDataKey(r)
		if err := st.Put(context.Background(), k, blockBlob(r), objectMeta{ContentType: ContentTypeMsgpack}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartupScansWhenPointerMissing(t *testing.T) {
	st := newMemStore()
	seed(t, st, 0, 12)
	c := newTestClient(t, st)
	if c.GetLedgerLastBlock() != 12 {
		t.Fatalf("last = %d, want 12", c.GetLedgerLastBlock())
	}
	waitFor(t, "latest.json repair", func() bool {
		doc, ok := readLatest(t, st, "latest.json")
		return ok && doc.LastRound == 12
	})
}

func TestStartupHealsStalePointer(t *testing.T) {
	st := newMemStore()
	seed(t, st, 0, 12)
	stale, _ := json.Marshal(latestDoc{LastRound: 10})
	_ = st.Put(context.Background(), "latest.json", stale, objectMeta{})
	c := newTestClient(t, st)
	if c.GetLedgerLastBlock() != 12 {
		t.Fatalf("last = %d, want 12", c.GetLedgerLastBlock())
	}
}

func TestStartupEmptyBucket(t *testing.T) {
	c := newTestClient(t, newMemStore())
	if c.GetLedgerLastBlock() != 0 {
		t.Fatalf("last = %d, want 0", c.GetLedgerLastBlock())
	}
	if _, ok := c.GetLedgerGenesisInfo(); ok {
		t.Fatal("unexpected genesis info")
	}
	if _, _, err := c.GetLedgerGenesis(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("genesis error = %v", err)
	}
}

func TestStartupFailsOnProbeError(t *testing.T) {
	st := &failingStore{memStore: newMemStore()}
	_, err := newFromStore(testLogger(), st, Config{Bucket: "test"})
	if err == nil {
		t.Fatal("expected startup error when probes fail")
	}
}

type failingStore struct{ *memStore }

func (f *failingStore) Exists(ctx context.Context, key string) (bool, error) {
	return false, errors.New("boom")
}

func TestLatestWriterCoalescesAndIsMonotonic(t *testing.T) {
	st := newMemStore()
	st.putDelay = 2 * time.Millisecond
	c := newTestClient(t, st)
	before := st.putCount()
	const n = 300
	for i := uint64(1); i <= n; i++ {
		c.rt.Advance(i)
		c.latest.kick()
	}
	// out-of-order kicks after the maximum must not regress the pointer
	c.latest.kick()
	waitFor(t, "final latest.json", func() bool {
		doc, ok := readLatest(t, st, "latest.json")
		return ok && doc.LastRound == n
	})
	writes := st.putCount() - before
	if writes >= n/2 {
		t.Fatalf("latest writer did %d writes for %d kicks; expected coalescing", writes, n)
	}
	if c.latest.current().LastRound != n {
		t.Fatalf("persisted = %d", c.latest.current().LastRound)
	}
}

func TestLatestWriterRetriesAfterFailure(t *testing.T) {
	st := newMemStore()
	c := newTestClient(t, st)
	var mu sync.Mutex
	fail := true
	st.failPut = func(key string) error {
		mu.Lock()
		defer mu.Unlock()
		if key == "latest.json" && fail {
			return errors.New("transient")
		}
		return nil
	}
	if err := c.PutLedgerBlockData(context.Background(), 5, blockBlob(5)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := readLatest(t, st, "latest.json"); ok {
		t.Fatal("latest.json written despite failing store")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	waitFor(t, "latest.json after retry", func() bool {
		doc, ok := readLatest(t, st, "latest.json")
		return ok && doc.LastRound == 5
	})
}

func TestGenesisConflict(t *testing.T) {
	st := newMemStore()
	c := newTestClient(t, st)
	ctx := context.Background()
	a := genesisBlob("net-a")
	if err := c.PutLedgerGenesis(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := c.PutLedgerGenesis(ctx, genesisBlob("net-b")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different genesis accepted: %v", err)
	}
	if err := c.PutLedgerGenesis(ctx, a); err != nil {
		t.Fatalf("re-put of the same genesis failed: %v", err)
	}
	// a fresh client on the same bucket sees the stored genesis
	c2 := newTestClient(t, st)
	blob, closer, err := c2.GetLedgerGenesis(ctx)
	if err != nil || !bytes.Equal(blob, a) {
		t.Fatalf("genesis round trip: %v", err)
	}
	_ = closer.Close()
	if err := c2.PutLedgerGenesis(ctx, genesisBlob("net-b")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict not detected by fresh client: %v", err)
	}
}

func TestWaitLedgerBlockWakesOnPut(t *testing.T) {
	c := newTestClient(t, newMemStore())
	ctx := context.Background()
	got := make(chan error, 1)
	go func() {
		blob, _, err := c.GetLedgerStateDelta(ctx, 2)
		if err == nil && !bytes.Equal(blob, blockBlob(2)) {
			err = fmt.Errorf("wrong blob")
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	for r := uint64(0); r <= 2; r++ {
		if err := c.PutLedgerBlockData(ctx, r, blockBlob(r)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll never resolved")
	}
}

func TestMeta(t *testing.T) {
	c := newTestClient(t, newMemStore())
	ctx := context.Background()
	if _, err := c.GetLedgerMeta(ctx, "import"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing meta error = %v", err)
	}
	if err := c.PutLedgerMeta(ctx, "import", []byte(`{"next":5}`)); err != nil {
		t.Fatal(err)
	}
	v, err := c.GetLedgerMeta(ctx, "import")
	if err != nil || string(v) != `{"next":5}` {
		t.Fatalf("meta = %q, %v", v, err)
	}
	if err := c.PutLedgerMeta(ctx, "a/b", nil); err == nil {
		t.Fatal("meta key with slash accepted")
	}
}
