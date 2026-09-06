// Package blockcodec holds the msgpack decoding helpers shared by the HTTP
// layer, the storage backends and the importer.
package blockcodec

import (
	"encoding/base64"
	"fmt"

	"github.com/algorand/conduit/conduit/data"
	"github.com/algorand/go-algorand-sdk/v2/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algonode/blocksrv/store"
)

// DecodeBlockData decodes a raw conduit BlockData blob (header + payset + delta + cert).
func DecodeBlockData(blob []byte) (*data.BlockData, error) {
	bd := &data.BlockData{}
	if err := msgpack.Decode(blob, bd); err != nil {
		return nil, fmt.Errorf("decode BlockData: %w", err)
	}
	return bd, nil
}

// DecodeGenesis decodes a msgpack encoded genesis.
func DecodeGenesis(blob []byte) (*types.Genesis, error) {
	g := &types.Genesis{}
	if err := msgpack.Decode(blob, g); err != nil {
		return nil, fmt.Errorf("decode genesis: %w", err)
	}
	return g, nil
}

// GenesisInfo decodes a genesis blob and returns its id and base64 hash.
func GenesisInfo(blob []byte) (store.GenesisInfo, error) {
	g, err := DecodeGenesis(blob)
	if err != nil {
		return store.GenesisInfo{}, err
	}
	h := g.Hash()
	return store.GenesisInfo{
		ID:   g.ID(),
		Hash: base64.StdEncoding.EncodeToString(h[:]),
	}, nil
}
