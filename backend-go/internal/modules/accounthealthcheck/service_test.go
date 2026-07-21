package accounthealthcheck

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	job "juhe-ai/backend-go/internal/jobs/accounthealthcheck"
	"juhe-ai/backend-go/internal/store/port"
)

type fakeCandidates struct {
	pages []port.AccountHealthCheckCandidatePage
	seen  []string
}

func (f *fakeCandidates) ListAccountHealthCheckCandidates(_ context.Context, cursor string, limit int, _ time.Time) (port.AccountHealthCheckCandidatePage, error) {
	f.seen = append(f.seen, cursor)
	if len(f.pages) == 0 {
		return port.AccountHealthCheckCandidatePage{}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
	}
	return page, nil
}

type fakeEnqueuer struct {
	mu       sync.Mutex
	items    []job.Task
	active   int
	max      int
	delay    time.Duration
	canceled bool
}

func (f *fakeEnqueuer) Enqueue(ctx context.Context, task job.Task) error {
	f.mu.Lock()
	f.active++
	if f.active > f.max {
		f.max = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		f.canceled = true
		return ctx.Err()
	case <-time.After(f.delay):
		f.mu.Lock()
		f.items = append(f.items, task)
		f.mu.Unlock()
		return nil
	}
}

func candidate(id string, revision int) port.AccountHealthCheckCandidate {
	return port.AccountHealthCheckCandidate{ID: id, ConfigRevision: revision, Status: "active", Schedulable: true, BoundGroupID: "group-1"}
}

func TestScheduleUsesCursorAndHardCandidateLimitWithBoundedConcurrency(t *testing.T) {
	reader := &fakeCandidates{pages: []port.AccountHealthCheckCandidatePage{
		{Items: []port.AccountHealthCheckCandidate{candidate("a", 1), candidate("b", 2)}, NextCursor: "b", HasMore: true},
		{Items: []port.AccountHealthCheckCandidate{candidate("c", 3), candidate("d", 4)}, NextCursor: "d", HasMore: true},
	}}
	enqueuer := &fakeEnqueuer{delay: 5 * time.Millisecond}
	result, err := NewService(reader, enqueuer).Schedule(context.Background(), ScheduleConfig{PageSize: 2, MaxCandidates: 3, Concurrency: 2})
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if result.Enqueued != 3 || result.Scanned != 3 {
		t.Fatalf("result = %+v, want three candidates", result)
	}
	if enqueuer.max > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", enqueuer.max)
	}
	if len(reader.seen) != 2 || reader.seen[0] != "" || reader.seen[1] != "b" {
		t.Fatalf("cursors = %#v, want ['', 'b']", reader.seen)
	}
}

func TestScheduleStopsOnCancellation(t *testing.T) {
	reader := &fakeCandidates{pages: []port.AccountHealthCheckCandidatePage{{Items: []port.AccountHealthCheckCandidate{candidate("a", 1), candidate("b", 1)}, HasMore: false}}}
	enqueuer := &fakeEnqueuer{delay: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewService(reader, enqueuer).Schedule(ctx, ScheduleConfig{PageSize: 2, MaxCandidates: 2, Concurrency: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if result.Enqueued != 0 {
		t.Fatalf("enqueued = %d, want 0", result.Enqueued)
	}
}
