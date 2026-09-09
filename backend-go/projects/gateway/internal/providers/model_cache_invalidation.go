// model_cache_invalidation.go carries the post-commit model catalog cache
// invalidation publisher, porting Node
// storage/model-cache-sync-warning.ts notifyCommittedModelCacheInvalidationAsync:
// every committed model write (custom model save, custom model delete,
// built-in model configuration update) publishes its Node reason onto the
// gateway runtime cache topic after the write commit succeeded, so the /v1
// model catalog cache (gatewayruntimecache, TTL 24h) drops its stale entries
// immediately instead of aging out.
package providers

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// Model catalog invalidation reasons mirror the Node publisher call sites
// verbatim (custom-provider-models.repository.ts,
// provider-model-catalog.repository.ts); the gatewayruntimecache subscriber
// maps them onto the model catalog clear through the
// model-catalog-cache-policy.ts reason table.
const (
	// modelCacheSavedReason publishes after a committed custom model upsert
	// or patch (Node 'custom_provider_model_saved'; the patch fork reuses the
	// save reason exactly like patchCustomProviderModelAsync).
	modelCacheSavedReason = "custom_provider_model_saved"
	// modelCacheDeletedReason publishes after a committed custom model delete
	// (Node 'custom_provider_model_deleted').
	modelCacheDeletedReason = "custom_provider_model_deleted"
	// modelCacheConfigurationUpdatedReason publishes after a committed
	// built-in model configuration patch (Node
	// 'provider_model_configuration_updated').
	modelCacheConfigurationUpdatedReason = "provider_model_configuration_updated"
)

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it;
// nil keeps the write family self-contained with no-op invalidation (the
// pre-handover Deps state the symbol audit recorded for providers).
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// notifyCommittedModelCacheInvalidation mirrors
// notifyCommittedModelCacheInvalidationAsync: call sites sit strictly on
// commit-success paths, before the response is rendered (the Node wrapper
// awaits the notify inside the repository function, before the route answer).
// Node logs model_cache_sync_failed_after_commit when the notify itself
// fails; the Go bus invalidation runs the local handlers synchronously and
// treats the shared-store publish as best-effort inside inval, so there is no
// failure arm to port. Every committed write notifies — the bus applies no
// dedup or drop window, matching the archived notify layer.
func (d *Deps) notifyCommittedModelCacheInvalidation(reason string) {
	if d.Inval == nil {
		return
	}
	d.Inval.Invalidate(inval.TopicGatewayRuntime, reason)
}
