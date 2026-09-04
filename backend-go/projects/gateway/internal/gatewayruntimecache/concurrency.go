package gatewayruntimecache

import (
	"context"
)

// ---------------------------------------------------------------------------
// generations, singleflight primitives and the background-work await seam
// ---------------------------------------------------------------------------

// refreshCall is the join handle for one background refresh so tests (and
// Close) can await quiescence without changing the fire-and-forget semantics.
type refreshCall struct {
	done chan struct{}
}

func newRefreshCall() *refreshCall { return &refreshCall{done: make(chan struct{})} }

func (c *refreshCall) finish() { close(c.done) }

func (c *refreshCall) wait(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) currentRuntimeGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeGeneration
}

func (s *Service) isRuntimeGenerationCurrent(generation int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeGeneration == generation
}

func (s *Service) currentAPIKeyRuntimeGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiKeyRuntimeGeneration
}

func (s *Service) isAPIKeyRuntimeGenerationCurrent(generation int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiKeyRuntimeGeneration == generation
}

func (s *Service) currentCatalogGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalogGeneration
}

func (s *Service) isCatalogGenerationCurrent(generation int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalogGeneration == generation
}

// AwaitBackgroundWork waits for every in-flight background refresh, runtime
// load and catalog load. It is a determinism seam for tests and graceful
// shutdown; production callers use the fire-and-forget semantics unchanged.
func (s *Service) AwaitBackgroundWork(ctx context.Context) error {
	s.mu.Lock()
	calls := make([]*refreshCall, 0, 8)
	for _, call := range s.pendingGroupRefreshes {
		calls = append(calls, call)
	}
	for _, call := range s.pendingAccountRefreshes {
		calls = append(calls, call)
	}
	for _, call := range s.pendingInspectRefreshes {
		calls = append(calls, call)
	}
	loads := make([]*runtimeLoad, 0, 4)
	for _, load := range s.pendingRuntimeLoads {
		loads = append(loads, load)
	}
	catalogs := make([]*catalogLoad, 0, 4)
	for _, load := range s.pendingCatalogLoads {
		catalogs = append(catalogs, load)
	}
	s.mu.Unlock()

	for _, call := range calls {
		if err := call.wait(ctx); err != nil {
			return err
		}
	}
	for _, load := range loads {
		select {
		case <-load.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, load := range catalogs {
		select {
		case <-load.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// pendingRuntimeLoadCount reports the singleflight load registrations.
func (s *Service) pendingRuntimeLoadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pendingRuntimeLoads)
}

// runtimeCacheSize exposes the runtime entry count for tests.
func (s *Service) runtimeCacheSize() int { return s.runtimeCache.size() }

// settingsCacheLoaded exposes the single-slot settings state for tests.
func (s *Service) settingsCacheLoaded() bool {
	_, ok := s.settingsCache.get("current")
	return ok
}
