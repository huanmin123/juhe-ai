package gatewaysession

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLineageService(capacity int, ttl time.Duration, now func() time.Time) *ContentFingerprintLineageService {
	return NewContentFingerprintLineageService(ContentFingerprintLineageConfig{Capacity: capacity, TTL: ttl, Now: now})
}

func TestContentFingerprintLineageRecordArms(t *testing.T) {
	base := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	now := base
	clock := func() time.Time { return now }

	t.Run("first seen produces", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		if !service.Record("c1", 3) {
			t.Fatal("first seen must produce")
		}
	})
	t.Run("growth produces and sets everGrown", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		if !service.Record("c1", 3) {
			t.Fatal("first seen must produce")
		}
		if !service.Record("c1", 5) {
			t.Fatal("growth must produce")
		}
		// everGrown 已置位：已增长谱系的同长重放（轮内重试/并发同轮）照常产出。
		if !service.Record("c1", 5) {
			t.Fatal("equal length after growth must produce")
		}
	})
	t.Run("shrink produces", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		service.Record("c1", 5)
		if !service.Record("c1", 3) {
			t.Fatal("shrink must produce (lineage evolution evidence)")
		}
	})
	t.Run("equal length without growth intercepts and keeps record", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		service.Record("c1", 3)
		if service.Record("c1", 3) {
			t.Fatal("constant length replay must not produce")
		}
		// 拦截不更新记录：与记录等长的消息数再次出现仍拦截。
		if service.Record("c1", 3) {
			t.Fatal("second constant length replay must not produce")
		}
	})
	t.Run("shrink then equal length intercepts", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		service.Record("c1", 4)
		if !service.Record("c1", 2) {
			t.Fatal("shrink must produce")
		}
		if service.Record("c1", 2) {
			t.Fatal("equal length after shrink without growth must not produce")
		}
	})
	t.Run("growth after intercept still produces", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		service.Record("c1", 3)
		service.Record("c1", 3) // 拦截
		if !service.Record("c1", 4) {
			t.Fatal("real growth must produce")
		}
	})
	t.Run("different keys are independent", func(t *testing.T) {
		service := newTestLineageService(0, 0, clock)
		service.Record("c1", 3)
		if !service.Record("c2", 3) {
			t.Fatal("different confirmed hex is first seen")
		}
	})
	t.Run("ttl expiry is first seen again", func(t *testing.T) {
		service := newTestLineageService(0, time.Hour, clock)
		service.Record("c1", 3)
		service.Record("c1", 3) // 拦截
		now = base.Add(2 * time.Hour)
		if !service.Record("c1", 3) {
			t.Fatal("expired record must be first seen again")
		}
		now = base
	})
	t.Run("eviction frees capacity and re-seeing produces", func(t *testing.T) {
		service := newTestLineageService(2, 0, clock)
		service.Record("c1", 1)
		now = base.Add(time.Second)
		service.Record("c2", 1) // c2 lastSeen 更新
		now = base.Add(2 * time.Second)
		if !service.Record("c3", 1) {
			t.Fatal("capacity insert must produce")
		}
		if len(service.entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(service.entries))
		}
		if _, ok := service.entries["c1"]; ok {
			t.Fatal("oldest entry must be evicted")
		}
		// 逐出后再次出现 → 再次首见、再次至多一次。
		if !service.Record("c1", 1) {
			t.Fatal("re-seen after eviction must produce")
		}
		if service.Record("c1", 1) {
			t.Fatal("re-seen constant replay must not produce twice")
		}
	})
}

// TestContentFingerprintLineageConcurrentSingleProduce 并发同键首请求串行化
// 后恰一个产出（原子性）。
func TestContentFingerprintLineageConcurrentSingleProduce(t *testing.T) {
	service := newTestLineageService(0, 0, nil)
	const goroutines = 32
	var produced atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if service.Record("same-key", 7) {
				produced.Add(1)
			}
		}()
	}
	wg.Wait()
	if produced.Load() != 1 {
		t.Fatalf("produced = %d, want exactly 1", produced.Load())
	}
	if len(service.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(service.entries))
	}
}

// TestContentFingerprintLineageDefaults 零值配置取默认容量与 TTL。
func TestContentFingerprintLineageDefaults(t *testing.T) {
	service := NewContentFingerprintLineageService(ContentFingerprintLineageConfig{})
	if service.capacity != DefaultContentFingerprintLineageCapacity {
		t.Errorf("capacity = %d, want %d", service.capacity, DefaultContentFingerprintLineageCapacity)
	}
	if service.ttl != DefaultContentFingerprintLineageTTL {
		t.Errorf("ttl = %v, want %v", service.ttl, DefaultContentFingerprintLineageTTL)
	}
}
