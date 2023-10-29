package mainrpc

import (
	"fmt"
	"io"
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
// @Router /v2/ledger/sync [get]
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
// @Router /v2/ledger/sync [delete]
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
// @Router /v2/ledger/sync [post]
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
// @Param round path int true "round number" example(1)
// @Success 200
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/deltas/{round} [get]
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
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer closer.Close()
		dBlob, err := getDeltaBlobFromBDBlob(data)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}
		server.RenderBlob(w, "application/msgpack", dBlob, blockResponseHasBlockCacheControl)
	}
}

func (s *Server) GetLedgerBlock() http.HandlerFunc {
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
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer closer.Close()
		bBlob, err := getBlockBlobFromBDBlob(data)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}
		server.RenderBlob(w, "application/msgpack", bBlob, blockResponseHasBlockCacheControl)
	}
}

func (s *Server) GetLedgerBlockData() http.HandlerFunc {
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
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer closer.Close()
		server.RenderBlob(w, "application/msgpack", data, blockResponseHasBlockCacheControl)
	}
}

func (s *Server) PutLedgerStateDelta() http.HandlerFunc {
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
		defer r.Body.Close()
		bData, err := io.ReadAll(r.Body)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}

		bd, err := getBlockDataFromBDBlob(bData)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}
		if bd.Round() != round {
			server.RenderErrInvalidRequest(w, fmt.Errorf("block round does not match URL param"))
			return
		}

		err = s.dbStore.PutLedgerBlockData(r.Context(), round, bData)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		server.RenderEmptyOK(w)
	}
}

func (s *Server) GetLedgerGenesis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blob, closer, err := s.dbStore.GetLedgerGenesis(r.Context())
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer closer.Close()
		server.RenderBlob(w, "application/msgpack", blob, blockResponseHasBlockCacheControl)
	}
}

func (s *Server) PutLedgerGenesis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		gData, err := io.ReadAll(r.Body)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}

		_, err = getGenesisFromGenesisBlob(gData)
		if err != nil {
			server.RenderErrInternal(w, err)
			return
		}

		err = s.dbStore.PutLedgerGenesis(r.Context(), gData)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		server.RenderEmptyOK(w)
	}
}
