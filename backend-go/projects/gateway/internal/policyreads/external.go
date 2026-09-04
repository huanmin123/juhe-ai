// external.go owns the M16b domain: the /external-integration-sources admin
// route family ported from backend/src/modules/external-integrations
// /external-integration-sources.routes.ts plus the
// storage/external-integration-source*.ts repositories. It covers the paged
// source list, the token-aware detail, the static scope options and public
// API catalog, guarded source/token mutations with optimistic locking and
// built-in test-token guards, the one-shot token secret reveal and the
// built-in test token reset. Token material is hashed like Node
// (sha256 of "external-integration-source-token:<token>") and sealed with the
// storage crypto AES-GCM envelope (apikeys.EncryptJSON/DecryptJSON, same
// format as storage/crypto.ts encryptJson).
package policyreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
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

// ---------------------------------------------------------------------------
// Scope normalizers (external-integration-source-normalizers.ts).
// ---------------------------------------------------------------------------

var externalRateLimitRuleKeys = []string{"windowSeconds", "maxRequests"}

func externalScopeSupported(value string) bool {
	for _, option := range externalIntegrationScopeOptions {
		if option.Value == value {
			return true
		}
	}
	return false
}

// normalizeExternalScopes mirrors normalizeScopes.
func normalizeExternalScopes(scopes any) ([]string, error) {
	items, ok := scopes.([]any)
	if !ok {
		if scopes == nil {
			return []string{}, nil
		}
		return nil, &ValidationError{Message: "来源系统 scopes 必须是字符串数组"}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		text, isString := item.(string)
		if !isString {
			return nil, &ValidationError{Message: "来源系统 scopes 必须是字符串数组"}
		}
		value := strings.TrimSpace(text)
		if value == "" {
			return nil, &ValidationError{Message: "来源系统 scopes 不能为空"}
		}
		if !externalScopeSupported(value) {
			return nil, &ValidationError{Message: "来源系统 scope 不受支持：" + value}
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return uniqueSortedStrings(out), nil
}

// decodeExternalScopes mirrors decodeScopes: unknown scopes on stored rows are
// dropped, the rest re-normalized.
func decodeExternalScopes(value string) ([]string, error) {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	if items, isArray := parsed.([]any); isArray {
		known := []any{}
		for _, item := range items {
			text, isString := item.(string)
			if !isString || externalScopeSupported(strings.TrimSpace(text)) {
				known = append(known, item)
			}
		}
		return normalizeExternalScopes(known)
	}
	return normalizeExternalScopes(parsed)
}

// normalizeExternalRateLimits mirrors normalizeRateLimits.
func normalizeExternalRateLimits(rules any) ([]ExternalRateLimitRule, error) {
	items, ok := rules.([]any)
	if !ok {
		if rules == nil {
			return []ExternalRateLimitRule{}, nil
		}
		return nil, &ValidationError{Message: "来源系统限频规则必须是数组"}
	}
	if len(items) > 8 {
		return nil, &ValidationError{Message: "来源系统限频规则最多 8 条"}
	}
	normalized := []ExternalRateLimitRule{}
	seen := map[int]bool{}
	for _, item := range items {
		record, isObject := item.(map[string]any)
		if !isObject {
			return nil, &ValidationError{Message: "来源系统限频规则必须是对象"}
		}
		unknown := []string{}
		for key := range record {
			if !containsString(externalRateLimitRuleKeys, key) {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			return nil, &ValidationError{Message: "来源系统限频规则包含未知字段：" + strings.Join(unknown, "、")}
		}
		windowSeconds, err := externalRateLimitInteger(record["windowSeconds"], 1, 86400, "来源系统限频窗口")
		if err != nil {
			return nil, err
		}
		maxRequests, err := externalRateLimitInteger(record["maxRequests"], 1, 100000, "来源系统限频次数")
		if err != nil {
			return nil, err
		}
		if seen[windowSeconds] {
			return nil, &ValidationError{Message: "来源系统限频窗口不能重复"}
		}
		seen[windowSeconds] = true
		normalized = append(normalized, ExternalRateLimitRule{WindowSeconds: windowSeconds, MaxRequests: maxRequests})
	}
	for i := 1; i < len(normalized); i++ {
		for j := i; j > 0 && normalized[j].WindowSeconds < normalized[j-1].WindowSeconds; j-- {
			normalized[j], normalized[j-1] = normalized[j-1], normalized[j]
		}
	}
	return normalized, nil
}

func externalRateLimitInteger(value any, min, max int, label string) (int, error) {
	number, isNumber := value.(float64)
	if !isNumber {
		return 0, &ValidationError{Message: label + "必须是整数"}
	}
	if number != float64(int64(number)) {
		return 0, &ValidationError{Message: label + "必须是整数"}
	}
	result := int(number)
	if result < min || result > max {
		return 0, &ValidationError{Message: label + "必须在 " + strconv.Itoa(min) + " 到 " + strconv.Itoa(max) + " 之间"}
	}
	return result, nil
}

func decodeExternalRateLimits(value string) ([]ExternalRateLimitRule, error) {
	if value == "" {
		return []ExternalRateLimitRule{}, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	return normalizeExternalRateLimits(parsed)
}

// normalizeExternalNullableISO mirrors normalizeNullableIso.
func normalizeExternalNullableISO(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, isString := value.(string)
	if !isString {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	canonical, ok := canonicalRFC3339Millis(trimmed)
	if !ok {
		return nil, &ValidationError{Message: "过期时间无效"}
	}
	return &canonical, nil
}

// normalizeExternalNullableText mirrors normalizeNullableText.
func normalizeExternalNullableText(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, isString := value.(string)
	if !isString {
		return nil, &ValidationError{Message: "备注必须是字符串"}
	}
	trimmed := strings.TrimSpace(text)
	if runeLen(trimmed) > 500 {
		return nil, &ValidationError{Message: "备注不能超过 500 个字符"}
	}
	if trimmed == "" {
		return nil, nil
	}
	return &trimmed, nil
}

func normalizeExternalName(value any, message string) (string, error) {
	text, isString := value.(string)
	if !isString {
		return "", &ValidationError{Message: message}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", &ValidationError{Message: message}
	}
	if runeLen(trimmed) > 80 {
		return "", &ValidationError{Message: "来源系统名称不能超过 80 个字符"}
	}
	return trimmed, nil
}

func normalizeSourceStatus(value string) (string, error) {
	if value == "active" || value == "disabled" {
		return value, nil
	}
	return "", &ValidationError{Message: "来源系统状态无效"}
}

func normalizeSourceStatusInput(status any) (string, error) {
	if status == nil {
		return "active", nil
	}
	text, isString := status.(string)
	if !isString {
		return "", &ValidationError{Message: "来源系统状态无效"}
	}
	return normalizeSourceStatus(text)
}

func normalizeTokenStatus(value string) (string, error) {
	if value == "active" || value == "disabled" || value == "revoked" {
		return value, nil
	}
	return "", &ValidationError{Message: "来源系统 token 状态无效"}
}

func normalizeTokenStatusInput(status any) (string, error) {
	if status == nil {
		return "active", nil
	}
	text, isString := status.(string)
	if !isString {
		return "", &ValidationError{Message: "来源系统 token 状态无效"}
	}
	return normalizeTokenStatus(text)
}

// hashExternalSourceToken mirrors hashExternalIntegrationSourceTokenValue.
func hashExternalSourceToken(token string) string {
	return apikeys.HashSecret("external-integration-source-token:" + token)
}

// createExternalSourceTokenValue mirrors createExternalIntegrationSourceTokenValue.
func createExternalSourceTokenValue() string {
	return "juis_" + randomBase64URLBytes(32)
}

// ---------------------------------------------------------------------------
// Row mapping.
// ---------------------------------------------------------------------------

type externalSourceRow struct {
	id         string
	name       string
	status     string
	scopesJSON string
	rateLimits sql.NullString
	expiresAt  sql.NullString
	notes      sql.NullString
	lastUsedAt sql.NullString
	createdAt  string
	updatedAt  string
}

const externalSourceColumns = `sources.id, sources.name, sources.status, sources.scopes_json,
	sources.rate_limits_json, sources.expires_at, sources.notes, sources.last_used_at,
	sources.created_at, sources.updated_at`

func scanExternalSourceRow(scan func(...any) error) (externalSourceRow, error) {
	var row externalSourceRow
	err := scan(&row.id, &row.name, &row.status, &row.scopesJSON, &row.rateLimits,
		&row.expiresAt, &row.notes, &row.lastUsedAt, &row.createdAt, &row.updatedAt)
	return row, err
}

func (s *ExternalStore) mapSourceRecord(ctx context.Context, row externalSourceRow) (*ExternalSourceRecord, error) {
	scopes, err := decodeExternalScopes(row.scopesJSON)
	if err != nil {
		return nil, err
	}
	rateLimits, err := decodeExternalRateLimits(row.rateLimits.String)
	if err != nil {
		return nil, err
	}
	return &ExternalSourceRecord{
		ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
		ExpiresAt: nullPtrString(row.expiresAt), Notes: nullPtrString(row.notes),
		LastUsedAt: nullPtrString(row.lastUsedAt), CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
		IsBuiltIn: row.id == builtInExternalTestSourceID,
	}, nil
}

func (s *ExternalStore) loadTokensBySourceIDs(ctx context.Context, q queryer, sourceIDs []string) (map[string][]ExternalTokenSummary, error) {
	result := map[string][]ExternalTokenSummary{}
	if len(sourceIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(sourceIDs))
	args := make([]any, 0, len(sourceIDs))
	for i, id := range sourceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, s.bind(`SELECT `+externalTokenColumns+` FROM `+s.table("external_integration_source_tokens")+`
		WHERE source_ref_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY created_at DESC, id DESC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		token, err := scanExternalTokenRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		result[token.sourceRefID] = append(result[token.sourceRefID], s.mapTokenSummary(token))
	}
	return result, rows.Err()
}

func (s *ExternalStore) mapTokenSummary(row externalTokenRow) ExternalTokenSummary {
	scopes, err := decodeExternalScopes(row.scopesJSON)
	if err != nil {
		scopes = []string{}
	}
	return ExternalTokenSummary{
		ID: row.id, Name: row.name, TokenPrefix: row.tokenPrefix, TokenSuffix: row.tokenSuffix,
		Status: row.status, Scopes: scopes, ExpiresAt: nullPtrString(row.expiresAt),
		LastUsedAt: nullPtrString(row.lastUsedAt), CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
		RevokedAt: nullPtrString(row.revokedAt), IsBuiltIn: row.id == builtInExternalTestTokenID,
	}
}

type externalTokenRow struct {
	id          string
	sourceRefID string
	name        string
	tokenPrefix string
	tokenSuffix string
	status      string
	scopesJSON  string
	expiresAt   sql.NullString
	lastUsedAt  sql.NullString
	createdAt   string
	updatedAt   string
	revokedAt   sql.NullString
}

func scanExternalTokenRow(scan func(...any) error) (externalTokenRow, error) {
	var row externalTokenRow
	err := scan(&row.id, &row.sourceRefID, &row.name, &row.tokenPrefix, &row.tokenSuffix, &row.status,
		&row.scopesJSON, &row.expiresAt, &row.lastUsedAt, &row.createdAt, &row.updatedAt, &row.revokedAt)
	return row, err
}

const externalTokenColumns = `id, source_ref_id, name, token_prefix, token_suffix, status,
	scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at`

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

// Scopes mirrors GET /scopes.
func (s *ExternalStore) Scopes() []ExternalScopeOption {
	out := make([]ExternalScopeOption, len(externalIntegrationScopeOptions))
	copy(out, externalIntegrationScopeOptions)
	return out
}

// ListPage mirrors listExternalIntegrationSourcesAsync.
func (s *ExternalStore) ListPage(ctx context.Context, page *int, pageSize *int, keyword, status string) (*ExternalSourceListResult, error) {
	ctx = ensureCtx(ctx)
	size := externalDefaultPageSize
	if pageSize != nil {
		size = *pageSize
		if size < 1 {
			size = 1
		}
		if size > externalMaxPageSize {
			size = externalMaxPageSize
		}
	}
	currentPage := 1
	if page != nil {
		currentPage = normalizeExternalListPage(*page, size)
	}
	offset := (currentPage - 1) * size
	clauses := []string{}
	args := []any{}
	if status != "" && status != "all" {
		clauses = append(clauses, "sources.status = ?")
		args = append(args, status)
	}
	trimmedKeyword := strings.TrimSpace(keyword)
	if trimmedKeyword != "" {
		if s.pg {
			clauses = append(clauses, "(LOWER(sources.name) = LOWER(?) OR LOWER(sources.name) LIKE LOWER(?) ESCAPE '\\')")
		} else {
			clauses = append(clauses, "(sources.name = ? OR sources.name LIKE ? ESCAPE '\\')")
		}
		pattern := escapeLikePrefix(trimmedKeyword) + "%"
		args = append(args, trimmedKeyword, pattern)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	queryArgs := append(args, size+1, offset)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+externalSourceColumns+`
		FROM `+s.table("external_integration_sources")+` AS sources
		`+where+`
		ORDER BY sources.updated_at DESC, sources.id DESC
		LIMIT ? OFFSET ?`), queryArgs...)
	if err != nil {
		return nil, err
	}
	pageRows := []externalSourceRow{}
	for rows.Next() {
		row, scanErr := scanExternalSourceRow(rows.Scan)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		pageRows = append(pageRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hasMore := len(pageRows) > size
	if hasMore {
		pageRows = pageRows[:size]
	}
	sourceIDs := make([]string, 0, len(pageRows))
	for _, row := range pageRows {
		sourceIDs = append(sourceIDs, row.id)
	}
	primaryTokens, err := s.loadPrimaryTokensBySourceIDs(ctx, sourceIDs)
	if err != nil {
		return nil, err
	}
	items := make([]ExternalSourceListItem, 0, len(pageRows))
	for _, row := range pageRows {
		scopes, scopesErr := decodeExternalScopes(row.scopesJSON)
		if scopesErr != nil {
			return nil, scopesErr
		}
		rateLimits, rateErr := decodeExternalRateLimits(row.rateLimits.String)
		if rateErr != nil {
			return nil, rateErr
		}
		item := ExternalSourceListItem{
			ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
			ExpiresAt: nullPtrString(row.expiresAt), Notes: nullPtrString(row.notes),
			LastUsedAt: nullPtrString(row.lastUsedAt), UpdatedAt: row.updatedAt,
			PrimaryToken: primaryTokens[row.id],
			IsBuiltIn:    row.id == builtInExternalTestSourceID,
		}
		items = append(items, item)
	}
	return &ExternalSourceListResult{
		Items:          items,
		Page:           currentPage,
		PageSize:       size,
		PageUpperBound: offset + len(items) + boolToInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

// normalizeExternalListPage mirrors normalizeListPage (default window 1001).
func normalizeExternalListPage(page, pageSize int) int {
	upperBound := (1000 - 1) / pageSize
	if upperBound < 1 {
		upperBound = 1
	}
	if page < 1 {
		return 1
	}
	if page > upperBound {
		return upperBound
	}
	return page
}

// loadPrimaryTokensBySourceIDs mirrors
// loadExternalIntegrationSourcePrimaryTokensBySourceIds.
func (s *ExternalStore) loadPrimaryTokensBySourceIDs(ctx context.Context, sourceIDs []string) (map[string]*ExternalPrimaryToken, error) {
	result := map[string]*ExternalPrimaryToken{}
	unique := uniqueSortedStrings(sourceIDs)
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, 0, len(unique))
	for i, id := range unique {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT
		id, source_ref_id, token_prefix, token_suffix
		FROM (
			SELECT
				tokens.id,
				tokens.source_ref_id,
				tokens.token_prefix,
				tokens.token_suffix,
				tokens.status,
				tokens.created_at,
				ROW_NUMBER() OVER (
					PARTITION BY tokens.source_ref_id
					ORDER BY CASE WHEN tokens.status = 'active' THEN 0 ELSE 1 END ASC, tokens.created_at DESC, tokens.id DESC
				) AS token_rank
			FROM `+s.table("external_integration_source_tokens")+` AS tokens
			WHERE tokens.source_ref_id IN (`+strings.Join(placeholders, ",")+`)
		) ranked_tokens
		WHERE token_rank = 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var token ExternalPrimaryToken
		var sourceRefID string
		if err := rows.Scan(&token.ID, &sourceRefID, &token.TokenPrefix, &token.TokenSuffix); err != nil {
			return nil, err
		}
		result[sourceRefID] = &token
	}
	return result, rows.Err()
}

// FindSource mirrors findExternalIntegrationSourceAsync.
func (s *ExternalStore) FindSource(ctx context.Context, id string) (*ExternalSourceSummary, error) {
	ctx = ensureCtx(ctx)
	row, err := scanExternalSourceRow(func(targets ...any) error {
		// Node selects `*, 0 AS token_count, 0 AS active_token_count`; the two
		// literal counts are recomputed from the token rows below, so they are
		// scanned into throwaway targets here.
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+externalSourceColumns+`,
			0 AS token_count, 0 AS active_token_count
			FROM `+s.table("external_integration_sources")+` AS sources WHERE sources.id = ?`), id).
			Scan(append(targets, new(any), new(any))...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := s.mapSourceRecord(ctx, row)
	if err != nil {
		return nil, err
	}
	tokensBySource, err := s.loadTokensBySourceIDs(ctx, s.db, []string{row.id})
	if err != nil {
		return nil, err
	}
	tokens := tokensBySource[row.id]
	if tokens == nil {
		tokens = []ExternalTokenSummary{}
	}
	activeCount := 0
	for _, token := range tokens {
		if token.Status == "active" {
			activeCount++
		}
	}
	return &ExternalSourceSummary{
		ExternalSourceRecord: *record,
		TokenCount:           len(tokens),
		ActiveTokenCount:     activeCount,
		Tokens:               tokens,
	}, nil
}

// FindTokenSecret mirrors findExternalIntegrationSourceTokenSecretAsync.
func (s *ExternalStore) FindTokenSecret(ctx context.Context, sourceRefID, tokenID string) (*string, error) {
	ctx = ensureCtx(ctx)
	var ciphertext sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT tokens.token_secret_encrypted
		FROM `+s.table("external_integration_source_tokens")+` AS tokens
		JOIN `+s.table("external_integration_sources")+` AS sources ON sources.id = tokens.source_ref_id
		WHERE sources.id = ? AND tokens.id = ?`), sourceRefID, tokenID).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ciphertext.Valid || ciphertext.String == "" {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := apikeys.DecryptJSON(s.CryptoSecret, ciphertext.String, &payload); err != nil {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	if payload.Token == "" {
		return nil, &ValidationError{Message: "来源系统 Token 密文缺少完整 Token"}
	}
	return &payload.Token, nil
}

// ---------------------------------------------------------------------------
// Writes.
// ---------------------------------------------------------------------------

// externalSourceInput is the normalized POST/PATCH payload subset shared by
// the source create/update paths.
type externalSourceInput struct {
	Name       any
	Status     any
	Scopes     any
	RateLimits any
	ExpiresAt  any
	Notes      any
}

// buildSourceCreateRow mirrors buildExternalIntegrationSourceCreateRow.
func buildSourceCreateRow(input externalSourceInput, now string, newID func(string) string) (*externalSourceRow, []ExternalRateLimitRule, []string, error) {
	name, err := normalizeExternalName(input.Name, "来源系统名称不能为空")
	if err != nil {
		return nil, nil, nil, err
	}
	status, err := normalizeSourceStatusInput(input.Status)
	if err != nil {
		return nil, nil, nil, err
	}
	scopes, err := normalizeExternalScopes(input.Scopes)
	if err != nil {
		return nil, nil, nil, err
	}
	rateLimits, err := normalizeExternalRateLimits(input.RateLimits)
	if err != nil {
		return nil, nil, nil, err
	}
	expiresAt, err := normalizeExternalNullableISO(input.ExpiresAt)
	if err != nil {
		return nil, nil, nil, err
	}
	notes, err := normalizeExternalNullableText(input.Notes)
	if err != nil {
		return nil, nil, nil, err
	}
	rateLimitsJSON, err := json.Marshal(rateLimits)
	if err != nil {
		return nil, nil, nil, err
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, nil, nil, err
	}
	row := &externalSourceRow{
		id:         newID("extsrc"),
		name:       name,
		status:     status,
		scopesJSON: string(scopesJSON),
		rateLimits: sql.NullString{String: string(rateLimitsJSON), Valid: true},
		notes:      ptrToNullString(notes),
		expiresAt:  ptrToNullString(expiresAt),
		createdAt:  now,
		updatedAt:  now,
	}
	return row, rateLimits, scopes, nil
}

func ptrToNullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

// ensureSourceNameAvailable mirrors ensureSourceNameAvailable.
func (s *ExternalStore) ensureSourceNameAvailable(ctx context.Context, q queryer, name, currentID string) error {
	var existingID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("external_integration_sources")+`
		WHERE lower(name) = lower(?) LIMIT 1`), name).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if existingID != currentID {
		return &ValidationError{Message: "来源系统名称已存在"}
	}
	return nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") || strings.Contains(message, "23505")
}

// insertSource mirrors the create INSERT with the duplicate-name mapping.
func (s *ExternalStore) insertSource(ctx context.Context, tx queryer, row *externalSourceRow) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("external_integration_sources")+`
		(id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.id, row.name, row.status, row.scopesJSON, row.rateLimits, row.expiresAt, row.notes,
		row.createdAt, row.updatedAt)
	if err != nil && isUniqueConstraintError(err) {
		return &ValidationError{Message: "来源系统名称已存在"}
	}
	return err
}

// insertToken mirrors the token INSERT with the duplicate mapping.
func (s *ExternalStore) insertToken(ctx context.Context, tx queryer, row *externalTokenWriteRow) error {
	_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("external_integration_source_tokens")+`
		(id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.id, row.sourceRefID, row.name, row.tokenHash, row.tokenSecretEncrypted,
		row.tokenPrefix, row.tokenSuffix, row.status, row.scopesJSON, row.expiresAt,
		row.createdAt, row.updatedAt)
	if err != nil && isUniqueConstraintError(err) {
		return &ValidationError{Message: "来源系统 token 已存在，请重新生成"}
	}
	return err
}

// externalTokenWriteRow carries a fresh token INSERT.
type externalTokenWriteRow struct {
	id                   string
	sourceRefID          string
	name                 string
	tokenHash            string
	tokenSecretEncrypted string
	tokenPrefix          string
	tokenSuffix          string
	status               string
	scopesJSON           string
	expiresAt            sql.NullString
	createdAt            string
	updatedAt            string
}

// buildTokenWriteRow mirrors the token value/normalize part of
// createExternalIntegrationSourceToken.
func (s *ExternalStore) buildTokenWriteRow(sourceRefID, name string, status string, scopes []string, expiresAt *string, now string, token string) (*externalTokenWriteRow, error) {
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, err
	}
	ciphertext, err := apikeys.EncryptJSON(s.CryptoSecret, map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	return &externalTokenWriteRow{
		id:                   s.generateID("exttok"),
		sourceRefID:          sourceRefID,
		name:                 name,
		tokenHash:            hashExternalSourceToken(token),
		tokenSecretEncrypted: ciphertext,
		tokenPrefix:          externalTokenSlice(token, 0, 8),
		tokenSuffix:          externalTokenSlice(token, len(token)-8, len(token)),
		status:               status,
		scopesJSON:           string(scopesJSON),
		expiresAt:            ptrToNullString(expiresAt),
		createdAt:            now,
		updatedAt:            now,
	}, nil
}

// externalTokenSlice mirrors JS token.slice(start, end) semantics for the
// prefix/suffix previews.
func externalTokenSlice(token string, start, end int) string {
	runes := []rune(token)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start >= end {
		return ""
	}
	return string(runes[start:end])
}

// CreateAuthorization mirrors createExternalIntegrationSourceAuthorizationAsync:
// the source row plus its "生产 Token" primary token inside one transaction.
func (s *ExternalStore) CreateAuthorization(ctx context.Context, input externalSourceInput) (*ExternalSourceRecord, *CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	now := s.nowISO()
	row, rateLimits, scopes, err := buildSourceCreateRow(input, now, s.generateID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.ensureSourceNameAvailable(ctx, tx, row.name, ""); err != nil {
		return nil, nil, err
	}
	if err := s.insertSource(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	tokenName := row.name + " 生产 Token"
	tokenValue := createExternalSourceTokenValue()
	tokenRow, err := s.buildTokenWriteRow(row.id, tokenName, row.status, scopes, nullToPtr(row.expiresAt), now, tokenValue)
	if err != nil {
		return nil, nil, err
	}
	if err := s.insertToken(ctx, tx, tokenRow); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	record := &ExternalSourceRecord{
		ID: row.id, Name: row.name, Status: row.status, Scopes: scopes, RateLimits: rateLimits,
		ExpiresAt: nullToPtr(row.expiresAt), Notes: nullToPtr(row.notes),
		CreatedAt: row.createdAt, UpdatedAt: row.updatedAt, IsBuiltIn: false,
	}
	created := &CreatedExternalToken{
		ID: tokenRow.id, Name: tokenRow.name, Token: tokenValue,
		TokenPrefix: tokenRow.tokenPrefix, TokenSuffix: tokenRow.tokenSuffix, Scopes: scopes,
		ExpiresAt: nullToPtr(row.expiresAt),
	}
	return record, created, nil
}

func nullToPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// resolveSourceForToken mirrors resolveSourceForToken.
func (s *ExternalStore) resolveSourceForToken(ctx context.Context, q queryer, sourceRefID string) (string, error) {
	if strings.TrimSpace(sourceRefID) == "" {
		return "", &ValidationError{Message: "来源系统不存在"}
	}
	var id string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), sourceRefID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &ValidationError{Message: "来源系统不存在"}
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// CreateToken mirrors createExternalIntegrationSourceTokenAsync.
func (s *ExternalStore) CreateToken(ctx context.Context, sourceRefID string, input externalTokenInput) (*CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	name, err := normalizeExternalName(input.Name, "来源系统 token 名称不能为空")
	if err == nil && runeLen(name) > 80 {
		err = &ValidationError{Message: "来源系统 token 名称不能超过 80 个字符"}
	}
	if err != nil {
		return nil, err
	}
	sourceID, err := s.resolveSourceForToken(ctx, tx, sourceRefID)
	if err != nil {
		return nil, err
	}
	if sourceID == builtInExternalTestSourceID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持新增 Token"}
	}
	status, err := normalizeTokenStatusInput(input.Status)
	if err != nil {
		return nil, err
	}
	scopes, err := normalizeExternalScopes(input.Scopes)
	if err != nil {
		return nil, err
	}
	expiresAt, err := normalizeExternalNullableISO(input.ExpiresAt)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	tokenValue := createExternalSourceTokenValue()
	tokenRow, err := s.buildTokenWriteRow(sourceID, name, status, scopes, expiresAt, now, tokenValue)
	if err != nil {
		return nil, err
	}
	if err := s.insertToken(ctx, tx, tokenRow); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &CreatedExternalToken{
		ID: tokenRow.id, Name: name, Token: tokenValue,
		TokenPrefix: tokenRow.tokenPrefix, TokenSuffix: tokenRow.tokenSuffix,
		Scopes: scopes, ExpiresAt: expiresAt,
	}, nil
}

// externalTokenInput is the zod-validated token payload.
type externalTokenInput struct {
	Name      any
	Status    any
	Scopes    any
	ExpiresAt any
}

// externalSourceUpdateInput is the zod-validated source PATCH payload.
type externalSourceUpdateInput struct {
	ExpectedUpdatedAt string
	Name              any // string when present
	Status            any
	Scopes            any
	RateLimits        any
	ExpiresAt         any
	Notes             any
	SetFields         map[string]bool
}

// UpdateSource mirrors updateExternalIntegrationSourceAsync.
func (s *ExternalStore) UpdateSource(ctx context.Context, id string, input externalSourceUpdateInput) (*ExternalSourcePatchOutcome, error) {
	ctx = ensureCtx(ctx)
	if strings.TrimSpace(input.ExpectedUpdatedAt) == "" {
		return nil, &ValidationError{Message: "外部来源配置版本不能为空"}
	}
	if id == builtInExternalTestSourceID {
		for _, field := range []string{"name", "scopes", "rateLimits", "expiresAt", "notes"} {
			if input.SetFields[field] {
				return nil, &ValidationError{Message: "内置测试 Token 只支持启用或停用，不支持编辑名称、授权范围、限频、到期时间或备注"}
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Patch projection: always id/name/updated_at, plus the patched columns.
	projection := "id, name, updated_at"
	if input.SetFields["status"] {
		projection += ", status"
	}
	if input.SetFields["scopes"] {
		projection += ", scopes_json"
	}
	if input.SetFields["rateLimits"] {
		projection += ", rate_limits_json"
	}
	if input.SetFields["expiresAt"] {
		projection += ", expires_at"
	}
	if input.SetFields["notes"] {
		projection += ", notes"
	}
	var existingID, existingName, existingUpdatedAt string
	var statusVal, scopesVal, rateLimitsVal, expiresVal, notesVal sql.NullString
	targets := []any{&existingID, &existingName, &existingUpdatedAt}
	if input.SetFields["status"] {
		targets = append(targets, &statusVal)
	}
	if input.SetFields["scopes"] {
		targets = append(targets, &scopesVal)
	}
	if input.SetFields["rateLimits"] {
		targets = append(targets, &rateLimitsVal)
	}
	if input.SetFields["expiresAt"] {
		targets = append(targets, &expiresVal)
	}
	if input.SetFields["notes"] {
		targets = append(targets, &notesVal)
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT `+projection+` FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), id).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existingUpdatedAt != input.ExpectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}

	type updateColumn struct {
		column string
		value  any
	}
	columns := []updateColumn{}
	changes := []ExternalPatchChange{}
	sourceName := existingName
	nextStatus := ""
	if input.SetFields["name"] {
		value, err := normalizeExternalName(input.Name, "来源系统名称不能为空")
		if err != nil {
			return nil, err
		}
		sourceName = value
		if value != existingName {
			columns = append(columns, updateColumn{"name", value})
			changes = append(changes, ExternalPatchChange{Field: "name", Before: existingName, After: value})
		}
	}
	if input.SetFields["status"] {
		before, err := normalizeSourceStatus(statusVal.String)
		if err != nil {
			return nil, err
		}
		value, err := normalizeSourceStatusInput(input.Status)
		if err != nil {
			return nil, err
		}
		if value != before {
			columns = append(columns, updateColumn{"status", value})
			changes = append(changes, ExternalPatchChange{Field: "status", Before: before, After: value})
			nextStatus = value
		}
	}
	if input.SetFields["scopes"] {
		value, err := normalizeExternalScopes(input.Scopes)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != scopesVal.String {
			columns = append(columns, updateColumn{"scopes_json", string(encoded)})
			before, beforeErr := decodeExternalScopes(scopesVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "scopes", Before: before, After: value})
		}
	}
	if input.SetFields["rateLimits"] {
		value, err := normalizeExternalRateLimits(input.RateLimits)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != rateLimitsVal.String {
			columns = append(columns, updateColumn{"rate_limits_json", string(encoded)})
			before, beforeErr := decodeExternalRateLimits(rateLimitsVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "rateLimits", Before: before, After: value})
		}
	}
	if input.SetFields["expiresAt"] {
		value, err := normalizeExternalNullableISO(input.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != expiresVal.String {
			columns = append(columns, updateColumn{"expires_at", value})
			changes = append(changes, ExternalPatchChange{
				Field: "expiresAt", Before: nullToPtr(expiresVal), After: value,
			})
		}
	}
	if input.SetFields["notes"] {
		value, err := normalizeExternalNullableText(input.Notes)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != notesVal.String {
			columns = append(columns, updateColumn{"notes", value})
			changes = append(changes, ExternalPatchChange{
				Field: "notes", Before: nullToPtr(notesVal), After: value,
			})
		}
	}

	nextUpdatedAt := existingUpdatedAt
	if len(columns) > 0 {
		nameChanged := sourceName != existingName
		if nameChanged {
			if err := s.ensureSourceNameAvailable(ctx, tx, sourceName, id); err != nil {
				return nil, err
			}
		}
		tokenUpdatedAt := ""
		if id != builtInExternalTestSourceID && nextStatus != "" {
			var latest sql.NullString
			err := tx.QueryRowContext(ctx, s.bind(`SELECT updated_at FROM `+s.table("external_integration_source_tokens")+`
				WHERE source_ref_id = ?
				ORDER BY updated_at DESC
				LIMIT 1`), id).Scan(&latest)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if latest.Valid {
				tokenUpdatedAt = latest.String
			}
		}
		base := existingUpdatedAt
		if tokenUpdatedAt > existingUpdatedAt {
			base = tokenUpdatedAt
		}
		updated, err := nextRFC3339Millis(base, s.now(), "外部集成来源 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		if err != nil {
			return nil, err
		}
		nextUpdatedAt = updated
		assignments := make([]string, 0, len(columns))
		values := make([]any, 0, len(columns))
		for _, column := range columns {
			assignments = append(assignments, column.column+" = ?")
			values = append(values, column.value)
		}
		values = append(values, nextUpdatedAt, id, input.ExpectedUpdatedAt)
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_sources")+`
			SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND updated_at = ?"), values...)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, &ConflictError{Message: externalConflictMessage}
		}
		if id != builtInExternalTestSourceID && nextStatus != "" {
			if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
				SET status = ?, updated_at = ?
				WHERE source_ref_id = ? AND status <> 'revoked' AND status <> ?`),
				nextStatus, nextUpdatedAt, id, nextStatus); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ExternalSourcePatchOutcome{
		Mutation:   ExternalMutationResult{ID: existingID, UpdatedAt: nextUpdatedAt},
		SourceName: sourceName,
		Changes:    changes,
	}, nil
}

func nullStringText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// DeleteSource mirrors deleteExternalIntegrationSourceAsync.
func (s *ExternalStore) DeleteSource(ctx context.Context, id, expectedUpdatedAt string) (*ExternalSourceDeleteReceipt, error) {
	ctx = ensureCtx(ctx)
	if id == builtInExternalTestSourceID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持删除"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceID, name, updatedAt string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, name, updated_at FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), id).Scan(&sourceID, &name, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if updatedAt != expectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("external_integration_source_tokens")+`
		WHERE source_ref_id = ?`), id); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("external_integration_sources")+`
		WHERE id = ? AND updated_at = ?`), id, expectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ExternalSourceDeleteReceipt{ID: sourceID, Name: name}, nil
}

// externalTokenUpdateInput is the zod-validated token PATCH payload.
type externalTokenUpdateInput struct {
	ExpectedUpdatedAt string
	Name              any
	Status            any
	Scopes            any
	ExpiresAt         any
	SetFields         map[string]bool
}

// UpdateToken mirrors updateExternalIntegrationSourceTokenAsync.
func (s *ExternalStore) UpdateToken(ctx context.Context, sourceRefID, tokenID string, input externalTokenUpdateInput) (*ExternalTokenPatchOutcome, error) {
	ctx = ensureCtx(ctx)
	if sourceRefID == builtInExternalTestSourceID || tokenID == builtInExternalTestTokenID {
		return nil, &ValidationError{Message: "内置测试 Token 不支持编辑"}
	}
	if strings.TrimSpace(input.ExpectedUpdatedAt) == "" {
		return nil, &ValidationError{Message: "来源系统 token 版本不能为空"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	projection := "tokens.id, tokens.source_ref_id, tokens.name, tokens.updated_at, sources.name AS source_name"
	if input.SetFields["status"] {
		projection += ", tokens.status"
	}
	if input.SetFields["scopes"] {
		projection += ", tokens.scopes_json"
	}
	if input.SetFields["expiresAt"] {
		projection += ", tokens.expires_at"
	}
	var id, refID, existingName, existingUpdatedAt, sourceName string
	var statusVal, scopesVal, expiresVal sql.NullString
	targets := []any{&id, &refID, &existingName, &existingUpdatedAt, &sourceName}
	if input.SetFields["status"] {
		targets = append(targets, &statusVal)
	}
	if input.SetFields["scopes"] {
		targets = append(targets, &scopesVal)
	}
	if input.SetFields["expiresAt"] {
		targets = append(targets, &expiresVal)
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT `+projection+`
		FROM `+s.table("external_integration_source_tokens")+` AS tokens
		INNER JOIN `+s.table("external_integration_sources")+` AS sources ON sources.id = tokens.source_ref_id
		WHERE tokens.id = ? AND tokens.source_ref_id = ?`), tokenID, sourceRefID).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existingUpdatedAt != input.ExpectedUpdatedAt {
		return nil, &ConflictError{Message: externalConflictMessage}
	}
	nextUpdatedAt, err := nextRFC3339Millis(existingUpdatedAt, s.now(), "外部集成来源 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	if err != nil {
		return nil, err
	}

	type tokenColumn struct {
		column string
		value  any
	}
	columns := []tokenColumn{}
	changes := []ExternalPatchChange{}
	if input.SetFields["name"] {
		value, err := normalizeExternalName(input.Name, "来源系统 token 名称不能为空")
		if err == nil && runeLen(value) > 80 {
			err = &ValidationError{Message: "来源系统 token 名称不能超过 80 个字符"}
		}
		if err != nil {
			return nil, err
		}
		if value != existingName {
			columns = append(columns, tokenColumn{"name", value})
			changes = append(changes, ExternalPatchChange{Field: "name", Before: existingName, After: value})
		}
	}
	if input.SetFields["status"] {
		before, err := normalizeTokenStatus(statusVal.String)
		if err != nil {
			return nil, err
		}
		value, err := normalizeTokenStatusInput(input.Status)
		if err != nil {
			return nil, err
		}
		if value != before {
			revokedAt := sql.NullString{}
			if value == "revoked" {
				revokedAt = sql.NullString{String: nextUpdatedAt, Valid: true}
			}
			columns = append(columns, tokenColumn{"status", value}, tokenColumn{"revoked_at", revokedAt})
			changes = append(changes, ExternalPatchChange{Field: "status", Before: before, After: value})
		}
	}
	if input.SetFields["scopes"] {
		value, err := normalizeExternalScopes(input.Scopes)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if string(encoded) != scopesVal.String {
			columns = append(columns, tokenColumn{"scopes_json", string(encoded)})
			before, beforeErr := decodeExternalScopes(scopesVal.String)
			if beforeErr != nil {
				return nil, beforeErr
			}
			changes = append(changes, ExternalPatchChange{Field: "scopes", Before: before, After: value})
		}
	}
	if input.SetFields["expiresAt"] {
		value, err := normalizeExternalNullableISO(input.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if nullStringText(value) != expiresVal.String {
			columns = append(columns, tokenColumn{"expires_at", value})
			changes = append(changes, ExternalPatchChange{
				Field: "expiresAt", Before: nullToPtr(expiresVal), After: value,
			})
		}
	}

	tokenName := existingName
	currentUpdatedAt := existingUpdatedAt
	if len(columns) > 0 {
		assignments := make([]string, 0, len(columns))
		values := make([]any, 0, len(columns))
		for _, column := range columns {
			assignments = append(assignments, column.column+" = ?")
			values = append(values, column.value)
		}
		values = append(values, nextUpdatedAt, tokenID, sourceRefID, input.ExpectedUpdatedAt)
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
			SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND source_ref_id = ? AND updated_at = ?"), values...)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, &ConflictError{Message: externalConflictMessage}
		}
		currentUpdatedAt = nextUpdatedAt
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if renamed, ok := input.Name.(string); ok && strings.TrimSpace(renamed) != "" {
		if value, err := normalizeExternalName(renamed, "来源系统 token 名称不能为空"); err == nil {
			tokenName = value
		}
	}
	return &ExternalTokenPatchOutcome{
		Mutation:   ExternalMutationResult{ID: id, UpdatedAt: currentUpdatedAt},
		SourceName: sourceName,
		TokenName:  tokenName,
		Changes:    changes,
	}, nil
}

// ResetBuiltInTestToken mirrors resetBuiltInExternalIntegrationTestTokenAsync.
func (s *ExternalStore) ResetBuiltInTestToken(ctx context.Context) (*CreatedExternalToken, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceScopesJSON string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT scopes_json FROM `+s.table("external_integration_sources")+`
		WHERE id = ?`), builtInExternalTestSourceID).Scan(&sourceScopesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "内置测试 Token 不存在"}
	}
	if err != nil {
		return nil, err
	}
	var tokenName string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT name FROM `+s.table("external_integration_source_tokens")+`
		WHERE id = ? AND source_ref_id = ?`), builtInExternalTestTokenID, builtInExternalTestSourceID).Scan(&tokenName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "内置测试 Token 不存在"}
	}
	if err != nil {
		return nil, err
	}
	token := createExternalSourceTokenValue()
	now := s.nowISO()
	ciphertext, err := apikeys.EncryptJSON(s.CryptoSecret, map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_source_tokens")+`
		SET token_hash = ?, token_secret_encrypted = ?, token_prefix = ?, token_suffix = ?,
			status = 'active', revoked_at = NULL, updated_at = ?
		WHERE id = ? AND source_ref_id = ?`),
		hashExternalSourceToken(token), ciphertext,
		externalTokenSlice(token, 0, 8), externalTokenSlice(token, len(token)-8, len(token)),
		now, builtInExternalTestTokenID, builtInExternalTestSourceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("external_integration_sources")+`
		SET updated_at = ? WHERE id = ?`), now, builtInExternalTestSourceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	scopes, err := decodeExternalScopes(sourceScopesJSON)
	if err != nil {
		return nil, err
	}
	return &CreatedExternalToken{
		ID: builtInExternalTestTokenID, Name: tokenName, Token: token,
		TokenPrefix: externalTokenSlice(token, 0, 8),
		TokenSuffix: externalTokenSlice(token, len(token)-8, len(token)),
		Scopes:      scopes,
	}, nil
}

// ---------------------------------------------------------------------------
// M16b route family (mounted behind requireAdmin).
// ---------------------------------------------------------------------------

// ExternalDeps bundles the M16b collaborators.
type ExternalDeps struct {
	Store *ExternalStore
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the external-integration-sources route family.
func (d *ExternalDeps) Mount(k *kernel.Kernel) {
	k.Register("GET "+externalPrefix+"/scopes", d.Auth.RequireAdmin(http.HandlerFunc(d.scopes)))
	k.Register("GET "+externalPrefix+"/api-docs", d.Auth.RequireAdmin(http.HandlerFunc(d.apiDocs)))
	k.Register("GET "+externalPrefix, d.Auth.RequireAdmin(http.HandlerFunc(d.list)))
	k.Register("POST "+externalPrefix+"/built-in-test-token/reset", d.Auth.RequireAdmin(d.guardedResetBuiltInTestToken()))
	k.Register("POST "+externalPrefix, d.Auth.RequireAdmin(d.guardedCreate()))
	k.Register("GET "+externalPrefix+"/{id}", d.Auth.RequireAdmin(http.HandlerFunc(d.detail)))
	k.Register("PATCH "+externalPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedPatchSource()))
	k.Register("DELETE "+externalPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedDeleteSource()))
	k.Register("POST "+externalPrefix+"/{id}/tokens", d.Auth.RequireAdmin(d.guardedCreateToken()))
	k.Register("GET "+externalPrefix+"/{id}/tokens/{tokenId}/secret", d.Auth.RequireAdmin(http.HandlerFunc(d.tokenSecret)))
	k.Register("PATCH "+externalPrefix+"/{id}/tokens/{tokenId}", d.Auth.RequireAdmin(d.guardedPatchToken()))
}

func (d *ExternalDeps) scopes(w http.ResponseWriter, _ *http.Request) {
	kernel.WriteOK(w, d.Store.Scopes(), "")
}

func (d *ExternalDeps) apiDocs(w http.ResponseWriter, _ *http.Request) {
	kernel.WriteOK(w, externalPublicAPICatalog(), "")
}

func (d *ExternalDeps) list(w http.ResponseWriter, r *http.Request) {
	page, pageSize, keyword, status, message := parseExternalListQuery(r.URL.Query())
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	result, err := d.Store.ListPage(r.Context(), page, pageSize, keyword, status)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ExternalDeps) detail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		kernel.WriteBadRequest(w, "来源系统不存在")
		return
	}
	source, err := d.Store.FindSource(r.Context(), id)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if source == nil {
		kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
		return
	}
	kernel.WriteOK(w, source, "")
}

func (d *ExternalDeps) tokenSecret(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	tokenID := strings.TrimSpace(r.PathValue("tokenId"))
	if id == "" {
		kernel.WriteBadRequest(w, "来源系统不存在")
		return
	}
	if tokenID == "" {
		kernel.WriteBadRequest(w, "Token 不存在")
		return
	}
	token, err := d.Store.FindTokenSecret(r.Context(), id, tokenID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if token == nil {
		kernel.WriteError(w, http.StatusNotFound, "Token 不存在")
		return
	}
	setNoStoreHeaders(w)
	kernel.WriteOK(w, map[string]any{"token": *token}, "")
}

// setSecretNoStoreHeaders mirrors setSecretResponseHeaders for token-bearing
// responses.
func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (d *ExternalDeps) guardedResetBuiltInTestToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.reset_builtin_test_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(*http.Request) (any, error) {
			return map[string]any{"target": "built_in_test_token"}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := d.Store.ResetBuiltInTestToken(r.Context())
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "重置内置测试 Token 失败"))
			return
		}
		sourceName := "内置测试 Token"
		if source, findErr := d.Store.FindSource(r.Context(), builtInExternalTestSourceID); findErr == nil && source != nil {
			sourceName = source.Name
		}
		d.recordSourceOperation(r, "reset_builtin_test_token", "external_integration_sources.reset_builtin_test_token",
			builtInExternalTestSourceID, sourceName, "重置内置测试 Token", []authsys.OperationLogChange{{
				Field: "tokenPreview", Label: "Token 标识",
				After: token.TokenPrefix + "..." + token.TokenSuffix,
			}})
		setNoStoreHeaders(w)
		kernel.WriteOK(w, map[string]any{"token": token}, "")
	}))
}

func (d *ExternalDeps) guardedCreate() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.create",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"name":       kernel.BodyField(r, "name"),
				"status":     kernel.BodyField(r, "status"),
				"scopes":     kernel.BodyField(r, "scopes"),
				"rateLimits": kernel.BodyField(r, "rateLimits"),
				"expiresAt":  kernel.BodyField(r, "expiresAt"),
				"notes":      kernel.BodyField(r, "notes"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalSourceBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		source, token, err := d.Store.CreateAuthorization(r.Context(), input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "来源系统创建失败"))
			return
		}
		d.recordSourceOperation(r, "create", "external_integration_sources.create",
			source.ID, source.Name, "创建外部来源系统："+source.Name, []authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: source.Name},
				{Field: "status", Label: "状态", After: source.Status},
				{Field: "expiresAt", Label: "到期时间", After: safeChangeText(source.ExpiresAt)},
				{Field: "rateLimits", Label: "限频规则", After: formatExternalRateLimits(source.RateLimits)},
			})
		setNoStoreHeaders(w)
		writeCreatedOK(w, map[string]any{
			"item":  externalCreatedListItem(source, token),
			"token": token,
		})
	}))
}

func externalCreatedListItem(source *ExternalSourceRecord, token *CreatedExternalToken) ExternalSourceListItem {
	return ExternalSourceListItem{
		ID: source.ID, Name: source.Name, Status: source.Status, Scopes: source.Scopes,
		RateLimits: source.RateLimits, ExpiresAt: source.ExpiresAt, Notes: source.Notes,
		LastUsedAt: source.LastUsedAt, UpdatedAt: source.UpdatedAt,
		PrimaryToken: &ExternalPrimaryToken{ID: token.ID, TokenPrefix: token.TokenPrefix, TokenSuffix: token.TokenSuffix},
		IsBuiltIn:    source.IsBuiltIn,
	}
}

func (d *ExternalDeps) guardedPatchSource() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.update",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"name":              kernel.BodyField(r, "name"),
				"status":            kernel.BodyField(r, "status"),
				"scopes":            kernel.BodyField(r, "scopes"),
				"expiresAt":         kernel.BodyField(r, "expiresAt"),
				"rateLimits":        kernel.BodyField(r, "rateLimits"),
				"notes":             kernel.BodyField(r, "notes"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalSourceUpdateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.UpdateSource(r.Context(), id, input)
		if err != nil {
			d.writeSourceMutationError(w, err)
			return
		}
		if outcome == nil {
			kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
			return
		}
		if len(outcome.Changes) > 0 {
			d.recordSourceOperation(r, "update", "external_integration_sources.update",
				outcome.Mutation.ID, outcome.SourceName, "更新外部来源系统："+outcome.SourceName,
				externalSourceOperationChanges(outcome.Changes))
		}
		kernel.WriteOK(w, outcome.Mutation, "")
	}))
}

func (d *ExternalDeps) guardedDeleteSource() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.delete",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		expected, message := parseExternalDeleteBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		receipt, err := d.Store.DeleteSource(r.Context(), id, expected)
		if err != nil {
			d.writeSourceMutationError(w, err, "删除来源授权失败")
			return
		}
		if receipt == nil {
			kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
			return
		}
		d.recordSourceOperation(r, "delete", "external_integration_sources.delete",
			receipt.ID, receipt.Name, "删除外部来源系统："+receipt.Name, []authsys.OperationLogChange{
				{Field: "deleted", Label: "删除状态", Before: "false", After: "true"},
			})
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (d *ExternalDeps) guardedCreateToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.create_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":        r.PathValue("id"),
				"name":      kernel.BodyField(r, "name"),
				"status":    kernel.BodyField(r, "status"),
				"scopes":    kernel.BodyField(r, "scopes"),
				"expiresAt": kernel.BodyField(r, "expiresAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalTokenBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		token, err := d.Store.CreateToken(r.Context(), id, input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "Token 创建失败"))
			return
		}
		sourceName := id
		if source, findErr := d.Store.FindSource(r.Context(), id); findErr == nil && source != nil {
			sourceName = source.Name
		}
		d.recordSourceOperation(r, "create_token", "external_integration_sources.create_token",
			id, sourceName, "生成外部来源系统 Token："+sourceName, []authsys.OperationLogChange{
				{Field: "tokenName", Label: "Token 名称", After: token.Name},
				{Field: "tokenPreview", Label: "Token 标识", After: token.TokenPrefix + "..." + token.TokenSuffix},
				{Field: "expiresAt", Label: "到期时间", After: safeChangeText(token.ExpiresAt)},
			})
		setNoStoreHeaders(w)
		writeCreatedOK(w, map[string]any{"token": token})
	}))
}

func (d *ExternalDeps) guardedPatchToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.update_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"tokenId":           r.PathValue("tokenId"),
				"name":              kernel.BodyField(r, "name"),
				"status":            kernel.BodyField(r, "status"),
				"scopes":            kernel.BodyField(r, "scopes"),
				"expiresAt":         kernel.BodyField(r, "expiresAt"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		tokenID := strings.TrimSpace(r.PathValue("tokenId"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		if tokenID == "" {
			kernel.WriteBadRequest(w, "Token 不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalTokenUpdateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.UpdateToken(r.Context(), id, tokenID, input)
		if err != nil {
			d.writeSourceMutationError(w, err)
			return
		}
		if outcome == nil {
			kernel.WriteError(w, http.StatusNotFound, "Token 不存在")
			return
		}
		if len(outcome.Changes) > 0 {
			d.recordSourceOperation(r, "update_token", "external_integration_sources.update_token",
				id, outcome.SourceName, "更新外部来源系统 Token："+outcome.TokenName,
				externalTokenOperationChanges(outcome.Changes))
		}
		kernel.WriteOK(w, outcome.Mutation, "")
	}))
}

// writeSourceMutationError maps store errors onto the Node contract:
// conflicts → 409, everything else → 400.
func (d *ExternalDeps) writeSourceMutationError(w http.ResponseWriter, err error, fallback ...string) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	message := "来源系统更新失败"
	if len(fallback) > 0 && fallback[0] != "" {
		message = fallback[0]
	}
	kernel.WriteBadRequest(w, storeErrorMessage(err, message))
}

func (d *ExternalDeps) recordSourceOperation(r *http.Request, action, operationKey, sourceRefID, sourceName, summary string, changes []authsys.OperationLogChange) {
	if d.Sink == nil {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auth.SystemAccountID,
		ActorUsername:        auth.Username,
		ActorDisplayName:     auth.DisplayName,
		ActorRole:            auth.Role,
		Mode:                 "admin",
		Module:               "external_integration_sources",
		Action:               action,
		OperationKey:         operationKey,
		ResourceType:         "external_integration_source",
		ResourceID:           sourceRefID,
		ResourceName:         sourceName,
		Summary:              summary,
		Changes:              changes,
	}, r)
}

func formatExternalRateLimits(rules []ExternalRateLimitRule) string {
	if len(rules) == 0 {
		return "不限制"
	}
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strconv.Itoa(rule.WindowSeconds)+"s/"+strconv.Itoa(rule.MaxRequests)+"次")
	}
	return strings.Join(parts, ", ")
}

// externalSourceOperationChanges mirrors sourcePatchOperationChanges.
func externalSourceOperationChanges(changes []ExternalPatchChange) []authsys.OperationLogChange {
	out := make([]authsys.OperationLogChange, 0, len(changes))
	for _, change := range changes {
		switch change.Field {
		case "rateLimits":
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: "限频规则",
				Before: formatExternalRateLimits(asRateLimitRules(change.Before)),
				After:  formatExternalRateLimits(asRateLimitRules(change.After)),
			})
		case "scopes":
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: "接口资源授权",
				Before: formatScopes(change.Before), After: formatScopes(change.After),
			})
		default:
			label := "备注"
			switch change.Field {
			case "name":
				label = "名称"
			case "status":
				label = "状态"
			case "expiresAt":
				label = "到期时间"
			}
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: label,
				Before: safeChangeText(change.Before), After: safeChangeText(change.After),
			})
		}
	}
	return out
}

// externalTokenOperationChanges mirrors tokenPatchOperationChanges.
func externalTokenOperationChanges(changes []ExternalPatchChange) []authsys.OperationLogChange {
	out := make([]authsys.OperationLogChange, 0, len(changes))
	for _, change := range changes {
		field := "token" + strings.ToUpper(change.Field[:1]) + change.Field[1:]
		label := "Token 到期时间"
		switch change.Field {
		case "name":
			label = "Token 名称"
		case "status":
			label = "Token 状态"
		case "scopes":
			label = "Token 接口资源授权"
		}
		before := safeChangeText(change.Before)
		after := safeChangeText(change.After)
		if change.Field == "scopes" {
			before = formatScopes(change.Before)
			after = formatScopes(change.After)
		}
		out = append(out, authsys.OperationLogChange{Field: field, Label: label, Before: before, After: after})
	}
	return out
}

func asRateLimitRules(value any) []ExternalRateLimitRule {
	items, ok := value.([]ExternalRateLimitRule)
	if ok {
		return items
	}
	list, ok := value.([]any)
	if !ok {
		return []ExternalRateLimitRule{}
	}
	out := []ExternalRateLimitRule{}
	for _, item := range list {
		record, isObject := item.(map[string]any)
		if !isObject {
			continue
		}
		window, windowOK := record["windowSeconds"].(float64)
		maxRequests, maxOK := record["maxRequests"].(float64)
		if windowOK && maxOK {
			out = append(out, ExternalRateLimitRule{WindowSeconds: int(window), MaxRequests: int(maxRequests)})
		}
	}
	return out
}

func formatScopes(value any) string {
	items, ok := asStringList(value)
	if !ok {
		return ""
	}
	return strings.Join(items, ", ")
}

// ---------------------------------------------------------------------------
// M16b zod schema mirrors.
// ---------------------------------------------------------------------------

// parseExternalListQuery mirrors listQuerySchema (non-strict).
func parseExternalListQuery(query map[string][]string) (page, pageSize *int, keyword, status, message string) {
	if _, exists := firstQueryValue(query, "page"); exists {
		number, issue := coerceQueryNumber(query["page"])
		if issue != "" {
			return nil, nil, "", "", issue
		}
		if number != float64(int64(number)) {
			return nil, nil, "", "", "Expected integer, received float"
		}
		value := int(number)
		if value < 1 {
			return nil, nil, "", "", zodNumberMin(1)
		}
		page = &value
	}
	if _, exists := firstQueryValue(query, "pageSize"); exists {
		number, issue := coerceQueryNumber(query["pageSize"])
		if issue != "" {
			return nil, nil, "", "", issue
		}
		if number != float64(int64(number)) {
			return nil, nil, "", "", "Expected integer, received float"
		}
		value := int(number)
		if value < 1 {
			return nil, nil, "", "", zodNumberMin(1)
		}
		if value > 100 {
			return nil, nil, "", "", zodNumberMax(100)
		}
		pageSize = &value
	}
	if raw, exists := firstQueryValue(query, "keyword"); exists {
		if len(query["keyword"]) > 1 {
			return nil, nil, "", "", zodInvalidType("string", []any{})
		}
		keyword = strings.TrimSpace(raw)
	}
	if raw, exists := firstQueryValue(query, "status"); exists {
		if len(query["status"]) > 1 {
			return nil, nil, "", "", zodInvalidType("string", []any{})
		}
		if raw != "all" && raw != "active" && raw != "disabled" {
			return nil, nil, "", "", zodEnumMessage([]string{"all", "active", "disabled"}, raw)
		}
		status = raw
	}
	return page, pageSize, keyword, status, ""
}

// coerceQueryNumber mirrors z.coerce.number: blank text coerces to 0 (fails
// min(1)); non-numeric text is NaN ("Expected number, received nan").
func coerceQueryNumber(values []string) (float64, string) {
	if len(values) > 1 {
		return 0, "Expected number, received nan"
	}
	text := strings.TrimSpace(values[0])
	if text == "" {
		return 0, ""
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, "Expected number, received nan"
	}
	return number, ""
}

// parseExternalSourceBody mirrors sourceBodySchema (strict).
func parseExternalSourceBody(body map[string]any) (externalSourceInput, string) {
	input := externalSourceInput{}
	raw, present := body["name"]
	if !present {
		return input, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return input, "来源系统名称不能为空"
	}
	if runeLen(trimmed) > 80 {
		return input, "来源系统名称不能超过 80 个字符"
	}
	input.Name = trimmed
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" {
			return input, zodEnumMessage([]string{"active", "disabled"}, text)
		}
		input.Status = text
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
	}
	if value, exists := body["rateLimits"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		if len(items) > 8 {
			return input, "限频规则最多 8 条"
		}
		for _, item := range items {
			if message := validateExternalRateLimitRule(item); message != "" {
				return input, message
			}
		}
		input.RateLimits = value
	}
	if value, exists := body["expiresAt"]; exists && value != nil {
		if _, isString := value.(string); !isString {
			return input, "过期时间无效"
		}
		if _, ok := canonicalRFC3339Millis(value.(string)); !ok {
			return input, "过期时间无效"
		}
		input.ExpiresAt = value
	}
	if value, exists := body["notes"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if runeLen(strings.TrimSpace(text)) > 500 {
			return input, "备注不能超过 500 个字符"
		}
		input.Notes = value
	}
	if message := externalUnknownBodyKey(body, externalSourceBodyKeys); message != "" {
		return input, message
	}
	return input, ""
}

var externalSourceBodyKeys = []string{"name", "status", "scopes", "rateLimits", "expiresAt", "notes"}

// validateExternalRateLimitRule mirrors rateLimitRuleSchema (strict).
func validateExternalRateLimitRule(item any) string {
	record, isObject := item.(map[string]any)
	if !isObject {
		return zodInvalidType("object", item)
	}
	unknown := []string{}
	for key := range record {
		if !containsString(externalRateLimitRuleKeys, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		return zodUnrecognizedKeys(unknown)
	}
	window, ok := record["windowSeconds"].(float64)
	if !ok {
		return zodInvalidType("number", record["windowSeconds"])
	}
	if window != float64(int64(window)) {
		return "Expected integer, received float"
	}
	if int(window) < 1 {
		return "限频窗口不能小于 1 秒"
	}
	if int(window) > 86400 {
		return "限频窗口不能超过 86400 秒"
	}
	maxRequests, ok := record["maxRequests"].(float64)
	if !ok {
		return zodInvalidType("number", record["maxRequests"])
	}
	if maxRequests != float64(int64(maxRequests)) {
		return "Expected integer, received float"
	}
	if int(maxRequests) < 1 {
		return "限频次数不能小于 1"
	}
	if int(maxRequests) > 100000 {
		return "限频次数不能超过 100000"
	}
	return ""
}

func externalUnknownBodyKey(body map[string]any, allowed []string) string {
	unknown := []string{}
	for key := range body {
		if !containsString(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	return zodUnrecognizedKeys(unknown)
}

// parseExternalSourceUpdateBody mirrors sourceUpdateBodySchema.
func parseExternalSourceUpdateBody(body map[string]any) (externalSourceUpdateInput, string) {
	input := externalSourceUpdateInput{SetFields: map[string]bool{}}
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return input, zodRequired
	}
	expected, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expected)
	if !ok {
		return input, "外部来源配置版本格式不正确"
	}
	input.ExpectedUpdatedAt = canonical
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, "来源系统名称不能为空"
		}
		if runeLen(trimmed) > 80 {
			return input, "来源系统名称不能超过 80 个字符"
		}
		input.Name = trimmed
		input.SetFields["name"] = true
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" {
			return input, zodEnumMessage([]string{"active", "disabled"}, text)
		}
		input.Status = text
		input.SetFields["status"] = true
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
		input.SetFields["scopes"] = true
	}
	if value, exists := body["rateLimits"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		if len(items) > 8 {
			return input, "限频规则最多 8 条"
		}
		for _, item := range items {
			if message := validateExternalRateLimitRule(item); message != "" {
				return input, message
			}
		}
		input.RateLimits = value
		input.SetFields["rateLimits"] = true
	}
	if value, exists := body["expiresAt"]; exists {
		if value == nil {
			input.ExpiresAt = nil
			input.SetFields["expiresAt"] = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, "过期时间无效"
			}
			canonical, ok := canonicalRFC3339Millis(text)
			if !ok {
				return input, "过期时间无效"
			}
			input.ExpiresAt = canonical
			input.SetFields["expiresAt"] = true
		}
	}
	if value, exists := body["notes"]; exists {
		if value == nil {
			input.Notes = nil
			input.SetFields["notes"] = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, zodInvalidType("string", value)
			}
			if runeLen(strings.TrimSpace(text)) > 500 {
				return input, "备注不能超过 500 个字符"
			}
			input.Notes = text
			input.SetFields["notes"] = true
		}
	}
	if message := externalUnknownBodyKey(body, append(externalSourceBodyKeys, "expectedUpdatedAt")); message != "" {
		return input, message
	}
	hasChange := false
	for _, field := range externalSourceBodyKeys {
		if input.SetFields[field] {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return input, "请提供要修改的来源配置字段"
	}
	return input, ""
}

// parseExternalDeleteBody mirrors sourceDeleteBodySchema (strict).
func parseExternalDeleteBody(body map[string]any) (string, string) {
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return "", zodRequired
	}
	text, isString := raw.(string)
	if !isString {
		return "", zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(text)
	if !ok {
		return "", "外部来源配置版本格式不正确"
	}
	if message := externalUnknownBodyKey(body, []string{"expectedUpdatedAt"}); message != "" {
		return "", message
	}
	return canonical, ""
}

// parseExternalTokenBody mirrors tokenBodySchema (strict).
func parseExternalTokenBody(body map[string]any) (externalTokenInput, string) {
	input := externalTokenInput{}
	raw, present := body["name"]
	if !present {
		return input, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return input, "Token 名称不能为空"
	}
	if runeLen(trimmed) > 80 {
		return input, "Token 名称不能超过 80 个字符"
	}
	input.Name = trimmed
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" && text != "revoked" {
			return input, zodEnumMessage([]string{"active", "disabled", "revoked"}, text)
		}
		input.Status = text
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
	}
	if value, exists := body["expiresAt"]; exists && value != nil {
		if _, isString := value.(string); !isString {
			return input, "过期时间无效"
		}
		if _, ok := canonicalRFC3339Millis(value.(string)); !ok {
			return input, "过期时间无效"
		}
		input.ExpiresAt = value
	}
	if message := externalUnknownBodyKey(body, []string{"name", "status", "scopes", "expiresAt"}); message != "" {
		return input, message
	}
	return input, ""
}

// parseExternalTokenUpdateBody mirrors tokenUpdateBodySchema.
func parseExternalTokenUpdateBody(body map[string]any) (externalTokenUpdateInput, string) {
	input := externalTokenUpdateInput{SetFields: map[string]bool{}}
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return input, zodRequired
	}
	expected, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expected)
	if !ok {
		return input, "外部来源配置版本格式不正确"
	}
	input.ExpectedUpdatedAt = canonical
	hasChange := false
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, "Token 名称不能为空"
		}
		if runeLen(trimmed) > 80 {
			return input, "Token 名称不能超过 80 个字符"
		}
		input.Name = trimmed
		input.SetFields["name"] = true
		hasChange = true
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return input, zodInvalidType("string", value)
		}
		if text != "active" && text != "disabled" && text != "revoked" {
			return input, zodEnumMessage([]string{"active", "disabled", "revoked"}, text)
		}
		input.Status = text
		input.SetFields["status"] = true
		hasChange = true
	}
	if value, exists := body["scopes"]; exists && value != nil {
		items, isList := value.([]any)
		if !isList {
			return input, zodInvalidType("array", value)
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return input, zodInvalidType("string", item)
			}
			if strings.TrimSpace(text) == "" {
				return input, zodStringMin(1)
			}
		}
		input.Scopes = value
		input.SetFields["scopes"] = true
		hasChange = true
	}
	if value, exists := body["expiresAt"]; exists {
		if value == nil {
			input.ExpiresAt = nil
			input.SetFields["expiresAt"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return input, "过期时间无效"
			}
			canonical, ok := canonicalRFC3339Millis(text)
			if !ok {
				return input, "过期时间无效"
			}
			input.ExpiresAt = canonical
			input.SetFields["expiresAt"] = true
			hasChange = true
		}
	}
	if message := externalUnknownBodyKey(body, []string{"expectedUpdatedAt", "name", "status", "scopes", "expiresAt"}); message != "" {
		return input, message
	}
	if !hasChange {
		return input, "请提供要修改的 Token 字段"
	}
	return input, ""
}
