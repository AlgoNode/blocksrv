package rounds

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAdvanceIsMonotonicUnderConcurrency(t *testing.T) {
	tr := New(0)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(base uint64) {
			defer wg.Done()
			for r := uint64(1); r <= 200; r++ {
				tr.Advance((r * 7) % 199) // deliberately out of order
			}
			tr.Advance(base)
		}(uint64(i))
	}
	wg.Wait()
	if got := tr.Last(); got != 198 {
		t.Fatalf("Last() = %d, want 198", got)
	}
	if tr.Advance(198) || tr.Advance(5) {
		t.Fatal("Advance must be a no-op for lower or equal rounds")
	}
	if !tr.Advance(199) {
		t.Fatal("Advance must move for a higher round")
	}
}

func TestWaitReturnsImmediatelyForKnownRounds(t *testing.T) {
	tr := New(10)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	last, err := tr.Wait(ctx, 10)
	if err != nil || last != 10 {
		t.Fatalf("Wait(10) = %d, %v", last, err)
	}
	last, err = tr.Wait(ctx, 3)
	if err != nil || last != 10 {
		t.Fatalf("Wait(3) = %d, %v", last, err)
	}
}

func TestWaitWakesOnAdvance(t *testing.T) {
	tr := New(0)
	done := make(chan uint64, 1)
	go func() {
		last, _ := tr.Wait(context.Background(), 5)
		done <- last
	}()
	time.Sleep(20 * time.Millisecond)
	tr.Advance(3) // not enough
	select {
	case <-done:
		t.Fatal("woke up before round 5")
	case <-time.After(30 * time.Millisecond):
	}
	tr.Advance(7) // past the waited round
	select {
	case last := <-done:
		if last != 7 {
			t.Fatalf("woke with last=%d, want 7", last)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestWaitHonoursContext(t *testing.T) {
	tr := New(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	last, err := tr.Wait(ctx, 100)
	if err == nil || last != 1 {
		t.Fatalf("Wait = %d, %v; want ctx error and last=1", last, err)
	}
}

func TestFindLast(t *testing.T) {
	for _, last := range []uint64{0, 1, 2, 999, 1000, 12_345_678, MaxRound - 1} {
		probes := 0
		exists := func(r uint64) bool { probes++; return r <= last }
		if got := FindLast(exists, MaxRound); got != last {
			t.Fatalf("FindLast(last=%d) = %d", last, got)
		}
		if probes > 45 {
			t.Fatalf("FindLast(last=%d) used %d probes", last, probes)
		}
	}
	// nothing stored at all: exists(0) is false
	if got := FindLast(func(uint64) bool { return false }, MaxRound); got != 0 {
		t.Fatalf("FindLast(empty) = %d", got)
	}
}

func TestProbeForward(t *testing.T) {
	exists := func(r uint64) bool { return r <= 12 }
	if got := ProbeForward(exists, 10, 100); got != 12 {
		t.Fatalf("ProbeForward = %d, want 12", got)
	}
	if got := ProbeForward(exists, 12, 100); got != 12 {
		t.Fatalf("ProbeForward at end = %d, want 12", got)
	}
	if got := ProbeForward(exists, 5, 3); got != 8 {
		t.Fatalf("ProbeForward limited = %d, want 8", got)
	}
}
