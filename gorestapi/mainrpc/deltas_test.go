package mainrpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"
	"github.com/go-chi/chi/v5"

	"github.com/algonode/blocksrv/store"
)

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// stubLedger returns canned results.
type stubLedger struct {
	blocks  map[uint64][]byte
	getErr  error
	putErr  error
	genesis []byte
	genErr  error
	last    uint64
	puts    map[uint64][]byte
}

func (s *stubLedger) GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error) {
	if s.getErr != nil {
		return nil, nil, s.getErr
	}
	b, ok := s.blocks[round]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return b, nopCloser{}, nil
}
func (s *stubLedger) PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if s.puts == nil {
		s.puts = map[uint64][]byte{}
	}
	s.puts[round] = bData
	return nil
}
func (s *stubLedger) HasLedgerBlock(ctx context.Context, round uint64) (bool, error) {
	_, ok := s.blocks[round]
	return ok, nil
}
func (s *stubLedger) GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error) {
	if s.genesis == nil {
		return nil, nil, store.ErrNotFound
	}
	return s.genesis, nopCloser{}, nil
}
func (s *stubLedger) PutLedgerGenesis(ctx context.Context, gData []byte) error { return s.genErr }
func (s *stubLedger) GetLedgerGenesisInfo() (store.GenesisInfo, bool) {
	return store.GenesisInfo{ID: "net-v1", Hash: "aGFzaA=="}, true
}
func (s *stubLedger) GetLedgerLastBlock() uint64 { return s.last }
func (s *stubLedger) WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error) {
	return s.last, nil
}
func (s *stubLedger) GetLedgerMeta(ctx context.Context, key string) ([]byte, error) {
	return nil, store.ErrNotFound
}
func (s *stubLedger) PutLedgerMeta(ctx context.Context, key string, val []byte) error { return nil }
func (s *stubLedger) Close() error                                                    { return nil }

func blockBlob(round uint64) []byte {
	return msgpack.Encode(data.BlockData{
		BlockHeader: types.BlockHeader{Round: types.Round(round)},
		Delta:       &types.LedgerStateDelta{Hdr: &types.BlockHeader{Round: types.Round(round)}},
	})
}

func newRouter(t *testing.T, l *stubLedger) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	if err := Setup(r, l); err != nil {
		t.Fatal(err)
	}
	return r
}

func do(h http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGetRoutes(t *testing.T) {
	l := &stubLedger{blocks: map[uint64][]byte{5: blockBlob(5)}, last: 5}
	h := newRouter(t, l)

	rec := do(h, http.MethodGet, "/n2/conduit/blockdata/5", nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), blockBlob(5)) {
		t.Fatalf("blockdata: %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != blockResponseHasBlockCacheControl {
		t.Fatalf("Cache-Control = %q", cc)
	}
	for _, p := range []string{"/v2/blocks/5", "/v2/deltas/5?format=msgpack", "/v2/deltas/5"} {
		if rec := do(h, http.MethodGet, p, nil); rec.Code != 200 {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{"/n2/conduit/blockdata/4", "/v2/blocks/4", "/v2/deltas/4", "/n2/conduit/genesis"} {
		if rec := do(h, http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", p, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v2/blocks/abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad round: %d", rec.Code)
	}
}

func TestErrorBodiesMatchAlgod(t *testing.T) {
	l := &stubLedger{blocks: map[uint64][]byte{5: blockBlob(5)}, last: 5}
	h := newRouter(t, l)
	cases := []struct {
		path string
		code int
		body string
	}{
		{"/v2/blocks/4", 404, `{"message":"failed to retrieve information from the ledger"}`},
		{"/n2/conduit/blockdata/4", 404, `{"message":"failed to retrieve information from the ledger"}`},
		{"/v2/deltas/4", 404, `{"message":"failed retrieving State Delta: round 4 not found"}`},
		{"/n2/conduit/genesis", 404, `{"message":"genesis not found"}`},
		{"/v2/blocks/abc", 400, `{"message":"Invalid format for parameter round: strconv.ParseUint: parsing \"abc\": invalid syntax"}`},
		{"/v2/blocks/-1", 400, `{"message":"Invalid format for parameter round: strconv.ParseUint: parsing \"-1\": invalid syntax"}`},
	}
	for _, c := range cases {
		rec := do(h, http.MethodGet, c.path, nil)
		if rec.Code != c.code || string(bytes.TrimSpace(rec.Body.Bytes())) != c.body {
			t.Fatalf("%s: %d %s\nwant %d %s", c.path, rec.Code, rec.Body.String(), c.code, c.body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s: Content-Type = %q", c.path, ct)
		}
	}
}

func TestStoreErrorsMapTo500(t *testing.T) {
	l := &stubLedger{getErr: errors.New("gateway down")}
	h := newRouter(t, l)
	rec := do(h, http.MethodGet, "/v2/blocks/1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	want := `{"message":"failed to retrieve information from the ledger","data":{"error":"gateway down"}}`
	if got := string(bytes.TrimSpace(rec.Body.Bytes())); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestContextErrorsWriteNothing(t *testing.T) {
	l := &stubLedger{getErr: context.DeadlineExceeded}
	h := newRouter(t, l)
	rec := do(h, http.MethodGet, "/v2/blocks/1", nil)
	if rec.Code != 200 || rec.Body.Len() != 0 { // recorder default: nothing written
		t.Fatalf("handler wrote %d %q on context error", rec.Code, rec.Body.String())
	}
}

func TestStatus(t *testing.T) {
	l := &stubLedger{last: 42}
	h := newRouter(t, l)
	rec := do(h, http.MethodGet, "/v2/status", nil)
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	want := `{"last-round":42,"genesis-id":"net-v1","genesis-hash":"aGFzaA=="}`
	if got := bytes.TrimSpace(rec.Body.Bytes()); string(got) != want {
		t.Fatalf("status body = %s", got)
	}
}

func TestPutValidation(t *testing.T) {
	l := &stubLedger{}
	h := newRouter(t, l)
	if rec := do(h, http.MethodPut, "/n2/conduit/blockdata/6", blockBlob(5)); rec.Code != http.StatusBadRequest {
		t.Fatalf("round mismatch: %d", rec.Code)
	}
	if rec := do(h, http.MethodPut, "/n2/conduit/blockdata/5", []byte("garbage")); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage: %d", rec.Code)
	}
	if rec := do(h, http.MethodPut, "/n2/conduit/blockdata/5", blockBlob(5)); rec.Code != 200 || !bytes.Equal(l.puts[5], blockBlob(5)) {
		t.Fatalf("valid put: %d", rec.Code)
	}
	l.putErr = errors.New("gateway down")
	if rec := do(h, http.MethodPut, "/n2/conduit/blockdata/5", blockBlob(5)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error on put: %d", rec.Code)
	}
}

func TestGenesisConflictIs409(t *testing.T) {
	l := &stubLedger{genErr: store.ErrConflict}
	h := newRouter(t, l)
	g := msgpack.Encode(types.Genesis{SchemaID: "v1", Network: "net"})
	rec := do(h, http.MethodPut, "/n2/conduit/genesis", g)
	if rec.Code != http.StatusConflict || string(bytes.TrimSpace(rec.Body.Bytes())) != `{"message":"conflict"}` {
		t.Fatalf("code = %d body = %s, want 409 conflict", rec.Code, rec.Body.String())
	}
	if rec := do(h, http.MethodPut, "/n2/conduit/genesis", []byte("garbage")); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage genesis: %d", rec.Code)
	}
}
