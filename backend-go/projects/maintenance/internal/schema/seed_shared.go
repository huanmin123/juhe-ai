// Shared helpers for the SQLite and PostgreSQL seed ports (seed-defaults.ts
// and postgres-seed-defaults.ts). Every helper mirrors the Node function named
// in its comment byte-for-byte at the value level: identical formats
// (pbkdf2$sha512 password envelope, v1 AES-256-GCM JSON envelope, sk-/juis_
// token shapes, Node JSON.stringify output, providerModelCatalogId slugs) so
// rows seeded by Go are consumable by the Node runtime and vice versa.
//
// Time is always injected through SeedOptions.Now so callers (and tests) can
// pin the seed clock; Node reads new Date() once per seedDefaults call and
// uses that single timestamp for every row.

package schema

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// seedDefaultRuntimeSecret mirrors defaultRuntimeSecret in Node
// src/config/runtime.ts: the seed encryption key falls back to the same dev
// default when JUHE_AI_SECRET is not configured.
const seedDefaultRuntimeSecret = "juhe-ai-dev-secret-change-me"

// Provider and external integration constants mirrored from Node
// src/domain/provider-protocol.ts and
// src/storage/external-integration-source-constants.ts.
const (
	gptVendorCode                    = "gpt"
	hybridProviderCode               = "hybrid"
	externalIntegrationTestTokenID   = "exttok_builtin_test"
	externalIntegrationTestTokenName = "内置测试 Token"
)

// seedEncryptJSONWithOptions encrypts one seed secret payload with the
// configured runtime secret (Node encryptJson over runtimeConfig.secret).
func seedEncryptJSONWithOptions(options SeedOptions, value any) (string, error) {
	return seedEncryptJSON(options.seedSecret(), value)
}

// seedNullableString maps a Node "... ?? null" string column.
func seedNullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// seedNullableInt64 maps a Node "... ?? null" integer column.
func seedNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// seedNullableFloat64 maps a Node "... ?? null" float column.
func seedNullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// seedBoolInt maps Node's boolean-to-SQLite-integer projection
// (condition ? 1 : 0).
func seedBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// seedKeyPrefix mirrors the Node key.slice(0, 8) display prefix.
func seedKeyPrefix(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

// seedKeySuffix mirrors the Node key.slice(-8) display suffix.
func seedKeySuffix(value string) string {
	if len(value) > 8 {
		return value[len(value)-8:]
	}
	return value
}

// SeedOptions carries the injected seed dependencies.
type SeedOptions struct {
	// Now is the seed clock (Node: new Date() at the top of seedDefaults /
	// seedPostgresDefaults). nil means time.Now.
	Now func() time.Time
	// Secret is the runtime secret (Node runtimeConfig.secret /
	// JUHE_AI_SECRET) used by encryptJson for API key secrets and the built-in
	// external integration token. Empty selects the Node dev default.
	Secret string
}

func (o SeedOptions) nowTime() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// seedSecret resolves the encryption secret the same way Node
// secretConfig('JUHE_AI_SECRET', defaultRuntimeSecret) does for dev runtimes.
func (o SeedOptions) seedSecret() string {
	if strings.TrimSpace(o.Secret) == "" {
		return seedDefaultRuntimeSecret
	}
	return o.Secret
}

// seedTimestamp formats one Node new Date().toISOString() value
// (millisecond precision, UTC "Z").
func seedTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// seedHashSecret mirrors hashSecret in Node src/storage/crypto.ts.
func seedHashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// seedCreateAPIKey mirrors createApiKey in Node src/storage/crypto.ts.
func seedCreateAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "sk-" + hex.EncodeToString(buf), nil
}

// seedCreateExternalIntegrationToken mirrors
// createExternalIntegrationSourceTokenValue in Node
// src/storage/external-integration-source-constants.ts.
func seedCreateExternalIntegrationToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate external integration token: %w", err)
	}
	return "juis_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// seedHashExternalIntegrationToken mirrors
// hashExternalIntegrationSourceTokenValue in Node
// src/storage/external-integration-source-constants.ts.
func seedHashExternalIntegrationToken(token string) string {
	return seedHashSecret("external-integration-source-token:" + token)
}

// seedEncryptJSON mirrors encryptJson in Node src/storage/crypto.ts: the v1
// AES-256-GCM envelope "v1:<iv base64url>:<tag base64url>:<ciphertext
// base64url>" over compact JSON with the sha256(secret) key.
func seedEncryptJSON(secret string, value any) (string, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", fmt.Errorf("seed encrypt json cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("seed encrypt json gcm: %w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("seed encrypt json iv: %w", err)
	}
	plainText, err := seedJSONStringify(value)
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plainText, nil)
	tag := sealed[len(sealed)-gcm.Overhead():]
	cipherText := sealed[:len(sealed)-gcm.Overhead()]
	return strings.Join([]string{
		"v1",
		base64.RawURLEncoding.EncodeToString(iv),
		base64.RawURLEncoding.EncodeToString(tag),
		base64.RawURLEncoding.EncodeToString(cipherText),
	}, ":"), nil
}

// seedJSONStringify mirrors JSON.stringify (compact, no spaces, no HTML
// escaping) for the objects the seeds encrypt ({"key":...} / {"token":...}).
func seedJSONStringify(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("seed encode json: %w", err)
	}
	return []byte(strings.TrimSuffix(builder.String(), "\n")), nil
}

// seedStringify is the string variant of seedJSONStringify for the fixed
// seed payloads (Node JSON.stringify of account types, capabilities and
// secrets).
func seedStringify(value any) string {
	encoded, err := seedJSONStringify(value)
	if err != nil {
		panic(fmt.Sprintf("marshal seed json: %v", err))
	}
	return string(encoded)
}

// providerModelCatalogID mirrors providerModelCatalogId in Node
// src/storage/provider-model-catalog-id.ts.
func providerModelCatalogID(providerCode, model string) string {
	slug := strings.ToLower(providerCode + "_" + model)
	var builder strings.Builder
	previousUnderscore := false
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			builder.WriteByte('_')
		}
		previousUnderscore = true
	}
	trimmed := strings.Trim(builder.String(), "_")
	if len(trimmed) > 72 {
		trimmed = trimmed[:72]
	}
	sum := sha256.Sum256([]byte(providerCode + "\x00" + model))
	return "provider_model_" + trimmed + "_" + hex.EncodeToString(sum[:])[:12]
}

// activeModelCatalogSeedRows mirrors the Node hasModelShutdown filter inside
// listProviderModelPricing: rows with shutdown_date <= the current UTC date
// are excluded from the seed. asOfUTCDate has the Node currentUtcDate shape
// (YYYY-MM-DD).
func activeModelCatalogSeedRows(asOfUTCDate string) []modelCatalogSeedRow {
	active := make([]modelCatalogSeedRow, 0, len(modelCatalogSeedRows))
	for _, row := range modelCatalogSeedRows {
		if row.ShutdownDate != nil && *row.ShutdownDate <= asOfUTCDate {
			continue
		}
		active = append(active, row)
	}
	return active
}

// defaultRouteStrategyIDForGroup mirrors defaultRouteStrategyIdForGroup.
func defaultRouteStrategyIDForGroup(groupID string) string {
	return seedReplacePrefix(groupID, "grp_", "route_strategy_")
}

// defaultRouteStrategyGroupBindingIDForGroup mirrors
// defaultRouteStrategyGroupBindingIdForGroup.
func defaultRouteStrategyGroupBindingIDForGroup(groupID string) string {
	return seedReplacePrefix(groupID, "grp_", "rsg_")
}

// defaultAPIKeyIDForRouteStrategy mirrors defaultApiKeyIdForRouteStrategy.
func defaultAPIKeyIDForRouteStrategy(routeStrategyID string) string {
	return seedReplacePrefix(routeStrategyID, "route_strategy_", "key_default_")
}

// defaultRouteStrategyNameForGroup mirrors defaultRouteStrategyNameForGroup
// (Node groupName.replace(/分组$/, '路由')).
func defaultRouteStrategyNameForGroup(groupName string) string {
	return seedReplaceSuffix(groupName, "分组", "路由")
}

// defaultAPIKeyNameForRouteStrategy mirrors defaultApiKeyNameForRouteStrategy
// (Node routeStrategyName.replace(/路由$/, 'API Key')).
func defaultAPIKeyNameForRouteStrategy(routeStrategyName string) string {
	return seedReplaceSuffix(routeStrategyName, "路由", "API Key")
}

// seedReplacePrefix mirrors the anchored Node prefix replace
// (value.replace(/^prefix/, replacement)).
func seedReplacePrefix(value, prefix, replacement string) string {
	if strings.HasPrefix(value, prefix) {
		return replacement + strings.TrimPrefix(value, prefix)
	}
	return value
}

// seedReplaceSuffix mirrors the anchored Node suffix replace
// (value.replace(/suffix$/, replacement)).
func seedReplaceSuffix(value, suffix, replacement string) string {
	if strings.HasSuffix(value, suffix) {
		return strings.TrimSuffix(value, suffix) + replacement
	}
	return value
}

// seedExecutor is the minimal SQL surface the seeds need; *sql.DB satisfies
// it and tests can capture statements instead of opening a server.
type seedExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// seedRowQuerier reads single rows (profile account-type repair).
type seedRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
