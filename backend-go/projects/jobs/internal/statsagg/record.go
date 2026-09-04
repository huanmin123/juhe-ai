package statsagg

import (
	"math"
	"strings"
)

// AuthorizationLookup mirrors usage-stats-aggregation.ts
// UsageStatsAuthorizationLookup，并补充授权日报 writer 需要的
// resource_id 查找（createPostgresUsageStatsAuthorizationLookup 返回值）。
type AuthorizationLookup struct {
	AccountAuthorizationInstanceAccountIDs map[string]string
	AccountAuthorizationResourceIDs        map[string]string
}

func (l *AuthorizationLookup) setAccountAuthorizationResourceID(authorizationID, resourceID string) {
	if l.AccountAuthorizationResourceIDs == nil {
		l.AccountAuthorizationResourceIDs = map[string]string{}
	}
	l.AccountAuthorizationResourceIDs[authorizationID] = resourceID
}

// UsageStatsEntries 是 usage-stats-aggregation.ts usageStatsEntries 的逐行移植：
// 一条 usage_record 展开为多组 (systemAccountId, scopeType, scopeId) 统计维度。
func UsageStatsEntries(row UsageStatsRecordRow, lookup *AuthorizationLookup) []UsageStatsEntry {
	accumulator := UsageStatsAccumulatorFromRecord(row)
	callerSystemAccountID := row.SystemAccountID
	accountMetadata := usageStatsAccountMetadata(row)
	groupMetadata := usageStatsGroupMetadata(row)
	var accountStatsSystemAccountID string
	if accountMetadata != nil && accountMetadata.accessType == "account_authorized" {
		accountStatsSystemAccountID = callerSystemAccountID
	} else if accountMetadata != nil {
		accountStatsSystemAccountID = accountMetadata.ownerSystemAccountID
	}
	// Node：skipOwnerAccountStats = accountMetadata?.accessType !== 'account_authorized'
	//   && groupMetadata?.accessType === 'authorized'
	//   && accountMetadata?.ownerSystemAccountId !== callerSystemAccountId
	//（accountMetadata 为 undefined 时前段为 true、尾段为 true）
	skipOwnerAccountStats := (accountMetadata == nil || accountMetadata.accessType != "account_authorized") &&
		groupMetadata != nil && groupMetadata.accessType == "authorized" &&
		(accountMetadata == nil || accountMetadata.ownerSystemAccountID != callerSystemAccountID)
	skipOwnerGroupStats := groupMetadata != nil &&
		groupMetadata.accessType == "authorized" &&
		groupMetadata.ownerSystemAccountID != callerSystemAccountID

	entries := []UsageStatsEntry{
		{SystemAccountID: callerSystemAccountID, ScopeType: "system_account", ScopeID: callerSystemAccountID, Accumulator: accumulator},
		{SystemAccountID: GlobalStatsSystemAccountID, ScopeType: "system_account", ScopeID: GlobalStatsScopeID, Accumulator: accumulator},
	}
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "provider", ScopeID: *row.ProviderCode, Accumulator: accumulator})
	}
	if row.ProviderProtocolProfileID != nil && *row.ProviderProtocolProfileID != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "provider_protocol_profile", ScopeID: *row.ProviderProtocolProfileID, Accumulator: accumulator})
	}
	if row.GroupID != nil && *row.GroupID != "" && groupMetadata != nil && !skipOwnerGroupStats {
		entries = append(entries, UsageStatsEntry{SystemAccountID: groupMetadata.ownerSystemAccountID, ScopeType: "group", ScopeID: *row.GroupID, Accumulator: accumulator})
	}
	if row.AccountID != nil && *row.AccountID != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "caller_account", ScopeID: *row.AccountID, Accumulator: accumulator})
	}
	if row.AccountID != nil && *row.AccountID != "" && accountStatsSystemAccountID != "" && !skipOwnerAccountStats {
		entries = append(entries, UsageStatsEntry{SystemAccountID: accountStatsSystemAccountID, ScopeType: "account", ScopeID: *row.AccountID, Accumulator: accumulator})
	}
	if row.AccountID != nil && *row.AccountID != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: GlobalStatsSystemAccountID, ScopeType: "account", ScopeID: *row.AccountID, Accumulator: accumulator})
	}
	if row.AccountAuthorizationID != nil && *row.AccountAuthorizationID != "" &&
		(accountMetadata == nil || accountMetadata.ownerSystemAccountID != callerSystemAccountID) {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "account_authorization", ScopeID: *row.AccountAuthorizationID, Accumulator: accumulator})
	}
	if row.AccountID != nil && *row.AccountID != "" && row.AccountAuthorizationSourceTeamID != nil && *row.AccountAuthorizationSourceTeamID != "" &&
		(accountMetadata == nil || accountMetadata.ownerSystemAccountID != callerSystemAccountID) {
		entries = append(entries, UsageStatsEntry{
			SystemAccountID: callerSystemAccountID,
			ScopeType:       "account_authorization_team",
			ScopeID:         accountAuthorizationTeamAccountID(row, lookup) + ":" + *row.AccountAuthorizationSourceTeamID,
			Accumulator:     accumulator,
		})
	}
	if row.GroupAuthorizationID != nil && *row.GroupAuthorizationID != "" &&
		groupMetadata != nil && groupMetadata.ownerSystemAccountID != callerSystemAccountID {
		entries = append(entries, UsageStatsEntry{SystemAccountID: groupMetadata.ownerSystemAccountID, ScopeType: "group_authorization", ScopeID: *row.GroupAuthorizationID, Accumulator: accumulator})
	}
	if row.GroupID != nil && *row.GroupID != "" && row.GroupAuthorizationSourceTeamID != nil && *row.GroupAuthorizationSourceTeamID != "" &&
		groupMetadata != nil && groupMetadata.ownerSystemAccountID != callerSystemAccountID {
		entries = append(entries, UsageStatsEntry{
			SystemAccountID: groupMetadata.ownerSystemAccountID,
			ScopeType:       "group_authorization_team",
			ScopeID:         *row.GroupID + ":" + *row.GroupAuthorizationSourceTeamID,
			Accumulator:     accumulator,
		})
	}
	if row.APIKeyID != nil && *row.APIKeyID != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "api_key", ScopeID: *row.APIKeyID, Accumulator: accumulator})
	}
	if row.Model != nil && *row.Model != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "model", ScopeID: *row.Model, Accumulator: accumulator})
	}
	if row.Endpoint != nil && *row.Endpoint != "" {
		entries = append(entries, UsageStatsEntry{SystemAccountID: callerSystemAccountID, ScopeType: "endpoint", ScopeID: *row.Endpoint, Accumulator: accumulator})
	}
	return entries
}

type ownershipMetadata struct {
	ownerSystemAccountID string
	accessType           string
}

func usageStatsAccountMetadata(row UsageStatsRecordRow) *ownershipMetadata {
	if row.AccountID == nil || *row.AccountID == "" {
		return nil
	}
	if row.AccountOwnerSystemAccountID == nil || *row.AccountOwnerSystemAccountID == "" ||
		row.AccountAccessType == nil || *row.AccountAccessType == "" {
		return nil
	}
	return &ownershipMetadata{ownerSystemAccountID: *row.AccountOwnerSystemAccountID, accessType: *row.AccountAccessType}
}

func usageStatsGroupMetadata(row UsageStatsRecordRow) *ownershipMetadata {
	if row.GroupID == nil || *row.GroupID == "" {
		return nil
	}
	if row.GroupOwnerSystemAccountID == nil || *row.GroupOwnerSystemAccountID == "" ||
		row.GroupAccessType == nil || *row.GroupAccessType == "" {
		return nil
	}
	return &ownershipMetadata{ownerSystemAccountID: *row.GroupOwnerSystemAccountID, accessType: *row.GroupAccessType}
}

func accountAuthorizationTeamAccountID(row UsageStatsRecordRow, lookup *AuthorizationLookup) string {
	if row.AccountAuthorizationID != nil && lookup != nil {
		if instanceAccountID, ok := lookup.AccountAuthorizationInstanceAccountIDs[*row.AccountAuthorizationID]; ok && instanceAccountID != "" {
			return instanceAccountID
		}
	}
	if row.AccountID != nil {
		return *row.AccountID
	}
	return ""
}

// ShouldAggregateUsageStatsRecord 是 usage-stats-aggregation.ts
// shouldAggregateUsageStatsRecord 的逐行移植。
func ShouldAggregateUsageStatsRecord(row UsageStatsRecordRow) bool {
	if !usageStatsDimensionValueIsCanonical(row.AccountID) ||
		!usageStatsDimensionValueIsCanonical(row.GroupID) ||
		!usageStatsDimensionValueIsCanonical(row.AccountOwnerSystemAccountID) ||
		!usageStatsDimensionValueIsCanonical(row.GroupOwnerSystemAccountID) ||
		!usageStatsDimensionValueIsCanonical(row.AccountAccessType) ||
		!usageStatsDimensionValueIsCanonical(row.GroupAccessType) ||
		!usageStatsDimensionValueIsCanonical(row.AccountAuthorizationID) ||
		!usageStatsDimensionValueIsCanonical(row.AccountAuthorizationSourceType) ||
		!usageStatsDimensionValueIsCanonical(row.AccountAuthorizationSourceTeamID) ||
		!usageStatsDimensionValueIsCanonical(row.GroupAuthorizationID) ||
		!usageStatsDimensionValueIsCanonical(row.GroupAuthorizationSourceType) ||
		!usageStatsDimensionValueIsCanonical(row.GroupAuthorizationSourceTeamID) {
		return false
	}
	if row.AccountAccessType != nil && *row.AccountAccessType != "" &&
		!contains([]string{"owner", "account_authorized", "group_authorized"}, *row.AccountAccessType) {
		return false
	}
	if row.GroupAccessType != nil && *row.GroupAccessType != "" &&
		!contains([]string{"owner", "authorized"}, *row.GroupAccessType) {
		return false
	}
	if row.AccountAuthorizationSourceType != nil && *row.AccountAuthorizationSourceType != "" &&
		!contains([]string{"manual", "team"}, *row.AccountAuthorizationSourceType) {
		return false
	}
	if row.GroupAuthorizationSourceType != nil && *row.GroupAuthorizationSourceType != "" &&
		!contains([]string{"manual", "team"}, *row.GroupAuthorizationSourceType) {
		return false
	}
	if !usageStatsAuthorizationSourcePairIsValid(row.AccountAuthorizationSourceType, row.AccountAuthorizationSourceTeamID) ||
		!usageStatsAuthorizationSourcePairIsValid(row.GroupAuthorizationSourceType, row.GroupAuthorizationSourceTeamID) {
		return false
	}
	hasAccount := row.AccountID != nil && *row.AccountID != ""
	if hasAccount && (row.AccountOwnerSystemAccountID == nil || *row.AccountOwnerSystemAccountID == "" ||
		row.AccountAccessType == nil || *row.AccountAccessType == "") {
		return false
	}
	if !hasAccount && (isSet(row.AccountOwnerSystemAccountID) || isSet(row.AccountAccessType) ||
		isSet(row.AccountAuthorizationID) || isSet(row.AccountAuthorizationSourceType) || isSet(row.AccountAuthorizationSourceTeamID)) {
		return false
	}
	hasGroup := row.GroupID != nil && *row.GroupID != ""
	if !hasGroup && (isSet(row.GroupOwnerSystemAccountID) || isSet(row.GroupAccessType) ||
		isSet(row.GroupAuthorizationID) || isSet(row.GroupAuthorizationSourceType) || isSet(row.GroupAuthorizationSourceTeamID)) {
		return false
	}
	if hasGroup && (row.GroupOwnerSystemAccountID == nil || *row.GroupOwnerSystemAccountID == "" ||
		row.GroupAccessType == nil || *row.GroupAccessType == "") {
		return false
	}
	accountAuthorized := row.AccountAccessType != nil && *row.AccountAccessType == "account_authorized"
	if accountAuthorized && !isSet(row.AccountAuthorizationID) {
		return false
	}
	if row.AccountAccessType != nil && *row.AccountAccessType == "group_authorized" &&
		(!hasGroup || row.GroupAccessType == nil || *row.GroupAccessType != "authorized" || !isSet(row.GroupAuthorizationID)) {
		return false
	}
	if !accountAuthorized && (isSet(row.AccountAuthorizationID) || isSet(row.AccountAuthorizationSourceType) || isSet(row.AccountAuthorizationSourceTeamID)) {
		return false
	}
	groupAuthorized := row.GroupAccessType != nil && *row.GroupAccessType == "authorized"
	if groupAuthorized && !isSet(row.GroupAuthorizationID) {
		return false
	}
	if !groupAuthorized && (isSet(row.GroupAuthorizationID) || isSet(row.GroupAuthorizationSourceType) || isSet(row.GroupAuthorizationSourceTeamID)) {
		return false
	}
	if !isSet(row.AccountAuthorizationID) && (isSet(row.AccountAuthorizationSourceType) || isSet(row.AccountAuthorizationSourceTeamID)) {
		return false
	}
	if !isSet(row.GroupAuthorizationID) && (isSet(row.GroupAuthorizationSourceType) || isSet(row.GroupAuthorizationSourceTeamID)) {
		return false
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isSet(value *string) bool {
	return value != nil && *value != ""
}

func usageStatsDimensionValueIsCanonical(value *string) bool {
	if value == nil {
		return true
	}
	normalized := strings.TrimSpace(*value)
	return len(normalized) > 0 && normalized == *value
}

func usageStatsAuthorizationSourcePairIsValid(sourceType, sourceTeamID *string) bool {
	if sourceType != nil && *sourceType == "team" {
		return sourceTeamID != nil && *sourceTeamID != ""
	}
	return sourceTeamID == nil || *sourceTeamID == ""
}

// UsageStatsAccumulatorFromRecord 是 usage-stats-aggregation.ts
// usageStatsAccumulatorFromRecord 的移植。
func UsageStatsAccumulatorFromRecord(row UsageStatsRecordRow) UsageStatsAccumulator {
	success := row.Success == 1
	durationMs := 0.0
	if row.DurationMs != nil {
		durationMs = math.Max(0, orZero(row.DurationMs))
	}
	firstTokenMs := 0.0
	if row.FirstTokenMs != nil {
		firstTokenMs = math.Max(0, orZero(row.FirstTokenMs))
	}
	accumulator := UsageStatsAccumulator{
		RequestCount:       1,
		InputTokens:        math.Max(0, orZero(row.InputTokens)),
		OutputTokens:       math.Max(0, orZero(row.OutputTokens)),
		CacheReadTokens:    math.Max(0, orZero(row.CacheReadTokens)),
		CacheReadCostUsd:   math.Max(0, orZero(row.CacheReadCostUsd)),
		CacheWriteTokens:   math.Max(0, orZero(row.CacheWriteTokens)),
		CacheWrite1hTokens: math.Max(0, orZero(row.CacheWrite1hTokens)),
		CacheWriteCostUsd:  math.Max(0, orZero(row.CacheWriteCostUsd)),
		ThinkingTokens:     math.Max(0, orZero(row.ThinkingTokens)),
		InputImageTokens:   math.Max(0, orZero(row.InputImageTokens)),
		OutputImageTokens:  math.Max(0, orZero(row.OutputImageTokens)),
		TotalCostUsd:       math.Max(0, orZero(row.CostUsd)),
		DurationMsSum:      durationMs,
		DurationMsMax:      durationMs,
		FirstTokenMsSum:    firstTokenMs,
		FirstTokenMsMax:    firstTokenMs,
		LastUsedAt:         row.CreatedAt,
	}
	if success {
		accumulator.SuccessCount = 1
	} else {
		accumulator.ErrorCount = 1
		accumulator.LastErrorAt = row.CreatedAt
	}
	if row.DurationMs != nil {
		accumulator.DurationMsCount = 1
	} else {
		accumulator.DurationMsMax = 0
	}
	if row.FirstTokenMs != nil {
		accumulator.FirstTokenMsCount = 1
	} else {
		accumulator.FirstTokenMsMax = 0
	}
	return accumulator
}

// MergeAccumulator mirrors mergePostgresUsageStatsAccumulator。
func MergeAccumulator(target *UsageStatsAccumulator, source UsageStatsAccumulator) error {
	target.RequestCount += source.RequestCount
	target.SuccessCount += source.SuccessCount
	target.ErrorCount += source.ErrorCount
	target.InputTokens += source.InputTokens
	target.OutputTokens += source.OutputTokens
	target.CacheReadTokens += source.CacheReadTokens
	target.CacheReadCostUsd += source.CacheReadCostUsd
	target.CacheWriteTokens += source.CacheWriteTokens
	target.CacheWrite1hTokens += source.CacheWrite1hTokens
	target.CacheWriteCostUsd += source.CacheWriteCostUsd
	target.ThinkingTokens += source.ThinkingTokens
	target.InputImageTokens += source.InputImageTokens
	target.OutputImageTokens += source.OutputImageTokens
	target.TotalCostUsd += source.TotalCostUsd
	target.DurationMsSum += source.DurationMsSum
	target.DurationMsCount += source.DurationMsCount
	target.DurationMsMax = math.Max(target.DurationMsMax, source.DurationMsMax)
	target.FirstTokenMsSum += source.FirstTokenMsSum
	target.FirstTokenMsCount += source.FirstTokenMsCount
	target.FirstTokenMsMax = math.Max(target.FirstTokenMsMax, source.FirstTokenMsMax)
	lastUsedAt, err := MaxOptionalISO(target.LastUsedAt, source.LastUsedAt)
	if err != nil {
		return err
	}
	target.LastUsedAt = lastUsedAt
	lastErrorAt, err := MaxOptionalISO(target.LastErrorAt, source.LastErrorAt)
	if err != nil {
		return err
	}
	target.LastErrorAt = lastErrorAt
	return nil
}

func orZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
