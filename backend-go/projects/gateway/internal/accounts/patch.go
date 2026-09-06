package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// PatchChange mirrors AccountManagementPatchChange.
type PatchChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// PatchResult mirrors the PATCH response payload: { id, configRevision,
// changedFields } plus the fields the operation log needs.
type PatchResult struct {
	ID                   string        `json:"id"`
	ConfigRevision       int64         `json:"configRevision"`
	ChangedFields        []string      `json:"changedFields"`
	Name                 string        `json:"-"`
	OwnerSystemAccountID string        `json:"-"`
	Changes              []PatchChange `json:"-"`
	Tags                 []TagSummary  `json:"-"`
	// HealthCheckRequired mirrors the repository outcome the route uses to
	// dispatch the post-commit configuration probe
	// (account-management-patch.repository.ts healthCheckReason).
	HealthCheckRequired bool   `json:"-"`
	HealthCheckReason   string `json:"-"`
	// GroupChanged mirrors groupChanged (the enabled binding row switched);
	// it feeds the gateway runtime invalidation condition
	// (gatewayRuntimeAffected: groupChanged || credentialsChanged || ...).
	GroupChanged bool `json:"-"`
}

// PatchInput is the validated basic-edit payload: the account-edit-basic
// editable field set plus the expectedConfigRevision optimistic lock. Nil
// pointers mean the field was absent (undefined vs null distinction follows
// the Node optional/nullable schema pairs).
type PatchInput struct {
	ExpectedConfigRevision      int64
	Name                        *string
	Notes                       *string
	Status                      *string
	ConcurrencyLimit            *int
	Priority                    *int
	SuperPriorityEnabled        *bool
	FallbackEnabled             *bool
	Schedulable                 *bool
	Credentials                 Credentials
	CredentialsPresent          bool
	SupportedModels             []string
	SupportedModelsPresent      bool
	HealthCheckModel            *string
	HealthCheckEndpointMode     *string
	Tags                        []string
	TagsPresent                 bool
	AccountExpiresAt            *string
	AccountExpiresAtPresent     bool
	AvailabilitySchedule        any
	AvailabilitySchedulePresent bool
	ClearFailureState           bool
	// ModelMappings mirrors input.modelMappings (accountUpdateSchema).
	ModelMappings        []ModelMapping
	ModelMappingsPresent bool
	// ProxyProfileID mirrors input.proxyProfileId (nullable id tri-state).
	ProxyProfileID        *string
	ProxyProfileIDPresent bool
	// GroupID mirrors input.groupId (required non-empty string when present).
	GroupID        *string
	GroupIDPresent bool
	// Balance fields mirror input.balanceQueryEnabled / balanceQueryConfig;
	// the config arrives already normalized (canonical JSON) from the body
	// parser, like the Node route.
	BalanceQueryEnabled            *bool
	BalanceQueryConfigCanonical    *string
	BalanceQueryConfigPresent      bool
	TemporaryUnavailableContinuousProbeEnabled *bool
}

// accountPatchChangeLabel mirrors accountPatchChangeLabel (credentials.*
// fields collapse to 凭据).
func accountPatchChangeLabel(field string) string {
	if strings.HasPrefix(field, "credentials.") {
		return "凭据"
	}
	switch field {
	case "name":
		return "名称"
	case "notes":
		return "备注"
	case "credentials":
		return "凭据"
	case "status":
		return "状态"
	case "concurrencyLimit":
		return "并发限制"
	case "priority":
		return "优先级"
	case "superPriorityEnabled":
		return "超级优先"
	case "fallbackEnabled":
		return "降级备用"
	case "supportedModels":
		return "支持模型"
	case "healthCheckModel":
		return "检查模型"
	case "healthCheckEndpointMode":
		return "检查协议"
	case "temporaryUnavailableContinuousProbeEnabled":
		return "持续恢复探活"
	case "modelMappings":
		return "模型映射"
	case "tags":
		return "标签"
	case "proxyProfileId":
		return "代理"
	case "schedulable":
		return "参与调度"
	case "accountExpiresAt":
		return "过期时间"
	case "availabilitySchedule":
		return "时间计划"
	case "groupId":
		return "绑定分组"
	case "balanceQueryEnabled":
		return "余额查询"
	case "balanceQueryConfig":
		return "余额查询配置"
	case "clearFailureState":
		return "异常恢复"
	default:
		return field
	}
}

// Patch mirrors patchAccountManagementAsync restricted to the basic editable
// field set: scope-checked row load, config_revision CAS (409 on mismatch),
// field-wise diff, tags/ models/ search-terms maintenance and the revision
// increment. Returns (nil, nil) when the account is missing or outside the
// access scope.
func (s *Store) Patch(ctx context.Context, accountID string, input PatchInput, access AccessScope) (*PatchResult, error) {
	ctx = ensureCtx(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, &ValidationError{Message: "账户配置版本无效"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	scoped := access.manageableID()
	scopeClause := ""
	args := []any{strings.TrimSpace(accountID)}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var row struct {
		id                        string
		configRevision            int64
		systemAccountID           string
		name                      string
		notes                     sql.NullString
		accountType               string
		credentialsEncrypted      string
		status                    string
		concurrencyLimit          int
		priority                  int
		superPriorityEnabled      int
		fallbackEnabled           int
		schedulable               int
		availabilitySchedule      sql.NullString
		accountExpiresAt          sql.NullString
		lastErrorCode             sql.NullString
		lastErrorMessage          sql.NullString
		lastErrorTraceID          sql.NullString
		cooldownUntil             sql.NullString
		healthCheckModel          string
		healthCheckEndpointMode   string
		providerCode              string
		providerProtocolProfileID string
		protocolCode              string
		protocolVersion           string
		clientCompatibility       string
		proxyProfileID            sql.NullString
		balanceQueryEnabled       int
		balanceQueryConfigJSON    string
		balanceQueryNextRefreshAt sql.NullString
		temporaryProbeEnabled     int
		nextHealthCheckAt         sql.NullString
		lastHealthCheckAt         sql.NullString
		lastHealthSuccessAt       sql.NullString
		healthCheckFailureCount   int
		healthCheckFailureStart   sql.NullString
		lastHealthStatusCode      sql.NullInt64
		lastHealthErrorCode       sql.NullString
		lastHealthErrorMessage    sql.NullString
		lastHealthTraceID         sql.NullString
		authorizationID           sql.NullString
	}
	err = tx.QueryRowContext(ctx, s.bind(`SELECT accounts.id, accounts.config_revision,
			accounts.system_account_id, accounts.name, accounts.notes, accounts.type,
			accounts.credentials_encrypted, accounts.status, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.schedulable, accounts.availability_schedule_json, accounts.account_expires_at,
			accounts.last_error_code, accounts.last_error_message, accounts.last_error_trace_id,
			accounts.cooldown_until, accounts.health_check_model, accounts.health_check_endpoint_mode,
			accounts.provider_code, accounts.provider_protocol_profile_id, accounts.protocol_code,
			accounts.protocol_version, accounts.client_compatibility, accounts.proxy_profile_id,
			accounts.balance_query_enabled, accounts.balance_query_config_json,
			accounts.balance_query_next_refresh_at,
			accounts.temporary_unavailable_continuous_probe_enabled, accounts.next_health_check_at,
			accounts.last_health_check_at, accounts.last_health_success_at,
			accounts.health_check_failure_count, accounts.health_check_failure_started_at,
			accounts.last_health_check_status_code, accounts.last_health_check_error_code,
			accounts.last_health_check_error_message, accounts.last_health_check_trace_id,
			accounts.authorization_instance_authorization_id
		FROM `+s.table("accounts")+` accounts
		WHERE accounts.id = ?
			AND accounts.deleted_at IS NULL`+scopeClause+`
		LIMIT 1`), args...).Scan(
		&row.id, &row.configRevision, &row.systemAccountID, &row.name, &row.notes,
		&row.accountType, &row.credentialsEncrypted, &row.status, &row.concurrencyLimit,
		&row.priority, &row.superPriorityEnabled, &row.fallbackEnabled, &row.schedulable,
		&row.availabilitySchedule, &row.accountExpiresAt, &row.lastErrorCode,
		&row.lastErrorMessage, &row.lastErrorTraceID, &row.cooldownUntil,
		&row.healthCheckModel, &row.healthCheckEndpointMode, &row.providerCode,
		&row.providerProtocolProfileID, &row.protocolCode, &row.protocolVersion,
		&row.clientCompatibility, &row.proxyProfileID, &row.balanceQueryEnabled,
		&row.balanceQueryConfigJSON, &row.balanceQueryNextRefreshAt, &row.temporaryProbeEnabled,
		&row.nextHealthCheckAt,
		&row.lastHealthCheckAt, &row.lastHealthSuccessAt, &row.healthCheckFailureCount,
		&row.healthCheckFailureStart, &row.lastHealthStatusCode, &row.lastHealthErrorCode,
		&row.lastHealthErrorMessage, &row.lastHealthTraceID, &row.authorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && row.systemAccountID != access.ViewerID {
		return nil, nil
	}
	if row.configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}

	now := s.now()
	nowISO := isoMillis(now)
	changes := []PatchChange{}
	addChange := func(field string, before, after any) {
		changes = append(changes, PatchChange{Field: field, Before: before, After: after})
	}
	sets := []string{}
	setArgs := []any{}

	// Health-relevant change tracking (Node account-management-patch.repository.ts
	// :356-360, 575-582, 677-694): a connection change (proxy switch, base URL
	// change or API Key pool membership rotation) resets the persisted health
	// projection; credentials/proxy/supported-models/model-mappings/health
	// check config changes clear next_health_check_at and the route dispatches
	// the post-commit configuration probe.
	credentialsChanged := false
	endpointModesChanged := false
	connectionChanged := false
	supportedModelsChanged := false
	modelMappingsChanged := false
	healthCheckModelChanged := false
	healthCheckEndpointModeChanged := false

	if input.ClearFailureState {
		addChange("clearFailureState", false, true)
		sets = append(sets,
			"last_error_code = NULL", "last_error_message = NULL", "last_error_trace_id = NULL",
			"cooldown_until = NULL", "health_check_failure_count = 0", "health_check_failure_started_at = NULL",
			"cooldown_retest_failure_count = 0", "cooldown_retest_observation_started_at = NULL",
			// 归档 patchAccountFailureStateInTransaction：观察起点清空时代际同步
			// 清空；悬挂代际会让 jobs direct input 把候选判为 cooldown fence 无效。
			"cooldown_retest_generation = NULL",
			"cooldown_retest_last_at = NULL", "cooldown_retest_last_status_code = NULL")
	}

	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, &ValidationError{Message: "账户名称不能为空"}
		}
		if len([]rune(name)) > maxAccountNameLength {
			return nil, &ValidationError{Message: "账户名称不能超过 128 个字符"}
		}
		if name != row.name {
			addChange("name", row.name, name)
			sets = append(sets, "name = ?")
			setArgs = append(setArgs, name)
		}
	}
	if input.Notes != nil {
		trimmed := strings.TrimSpace(*input.Notes)
		var next sql.NullString
		if trimmed != "" {
			next = sql.NullString{String: trimmed, Valid: true}
		}
		if next.Valid != row.notes.Valid || next.String != row.notes.String {
			addChange("notes", nullPtrString(row.notes), nullPtrString(next))
			sets = append(sets, "notes = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.Status != nil {
		if !accountStatusValues[*input.Status] {
			return nil, &ValidationError{Message: "账户状态无效"}
		}
		if *input.Status != row.status {
			addChange("status", row.status, *input.Status)
			sets = append(sets, "status = ?")
			setArgs = append(setArgs, *input.Status)
		}
	}
	if input.ConcurrencyLimit != nil {
		if *input.ConcurrencyLimit < 1 {
			return nil, &ValidationError{Message: "并发限制必须是大于 0 的整数"}
		}
		if *input.ConcurrencyLimit != row.concurrencyLimit {
			addChange("concurrencyLimit", row.concurrencyLimit, *input.ConcurrencyLimit)
			sets = append(sets, "concurrency_limit = ?")
			setArgs = append(setArgs, *input.ConcurrencyLimit)
		}
	}
	if input.Priority != nil {
		if *input.Priority < 0 {
			return nil, &ValidationError{Message: "优先级必须是大于等于 0 的整数"}
		}
		if *input.Priority != row.priority {
			addChange("priority", row.priority, *input.Priority)
			sets = append(sets, "priority = ?")
			setArgs = append(setArgs, *input.Priority)
		}
	}
	if input.SuperPriorityEnabled != nil {
		next := boolInt(*input.SuperPriorityEnabled)
		if next != row.superPriorityEnabled {
			if next == 1 && boolInt(input.FallbackEnabled != nil && *input.FallbackEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("superPriorityEnabled", row.superPriorityEnabled == 1, *input.SuperPriorityEnabled)
			sets = append(sets, "super_priority_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.FallbackEnabled != nil {
		next := boolInt(*input.FallbackEnabled)
		if next != row.fallbackEnabled {
			if next == 1 && boolInt(input.SuperPriorityEnabled != nil && *input.SuperPriorityEnabled) == 1 {
				return nil, &ValidationError{Message: "超级优先和降级备用不能同时开启"}
			}
			addChange("fallbackEnabled", row.fallbackEnabled == 1, *input.FallbackEnabled)
			sets = append(sets, "fallback_enabled = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.Schedulable != nil {
		next := boolInt(*input.Schedulable)
		if next != row.schedulable {
			addChange("schedulable", row.schedulable == 1, *input.Schedulable)
			sets = append(sets, "schedulable = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.AccountExpiresAtPresent {
		var next sql.NullString
		if input.AccountExpiresAt != nil && strings.TrimSpace(*input.AccountExpiresAt) != "" {
			canonical, valid := canonicalRFC3339(*input.AccountExpiresAt)
			if !valid {
				return nil, &ValidationError{Message: "账户套餐到期时间必须是有效时间字符串"}
			}
			next = sql.NullString{String: canonical, Valid: true}
		}
		if next.Valid != row.accountExpiresAt.Valid || next.String != row.accountExpiresAt.String {
			addChange("accountExpiresAt", nullPtrString(row.accountExpiresAt), nullPtrString(next))
			sets = append(sets, "account_expires_at = ?")
			setArgs = append(setArgs, next)
		}
	}
	if input.AvailabilitySchedulePresent {
		schedule, err := NormalizeSchedule(input.AvailabilitySchedule)
		if err != nil {
			return nil, err
		}
		var next sql.NullString
		if raw, ok := ScheduleJSON(schedule); ok {
			next = sql.NullString{String: raw, Valid: true}
		}
		if next.Valid != row.availabilitySchedule.Valid || next.String != row.availabilitySchedule.String {
			addChange("availabilitySchedule", parseScheduleOrNull(row.availabilitySchedule), schedule)
			sets = append(sets, "availability_schedule_json = ?", "availability_schedule_next_check_at = ?")
			setArgs = append(setArgs, next, scheduleNextCheckArg(schedule, now))
		}
	}

	// Proxy profile: nullable id tri-state; a switch must resolve to an
	// enabled profile (Node resolveEnabledProxyProfileIdInClient →
	// ProxyProfileUnavailableError). Counts as a connection change.
	if input.ProxyProfileIDPresent {
		var requested *string
		if input.ProxyProfileID != nil {
			trimmed := strings.TrimSpace(*input.ProxyProfileID)
			if trimmed == "" {
				return nil, &ValidationError{Message: "代理配置无效"}
			}
			requested = &trimmed
		}
		current := row.proxyProfileID
		changed := (requested == nil) != current.Valid ||
			(requested != nil && (!current.Valid || *requested != current.String))
		if changed {
			var next sql.NullString
			if requested != nil {
				enabled, err := s.resolveEnabledProxyProfile(ctx, tx, *requested)
				if err != nil {
					return nil, err
				}
				if !enabled {
					return nil, &ValidationError{Message: "代理不存在或已停用，请选择一个已启用的代理"}
				}
				next = sql.NullString{String: *requested, Valid: true}
			}
			connectionChanged = true
			addChange("proxyProfileId", nullPtrString(current), nullPtrString(next))
			sets = append(sets, "proxy_profile_id = ?")
			setArgs = append(setArgs, next)
		}
	}

	// Health check model: must remain inside the supported model set.
	if input.HealthCheckModel != nil || input.HealthCheckEndpointMode != nil || input.SupportedModelsPresent {
		supportedModels := []string{}
		modelRows, err := tx.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+`
			WHERE account_id = ? ORDER BY model ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for modelRows.Next() {
			var model string
			if err := modelRows.Scan(&model); err != nil {
				modelRows.Close()
				return nil, err
			}
			supportedModels = append(supportedModels, model)
		}
		modelRows.Close()
		if err := modelRows.Err(); err != nil {
			return nil, err
		}
		if input.SupportedModelsPresent {
			next, err := normalizeSupportedModelsInput(anySliceOrNil(input.SupportedModels))
			if err != nil {
				return nil, err
			}
			if err := assertSupportedModelsRequired(next); err != nil {
				return nil, err
			}
			if !stringSlicesEqual(supportedModels, next) {
				addChange("supportedModels", supportedModels, next)
				if err := s.replaceAccountSupportedModels(ctx, tx, row.id, row.providerCode, next, nowISO); err != nil {
					return nil, err
				}
				supportedModels = next
				supportedModelsChanged = true
			}
		}
		if input.HealthCheckModel != nil {
			next, err := normalizedHealthCheckModel(*input.HealthCheckModel, supportedModels)
			if err != nil {
				return nil, err
			}
			if next != strings.TrimSpace(row.healthCheckModel) {
				addChange("healthCheckModel", strings.TrimSpace(row.healthCheckModel), next)
				sets = append(sets, "health_check_model = ?")
				setArgs = append(setArgs, next)
				healthCheckModelChanged = true
			}
		}
		if input.HealthCheckEndpointMode != nil {
			if !accountHealthCheckEndpointModes[*input.HealthCheckEndpointMode] {
				return nil, &ValidationError{Message: "账户参数无效"}
			}
			if *input.HealthCheckEndpointMode != row.healthCheckEndpointMode {
				addChange("healthCheckEndpointMode", row.healthCheckEndpointMode, *input.HealthCheckEndpointMode)
				sets = append(sets, "health_check_endpoint_mode = ?")
				setArgs = append(setArgs, *input.HealthCheckEndpointMode)
				healthCheckEndpointModeChanged = true
			}
		}
	}

	// Model mappings: schema-validated (endpoint family enums in the body
	// parser), diffed against the persisted rows and replaced in-place
	// (account-management-patch.repository.ts modelMappingsChanged).
	if input.ModelMappingsPresent {
		currentMappings, err := s.loadAccountModelMappings(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		if !modelMappingsEqual(currentMappings, input.ModelMappings) {
			addChange("modelMappings", currentMappings, input.ModelMappings)
			if err := s.replaceAccountModelMappings(ctx, tx, row.id, row.providerCode, input.ModelMappings, nowISO); err != nil {
				return nil, err
			}
			modelMappingsChanged = true
		}
	}

	// Credentials: editable-key merge into the decrypted record, normalize
	// through the ported normalizeAccountCredentialsForWrite family, then
	// re-seal with fresh fingerprint/mask columns when the deep-equal
	// comparison reports a change (Node account-management-patch.repository.ts
	// :336-360). A base URL change or a Key pool membership rotation counts as
	// a connection change; a supported_endpoint_modes change feeds the health
	// reset below.
	var currentCredentials, nextCredentials Credentials
	haveCredentials := false
	if input.CredentialsPresent {
		current := Credentials{}
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &current); err != nil {
			return nil, err
		}
		next := Credentials{}
		for key, value := range current {
			next[key] = value
		}
		for key, value := range input.Credentials {
			next[key] = value
		}
		normalized, err := NormalizeAccountCredentialsForWrite(row.accountType, next, &EndpointModeDefaultContext{
			ProviderCode:              row.providerCode,
			AccountType:               row.accountType,
			ClientCompatibility:       row.clientCompatibility,
			ProviderProtocolProfileID: row.providerProtocolProfileID,
			ProtocolCode:              row.protocolCode,
			ProtocolVersion:           row.protocolVersion,
		})
		if err != nil {
			return nil, err
		}
		currentCredentials = current
		haveCredentials = true
		if !credentialsDeepEqual(current, normalized) {
			credentialsChanged = true
			nextCredentials = normalized
			if credentialValueJSONText(current["base_url"]) != credentialValueJSONText(normalized["base_url"]) {
				connectionChanged = true
			}
			if !accountApiKeyPoolMembershipEqual(current, normalized) {
				connectionChanged = true
			}
			if credentialValueJSONText(current["supported_endpoint_modes"]) != credentialValueJSONText(normalized["supported_endpoint_modes"]) {
				endpointModesChanged = true
			}
			source, err := requiredAccountCredentialSource(row.accountType, normalized)
			if err != nil {
				return nil, err
			}
			sealed, err := EncryptJSON(s.secret, map[string]any(normalized))
			if err != nil {
				return nil, err
			}
			fingerprint := accountCredentialFingerprint(source)
			addChange("credentials", "已设置", "已变更")
			sets = append(sets, "credentials_encrypted = ?", "credential_fingerprint = ?", "credential_mask = ?")
			setArgs = append(setArgs, sealed, fingerprint, MaskSecret(source))
		}
	}

	// Provider model-catalog validations (account-management-patch.repository.ts
	// :445-452 + :667 + normalizedModelMappingsForPatch): the gpt
	// request-override assertion rides the final credential record and the
	// final supported-model set whenever credentials or supportedModels
	// participate; the mapping catalog assertion rides the final mapping set
	// when modelMappings are present (with an actual change) or the enabled
	// endpoint modes changed. Assertion failures roll the transaction back,
	// so running it after the satellite writes stays observation-equivalent
	// to the Node pre-write ordering.
	if input.CredentialsPresent || input.SupportedModelsPresent {
		finalCredentials := Credentials{}
		if input.CredentialsPresent && credentialsChanged {
			finalCredentials = nextCredentials
		} else {
			// 未变化（或无凭据输入）时归档断言用归一化后/当前凭据：normalized
			// 与 current 深相等，解密 current 即可。
			if err := DecryptJSON(s.secret, row.credentialsEncrypted, &finalCredentials); err != nil {
				return nil, err
			}
		}
		finalSupportedModels := []string{}
		modelRows, err := tx.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+`
			WHERE account_id = ? ORDER BY model ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for modelRows.Next() {
			var model string
			if err := modelRows.Scan(&model); err != nil {
				modelRows.Close()
				return nil, err
			}
			finalSupportedModels = append(finalSupportedModels, model)
		}
		modelRows.Close()
		if err := modelRows.Err(); err != nil {
			return nil, err
		}
		if err := s.assertAccountGptRequestOverridesSupported(ctx, accountGptRequestOverridesInput{
			ProviderCode:    row.providerCode,
			AccountType:     row.accountType,
			Credentials:     finalCredentials,
			SupportedModels: finalSupportedModels,
			SystemAccountID: row.systemAccountID,
		}); err != nil {
			return nil, err
		}
	}
	mappingValidationSource := []ModelMapping{}
	mappingValidationNeeded := false
	if input.ModelMappingsPresent {
		currentMappingsForValidation, err := s.loadAccountModelMappings(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		mappingValidationNeeded = len(input.ModelMappings) > 0 &&
			(endpointModesChanged || !modelMappingsEqual(currentMappingsForValidation, input.ModelMappings))
		mappingValidationSource = input.ModelMappings
	} else if endpointModesChanged {
		currentMappingsForValidation, err := s.loadAccountModelMappings(ctx, tx, row.id)
		if err != nil {
			return nil, err
		}
		mappingValidationNeeded = len(currentMappingsForValidation) > 0
		mappingValidationSource = currentMappingsForValidation
	}
	if mappingValidationNeeded {
		if err := s.assertAccountModelMappingsInProviderCatalog(ctx, tx, row.providerCode, row.systemAccountID, protocolPredicateInput{
			providerCode: row.providerCode,
			protocolCode: row.protocolCode,
			// 归档 protocolProfileFromRow(row) 不带 profile id；成员判定只用
			// providerCode + protocolCode + protocolVersion。
			protocolVersion: row.protocolVersion,
		}, mappingValidationSource); err != nil {
			return nil, err
		}
	}

	// Temporary-unavailable continuous probe switch (Node
	// :643-694): absent keeps the current flag; turning it off while the
	// account sits in temporary_unavailable arms the bounded recovery window.
	probeSwitchNext := 0
	probeSwitchChanged := false
	boundedRecoveryActivated := false
	boundedRecoveryGeneration := ""
	if input.TemporaryUnavailableContinuousProbeEnabled != nil {
		next := 0
		if *input.TemporaryUnavailableContinuousProbeEnabled {
			next = 1
		}
		if next != row.temporaryProbeEnabled {
			probeSwitchChanged = true
			probeSwitchNext = next
			// Node boundedRecoveryActivated = current && !next（与来源账户
			// 自身状态无关；实例 UPDATE 的 CASE WHEN 按每行自己的 status 判定）。
			boundedRecoveryActivated = row.temporaryProbeEnabled == 1 && next == 0
			boundedRecoveryGeneration = ""
			if boundedRecoveryActivated {
				boundedRecoveryGeneration = newCooldownGeneration()
			}
			addChange("temporaryUnavailableContinuousProbeEnabled", row.temporaryProbeEnabled == 1, *input.TemporaryUnavailableContinuousProbeEnabled)
			sets = append(sets, "temporary_unavailable_continuous_probe_enabled = ?")
			setArgs = append(setArgs, next)
			if row.temporaryProbeEnabled == 1 && next == 0 && row.status == "temporary_unavailable" {
				// 热修（归档 account-management-patch.repository.ts
				// boundedRecoveryActivated / restartBoundedRecoveryObservation）：
				// bounded recovery 观察窗口重启时写入新生成代际，替代旧的
				// 无条件 NULL——丢失代际会让 jobs 侧冷却恢复候选无法通过
				// fence 认领，恢复任务无法续跑。
				sets = append(sets,
					"cooldown_retest_failure_count = 0",
					"cooldown_retest_observation_started_at = ?",
					"cooldown_retest_generation = ?",
					"cooldown_retest_last_at = NULL",
					"cooldown_retest_last_status_code = NULL")
				setArgs = append(setArgs, nowISO, boundedRecoveryGeneration)
				if cooldown := initialCooldownUntilForStatus("temporary_unavailable", now); cooldown != "" {
					sets = append(sets, "cooldown_until = ?")
					setArgs = append(setArgs, cooldown)
				}
			}
		}
	}

	// Balance query (Node :712-773): any balance-relevant change revalidates
	// the capability boundary, writes the enabled flag plus the normalized
	// config and refreshes the next-refresh generation when the balance
	// identity changed.
	if input.BalanceQueryEnabled != nil || input.BalanceQueryConfigPresent || credentialsChanged {
		requestedEnabled := row.balanceQueryEnabled == 1
		if input.BalanceQueryEnabled != nil {
			requestedEnabled = *input.BalanceQueryEnabled
		}
		currentCredentialsForBalance := currentCredentials
		_ = haveCredentials
		if credentialsChanged {
			currentCredentialsForBalance = nextCredentials
		}
		authorizedInstance := row.authorizationID.Valid && strings.TrimSpace(row.authorizationID.String) != ""
		enabled, err := ValidateAccountBalanceCapability(BalanceCapabilityInput{
			AccountType:        row.accountType,
			Credentials:        currentCredentialsForBalance,
			AuthorizedInstance: authorizedInstance,
		}, requestedEnabled)
		if err != nil {
			return nil, err
		}
		currentBalanceConfig, err := parseStoredBalanceConfig(row.balanceQueryConfigJSON)
		if err != nil {
			return nil, err
		}
		var nextBalanceConfig map[string]any
		if input.BalanceQueryConfigPresent {
			nextBalanceConfig, err = parseStoredBalanceConfig(*input.BalanceQueryConfigCanonical)
			if err != nil {
				return nil, err
			}
		} else {
			nextBalanceConfig = currentBalanceConfig
		}
		if enabled && nextBalanceConfig == nil {
			return nil, &ValidationError{Message: "开启上游余额查询时必须选择查询类型"}
		}
		nextProxyProfileID := row.proxyProfileID.String
		if input.ProxyProfileIDPresent {
			if input.ProxyProfileID == nil {
				nextProxyProfileID = ""
			} else {
				nextProxyProfileID = strings.TrimSpace(*input.ProxyProfileID)
			}
		}
		nextProxyValue := any(nil)
		if nextProxyProfileID != "" {
			nextProxyValue = nextProxyProfileID
		}
		identityChanged := !balanceIdentityEqual(
			s.balanceIdentityValue(row.balanceQueryEnabled == 1, currentBalanceConfig, row.providerCode,
				row.accountType, currentCredentialsForBalance, row.proxyProfileID.String),
			s.balanceIdentityValue(enabled, nextBalanceConfig, row.providerCode,
				row.accountType, currentCredentialsForBalance, nextProxyValue),
		)
		if enabled != (row.balanceQueryEnabled == 1) {
			addChange("balanceQueryEnabled", row.balanceQueryEnabled == 1, enabled)
			sets = append(sets, "balance_query_enabled = ?")
			setArgs = append(setArgs, boolInt(enabled))
		}
		if credentialValueJSONText(currentBalanceConfig) != credentialValueJSONText(nextBalanceConfig) {
			addChange("balanceQueryConfig", currentBalanceConfig, nextBalanceConfig)
			raw := "{}"
			if nextBalanceConfig != nil {
				encoded, err := canonicalBalanceConfigJSON(nextBalanceConfig)
				if err != nil {
					return nil, err
				}
				raw = encoded
			}
			sets = append(sets, "balance_query_config_json = ?")
			setArgs = append(setArgs, raw)
		}
		// next refresh generation: a changed identity schedules an immediate
		// refresh, a kept identity preserves the schedule, a disabled query
		// clears it.
		nextRefreshAt := sql.NullString{}
		if enabled {
			if identityChanged {
				nextRefreshAt = sql.NullString{String: nowISO, Valid: true}
			} else if row.balanceQueryNextRefreshAt.Valid {
				nextRefreshAt = row.balanceQueryNextRefreshAt
			}
		}
		if nextRefreshAt.Valid != row.balanceQueryNextRefreshAt.Valid || nextRefreshAt.String != row.balanceQueryNextRefreshAt.String {
			sets = append(sets, "balance_query_next_refresh_at = ?")
			setArgs = append(setArgs, nextRefreshAt)
		}
	}

	// Tags: replace through the shared tag maintenance.
	var savedTags []TagSummary
	if input.TagsPresent {
		tagNames, err := normalizeAccountTagNamesInput(anySliceOrNil(input.Tags))
		if err != nil {
			return nil, err
		}
		currentTags := []TagSummary{}
		tagRows, err := tx.QueryContext(ctx, s.bind(`SELECT account_tags.id, account_tags.name
			FROM `+s.table("account_tag_bindings")+` account_tag_bindings
			INNER JOIN `+s.table("account_tags")+` account_tags
				ON account_tags.id = account_tag_bindings.tag_id
			WHERE account_tag_bindings.account_id = ?
			ORDER BY account_tags.name ASC, account_tags.id ASC`), row.id)
		if err != nil {
			return nil, err
		}
		for tagRows.Next() {
			var tag TagSummary
			if err := tagRows.Scan(&tag.ID, &tag.Name); err != nil {
				tagRows.Close()
				return nil, err
			}
			currentTags = append(currentTags, tag)
		}
		tagRows.Close()
		if err := tagRows.Err(); err != nil {
			return nil, err
		}
		if !tagListsEqual(currentTags, tagNames) {
			addChange("tags", currentTags, tagNames)
			savedTags, err = s.replaceAccountTags(ctx, tx, row.id, row.systemAccountID, tagNames, nowISO)
			if err != nil {
				return nil, err
			}
		}
	}

	// Group binding (Node :701-710, 1589-1603, 1606-1635): the requested
	// group must exist, stay enabled and match the account owner + provider;
	// the enabled binding row is replaced after the CAS update with the
	// account's dispatch fields preserved.
	groupBindingTarget := ""
	groupChanged := false
	if input.GroupIDPresent {
		requested := strings.TrimSpace(*input.GroupID)
		if requested == "" {
			return nil, &ValidationError{Message: "账户分组不能为空"}
		}
		currentGroupID, err := s.loadEnabledGroupBindingID(ctx, tx, row.id, row.systemAccountID)
		if err != nil {
			return nil, err
		}
		if requested != currentGroupID {
			if err := s.assertGroupCanBind(ctx, tx, requested, row.systemAccountID, row.providerCode); err != nil {
				return nil, err
			}
			var before any
			if currentGroupID != "" {
				before = currentGroupID
			}
			addChange("groupId", before, requested)
			groupBindingTarget = requested
			groupChanged = true
		}
	}

	// Health state reset (Node :677-694): any health-relevant change clears
	// the next scheduled check; a connection change also wipes the persisted
	// health projection so the next probe starts clean.
	healthCheckRequired := connectionChanged || supportedModelsChanged || modelMappingsChanged ||
		healthCheckModelChanged || healthCheckEndpointModeChanged || endpointModesChanged
	if healthCheckRequired {
		sets = append(sets, "next_health_check_at = NULL")
	}
	if connectionChanged {
		sets = append(sets,
			"last_health_check_at = NULL",
			"last_health_success_at = NULL",
			"health_check_failure_count = 0",
			"health_check_failure_started_at = NULL",
			"last_health_check_status_code = NULL",
			"last_health_check_error_code = NULL",
			"last_health_check_error_message = NULL",
			"last_health_check_trace_id = NULL")
	}

	result := &PatchResult{
		ID:                   row.id,
		ConfigRevision:       row.configRevision,
		ChangedFields:        []string{},
		Name:                 row.name,
		OwnerSystemAccountID: row.systemAccountID,
		Changes:              changes,
	}
	if len(changes) == 0 {
		return result, nil
	}
	// config_revision = config_revision + 1 with the CAS guard re-checked.
	sets = append(sets, "config_revision = config_revision + 1", "updated_at = ?")
	setArgs = append(setArgs, nowISO)
	updateArgs := append(append([]any{}, setArgs...), row.id, row.configRevision)
	exec, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		`+strings.Join(sets, ", ")+`
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`), updateArgs...)
	if err != nil {
		if duplicate := duplicateAccountNameError(err, row.name); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if affected, _ := exec.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: RevisionConflictMessage}
	}
	// 授权实例探活开关传播链（归档 account-management-patch.repository.ts
	// :805-843 continuousProbeChanged 臂）：来源账户翻转
	// temporaryUnavailableContinuousProbeEnabled 时，同一事务内对每个授权
	// 实例（authorization_instance_source_account_id = 来源 id）跟进同款
	// UPDATE——开关值跟随、config_revision +1，且 bounded recovery 激活时
	// 对 temporary_unavailable 实例重置冷却重试观察窗口（CASE WHEN 逐字段
	// 移植；非 temporary_unavailable 实例的冷却列保持原值）。归档侧还将
	// 实例 id 归入 renamedAuthorizationInstanceIds 驱动事后 per-instance
	// lookup 缓存失效——Go lookup 失效通道是登记过的 no-op hook
	// （invalidation.go），网关运行时缓存经来源账户已翻转的
	// gatewayRuntimeAffected 整面失效，无需额外通道。
	if probeSwitchChanged {
		if err := s.propagateProbeSwitchToAuthorizationInstances(ctx, tx, row.id, probeSwitchNext,
			boundedRecoveryActivated, boundedRecoveryGeneration, now, nowISO); err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		result.ChangedFields = append(result.ChangedFields, change.Field)
	}
	result.ConfigRevision = row.configRevision + 1
	result.Tags = savedTags
	if healthCheckRequired {
		result.HealthCheckRequired = true
		result.HealthCheckReason = "configuration"
	}
	result.GroupChanged = groupChanged
	if groupChanged {
		if err := s.replaceGroupBinding(ctx, tx, row.id, row.systemAccountID, row.authorizationID,
			groupBindingTarget, row.priority, row.superPriorityEnabled == 1, row.fallbackEnabled == 1, nowISO); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Post-commit invalidation (T2 audit; Node
	// account-management-patch.repository.ts:1877-1896): conditional lookup
	// flush + gateway runtime invalidation, best-effort.
	s.finishPatchSideEffects(result)
	return result, nil
}

// propagateProbeSwitchToAuthorizationInstances mirrors the Node
// continuousProbeChanged arm (:805-843): lock the authorization instances of
// the source account (Postgres FOR UPDATE), then fan the switch across them
// in the same transaction — flag follows the source, config_revision bumps,
// and an activated bounded recovery window resets the cooldown-retry
// observation fields for temporary_unavailable instances only (CASE WHEN
// keeps other statuses untouched).
func (s *Store) propagateProbeSwitchToAuthorizationInstances(ctx context.Context, tx *sql.Tx, sourceAccountID string, next int, activated bool, generation string, now time.Time, nowISO string) error {
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+`
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL
		ORDER BY id ASC`+lockSuffix), sourceAccountID)
	if err != nil {
		return err
	}
	instanceIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		instanceIDs = append(instanceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(instanceIDs) == 0 {
		return nil
	}
	activatedFlag := 0
	if activated {
		activatedFlag = 1
	}
	observationStarted := any(nil)
	if activated {
		observationStarted = nowISO
	}
	cooldownGeneration := any(nil)
	if activated && generation != "" {
		cooldownGeneration = generation
	}
	cooldownUntil := any(nil)
	if activated {
		if cooldown := initialCooldownUntilForStatus("temporary_unavailable", now); cooldown != "" {
			cooldownUntil = cooldown
		}
	}
	_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		temporary_unavailable_continuous_probe_enabled = ?,
		config_revision = config_revision + 1,
		cooldown_retest_failure_count = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN 0 ELSE cooldown_retest_failure_count END,
		cooldown_retest_observation_started_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_observation_started_at END,
		cooldown_retest_generation = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_retest_generation END,
		cooldown_retest_last_at = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_at END,
		cooldown_retest_last_status_code = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN NULL ELSE cooldown_retest_last_status_code END,
		cooldown_until = CASE WHEN ? = 1 AND status = 'temporary_unavailable' THEN ? ELSE cooldown_until END,
		updated_at = ?
		WHERE authorization_instance_source_account_id = ?
			AND deleted_at IS NULL`),
		next,
		activatedFlag, activatedFlag, observationStarted,
		activatedFlag, cooldownGeneration,
		activatedFlag,
		activatedFlag,
		activatedFlag, cooldownUntil,
		nowISO,
		sourceAccountID)
	return err
}

func scheduleNextCheckArg(schedule *AvailabilitySchedule, now time.Time) sql.NullString {
	if raw, ok := NextScheduleCheckAt(schedule, now); ok {
		return sql.NullString{String: raw, Valid: true}
	}
	return sql.NullString{}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func tagListsEqual(tags []TagSummary, names []string) bool {
	if len(tags) != len(names) {
		return false
	}
	for index := range tags {
		if tags[index].Name != names[index] {
			return false
		}
	}
	return true
}

// loadAccountModelMappings mirrors loadModelMappingsInClient: the persisted
// mapping rows projected in the AccountModelMapping shape.
func (s *Store) loadAccountModelMappings(ctx context.Context, q queryer, accountID string) ([]ModelMapping, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled
		FROM `+s.table("account_model_mappings")+`
		WHERE account_id = ?
		ORDER BY source_model ASC, source_endpoint_family ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mappings := []ModelMapping{}
	for rows.Next() {
		var mapping ModelMapping
		var enabled int
		if err := rows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily,
			&mapping.UpstreamModel, &mapping.UpstreamEndpointFamily, &enabled); err != nil {
			return nil, err
		}
		if enabled == 1 {
			enabledFlag := true
			mapping.Enabled = &enabledFlag
		} else {
			disabledFlag := false
			mapping.Enabled = &disabledFlag
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

// accountApiKeyPoolMembershipEqual mirrors accountApiKeyFingerprintSetsEqual:
// the effective (trimmed, deduplicated) Key pools must match.
func accountApiKeyPoolMembershipEqual(left, right Credentials) bool {
	leftKeys := EffectiveAccountApiKeys(left)
	rightKeys := EffectiveAccountApiKeys(right)
	if len(leftKeys) != len(rightKeys) {
		return false
	}
	seen := map[string]bool{}
	for _, key := range leftKeys {
		seen[key] = true
	}
	for _, key := range rightKeys {
		if !seen[key] {
			return false
		}
	}
	return true
}

// parseStoredBalanceConfig decodes a stored/canonical balance config JSON
// payload; "{}" and empty text decode to nil (the Node parseBalanceConfig
// fallback for a disabled config).
func parseStoredBalanceConfig(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, nil
	}
	if len(parsed) == 0 {
		return nil, nil
	}
	return parsed, nil
}

// balanceIdentityValue mirrors accountBalanceQueryIdentity: the observable
// balance query identity (enabled flag, normalized config, provider/type,
// effective Key fingerprints, normalized base URL and proxy reference).
func (s *Store) balanceIdentityValue(enabled bool, config map[string]any, providerCode, accountType string, credentials Credentials, proxyProfileID any) map[string]any {
	fingerprints := []any{}
	for _, key := range EffectiveAccountApiKeys(credentials) {
		fingerprints = append(fingerprints, s.balanceAPIKeyFingerprint(key))
	}
	identity := map[string]any{
		"enabled":                     enabled,
		"providerCode":                providerCode,
		"accountType":                 accountType,
		"effectiveApiKeyFingerprints": fingerprints,
	}
	if config != nil {
		identity["normalizedConfig"] = config
	}
	identity["normalizedBaseUrl"] = normalizedBalanceBaseURL(credentials["base_url"])
	if text, ok := proxyProfileID.(string); ok && strings.TrimSpace(text) != "" {
		identity["proxyProfileId"] = strings.TrimSpace(text)
	}
	return identity
}

// balanceIdentityEqual deep-compares two identity records via the canonical
// JSON shapes (Node isDeepStrictEqual).
func balanceIdentityEqual(left, right map[string]any) bool {
	return credentialValueJSONText(left) == credentialValueJSONText(right)
}

// credentialValueJSONText renders a decoded-JSON value's canonical text so
// deep-equality checks compare deterministic JSON text (mirrors
// isDeepStrictEqual over decoded records; plain == would panic on slices).
func credentialValueJSONText(value any) string {
	raw, err := json.Marshal(canonicalizeJSONValue(value))
	if err != nil {
		return "unmarshalable"
	}
	return string(raw)
}

// initialCooldownUntilForStatus mirrors initialCooldownUntilForStatus for the
// statuses the basic-edit surface can reach: temporary_unavailable re-arms the
// standard runtime cooldown window (5 minutes, runtime.ts default).
func initialCooldownUntilForStatus(status string, now time.Time) string {
	if status == "temporary_unavailable" {
		return isoMillis(now.Add(5 * time.Minute))
	}
	return ""
}

// loadEnabledGroupBindingID mirrors loadEnabledGroupIdInClient.
func (s *Store) loadEnabledGroupBindingID(ctx context.Context, q queryer, accountID, systemAccountID string) (string, error) {
	var groupID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT group_id
		FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?
			AND enabled = 1
		ORDER BY updated_at DESC, group_id ASC, account_id ASC
		LIMIT 1`), accountID, systemAccountID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return groupID, nil
}

// assertGroupCanBind mirrors assertGroupCanBindInClient: same owner, same
// provider, still enabled — otherwise the route renders 400 账户分组无效.
func (s *Store) assertGroupCanBind(ctx context.Context, q queryer, groupID, systemAccountID, providerCode string) error {
	var groupOwner, groupProvider string
	var enabled int64
	lockSuffix := ""
	if s.pg {
		lockSuffix = " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, s.bind(`SELECT system_account_id, provider_code, enabled
		FROM `+s.table("groups")+`
		WHERE id = ?`+lockSuffix), groupID).Scan(&groupOwner, &groupProvider, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "账户分组无效"}
	}
	if err != nil {
		return err
	}
	if groupOwner != systemAccountID || groupProvider != providerCode || enabled != 1 {
		return &ValidationError{Message: "账户分组无效"}
	}
	return nil
}

// replaceGroupBinding mirrors replaceGroupBindingInClient: delete the enabled
// binding rows for the account, then upsert the target binding with the
// account's dispatch fields preserved (local_priority family).
func (s *Store) replaceGroupBinding(ctx context.Context, q queryer, accountID, systemAccountID string, authorizationID sql.NullString, groupID string, priority int, superPriority, fallback bool, nowISO string) error {
	if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("group_accounts")+`
		WHERE account_id = ?
			AND system_account_id = ?`), accountID, systemAccountID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("group_accounts")+`
		(system_account_id, group_id, account_id, account_authorization_id,
		 local_priority, local_super_priority_enabled, local_fallback_enabled,
		 enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(group_id, account_id) DO UPDATE SET
			system_account_id = excluded.system_account_id,
			account_authorization_id = excluded.account_authorization_id,
			local_priority = excluded.local_priority,
			local_super_priority_enabled = excluded.local_super_priority_enabled,
			local_fallback_enabled = excluded.local_fallback_enabled,
			enabled = 1,
			updated_at = excluded.updated_at`),
		systemAccountID, groupID, accountID, authorizationID,
		priority, boolInt(superPriority), boolInt(fallback), nowISO, nowISO); err != nil {
		return err
	}
	return nil
}
