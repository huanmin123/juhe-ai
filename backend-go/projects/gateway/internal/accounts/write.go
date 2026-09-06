package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ModelMapping mirrors AccountModelMapping (accountModelMappingSchema).
type ModelMapping struct {
	SourceModel            string `json:"sourceModel"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily"`
	UpstreamModel          string `json:"upstreamModel"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily"`
	Enabled                *bool  `json:"enabled,omitempty"`
}

// Endpoint family enums of accountModelMappingSchema: an unknown value fails
// the create/PATCH schema parse (400) instead of persisting an unmappable row.
var (
	accountSourceEndpointFamilies = map[string]bool{
		"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true, "stream_generate_content": true,
	}
	accountUpstreamEndpointFamilies = map[string]bool{
		"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true,
	}
)

// normalizeModelMappingBody mirrors accountModelMappingSchema.parse: the four
// trimmed string fields are required and the endpoint families are strict
// enums. The message matches the route-family 400 copy.
func normalizeModelMappingBody(object map[string]any) (ModelMapping, bool) {
	mapping := ModelMapping{
		SourceModel:            strings.TrimSpace(textString(object["sourceModel"])),
		SourceEndpointFamily:   strings.TrimSpace(textString(object["sourceEndpointFamily"])),
		UpstreamModel:          strings.TrimSpace(textString(object["upstreamModel"])),
		UpstreamEndpointFamily: strings.TrimSpace(textString(object["upstreamEndpointFamily"])),
	}
	if mapping.SourceModel == "" || mapping.UpstreamModel == "" ||
		mapping.SourceEndpointFamily == "" || mapping.UpstreamEndpointFamily == "" {
		return ModelMapping{}, false
	}
	if !accountSourceEndpointFamilies[mapping.SourceEndpointFamily] ||
		!accountUpstreamEndpointFamilies[mapping.UpstreamEndpointFamily] {
		return ModelMapping{}, false
	}
	if enabled, ok := object["enabled"].(bool); ok {
		mapping.Enabled = &enabled
	} else if value, exists := object["enabled"]; exists && value != nil {
		return ModelMapping{}, false
	}
	// strict(): unknown keys fail the parse.
	for key := range object {
		switch key {
		case "sourceModel", "sourceEndpointFamily", "upstreamModel", "upstreamEndpointFamily", "enabled":
		default:
			return ModelMapping{}, false
		}
	}
	return mapping, true
}

var accountHealthCheckEndpointModes = map[string]bool{
	"images_json": true, "chat_json": true, "chat_sse": true,
	"responses_json": true, "responses_sse": true,
	"messages_json": true, "messages_sse": true,
	"generate_content_json": true, "generate_content_sse": true,
	"interactions_json": true, "interactions_sse": true,
}

var accountStatusValues = map[string]bool{
	"active": true, "pending_test": true, "disabled": true, "error": true,
	"rate_limited": true, "temporary_unavailable": true, "quality_isolated": true,
}

// defaultAccountConcurrencyLimit mirrors DEFAULT_ACCOUNT_CONCURRENCY_LIMIT
// (schema default 5000).
const defaultAccountConcurrencyLimit = 5000

// CreationStatus mirrors accountCreationStatusInput: the user-facing creation
// choice plus the derived guarded write flags (Node overrides the body fields
// with these in accounts.routes.ts).
type CreationStatus struct {
	Status                 string
	SkipInitialHealthCheck bool
	Schedulable            bool
}

// AccountCreationStatusInput mirrors accountCreationStatusInput.
func AccountCreationStatusInput(value any) CreationStatus {
	status := "pending_test"
	if text, ok := value.(string); ok && (text == "active" || text == "disabled") {
		status = text
	}
	return CreationStatus{
		Status:                 status,
		SkipInitialHealthCheck: status == "active",
		Schedulable:            status == "active",
	}
}

// CreateInput is the validated create payload (accountCreateSchema subset the
// store consumes); nil pointers mean the field was absent.
type CreateInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	AccountType               string
	Credentials               Credentials
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	ModelMappings             []ModelMapping
	Tags                      []string
	Status                    CreationStatus
	ConcurrencyLimit          *int
	Priority                  *int
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	ProxyProfileID            *string
	GroupID                   *string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
	BalanceQueryEnabled       bool
	// BalanceQueryConfigCanonical carries the normalized config JSON (the
	// create body parser already ran normalizeAccountBalanceConfig, exactly
	// like the Node route); nil means the request did not include a config.
	BalanceQueryConfigCanonical *string
	// TemporaryUnavailableContinuousProbeEnabled mirrors the
	// normalizeOptionalBooleanInput tri-state: nil = not provided (defaults
	// to enabled), false persists the explicit opt-out.
	TemporaryUnavailableContinuousProbeEnabled *bool
}

// providerProfile mirrors requireEnabledProviderProtocolProfileInClientAsync.
type providerProfile struct {
	id                      string
	providerCode            string
	name                    string
	protocolCode            string
	protocolVersion         string
	baseURL                 string
	defaultHealthCheckModel string
	accountTypes            []string
}

func (s *Store) requireEnabledProviderProtocolProfile(ctx context.Context, q queryer, providerCode, profileID string) (*providerProfile, error) {
	var row struct {
		id                      sql.NullString
		providerCode            sql.NullString
		name                    sql.NullString
		enabled                 sql.NullInt64
		protocolCode            sql.NullString
		protocolVersion         sql.NullString
		baseURL                 sql.NullString
		defaultHealthCheckModel sql.NullString
		accountTypesJSON        sql.NullString
		providerEnabled         sql.NullInt64
	}
	err := q.QueryRowContext(ctx, s.bind(`SELECT ppp.id, ppp.provider_code, ppp.name, ppp.enabled,
			ppp.protocol_code, ppp.protocol_version, ppp.base_url,
			ppp.default_health_check_model, ppp.account_types_json,
			p.enabled AS provider_enabled
		FROM `+s.table("providers")+` p
		LEFT JOIN `+s.table("provider_protocol_profiles")+` ppp
			ON ppp.provider_code = p.code
			AND ppp.id = ?
		WHERE p.code = ?
		LIMIT 1`), profileID, providerCode).Scan(
		&row.id, &row.providerCode, &row.name, &row.enabled,
		&row.protocolCode, &row.protocolVersion, &row.baseURL,
		&row.defaultHealthCheckModel, &row.accountTypesJSON, &row.providerEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "不支持的供应商：" + providerCode}
	}
	if err != nil {
		return nil, err
	}
	if !row.providerEnabled.Valid || row.providerEnabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商已停用：" + providerCode}
	}
	if profileID == "" {
		return nil, &ValidationError{Message: "供应商协议档案不能为空"}
	}
	if !row.id.Valid || row.id.String == "" || (row.providerCode.Valid && row.providerCode.String != providerCode) {
		return nil, &ValidationError{Message: "供应商协议档案无效：" + profileID}
	}
	if !row.enabled.Valid || row.enabled.Int64 != 1 {
		return nil, &ValidationError{Message: "供应商协议档案已停用：" + row.name.String}
	}
	profile := &providerProfile{
		id:                      row.id.String,
		providerCode:            providerCode,
		name:                    row.name.String,
		protocolCode:            row.protocolCode.String,
		protocolVersion:         row.protocolVersion.String,
		baseURL:                 row.baseURL.String,
		defaultHealthCheckModel: row.defaultHealthCheckModel.String,
		accountTypes:            []string{},
	}
	if row.accountTypesJSON.Valid && strings.TrimSpace(row.accountTypesJSON.String) != "" {
		_ = json.Unmarshal([]byte(row.accountTypesJSON.String), &profile.accountTypes)
	}
	return profile, nil
}

// requiredAccountCredentialSource mirrors requiredAccountCredentialSource
// (account-credentials-normalization.ts): the secret field that backs the
// fingerprint/mask columns per account type.
func requiredAccountCredentialSource(accountType string, credentials Credentials) (string, error) {
	text := func(value any) string {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
		return ""
	}
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := text(credentials[key]); value != "" {
				return value
			}
		}
		return ""
	}
	apiKeys := func() string {
		if list, ok := credentials["api_keys"].([]any); ok {
			for _, item := range list {
				if value := text(item); value != "" {
					return value
				}
			}
		}
		return text(credentials["api_key"])
	}
	switch accountType {
	case "oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "OAuth 凭据不能为空"}
	case "api_key":
		if value := apiKeys(); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "API Key 不能为空"}
	case "google_oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "Google OAuth 凭据不能为空"}
	default:
		if value := pick("api_key", "refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", &ValidationError{Message: "账户凭据不能为空"}
	}
}

// accountCredentialFingerprint mirrors account-identity.ts: sha256 hex of the
// trimmed source secret.
func accountCredentialFingerprint(source string) string {
	return HashSecret(strings.TrimSpace(source))
}

// normalizeAccountTagNamesInput mirrors normalizeAccountTagNamesInput.
func normalizeAccountTagNamesInput(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户标签必须是字符串数组"}
	}
	output := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, &ValidationError{Message: "账户标签必须是字符串数组"}
		}
		name := strings.Join(strings.Fields(text), " ")
		if name == "" {
			continue
		}
		if len([]rune(name)) > maxTagNameLength {
			return nil, &ValidationError{Message: "账户标签不能超过 40 个字符"}
		}
		if seen[name] {
			continue
		}
		if len(output) >= maxTagsPerAccount {
			return nil, &ValidationError{Message: "单个账户最多配置 24 个标签"}
		}
		seen[name] = true
		output = append(output, name)
	}
	return output, nil
}

// normalizeSupportedModelsInput trims, dedupes and drops blanks (mirror of the
// supported-models normalization subset the slice keeps).
func normalizeSupportedModelsInput(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, &ValidationError{Message: "账户支持模型必须是字符串数组"}
	}
	seen := map[string]bool{}
	output := []string{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, &ValidationError{Message: "账户支持模型必须是字符串数组"}
		}
		model := strings.TrimSpace(text)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		output = append(output, model)
	}
	return output, nil
}

func assertSupportedModelsRequired(models []string) error {
	for _, model := range models {
		if strings.TrimSpace(model) != "" {
			return nil
		}
	}
	return &ValidationError{Message: "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型"}
}

// normalizedHealthCheckModel mirrors normalizedAccountHealthCheckModelInput.
func normalizedHealthCheckModel(value string, supportedModels []string) (string, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return "", &ValidationError{Message: "账户检查模型不能为空"}
	}
	for _, candidate := range supportedModels {
		if candidate == model {
			return model, nil
		}
	}
	return "", &ValidationError{Message: "账户检查模型必须属于账户支持模型"}
}

// groupReference mirrors groupOwnerAndProviderForAccountWriteAsync /
// defaultGroupForAccountWrite rows.
type groupReference struct {
	id              string
	systemAccountID string
	providerCode    string
	name            sql.NullString
}

// Create mirrors createAccountInClientAsync (owner-mode core): provider
// profile check, group binding resolution, sealed credentials with fingerprint
// and mask, schedule-aware initial status, supported models / mappings / tags
// / name search terms writes. config_revision and dispatch_revision start at
// the schema defaults (1 / 1); the circuit outbox transition family is owned
// by the J1 companion slice.
func (s *Store) Create(ctx context.Context, input CreateInput, access AccessScope) (*CreateResult, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := s.now()
	nowISO := isoMillis(now)
	result, err := s.createInTx(ctx, tx, input, access, now, nowISO)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Node accounts.routes.ts dispatches the initial probe right after the
	// create transaction succeeds and before the 201 is rendered. A nil
	// effects port keeps the create self-contained (tests); the probe reason
	// mirrors Node's 'activation'.
	if result.InitialHealthCheckRequired {
		if effects := s.runtimeResetEffectsOrNil(); effects != nil {
			effects.DispatchAccountHealthCheck(result.ID, "activation")
		}
	}
	return result, nil
}

func (s *Store) createInTx(ctx context.Context, tx *sql.Tx, input CreateInput, access AccessScope, now time.Time, nowISO string) (*CreateResult, error) {
	id := s.newI("acc")
	providerCode := strings.TrimSpace(input.ProviderCode)
	if providerCode == "" {
		return nil, &ValidationError{Message: "供应商不能为空"}
	}
	profileID := strings.TrimSpace(input.ProviderProtocolProfileID)
	profile, err := s.requireEnabledProviderProtocolProfile(ctx, tx, providerCode, profileID)
	if err != nil {
		return nil, err
	}
	accountType := strings.TrimSpace(input.AccountType)
	if accountType == "" {
		return nil, &ValidationError{Message: "账户类型不能为空"}
	}
	supported := false
	for _, candidate := range profile.accountTypes {
		if candidate == accountType {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &ValidationError{Message: "供应商协议档案 " + profile.name + " 不支持账户类型 " + accountType}
	}

	// Credentials: normalized through the ported
	// normalizeAccountCredentialsForWrite family, then sealed with the shared
	// AES-GCM envelope plus the fingerprint and mask columns.
	credentials, err := NormalizeAccountCredentialsForWrite(accountType, input.Credentials, &EndpointModeDefaultContext{
		ProviderCode: providerCode,
		AccountType:  accountType,
		ClientCompatibility: deriveOpenAIAccountClientCompatibility(providerCode, accountType, protocolProfileRef{
			ProviderCode:              profile.providerCode,
			ProtocolCode:              profile.protocolCode,
			ProtocolVersion:           profile.protocolVersion,
			ProviderProtocolProfileID: profile.id,
		}),
		ProviderProtocolProfileID: profile.id,
		ProtocolCode:              profile.protocolCode,
		ProtocolVersion:           profile.protocolVersion,
	})
	if err != nil {
		return nil, err
	}
	source, err := requiredAccountCredentialSource(accountType, credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(credentials))
	if err != nil {
		return nil, err
	}
	fingerprint := accountCredentialFingerprint(source)
	mask := MaskSecret(source)

	// OAuth refresh metadata (gpt/openai oauth only).
	accessTokenExpiresAt := sql.NullString{}
	refreshTokenPresent := 0
	if accountType == "oauth" && (providerCode == "gpt" || providerCode == "openai") {
		if accessToken, ok := credentials["access_token"].(string); ok && strings.TrimSpace(accessToken) != "" {
			if expiresAt, ok := credentials["expires_at"].(string); ok && strings.TrimSpace(expiresAt) != "" {
				if canonical, valid := canonicalRFC3339(expiresAt); valid {
					accessTokenExpiresAt = sql.NullString{String: canonical, Valid: true}
				} else {
					return nil, &ValidationError{Message: "时间必须是有效时间字符串"}
				}
			}
		}
		if refreshToken, ok := credentials["refresh_token"].(string); ok && strings.TrimSpace(refreshToken) != "" {
			refreshTokenPresent = 1
		}
	}

	// Account expiry.
	accountExpiresAt := sql.NullString{}
	if input.AccountExpiresAt != nil && strings.TrimSpace(*input.AccountExpiresAt) != "" {
		canonical, valid := canonicalRFC3339(*input.AccountExpiresAt)
		if !valid {
			return nil, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
		}
		accountExpiresAt = sql.NullString{String: canonical, Valid: true}
	}

	// Availability schedule + status derivation.
	schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
	if err != nil {
		return nil, err
	}
	initialStatus := input.Status.Status
	if input.Status.Status == "active" && input.Status.SkipInitialHealthCheck {
		initialStatus = "active"
	} else if input.Status.Status != "disabled" {
		initialStatus = "pending_test"
	}
	expiredByPackage := isAccountExpired(accountExpiresAt.String, now)
	nextStatus := initialStatus
	if !expiredByPackage {
		if override, ok := ScheduleStatus(schedule, now); ok && (initialStatus == "active" || initialStatus == "disabled") {
			nextStatus = override
		}
	} else {
		nextStatus = "disabled"
	}
	schedulable := input.Status.Schedulable
	if expiredByPackage {
		schedulable = false
	}
	lastErrorCode := sql.NullString{}
	lastErrorMessage := sql.NullString{}
	if expiredByPackage {
		lastErrorCode = sql.NullString{String: "account_expired", Valid: true}
		lastErrorMessage = sql.NullString{String: "账户套餐已过期，已自动停用", Valid: true}
	} else if initialStatus == "pending_test" {
		lastErrorMessage = sql.NullString{String: "账户已保存，等待后台健康检查", Valid: true}
	}

	// Supported models + health check model.
	supportedModels := input.SupportedModels
	if supportedModels == nil {
		supportedModels = []string{}
	}
	if err := assertSupportedModelsRequired(supportedModels); err != nil {
		return nil, err
	}
	configuredHealthCheckModel := profile.defaultHealthCheckModel
	if input.HealthCheckModel != nil {
		configuredHealthCheckModel = *input.HealthCheckModel
	}
	if input.HealthCheckModel == nil && !containsString(supportedModels, configuredHealthCheckModel) && len(supportedModels) > 0 {
		configuredHealthCheckModel = supportedModels[0]
	}
	healthCheckModel, err := normalizedHealthCheckModel(configuredHealthCheckModel, supportedModels)
	if err != nil {
		return nil, err
	}
	healthCheckEndpointMode := "chat_json"
	if input.HealthCheckEndpointMode != nil {
		if !accountHealthCheckEndpointModes[*input.HealthCheckEndpointMode] {
			return nil, &ValidationError{Message: "账户参数无效"}
		}
		healthCheckEndpointMode = *input.HealthCheckEndpointMode
	}

	// Gpt request-override catalog assertion (accounts.routes.ts:220): the
	// request-scope system account feeds the personal catalog scope (Node
	// effectiveRequestSystemAccountId). The mapping catalog assertion rides
	// the final owner below (repositories.ts create: the write-side
	// normalizeAccountModelMappingsForProvider call), after the group switch.
	if err := s.assertAccountGptRequestOverridesSupported(ctx, accountGptRequestOverridesInput{
		ProviderCode:    providerCode,
		AccountType:     accountType,
		Credentials:     credentials,
		SupportedModels: supportedModels,
		SystemAccountID: access.viewerID(),
	}); err != nil {
		return nil, err
	}

	// Owner context (the group block below switches the owner for admins
	// binding an explicit group).
	systemAccountID, err := access.ownerID()
	if err != nil {
		return nil, err
	}

	// Group resolution: explicit group (owner switch for managed owners) or
	// the owner's enabled default group for the provider.
	explicitGroupID := ""
	if input.GroupID != nil {
		explicitGroupID = strings.TrimSpace(*input.GroupID)
	}
	var explicitGroup *groupReference
	if explicitGroupID != "" {
		explicitGroup, err = s.groupOwnerAndProvider(ctx, tx, explicitGroupID)
		if err != nil {
			return nil, err
		}
		if explicitGroup != nil && access.canAccessAll() {
			systemAccountID = explicitGroup.systemAccountID
		}
	}
	group := explicitGroup
	if explicitGroupID == "" {
		group, err = s.defaultGroupForWrite(ctx, tx, systemAccountID, providerCode)
		if err != nil {
			return nil, err
		}
	}
	if explicitGroupID != "" && group == nil {
		return nil, &ValidationError{Message: "账户分组无效"}
	}
	if group == nil || group.id == "" {
		return nil, &ValidationError{Message: "当前用户缺少供应商 " + providerCode + " 的启用默认分组"}
	}
	if group.systemAccountID != systemAccountID || group.providerCode != providerCode {
		return nil, &ValidationError{Message: "账户分组无效"}
	}

	// Model mapping catalog assertion (repositories.ts create:1936, the
	// write-side normalizeAccountModelMappingsForProvider): 来源/目标模型必须
	// 落在当前供应商模型目录中且目标模型支持对应上游协议；personal 目录按最终
	// owner scope 读取。
	if err := s.assertAccountModelMappingsInProviderCatalog(ctx, tx, providerCode, systemAccountID, protocolPredicateInput{
		providerCode:              providerCode,
		protocolCode:              profile.protocolCode,
		protocolVersion:           profile.protocolVersion,
		providerProtocolProfileID: profile.id,
	}, input.ModelMappings); err != nil {
		return nil, err
	}

	// Dispatch fields.
	concurrencyLimit := defaultAccountConcurrencyLimit
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit < 1 {
			return nil, &ValidationError{Message: "并发限制必须是大于 0 的整数"}
		}
		concurrencyLimit = *input.ConcurrencyLimit
	}
	priority := 0
	if input.Priority != nil {
		if *input.Priority < 0 {
			return nil, &ValidationError{Message: "优先级必须是大于等于 0 的整数"}
		}
		priority = *input.Priority
	}
	superPriorityEnabled := input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled
	fallbackEnabled := input.FallbackEnabled != nil && *input.FallbackEnabled
	if superPriorityEnabled && fallbackEnabled {
		return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
	}

	// Proxy profile (must exist and stay enabled).
	proxyProfileID := sql.NullString{}
	if input.ProxyProfileID != nil && strings.TrimSpace(*input.ProxyProfileID) != "" {
		enabled, err := s.resolveEnabledProxyProfile(ctx, tx, strings.TrimSpace(*input.ProxyProfileID))
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, &ValidationError{Message: "代理不存在或已停用，请选择一个已启用的代理"}
		}
		proxyProfileID = sql.NullString{String: strings.TrimSpace(*input.ProxyProfileID), Valid: true}
	}

	// Notes.
	notes := sql.NullString{}
	if input.Notes != nil {
		trimmed := strings.TrimSpace(*input.Notes)
		if trimmed != "" {
			notes = sql.NullString{String: trimmed, Valid: true}
		}
	}

	// Balance query columns: the body parser already normalized the config
	// and validated the capability boundary (Node accounts.routes.ts
	// validateAccountBalanceCapability + normalizeAccountBalanceConfig); the
	// write keeps the normalized JSON contract-complete.
	balanceQueryEnabledInt := 0
	balanceQueryConfigJSON := "{}"
	if input.BalanceQueryEnabled {
		if input.BalanceQueryConfigCanonical == nil {
			return nil, &ValidationError{Message: "开启上游余额查询时必须选择查询类型"}
		}
		balanceQueryEnabledInt = 1
		balanceQueryConfigJSON = *input.BalanceQueryConfigCanonical
	}

	// Temporary-unavailable continuous probe switch: absent = enabled (1),
	// explicit false persists 0 (normalizeOptionalBooleanInput fallback true).
	temporaryProbeEnabled := 1
	if input.TemporaryUnavailableContinuousProbeEnabled != nil && !*input.TemporaryUnavailableContinuousProbeEnabled {
		temporaryProbeEnabled = 0
	}

	// Tags.
	tagNames, err := normalizeAccountTagNamesInput(anySliceOrNil(input.Tags))
	if err != nil {
		return nil, err
	}

	scheduleJSONValue := sql.NullString{}
	if rawSchedule, ok := ScheduleJSON(schedule); ok {
		scheduleJSONValue = sql.NullString{String: rawSchedule, Valid: true}
	}
	nextCheckAt := sql.NullString{}
	if rawNextCheck, ok := NextScheduleCheckAt(schedule, now); ok {
		nextCheckAt = sql.NullString{String: rawNextCheck, Valid: true}
	}

	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("accounts")+`
		(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		 oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit,
		 priority, super_priority_enabled, fallback_enabled, client_compatibility, schedulable,
		 availability_schedule_json, availability_schedule_next_check_at, notes, account_expires_at,
		 cooldown_until, last_error_code, last_error_message, health_check_model, health_check_endpoint_mode,
		 balance_query_enabled, balance_query_config_json, temporary_unavailable_continuous_probe_enabled,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, systemAccountID, providerCode, profile.id, profile.protocolCode, profile.protocolVersion,
		strings.TrimSpace(input.Name), accountType, nextStatus, sealed, fingerprint, mask,
		accessTokenExpiresAt, refreshTokenPresent, proxyProfileID, concurrencyLimit,
		priority, boolInt(superPriorityEnabled), boolInt(fallbackEnabled), "openai_standard", boolInt(schedulable),
		scheduleJSONValue, nextCheckAt, notes, accountExpiresAt,
		sql.NullString{}, lastErrorCode, lastErrorMessage, healthCheckModel, healthCheckEndpointMode,
		balanceQueryEnabledInt, balanceQueryConfigJSON, temporaryProbeEnabled,
		nowISO, nowISO); err != nil {
		if duplicate := duplicateAccountNameError(err, strings.TrimSpace(input.Name)); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_accounts")+`
		(system_account_id, group_id, account_id, local_priority, local_super_priority_enabled,
		 local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`),
		systemAccountID, group.id, id, priority, boolInt(superPriorityEnabled), boolInt(fallbackEnabled),
		nowISO, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountSupportedModels(ctx, tx, id, providerCode, supportedModels, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountModelMappings(ctx, tx, id, providerCode, input.ModelMappings, nowISO); err != nil {
		return nil, err
	}
	if err := s.replaceAccountNameSearchTerms(ctx, tx, id, systemAccountID, strings.TrimSpace(input.Name), nowISO); err != nil {
		return nil, err
	}
	if _, err := s.replaceAccountTags(ctx, tx, id, systemAccountID, tagNames, nowISO); err != nil {
		return nil, err
	}
	// dispatchInitialAccountHealthCheck condition (Node
	// account-health-check-dispatch.service.ts): a pending_test account always
	// probes once; a freshly saved active single-Key API Key account without
	// balance query probes once so the balance detector can classify it.
	initialHealthCheck := nextStatus == "pending_test" ||
		(nextStatus == "active" && accountType == "api_key" &&
			EffectiveAccountApiKeyCount(credentials) == 1 &&
			balanceQueryEnabledInt != 1 &&
			balanceQueryConfigJSON == "{}")
	return &CreateResult{
		ID: id, Status: nextStatus, ConfigRevision: 1, DispatchRevision: 1,
		OwnerSystemAccountID: systemAccountID, Name: strings.TrimSpace(input.Name),
		InitialHealthCheckRequired: initialHealthCheck,
	}, nil
}

// CreateResult mirrors the create response payload plus the fields the
// operation log needs. configRevision/dispatchRevision document the initialized
// revision pair (schema defaults 1/1).
type CreateResult struct {
	ID                   string `json:"id"`
	Status               string `json:"status"`
	ConfigRevision       int64  `json:"configRevision"`
	DispatchRevision     int64  `json:"dispatchRevision"`
	OwnerSystemAccountID string `json:"-"`
	Name                 string `json:"-"`
	// InitialHealthCheckRequired mirrors dispatchInitialAccountHealthCheck's
	// activation condition; the Create caller dispatches the probe after the
	// commit (reason='activation').
	InitialHealthCheckRequired bool `json:"-"`
}

func isAccountExpired(accountExpiresAt string, now time.Time) bool {
	if strings.TrimSpace(accountExpiresAt) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, accountExpiresAt)
	return err == nil && parsed.UnixMilli() <= now.UnixMilli()
}

func (s *Store) groupOwnerAndProvider(ctx context.Context, q queryer, groupID string) (*groupReference, error) {
	var row groupReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code, name
		FROM `+s.table("groups")+` WHERE id = ?`), groupID).
		Scan(&row.id, &row.systemAccountID, &row.providerCode, &row.name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.systemAccountID == "" || row.providerCode == "" {
		return nil, nil
	}
	return &row, nil
}

func (s *Store) defaultGroupForWrite(ctx context.Context, q queryer, systemAccountID, providerCode string) (*groupReference, error) {
	var row groupReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, provider_code, name
		FROM `+s.table("groups")+`
		WHERE system_account_id = ?
			AND provider_code = ?
			AND is_default = 1
			AND enabled = 1
		ORDER BY updated_at DESC, id ASC
		LIMIT 1`), systemAccountID, providerCode).
		Scan(&row.id, &row.systemAccountID, &row.providerCode, &row.name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) resolveEnabledProxyProfile(ctx context.Context, q queryer, proxyProfileID string) (bool, error) {
	var id string
	var enabled int64
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, enabled FROM `+s.table("proxy_profiles")+`
		WHERE id = ?`), proxyProfileID).Scan(&id, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

// replaceAccountSupportedModels mirrors replaceAccountSupportedModels.
func (s *Store) replaceAccountSupportedModels(ctx context.Context, q queryer, accountID, providerCode string, models []string, nowISO string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_supported_models")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	for _, model := range models {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_supported_models")+`
			(account_id, provider_code, model, created_at) VALUES (?, ?, ?, ?)`,
			` ON CONFLICT (account_id, model) DO NOTHING`)), accountID, providerCode, model, nowISO); err != nil {
			return err
		}
	}
	return nil
}

// replaceAccountModelMappings mirrors replaceAccountModelMappings (catalog
// validation belongs to the model-validation companion slice).
func (s *Store) replaceAccountModelMappings(ctx context.Context, q queryer, accountID, providerCode string, mappings []ModelMapping, nowISO string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_model_mappings")+` WHERE account_id = ?`), accountID); err != nil {
		return err
	}
	for _, mapping := range mappings {
		enabled := 1
		if mapping.Enabled != nil && !*mapping.Enabled {
			enabled = 0
		}
		if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_model_mappings")+`
			(account_id, provider_code, source_model, source_endpoint_family, upstream_model,
			 upstream_endpoint_family, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account_id, source_model, source_endpoint_family) DO NOTHING`),
			accountID, providerCode, mapping.SourceModel, mapping.SourceEndpointFamily,
			mapping.UpstreamModel, mapping.UpstreamEndpointFamily, enabled, nowISO, nowISO); err != nil {
			return err
		}
	}
	return nil
}

// replaceAccountTags mirrors replaceAccountTags: existing tags are reused by
// (system_account_id, name), missing ones are created with a fresh acctag id.
func (s *Store) replaceAccountTags(ctx context.Context, q queryer, accountID, systemAccountID string, tagNames []string, nowISO string) ([]TagSummary, error) {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tag_bindings")+` WHERE account_id = ?`), accountID); err != nil {
		return nil, err
	}
	tags := []TagSummary{}
	for _, name := range tagNames {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_tags")+`
			(id, system_account_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			` ON CONFLICT (system_account_id, name) DO NOTHING`)),
			s.newI("acctag"), systemAccountID, name, nowISO, nowISO); err != nil {
			return nil, err
		}
		var tag TagSummary
		if err := q.QueryRowContext(ctx, s.bind(`SELECT id, name FROM `+s.table("account_tags")+`
			WHERE system_account_id = ? AND name = ? LIMIT 1`), systemAccountID, name).
			Scan(&tag.ID, &tag.Name); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	for _, tag := range tags {
		if _, err := q.ExecContext(ctx, s.bind(s.insertIgnore(`INSERT INTO `+s.table("account_tag_bindings")+`
			(account_id, tag_id, system_account_id, created_at) VALUES (?, ?, ?, ?)`,
			` ON CONFLICT (account_id, tag_id) DO NOTHING`)),
			accountID, tag.ID, systemAccountID, nowISO); err != nil {
			return nil, err
		}
	}
	return tags, nil
}

// duplicateAccountNameError mirrors isDuplicateAccountNameError.
func duplicateAccountNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_accounts_owner_name_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: accounts.system_account_id, accounts.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.accounts.system_account_id, juhe_business.accounts.name") {
		return &ConflictError{Message: "同一用户下账户名称已存在：" + name}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func anySliceOrNil(values []string) any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// stringSliceToAny adapts an ID slice for IN (...) placeholder lists; unlike
// anySliceOrNil it keeps an empty slice as an empty (never nil) driver slice,
// which is what the delete/tombstone clauses rely on.
func stringSliceToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
