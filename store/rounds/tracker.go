// Package rounds tracks the last round a store holds and lets readers wait for
// future rounds. It is shared by every storage backend.
package rounds

import (
	"context"
	"sync/atomic"
)

// MaxRound bounds the round space the stores are laid out for (12 decimal digits).
const MaxRound = uint64(1_000_000_000_000)

// Tracker keeps the highest round known to be stored and wakes waiters.
type Tracker struct {
	last atomic.Uint64
	b    *bulletin
}

// New returns a tracker starting at last.
func New(last uint64) *Tracker {
	t := &Tracker{b: newBulletin(last)}
	t.last.Store(last)
	return t
}

// Last returns the highest stored round.
func (t *Tracker) Last() uint64 {
	return t.last.Load()
}

// Wait blocks until round is stored or ctx is done. It returns the last round in either case.
func (t *Tracker) Wait(ctx context.Context, round uint64) (uint64, error) {
	if cur := t.last.Load(); cur >= round {
		return cur, nil
	}
	select {
	case <-ctx.Done():
		return t.last.Load(), ctx.Err()
	case <-t.b.Wait(round):
	}
	return t.last.Load(), nil
}

// Advance moves the last round forward to round and wakes waiters.
// It returns true only when it moved; a lower or equal round is a no-op.
func (t *Tracker) Advance(round uint64) bool {
	for {
		cur := t.last.Load()
		if round <= cur {
			return false
		}
		if t.last.CompareAndSwap(cur, round) {
			t.b.notifyRound(round)
			return true
		}
	}
}
