// external.go owns the M16b domain: the /external-integration-sources admin
// route family ported from backend/src/modules/external-integrations
// /external-integration-sources.routes.ts plus the
// storage/external-integration-source*.ts repositories. It covers the paged
// source list, the token-aware detail, the static scope options and public
// API catalog, guarded source/token mutations with optimistic locking and
// built-in test-token guards, the repeatable token secret reveal (Node
// findExternalIntegrationSourceTokenSecretAsync keeps the sealed secret; 404
// only when the token is gone) and the built-in test token reset. Token
// material is hashed like Node
// (sha256 of "external-integration-source-token:<token>") and sealed with the
// storage crypto AES-GCM envelope (apikeys.EncryptJSON/DecryptJSON, same
// format as storage/crypto.ts encryptJson).
package policyreads

import (
	"database/sql"
	"time"
)

const (
	externalPrefix = "/__aisys__/api/external-integration-sources"

	externalDefaultPageSize = 20
	externalMaxPageSize     = 100

	builtInExternalTestSourceID = "extsrc_builtin_test"
	builtInExternalTestTokenID  = "exttok_builtin_test"

	externalConflictMessage = "外部来源配置已被其他操作更新，请刷新后重试"
)

// ExternalScopeOption mirrors one externalIntegrationScopeOptions entry.
type ExternalScopeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// externalIntegrationScopeOptions mirrors storage/
// external-integration-source-constants.ts externalIntegrationScopeOptions.
var externalIntegrationScopeOptions = []ExternalScopeOption{
	{Value: "juhe_ai_public:api_key_list:read", Label: "GET API Key 列表"},
	{Value: "juhe_ai_public:route_strategy_list:read", Label: "GET 路由策略列表"},
	{Value: "juhe_ai_public:group_list:read", Label: "GET 分组列表"},
	{Value: "juhe_ai_public:account_list:read", Label: "GET 账号列表"},
	{Value: "juhe_ai_public:api_key_add:write", Label: "POST API Key 新增"},
	{Value: "juhe_ai_public:api_key_update:write", Label: "POST API Key 修改"},
	{Value: "juhe_ai_public:api_key_delete:write", Label: "POST API Key 删除"},
	{Value: "juhe_ai_public:route_strategy_add:write", Label: "POST 路由策略新增"},
	{Value: "juhe_ai_public:route_strategy_update:write", Label: "POST 路由策略修改"},
	{Value: "juhe_ai_public:route_strategy_delete:write", Label: "POST 路由策略删除"},
	{Value: "juhe_ai_public:group_add:write", Label: "POST 分组新增"},
	{Value: "juhe_ai_public:group_update:write", Label: "POST 分组修改"},
	{Value: "juhe_ai_public:group_delete:write", Label: "POST 分组删除"},
	{Value: "juhe_ai_public:account_add:write", Label: "POST 账号新增"},
	{Value: "juhe_ai_public:account_update:write", Label: "POST 账号修改"},
	{Value: "juhe_ai_public:account_delete:write", Label: "POST 账号删除"},
}

// ExternalRateLimitRule mirrors ExternalIntegrationRateLimitRule.
type ExternalRateLimitRule struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

// ExternalPrimaryToken mirrors ExternalIntegrationSourcePrimaryTokenSummary.
type ExternalPrimaryToken struct {
	ID          string `json:"id"`
	TokenPrefix string `json:"tokenPrefix"`
	TokenSuffix string `json:"tokenSuffix"`
}

// ExternalSourceListItem mirrors ExternalIntegrationSourceListItem.
type ExternalSourceListItem struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Status       string                  `json:"status"`
	Scopes       []string                `json:"scopes"`
	RateLimits   []ExternalRateLimitRule `json:"rateLimits"`
	ExpiresAt    *string                 `json:"expiresAt,omitempty"`
	Notes        *string                 `json:"notes,omitempty"`
	LastUsedAt   *string                 `json:"lastUsedAt,omitempty"`
	UpdatedAt    string                  `json:"updatedAt"`
	PrimaryToken *ExternalPrimaryToken   `json:"primaryToken,omitempty"`
	IsBuiltIn    bool                    `json:"isBuiltIn"`
}

// ExternalSourceRecord mirrors ExternalIntegrationSourceRecord.
type ExternalSourceRecord struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Status     string                  `json:"status"`
	Scopes     []string                `json:"scopes"`
	RateLimits []ExternalRateLimitRule `json:"rateLimits"`
	ExpiresAt  *string                 `json:"expiresAt,omitempty"`
	Notes      *string                 `json:"notes,omitempty"`
	LastUsedAt *string                 `json:"lastUsedAt,omitempty"`
	CreatedAt  string                  `json:"createdAt"`
	UpdatedAt  string                  `json:"updatedAt"`
	IsBuiltIn  bool                    `json:"isBuiltIn"`
}

// ExternalTokenSummary mirrors ExternalIntegrationSourceTokenSummary.
type ExternalTokenSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TokenPrefix string   `json:"tokenPrefix"`
	TokenSuffix string   `json:"tokenSuffix"`
	Status      string   `json:"status"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   *string  `json:"expiresAt,omitempty"`
	LastUsedAt  *string  `json:"lastUsedAt,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	RevokedAt   *string  `json:"revokedAt,omitempty"`
	IsBuiltIn   bool     `json:"isBuiltIn"`
}

// ExternalSourceSummary mirrors ExternalIntegrationSourceSummary.
type ExternalSourceSummary struct {
	ExternalSourceRecord
	TokenCount       int                    `json:"tokenCount"`
	ActiveTokenCount int                    `json:"activeTokenCount"`
	Tokens           []ExternalTokenSummary `json:"tokens"`
}

// ExternalSourceListResult mirrors ExternalIntegrationSourceListResult.
type ExternalSourceListResult struct {
	Items          []ExternalSourceListItem `json:"items"`
	Page           int                      `json:"page"`
	PageSize       int                      `json:"pageSize"`
	PageUpperBound int                      `json:"pageUpperBound"`
	HasMore        bool                     `json:"hasMore"`
}

// CreatedExternalToken mirrors CreatedExternalIntegrationSourceToken.
type CreatedExternalToken struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Token       string   `json:"token"`
	TokenPrefix string   `json:"tokenPrefix"`
	TokenSuffix string   `json:"tokenSuffix"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   *string  `json:"expiresAt,omitempty"`
}

// ExternalPatchChange mirrors ExternalIntegrationSourcePatchChange /
// ExternalIntegrationSourceTokenPatchChange with decoded values.
type ExternalPatchChange struct {
	Field  string
	Before any
	After  any
}

// ExternalSourcePatchOutcome mirrors ExternalIntegrationSourcePatchOutcome.
type ExternalSourcePatchOutcome struct {
	Mutation   ExternalMutationResult
	SourceName string
	Changes    []ExternalPatchChange
}

// ExternalTokenPatchOutcome mirrors ExternalIntegrationSourceTokenPatchOutcome.
type ExternalTokenPatchOutcome struct {
	Mutation   ExternalMutationResult
	SourceName string
	TokenName  string
	Changes    []ExternalPatchChange
}

// ExternalMutationResult mirrors ExternalIntegrationSourceMutationResult.
type ExternalMutationResult struct {
	ID        string `json:"id"`
	UpdatedAt string `json:"updatedAt"`
}

// ExternalSourceDeleteReceipt mirrors ExternalIntegrationSourceDeleteReceipt.
type ExternalSourceDeleteReceipt struct {
	ID   string
	Name string
}

// ExternalStore is the dual-mode external_integration_sources persistence.
type ExternalStore struct {
	baseStore
	// CryptoSecret mirrors runtimeConfig.secret: the storage crypto key used
	// for token_secret_encrypted envelopes.
	CryptoSecret string
}

// NewExternalStore builds the external integration store.
func NewExternalStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator, cryptoSecret string) (*ExternalStore, error) {
	base, err := newBaseStore(db, postgres, now, newID, inval)
	if err != nil {
		return nil, err
	}
	return &ExternalStore{baseStore: base, CryptoSecret: cryptoSecret}, nil
}
