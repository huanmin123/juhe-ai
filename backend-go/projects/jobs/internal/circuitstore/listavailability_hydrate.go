package circuitstore

// 投影 LoadItems 的水合面：对照归档 Node
//   - storage/account-status-snapshot.repository.ts
//     hydrateAccountManagementStatusSeedsDirect / hydrateAccountManagementStatusFilterSeedsDirect
//   - domain/account-effective-availability.ts（状态机全分支）
//   - domain/account-status-classification.ts accountFilterStatuses
//   - domain/account-status-presentation.ts accountAvailabilityPresentation
//   - modules/accounts/account-status-snapshot.service.ts getAccountStatusSnapshotFromProjections

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// hydratePage 对齐 loadItems 水合链：base 行 → status seeds 派生 → 运行态读 →
// snapshot 合成 → hydratedEntry。
func (l *ProjectionItemLoader) hydratePage(ctx context.Context, page *managementPage, now time.Time) ([]hydratedEntry, error) {
	rows := page.rows
	accountIDs := make([]string, 0, len(rows))
	for i := range rows {
		accountIDs = append(accountIDs, rows[i].id)
	}
	tagsByAccount, err := l.loadAccountTags(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	locksByAccount, err := l.loadAccountLockViews(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	timezone, err := l.timezone.StatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	quotaByAuth, resetByAuth, err := l.loadAuthorizationQuotaStatus(ctx, rows, now, timezone)
	if err != nil {
		return nil, err
	}
	todayUsage, authorizationTotal, err := l.loadUsageSummaries(ctx, rows, now, timezone)
	if err != nil {
		return nil, err
	}
	// 运行态读（getAccountStatusSnapshotFromProjections 的并行依赖面；
	// 任一失败 → fail closed）。
	runtimeKeys := make([]string, 0, len(rows))
	concurrencyIDs := make([]string, 0, len(rows))
	sourceAccountIDs := make([]string, 0, len(rows))
	balanceOwnerIDs := make([]string, 0, len(rows))
	for i := range rows {
		row := rows[i]
		authorized := isAuthorizedRow(row)
		runtimeKeys = append(runtimeKeys, runtimeKeyOf(row))
		concurrencyIDs = append(concurrencyIDs, concurrencyAccountIDOf(row))
		if authorized && row.authorizationInstanceSourceID.Valid && row.authorizationInstanceSourceID.String != "" {
			sourceAccountIDs = append(sourceAccountIDs, row.authorizationInstanceSourceID.String)
		}
		if !authorized && booleanValue(row.balanceQueryEnabled) {
			balanceOwnerIDs = append(balanceOwnerIDs, row.id)
		}
	}
	runtimeByKeys, err := l.runtime.LoadRuntimeAvailability(ctx, runtimeKeys)
	if err != nil {
		return nil, fmt.Errorf("账户列表投影运行态可用性读取不可用: %w", err)
	}
	concurrencyValues, err := l.concurrency.LoadConcurrency(ctx, concurrencyIDs)
	if err != nil {
		return nil, fmt.Errorf("账户列表投影并发快照读取不可用: %w", err)
	}
	circuitsByKeys, err := l.loadCircuitSummaries(ctx, runtimeKeys, now)
	if err != nil {
		return nil, err
	}
	balanceByAccount, err := l.loadBalanceSnapshotRecords(ctx, balanceOwnerIDs)
	if err != nil {
		return nil, err
	}
	apiKeyRuntimeByAccount, err := l.loadAPIKeyRuntimeSummaries(ctx, sourceAccountIDs)
	if err != nil {
		return nil, err
	}

	entries := make([]hydratedEntry, 0, len(rows))
	for i := range rows {
		row := rows[i]
		tags := tagsByAccount[row.id]
		if tags == nil {
			tags = []map[string]any{}
		}
		payload := buildBasePayload(row, tags, locksByAccount[row.id])
		key := runtimeKeyOf(row)
		entry, composeErr := l.composeEntry(composeInput{
			row:           row,
			payload:       payload,
			now:           now,
			timezone:      timezone,
			quotaExceeded: quotaByAuth[row.authorizationID.String],
			quotaResetAt:  resetByAuth[row.authorizationID.String],
			todayUsage:    todayUsage[row.id],
			authTotal:     authorizationTotal[row.id],
			runtime:       runtimeByKeys[key],
			circuit:       circuitsByKeys[key],
			concurrency:   concurrencyValues[concurrencyAccountIDOf(row)],
			balance:       balanceByAccount[row.id],
			apiKeyRuntime: apiKeyRuntimeByAccount[apiKeyRuntimeAccountIDOf(row)],
		})
		if composeErr != nil {
			return nil, composeErr
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func isAuthorizedRow(row managementRow) bool {
	return row.authorizationID.Valid && strings.TrimSpace(row.authorizationID.String) != ""
}

func runtimeKeyOf(row managementRow) string {
	if !isAuthorizedRow(row) {
		return row.id
	}
	binding := groupBindingOf(row)
	if binding == nil || row.authorizationID.String == "" {
		return row.id
	}
	return fmt.Sprintf("%s:authorized:%s:%s:%s", row.id, row.systemAccountID, binding.groupID, row.authorizationID.String)
}

func concurrencyAccountIDOf(row managementRow) string {
	if row.authorizationInstanceSourceID.Valid && row.authorizationInstanceSourceID.String != "" {
		return row.authorizationInstanceSourceID.String
	}
	return row.id
}

func apiKeyRuntimeAccountIDOf(row managementRow) string {
	if row.authorizationInstanceSourceID.Valid && row.authorizationInstanceSourceID.String != "" {
		return row.authorizationInstanceSourceID.String
	}
	return row.id
}

type groupBinding struct {
	groupID         string
	groupBindStatus string // bound | authorization_unavailable
}

// groupBindingOf 对齐 accountStatusGroupBinding（status seed 面）。
func groupBindingOf(row managementRow) *groupBinding {
	if !row.boundGroupID.Valid || row.boundGroupID.String == "" {
		return nil
	}
	if row.bindingSystemAccountID.String != row.systemAccountID {
		return nil
	}
	status := "bound"
	if row.boundGroupAccountAuthorizationID.String != row.authorizationID.String {
		status = "authorization_unavailable"
	}
	return &groupBinding{groupID: row.boundGroupID.String, groupBindStatus: status}
}

// composeInput 是单账户快照合成的输入集合。
type composeInput struct {
	row           managementRow
	payload       map[string]any
	now           time.Time
	timezone      *time.Location
	quotaExceeded bool
	quotaResetAt  string
	todayUsage    usageValue
	authTotal     usageValue
	runtime       AccountRuntimeAvailability
	circuit       publicCircuitSummary
	concurrency   int
	balance       *balanceSnapshotRecord
	apiKeyRuntime *apiKeyRuntimeSummary
}

// composeEntry 对齐 getAccountStatusSnapshotFromProjections 的单账户合成：
// status seed 派生 + effectiveAvailability + payload 快照键覆盖 + 投影列输入。
func (l *ProjectionItemLoader) composeEntry(input composeInput) (hydratedEntry, error) {
	row := input.row
	authorized := isAuthorizedRow(row)
	binding := groupBindingOf(row)
	now := input.now

	// ---- status seed 派生（hydrateAccountManagementStatusFilterSeedsDirect）----
	status := row.status
	if authorized {
		// authorizationRuntimeBlockingStatus(status, expiresAt) ?? row.status。
		if row.authorizationStatus.Valid && row.authorizationStatus.String != "" && row.authorizationStatus.String != "active" {
			status = "disabled"
		} else if authorizationExpired(row.authorizationExpiresAt.String, now) {
			status = "disabled"
		}
	}

	// ---- runtime availability（publicAccountRuntimeAvailability 形状 + probe 派生）----
	var runtimePublic map[string]any
	runtimeStatus := ""
	runtimeReason := ""
	var runtimeNextAttemptAt, runtimeRecoveryAt *string
	if input.runtime.Status != "" {
		runtimeStatus = input.runtime.Status
		runtimeReason = input.runtime.Reason
		runtimePublic = runtimeAvailabilityPayload(input.runtime)
		if input.runtime.ProbePresentation != nil {
			if schedule, ok := input.runtime.ProbePresentation["schedule"].(map[string]any); ok {
				if next, ok := schedule["nextAttemptAt"].(string); ok {
					runtimeNextAttemptAt = &next
				}
			}
			if recovery, ok := input.runtime.ProbePresentation["recoveryAt"].(string); ok && recovery != "" {
				runtimeRecoveryAt = &recovery
			}
		}
	}

	// ---- apiKeyRuntime（public 汇总形状）----
	var apiKeyPublic map[string]any
	apiKeyAllUnavailable := false
	apiKeyTotal := 0
	var apiKeyNextProbeAt *string
	if input.apiKeyRuntime != nil {
		apiKeyPublic = input.apiKeyRuntime.publicPayload()
		apiKeyAllUnavailable = input.apiKeyRuntime.AllUnavailable
		apiKeyTotal = input.apiKeyRuntime.Total
		if input.apiKeyRuntime.NextProbeAt != "" {
			apiKeyNextProbeAt = &input.apiKeyRuntime.NextProbeAt
		}
	}

	// ---- effectiveAvailability（状态机输入集合）----
	availability := accountEffectiveAvailability(availabilityInput{
		accessType:                 accessTypeOf(authorized),
		boundGroupID:               bindingGroupID(binding),
		groupBindStatus:            bindingStatus(binding),
		authorizationStatus:        row.authorizationStatus.String,
		authorizationExpiresAt:     row.authorizationExpiresAt.String,
		authorizationQuotaExceeded: authorized && input.quotaExceeded,
		sourceAccountID:            row.authorizationInstanceSourceID.String,
		sourceStatus:               row.sourceStatus.String,
		sourceSchedulable:          sourceSchedulablePtr(row),
		sourceExpiresAt:            row.sourceAccountExpiresAt.String,
		sourceCooldownUntil:        row.sourceCooldownUntil.String,
		sourceLastErrorCode:        row.sourceLastErrorCode.String,
		sourceLastErrorMessage:     diagnosticText(row.sourceLastErrorMessage.String),
		accountExpiresAt:           row.accountExpiresAt.String,
		status:                     status,
		schedulable:                booleanValue(row.schedulable),
		cooldownUntil:              row.cooldownUntil.String,
		lastErrorCode:              row.lastErrorCode.String,
		lastErrorMessage:           diagnosticText(row.lastErrorMessage.String),
		lastHealthCheckAt:          row.lastHealthCheckAt.String,
		lastHealthCheckErrorCode:   row.lastHealthCheckErrorCode.String,
		lastHealthCheckErrorMessage: diagnosticText(row.lastHealthCheckErrorMessage.String),
		apiKeyAllUnavailable:       apiKeyAllUnavailable,
		apiKeyTotal:                apiKeyTotal,
		apiKeyNextProbeAt:          apiKeyNextProbeAtString(apiKeyNextProbeAt),
		runtimeStatus:              runtimeStatus,
		runtimeReason:              runtimeReason,
	}, now)

	// ---- 投影状态分类（accountFilterStatuses；无法唯一归类即抛错）----
	statuses := accountFilterStatuses(status, &availability, authorized && input.quotaExceeded)
	if len(statuses) != 1 {
		return hydratedEntry{}, fmt.Errorf("账户 %s 无法归类为唯一投影状态", row.id)
	}

	// ---- lastUsedAt（owner=accounts.last_used_at；authorized=授权 total）----
	var lastUsedAt *string
	if authorized {
		if input.authTotal.LastUsedAt != "" {
			lastUsedAt = &input.authTotal.LastUsedAt
		}
	} else if row.lastUsedAt.Valid && row.lastUsedAt.String != "" {
		value := row.lastUsedAt.String
		lastUsedAt = &value
	}

	// ---- balance snapshot（配置匹配才发布；forList 去掉 keyBalances）----
	var balancePayload map[string]any
	if !authorized && booleanValue(row.balanceQueryEnabled) && input.balance != nil &&
		balanceSnapshotMatchesConfiguration(row.balanceQueryNextRefreshAt.String, numberValue(row.configRevision, 1), input.balance) {
		balancePayload = balanceForListPayload(input.balance.snapshot)
	}

	// ---- payload 快照键覆盖（{...item, ...snapshot, currentConcurrency}）----
	payload := input.payload
	payload["status"] = status
	payload["schedulable"] = booleanValue(row.schedulable)
	payload["todayUsage"] = map[string]any{
		"requestCount": input.todayUsage.RequestCount,
		"totalTokens":  input.todayUsage.TotalTokens,
		"totalCost":    input.todayUsage.TotalCost,
	}
	payload["currentConcurrency"] = maxInt(0, input.concurrency)
	if authorized && row.authorizationStatus.Valid && row.authorizationStatus.String != "" {
		payload["authorizationStatus"] = row.authorizationStatus.String
	}
	if row.authorizationExpiresAt.Valid && row.authorizationExpiresAt.String != "" {
		payload["authorizationExpiresAt"] = row.authorizationExpiresAt.String
	}
	if authorized {
		// Node filterSeedsDirect：authorized 行恒写入（false 也保留），
		// owner 行 undefined 省略。
		payload["authorizationQuotaExceeded"] = input.quotaExceeded
	}
	if authorized {
		// Node authorizationLimits：parseRequestQuotaLimitsJson 空串返回
		// 空对象（payload 恒有该键）；非法 JSON 抛错走释放重放。
		limits, limitsErr := parseQuotaLimitsPayload(row.authorizationLimitsJSON.String)
		if limitsErr != nil {
			return hydratedEntry{}, limitsErr
		}
		payload["authorizationLimits"] = limits
	}
	if lastUsedAt != nil {
		payload["lastUsedAt"] = *lastUsedAt
	}
	if !authorized && booleanValue(row.balanceQueryEnabled) {
		payload["balanceQueryEnabled"] = true
	}
	if !authorized && row.balanceQueryNextRefreshAt.Valid && row.balanceQueryNextRefreshAt.String != "" {
		payload["balanceQueryNextRefreshAt"] = row.balanceQueryNextRefreshAt.String
	}
	if balancePayload != nil {
		payload["balanceSnapshot"] = balancePayload
	}
	if runtimePublic != nil {
		payload["runtimeAvailability"] = runtimePublic
	}
	if circuit := circuitSummaryPayload(input.circuit); circuit != nil {
		payload["circuitSummary"] = circuit
	}
	if apiKeyPublic != nil {
		payload["apiKeyRuntime"] = apiKeyPublic
	}
	if presentation := presentationPayload(&availability, row, runtimeRecoveryAt, now); presentation != nil {
		payload["availabilityPresentation"] = presentation
	}
	payload["effectiveAvailability"] = effectiveAvailabilityPayload(availability)

	// ---- 投影列输入 ----
	priority := numberValue(row.priority)
	superPriority := booleanValue(row.superPriorityEnabled)
	fallback := booleanValue(row.fallbackEnabled)
	concurrencyLimit := numberValue(row.concurrencyLimit)
	providerCode := row.providerCode
	profileID := row.providerProtocolProfileID
	accountType := row.accountType
	if authorized {
		// Node write 取 item（authorized 派生后的 base 值）。
		priority = numberValue(coalesceAny(row.boundGroupLocalPriority, row.priority))
		superPriority = booleanValue(row.boundGroupLocalSuperPriorityEnabled)
		fallback = booleanValue(row.boundGroupLocalFallbackEnabled)
		if row.sourceConcurrencyLimit != nil {
			concurrencyLimit = numberValue(row.sourceConcurrencyLimit)
		}
		if row.sourceProviderCode.Valid && row.sourceProviderCode.String != "" {
			providerCode = row.sourceProviderCode.String
		}
		if row.sourceProviderProfileID.Valid && row.sourceProviderProfileID.String != "" {
			profileID = row.sourceProviderProfileID.String
		}
		if row.sourceType.Valid && row.sourceType.String != "" {
			accountType = row.sourceType.String
		}
	}
	boundGroupID := ""
	if binding != nil {
		boundGroupID = binding.groupID
	} else if row.boundGroupID.Valid {
		boundGroupID = row.boundGroupID.String
	}
	var sortLastUsedAt *string
	if row.lastUsedAt.Valid && row.lastUsedAt.String != "" {
		value := row.lastUsedAt.String
		sortLastUsedAt = &value
	}
	textPtrOf := func(value sql.NullString) *string {
		if value.Valid && value.String != "" {
			return &value.String
		}
		return nil
	}
	return hydratedEntry{
		payload:            payload,
		accountID:          row.id,
		effectiveStatus:    statuses[0],
		effectiveAvailable: availability.available,
		currentConcurrency: maxInt(0, input.concurrency),
		providerCode:       providerCode,
		profileID:          profileID,
		accountType:        accountType,
		boundGroupID:       boundGroupID,
		name:               row.name,
		priority:           int(priority),
		superPriority:      superPriority,
		fallback:           fallback,
		concurrencyLimit:   int(concurrencyLimit),
		sourceAccountID:    row.authorizationInstanceSourceID.String,
		authorizationID:    row.authorizationID.String,
		sortLastUsedAt:     sortLastUsedAt,
		accountExpiresAt:   textPtrOf(row.accountExpiresAt),
		authorizationExpiresAt: textPtrOf(row.authorizationExpiresAt),
		sourceExpiresAt:        textPtrOf(row.sourceAccountExpiresAt),
		sourceCooldownUntil:    textPtrOf(row.sourceCooldownUntil),
		cooldownUntil:          textPtrOf(row.cooldownUntil),
		apiKeyNextProbeAt:      apiKeyNextProbeAt,
		runtimeNextAttemptAt:   runtimeNextAttemptAt,
		runtimeRecoveryAt:      runtimeRecoveryAt,
		statusBoundaryAt:       statusBoundaryAtOf(&availability, row, input.quotaResetAt, runtimeRecoveryAt),
		quotaResetAt:           ptrStringIf(input.quotaResetAt),
		availabilityScheduleJSON:       textPtrOf(row.availabilityScheduleJSON),
		sourceAvailabilityScheduleJSON: textPtrOf(row.sourceAvailabilityScheduleJSON),
	}, nil
}

func bindingGroupID(binding *groupBinding) string {
	if binding == nil {
		return ""
	}
	return binding.groupID
}

func bindingStatus(binding *groupBinding) string {
	if binding == nil {
		return ""
	}
	return binding.groupBindStatus
}

func sourceSchedulablePtr(row managementRow) *bool {
	if row.sourceSchedulable == nil {
		return nil
	}
	value := booleanValue(row.sourceSchedulable)
	return &value
}

func apiKeyNextProbeAtString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func authorizationExpired(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return false
	}
	timestamp, ok := rfc3339Millis(expiresAt)
	if !ok {
		return false
	}
	return timestamp <= now.UnixMilli()
}

// statusBoundaryAtOf 对齐 accountAvailabilityPresentation 的 statusBoundary
// 分支（含 runtime_local_suppressed 的 policy_ttl_expiry recoveryAt）。
func statusBoundaryAtOf(availability *effectiveAvailability, row managementRow, quotaResetAt string, runtimeRecoveryAt *string) *string {
	if availability == nil {
		return nil
	}
	switch availability.status {
	case "authorization_expired":
		if row.authorizationExpiresAt.Valid && row.authorizationExpiresAt.String != "" {
			return &row.authorizationExpiresAt.String
		}
	case "source_expired":
		if row.sourceAccountExpiresAt.Valid && row.sourceAccountExpiresAt.String != "" {
			return &row.sourceAccountExpiresAt.String
		}
	case "instance_expired":
		if row.accountExpiresAt.Valid && row.accountExpiresAt.String != "" {
			return &row.accountExpiresAt.String
		}
	case "authorization_quota_exceeded":
		if quotaResetAt != "" {
			return &quotaResetAt
		}
	case "source_cooldown":
		if row.sourceCooldownUntil.Valid && row.sourceCooldownUntil.String != "" {
			return &row.sourceCooldownUntil.String
		}
	case "instance_cooldown":
		if row.cooldownUntil.Valid && row.cooldownUntil.String != "" {
			return &row.cooldownUntil.String
		}
	case "runtime_local_suppressed":
		if runtimeRecoveryAt != nil && *runtimeRecoveryAt != "" {
			return runtimeRecoveryAt
		}
	}
	return nil
}

func ptrStringIf(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// runtimeAvailabilityPayload 对齐 publicAccountRuntimeAvailability 投影
// （status/reason/since + probePresentation 深拷贝）。
func runtimeAvailabilityPayload(runtime AccountRuntimeAvailability) map[string]any {
	payload := map[string]any{"status": runtime.Status}
	if runtime.Reason != "" {
		payload["reason"] = runtime.Reason
	}
	if runtime.Since != "" {
		payload["since"] = runtime.Since
	}
	if runtime.ProbePresentation != nil {
		payload["probePresentation"] = runtime.ProbePresentation
	}
	return payload
}

// diagnosticText 对齐 accountListDiagnosticText（96 字符截断，省略空白）。
func diagnosticText(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	if len(text) <= 96 {
		return text
	}
	return text[:95] + "…"
}

// circuitSummaryPayload 把公共电路摘要序列化为 payload 形状（Node
// PublicAccountCircuitSummary；normal 且无 since/reason 时仍写 status——
// Node circuits.values[key] 缺失时键不存在，此处以 nil 表示缺失）。
func circuitSummaryPayload(summary publicCircuitSummary) map[string]any {
	if summary.Status == "" {
		return nil
	}
	payload := map[string]any{"status": summary.Status}
	if summary.Reason != "" {
		payload["reason"] = summary.Reason
	}
	if summary.Since != "" {
		payload["since"] = summary.Since
	}
	if summary.NextCheckAt != "" {
		payload["nextCheckAt"] = summary.NextCheckAt
	}
	return payload
}

// effectiveAvailabilityPayload 对齐 blocked()/available 返回对象的 JSON 形状。
func effectiveAvailabilityPayload(availability effectiveAvailability) map[string]any {
	payload := map[string]any{
		"available":    availability.available,
		"status":       availability.status,
		"label":        availability.label,
		"color":        availability.color,
		"blockerScope": availability.blockerScope,
		"reason":       availability.reason,
	}
	if availability.retryAt != "" {
		payload["retryAt"] = availability.retryAt
	}
	if !availability.available {
		return payload
	}
	// available=true 的 available/runtime_degraded 无 blockerScope/retryAt。
	delete(payload, "blockerScope")
	delete(payload, "retryAt")
	return payload
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
