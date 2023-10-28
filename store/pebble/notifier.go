package pebble

import (
	"sync"
	"sync/atomic"
)

// notifier is a struct that encapsulates a single-shot channel; it will only be signaled once.
type notifier struct {
	signal   chan struct{}
	notified uint32
}

// makeNotifier constructs a notifier that has not been signaled.
func makeNotifier() notifier {
	return notifier{signal: make(chan struct{}), notified: 0}
}

// notify signals the channel if it hasn't already done so
func (notifier *notifier) notify() {
	if atomic.CompareAndSwapUint32(&notifier.notified, 0, 1) {
		close(notifier.signal)
	}
}

// bulletin provides an easy way to wait on a round to be written to the ledger.
// To use it, call <-Wait(round).
type bulletin struct {
	mu                          sync.Mutex
	pendingNotificationRequests map[uint64]notifier
	latestRound                 uint64
}

func makeBulletin(lastRound uint64) *bulletin {
	b := new(bulletin)
	b.pendingNotificationRequests = make(map[uint64]notifier)
	b.latestRound = lastRound
	return b
}

// Wait returns a channel which gets closed when the ledger reaches a given round.
func (b *bulletin) Wait(round uint64) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Return an already-closed channel if we already have the block.
	if round <= b.latestRound {
		closed := make(chan struct{})
		close(closed)
		return closed
	}

	signal, exists := b.pendingNotificationRequests[round]
	if !exists {
		signal = makeNotifier()
		b.pendingNotificationRequests[round] = signal
	}
	return signal.signal
}

func (b *bulletin) notifyRound(rnd uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for pending, signal := range b.pendingNotificationRequests {
		if pending > rnd {
			continue
		}

		delete(b.pendingNotificationRequests, pending)
		signal.notify()
	}

	b.latestRound = rnd
}
