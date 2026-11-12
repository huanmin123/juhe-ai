package apikeys

// PATCH /api-keys/{id} for the management surface: the Node patchApiKeyAsync
// flow (backend/src/modules/api-keys/api-keys.routes.ts +
// storage/api-key.repository.ts) — apiKeyUpdateSchema parse with the
// first-issue-message contract, the optimistic revision lock
// (ApiKeyRevisionConflictError → 409 + currentRevision), the default/chat
// name guards, the selectable route-strategy probe, quota/schedule
// normalization with the schedule-driven status override and the
// changedFields/rowPatch outcome the route renders verbatim. This covers the
// full mutable field set and the dual-mode store lives here.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/dbtime"
)

// RevisionConflictError mirrors ApiKeyRevisionConflictError: the route renders
// 409 {message, currentRevision}.
type RevisionConflictError struct{ CurrentRevision string }

const apiKeyRevisionConflictMessage = "API Key 已被其他操作修改，请刷新后重试"

func (e *RevisionConflictError) Error() string { return apiKeyRevisionConflictMessage }

// Node invalidation reasons for the committed patch (shared/gateway-cache-
// invalidation.ts): the validation flush is required on
// routeStrategyId/status/expiresAt/quotaLimits changes, the runtime lookup
// follows a name change and the quota cache a quotaLimits change.
const (
	ReasonAPIKeyUpdated      = "api_key_updated"
	ReasonAPIKeyQuotaUpdated = "api_key_quota_updated"
)

// PatchInput is the parsed apiKeyUpdateSchema payload. Every mutable field
// keeps its own presence flag so absent / null / value stay distinguishable
// exactly the way patchApiKeyAsync consumes them (Object.hasOwn + ?? null).
// description/expiresAt use nil pointers for "clear the column"; quotaLimits
// and availabilitySchedule keep the raw decoded value (nil = JSON null).
type PatchInput struct {
	ExpectedRevision string

	Name    string
	HasName bool

	Description    *string
	HasDescription bool

	RouteStrategyID    string
	HasRouteStrategyID bool

	// 调度偏好绑定（P1 Has 语义）：缺失 = 保持原值（含历史 NULL）；显式
	// null（SchedulingPreferenceID 为 nil 且 HasSchedulingPreferenceID）=
	// 持久化内置固定 ID（显式回默认）；非空 ID = 校验后持久化。
	SchedulingPreferenceID    *string
	HasSchedulingPreferenceID bool

	// 安全策略绑定（批A Has 语义，§4.2）：缺失 = 保持原值（含历史 NULL）；
	// 显式 null（SecurityPolicyID 为 nil 且 HasSecurityPolicyID）= 持久化内置
	// 固定 ID（显式回默认）；非空 ID = 校验后持久化。
	SecurityPolicyID    *string
	HasSecurityPolicyID bool

	Status    string
	HasStatus bool

	ExpiresAt    *string
	HasExpiresAt bool

	QuotaLimits    any
	HasQuotaLimits bool

	AvailabilitySchedule any
	HasSchedule          bool
}

// patchTypeName mirrors the zod v3 parsed-type names used by the default
// invalid_type message.
func patchTypeName(raw any) string {
	switch raw.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func patchTypeIssue(expected string, raw any) string {
	return "Expected " + expected + ", received " + patchTypeName(raw)
}

// patchReceived mirrors the JS String() coercion zod renders inside
// `Invalid enum value. Expected ..., received '<value>'`.
func patchReceived(raw any) string {
	switch value := raw.(type) {
	case nil:
		return "null"
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'g', -1, 64)
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, patchReceived(item))
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	}
	return "unknown"
}

// parsePatchBody mirrors apiKeyUpdateSchema.safeParse with the route's
// firstIssueMessage contract: per-field issues in schema order (name,
// description, routeStrategyId, status, expiresAt, quotaLimits,
// availabilitySchedule, expectedRevision), then the strict unknown-key set,
// then the at-least-one-mutable-field refine. The second return value is the
// 400 message; an empty string accepts the payload.
func parsePatchBody(body map[string]any) (*PatchInput, string) {
	input := &PatchInput{}
	if value, exists := body["name"]; exists {
		text, isString := value.(string)
		if !isString {
			return nil, patchTypeIssue("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "请填写 API Key 名称"
		}
		input.HasName = true
		input.Name = trimmed
	}
	if value, exists := body["description"]; exists {
		input.HasDescription = true
		if value == nil {
			input.Description = nil
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, patchTypeIssue("string", value)
			}
			trimmed := strings.TrimSpace(text)
			// zod's max(200) counts UTF-16 code units (JavaScript .length).
			if utf16Length(trimmed) > 200 {
				return nil, "String must contain at most 200 character(s)"
			}
			if trimmed == "" {
				input.Description = nil
			} else {
				input.Description = &trimmed
			}
		}
	}
	if value, exists := body["routeStrategyId"]; exists {
		text, isString := value.(string)
		if !isString {
			return nil, patchTypeIssue("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "请选择策略路由"
		}
		input.HasRouteStrategyID = true
		input.RouteStrategyID = trimmed
	}
	if value, exists := body["schedulingPreferenceId"]; exists {
		input.HasSchedulingPreferenceID = true
		if value == nil {
			// 显式 null：回默认（内置固定 ID），不再继承策略配置。
			input.SchedulingPreferenceID = nil
		} else {
			text, isString := value.(string)
			// 契约：非法类型、空字符串与不存在/跨 owner 统一 400，不区分提示
			// （zod 类型文案被绑定契约显式覆盖；停用分支已随 v2 第 4 条退役）。
			if !isString {
				return nil, SchedulingPreferenceUnavailableMessage
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return nil, SchedulingPreferenceUnavailableMessage
			}
			input.SchedulingPreferenceID = &trimmed
		}
	}
	if value, exists := body["securityPolicyId"]; exists {
		input.HasSecurityPolicyID = true
		if value == nil {
			// 显式 null：回默认（内置固定 ID）。
			input.SecurityPolicyID = nil
		} else {
			text, isString := value.(string)
			// 契约 §4.2：非法类型、空字符串与不存在/跨 owner 统一 400，不区分
			// 提示（安全策略无停用态，无停用分支）。
			if !isString {
				return nil, SecurityPolicyUnavailableMessage
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return nil, SecurityPolicyUnavailableMessage
			}
			input.SecurityPolicyID = &trimmed
		}
	}
	if value, exists := body["status"]; exists {
		text, isString := value.(string)
		if !isString || (text != "active" && text != "disabled") {
			return nil, "Invalid enum value. Expected 'active' | 'disabled', received '" + patchReceived(value) + "'"
		}
		input.HasStatus = true
		input.Status = text
	}
	if value, exists := body["expiresAt"]; exists {
		input.HasExpiresAt = true
		if value == nil {
			input.ExpiresAt = nil
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, patchTypeIssue("string", value)
			}
			input.ExpiresAt = &text
		}
	}
	if value, exists := body["quotaLimits"]; exists {
		input.HasQuotaLimits = true
		if value != nil {
			if _, isObject := value.(map[string]any); !isObject {
				return nil, patchTypeIssue("object", value)
			}
		}
		input.QuotaLimits = value
	}
	if value, exists := body["availabilitySchedule"]; exists {
		input.HasSchedule = true
		if value != nil {
			if _, isObject := value.(map[string]any); !isObject {
				return nil, patchTypeIssue("object", value)
			}
		}
		input.AvailabilitySchedule = value
	}
	if value, exists := body["expectedRevision"]; exists {
		text, isString := value.(string)
		if !isString {
			return nil, patchTypeIssue("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "缺少 API Key revision"
		}
		input.ExpectedRevision = trimmed
	} else {
		return nil, "Required"
	}
	var unknown []string
	for key := range body {
		switch key {
		case "name", "description", "routeStrategyId", "schedulingPreferenceId", "securityPolicyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule", "expectedRevision":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		// Node renders the keys in insertion order; Go maps lose it, so the
		// rendering is sorted for determinism.
		sortStrings(unknown)
		quoted := make([]string, len(unknown))
		for index, key := range unknown {
			quoted[index] = "'" + key + "'"
		}
		return nil, "Unrecognized key(s) in object: " + strings.Join(quoted, ", ")
	}
	if !input.HasName && !input.HasDescription && !input.HasRouteStrategyID &&
		!input.HasSchedulingPreferenceID && !input.HasSecurityPolicyID && !input.HasStatus && !input.HasExpiresAt &&
		!input.HasQuotaLimits && !input.HasSchedule {
		return nil, "请提供要修改的 API Key 内容"
	}
	return input, ""
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// PatchResult mirrors ApiKeyPatchResult: the route renders ok(outcome.result).
type PatchResult struct {
	ID            string         `json:"id"`
	Revision      string         `json:"revision"`
	ChangedFields []string       `json:"changedFields"`
	RowPatch      map[string]any `json:"rowPatch"`
}

// PatchOutcome mirrors ApiKeyPatchOutcome: the rendered result plus the
// operation-log context (owner, display name — the NEW name after a rename —
// and the before/after diff source) and the required validation-cache failure
// the route surfaces as 500.
type PatchOutcome struct {
	Result               PatchResult
	OwnerSystemAccountID string
	ResourceName         string
	Before               map[string]any
	After                map[string]any
	ValidationCacheError error
}

// Patch mirrors patchApiKeyAsync: locked row, revision gate, per-field
// mutation with changed-field collection, monotonic revision bump guarded by
// `updated_at = ?`, the quota hourly-window binding sync on quota/status
// changes and the committed cache invalidations (validation required,
// runtime/quota best effort).
func (s *Store) Patch(ctx context.Context, id string, input *PatchInput, access AccessScope) (*PatchOutcome, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lock := " FOR UPDATE"
	where := ""
	args := []any{id}
	if scoped := access.manageableID(); scoped != "" {
		where = " AND api_keys.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, ownerID, name, routeStrategyID, status, keyHash string
	var description, purpose, quotaJSON, scheduleJSON, schedulingPreferenceID, securityPolicyID sql.NullString
	var expiresAt, updatedAt dbtime.Nullable
	var expiresAtText sql.NullString
	var isDefault int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT api_keys.id, api_keys.system_account_id, api_keys.name,
			api_keys.description, api_keys.route_strategy_id, api_keys.status, api_keys.is_default, api_keys.purpose,
			api_keys.key_hash, api_keys.expires_at, api_keys.quota_limits_json, api_keys.availability_schedule_json,
			api_keys.scheduling_preference_id, api_keys.security_policy_id, api_keys.updated_at
		FROM `+s.table("api_keys")+` api_keys
		WHERE api_keys.id = ?`+where+` LIMIT 1`+lock), args...).
		Scan(&rowID, &ownerID, &name, &description, &routeStrategyID, &status, &isDefault, &purpose,
			&keyHash, &expiresAt, &quotaJSON, &scheduleJSON, &schedulingPreferenceID, &securityPolicyID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		expiresAtText = sql.NullString{String: expiresAt.APIString(), Valid: true}
	}
	updatedAtText := ""
	if updatedAt.Valid {
		updatedAtText = dbtime.API6(updatedAt.Time)
	}
	if !dbtime.SameInstant(updatedAtText, input.ExpectedRevision) {
		return nil, &RevisionConflictError{CurrentRevision: updatedAtText}
	}

	now := s.now()
	changed := []string{}
	rowPatch := map[string]any{"revision": updatedAtText}
	before := map[string]any{}
	after := map[string]any{}
	setClauses := []string{}
	setArgs := []any{}
	nextName := name
	addChange := func(field string, previous, next any) {
		changed = append(changed, field)
		before[field] = previous
		after[field] = next
	}

	// nextQuotaJSON/nextStatus mirror the Node mutation locals: they start at
	// the stored values whenever the corresponding inputs participate and are
	// rewritten when the mutation changes them; the quota binding sync runs on
	// quota/status changes with exactly these values.
	nextQuotaJSON := sql.NullString{}
	nextStatus := ""
	quotaStatusOrScheduleInput := input.HasStatus || input.HasQuotaLimits || input.HasSchedule
	if quotaStatusOrScheduleInput {
		nextQuotaJSON = quotaJSON
		nextStatus = status
	}

	if input.HasName {
		// name 非空由 HTTP 层先行校验（parsePatchBody / aipublic
		// optionalTrimmedBody 同 strings.TrimSpace + min=1），store 层不再重复。
		if input.Name != name {
			if purpose.Valid && purpose.String == "chat" {
				return nil, &ValidationError{Message: "AI 对话 API Key 不允许修改名称"}
			}
			if isDefault == 1 {
				return nil, &ValidationError{Message: "默认 API Key 不允许修改名称"}
			}
			nextName = input.Name
			addChange("name", name, input.Name)
			setClauses = append(setClauses, "name = ?")
			setArgs = append(setArgs, input.Name)
			rowPatch["name"] = input.Name
		}
	}

	if input.HasDescription {
		nextDescription := sql.NullString{}
		if input.Description != nil {
			// Store-layer guard (UTF-16 units): Node's
			// normalizeOptionalApiKeyDescription enforces the same cap with
			// this message for any non-route caller.
			if utf16Length(*input.Description) > 200 {
				return nil, &ValidationError{Message: "API Key 说明不能超过 200 个字符"}
			}
			nextDescription = sql.NullString{String: *input.Description, Valid: true}
		}
		if nextDescription != description {
			addChange("description", nullStringValue(description), nullStringValue(nextDescription))
			setClauses = append(setClauses, "description = ?")
			setArgs = append(setArgs, nullStringParam(nextDescription))
			rowPatch["description"] = nullStringValue(nextDescription)
		}
	}

	if input.HasRouteStrategyID && input.RouteStrategyID != routeStrategyID {
		if isDefault == 1 && !(purpose.Valid && purpose.String == "chat") {
			return nil, &ValidationError{Message: "默认 API Key 不允许更换策略路由"}
		}
		reference, err := s.selectableRouteStrategyForPatch(ctx, tx, ownerID, input.RouteStrategyID)
		if err != nil {
			return nil, err
		}
		addChange("routeStrategyId", routeStrategyID, reference.id)
		setClauses = append(setClauses, "route_strategy_id = ?")
		setArgs = append(setArgs, reference.id)
		rowPatch["routeStrategyId"] = reference.id
		rowPatch["routeStrategyName"] = reference.name
		rowPatch["routeStrategyMode"] = reference.mode.String
		rowPatch["routeStrategyStatus"] = reference.status.String
	}

	// 调度偏好绑定（P1 Has 语义）。「默认 Key 不允许更换策略路由」限制不扩
	// 展到本字段（契约明示）；purpose=chat Key 保持列恒 NULL 强不变量：提交
	// schedulingPreferenceId（非空 ID 或显式 null）一律统一同型 400，不走普通
	// Key 的「显式 null 回默认内置 ID」矩阵。studio 已退役，不再豁免，存量
	// studio 行由 maintenance `--apply-studio-retirement` 清理（D06）。
	if input.HasSchedulingPreferenceID {
		if schedulingPreferenceExemptPurpose(purpose) {
			return nil, &ValidationError{Message: SchedulingPreferenceUnavailableMessage}
		}
		// 期望存储值：显式 null/缺省提交位 → 内置固定 ID；非空 ID → trim 后
		// 的值（空字符串经绑定解析统一拒绝）。同值提交是 no-op：不 bump
		// revision、不失效、不写操作日志。
		desired := sql.NullString{String: BuiltinSchedulingPreferenceID, Valid: true}
		if input.SchedulingPreferenceID != nil {
			desired = sql.NullString{String: strings.TrimSpace(*input.SchedulingPreferenceID), Valid: true}
		}
		if desired != schedulingPreferenceID {
			binding, err := s.resolveSchedulingPreferenceBinding(ctx, tx, ownerID, input.SchedulingPreferenceID)
			if err != nil {
				return nil, err
			}
			addChange("schedulingPreferenceId", nullStringValue(schedulingPreferenceID), binding.id)
			setClauses = append(setClauses, "scheduling_preference_id = ?")
			setArgs = append(setArgs, binding.id)
			rowPatch["schedulingPreferenceId"] = binding.id
			rowPatch["schedulingPreferenceName"] = binding.name
			rowPatch["schedulingPreferenceBuiltIn"] = binding.builtIn
		}
	}

	// 安全策略绑定（批A Has 语义，§4.2）。「默认 Key 不允许更换策略路由」限制
	// 不扩展到本字段；无 purpose 豁免（契约 §4.2：所有 purpose 的 Key 均可绑
	// 定）。期望存储值：显式 null/缺省提交位 → 内置固定 ID；非空 ID → trim 后
	// 的值（空字符串经绑定解析统一拒绝）。同值提交是 no-op：不 bump revision、
	// 不失效、不写操作日志。
	if input.HasSecurityPolicyID {
		desired := sql.NullString{String: BuiltinSecurityPolicyID, Valid: true}
		if input.SecurityPolicyID != nil {
			desired = sql.NullString{String: strings.TrimSpace(*input.SecurityPolicyID), Valid: true}
		}
		if desired != securityPolicyID {
			binding, err := s.resolveSecurityPolicyBinding(ctx, tx, ownerID, input.SecurityPolicyID)
			if err != nil {
				return nil, err
			}
			addChange("securityPolicyId", nullStringValue(securityPolicyID), binding.id)
			setClauses = append(setClauses, "security_policy_id = ?")
			setArgs = append(setArgs, binding.id)
			rowPatch["securityPolicyId"] = binding.id
			rowPatch["securityPolicyName"] = binding.name
			rowPatch["securityPolicyBuiltIn"] = binding.builtIn
		}
	}

	if input.HasExpiresAt {
		nextExpiresAt, err := normalizeOptionalExpiresAt(input.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if nextExpiresAt != expiresAtText {
			addChange("expiresAt", nullStringValue(expiresAtText), nullStringValue(nextExpiresAt))
			setClauses = append(setClauses, "expires_at = ?")
			setArgs = append(setArgs, nullStringParam(nextExpiresAt))
			rowPatch["expiresAt"] = nullStringValue(nextExpiresAt)
		}
	}

	if input.HasQuotaLimits {
		currentQuotaLimits, err := ParseQuotaLimitsJSON(quotaJSON.String)
		if err != nil {
			return nil, err
		}
		quotaLimits, err := normalizeQuotaLimits(input.QuotaLimits, currentQuotaLimits)
		if err != nil {
			return nil, err
		}
		rawQuotaJSON, quotaJSONValid := QuotaLimitsJSON(quotaLimits)
		nextQuotaLimits := sql.NullString{}
		if quotaJSONValid {
			nextQuotaLimits = sql.NullString{String: rawQuotaJSON, Valid: true}
		}
		if nextQuotaLimits != quotaJSON {
			nextQuotaJSON = nextQuotaLimits
			addChange("quotaLimits", currentQuotaLimits, quotaLimits)
			setClauses = append(setClauses, "quota_limits_json = ?")
			setArgs = append(setArgs, nullStringParam(nextQuotaLimits))
			rowPatch["quotaLimits"] = quotaLimits
		}
	}

	if input.HasSchedule {
		// 旧计划非法时不能阻断修复：清空或替换计划必须始终可用。解析失败按
		// “无旧计划”处理（change 检测自然为真），由新计划覆盖坏数据；列表
		// 读面同样降级，避免单条坏行打挂整个列表。
		currentSchedule, err := s.parseScheduleJSON(scheduleJSON.String)
		if err != nil {
			currentSchedule = nil
		}
		schedule, err := normalizeScheduleWithDefault(input.AvailabilitySchedule, s.defaultScheduleTimezone)
		if err != nil {
			return nil, err
		}
		nextScheduleJSONRaw, nextScheduleValid := ScheduleJSON(schedule)
		currentScheduleJSONRaw, currentScheduleValid := ScheduleJSON(currentSchedule)
		if nextScheduleJSONRaw != currentScheduleJSONRaw || nextScheduleValid != currentScheduleValid {
			nextScheduleJSON := sql.NullString{}
			if nextScheduleValid {
				nextScheduleJSON = sql.NullString{String: nextScheduleJSONRaw, Valid: true}
			}
			nextCheckParam := any(nil)
			if rawNextCheck, ok := NextScheduleCheckAt(schedule, now); ok {
				nextCheckParam = rawNextCheck
			}
			addChange("availabilitySchedule", scheduleOrNil(currentSchedule), scheduleOrNil(schedule))
			// 计划变化只重算派生闸门（时间计划闸门与账户状态解耦设计 §4.2）：
			// 计划清除 / 未启用写 NULL（闸门恒开），否则按当前时刻落 1/0；
			// status 只由用户启停决定，不再被计划覆盖。
			setClauses = append(setClauses, "availability_schedule_json = ?",
				"availability_schedule_next_check_at = ?", "availability_schedule_gate = ?")
			setArgs = append(setArgs, nullStringParam(nextScheduleJSON), nextCheckParam, ScheduleGateValue(schedule, now))
			rowPatch["availabilitySchedule"] = scheduleOrNil(schedule)
		}
	}

	if input.HasStatus {
		requestedStatus := input.Status
		if requestedStatus != "active" && requestedStatus != "disabled" {
			return nil, &ValidationError{Message: "API Key 状态无效"}
		}
		if requestedStatus != status {
			addChange("status", status, requestedStatus)
			setClauses = append(setClauses, "status = ?")
			setArgs = append(setArgs, requestedStatus)
			rowPatch["status"] = requestedStatus
		}
		nextStatus = requestedStatus
	}

	if len(changed) == 0 {
		return &PatchOutcome{
			Result:               PatchResult{ID: rowID, Revision: updatedAtText, ChangedFields: changed, RowPatch: rowPatch},
			OwnerSystemAccountID: ownerID,
			ResourceName:         name,
			Before:               before,
			After:                after,
		}, nil
	}

	revision, err := nextRevision(updatedAtText, now)
	if err != nil {
		return nil, err
	}
	setClauses = append(setClauses, "updated_at = ?")
	setArgs = append(setArgs, revision)
	updateArgs := append([]any{}, setArgs...)
	updateArgs = append(updateArgs, rowID, ownerID, updatedAtText)
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("api_keys")+` SET `+strings.Join(setClauses, ", ")+`
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), updateArgs...)
	if err != nil {
		if duplicate := duplicateAPIKeyNameError(err, nextName); duplicate != nil {
			return nil, duplicate
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{CurrentRevision: updatedAtText}
	}

	bindingUpserted := false
	if containsString(changed, "quotaLimits") || containsString(changed, "status") {
		upserted, err := s.syncQuotaHourlyWindowBinding(ctx, tx, rowID, ownerID, nextQuotaJSON, nextStatus == "active", revision)
		if err != nil {
			return nil, err
		}
		bindingUpserted = upserted
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// W2-A 待办1：limits/状态变更重建绑定后打脏（PG；失败不回滚主写，
	// warn 告警；停用分支按归档 :94-105 早退不打脏）。
	if bindingUpserted {
		s.markQuotaHourlyWindowDirtyScopeAfterCommit(ctx, ownerID, rowID, revision)
	}

	rowPatch["revision"] = revision
	outcome := &PatchOutcome{
		Result:               PatchResult{ID: rowID, Revision: revision, ChangedFields: changed, RowPatch: rowPatch},
		OwnerSystemAccountID: ownerID,
		ResourceName:         nextName,
		Before:               before,
		After:                after,
	}
	if s.inval != nil {
		validationChanged := containsString(changed, "routeStrategyId") || containsString(changed, "schedulingPreferenceId") ||
			containsString(changed, "securityPolicyId") ||
			containsString(changed, "status") ||
			containsString(changed, "expiresAt") || containsString(changed, "quotaLimits")
		if validationChanged {
			if err := s.inval.InvalidateValidation(id, ReasonAPIKeyUpdated, []string{keyHash}); err != nil {
				outcome.ValidationCacheError = err
			}
		}
		if containsString(changed, "name") || containsString(changed, "availabilitySchedule") {
			// 计划变化会改写派生闸门 availability_schedule_gate（时间计划闸门与
			// 账户状态解耦设计 §5.2）：复用既有运行时缓存失效链，让网关立即按
			// 新闸门鉴权，不等 TTL。
			s.inval.InvalidateRuntime(id, ReasonAPIKeyUpdated)
		}
		if containsString(changed, "quotaLimits") {
			s.inval.InvalidateQuota(id, ReasonAPIKeyQuotaUpdated)
		}
	}
	return outcome, nil
}

// selectableRouteStrategyForPatch mirrors assertRouteStrategySelectableForApi
// KeyAsync(lockRow=true) + apiKeyRouteStrategyReferenceAsync: the owner-scoped
// lookup (row-locked on PostgreSQL) must exist and be active.
func (s *Store) selectableRouteStrategyForPatch(ctx context.Context, q queryer, ownerID, strategyID string) (*routeStrategyReference, error) {
	lock := " FOR UPDATE"
	var reference routeStrategyReference
	err := q.QueryRowContext(ctx, s.bind(`SELECT route_strategies.id, route_strategies.name, route_strategies.mode, route_strategies.status
		FROM `+s.table("route_strategies")+` route_strategies
		WHERE route_strategies.id = ? AND route_strategies.system_account_id = ?
		LIMIT 1`+lock), strategyID, ownerID).
		Scan(&reference.id, &reference.name, &reference.mode, &reference.status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ValidationError{Message: "API Key 绑定的策略路由不存在或不属于当前用户"}
	}
	if err != nil {
		return nil, err
	}
	if !reference.status.Valid || reference.status.String != "active" {
		return nil, &ValidationError{Message: "API Key 只能绑定启用状态的策略路由"}
	}
	return &reference, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func nullStringParam(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func scheduleOrNil(schedule *AvailabilitySchedule) any {
	if schedule == nil {
		return nil
	}
	return schedule
}
