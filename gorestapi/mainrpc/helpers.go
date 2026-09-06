package mainrpc

import (
	"context"
	"errors"
	"net/http"

	"github.com/algorand/go-algorand-sdk/v2/client/v2/common/models"
	"github.com/algorand/go-algorand-sdk/v2/encoding/json"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algonode/blocksrv/server"
	"github.com/algonode/blocksrv/store"
	"github.com/algonode/blocksrv/store/blockcodec"
)

func getDeltaFromBD(blob []byte) (*types.LedgerStateDelta, error) {
	bd, err := blockcodec.DecodeBlockData(blob)
	if err != nil {
		return nil, err
	}
	return bd.Delta, nil
}

func getDeltaBlobFromBDBlob(blob []byte) ([]byte, error) {
	delta, err := getDeltaFromBD(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(delta), nil
}

func getBlockFromBDBlob(blob []byte) (*models.BlockResponse, error) {
	bd, err := blockcodec.DecodeBlockData(blob)
	if err != nil {
		return nil, err
	}
	tmpBlk := new(models.BlockResponse)
	tmpBlk.Block.BlockHeader = bd.BlockHeader
	tmpBlk.Block.Payset = bd.Payset
	tmpBlk.Cert = bd.Certificate
	return tmpBlk, nil
}

func getBlockBlobFromBDBlob(blob []byte) ([]byte, error) {
	blk, err := getBlockFromBDBlob(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(blk), nil
}

func getJSONDeltaFromBD(blob []byte) ([]byte, error) {
	delta, err := getDeltaFromBD(blob)
	if err != nil {
		return nil, err
	}
	if delta == nil {
		return nil, errors.New("block data has no delta")
	}
	delta.Txleases = nil
	return json.Encode(delta), nil
}

// renderStoreErr maps storage errors to HTTP responses. Context errors are
// left to the timeout middleware, which already answers 504.
func renderStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		server.RenderErrNotFound(w)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, store.ErrConflict):
		server.RenderErrConflict(w, err)
	default:
		server.RenderErrInternal(w, err)
	}
}
