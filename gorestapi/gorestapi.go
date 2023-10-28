package gorestapi

import (
	"context"
	"io"
)

type Ledger interface {
	GetLedgerStateDelta(ctx context.Context, round uint64) ([]byte, io.Closer, error)
	GetLedgerBlock(ctx context.Context, round uint64) ([]byte, io.Closer, error)
}
