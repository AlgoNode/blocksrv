package store

import (
	"errors"
	"fmt"
)

// Standard storage errors shared by every backend.
var (
	// ErrNotFound is returned when a round, the genesis or a meta key does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write would replace existing data with
	// something incompatible, e.g. a genesis for a different network.
	ErrConflict = errors.New("conflict")
)

// GenesisInfo identifies the ledger a store holds, in the same form as algod's /v2/status.
type GenesisInfo struct {
	ID   string `json:"genesis-id"`
	Hash string `json:"genesis-hash"` // base64 of the genesis hash
}

type ErrorType int
type ErrorOp int

const (
	ErrorTypeNone ErrorType = iota
	ErrorTypeIncomplete
	ErrorTypeForeignKey
	ErrorTypeDuplicate
	ErrorTypeInvalid
	ErrorTypeQuery

	ErrorOpSave ErrorOp = iota
	ErrorOpGet
	ErrorOpDelete
	ErrorOpFind
)

type Error struct {
	Type ErrorType
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) ErrorForOp(op ErrorOp) error {
	switch e.Type {
	case ErrorTypeNone:
		return nil
	case ErrorTypeIncomplete:
		return fmt.Errorf("missing data: %w", e.Err)
	case ErrorTypeForeignKey:
		return fmt.Errorf("foreign key: %w", e.Err)
	case ErrorTypeDuplicate:
		return fmt.Errorf("duplicate: %w", e.Err)
	case ErrorTypeInvalid:
		return fmt.Errorf("invalid data: %w", e.Err)
	}
	return e.Err
}

type Results struct {
	Count   int64       `json:"count"`
	Results interface{} `json:"results"`
}
