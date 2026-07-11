package gatewaycache

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	redisRootPrefix = "juhe-ai:"

	APIKeyValidationCacheName = "gateway:api-key-validation"
	GlobalSettingsCacheName   = "settings:global"
	SystemSettingsCacheName   = "settings:system"
	GroupLookupCacheName      = "lookup:group"
	GroupAccountIDsCacheName  = "lookup:group-account-ids"
	APIKeyLookupCacheName     = "lookup:api-key"

	RuntimeInvalidationStoreName = "gateway_cache_invalidation"
	GatewayRuntimeCacheTopic     = "gateway_runtime_cache"
	AuthorizationQuotaCacheTopic = "authorization_quota_cache"
	APIKeyQuotaCacheTopic        = "api_key_quota_cache"

	SystemAccountStatusChangedReason          = "system_account_status_changed"
	SystemAccountImageGenerationChangedReason = "system_account_image_generation_changed"
	TeamAuthorizationChangedReason            = "team_authorization_changed"
	CustomProviderModelSavedReason            = "custom_provider_model_saved"
	CustomProviderModelDeletedReason          = "custom_provider_model_deleted"
	ProxyCreatedReason                        = "proxy_created"
	ProxyUpdatedReason                        = "proxy_updated"
	ProxyDeletedReason                        = "proxy_deleted"

	SharedCacheVersionTTL = 30 * 24 * time.Hour
	RuntimeStateTTL       = 24 * time.Hour
)

var (
	invalidNamespacePartChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)
	invalidRedisKeyPartChars  = regexp.MustCompile(`[^a-zA-Z0-9:_-]`)
)

type RawSetter interface {
	SetRaw(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type VersionGenerator func(now time.Time) (string, error)

type SystemAccountInvalidator struct {
	cache      RawSetter
	state      RawSetter
	namespace  string
	now        func() time.Time
	newVersion VersionGenerator
}

type SystemAccountInvalidatorOptions struct {
	Cache      RawSetter
	State      RawSetter
	Namespace  string
	Now        func() time.Time
	NewVersion VersionGenerator
}

type runtimeInvalidationState struct {
	Version     string `json:"version"`
	Reason      string `json:"reason"`
	APIKeyID    string `json:"apiKeyId,omitempty"`
	PublishedAt string `json:"publishedAt"`
}

func NewSystemAccountInvalidator(opts SystemAccountInvalidatorOptions) (*SystemAccountInvalidator, error) {
	if opts.State == nil {
		return nil, fmt.Errorf("gateway runtime state redis setter is required")
	}
	namespace, err := SanitizeNamespacePart(opts.Namespace)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newVersion := opts.NewVersion
	if newVersion == nil {
		newVersion = GenerateVersion
	}
	return &SystemAccountInvalidator{
		cache:      opts.Cache,
		state:      opts.State,
		namespace:  namespace,
		now:        now,
		newVersion: newVersion,
	}, nil
}

func (i *SystemAccountInvalidator) InvalidateSystemAccountStatusChanged(ctx context.Context, _ string) error {
	return i.invalidateGatewayRuntime(ctx, SystemAccountStatusChangedReason)
}

func (i *SystemAccountInvalidator) InvalidateSystemAccountImageGenerationChanged(ctx context.Context, _ string) error {
	return i.invalidateGatewayRuntime(ctx, SystemAccountImageGenerationChangedReason)
}

func (i *SystemAccountInvalidator) InvalidateAuthorizationChanged(ctx context.Context, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("gateway authorization invalidation reason is required")
	}
	if err := i.publishGatewayCacheInvalidation(ctx, GatewayRuntimeCacheTopic, reason, runtimeInvalidationFields{}); err != nil {
		return err
	}
	return i.publishGatewayCacheInvalidation(ctx, AuthorizationQuotaCacheTopic, reason, runtimeInvalidationFields{})
}

func (i *SystemAccountInvalidator) InvalidateAPIKeyQuotaChanged(ctx context.Context, apiKeyID string, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("gateway api key quota invalidation reason is required")
	}
	return i.publishGatewayCacheInvalidation(ctx, APIKeyQuotaCacheTopic, reason, runtimeInvalidationFields{
		APIKeyID: strings.TrimSpace(apiKeyID),
	})
}

func (i *SystemAccountInvalidator) InvalidateCustomProviderModelChanged(ctx context.Context, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("gateway custom provider model invalidation reason is required")
	}
	return i.publishGatewayCacheInvalidation(ctx, GatewayRuntimeCacheTopic, reason, runtimeInvalidationFields{})
}

func (i *SystemAccountInvalidator) InvalidateProxyChanged(ctx context.Context, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("gateway proxy invalidation reason is required")
	}
	return i.publishGatewayCacheInvalidation(ctx, GatewayRuntimeCacheTopic, reason, runtimeInvalidationFields{})
}

func (i *SystemAccountInvalidator) InvalidateAPIKeyValidationCache(ctx context.Context) error {
	return i.clearAPIKeyValidationCache(ctx)
}

func (i *SystemAccountInvalidator) InvalidateAPIKeyLookupCache(
	ctx context.Context,
	_ string,
	reason string,
) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("gateway api key lookup invalidation reason is required")
	}
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate api key lookup cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, APIKeyLookupCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear api key lookup shared cache: %w", err)
	}
	return nil
}

func (i *SystemAccountInvalidator) InvalidateGlobalSettingsCache(ctx context.Context) error {
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate global settings cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, GlobalSettingsCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear global settings shared cache: %w", err)
	}
	return nil
}

func (i *SystemAccountInvalidator) InvalidateSystemSettingsCache(ctx context.Context) error {
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate system settings cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, SystemSettingsCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear system settings shared cache: %w", err)
	}
	return nil
}

func (i *SystemAccountInvalidator) InvalidateGroupLookupCache(ctx context.Context) error {
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate group lookup cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, GroupLookupCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear group lookup shared cache: %w", err)
	}
	return nil
}

func (i *SystemAccountInvalidator) InvalidateGroupAccountIDsCache(ctx context.Context) error {
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate group account IDs cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, GroupAccountIDsCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear group account IDs shared cache: %w", err)
	}
	return nil
}

func (i *SystemAccountInvalidator) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("gateway runtime invalidation reason is required")
	}
	return i.publishGatewayCacheInvalidation(ctx, GatewayRuntimeCacheTopic, reason, runtimeInvalidationFields{})
}

func (i *SystemAccountInvalidator) invalidateGatewayRuntime(ctx context.Context, reason string) error {
	if err := i.InvalidateAPIKeyValidationCache(ctx); err != nil {
		return err
	}
	return i.InvalidateGatewayRuntime(ctx, reason)
}

func (i *SystemAccountInvalidator) clearAPIKeyValidationCache(ctx context.Context) error {
	if i.cache == nil {
		return fmt.Errorf("gateway cache redis setter is required")
	}
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate gateway api key validation cache version: %w", err)
	}
	key, err := SharedCacheVersionKey(i.namespace, APIKeyValidationCacheName)
	if err != nil {
		return err
	}
	if err := i.cache.SetRaw(ctx, key, []byte(version), SharedCacheVersionTTL); err != nil {
		return fmt.Errorf("clear gateway api key validation shared cache: %w", err)
	}
	return nil
}

type runtimeInvalidationFields struct {
	APIKeyID string
}

func (i *SystemAccountInvalidator) publishGatewayCacheInvalidation(ctx context.Context, topic string, reason string, fields runtimeInvalidationFields) error {
	now := i.now().UTC()
	version, err := i.newVersion(now)
	if err != nil {
		return fmt.Errorf("generate gateway cache invalidation version: %w", err)
	}
	key, err := RuntimeStateKey(i.namespace, RuntimeInvalidationStoreName, "topic:"+SanitizeRedisKeyPart(topic))
	if err != nil {
		return err
	}
	payload, err := json.Marshal(runtimeInvalidationState{
		Version:     version,
		Reason:      reason,
		APIKeyID:    strings.TrimSpace(fields.APIKeyID),
		PublishedAt: nodeISOString(now),
	})
	if err != nil {
		return fmt.Errorf("marshal gateway cache invalidation state: %w", err)
	}
	if err := i.state.SetRaw(ctx, key, payload, RuntimeStateTTL); err != nil {
		return fmt.Errorf("publish gateway cache invalidation: %w", err)
	}
	return nil
}

func SharedCacheVersionKey(namespace string, cacheName string) (string, error) {
	return RedisNamespacedKey(namespace, "juhe-ai:cache-version:"+SanitizeRedisKeyPart(cacheName))
}

func RuntimeStateKey(namespace string, storeName string, key string) (string, error) {
	safeStoreName := SanitizeRedisKeyPart(storeName)
	return RedisNamespacedKey(namespace, "juhe-ai:state:"+safeStoreName+":"+strings.TrimSpace(key))
}

func RedisNamespacedKey(namespace string, key string) (string, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", fmt.Errorf("redis key is required")
	}
	safeNamespace, err := SanitizeNamespacePart(namespace)
	if err != nil {
		return "", err
	}
	namespacePrefix := redisRootPrefix + safeNamespace + ":"
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized, nil
	}
	if strings.HasPrefix(normalized, redisRootPrefix) {
		return namespacePrefix + strings.TrimPrefix(normalized, redisRootPrefix), nil
	}
	return namespacePrefix + normalized, nil
}

func SanitizeNamespacePart(value string) (string, error) {
	normalized := invalidNamespacePartChars.ReplaceAllString(strings.TrimSpace(value), "_")
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "", fmt.Errorf("redis namespace is required")
	}
	return normalized, nil
}

func SanitizeRedisKeyPart(value string) string {
	normalized := invalidRedisKeyPartChars.ReplaceAllString(strings.TrimSpace(value), "_")
	if normalized == "" {
		return "default"
	}
	return normalized
}

func GenerateVersion(now time.Time) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%x", now.UTC().UnixMilli(), suffix[:]), nil
}

func nodeISOString(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
