package rounds

import (
	"sync"
	"sync/atomic"
)

// notifier is a single-shot channel; it is only ever closed once.
type notifier struct {
	signal   chan struct{}
	notified uint32
}

func newNotifier() *notifier {
	return &notifier{signal: make(chan struct{})}
}

func (n *notifier) notify() {
	if atomic.CompareAndSwapUint32(&n.notified, 0, 1) {
		close(n.signal)
	}
}

// bulletin lets callers wait for a round to be written: <-Wait(round).
type bulletin struct {
	mu          sync.Mutex
	pending     map[uint64]*notifier
	latestRound uint64
}

func newBulletin(lastRound uint64) *bulletin {
	return &bulletin{
		pending:     make(map[uint64]*notifier),
		latestRound: lastRound,
	}
}

// Wait returns a channel that is closed once the ledger reaches round.
func (b *bulletin) Wait(round uint64) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	if round <= b.latestRound {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	n, ok := b.pending[round]
	if !ok {
		n = newNotifier()
		b.pending[round] = n
	}
	return n.signal
}

// notifyRound wakes every waiter for rounds <= rnd. latestRound never regresses.
func (b *bulletin) notifyRound(rnd uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for pending, n := range b.pending {
		if pending > rnd {
			continue
		}
		delete(b.pending, pending)
		n.notify()
	}
	if rnd > b.latestRound {
		b.latestRound = rnd
	}
}
