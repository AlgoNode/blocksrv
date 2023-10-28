package mainrpc

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/algonode/blocksrv/server"
	"github.com/go-chi/chi/v5"
)

type SyncGetResponse struct {
	Round uint64 `json:"round"`
}

const blockResponseHasBlockCacheControl = "public, max-age=31536000, immutable" // 31536000 seconds are one year.

// SyncGet Gets
//
// @ID SyncGet
// @Tags Sync
// @Summary Sync
// @Description Sync
// @Success 200 {object} SyncGetResponse
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/sync [get]
func (s *Server) SyncGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := SyncGetResponse{
			Round: 0,
		}
		server.RenderJSON(w, http.StatusOK, data)
	}
}

// SyncDelete Deletes
//
// @ID SyncDelete
// @Tags Sync
// @Summary Sync
// @Description Sync
// @Success 200
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/sync [delete]
func (s *Server) SyncDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		server.RenderEmptyOK(w)
	}
}

// SyncPost Posts
//
// @ID SyncPost
// @Tags Sync
// @Summary Sync
// @Description Sync
// @Success 200
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/sync [post]
func (s *Server) SyncPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		server.RenderEmptyOK(w)
	}
}

// GetLedgerStateDelta Gets Block with deltas
//
// @ID GetLedgerStateDelta
// @Tags Ledger
// @Summary GetLedgerStateDelta
// @Description GetLedgerStateDelta
// @Success 200
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/sync [post]
func (s *Server) GetLedgerStateDelta() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		strRound := chi.URLParam(r, "round")
		if strRound == "" {
			server.RenderErrInvalidRequest(w, fmt.Errorf("required round parameter missing"))
			return
		}
		round, err := strconv.ParseUint(strRound, 10, 64)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		//ToDo blocking read - wait for next round
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer closer.Close()
		server.RenderBlob(w, "application/msgpack", data, blockResponseHasBlockCacheControl)

	}
}
