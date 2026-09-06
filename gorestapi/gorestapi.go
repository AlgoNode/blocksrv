package gorestapi

import (
	"context"
	"io"

	"github.com/algonode/blocksrv/store"
)

// Ledger is the block storage backend. Every backend stores one raw conduit
// BlockData blob per round plus the genesis; the HTTP layer derives the
// algod-shaped responses from that blob.
type Ledger interface {
	// GetLedgerStateDelta returns the raw BlockData blob for round. Rounds above
	// the last stored round long-poll until they arrive or ctx is done. Missing
	// rounds return store.ErrNotFound. The closer must be called once the blob
	// is no longer needed.
	GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error)
	// PutLedgerBlockData stores the raw BlockData blob for round.
	PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error
	// HasLedgerBlock reports whether round is stored, without long-polling.
	HasLedgerBlock(ctx context.Context, round uint64) (bool, error)

	GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error)
	// PutLedgerGenesis stores the genesis; a different genesis than the one
	// already stored is refused with store.ErrConflict.
	PutLedgerGenesis(ctx context.Context, gData []byte) error
	// GetLedgerGenesisInfo returns the stored genesis identity, if any.
	GetLedgerGenesisInfo() (store.GenesisInfo, bool)

	GetLedgerLastBlock() uint64
	WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error)

	// GetLedgerMeta and PutLedgerMeta keep small opaque values (e.g. import
	// progress) next to the blocks. Missing keys return store.ErrNotFound.
	GetLedgerMeta(ctx context.Context, key string) ([]byte, error)
	PutLedgerMeta(ctx context.Context, key string, val []byte) error

	// Close flushes pending state and releases the backend.
	Close() error
}
