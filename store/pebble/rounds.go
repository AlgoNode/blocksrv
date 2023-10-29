package pebble

import "context"

func (c *Client) GetLedgerLastBlock() uint64 {
	return c.lastRound.Load()
}

func (c *Client) WaitLedgerBlock(ctx context.Context, round uint64) (uint64, error) {
	currentLast := c.lastRound.Load()
	if currentLast >= round {
		return currentLast, nil
	}
	select {
	case <-ctx.Done():
		return c.lastRound.Load(), ctx.Err()
	case <-c.b.Wait(round):
	}
	return c.lastRound.Load(), nil
}

func (c *Client) updateLedgerLastBlock(newLast uint64) bool {
	currentLast := c.lastRound.Load()
	if newLast > currentLast && c.lastRound.CompareAndSwap(currentLast, newLast) {
		c.b.notifyRound(newLast)
		c.saveLastRnd(newLast)
		return true
	}
	return false
}
