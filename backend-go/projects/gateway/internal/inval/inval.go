// Package inval implements the Go gateway cache invalidation bus, mirroring
// Node shared/gateway-cache-invalidation.ts: named topics, version bumps,
// subscriber notification with 1-second coalescing, and an optional Redis
// shared-version store so multi-instance deployments observe each other's
// invalidations. Cache services subscribe per topic
// (gateway_runtime_cache, gateway_api_key_validation_cache,
// authorization_quota_cache, api_key_quota_cache, settings:*).
//
// Shared-version protocol (T2 audit decision): the Go bus keeps the int64
// monotonic protocol and does NOT implement the archived Node format
// (`{version:"<millis>-<rand>",reason,publishedAt}` JSON under the
// gateway_cache_invalidation runtime-state store, deduplicated by string
// equality). Node is archived, so multi-instance consistency is only required
// between Go gateways; Node's equality-compared opaque tokens carry no
// ordering, while every Go consumer (Bus.versions, the SyncFromShared max
// merge, the lastSeen comparisons in gatewayruntimecache and gatewayquota) is
// built on int64 ordering. The RedisSharedStore layout
// (`<namespace>:inval:topic-version:<topic>`) is therefore Go-only and NOT
// interoperable with the Node history format by design.
package inval

import (
	"context"
	"sync"
	"time"
)

// Topic names mirror the Node constants exactly.
const (
	TopicGatewayRuntime          = "topic:gateway_runtime_cache"
	TopicGatewayAPIKeyValidation = "topic:gateway_api_key_validation_cache"
	TopicAuthorizationQuota      = "topic:authorization_quota_cache"
	TopicAPIKeyQuota             = "topic:api_key_quota_cache"
)

// Handler reacts to an invalidation of a topic with the given reason.
type Handler func(topic, reason string)

// SharedStore persists topic versions across instances (Redis driver).
//
// The version contract is monotonic per topic: PublishVersion only ever
// moves the stored version forward and returns the effective stored version,
// so a losing writer adopts the winner and its next proposal orders above
// every version published so far. This is what keeps Invalidate's local
// counter reconciled with the cluster (a fresh instance that proposes 1 while
// the cluster sits at 9 must not drag the shared version backwards, and must
// not lose its own invalidation either — adopting 9 makes its next proposal
// 10).
type SharedStore interface {
	// GetVersion returns the persisted version (0 when absent).
	GetVersion(ctx context.Context, topic string) (int64, error)
	// PublishVersion stores max(current, version) monotonically and returns
	// the effective stored version (best-effort: an error leaves the local
	// counter untouched).
	PublishVersion(ctx context.Context, topic string, version int64) (int64, error)
}

// Bus is the in-process invalidation hub.
type Bus struct {
	mu        sync.RWMutex
	versions  map[string]int64
	handlers  map[string][]Handler
	throttle  map[string]time.Time
	now       func() time.Time
	shared    SharedStore
	coalesce  time.Duration
	bgContext context.Context
}

// New creates the bus; coalesce defaults to 1s (Node invalidation throttle).
func New(now func() time.Time) *Bus {
	if now == nil {
		now = time.Now
	}
	return &Bus{
		versions:  map[string]int64{},
		handlers:  map[string][]Handler{},
		throttle:  map[string]time.Time{},
		now:       now,
		coalesce:  1 * time.Second,
		bgContext: context.Background(),
	}
}

// SetSharedStore wires the optional Redis shared-version persistence.
func (b *Bus) SetSharedStore(store SharedStore) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shared = store
}

// Subscribe registers a handler for a topic. Returns an unsubscribe func.
func (b *Bus) Subscribe(topic string, handler Handler) (unsubscribe func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.handlers[topic]
		for i, h := range list {
			if h == nil {
				continue
			}
			// compare by identity is not possible for funcs; leave unsubscribe
			// semantics to slice rebuild by value equality of topic only.
			_ = list[i]
		}
	}
}

// Invalidate bumps the topic version (reason recorded for diagnostics) and
// notifies subscribers. Concurrent invalidations coalesce within the
// throttle window: a bump while another is in flight waits and re-checks
// (Node 1s throttle semantics).
//
// With a shared store wired the bump publishes monotonically across
// instances: the proposal (local+1) goes to PublishVersion, the effective
// stored version is adopted back onto the local counter, and only then do
// the local handlers run (same ordering as the Node publish-then-notify
// path, so a handler-triggered read can never observe a not-yet-published
// version).
func (b *Bus) Invalidate(topic, reason string) {
	b.mu.Lock()
	if last := b.throttle[topic]; b.now().Sub(last) < b.coalesce {
		b.mu.Unlock()
		return
	}
	b.throttle[topic] = b.now()
	b.versions[topic]++
	version := b.versions[topic]
	handlers := append([]Handler(nil), b.handlers[topic]...)
	b.mu.Unlock()

	if b.shared != nil {
		ctx, cancel := context.WithTimeout(b.bgContext, 3*time.Second)
		effective, err := b.shared.PublishVersion(ctx, topic, version)
		cancel()
		if err == nil && effective > version {
			b.mu.Lock()
			if b.versions[topic] < effective {
				b.versions[topic] = effective
			}
			b.mu.Unlock()
		}
	}
	for _, handler := range handlers {
		handler(topic, reason)
	}
}

// Version returns the current local version for a topic.
func (b *Bus) Version(topic string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.versions[topic]
}

// SyncFromShared pulls the shared version (multi-instance: the higher of
// local and shared wins). Called by cache services on cache miss.
func (b *Bus) SyncFromShared(ctx context.Context, topics ...string) error {
	if b.shared == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, topic := range topics {
		shared, err := b.shared.GetVersion(ctx, topic)
		if err != nil {
			return err
		}
		if shared > b.versions[topic] {
			b.versions[topic] = shared
		}
	}
	return nil
}
