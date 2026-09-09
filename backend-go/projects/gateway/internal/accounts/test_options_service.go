package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	ModelMappings           []ModelMapping
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
	if text := firstQueryText(query["keyword"]); text != "" {
		normalized.Keyword = text
	}
	limitText := firstQueryText(query["limit"])
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
	normalized.SelectedIDs = normalizedQueryTextList(query["selectedIds"], query["selectedIds[]"])
	return normalized, ""
}

func firstQueryText(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func normalizedQueryTextList(groups ...[]string) []string {
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

type manualTestContextRow struct {
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

// scopedOwnerID mirrors scopedSystemAccountId: admins pass the filter through
// (empty = unscoped), users are pinned to themselves.
func scopedOwnerID(access *AccessScope) string {
	if access == nil {
		return ""
	}
	return access.manageableID()
}

func (s *Store) findManualTestContextRow(ctx context.Context, accountID string, access *AccessScope, includeCredentials bool) (*manualTestContextRow, error) {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return nil, nil
	}
	owner := scopedOwnerID(access)
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
	FROM ` + s.table("accounts") + ` accounts
	LEFT JOIN ` + s.table("accounts") + ` source_accounts
		ON source_accounts.id = accounts.authorization_instance_source_account_id
		AND source_accounts.deleted_at IS NULL
	LEFT JOIN ` + s.table("resource_authorizations") + ` authorizations
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

	var row manualTestContextRow
	scan := func(target ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(query), args...).Scan(target...)
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
func (s *Store) FindManualTestOptionsContext(ctx context.Context, accountID string, access *AccessScope) (*ManualTestContext, error) {
	ctx = ensureCtx(ctx)
	row, err := s.findManualTestContextRow(ctx, accountID, access, true)
	if err != nil || row == nil {
		return nil, err
	}
	if !row.credentialsEncrypted.Valid || row.credentialsEncrypted.String == "" {
		return nil, nil
	}
	var credentials Credentials
	if err := DecryptJSON(s.secret, row.credentialsEncrypted.String, &credentials); err != nil {
		return nil, nil
	}
	mappings, err := s.loadTestAccountModelMappings(ctx, s.db, row.factAccountID, "")
	if err != nil {
		return nil, err
	}
	return s.manualTestContextFromRow(row, credentials, mappings), nil
}

// FindManualTestCapabilitiesContext mirrors
// findAccountManualTestCapabilitiesContextAsync (model-scoped mappings).
func (s *Store) FindManualTestCapabilitiesContext(ctx context.Context, accountID, modelID string, access *AccessScope) (*ManualTestContext, error) {
	ctx = ensureCtx(ctx)
	row, err := s.findManualTestContextRow(ctx, accountID, access, true)
	if err != nil || row == nil {
		return nil, err
	}
	if !row.credentialsEncrypted.Valid || row.credentialsEncrypted.String == "" {
		return nil, nil
	}
	var credentials Credentials
	if err := DecryptJSON(s.secret, row.credentialsEncrypted.String, &credentials); err != nil {
		return nil, nil
	}
	mappings, err := s.loadTestAccountModelMappings(ctx, s.db, row.factAccountID, strings.TrimSpace(modelID))
	if err != nil {
		return nil, err
	}
	return s.manualTestContextFromRow(row, credentials, mappings), nil
}

func (s *Store) manualTestContextFromRow(row *manualTestContextRow, credentials Credentials, mappings []ModelMapping) *ManualTestContext {
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
		SupportedEndpointModes:    supportedEndpointModesFromCredentials(credentials),
		ModelMappings:             mappings,
	}
}

// supportedEndpointModesFromCredentials mirrors
// accountSupportedEndpointModes(credentials.supported_endpoint_modes).
func supportedEndpointModesFromCredentials(credentials Credentials) []string {
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

// loadTestAccountModelMappings mirrors loadModelMappingsByAccountIdsAsync /
// loadModelMappingsForAccountModel (single account; empty model = all).
// Named apart from loadAccountModelMappings (patch.go), which carries the
// write-path projection.
func (s *Store) loadTestAccountModelMappings(ctx context.Context, q queryer, accountID, model string) ([]ModelMapping, error) {
	query := `SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
		FROM ` + s.table("account_model_mappings") + `
		WHERE account_id = ?`
	args := []any{accountID}
	if model != "" {
		query += ` AND source_model = ?`
		args = append(args, model)
	}
	query += ` ORDER BY source_endpoint_family ASC, source_model ASC`
	rows, err := q.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mappings := []ModelMapping{}
	for rows.Next() {
		var mapping ModelMapping
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

// manualTestModeSource carries the account fields
// accountManualTestEndpointModes consumes.
type manualTestModeSource struct {
	providerCode              string
	providerProtocolProfileID string
	protocolCode              string
	protocolVersion           string
	accountType               string
	clientCompatibility       string
	healthCheckEndpointMode   string
	supportedEndpointModes    []string
	modelMappings             []ModelMapping
}

func (m manualTestModeSource) predicate() protocolPredicateInput {
	return protocolPredicateInput{
		providerCode:              m.providerCode,
		protocolCode:              m.protocolCode,
		protocolVersion:           m.protocolVersion,
		providerProtocolProfileID: m.providerProtocolProfileID,
	}
}

func (m manualTestModeSource) defaultContext() endpointModeDefaultContext {
	return endpointModeDefaultContext{
		providerCode:              m.providerCode,
		accountType:               m.accountType,
		protocolCode:              m.protocolCode,
		protocolVersion:           m.protocolVersion,
		providerProtocolProfileID: m.providerProtocolProfileID,
		clientCompatibility:       m.clientCompatibility,
	}
}

// normalizeOpenAIEndpointModesForRuntime (runtime fallback: non-array or
// empty → defaults, unknown values dropped).
func normalizeOpenAIEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !isOpenAIEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return defaultOpenAIEndpointModes(defaults)
	}
	return output
}

func normalizeAnthropicEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !isAnthropicEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return defaultAnthropicEndpointModes(defaults)
	}
	return output
}

func normalizeGeminiEndpointModesForRuntime(value []string, defaults endpointModeDefaultContext) []string {
	output := []string{}
	seen := map[string]bool{}
	for _, item := range value {
		if !isGeminiEndpointMode(item) || seen[item] {
			continue
		}
		seen[item] = true
		output = append(output, item)
	}
	if len(output) == 0 {
		return defaultGeminiEndpointModes(defaults)
	}
	return output
}

func normalizeHybridEndpointModesForRuntime(value []string) []string {
	known := stringSet(hybridEndpointModeValues)
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
		return append([]string{}, hybridEndpointModeValues...)
	}
	return output
}

// accountManualTestEndpointModes mirrors the same-named helper.
func accountManualTestEndpointModes(source manualTestModeSource) []string {
	enabled := source.supportedEndpointModes
	switch {
	case isHybridProviderCodeToken(source.providerCode):
		enabled = normalizeHybridEndpointModesForRuntime(source.supportedEndpointModes)
	case isAnthropicProtocolProfileOf(source.predicate()):
		enabled = normalizeAnthropicEndpointModesForRuntime(source.supportedEndpointModes, source.defaultContext())
	case isGeminiProtocolProfileOf(source.predicate()):
		enabled = normalizeGeminiEndpointModesForRuntime(source.supportedEndpointModes, source.defaultContext())
	case isOpenAIProtocolProfileOf(source.predicate()):
		enabled = normalizeOpenAIEndpointModesForRuntime(source.supportedEndpointModes, source.defaultContext())
	default:
		enabled = []string{}
	}
	enabledSet := map[string]bool{}
	for _, mode := range enabled {
		enabledSet[mode] = true
	}
	out := []string{}
	for _, mode := range accountTestEndpointModeOrder(source) {
		if enabledSet[mode] {
			out = append(out, mode)
		}
	}
	return out
}

// accountTestEndpointModeOrder mirrors the same-named helper.
func accountTestEndpointModeOrder(source manualTestModeSource) []string {
	defaultMode := source.healthCheckEndpointMode
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
	case isHybridProviderCodeToken(source.providerCode):
		return unique(defaultMode,
			"chat_json", "chat_sse", "responses_json", "responses_sse",
			"messages_json", "messages_sse", "generate_content_json", "generate_content_sse")
	case isAnthropicProtocolProfileOf(source.predicate()):
		return unique(defaultMode, "messages_json", "messages_sse")
	case isGeminiProtocolProfileOf(source.predicate()):
		return unique(defaultMode, "interactions_json", "interactions_sse", "generate_content_json", "generate_content_sse")
	case source.accountType == "oauth":
		return unique(defaultMode, "responses_json", "responses_sse")
	default:
		return unique(defaultMode, "chat_sse", "responses_sse", "chat_json", "responses_json")
	}
}

// ---- model mapping resolution (openai-v1/model-mapping.ts subset) ----

const geminiOpenAIChatV1BetaProfile = "profile_gemini_openai_chat_v1beta"

type resolvedTestModelMapping struct {
	upstreamModel          string
	upstreamEndpointFamily string
}

func isTestMappingSourceFamily(value string) bool {
	switch value {
	case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
		return true
	}
	return false
}

// isOpenAIModelMappingRuntimeConversionSupported mirrors the same-named
// helper (model-mapping.ts; also ported in gatewayopenai/mapping.go).
func isOpenAIModelMappingRuntimeConversionSupported(mapping ModelMapping, source manualTestModeSource) bool {
	src := mapping.SourceEndpointFamily
	upstream := mapping.UpstreamEndpointFamily
	if src == upstream || (src == "stream_generate_content" && upstream == "generate_content") {
		return true
	}
	if src == "responses" && upstream == "chat_completions" && isOpenAIProtocolProfileOf(source.predicate()) {
		return true
	}
	if !isHybridProviderCodeToken(source.providerCode) {
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

// resolveTestAccountModelMapping mirrors resolveOpenAIAccountModelMapping for
// the options path (Go ModelMapping rows never carry the runtime-only
// explicit_hybrid_route source, so that guard has no DB-reachable input).
func resolveTestAccountModelMapping(source manualTestModeSource, model, sourceFamily string) *resolvedTestModelMapping {
	if model == "" || sourceFamily == "" || !isTestMappingSourceFamily(sourceFamily) {
		return nil
	}
	if source.providerProtocolProfileID == geminiOpenAIChatV1BetaProfile && sourceFamily == "messages" {
		return nil
	}
	var mapping *ModelMapping
	for index := range source.modelMappings {
		candidate := source.modelMappings[index]
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
	if !isOpenAIModelMappingRuntimeConversionSupported(*mapping, source) {
		return nil
	}
	return &resolvedTestModelMapping{
		upstreamModel:          mapping.UpstreamModel,
		upstreamEndpointFamily: mapping.UpstreamEndpointFamily,
	}
}

// ---- test catalog reads ----

// testOptionRow mirrors ProviderModelOptionRow (the projection the options
// merge consumes).
type testOptionRow struct {
	provider              string
	model                 string
	scope                 string
	mode                  string
	releaseDate           string
	supportedAPIProtocols []string
}

// testCatalogItem mirrors ProviderModelTestCatalogItem (protocolsOnly
// projection: model + mode + supported protocols).
type testCatalogItem struct {
	model                 string
	mode                  string
	supportedAPIProtocols []string
}

// testCatalogSourceCodes mirrors providerModelSourceCodesAsync for the
// providerCode branch (modelCatalogSourceProviderCodesAsync).
func (s *Store) testCatalogSourceCodes(ctx context.Context, providerCode string) ([]string, error) {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}, nil
	}
	if normalized == hybridProviderCode {
		codes := []string{}
		for _, pair := range [][2]string{
			{openAIProtocolCode, openAIProtocolVersion},
			{anthropicProviderCode, anthropicProtocolVersionConstant},
			{geminiProviderCode, geminiProtocolVersionConstant},
		} {
			list, err := s.protocolProviderCodes(ctx, pair[0], pair[1])
			if err != nil {
				return nil, err
			}
			for _, code := range list {
				token := normalizeProviderToken(code)
				if token == "" || token == hybridProviderCode {
					continue
				}
				codes = append(codes, token)
			}
		}
		return dedupeTestStrings(codes), nil
	}
	if normalized != openAICompatibleProviderCodeConstant {
		return []string{normalized}, nil
	}
	list, err := s.protocolProviderCodes(ctx, openAIProtocolCode, openAIProtocolVersion)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, code := range list {
		token := normalizeProviderToken(code)
		if token == "" || token == normalized {
			continue
		}
		codes = append(codes, token)
	}
	return dedupeTestStrings(append(codes, normalized)), nil
}

func dedupeTestStrings(values []string) []string {
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

// protocolProviderCodes mirrors listOpenAI/Anthropic/GeminiProtocolProviderCodesAsync:
// enabled providers with an enabled profile on the protocol pair.
func (s *Store) protocolProviderCodes(ctx context.Context, protocolCode, protocolVersion string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT DISTINCT p.code
		FROM `+s.table("providers")+` p
		JOIN `+s.table("provider_protocol_profiles")+` ppp ON ppp.provider_code = p.code
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
func (s *Store) testCatalogAvailability() string {
	return ` AND status = 'active'
		AND catalog_visible = 1
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ` + testTodayText(s) + `)`
}

// testTodayText renders the SQL "today" literal (SQLite date('now') vs
// PostgreSQL CURRENT_DATE::text), mirroring the providers slice helper.
func testTodayText(s *Store) string {
	if s.pg {
		return "CURRENT_DATE::text"
	}
	return "date('now')"
}

func normalizeTestProviderCodeList(codes []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, code := range codes {
		token := normalizeProviderToken(code)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// testCatalogBuiltInSourceCodes mirrors modelCatalogBuiltInSourceProviderCodes.
func testCatalogBuiltInSourceCodes(providerCode string, sourceCodes []string) []string {
	if normalizeProviderToken(providerCode) != openAICompatibleProviderCodeConstant {
		return sourceCodes
	}
	codes := []string{}
	for _, code := range sourceCodes {
		if normalizeProviderToken(code) == openAICompatibleProviderCodeConstant {
			continue
		}
		codes = append(codes, code)
	}
	return codes
}

// listBuiltInTestCatalogOptions mirrors listBuiltInProviderModelOptionsAsync.
func (s *Store) listBuiltInTestCatalogOptions(ctx context.Context, providerCodes []string, query ManualTestOptionsQuery) ([]testOptionRow, error) {
	codes := normalizeTestProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []testOptionRow{}, nil
	}
	clauses := []string{"provider_code IN (" + placeholders(len(codes)) + ")"}
	args := append([]any{}, anySlice(codes)...)
	args = s.appendTestOptionTextFilter(&clauses, args, query)
	args = append(args, selectedOrderArgs(query)...)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model, mode, release_date, supported_api_protocols_json
		FROM `+s.table("provider_model_catalog")+`
		WHERE `+strings.Join(clauses, " AND ")+s.testCatalogAvailability()+`
		ORDER BY `+testSelectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, lower(model) ASC, provider_code ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTestOptionRows(rows, catalogScopeBuiltIn)
}

// listCustomTestCatalogOptions mirrors listCustomProviderModelOptionsAsync.
func (s *Store) listCustomTestCatalogOptions(ctx context.Context, providerCodes []string, systemAccountID string, query ManualTestOptionsQuery) ([]testOptionRow, error) {
	codes := normalizeTestProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []testOptionRow{}, nil
	}
	clauses := []string{"provider_code IN (" + placeholders(len(codes)) + ")"}
	args := append([]any{}, anySlice(codes)...)
	if trimmed := strings.TrimSpace(systemAccountID); trimmed != "" {
		clauses = append(clauses, "((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, trimmed)
	} else {
		clauses = append(clauses, "scope = 'global' AND system_account_id IS NULL")
	}
	args = s.appendTestOptionTextFilter(&clauses, args, query)
	args = append(args, selectedOrderArgs(query)...)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model, scope, mode, release_date, supported_api_protocols_json
		FROM `+s.table("custom_provider_models")+`
		WHERE `+strings.Join(clauses, " AND ")+` AND status = 'active'
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+testTodayText(s)+`)
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
func (s *Store) appendTestOptionTextFilter(clauses *[]string, args []any, query ManualTestOptionsQuery) []any {
	if query.Keyword == "" {
		return args
	}
	parts := []string{}
	if len(query.SelectedIDs) > 0 {
		parts = append(parts, "model IN ("+placeholders(len(query.SelectedIDs))+")")
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
	return "CASE WHEN model IN (" + placeholders(len(query.SelectedIDs)) + ") THEN 0 ELSE 1 END, "
}

func selectedOrderArgs(query ManualTestOptionsQuery) []any {
	out := []any{}
	for _, id := range query.SelectedIDs {
		out = append(out, id)
	}
	return out
}

func scanTestOptionRows(rows *sql.Rows, fixedScope string) ([]testOptionRow, error) {
	items := []testOptionRow{}
	for rows.Next() {
		var row testOptionRow
		var mode, releaseDate, protocols sql.NullString
		var scope sql.NullString
		var err error
		if fixedScope != "" {
			err = rows.Scan(&row.provider, &row.model, &mode, &releaseDate, &protocols)
			row.scope = fixedScope
		} else {
			err = rows.Scan(&row.provider, &row.model, &scope, &mode, &releaseDate, &protocols)
			row.scope = scope.String
		}
		if err != nil {
			return nil, err
		}
		row.mode = mode.String
		row.releaseDate = releaseDate.String
		row.supportedAPIProtocols = testParseJSONArray(protocols)
		items = append(items, row)
	}
	return items, rows.Err()
}

// findTestCatalogItem mirrors findProviderModelTestCatalogItemAsync
// (protocolsOnly projection: model + mode + supported protocols; the winner
// of the scope-priority merge ordered by the test-catalog comparator).
func (s *Store) findTestCatalogItem(ctx context.Context, providerCode, systemAccountID, model string) (*testCatalogItem, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil, nil
	}
	sourceCodes, err := s.testCatalogSourceCodes(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return nil, nil
	}
	builtInCodes := testCatalogBuiltInSourceCodes(providerCode, sourceCodes)
	availability := s.testCatalogAvailability()
	candidates, err := s.collectTestCatalogCandidates(ctx, builtInCodes, sourceCodes, trimmed, availability)
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
		if testCatalogScopePriority(item.scope) >= testCatalogScopePriority(winner.scope) {
			winner = item
		}
	}
	for _, item := range candidates {
		if testCatalogScopePriority(item.scope) != testCatalogScopePriority(winner.scope) {
			continue
		}
		leftDate, rightDate := testCatalogReleaseDate(item.releaseDate), testCatalogReleaseDate(winner.releaseDate)
		if leftDate != "" && rightDate != "" && leftDate != rightDate && leftDate > rightDate {
			winner = item
		}
	}
	return &winner.item, nil
}

type testCatalogCandidate struct {
	item        testCatalogItem
	scope       string
	releaseDate string
}

func (s *Store) collectTestCatalogCandidates(ctx context.Context, builtInCodes, sourceCodes []string, model, availability string) ([]testCatalogCandidate, error) {
	candidates := []testCatalogCandidate{}
	if codes := normalizeTestProviderCodeList(builtInCodes); len(codes) > 0 {
		rows, err := s.db.QueryContext(ctx, s.bind(`SELECT model, mode, supported_api_protocols_json, release_date
			FROM `+s.table("provider_model_catalog")+`
			WHERE provider_code IN (`+placeholders(len(codes))+`) AND model = ?`+availability+`
			ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC`), append(anySlice(codes), model)...)
		if err != nil {
			return nil, err
		}
		collected, err := scanTestCandidates(rows, catalogScopeBuiltIn, false)
		rows.Close()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, collected...)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT model, mode, supported_api_protocols_json, release_date, scope
		FROM `+s.table("custom_provider_models")+`
		WHERE provider_code IN (`+placeholders(len(sourceCodes))+`) AND model = ? AND status = 'active'
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+testTodayText(s)+`)
		ORDER BY provider_code ASC, scope ASC, lower(model) ASC, id ASC`),
		append(anySlice(sourceCodes), model)...)
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

func scanTestCandidates(rows *sql.Rows, fixedScope string, withScope bool) ([]testCatalogCandidate, error) {
	candidates := []testCatalogCandidate{}
	for rows.Next() {
		var (
			item        testCatalogItem
			mode        sql.NullString
			protocols   sql.NullString
			releaseDate sql.NullString
			scope       sql.NullString
		)
		var err error
		if withScope {
			err = rows.Scan(&item.model, &mode, &protocols, &releaseDate, &scope)
		} else {
			err = rows.Scan(&item.model, &mode, &protocols, &releaseDate)
		}
		if err != nil {
			return nil, err
		}
		item.mode = mode.String
		item.supportedAPIProtocols = testParseJSONArray(protocols)
		entry := testCatalogCandidate{item: item, releaseDate: releaseDate.String, scope: fixedScope}
		if withScope {
			entry.scope = scope.String
		}
		candidates = append(candidates, entry)
	}
	return candidates, rows.Err()
}

func testCatalogScopePriority(scope string) int {
	switch scope {
	case catalogScopePersonal:
		return 3
	case catalogScopeGlobal:
		return 2
	}
	return 1
}

func testCatalogReleaseDate(value string) string {
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
func (s *Store) AccountManualTestOptions(ctx context.Context, account *ManualTestContext, query ManualTestOptionsQuery) ([]ManualTestOption, error) {
	systemAccountID := account.OwnerSystemAccountID
	if systemAccountID == "" {
		return nil, &ValidationError{Message: "账户归属数据异常，无法读取测试模型"}
	}
	selectedIDs := append([]string{}, query.SelectedIDs...)
	if healthModel := strings.TrimSpace(account.HealthCheckModel); healthModel != "" {
		if !containsString(selectedIDs, healthModel) {
			selectedIDs = append(selectedIDs, healthModel)
		}
	}
	sourceCodes, err := s.testCatalogSourceCodes(ctx, account.ProviderCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []ManualTestOption{}, nil
	}
	builtInCodes := testCatalogBuiltInSourceCodes(account.ProviderCode, sourceCodes)
	optionQuery := ManualTestOptionsQuery{Keyword: query.Keyword, Limit: query.Limit, SelectedIDs: selectedIDs}
	builtIn, err := s.listBuiltInTestCatalogOptions(ctx, builtInCodes, optionQuery)
	if err != nil {
		return nil, err
	}
	custom, err := s.listCustomTestCatalogOptions(ctx, sourceCodes, systemAccountID, optionQuery)
	if err != nil {
		return nil, err
	}
	source := account.modeSource()
	eligible := []testOptionRow{}
	for _, row := range append(builtIn, custom...) {
		if isAccountManualTestModel(testCatalogItem{model: row.model, mode: row.mode, supportedAPIProtocols: row.supportedAPIProtocols}, source) {
			eligible = append(eligible, row)
		}
	}
	options := mergeTestOptionRows(eligible, optionQuery)
	cache := map[string]*testCatalogItem{}
	resolved := []ManualTestOption{}
	for _, option := range options {
		modes, err := s.manualTestEndpointModesForTargetModel(ctx, source, testCatalogItem{
			model:                 option.model,
			supportedAPIProtocols: option.protocols,
		}, systemAccountID, cache)
		if err != nil {
			return nil, err
		}
		if len(modes) > 0 {
			resolved = append(resolved, ManualTestOption{ID: option.model, Name: option.model, TestEndpointModes: modes})
		}
	}
	return resolved, nil
}

func (a *ManualTestContext) modeSource() manualTestModeSource {
	return manualTestModeSource{
		providerCode:              a.ProviderCode,
		providerProtocolProfileID: a.ProviderProtocolProfileID,
		protocolCode:              a.ProtocolCode,
		protocolVersion:           a.ProtocolVersion,
		accountType:               a.Type,
		clientCompatibility:       a.ClientCompatibility,
		healthCheckEndpointMode:   a.HealthCheckEndpointMode,
		supportedEndpointModes:    a.SupportedEndpointModes,
		modelMappings:             a.ModelMappings,
	}
}

type mergedTestOption struct {
	model     string
	protocols []string
}

// mergeTestOptionRows mirrors mergeProviderModelOptionRows (keyword/selected
// filter, dedupe with scope priority, release-date ordering, selected-aware
// limit).
func mergeTestOptionRows(rows []testOptionRow, query ManualTestOptionsQuery) []mergedTestOption {
	selected := map[string]bool{}
	for _, id := range query.SelectedIDs {
		selected[id] = true
	}
	keyword := strings.ToLower(query.Keyword)
	byModel := map[string]testOptionRow{}
	order := []string{}
	for _, row := range rows {
		row.provider = strings.TrimSpace(row.provider)
		row.model = strings.TrimSpace(row.model)
		if row.provider == "" || row.model == "" {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(row.model), keyword) && !selected[row.model] {
			continue
		}
		existing, ok := byModel[row.model]
		if !ok {
			order = append(order, row.model)
			byModel[row.model] = row
			continue
		}
		if optionScopePriorityValue(row.scope) > optionScopePriorityValue(existing.scope) {
			byModel[row.model] = row
		}
	}
	sort.SliceStable(order, func(left, right int) bool {
		leftRow, rightRow := byModel[order[left]], byModel[order[right]]
		leftDate, rightDate := normalizedOptionReleaseDate(leftRow.releaseDate), normalizedOptionReleaseDate(rightRow.releaseDate)
		if leftDate != "" && rightDate != "" && leftDate != rightDate {
			return leftDate > rightDate
		}
		if leftDate != "" && rightDate == "" {
			return true
		}
		if leftDate == "" && rightDate != "" {
			return false
		}
		if cmp := strings.Compare(leftRow.model, rightRow.model); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(leftRow.provider, rightRow.provider) < 0
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
	options := []mergedTestOption{}
	for _, model := range order {
		if !visible[model] {
			continue
		}
		options = append(options, mergedTestOption{model: model, protocols: byModel[model].supportedAPIProtocols})
	}
	return options
}

func optionScopePriorityValue(scope string) int {
	switch scope {
	case catalogScopePersonal:
		return 3
	case catalogScopeGlobal:
		return 2
	}
	return 1
}

const catalogScopeBuiltIn = "built_in"
const catalogScopeGlobal = "global"
const catalogScopePersonal = "personal"

func normalizedOptionReleaseDate(value string) string {
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
func (s *Store) AccountManualTestModelCapabilities(ctx context.Context, account *ManualTestContext, modelInput string) (*ManualTestModelCapabilities, error) {
	model := strings.TrimSpace(modelInput)
	if model == "" {
		return nil, &ValidationError{Message: "请选择测试模型"}
	}
	systemAccountID := account.OwnerSystemAccountID
	if systemAccountID == "" {
		return nil, &ValidationError{Message: "账户归属数据异常，无法读取测试模型"}
	}
	item, err := s.findTestCatalogItem(ctx, account.ProviderCode, systemAccountID, model)
	if err != nil {
		return nil, err
	}
	source := account.modeSource()
	if item == nil || !isAccountManualTestModel(*item, source) {
		return nil, &ValidationError{Message: fmt.Sprintf("模型不在当前账户供应商可用目录中：%s", model)}
	}
	modes, err := s.manualTestEndpointModesForTargetModel(ctx, source, *item, systemAccountID, map[string]*testCatalogItem{})
	if err != nil {
		return nil, err
	}
	if len(modes) == 0 {
		return nil, &ValidationError{Message: "账户上游接口能力中没有可用于连接测试的请求形态"}
	}
	return &ManualTestModelCapabilities{ID: item.model, TestEndpointModes: modes}, nil
}

// ResolveAccountManualTestSelection mirrors resolveAccountManualTestSelectionAsync.
func (s *Store) ResolveAccountManualTestSelection(ctx context.Context, account *ManualTestContext, modelInput, testEndpointMode string) (model, resolvedMode string, err error) {
	model = strings.TrimSpace(modelInput)
	if model == "" {
		return "", "", &ValidationError{Message: "请选择测试模型"}
	}
	option, err := s.AccountManualTestModelCapabilities(ctx, account, model)
	if err != nil {
		return "", "", err
	}
	resolvedMode = testEndpointMode
	if resolvedMode == "" && len(option.TestEndpointModes) > 0 {
		resolvedMode = option.TestEndpointModes[0]
	}
	if resolvedMode == "" || !containsString(option.TestEndpointModes, resolvedMode) {
		requested := testEndpointMode
		if requested == "" {
			requested = "未选择"
		}
		return "", "", &ValidationError{Message: fmt.Sprintf("模型 %s 不支持本次检查协议：%s", model, requested)}
	}
	return model, resolvedMode, nil
}

// manualTestEndpointModesForTargetModel mirrors
// accountManualTestEndpointModesForTargetModelAsync.
func (s *Store) manualTestEndpointModesForTargetModel(ctx context.Context, source manualTestModeSource, item testCatalogItem, systemAccountID string, cache map[string]*testCatalogItem) ([]string, error) {
	out := []string{}
	for _, mode := range accountManualTestEndpointModes(source) {
		if mode == "interactions_json" || mode == "interactions_sse" {
			if testModelSupportsProtocol(item, "interactions") {
				out = append(out, mode)
			}
			continue
		}
		sourceFamily, err := endpointModeProtocolFamily(mode)
		if err != nil {
			return nil, err
		}
		mapping := resolveTestAccountModelMapping(source, item.model, sourceFamily)
		if mapping == nil {
			if testModelSupportsProtocol(item, sourceFamily) {
				out = append(out, mode)
			}
			continue
		}
		upstream := cache[mapping.upstreamModel]
		if upstream == nil {
			loaded, err := s.findTestCatalogItem(ctx, source.providerCode, systemAccountID, mapping.upstreamModel)
			if err != nil {
				return nil, err
			}
			cache[mapping.upstreamModel] = loaded
			upstream = loaded
		}
		if upstream != nil && testModelSupportsProtocol(*upstream, mapping.upstreamEndpointFamily) {
			out = append(out, mode)
		}
	}
	if testModelSupportsImagesProtocol(item, source) {
		out = append(out, "images_json")
	}
	return out, nil
}

// endpointModeProtocolFamily mirrors endpointModeProtocol.
func endpointModeProtocolFamily(mode string) (string, error) {
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

func testModelSupportsProtocol(item testCatalogItem, protocol string) bool {
	protocols := item.supportedAPIProtocols
	return len(protocols) == 0 || containsString(protocols, protocol)
}

func testModelSupportsImagesProtocol(item testCatalogItem, source manualTestModeSource) bool {
	return source.accountType == "api_key" &&
		isOpenAIProtocolProfileOf(source.predicate()) &&
		containsString(item.supportedAPIProtocols, "images")
}

// isAccountManualTestModel mirrors the same-named helper.
func isAccountManualTestModel(item testCatalogItem, source manualTestModeSource) bool {
	if strings.ToLower(strings.TrimSpace(item.mode)) == "audio" {
		return false
	}
	if hasEnabledTestModelMapping(source.modelMappings, item.model) {
		return true
	}
	protocols := item.supportedAPIProtocols
	if len(protocols) == 0 {
		mode := strings.ToLower(strings.TrimSpace(item.mode))
		return mode != "image_generation" && mode != "image"
	}
	switch {
	case isHybridProviderCodeToken(source.providerCode):
		for _, protocol := range protocols {
			switch protocol {
			case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
				return true
			case "images":
				if source.accountType == "api_key" {
					return true
				}
			}
		}
		return false
	case isOpenAIProtocolProfileOf(source.predicate()):
		for _, protocol := range protocols {
			switch protocol {
			case "chat_completions", "responses":
				return true
			case "images":
				if source.accountType == "api_key" {
					return true
				}
			}
		}
		return false
	case isAnthropicProtocolProfileOf(source.predicate()):
		return containsString(protocols, "messages")
	case isGeminiProtocolProfileOf(source.predicate()):
		for _, protocol := range protocols {
			if protocol == "generate_content" || protocol == "stream_generate_content" || protocol == "interactions" {
				return true
			}
		}
		return false
	}
	return false
}

func hasEnabledTestModelMapping(mappings []ModelMapping, model string) bool {
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

func testParseJSONArray(value sql.NullString) []string {
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
