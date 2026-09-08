package tablemonitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// D-102 (BUG-0175 wave 4 W4-E): the two env window knobs and the startup
// prewarm port of table-monitor.repository.ts.

func TestNewOverviewCacheWithLookupWindowKnobs(t *testing.T) {
	fixed := map[string]string{
		"JUHE_AI_TABLE_MONITOR_OVERVIEW_FRESH_MS": "30000",
		"JUHE_AI_TABLE_MONITOR_OVERVIEW_STALE_MS": "90000",
	}
	cache := NewOverviewCacheWithLookup(func(name string) string { return fixed[name] })
	if cache.fresh != 30*time.Second {
		t.Fatalf("fresh = %v, want 30s", cache.fresh)
	}
	// Node Math.min(maxStaleMs, env): 90s < 60min so the env value wins.
	if cache.stale != 90*time.Second {
		t.Fatalf("stale = %v, want 1m30s", cache.stale)
	}
	if cache.failureBackoff != 30*time.Second {
		t.Fatalf("failureBackoff = %v, want 30s", cache.failureBackoff)
	}
}

func TestNewOverviewCacheWithLookupStaleFloorsAtFresh(t *testing.T) {
	cache := NewOverviewCacheWithLookup(func(name string) string {
		if name == "JUHE_AI_TABLE_MONITOR_OVERVIEW_FRESH_MS" {
			return "120000"
		}
		// stale falls back to the 60min default but must not sink below fresh;
		// the fresh value itself clamps to the 60min ceiling, so stale == fresh.
		return ""
	})
	if cache.fresh != 2*time.Minute {
		t.Fatalf("fresh = %v, want 2m", cache.fresh)
	}
	if cache.stale < cache.fresh {
		t.Fatalf("stale %v must stay >= fresh", cache.stale)
	}
}

func TestNewOverviewCacheWithLookupInvalidValuesFallBack(t *testing.T) {
	cache := NewOverviewCacheWithLookup(func(name string) string {
		return map[string]string{
			"JUHE_AI_TABLE_MONITOR_OVERVIEW_FRESH_MS": "abc",
			"JUHE_AI_TABLE_MONITOR_OVERVIEW_STALE_MS": "-5",
		}[name]
	})
	if cache.fresh != 10*time.Minute {
		t.Fatalf("fresh = %v, want the 10m default", cache.fresh)
	}
	if cache.stale != time.Hour {
		t.Fatalf("stale = %v, want the 1h default", cache.stale)
	}
}

func TestNewOverviewCacheDefaults(t *testing.T) {
	cache := NewOverviewCacheWithLookup(func(string) string { return "" })
	if cache.fresh != 10*time.Minute || cache.stale != time.Hour || cache.failureBackoff != 30*time.Second {
		t.Fatalf("defaults drifted: fresh=%v stale=%v backoff=%v", cache.fresh, cache.stale, cache.failureBackoff)
	}
}

type prewarmStore struct {
	overview Overview
	err      error
	calls    int
}

func (s *prewarmStore) LoadOverview(ctx context.Context, page, pageSize int, keyword string) (Overview, error) {
	s.calls++
	if s.err != nil {
		return Overview{}, s.err
	}
	return s.overview, nil
}
func (s *prewarmStore) LoadTableHistory(context.Context, string, string, string, string, int) ([]TableHistoryPoint, error) {
	return nil, nil
}
func (s *prewarmStore) LoadDatabaseHistory(context.Context, string, string, int) ([]DatabaseHistoryPoint, error) {
	return nil, nil
}

func TestPrewarmPopulatesCache(t *testing.T) {
	sampledAt := "2026-09-06T00:00:00.000Z"
	store := &prewarmStore{overview: Overview{SampledAt: &sampledAt}}
	deps := &Deps{Store: store, Cache: NewOverviewCache()}
	deps.Prewarm(context.Background())
	if store.calls != 1 {
		t.Fatalf("prewarm loads = %d, want 1", store.calls)
	}
	deps.Cache.mu.Lock()
	cached := deps.Cache.value != nil
	deps.Cache.mu.Unlock()
	if !cached {
		t.Fatal("prewarm must seed the cache value")
	}
}

func TestPrewarmFailureLeavesNoBackoffEntry(t *testing.T) {
	store := &prewarmStore{err: errors.New("schema unavailable")}
	deps := &Deps{Store: store, Cache: NewOverviewCache()}
	deps.Prewarm(context.Background())
	deps.Cache.mu.Lock()
	failed := !deps.Cache.refreshFailed.IsZero()
	value := deps.Cache.value
	deps.Cache.mu.Unlock()
	if failed || value != nil {
		// Node rememberFailure:false: a failed prewarm leaves the cache entry
		// absent so the first real request retries immediately.
		t.Fatalf("failed prewarm must not poison the cache (failed=%v value=%v)", failed, value)
	}
}

func TestPrewarmNilCacheIsNoop(t *testing.T) {
	deps := &Deps{}
	deps.Prewarm(context.Background()) // must not panic
}
