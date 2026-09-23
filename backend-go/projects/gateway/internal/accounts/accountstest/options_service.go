package accountstest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// Manual test model options surface: the port of
// storage/account-manual-test-context.repository.ts +
// modules/accounts/account-test-endpoint-modes.ts +
// modules/accounts/account-test-options.service.ts plus the catalog read
// subset (provider-model-options.service.ts + model-catalog.service.ts
// findProviderModelTestCatalogItemAsync). The catalog reads query the same
// provider_model_catalog / custom_provider_models / providers tables the
// providers slice serves; the accounts store reads them directly (same
// pattern as requireEnabledProviderProtocolProfile) so the slice stays
// self-contained without a cross-package dependency.

// ManualTestContext mirrors AccountManualTestOptionsContext: the controlled
// account projection the test-options surface consumes. Credentials are
// server-side only (endpoint-mode resolution) and never serialized.
type ManualTestContext struct {
	ID                        string
	FactAccountID             string
	OwnerSystemAccountID      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	ClientCompatibility       string
	HealthCheckModel          string
	// capabilities-context fields
	HealthCheckEndpointMode string
	SupportedEndpointModes  []string
	ModelMappings           []accountscore.ModelMapping
}

// ManualTestOption mirrors AccountManualTestOption.
type ManualTestOption struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	TestEndpointModes []string `json:"testEndpointModes"`
}

// ManualTestModelCapabilities mirrors AccountManualTestModelCapabilities.
type ManualTestModelCapabilities struct {
	ID                string   `json:"id"`
	TestEndpointModes []string `json:"testEndpointModes"`
}

// ManualTestOptionsQuery mirrors normalizeAccountManualTestOptionsQuery
// output (keyword/limit/selectedIds only — the provider code comes from the
// account).
type ManualTestOptionsQuery struct {
	Keyword     string
	Limit       int
	SelectedIDs []string
}

// NormalizeManualTestOptionsQuery mirrors normalizeAccountManualTestOptionsQuery
// → normalizeProviderModelOptionQuery. A non-empty message means 400.
func NormalizeManualTestOptionsQuery(query map[string][]string) (ManualTestOptionsQuery, string) {
	normalized := ManualTestOptionsQuery{Limit: 50}
	if text := FirstQueryText(query["keyword"]); text != "" {
		normalized.Keyword = text
	}
	limitText := FirstQueryText(query["limit"])
	if limitText != "" {
		limit := 0
		strict := len(limitText) > 0
		for _, char := range limitText {
			if char < '0' || char > '9' {
				strict = false
				break
			}
			limit = limit*10 + int(char-'0')
		}
		if !strict || limit < 1 || limit > 50 {
			return ManualTestOptionsQuery{}, "limit 必须是 1 到 50 的整数"
		}
		normalized.Limit = limit
	}
	normalized.SelectedIDs = NormalizedQueryTextList(query["selectedIds"], query["selectedIds[]"])
	return normalized, ""
}

// FirstQueryText mirrors the first non-empty query value read
// （原根包私有函数 firstQueryText，子域化导出）.
func FirstQueryText(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// NormalizedQueryTextList mirrors the comma-split, dedupe, cap-50 list
// normalization（原根包私有函数 normalizedQueryTextList，子域化导出）.
func NormalizedQueryTextList(groups ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range groups {
		for _, value := range group {
			for _, item := range strings.Split(value, ",") {
				text := strings.TrimSpace(item)
				if text == "" || seen[text] {
					continue
				}
				seen[text] = true
				out = append(out, text)
				if len(out) >= 50 {
					return out
				}
			}
		}
	}
	return out
}

// ---- context reads (account-manual-test-context.repository.ts) ----

// ManualTestContextRow mirrors the manual-test context join row scan target
// （原根包私有类型 manualTestContextRow，类型随子域化导出）.
type ManualTestContextRow struct {
	viewAccountID             string
	factAccountID             string
	ownerSystemAccountID      string
	providerCode              string
	providerProtocolProfileID sql.NullString
	protocolCode              sql.NullString
	protocolVersion           sql.NullString
	accountType               string
	clientCompatibility       string
	healthCheckModel          string
	healthCheckEndpointMode   sql.NullString
	credentialsEncrypted      sql.NullString
}

// ScopedOwnerID mirrors scopedSystemAccountId: admins pass the filter through
// (empty = unscoped), users are pinned to themselves
// （原根包私有函数 scopedOwnerID，子域化导出）.
func ScopedOwnerID(access *accountscore.AccessScope) string {
	if access == nil {
		return ""
	}
	return access.ManageableID()
}

// FindManualTestContextRow mirrors the raw manual-test context read
// （原根包私有方法 findManualTestContextRow，根包测试经由 Store 转发访问）.
func (s *Service) FindManualTestContextRow(ctx context.Context, accountID string, access *accountscore.AccessScope, includeCredentials bool) (*ManualTestContextRow, error) {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return nil, nil
	}
	owner := ScopedOwnerID(access)
	restrictOwner := owner != ""
	credentialsColumn := ""
	if includeCredentials {
		credentialsColumn = `, COALESCE(source_accounts.credentials_encrypted, accounts.credentials_encrypted) AS credentials_encrypted`
	}
	query := `SELECT
		accounts.id,
		COALESCE(source_accounts.id, accounts.id),
		COALESCE(source_accounts.system_account_id, accounts.system_account_id),
		COALESCE(source_accounts.provider_code, accounts.provider_code),
		COALESCE(source_accounts.provider_protocol_profile_id, accounts.provider_protocol_profile_id),
		COALESCE(source_accounts.protocol_code, accounts.protocol_code),
		COALESCE(source_accounts.protocol_version, accounts.protocol_version),
		COALESCE(source_accounts.type, accounts.type),
		COALESCE(source_accounts.client_compatibility, accounts.client_compatibility),
		COALESCE(source_accounts.health_check_model, accounts.health_check_model),
		COALESCE(source_accounts.health_check_endpoint_mode, accounts.health_check_endpoint_mode)` +
		credentialsColumn + `
	FROM ` + s.store.Table("accounts") + ` accounts
	LEFT JOIN ` + s.store.Table("accounts") + ` source_accounts
		ON source_accounts.id = accounts.authorization_instance_source_account_id
		AND source_accounts.deleted_at IS NULL
	LEFT JOIN ` + s.store.Table("resource_authorizations") + ` authorizations
		ON authorizations.id = accounts.authorization_instance_authorization_id
	WHERE accounts.id = ?
		AND accounts.deleted_at IS NULL
		AND (
			accounts.authorization_instance_authorization_id IS NULL
			OR (
				authorizations.id IS NOT NULL
				AND authorizations.status IN ('active', 'paused', 'expired')
				AND source_accounts.id IS NOT NULL
			)
		)`
	args := []any{normalized}
	if restrictOwner {
		query += ` AND accounts.system_account_id = ?`
		args = append(args, owner)
	}
	query += ` LIMIT 1`

	var row ManualTestContextRow
	scan := func(target ...any) error {
		return s.store.DB().QueryRowContext(ctx, s.store.Bind(query), args...).Scan(target...)
	}
	var err error
	if includeCredentials {
		err = scan(&row.viewAccountID, &row.factAccountID, &row.ownerSystemAccountID, &row.providerCode,
			&row.providerProtocolProfileID, &row.protocolCode, &row.protocolVersion, &row.accountType,
			&row.clientCompatibility, &row.healthCheckModel, &row.healthCheckEndpointMode,
			&row.credentialsEncrypted)
	} else {
		err = scan(&row.viewAccountID, &row.factAccountID, &row.ownerSystemAccountID, &row.providerCode,
			&row.providerProtocolProfileID, &row.protocolCode, &row.protocolVersion, &row.accountType,
			&row.clientCompatibility, &row.healthCheckModel, &row.healthCheckEndpointMode)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindManualTestOptionsContext mirrors findAccountManualTestOptionsContextAsync.
func (s *Service) FindManualTestOptionsContext(ctx context.Context, accountID string, access *accountscore.AccessScope) (*ManualTestContext, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.FindManualTestContextRow(ctx, accountID, access, true)
	if err != nil || row == nil {
		return nil, err
	}
	if !row.credentialsEncrypted.Valid || row.credentialsEncrypted.String == "" {
		return nil, nil
	}
	var credentials accountscore.Credentials
	if err := accountscore.DecryptJSON(s.store.Secret(), row.credentialsEncrypted.String, &credentials); err != nil {
		return nil, nil
	}
	mappings, err := s.LoadTestAccountModelMappings(ctx, s.store.DB(), row.factAccountID, "")
	if err != nil {
		return nil, err
	}
	return s.manualTestContextFromRow(row, credentials, mappings), nil
}

// FindManualTestCapabilitiesContext mirrors
// findAccountManualTestCapabilitiesContextAsync (model-scoped mappings).
func (s *Service) FindManualTestCapabilitiesContext(ctx context.Context, accountID, modelID string, access *accountscore.AccessScope) (*ManualTestContext, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.FindManualTestContextRow(ctx, accountID, access, true)
	if err != nil || row == nil {
		return nil, err
	}
	if !row.credentialsEncrypted.Valid || row.credentialsEncrypted.String == "" {
		return nil, nil
	}
	var credentials accountscore.Credentials
	if err := accountscore.DecryptJSON(s.store.Secret(), row.credentialsEncrypted.String, &credentials); err != nil {
		return nil, nil
	}
	mappings, err := s.LoadTestAccountModelMappings(ctx, s.store.DB(), row.factAccountID, strings.TrimSpace(modelID))
	if err != nil {
		return nil, err
	}
	return s.manualTestContextFromRow(row, credentials, mappings), nil
}

func (s *Service) manualTestContextFromRow(row *ManualTestContextRow, credentials accountscore.Credentials, mappings []accountscore.ModelMapping) *ManualTestContext {
	return &ManualTestContext{
		ID:                        row.viewAccountID,
		FactAccountID:             row.factAccountID,
		OwnerSystemAccountID:      row.ownerSystemAccountID,
		ProviderCode:              row.providerCode,
		ProviderProtocolProfileID: row.providerProtocolProfileID.String,
		ProtocolCode:              row.protocolCode.String,
		ProtocolVersion:           row.protocolVersion.String,
		Type:                      row.accountType,
		ClientCompatibility:       row.clientCompatibility,
		HealthCheckModel:          row.healthCheckModel,
		HealthCheckEndpointMode:   row.healthCheckEndpointMode.String,
		SupportedEndpointModes:    SupportedEndpointModesFromCredentials(credentials),
		ModelMappings:             mappings,
	}
}

// SupportedEndpointModesFromCredentials mirrors
// accountSupportedEndpointModes(credentials.supported_endpoint_modes)
// （原根包私有函数，根包路由与测试经由转发访问）.
func SupportedEndpointModesFromCredentials(credentials accountscore.Credentials) []string {
	raw, ok := credentials["supported_endpoint_modes"]
	if !ok || raw == nil {
		return []string{}
	}
	list, ok := raw.([]any)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// LoadTestAccountModelMappings mirrors loadModelMappingsByAccountIdsAsync /
// loadModelMappingsForAccountModel (single account; empty model = all).
// Named apart from loadAccountModelMappings (patch.go), which carries the
// write-path projection
// （原根包私有方法 loadTestAccountModelMappings，根包测试经由 Store 转发访问）.
func (s *Service) LoadTestAccountModelMappings(ctx context.Context, q accountscore.Queryer, accountID, model string) ([]accountscore.ModelMapping, error) {
	query := `SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
		FROM ` + s.store.Table("account_model_mappings") + `
		WHERE account_id = ?`
	args := []any{accountID}
	if model != "" {
		query += ` AND source_model = ?`
		args = append(args, model)
	}
	query += ` ORDER BY source_endpoint_family ASC, source_model ASC`
	rows, err := q.QueryContext(ctx, s.store.Bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mappings := []accountscore.ModelMapping{}
	for rows.Next() {
		var mapping accountscore.ModelMapping
		var enabled sql.NullInt64
		if err := rows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily,
			&mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &enabled); err != nil {
			return nil, err
		}
		if enabled.Valid {
			value := enabled.Int64 == 1
			mapping.Enabled = &value
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

// ---- endpoint-mode resolution (account-test-endpoint-modes.ts) ----

// ManualTestModeSource carries the account fields
// AccountManualTestEndpointModes consumes
// （原根包私有类型 manualTestModeSource，字段随子域化导出）.
type ManualTestModeSource struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	AccountType               string
	ClientCompatibility       string
	HealthCheckEndpointMode   string
	SupportedEndpointModes    []string
	ModelMappings             []accountscore.ModelMapping
}

func (m ManualTestModeSource) predicate() accountscore.ProtocolPredicate {
	return accountscore.ProtocolPredicate{
		ProviderCode:              m.ProviderCode,
		ProtocolCode:              m.ProtocolCode,
		ProtocolVersion:           m.ProtocolVersion,
		ProviderProtocolProfileID: m.ProviderProtocolProfileID,
	}
}

func (m ManualTestModeSource) defaultContext() accountscore.ModeDefaultContext {
	return accountscore.ModeDefaultContext{
		ProviderCode:              m.ProviderCode,
		AccountType:               m.AccountType,
		ProtocolCode:              m.ProtocolCode,
		ProtocolVersion:           m.ProtocolVersion,
		ProviderProtocolProfileID: m.ProviderProtocolProfileID,
		ClientCompatibility:       m.ClientCompatibility,
	}
}

// NormalizeOpenAIEndpointModesForRuntime (runtime fallback: non-array or
// empty → defaults, unknown values dropped)
// （原根包私有函数，根包测试经由转发访问）.
func NormalizeOpenAIEndpointModesForRuntime(value []string, defaults accountscore.ModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !accountscore.IsOpenAIEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return accountscore.DefaultOpenAIEndpointModes(defaults)
	}
	return output
}

// NormalizeAnthropicEndpointModesForRuntime mirrors the anthropic runtime
// normalization（原根包私有函数，根包测试经由转发访问）.
func NormalizeAnthropicEndpointModesForRuntime(value []string, defaults accountscore.ModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !accountscore.IsAnthropicEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return accountscore.DefaultAnthropicEndpointModes(defaults)
	}
	return output
}

// NormalizeGeminiEndpointModesForRuntime mirrors the gemini runtime
// normalization（原根包私有函数，根包测试经由转发访问）.
func NormalizeGeminiEndpointModesForRuntime(value []string, defaults accountscore.ModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !accountscore.IsGeminiEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return accountscore.DefaultGeminiEndpointModes(defaults)
	}
	return output
}

// NormalizeHybridEndpointModesForRuntime mirrors the hybrid runtime
// normalization（原根包私有函数，根包测试经由转发访问）.
func NormalizeHybridEndpointModesForRuntime(value []string) []string {
	known := accountscore.StringSet(accountscore.HybridEndpointModeValues)
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !known[item] || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return append([]string{}, accountscore.HybridEndpointModeValues...)
	}
	return output
}

// AccountManualTestEndpointModes mirrors the same-named helper
// （原根包私有函数，根包测试经由转发访问）.
func AccountManualTestEndpointModes(source ManualTestModeSource) []string {
	enabled := source.SupportedEndpointModes
	switch {
	case accountscore.IsHybridProviderCodeToken(source.ProviderCode):
		enabled = NormalizeHybridEndpointModesForRuntime(source.SupportedEndpointModes)
	case accountscore.IsAnthropicProtocolProfileOf(source.predicate()):
		enabled = NormalizeAnthropicEndpointModesForRuntime(source.SupportedEndpointModes, source.defaultContext())
	case accountscore.IsGeminiProtocolProfileOf(source.predicate()):
		enabled = NormalizeGeminiEndpointModesForRuntime(source.SupportedEndpointModes, source.defaultContext())
	case accountscore.IsOpenAIProtocolProfileOf(source.predicate()):
		enabled = NormalizeOpenAIEndpointModesForRuntime(source.SupportedEndpointModes, source.defaultContext())
	default:
		enabled = []string{}
	}
	enabledSet := map[string]bool{}
	for _, mode := range enabled {
		enabledSet[mode] = true
	}
	out := []string{}
	for _, mode := range AccountTestEndpointModeOrder(source) {
		if enabledSet[mode] {
			out = append(out, mode)
		}
	}
	return out
}

// AccountTestEndpointModeOrder mirrors the same-named helper
// （原根包私有函数，根包测试经由转发访问）.
func AccountTestEndpointModeOrder(source ManualTestModeSource) []string {
	defaultMode := source.HealthCheckEndpointMode
	unique := func(modes ...string) []string {
		seen := map[string]bool{}
		out := []string{}
		for _, mode := range modes {
			if mode == "" || seen[mode] {
				continue
			}
			seen[mode] = true
			out = append(out, mode)
		}
		return out
	}
	switch {
	case accountscore.IsHybridProviderCodeToken(source.ProviderCode):
		return unique(defaultMode,
			"chat_json", "chat_sse", "responses_json", "responses_sse",
			"messages_json", "messages_sse", "generate_content_json", "generate_content_sse")
	case accountscore.IsAnthropicProtocolProfileOf(source.predicate()):
		return unique(defaultMode, "messages_json", "messages_sse")
	case accountscore.IsGeminiProtocolProfileOf(source.predicate()):
		return unique(defaultMode, "interactions_json", "interactions_sse", "generate_content_json", "generate_content_sse")
	case source.AccountType == "oauth":
		return unique(defaultMode, "responses_json", "responses_sse")
	default:
		return unique(defaultMode, "chat_sse", "responses_sse", "chat_json", "responses_json")
	}
}

// ---- model mapping resolution (openai-v1/model-mapping.ts subset) ----

// GeminiOpenAIChatV1BetaProfile mirrors the pinned gemini openai-chat profile
// token（原根包私有常量，子域化导出）.
const GeminiOpenAIChatV1BetaProfile = "profile_gemini_openai_chat_v1beta"

// ResolvedTestModelMapping mirrors the resolved upstream mapping projection
// （原根包私有类型 resolvedTestModelMapping，字段随子域化导出）.
type ResolvedTestModelMapping struct {
	UpstreamModel          string
	UpstreamEndpointFamily string
}

// IsTestMappingSourceFamily mirrors the source family gate
// （原根包私有函数，根包测试经由转发访问）.
func IsTestMappingSourceFamily(value string) bool {
	switch value {
	case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
		return true
	}
	return false
}

// IsOpenAIModelMappingRuntimeConversionSupported mirrors the same-named
// helper (model-mapping.ts; also ported in gatewayopenai/mapping.go)
// （原根包私有函数，根包测试经由转发访问）.
func IsOpenAIModelMappingRuntimeConversionSupported(mapping accountscore.ModelMapping, source ManualTestModeSource) bool {
	src := mapping.SourceEndpointFamily
	upstream := mapping.UpstreamEndpointFamily
	if src == upstream || (src == "stream_generate_content" && upstream == "generate_content") {
		return true
	}
	if src == "responses" && upstream == "chat_completions" && accountscore.IsOpenAIProtocolProfileOf(source.predicate()) {
		return true
	}
	if !accountscore.IsHybridProviderCodeToken(source.ProviderCode) {
		return false
	}
	switch {
	case src == "responses" && upstream == "chat_completions",
		src == "messages" && upstream == "chat_completions",
		(src == "generate_content" || src == "stream_generate_content") && upstream == "chat_completions",
		src == "chat_completions" && upstream == "messages",
		src == "responses" && upstream == "messages",
		(src == "generate_content" || src == "stream_generate_content") && upstream == "messages",
		src == "chat_completions" && upstream == "generate_content",
		src == "responses" && upstream == "generate_content",
		src == "messages" && upstream == "generate_content":
		return true
	}
	return false
}

// ResolveTestAccountModelMapping mirrors resolveOpenAIAccountModelMapping for
// the options path (Go ModelMapping rows never carry the runtime-only
// explicit_hybrid_route source, so that guard has no DB-reachable input)
// （原根包私有函数，根包测试经由转发访问）.
func ResolveTestAccountModelMapping(source ManualTestModeSource, model, sourceFamily string) *ResolvedTestModelMapping {
	if model == "" || sourceFamily == "" || !IsTestMappingSourceFamily(sourceFamily) {
		return nil
	}
	if source.ProviderProtocolProfileID == GeminiOpenAIChatV1BetaProfile && sourceFamily == "messages" {
		return nil
	}
	var mapping *accountscore.ModelMapping
	for index := range source.ModelMappings {
		candidate := source.ModelMappings[index]
		if candidate.Enabled != nil && !*candidate.Enabled {
			continue
		}
		if candidate.SourceModel == model && candidate.SourceEndpointFamily == sourceFamily {
			mapping = &candidate
			break
		}
	}
	if mapping == nil || (mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily) {
		return nil
	}
	if !IsOpenAIModelMappingRuntimeConversionSupported(*mapping, source) {
		return nil
	}
	return &ResolvedTestModelMapping{
		UpstreamModel:          mapping.UpstreamModel,
		UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
	}
}

// ---- test catalog reads ----

// Catalog scope tokens（原根包私有常量，子域化导出）.
const (
	CatalogScopeBuiltIn  = "built_in"
	CatalogScopeGlobal   = "global"
	CatalogScopePersonal = "personal"
)

// TestOptionRow mirrors ProviderModelOptionRow (the projection the options
// merge consumes)
// （原根包私有类型 testOptionRow，字段随子域化导出）.
type TestOptionRow struct {
	Provider              string
	Model                 string
	Scope                 string
	Mode                  string
	ReleaseDate           string
	SupportedAPIProtocols []string
}

// TestCatalogItem mirrors ProviderModelTestCatalogItem (protocolsOnly
// projection: model + mode + supported protocols)
// （原根包私有类型 testCatalogItem，字段随子域化导出）.
type TestCatalogItem struct {
	Model                 string
	Mode                  string
	SupportedAPIProtocols []string
}

// TestCatalogSourceCodes mirrors providerModelSourceCodesAsync for the
// providerCode branch (modelCatalogSourceProviderCodesAsync)
// （原根包私有方法 testCatalogSourceCodes，根包测试经由 Store 转发访问）.
func (s *Service) TestCatalogSourceCodes(ctx context.Context, providerCode string) ([]string, error) {
	normalized := accountscore.NormalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}, nil
	}
	if normalized == accountscore.HybridProviderCode {
		codes := []string{}
		for _, pair := range [][2]string{
			{accountscore.OpenAIProtocolCode, accountscore.OpenAIProtocolVersion},
			{accountscore.AnthropicProviderCode, accountscore.AnthropicProtocolVersion},
			{accountscore.GeminiProviderCode, accountscore.GeminiProtocolVersion},
		} {
			list, err := s.ProtocolProviderCodes(ctx, pair[0], pair[1])
			if err != nil {
				return nil, err
			}
			for _, code := range list {
				token := accountscore.NormalizeProviderToken(code)
				if token == "" || token == accountscore.HybridProviderCode {
					continue
				}
				codes = append(codes, token)
			}
		}
		return DedupeTestStrings(codes), nil
	}
	if normalized != accountscore.OpenAICompatibleProviderCode {
		return []string{normalized}, nil
	}
	list, err := s.ProtocolProviderCodes(ctx, accountscore.OpenAIProtocolCode, accountscore.OpenAIProtocolVersion)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, code := range list {
		token := accountscore.NormalizeProviderToken(code)
		if token == "" || token == normalized {
			continue
		}
		codes = append(codes, token)
	}
	return DedupeTestStrings(append(codes, normalized)), nil
}

// DedupeTestStrings keeps first-occurrence order
// （原根包私有函数 dedupeTestStrings，根包测试经由转发访问）.
func DedupeTestStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// ProtocolProviderCodes mirrors listOpenAI/Anthropic/GeminiProtocolProviderCodesAsync:
// enabled providers with an enabled profile on the protocol pair
// （原根包私有方法 protocolProviderCodes，根包测试经由 Store 转发访问）.
func (s *Service) ProtocolProviderCodes(ctx context.Context, protocolCode, protocolVersion string) ([]string, error) {
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT DISTINCT p.code
		FROM `+s.store.Table("providers")+` p
		JOIN `+s.store.Table("provider_protocol_profiles")+` ppp ON ppp.provider_code = p.code
		WHERE p.enabled = 1 AND ppp.enabled = 1
			AND ppp.protocol_code = ? AND ppp.protocol_version = ?
		ORDER BY p.code ASC`), protocolCode, protocolVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes := []string{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// testCatalogAvailability mirrors the availability filter shared by the
// test-catalog reads (active + visible + not shutdown).
func (s *Service) testCatalogAvailability() string {
	// catalog_visible 在 PostgreSQL 为 boolean、SQLite 为 integer，谓词按方言生成。
	visible := "CAST(catalog_visible AS integer) = 1"
	if s.store.PG() {
		visible = "catalog_visible = TRUE"
	}
	return ` AND status = 'active'
		AND ` + visible + `
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ` + s.TestTodayText() + `)`
}

// TestTodayText renders the SQL "today" literal (SQLite date('now') vs
// PostgreSQL CURRENT_DATE::text), mirroring the providers slice helper
// （原根包自由函数 testTodayText(s *Store)，子域化改为 Service 方法）.
func (s *Service) TestTodayText() string {
	if s.store.PG() {
		return "CURRENT_DATE::text"
	}
	return "date('now')"
}

// NormalizeTestProviderCodeList mirrors the provider-code list normalization
// （原根包私有函数，根包测试经由转发访问）.
func NormalizeTestProviderCodeList(codes []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, code := range codes {
		token := accountscore.NormalizeProviderToken(code)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// TestCatalogBuiltInSourceCodes mirrors modelCatalogBuiltInSourceProviderCodes
// （原根包私有函数，根包测试经由转发访问）.
func TestCatalogBuiltInSourceCodes(providerCode string, sourceCodes []string) []string {
	if accountscore.NormalizeProviderToken(providerCode) != accountscore.OpenAICompatibleProviderCode {
		return sourceCodes
	}
	codes := []string{}
	for _, code := range sourceCodes {
		if accountscore.NormalizeProviderToken(code) == accountscore.OpenAICompatibleProviderCode {
			continue
		}
		codes = append(codes, code)
	}
	return codes
}

// ListBuiltInTestCatalogOptions mirrors listBuiltInProviderModelOptionsAsync
// （原根包私有方法 listBuiltInTestCatalogOptions，根包测试经由 Store 转发访问）.
func (s *Service) ListBuiltInTestCatalogOptions(ctx context.Context, providerCodes []string, query ManualTestOptionsQuery) ([]TestOptionRow, error) {
	codes := NormalizeTestProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []TestOptionRow{}, nil
	}
	clauses := []string{"provider_code IN (" + accountscore.Placeholders(len(codes)) + ")"}
	args := append([]any{}, accountscore.AnySlice(codes)...)
	args = s.appendTestOptionTextFilter(&clauses, args, query)
	args = append(args, selectedOrderArgs(query)...)
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT provider_code, model, mode, release_date, supported_api_protocols_json
		FROM `+s.store.Table("provider_model_catalog")+`
		WHERE `+strings.Join(clauses, " AND ")+s.testCatalogAvailability()+`
		ORDER BY `+testSelectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, lower(model) ASC, provider_code ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTestOptionRows(rows, CatalogScopeBuiltIn)
}

// ListCustomTestCatalogOptions mirrors listCustomProviderModelOptionsAsync
// （原根包私有方法 listCustomTestCatalogOptions，根包测试经由 Store 转发访问）.
func (s *Service) ListCustomTestCatalogOptions(ctx context.Context, providerCodes []string, systemAccountID string, query ManualTestOptionsQuery) ([]TestOptionRow, error) {
	codes := NormalizeTestProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []TestOptionRow{}, nil
	}
	clauses := []string{"provider_code IN (" + accountscore.Placeholders(len(codes)) + ")"}
	args := append([]any{}, accountscore.AnySlice(codes)...)
	if trimmed := strings.TrimSpace(systemAccountID); trimmed != "" {
		clauses = append(clauses, "((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, trimmed)
	} else {
		clauses = append(clauses, "scope = 'global' AND system_account_id IS NULL")
	}
	args = s.appendTestOptionTextFilter(&clauses, args, query)
	args = append(args, selectedOrderArgs(query)...)
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT provider_code, model, scope, mode, release_date, supported_api_protocols_json
		FROM `+s.store.Table("custom_provider_models")+`
		WHERE `+strings.Join(clauses, " AND ")+` AND status = 'active'
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+s.TestTodayText()+`)
		ORDER BY `+testSelectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, lower(model) ASC, provider_code ASC, scope ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTestOptionRows(rows, "")
}

// appendTestOptionTextFilter mirrors the WHERE side of the options queries:
// the (selectedIds OR keyword LIKE) clause is pushed only when a keyword is
// present.
func (s *Service) appendTestOptionTextFilter(clauses *[]string, args []any, query ManualTestOptionsQuery) []any {
	if query.Keyword == "" {
		return args
	}
	parts := []string{}
	if len(query.SelectedIDs) > 0 {
		parts = append(parts, "model IN ("+accountscore.Placeholders(len(query.SelectedIDs))+")")
		for _, id := range query.SelectedIDs {
			args = append(args, id)
		}
	}
	parts = append(parts, "lower(model) LIKE ?")
	args = append(args, "%"+strings.ToLower(query.Keyword)+"%")
	*clauses = append(*clauses, "("+strings.Join(parts, " OR ")+")")
	return args
}

func testSelectedOrderClause(query ManualTestOptionsQuery) string {
	if len(query.SelectedIDs) == 0 {
		return ""
	}
	return "CASE WHEN model IN (" + accountscore.Placeholders(len(query.SelectedIDs)) + ") THEN 0 ELSE 1 END, "
}

func selectedOrderArgs(query ManualTestOptionsQuery) []any {
	out := []any{}
	for _, id := range query.SelectedIDs {
		out = append(out, id)
	}
	return out
}

func scanTestOptionRows(rows *sql.Rows, fixedScope string) ([]TestOptionRow, error) {
	items := []TestOptionRow{}
	for rows.Next() {
		var row TestOptionRow
		var mode, releaseDate, protocols sql.NullString
		var scope sql.NullString
		var err error
		if fixedScope != "" {
			err = rows.Scan(&row.Provider, &row.Model, &mode, &releaseDate, &protocols)
			row.Scope = fixedScope
		} else {
			err = rows.Scan(&row.Provider, &row.Model, &scope, &mode, &releaseDate, &protocols)
			row.Scope = scope.String
		}
		if err != nil {
			return nil, err
		}
		row.Mode = mode.String
		row.ReleaseDate = releaseDate.String
		row.SupportedAPIProtocols = TestParseJSONArray(protocols)
		items = append(items, row)
	}
	return items, rows.Err()
}

// FindTestCatalogItem mirrors findProviderModelTestCatalogItemAsync
// (protocolsOnly projection: model + mode + supported protocols; the winner
// of the scope-priority merge ordered by the test-catalog comparator)
// （原根包私有方法 findTestCatalogItem，根包测试经由 Store 转发访问）.
func (s *Service) FindTestCatalogItem(ctx context.Context, providerCode, systemAccountID, model string) (*TestCatalogItem, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil, nil
	}
	sourceCodes, err := s.TestCatalogSourceCodes(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return nil, nil
	}
	builtInCodes := TestCatalogBuiltInSourceCodes(providerCode, sourceCodes)
	availability := s.testCatalogAvailability()
	candidates, err := s.CollectTestCatalogCandidates(ctx, builtInCodes, sourceCodes, trimmed, availability)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	// mergeProviderModelTestCatalogItems: scope priority first, later wins
	// ties; then compareProviderModelTestCatalogItems (release date desc).
	winner := candidates[0]
	for _, item := range candidates {
		if TestCatalogScopePriority(item.scope) >= TestCatalogScopePriority(winner.scope) {
			winner = item
		}
	}
	for _, item := range candidates {
		if TestCatalogScopePriority(item.scope) != TestCatalogScopePriority(winner.scope) {
			continue
		}
		leftDate, rightDate := TestCatalogReleaseDate(item.releaseDate), TestCatalogReleaseDate(winner.releaseDate)
		if leftDate != "" && rightDate != "" && leftDate != rightDate && leftDate > rightDate {
			winner = item
		}
	}
	return &winner.item, nil
}

// TestCatalogCandidate mirrors one catalog candidate of the merge
// （原根包私有类型 testCatalogCandidate，类型随子域化导出）.
type TestCatalogCandidate struct {
	item        TestCatalogItem
	scope       string
	releaseDate string
}

// CollectTestCatalogCandidates mirrors the built-in + custom catalog sweep
// （原根包私有方法 collectTestCatalogCandidates，根包测试经由 Store 转发访问）.
func (s *Service) CollectTestCatalogCandidates(ctx context.Context, builtInCodes, sourceCodes []string, model, availability string) ([]TestCatalogCandidate, error) {
	candidates := []TestCatalogCandidate{}
	if codes := NormalizeTestProviderCodeList(builtInCodes); len(codes) > 0 {
		rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT model, mode, supported_api_protocols_json, release_date
			FROM `+s.store.Table("provider_model_catalog")+`
			WHERE provider_code IN (`+accountscore.Placeholders(len(codes))+`) AND model = ?`+availability+`
			ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC`), append(accountscore.AnySlice(codes), model)...)
		if err != nil {
			return nil, err
		}
		collected, err := scanTestCandidates(rows, CatalogScopeBuiltIn, false)
		rows.Close()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, collected...)
	}
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT model, mode, supported_api_protocols_json, release_date, scope
		FROM `+s.store.Table("custom_provider_models")+`
		WHERE provider_code IN (`+accountscore.Placeholders(len(sourceCodes))+`) AND model = ? AND status = 'active'
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+s.TestTodayText()+`)
		ORDER BY provider_code ASC, scope ASC, lower(model) ASC, id ASC`),
		append(accountscore.AnySlice(sourceCodes), model)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	collected, err := scanTestCandidates(rows, "", true)
	if err != nil {
		return nil, err
	}
	return append(candidates, collected...), nil
}

func scanTestCandidates(rows *sql.Rows, fixedScope string, withScope bool) ([]TestCatalogCandidate, error) {
	candidates := []TestCatalogCandidate{}
	for rows.Next() {
		var (
			item        TestCatalogItem
			mode        sql.NullString
			protocols   sql.NullString
			releaseDate sql.NullString
			scope       sql.NullString
		)
		var err error
		if withScope {
			err = rows.Scan(&item.Model, &mode, &protocols, &releaseDate, &scope)
		} else {
			err = rows.Scan(&item.Model, &mode, &protocols, &releaseDate)
		}
		if err != nil {
			return nil, err
		}
		item.Mode = mode.String
		item.SupportedAPIProtocols = TestParseJSONArray(protocols)
		entry := TestCatalogCandidate{item: item, releaseDate: releaseDate.String, scope: fixedScope}
		if withScope {
			entry.scope = scope.String
		}
		candidates = append(candidates, entry)
	}
	return candidates, rows.Err()
}

// TestCatalogScopePriority mirrors the scope priority of the merge
// （原根包私有函数，根包测试经由转发访问）.
func TestCatalogScopePriority(scope string) int {
	switch scope {
	case CatalogScopePersonal:
		return 3
	case CatalogScopeGlobal:
		return 2
	}
	return 1
}

// TestCatalogReleaseDate normalizes the release-date prefix
// （原根包私有函数，根包测试经由转发访问）.
func TestCatalogReleaseDate(value string) string {
	normalized := strings.TrimSpace(value)
	if len(normalized) >= 10 {
		normalized = normalized[:10]
	}
	if len(normalized) != 10 {
		return ""
	}
	return normalized
}

// ---- options assembly (account-test-options.service.ts) ----

// AccountManualTestOptions mirrors accountManualTestOptionsAsync.
func (s *Service) AccountManualTestOptions(ctx context.Context, account *ManualTestContext, query ManualTestOptionsQuery) ([]ManualTestOption, error) {
	systemAccountID := account.OwnerSystemAccountID
	if systemAccountID == "" {
		return nil, &accountscore.ValidationError{Message: "账户归属数据异常，无法读取测试模型"}
	}
	selectedIDs := append([]string{}, query.SelectedIDs...)
	if healthModel := strings.TrimSpace(account.HealthCheckModel); healthModel != "" {
		if !accountscore.ContainsString(selectedIDs, healthModel) {
			selectedIDs = append(selectedIDs, healthModel)
		}
	}
	sourceCodes, err := s.TestCatalogSourceCodes(ctx, account.ProviderCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []ManualTestOption{}, nil
	}
	builtInCodes := TestCatalogBuiltInSourceCodes(account.ProviderCode, sourceCodes)
	optionQuery := ManualTestOptionsQuery{Keyword: query.Keyword, Limit: query.Limit, SelectedIDs: selectedIDs}
	builtIn, err := s.ListBuiltInTestCatalogOptions(ctx, builtInCodes, optionQuery)
	if err != nil {
		return nil, err
	}
	custom, err := s.ListCustomTestCatalogOptions(ctx, sourceCodes, systemAccountID, optionQuery)
	if err != nil {
		return nil, err
	}
	source := account.ModeSource()
	eligible := []TestOptionRow{}
	for _, row := range append(builtIn, custom...) {
		if IsAccountManualTestModel(TestCatalogItem{Model: row.Model, Mode: row.Mode, SupportedAPIProtocols: row.SupportedAPIProtocols}, source) {
			eligible = append(eligible, row)
		}
	}
	options := MergeTestOptionRows(eligible, optionQuery)
	cache := map[string]*TestCatalogItem{}
	resolved := []ManualTestOption{}
	for _, option := range options {
		modes, err := s.manualTestEndpointModesForTargetModel(ctx, source, TestCatalogItem{
			Model:                 option.Model,
			SupportedAPIProtocols: option.Protocols,
		}, systemAccountID, cache)
		if err != nil {
			return nil, err
		}
		if len(modes) > 0 {
			resolved = append(resolved, ManualTestOption{ID: option.Model, Name: option.Model, TestEndpointModes: modes})
		}
	}
	return resolved, nil
}

// ModeSource projects the context onto the endpoint-mode resolution input
// （原根包私有方法 modeSource，根包路由经由转发访问）.
func (a *ManualTestContext) ModeSource() ManualTestModeSource {
	return ManualTestModeSource{
		ProviderCode:              a.ProviderCode,
		ProviderProtocolProfileID: a.ProviderProtocolProfileID,
		ProtocolCode:              a.ProtocolCode,
		ProtocolVersion:           a.ProtocolVersion,
		AccountType:               a.Type,
		ClientCompatibility:       a.ClientCompatibility,
		HealthCheckEndpointMode:   a.HealthCheckEndpointMode,
		SupportedEndpointModes:    a.SupportedEndpointModes,
		ModelMappings:             a.ModelMappings,
	}
}

// MergedTestOption mirrors the merged option row of the options assembly
// （原根包私有类型 mergedTestOption，字段随子域化导出）.
type MergedTestOption struct {
	Model     string
	Protocols []string
}

// MergeTestOptionRows mirrors mergeProviderModelOptionRows (keyword/selected
// filter, dedupe with scope priority, release-date ordering, selected-aware
// limit)
// （原根包私有函数，根包测试经由转发访问）.
func MergeTestOptionRows(rows []TestOptionRow, query ManualTestOptionsQuery) []MergedTestOption {
	selected := map[string]bool{}
	for _, id := range query.SelectedIDs {
		selected[id] = true
	}
	keyword := strings.ToLower(query.Keyword)
	byModel := map[string]TestOptionRow{}
	order := []string{}
	for _, row := range rows {
		row.Provider = strings.TrimSpace(row.Provider)
		row.Model = strings.TrimSpace(row.Model)
		if row.Provider == "" || row.Model == "" {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(row.Model), keyword) && !selected[row.Model] {
			continue
		}
		existing, ok := byModel[row.Model]
		if !ok {
			order = append(order, row.Model)
			byModel[row.Model] = row
			continue
		}
		if OptionScopePriorityValue(row.Scope) > OptionScopePriorityValue(existing.Scope) {
			byModel[row.Model] = row
		}
	}
	sort.SliceStable(order, func(left, right int) bool {
		leftRow, rightRow := byModel[order[left]], byModel[order[right]]
		leftDate, rightDate := NormalizedOptionReleaseDate(leftRow.ReleaseDate), NormalizedOptionReleaseDate(rightRow.ReleaseDate)
		if leftDate != "" && rightDate != "" && leftDate != rightDate {
			return leftDate > rightDate
		}
		if leftDate != "" && rightDate == "" {
			return true
		}
		if leftDate == "" && rightDate != "" {
			return false
		}
		if cmp := strings.Compare(leftRow.Model, rightRow.Model); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(leftRow.Provider, rightRow.Provider) < 0
	})
	visible := map[string]bool{}
	for id := range selected {
		visible[id] = true
	}
	admitted := 0
	for _, model := range order {
		if visible[model] || admitted >= query.Limit {
			continue
		}
		visible[model] = true
		admitted++
	}
	options := []MergedTestOption{}
	for _, model := range order {
		if !visible[model] {
			continue
		}
		options = append(options, MergedTestOption{Model: model, Protocols: byModel[model].SupportedAPIProtocols})
	}
	return options
}

// OptionScopePriorityValue mirrors the option scope priority
// （原根包私有函数，根包测试经由转发访问）.
func OptionScopePriorityValue(scope string) int {
	switch scope {
	case CatalogScopePersonal:
		return 3
	case CatalogScopeGlobal:
		return 2
	}
	return 1
}

// NormalizedOptionReleaseDate validates the YYYY-MM-DD prefix
// （原根包私有函数，根包测试经由转发访问）.
func NormalizedOptionReleaseDate(value string) string {
	normalized := strings.TrimSpace(value)
	if len(normalized) > 10 {
		normalized = normalized[:10]
	}
	if len(normalized) != 10 || normalized[4] != '-' || normalized[7] != '-' {
		return ""
	}
	for index, char := range normalized {
		if index == 4 || index == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return ""
		}
	}
	month := (int(normalized[5]-'0') * 10) + int(normalized[6]-'0')
	day := (int(normalized[8]-'0') * 10) + int(normalized[9]-'0')
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return normalized
}

// AccountManualTestModelCapabilities mirrors
// accountManualTestModelCapabilitiesAsync.
func (s *Service) AccountManualTestModelCapabilities(ctx context.Context, account *ManualTestContext, modelInput string) (*ManualTestModelCapabilities, error) {
	model := strings.TrimSpace(modelInput)
	if model == "" {
		return nil, &accountscore.ValidationError{Message: "请选择测试模型"}
	}
	systemAccountID := account.OwnerSystemAccountID
	if systemAccountID == "" {
		return nil, &accountscore.ValidationError{Message: "账户归属数据异常，无法读取测试模型"}
	}
	item, err := s.FindTestCatalogItem(ctx, account.ProviderCode, systemAccountID, model)
	if err != nil {
		return nil, err
	}
	source := account.ModeSource()
	if item == nil || !IsAccountManualTestModel(*item, source) {
		return nil, &accountscore.ValidationError{Message: fmt.Sprintf("模型不在当前账户供应商可用目录中：%s", model)}
	}
	modes, err := s.manualTestEndpointModesForTargetModel(ctx, source, *item, systemAccountID, map[string]*TestCatalogItem{})
	if err != nil {
		return nil, err
	}
	if len(modes) == 0 {
		return nil, &accountscore.ValidationError{Message: "账户上游接口能力中没有可用于连接测试的请求形态"}
	}
	return &ManualTestModelCapabilities{ID: item.Model, TestEndpointModes: modes}, nil
}

// ResolveAccountManualTestSelection mirrors resolveAccountManualTestSelectionAsync.
func (s *Service) ResolveAccountManualTestSelection(ctx context.Context, account *ManualTestContext, modelInput, testEndpointMode string) (model, resolvedMode string, err error) {
	model = strings.TrimSpace(modelInput)
	if model == "" {
		return "", "", &accountscore.ValidationError{Message: "请选择测试模型"}
	}
	option, err := s.AccountManualTestModelCapabilities(ctx, account, model)
	if err != nil {
		return "", "", err
	}
	resolvedMode = testEndpointMode
	if resolvedMode == "" && len(option.TestEndpointModes) > 0 {
		resolvedMode = option.TestEndpointModes[0]
	}
	if resolvedMode == "" || !accountscore.ContainsString(option.TestEndpointModes, resolvedMode) {
		requested := testEndpointMode
		if requested == "" {
			requested = "未选择"
		}
		return "", "", &accountscore.ValidationError{Message: fmt.Sprintf("模型 %s 不支持本次检查协议：%s", model, requested)}
	}
	return model, resolvedMode, nil
}

// manualTestEndpointModesForTargetModel mirrors
// accountManualTestEndpointModesForTargetModelAsync.
func (s *Service) manualTestEndpointModesForTargetModel(ctx context.Context, source ManualTestModeSource, item TestCatalogItem, systemAccountID string, cache map[string]*TestCatalogItem) ([]string, error) {
	out := []string{}
	for _, mode := range AccountManualTestEndpointModes(source) {
		if mode == "interactions_json" || mode == "interactions_sse" {
			if testModelSupportsProtocol(item, "interactions") {
				out = append(out, mode)
			}
			continue
		}
		sourceFamily, err := EndpointModeProtocolFamily(mode)
		if err != nil {
			return nil, err
		}
		mapping := ResolveTestAccountModelMapping(source, item.Model, sourceFamily)
		if mapping == nil {
			if testModelSupportsProtocol(item, sourceFamily) {
				out = append(out, mode)
			}
			continue
		}
		upstream := cache[mapping.UpstreamModel]
		if upstream == nil {
			loaded, err := s.FindTestCatalogItem(ctx, source.ProviderCode, systemAccountID, mapping.UpstreamModel)
			if err != nil {
				return nil, err
			}
			cache[mapping.UpstreamModel] = loaded
			upstream = loaded
		}
		if upstream != nil && testModelSupportsProtocol(*upstream, mapping.UpstreamEndpointFamily) {
			out = append(out, mode)
		}
	}
	if testModelSupportsImagesProtocol(item, source) {
		out = append(out, "images_json")
	}
	return out, nil
}

// EndpointModeProtocolFamily mirrors endpointModeProtocol
// （原根包私有函数，根包测试经由转发访问）.
func EndpointModeProtocolFamily(mode string) (string, error) {
	switch mode {
	case "images_json":
		return "", errors.New("图片生成测试不使用文本模型映射协议")
	case "chat_json", "chat_sse":
		return "chat_completions", nil
	case "responses_json", "responses_sse":
		return "responses", nil
	case "messages_json", "messages_sse":
		return "messages", nil
	case "generate_content_sse":
		return "stream_generate_content", nil
	default:
		return "generate_content", nil
	}
}

func testModelSupportsProtocol(item TestCatalogItem, protocol string) bool {
	protocols := item.SupportedAPIProtocols
	return len(protocols) == 0 || accountscore.ContainsString(protocols, protocol)
}

func testModelSupportsImagesProtocol(item TestCatalogItem, source ManualTestModeSource) bool {
	return source.AccountType == "api_key" &&
		accountscore.IsOpenAIProtocolProfileOf(source.predicate()) &&
		accountscore.ContainsString(item.SupportedAPIProtocols, "images")
}

// IsAccountManualTestModel mirrors the same-named helper
// （原根包私有函数，根包测试经由转发访问）.
func IsAccountManualTestModel(item TestCatalogItem, source ManualTestModeSource) bool {
	if strings.ToLower(strings.TrimSpace(item.Mode)) == "audio" {
		return false
	}
	if HasEnabledTestModelMapping(source.ModelMappings, item.Model) {
		return true
	}
	protocols := item.SupportedAPIProtocols
	if len(protocols) == 0 {
		mode := strings.ToLower(strings.TrimSpace(item.Mode))
		return mode != "image_generation" && mode != "image"
	}
	switch {
	case accountscore.IsHybridProviderCodeToken(source.ProviderCode):
		for _, protocol := range protocols {
			switch protocol {
			case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
				return true
			case "images":
				if source.AccountType == "api_key" {
					return true
				}
			}
		}
		return false
	case accountscore.IsOpenAIProtocolProfileOf(source.predicate()):
		for _, protocol := range protocols {
			switch protocol {
			case "chat_completions", "responses":
				return true
			case "images":
				if source.AccountType == "api_key" {
					return true
				}
			}
		}
		return false
	case accountscore.IsAnthropicProtocolProfileOf(source.predicate()):
		return accountscore.ContainsString(protocols, "messages")
	case accountscore.IsGeminiProtocolProfileOf(source.predicate()):
		for _, protocol := range protocols {
			if protocol == "generate_content" || protocol == "stream_generate_content" || protocol == "interactions" {
				return true
			}
		}
		return false
	}
	return false
}

// HasEnabledTestModelMapping mirrors the enabled-mapping gate
// （原根包私有函数，根包测试经由转发访问）.
func HasEnabledTestModelMapping(mappings []accountscore.ModelMapping, model string) bool {
	for _, mapping := range mappings {
		if mapping.Enabled != nil && !*mapping.Enabled {
			continue
		}
		if mapping.SourceModel == model {
			return true
		}
	}
	return false
}

// TestParseJSONArray decodes a JSON string column into the string list
// （原根包私有函数 testParseJSONArray，根包测试经由转发访问）.
func TestParseJSONArray(value sql.NullString) []string {
	out := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return out
	}
	var raw []any
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return out
	}
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
