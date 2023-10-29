package gorestapi

import (
	"context"
	"io"
)

type Ledger interface {
	GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error)
	PutLedgerBlockData(ctx context.Context, round uint64, bData []byte) error
	GetLedgerGenesis(ctx context.Context) ([]byte, io.Closer, error)
	PutLedgerGenesis(ctx context.Context, gData []byte) error
	GetLedgerLastBlock() uint64
	WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error)
}
