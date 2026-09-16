package gatewayobs

// w11c: redis namespace helpers, command-client corners, store identity
// branches and observer construction defaults.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestW11CRedisNamespaceHelpers(t *testing.T) {
	// Empty inputs are rejected.
	if _, err := SanitizeRedisNamespacePart("  "); err == nil {
		t.Fatalf("blank namespace must fail")
	}
	if _, err := RedisNamespacedKey("ns", " "); err == nil {
		t.Fatalf("blank key must fail")
	}
	if _, err := RedisNamespacedKey("!!!", "key"); err == nil {
		t.Fatalf("unusable namespace must fail")
	}
	// Keys already carrying the namespace prefix are not double-prefixed.
	if key, err := RedisNamespacedKey("ns", "juhe-ai:ns:thing"); err != nil || key != "juhe-ai:ns:thing" {
		t.Fatalf("namespaced key = (%s, %v)", key, err)
	}
	// Keys carrying only the root prefix get the namespace inserted.
	if key, err := RedisNamespacedKey("ns", "juhe-ai:thing"); err != nil || key != "juhe-ai:ns:thing" {
		t.Fatalf("root key = (%s, %v)", key, err)
	}
	// Plain keys are fully prefixed.
	if key, err := RedisNamespacedKey("ns", "thing"); err != nil || key != "juhe-ai:ns:thing" {
		t.Fatalf("plain key = (%s, %v)", key, err)
	}
	// A different namespace prefix keeps the remaining key intact.
	if key, err := RedisNamespacedKey("ns2", "juhe-ai:ns:thing"); err != nil || key != "juhe-ai:ns2:ns:thing" {
		t.Fatalf("foreign prefix = (%s, %v)", key, err)
	}
	// Namespace prefix sanitization.
	if prefix, err := RedisNamespacePrefix("A B"); err != nil || prefix != "juhe-ai:A_B:" {
		t.Fatalf("prefix = (%s, %v)", prefix, err)
	}
}

func TestW11CRedisCommandClientAdapters(t *testing.T) {
	// The failed command client rejects every operation with its error.
	failure := errors.New("w11c redis down")
	failed := failedCommandClient{err: failure}
	if _, err := failed.Eval(context.Background(), "script", nil); !errors.Is(err, failure) {
		t.Fatalf("eval = %v", err)
	}
	if _, err := failed.SendCommand(context.Background(), "PING"); !errors.Is(err, failure) {
		t.Fatalf("send = %v", err)
	}
	// GetRedisClient rejects blank URLs.
	if _, err := GetRedisClient(context.Background(), "  "); err == nil {
		t.Fatalf("blank redis url must fail")
	}
	if _, err := GetRedisClient(context.Background(), "://bad"); err == nil {
		t.Fatalf("malformed redis url must fail")
	}
	// A store with a nil client surfaces the failed adapter lazily.
	store, err := NewRedisGatewayRoutingObservabilityStore(nil, "redis://w11c-unused", "ns", "w11c-name")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if store.client != nil {
		t.Fatalf("client should stay nil until first use")
	}
	if _, err := store.Snapshot(context.Background()); err == nil {
		t.Fatalf("snapshot without a reachable redis must fail")
	}
	// Recording without a reachable client fails too.
	if err := store.RecordBatch(context.Background(), []BatchEntry{{Observation: Observation{Kind: "circuit_dispatch", Outcome: "blocked"}, Count: 1}}, 1); err == nil {
		t.Fatalf("record without a reachable redis must fail")
	}
}

func TestW11CStoreConstructorValidation(t *testing.T) {
	// Empty redis URL.
	if _, err := NewRedisGatewayRoutingObservabilityStore(nil, "  ", "ns", "name"); err == nil {
		t.Fatalf("blank url must fail")
	}
	// Invalid store names.
	if _, err := NewRedisGatewayRoutingObservabilityStore(nil, "redis://x", "ns", strings.Repeat("n", 200)); err == nil {
		t.Fatalf("oversized name must fail")
	}
	if _, err := NewRedisGatewayRoutingObservabilityStore(nil, "redis://x", "ns", "bad name!"); err == nil {
		t.Fatalf("unsafe name must fail")
	}
	// The default name applies when blank.
	store, err := NewRedisGatewayRoutingObservabilityStore(nil, "redis://x", "ns", "")
	if err != nil {
		t.Fatalf("default name store: %v", err)
	}
	if !strings.Contains(store.key, "gateway-routing-observability") {
		t.Fatalf("default name key = %s", store.key)
	}
}

func TestW11CRoutingStoreIdentityBranches(t *testing.T) {
	// Standalone identity always maps to memory.
	if identity, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "redis"}); err != nil || identity != "standalone:memory" {
		t.Fatalf("standalone identity = (%s, %v)", identity, err)
	}
	if _, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"}); err != nil {
		t.Fatalf("standalone identity = %v", err)
	}
	// Unknown modes are rejected.
	if _, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "bogus", RuntimeStateDriver: "memory"}); err == nil {
		t.Fatalf("unknown mode must fail")
	}
	// Performance mode requires Redis.
	if _, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"}); err == nil {
		t.Fatalf("performance+memory must fail")
	}
	if _, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis", RedisStateURL: " "}); err == nil {
		t.Fatalf("performance without url must fail")
	}
	// Store construction follows the identity branches.
	if _, err := buildRoutingObservabilityStore(context.Background(), RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "redis"}); err == nil {
		t.Fatalf("standalone redis store must fail")
	}
	if _, err := buildRoutingObservabilityStore(context.Background(), RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis", RedisStateURL: ""}); err == nil {
		t.Fatalf("performance without url store must fail")
	}
	if _, err := buildRoutingObservabilityStore(context.Background(), RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"}); err == nil {
		t.Fatalf("performance memory store must fail")
	}
	store, err := buildRoutingObservabilityStore(context.Background(), RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"})
	if err != nil || store == nil {
		t.Fatalf("standalone store = (%v, %v)", store, err)
	}
}

func TestW11CNopLoggerAndObserverDefaults(t *testing.T) {
	// The nop logger accepts everything.
	logger := NopLogger()
	logger.Info(map[string]interface{}{"k": "v"}, "info")
	logger.Warn(map[string]interface{}{"k": "v"}, "warn")
	logger.Debug(map[string]interface{}{"k": "v"}, "debug")
	// A fresh observer with a memory store works end to end.
	observer := NewObserver(ObserverOptions{Store: NewMemoryGatewayRoutingObservabilityStore()})
	if observer == nil {
		t.Fatalf("observer missing")
	}
	// The upstream protocol getter returns the configured protocol.
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{Protocol: UpstreamResponseModelProtocolOpenAI})
	if observation.Protocol() != UpstreamResponseModelProtocolOpenAI {
		t.Fatalf("protocol getter misbehaves")
	}
}
