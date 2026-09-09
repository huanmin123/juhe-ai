// details.go 移植 Node loadAccountApiKeyRuntimeDetailsByAccountIds(Async) +
// accountApiKeyRuntimeDetailsFromRows（account-api-key-runtime-state.repository.ts
// :912-982）：账户关联 API Key 池的运行明细列表（键掩码 + 运行态），经
// accounts.APIKeyRuntimeDetailsReader 端口供 GET /accounts/{id}/api-key-runtime
// 渲染（verdict-aa 风险注记补齐：端口此前无实现无接线，items 恒为 []）。
//
// 脱敏语义跟随 Go 既有凭据视图合同：items 只输出 keyFingerprintPrefix（指纹前
// 12 位）与 keySuffix（明文末 4 位），不泄露完整 Key（Node sanitizeAccountApiKeyRuntimeResponse
// 同款窄投影，Go 以构造定形承担）。
package accountkeystates

import (
	"context"
	"strings"
)

// positiveInteger 等价 Node positiveInteger：有限正整数截断，否则 0。
func positiveInteger(value int) int {
	if value > 0 {
		return value
	}
	return 0
}

// keySuffixForRuntimeDisplay 等价 keySuffixForRuntimeDisplay：trim 后末 4 位；
// 空串不出字段（Node undefined 分支）。
func keySuffixForRuntimeDisplay(key string) (string, bool) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", false
	}
	runes := []rune(normalized)
	if len(runes) <= 4 {
		return normalized, true
	}
	return string(runes[len(runes)-4:]), true
}

// LoadAPIKeyRuntimeDetails 实现 accounts.APIKeyRuntimeDetailsReader 端口：
// 单账户的池明细投影。非池账户 / 解密失败 / 池内 Key 不足 2 个按 Node 分支
// 跳过，渲染空列表。
func (s *Store) LoadAPIKeyRuntimeDetails(ctx context.Context, accountID string) ([]map[string]any, error) {
	ids := normalizeAccountIds([]string{accountID})
	if len(ids) == 0 {
		return []map[string]any{}, nil
	}
	rows, err := s.loadSummarySourceRows(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []map[string]any{}, nil
	}
	sourceIds := make([]string, 0, len(rows))
	for _, row := range rows {
		sourceIds = append(sourceIds, row.sourceAccountID)
	}
	statesByAccountId, err := s.loadSummaryDetailRows(ctx, sourceIds)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.viewAccountID != ids[0] {
			continue
		}
		credentials, err := s.DecryptCredentials(row.credentialsEncrypted)
		if err != nil {
			// Node：解密失败按账户缺失处理（continue 渲染空列表）。
			return []map[string]any{}, nil
		}
		if !s.IsAccountAPIKeyPoolIsolationEnabled(row.providerCode, row.protocolCode, row.protocolVersion, row.accountType, credentials) {
			return []map[string]any{}, nil
		}
		entries := s.AccountAPIKeyEntries(credentials)
		if len(entries) < 2 {
			return []map[string]any{}, nil
		}
		statesByFingerprint := map[string]summaryDetailRow{}
		for _, state := range statesByAccountId[row.sourceAccountID] {
			statesByFingerprint[state.keyFingerprint] = state
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			state, hasState := statesByFingerprint[entry.Fingerprint]
			status := "active"
			if hasState && state.status != "" {
				status = state.status
			}
			item := map[string]any{
				"keyIndex":             entry.Index,
				"keyFingerprintPrefix": truncatePrefix(entry.Fingerprint, 12),
				"weight":               entry.Weight,
				"status":               status,
				"failureCount":         positiveInteger(state.failureCount),
				"consecutiveFailures":  positiveInteger(state.consecutiveFailures),
				"successCount":         positiveInteger(state.successCount),
			}
			if suffix, ok := keySuffixForRuntimeDisplay(entry.Key); ok {
				item["keySuffix"] = suffix
			}
			putRuntimeText(item, "cooldownUntil", state.cooldownUntil)
			putRuntimeText(item, "nextProbeAt", state.nextProbeAt)
			putRuntimeText(item, "lastAttemptAt", state.lastAttemptAt)
			putRuntimeText(item, "lastSuccessAt", state.lastSuccessAt)
			putRuntimeText(item, "lastFailureAt", state.lastFailureAt)
			putRuntimeText(item, "lastErrorCode", state.lastErrorCode)
			putRuntimeText(item, "lastErrorMessage", runtimeErrorMessageForResponse(state.lastErrorMessage))
			putRuntimeText(item, "lastTraceId", runtimeTraceIdForResponse(state.lastTraceID))
			items = append(items, item)
		}
		return items, nil
	}
	return []map[string]any{}, nil
}

// truncatePrefix 等价 Node fingerprint.slice(0, 12)。
func truncatePrefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// putRuntimeText 渲染 Node `state?.x ?? undefined` 分支：空值不出键。
func putRuntimeText(item map[string]any, key, value string) {
	if value == "" {
		return
	}
	item[key] = value
}
