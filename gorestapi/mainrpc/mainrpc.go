package mainrpc

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/algonode/blocksrv/gorestapi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/stampede"
	"go.uber.org/zap"
)

// Server is the API web server
type Server struct {
	logger  *zap.SugaredLogger
	router  chi.Router
	dbStore gorestapi.Ledger
}

// Setup will setup the API listener
func Setup(router chi.Router, dbStore gorestapi.Ledger) error {

	s := &Server{
		logger:  zap.S().With("package", "rpc"),
		router:  router,
		dbStore: dbStore,
	}

	customKeyFunc := func(r *http.Request) uint64 {
		// Read the request payload, and then setup buffer for future reader
		var buf []byte
		if r.Body != nil {
			buf, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(buf))
		}

		// Prepare cache key based on request URL path and the request data payload.
		key := stampede.BytesToHash([]byte(strings.ToLower(r.URL.Path)), []byte(r.URL.RawQuery), buf)
		//zap.S().Infof("Key:%d Path:%s Q:%s", key, r.URL.Path, r.URL.RawQuery)
		return key
	}

	cached10Sec := stampede.HandlerWithKey(512, 10*time.Second, customKeyFunc)

	s.router.Route("/v2/ledger", func(r chi.Router) {
		r.Get("/sync", s.SyncGet())
		r.Delete("/sync", s.SyncDelete())
		r.Post("/sync", s.SyncPost())
	})

	s.router.With(cached10Sec).Route("/v2/deltas", func(r chi.Router) {
		r.Get("/{round}", s.GetLedgerStateDelta())
	})

	s.router.With(cached10Sec).Route("/v2/blocks", func(r chi.Router) {
		r.Get("/{round}", s.GetLedgerBlock())
	})

	s.router.Route("/n2/conduit", func(r chi.Router) {
		r.With(cached10Sec).Get("/blockdata/{round}", s.GetLedgerBlockData())
		r.Put("/blockdata/{round}", s.PutLedgerStateDelta())
		r.With(cached10Sec).Get("/genesis", s.GetLedgerGenesis())
		r.Put("/genesis", s.PutLedgerGenesis())
	})

	return nil

}
