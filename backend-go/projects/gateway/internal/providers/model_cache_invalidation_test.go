// model_cache_invalidation_test.go pins the post-commit model catalog cache
// invalidation publisher against the Node trigger semantics
// (model-cache-sync-warning.ts + custom-provider-models.repository.ts +
// provider-model-catalog.repository.ts): every committed write publishes its
// Node reason on the gateway runtime topic before the response, failed or
// no-op writes stay silent, and repeated commits notify repeatedly with no
// dedup. The fake invalidator replaces the real *inval.Bus +
// gatewayruntimecache subscriber pair (Mock 优先).
package providers

import (
	"net/http"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// invalidationCall is one published (topic, reason) pair.
type invalidationCall struct {
	topic  string
	reason string
}

// fakeRuntimeInvalidator records published invalidations in order.
type fakeRuntimeInvalidator struct {
	mu    sync.Mutex
	calls []invalidationCall
}

func (f *fakeRuntimeInvalidator) Invalidate(topic, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, invalidationCall{topic: topic, reason: reason})
}

func (f *fakeRuntimeInvalidator) recorded() []invalidationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]invalidationCall{}, f.calls...)
}

// reasons returns the published reason sequence.
func (f *fakeRuntimeInvalidator) reasons() []string {
	calls := f.recorded()
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.reason)
	}
	return out
}

func (f *fakeRuntimeInvalidator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newInvalidationTestEnv mounts the write family and injects the fake
// invalidator post-Mount — the same Deps hook the captureSink tests use
// (handlers close over the Deps pointer).
func newInvalidationTestEnv(t *testing.T) (*testEnv, *fakeRuntimeInvalidator) {
	t.Helper()
	env := newTestEnv(t)
	recorder := &fakeRuntimeInvalidator{}
	env.providersDeps.Inval = recorder
	return env, recorder
}

// assertCalls fails when the recorded (topic, reason) sequence differs.
func assertCalls(t *testing.T, recorder *fakeRuntimeInvalidator, want ...invalidationCall) {
	t.Helper()
	got := recorder.recorded()
	if len(got) != len(want) {
		t.Fatalf("invalidation calls = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("invalidation calls[%d] = %v, want %v (full: %v)", index, got[index], want[index], got)
		}
	}
}

// TestProvidersModelCacheReasonsMatchSubscriber pins the publisher reason
// vocabulary onto the gatewayruntimecache subscriber table (the Node
// model-catalog-cache-policy.ts reasons): a published reason must actually
// clear the /v1 model catalog cache.
func TestProvidersModelCacheReasonsMatchSubscriber(t *testing.T) {
	for _, reason := range []string{modelCacheSavedReason, modelCacheDeletedReason, modelCacheConfigurationUpdatedReason} {
		if !gatewayruntimecache.ShouldInvalidateProviderModelCatalog(reason) {
			t.Fatalf("publisher reason %q must be in the subscriber invalidation table", reason)
		}
	}
	// The publisher rides the gateway runtime topic the cache service
	// subscribes to (handleRuntimeTopicInvalidation, gatewayruntimecache
	// service.go registers inval.TopicGatewayRuntime).
	if inval.TopicGatewayRuntime != "topic:gateway_runtime_cache" {
		t.Fatalf("gateway runtime topic drifted: %q", inval.TopicGatewayRuntime)
	}
}

// TestProvidersModelCacheInvalidationCreate covers POST /{code}/models: the
// committed upsert publishes exactly one custom_provider_model_saved on the
// gateway runtime topic, duplicate commits publish again (no dedup), and
// failed or unauthenticated creates publish nothing.
func TestProvidersModelCacheInvalidationCreate(t *testing.T) {
	env, recorder := newInvalidationTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	// Validation failures publish nothing.
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":" "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid create: %d %v", code, invalid)
	}
	code, unpriced := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"no-price"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unpriced create: %d %v", code, unpriced)
	}
	assertCalls(t, recorder)

	// The committed create publishes one saved reason.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"inv-model","inputUsdPer1M":1,"outputUsdPer1M":2}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})

	// A duplicate upsert is its own commit and notifies again (Node has no
	// dedup on the notify layer).
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"inv-model","inputUsdPer1M":3}`)
	if code != http.StatusCreated {
		t.Fatalf("duplicate create: %d %v", code, duplicate)
	}
	assertCalls(t, recorder,
		invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"},
		invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})
	if reasons := recorder.reasons(); len(reasons) != 2 || reasons[0] != reasons[1] {
		t.Fatalf("duplicate commit must repeat the save reason: %v", reasons)
	}

	// Unknown provider and anonymous callers stay silent.
	code, unknown := env.do(t, http.MethodPost, "/__aisys__/api/providers/nope/models", `{"model":"a","inputUsdPer1M":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown provider create: %d %v", code, unknown)
	}
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `{"model":"a","inputUsdPer1M":1}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d %v", code, anonymous)
	}
	assertCalls(t, recorder,
		invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"},
		invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})
}

// TestProvidersModelCacheInvalidationPatchCustom covers the custom PATCH
// fork: the updated outcome publishes custom_provider_model_saved, while
// conflict, no-op and not-found outcomes stay silent.
func TestProvidersModelCacheInvalidationPatchCustom(t *testing.T) {
	env, recorder := newInvalidationTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")

	// The committed patch publishes the save reason (Node reuses
	// custom_provider_model_saved on the patch path).
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-1")+`","outputUsdPer1M":4}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})

	// A stale optimistic-concurrency guard (409) commits nothing.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","outputUsdPer1M":5}`)
	if code != http.StatusConflict {
		t.Fatalf("conflict patch: %d %v", code, conflict)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})

	// Re-submitting the stored value collapses into no_op: 200, no notify.
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-1")+`","outputUsdPer1M":4}`)
	if code != http.StatusOK {
		t.Fatalf("no-op patch: %d %v", code, noop)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})

	// Unknown id and invisible owner stay silent.
	code, unknown := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_missing",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","outputUsdPer1M":9}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown patch: %d %v", code, unknown)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_saved"})
}

// TestProvidersModelCacheInvalidationPatchBuiltIn covers the built-in PATCH
// fork: the committed configuration update publishes
// provider_model_configuration_updated before the operation log, while the
// no-change fork, the 409 guard, the admin gate and schema failures stay
// silent; the default-health-check-model preference write is not a catalog
// commit and never publishes.
func TestProvidersModelCacheInvalidationPatchBuiltIn(t *testing.T) {
	env, recorder := newInvalidationTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// The visibility-only commit publishes the configuration reason.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","catalogVisible":false}`)
	if code != http.StatusOK {
		t.Fatalf("hide built-in: %d %v", code, patched)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "provider_model_configuration_updated"})

	// Re-submitting the stored value diffs to an empty patch: the handler
	// answers the unchanged record without a notify.
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","catalogVisible":false}`)
	if code != http.StatusOK {
		t.Fatalf("no-op built-in patch: %d %v", code, noop)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "provider_model_configuration_updated"})

	// Stale guard, non-admin gate and schema failure commit nothing.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","inputUsdPer1M":1}`)
	if code != http.StatusConflict {
		t.Fatalf("stale built-in patch: %d %v", code, conflict)
	}
	env.login(t, "user1", "user-pass", "user")
	code, forbidden := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","catalogVisible":true}`)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin built-in patch: %d %v", code, forbidden)
	}
	env.login(t, "root", "root-pass", "super_admin")
	code, schema := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cat-1",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-1")+`","notes":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("schema built-in patch: %d %v", code, schema)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "provider_model_configuration_updated"})

	// The default-health-check-model preference is not a model catalog
	// commit: Node attaches no invalidation to that route either.
	code, preference := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("preference save: %d %v", code, preference)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "provider_model_configuration_updated"})
}

// TestProvidersModelCacheInvalidationDelete covers DELETE: only a committed
// row delete publishes custom_provider_model_deleted; the binding guard,
// unknown ids and double deletes stay silent.
func TestProvidersModelCacheInvalidationDelete(t *testing.T) {
	env, recorder := newInvalidationTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// The AI-account binding guard (409) commits nothing.
	env.exec(t, `INSERT INTO accounts (id, system_account_id, name) VALUES ('acc-inv', 'sys-inv', 'A1')`)
	env.exec(t, `INSERT INTO account_supported_models (account_id, provider_code, model) VALUES ('acc-inv', 'gpt', 'gpt-4o')`)
	code, bound := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-2", "")
	if code != http.StatusConflict {
		t.Fatalf("bound delete: %d %v", code, bound)
	}
	code, unknown := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_nope", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown delete: %d %v", code, unknown)
	}
	assertCalls(t, recorder)

	// The owner delete publishes the deleted reason.
	env.login(t, "user1", "user-pass", "user")
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusOK {
		t.Fatalf("owner delete: %d %v", code, deleted)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_deleted"})

	// The double delete finds no row and stays silent.
	code, again := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d %v", code, again)
	}
	assertCalls(t, recorder, invalidationCall{topic: inval.TopicGatewayRuntime, reason: "custom_provider_model_deleted"})
}

// TestProvidersModelCacheInvalidationNilPort pins the nil-tolerant port: the
// pre-handover composition (no Inval) keeps every write path working.
func TestProvidersModelCacheInvalidationNilPort(t *testing.T) {
	env := newTestEnv(t)
	if env.providersDeps.Inval != nil {
		t.Fatalf("default test composition must leave the port nil")
	}
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "user1", "user-pass", "user")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"nil-port-model","inputUsdPer1M":1}`)
	if code != http.StatusCreated {
		t.Fatalf("nil port create: %d %v", code, created)
	}
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+dataMap(t, created)["id"].(string), "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("nil port delete: %d %v", code, deleted)
	}
}
