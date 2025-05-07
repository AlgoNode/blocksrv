package mainrpc

import (
	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/client/v2/common/models"
	"github.com/algorand/go-algorand-sdk/v2/encoding/json"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"
)

func getDeltaFromBD(blob []byte) (*types.LedgerStateDelta, error) {
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, err
	}
	return bd.Delta, nil
}

func getBlockDataFromBDBlob(blob []byte) (*data.BlockData, error) {
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, err
	}
	return bd, nil
}

func getDeltaBlobFromBDBlob(blob []byte) ([]byte, error) {
	delta, err := getDeltaFromBD(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(delta), nil
}

func getBlockFromBDBlob(blob []byte) (*models.BlockResponse, error) {
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

func getBlockBlobFromBDBlob(blob []byte) ([]byte, error) {
	blk, err := getBlockFromBDBlob(blob)
	if err != nil {
		return nil, err
	}
	return msgpack.Encode(blk), nil
}

func getGenesisFromGenesisBlob(blob []byte) (*types.Genesis, error) {
	g := &types.Genesis{}
	if err := msgpack.Decode(blob, g); err != nil {
		return nil, err
	}
	return g, nil
}

func getJSONDeltaFromBD(blob []byte) ([]byte, error) {
	delta, err := getDeltaFromBD(blob)
	delta.Txleases = nil
	if err != nil {
		return nil, err
	}
	return json.Encode(delta), nil
}

// // StateDeltaSubset exports a subset of ledgercore.StateDelta fields for a sparse encoding
// type StateDeltaSubset struct {
// 	_struct    struct{} `codec:",omitempty,omitemptyarray"`
// 	Accts      types.AccountDeltas
// 	KvMods     map[string]types.KvValueDelta
// 	Txids      map[types.Txid]types.IncludedTransactions
// 	Txleases   map[types.Txlease]types.Round
// 	Creatables map[types.CreatableIndex]types.ModifiedCreatable
// 	Hdr        *types.BlockHeader
// }

// func convertStateDelta(delta *types.LedgerStateDelta) *StateDeltaSubset {
// 	kvmods := maps.Clone(delta.KvMods)
// 	txids := maps.Clone(delta.Txids)
// 	txleases := maps.Clone(delta.Txleases)
// 	creatables := maps.Clone(delta.Creatables)

// 	var accR []types.BalanceRecord
// 	var appR []types.AppResourceRecord
// 	var assetR []types.AssetResourceRecord
// 	if len(delta.Accts.Accts) > 0 {
// 		accR = slices.Clone(delta.Accts.Accts)
// 	}
// 	if len(delta.Accts.AppResources) > 0 {
// 		appR = slices.Clone(delta.Accts.AppResources)
// 	}
// 	if len(delta.Accts.AssetResources) > 0 {
// 		assetR = slices.Clone(delta.Accts.AssetResources)
// 	}
// 	return &StateDeltaSubset{
// 		Accts: types.AccountDeltas{
// 			Accts:          accR,
// 			AppResources:   appR,
// 			AssetResources: assetR,
// 		},
// 		KvMods:     kvmods,
// 		Txids:      txids,
// 		Txleases:   txleases,
// 		Creatables: creatables,
// 		Hdr:        delta.Hdr,
// 	}
// }
