package mainrpc

import (
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/client/v2/common/models"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"
)

func getDelta(blob []byte) (*types.LedgerStateDelta, error) {
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, err
	}
	return bd.Delta, nil
}

func getBlockData(blob []byte) (*data.BlockData, error) {
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, err
	}
	return bd, nil
}

func getDeltaBlob(blob []byte) ([]byte, error) {
	delta, err := getDelta(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(delta), nil
}

func getBlock(blob []byte) (*models.BlockResponse, error) {
	tmpBlk := new(models.BlockResponse)
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, err
	}
	tmpBlk.Block.BlockHeader = bd.BlockHeader
	tmpBlk.Block.Payset = bd.Payset
	tmpBlk.Cert = bd.Certificate
	return tmpBlk, nil
}

func getBlockBlob(blob []byte) ([]byte, error) {
	blk, err := getBlock(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(blk), nil
}
