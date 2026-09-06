package mainrpc

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/algonode/blocksrv/server"
	"github.com/algonode/blocksrv/store/blockcodec"
	"github.com/go-chi/chi/v5"
)

type SyncGetResponse struct {
	Round uint64 `json:"round"`
}

// StatusResponse is the algod-compatible subset of /v2/status blocksrv can answer.
type StatusResponse struct {
	LastRound   uint64 `json:"last-round"`
	GenesisID   string `json:"genesis-id,omitempty"`
	GenesisHash string `json:"genesis-hash,omitempty"`
}

const blockResponseHasBlockCacheControl = "public, max-age=31536000, immutable" // 31536000 seconds are one year.

func parseRound(r *http.Request) (uint64, error) {
	strRound := chi.URLParam(r, "round")
	if strRound == "" {
		return 0, fmt.Errorf(errInvalidRoundParam, "missing")
	}
	round, err := strconv.ParseUint(strRound, 10, 64)
	if err != nil {
		return 0, fmt.Errorf(errInvalidRoundParam, err)
	}
	return round, nil
}

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

// StatusGet returns the last stored round and the genesis identity.
//
// @ID StatusGet
// @Tags Ledger
// @Summary Status
// @Description Last stored round and genesis identity (algod /v2/status subset)
// @Success 200 {object} StatusResponse
// @Router /v2/status [get]
func (s *Server) StatusGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := StatusResponse{LastRound: s.dbStore.GetLedgerLastBlock()}
		if gi, ok := s.dbStore.GetLedgerGenesisInfo(); ok {
			resp.GenesisID = gi.ID
			resp.GenesisHash = gi.Hash
		}
		w.Header().Set("Cache-Control", "no-cache")
		server.RenderJSON(w, http.StatusOK, resp)
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
// @Failure 404 {object} server.ErrResponse "Round not found"
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/deltas/{round} [get]
func (s *Server) GetLedgerStateDelta() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		round, err := parseRound(r)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			renderStoreErr(w, err, fmt.Sprintf(errFailedRetrievingStateDelta, roundNotFound(round)))
			return
		}
		defer closer.Close()
		format := r.URL.Query().Get("format")
		if format == "msgp" || format == "msgpack" {
			dBlob, err := getDeltaBlobFromBDBlob(data)
			if err != nil {
				server.RenderErrInternal(w, errFailedEncodingResponse, err)
				return
			}
			server.RenderBlob(w, "application/msgpack", dBlob, blockResponseHasBlockCacheControl)
			return
		}
		delta, err := getJSONDeltaFromBD(data)
		if err != nil {
			server.RenderErrInternal(w, errFailedEncodingResponse, err)
			return
		}
		server.RenderBlob(w, "application/json", delta, "")
	}
}

// GetLedgerBlock returns the algod-shaped block (header, payset, cert) as msgpack.
//
// @ID GetLedgerBlock
// @Tags Ledger
// @Summary GetLedgerBlock
// @Description Block with certificate, msgpack encoded like algod /v2/blocks/{round}?format=msgpack
// @Param round path int true "round number" example(1)
// @Success 200
// @Failure 404 {object} server.ErrResponse "Round not found"
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /v2/blocks/{round} [get]
func (s *Server) GetLedgerBlock() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		round, err := parseRound(r)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			renderStoreErr(w, err, errFailedLookingUpLedger)
			return
		}
		defer closer.Close()
		bBlob, err := getBlockBlobFromBDBlob(data)
		if err != nil {
			server.RenderErrInternal(w, errFailedEncodingResponse, err)
			return
		}
		server.RenderBlob(w, "application/msgpack", bBlob, blockResponseHasBlockCacheControl)
	}
}

// GetLedgerBlockData returns the raw stored BlockData blob.
//
// @ID GetLedgerBlockData
// @Tags Conduit
// @Summary GetLedgerBlockData
// @Description Raw conduit BlockData (header, payset, delta, cert) msgpack blob
// @Param round path int true "round number" example(1)
// @Success 200
// @Failure 404 {object} server.ErrResponse "Round not found"
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /n2/conduit/blockdata/{round} [get]
func (s *Server) GetLedgerBlockData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		round, err := parseRound(r)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		data, closer, err := s.dbStore.GetLedgerStateDelta(r.Context(), round)
		if err != nil {
			renderStoreErr(w, err, errFailedLookingUpLedger)
			return
		}
		defer closer.Close()
		server.RenderBlob(w, "application/msgpack", data, blockResponseHasBlockCacheControl)
	}
}

// PutLedgerStateDelta stores a raw BlockData blob for a round.
//
// @ID PutLedgerBlockData
// @Tags Conduit
// @Summary PutLedgerBlockData
// @Description Store a raw conduit BlockData msgpack blob
// @Param round path int true "round number" example(1)
// @Success 200
// @Failure 400 {object} server.ErrResponse "Invalid blob or round mismatch"
// @Failure 500 {object} server.ErrResponse "Internal Error"
// @Router /n2/conduit/blockdata/{round} [put]
func (s *Server) PutLedgerStateDelta() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		round, err := parseRound(r)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		defer r.Body.Close()
		bData, err := io.ReadAll(r.Body)
		if err != nil {
			server.RenderErrInvalidRequest(w, fmt.Errorf("failed to read request body: %w", err))
			return
		}

		bd, err := blockcodec.DecodeBlockData(bData)
		if err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}
		if bd.Round() != round {
			server.RenderErrInvalidRequest(w, fmt.Errorf("block round does not match URL param"))
			return
		}

		if err := s.dbStore.PutLedgerBlockData(r.Context(), round, bData); err != nil {
			renderStoreErr(w, err, errFailedLookingUpLedger)
			return
		}
		server.RenderEmptyOK(w)
	}
}

// GetLedgerGenesis returns the raw genesis msgpack blob.
//
// @ID GetLedgerGenesis
// @Tags Conduit
// @Summary GetLedgerGenesis
// @Description Raw genesis msgpack blob
// @Success 200
// @Failure 404 {object} server.ErrResponse "No genesis stored"
// @Router /n2/conduit/genesis [get]
func (s *Server) GetLedgerGenesis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		blob, closer, err := s.dbStore.GetLedgerGenesis(r.Context())
		if err != nil {
			renderStoreErr(w, err, "genesis not found")
			return
		}
		defer closer.Close()
		server.RenderBlob(w, "application/msgpack", blob, blockResponseHasBlockCacheControl)
	}
}

// PutLedgerGenesis stores the genesis msgpack blob.
//
// @ID PutLedgerGenesis
// @Tags Conduit
// @Summary PutLedgerGenesis
// @Description Store the genesis msgpack blob; a different genesis than the stored one is refused
// @Success 200
// @Failure 400 {object} server.ErrResponse "Invalid genesis"
// @Failure 409 {object} server.ErrResponse "Genesis mismatch"
// @Router /n2/conduit/genesis [put]
func (s *Server) PutLedgerGenesis() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		gData, err := io.ReadAll(r.Body)
		if err != nil {
			server.RenderErrInvalidRequest(w, fmt.Errorf("failed to read request body: %w", err))
			return
		}

		if _, err := blockcodec.DecodeGenesis(gData); err != nil {
			server.RenderErrInvalidRequest(w, err)
			return
		}

		if err := s.dbStore.PutLedgerGenesis(r.Context(), gData); err != nil {
			renderStoreErr(w, err, "genesis not found")
			return
		}
		server.RenderEmptyOK(w)
	}
}
