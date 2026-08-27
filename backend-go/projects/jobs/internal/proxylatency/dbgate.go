package proxylatency

import (
	"context"
	"errors"
	"time"
)

// DBConcurrencyGate bounds PostgreSQL phases without limiting upstream probe
// goroutines. A queue token is held for the whole DB operation, so callers
// cannot create an unbounded backlog while waiting for a worker slot.
type DBConcurrencyGate struct {
	queue chan struct{}
	slots chan struct{}
}

func NewDBConcurrencyGate(concurrency, queueSize int) *DBConcurrencyGate {
	if concurrency <= 0 {
		return nil
	}
	if queueSize < concurrency {
		queueSize = concurrency
	}
	return &DBConcurrencyGate{queue: make(chan struct{}, queueSize), slots: make(chan struct{}, concurrency)}
}

func (g *DBConcurrencyGate) Acquire(ctx context.Context) (release func(), wait time.Duration, err error) {
	if g == nil {
		return func() {}, 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	select {
	case g.queue <- struct{}{}:
	case <-ctx.Done():
		return nil, time.Since(started), ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
		return func() {
			<-g.slots
			<-g.queue
		}, time.Since(started), nil
	case <-ctx.Done():
		<-g.queue
		return nil, time.Since(started), ctx.Err()
	}
}

func (g *DBConcurrencyGate) QueueDepth() int {
	if g == nil {
		return 0
	}
	return len(g.queue)
}

func (g *DBConcurrencyGate) Capacity() int {
	if g == nil {
		return 0
	}
	return cap(g.slots)
}

var errDBGateUnavailable = errors.New("J3a DB concurrency gate unavailable")
