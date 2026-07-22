package auditruntime

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestServiceRefreshBuildsGoNativeSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 30, 0, 123, time.FixedZone("CST", 8*60*60))
	service := NewService(Sources{
		Queue: queuePortFunc(func() QueueMetrics {
			return QueueMetrics{State: StateReady, Pending: 7, PendingBytes: 8192, Capacity: 256, CapacityBytes: 32 << 20}
		}),
		Worker: workerPortFunc(func() WorkerMetrics {
			return WorkerMetrics{State: StateReady, Running: 2, Desired: 2, Restarts: 1}
		}),
		Dropped: droppedPortFunc(func() DroppedMetrics {
			return DroppedMetrics{State: StateDegraded, Success: 3, Failure: 2, Overflow: 4, Oversize: 1}
		}),
		Inflight: inflightPortFunc(func() InflightMetrics {
			return InflightMetrics{State: StateReady, Captures: 5, Bytes: 4096}
		}),
		Transport: transportPortFunc(func() TransportMetrics {
			return TransportMetrics{State: StateReady, Queued: 3, QueuedBytes: 2048, Inflight: 2, InflightBytes: 1024, Workers: 2, Completed: 20, Failed: 1, Rejected: 2}
		}),
		Storage: storagePortFunc(func() StorageMetrics {
			return StorageMetrics{State: StateDegraded, PendingWrites: 4, CompletedWrites: 18, FailedWrites: 2, ConsecutiveFailures: 1, LastSuccessAt: now.Add(-time.Minute), LastFailureAt: now.Add(-30 * time.Second), LastErrorCode: "postgres_timeout"}
		}),
	}, WithClock(func() time.Time { return now }))

	snapshot := service.Refresh()
	if snapshot.Revision != 1 {
		t.Fatalf("revision = %d, want 1", snapshot.Revision)
	}
	if !snapshot.CapturedAt.Equal(now.UTC()) || snapshot.CapturedAt.Location() != time.UTC {
		t.Fatalf("capturedAt = %s, want UTC %s", snapshot.CapturedAt, now.UTC())
	}
	if snapshot.Queue.Pending != 7 || snapshot.Queue.CapacityBytes != 32<<20 {
		t.Fatalf("queue = %#v", snapshot.Queue)
	}
	if snapshot.Worker.Running != 2 || snapshot.Worker.Restarts != 1 {
		t.Fatalf("worker = %#v", snapshot.Worker)
	}
	if snapshot.Dropped.Total != 5 {
		t.Fatalf("dropped total = %d, want success + failure without double-counting reasons", snapshot.Dropped.Total)
	}
	if snapshot.Dropped.Overflow != 4 || snapshot.Dropped.Oversize != 1 {
		t.Fatalf("dropped reasons = %#v", snapshot.Dropped)
	}
	if snapshot.Inflight.Captures != 5 || snapshot.Inflight.Bytes != 4096 {
		t.Fatalf("inflight = %#v", snapshot.Inflight)
	}
	if snapshot.Transport.Inflight != 2 || snapshot.Transport.Rejected != 2 {
		t.Fatalf("transport = %#v", snapshot.Transport)
	}
	if snapshot.Storage.LastErrorCode != "postgres_timeout" || !snapshot.Storage.LastSuccessAt.Equal(now.Add(-time.Minute).UTC()) {
		t.Fatalf("storage = %#v", snapshot.Storage)
	}
	if got := service.Snapshot(); got != snapshot {
		t.Fatalf("Snapshot() = %#v, want published %#v", got, snapshot)
	}
}

func TestServiceMarksMissingAndInvalidSourcesUnknown(t *testing.T) {
	service := NewService(Sources{
		Queue: queuePortFunc(func() QueueMetrics { return QueueMetrics{State: State("broken"), Pending: 9} }),
	})

	initial := service.Snapshot()
	assertAllUnknown(t, initial)
	if initial.Revision != 0 || !initial.CapturedAt.IsZero() {
		t.Fatalf("initial metadata = revision %d capturedAt %s", initial.Revision, initial.CapturedAt)
	}

	snapshot := service.Refresh()
	assertAllUnknown(t, snapshot)
	if snapshot.Queue.Pending != 9 {
		t.Fatalf("unknown source counters must remain observable, pending = %d", snapshot.Queue.Pending)
	}
}

func TestDroppedTotalSaturatesInsteadOfWrapping(t *testing.T) {
	service := NewService(Sources{
		Dropped: droppedPortFunc(func() DroppedMetrics {
			return DroppedMetrics{State: StateDegraded, Success: math.MaxUint64, Failure: 10, Overflow: math.MaxUint64, Oversize: math.MaxUint64}
		}),
	})

	snapshot := service.Refresh()
	if snapshot.Dropped.Total != math.MaxUint64 {
		t.Fatalf("total = %d, want saturated max uint64", snapshot.Dropped.Total)
	}
}

func TestRefreshPublishesWholeSnapshotsAtomically(t *testing.T) {
	const iterations = 5000
	var source atomicQueueMetrics
	service := NewService(Sources{Queue: &source})

	var wait sync.WaitGroup
	wait.Add(2)
	start := make(chan struct{})
	failure := make(chan QueueSnapshot, 1)

	go func() {
		defer wait.Done()
		<-start
		for index := 1; index <= iterations; index++ {
			value := uint64(index)
			source.Set(QueueMetrics{State: StateReady, Pending: value, PendingBytes: value * 2, Capacity: value * 3, CapacityBytes: value * 4})
			service.Refresh()
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < iterations*4; index++ {
			queue := service.Snapshot().Queue
			if queue.State == StateUnknown {
				continue
			}
			if queue.PendingBytes != queue.Pending*2 || queue.Capacity != queue.Pending*3 || queue.CapacityBytes != queue.Pending*4 {
				select {
				case failure <- queue:
				default:
				}
				return
			}
		}
	}()
	close(start)
	wait.Wait()
	select {
	case queue := <-failure:
		t.Fatalf("observed torn snapshot: %#v", queue)
	default:
	}
}

func assertAllUnknown(t *testing.T, snapshot Snapshot) {
	t.Helper()
	states := []State{snapshot.Queue.State, snapshot.Worker.State, snapshot.Dropped.State, snapshot.Inflight.State, snapshot.Transport.State, snapshot.Storage.State}
	for index, state := range states {
		if state != StateUnknown {
			t.Fatalf("state[%d] = %q, want unknown", index, state)
		}
	}
}

type queuePortFunc func() QueueMetrics

func (f queuePortFunc) AuditQueueMetrics() QueueMetrics { return f() }

type workerPortFunc func() WorkerMetrics

func (f workerPortFunc) AuditWorkerMetrics() WorkerMetrics { return f() }

type droppedPortFunc func() DroppedMetrics

func (f droppedPortFunc) AuditDroppedMetrics() DroppedMetrics { return f() }

type inflightPortFunc func() InflightMetrics

func (f inflightPortFunc) AuditInflightMetrics() InflightMetrics { return f() }

type transportPortFunc func() TransportMetrics

func (f transportPortFunc) AuditTransportMetrics() TransportMetrics { return f() }

type storagePortFunc func() StorageMetrics

func (f storagePortFunc) AuditStorageMetrics() StorageMetrics { return f() }

type atomicQueueMetrics struct {
	mu      sync.RWMutex
	metrics QueueMetrics
}

func (s *atomicQueueMetrics) Set(metrics QueueMetrics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = metrics
}

func (s *atomicQueueMetrics) AuditQueueMetrics() QueueMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metrics
}
