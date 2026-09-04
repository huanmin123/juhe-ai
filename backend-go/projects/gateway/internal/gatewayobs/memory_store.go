package gatewayobs

import (
	"context"
	"errors"
	"sync"
)

// 内存路由观测 store，逐行为对齐
// backend/src/modules/gateway/observability/routing-observability-memory-store.ts。
// Node 依赖单线程执行序；Go 用 sync.Mutex 保证同样的先校验后落账原子性。
type MemoryGatewayRoutingObservabilityStore struct {
	mu             sync.Mutex
	counters       map[string]int64
	counterOrder   []string
	recordedEvents int64
	updatedAtMs    int64
}

// NewMemoryGatewayRoutingObservabilityStore mirrors the constructor.
func NewMemoryGatewayRoutingObservabilityStore() *MemoryGatewayRoutingObservabilityStore {
	return &MemoryGatewayRoutingObservabilityStore{counters: make(map[string]int64)}
}

// Record mirrors record.
func (store *MemoryGatewayRoutingObservabilityStore) Record(ctx context.Context, observation Observation, nowMs int64) error {
	return store.RecordBatch(ctx, []BatchEntry{{Observation: observation, Count: 1}}, nowMs)
}

// RecordBatch mirrors recordBatch: validation happens before any mutation, so
// a rejected batch leaves no partial counters.
func (store *MemoryGatewayRoutingObservabilityStore) RecordBatch(ctx context.Context, entries []BatchEntry, nowMs int64) error {
	if len(entries) == 0 {
		return nil
	}
	now, err := normalizedNow(nowMs)
	if err != nil {
		return err
	}
	order := make([]string, 0, len(entries))
	increments := make(map[string]int64, len(entries))
	for _, entry := range entries {
		count, err := positiveCount(entry.Count)
		if err != nil {
			return err
		}
		key := GatewayRoutingObservationMetricKey(entry.Observation)
		if _, seen := increments[key]; !seen {
			order = append(order, key)
		}
		increments[key] = saturatedAdd(increments[key], count)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	newMetricCount := 0
	for _, key := range order {
		if _, exists := store.counters[key]; !exists {
			newMetricCount += 1
		}
	}
	if len(store.counters)+newMetricCount > GatewayRoutingObservabilityMetricCapacity {
		return errors.New("routing observability metric capacity exhausted")
	}
	recordedIncrement := int64(0)
	for _, key := range order {
		store.counters[key] = saturatedAdd(store.counters[key], increments[key])
		recordedIncrement = saturatedAdd(recordedIncrement, increments[key])
	}
	store.recordedEvents = saturatedAdd(store.recordedEvents, recordedIncrement)
	if now > store.updatedAtMs {
		store.updatedAtMs = now
	}
	return nil
}

// Snapshot mirrors snapshot; counters serialize sorted by key like the Node
// localeCompare sort (byte order is equivalent for the fixed metric-key
// alphabet [a-z0-9._-], and encoding/json sorts map keys anyway).
func (store *MemoryGatewayRoutingObservabilityStore) Snapshot(ctx context.Context) (Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	counters := make(map[string]int64, len(store.counters))
	for key, value := range store.counters {
		counters[key] = value
	}
	return Snapshot{
		Version:        1,
		RecordedEvents: store.recordedEvents,
		UpdatedAtMs:    store.updatedAtMs,
		Counters:       counters,
	}, nil
}
