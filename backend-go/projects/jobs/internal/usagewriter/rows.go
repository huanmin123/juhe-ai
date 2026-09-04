package usagewriter

import (
	"encoding/json"
	"fmt"
)

// 批量写计划移植，对照 backend/src/storage/usage-records.repository.ts
// buildUsageRecordBatchWritePlan 与 usage-records.repository.ts 的行参数序、
// 默认值、失败归因与流量来源校验。

// ShardWriteRow mirrors UsageRecordShardWriteRow: the insert parameters in
// the canonical usageRecordColumns order plus the account side-effect facts.
type ShardWriteRow struct {
	ID     string
	Params []any
	// AccountID mirrors row.accountId (undefined → nil).
	AccountID string
	// AccountLastUsedAt / AccountHealthSuccessAt mirror the Node fields
	// (empty = undefined).
	AccountLastUsedAt      string
	AccountHealthSuccessAt string
}

// ShardEntry mirrors UsageRecordShardEntryInput: the catalog entry row.
type ShardEntry struct {
	ID                 string   `json:"id"`
	ShardKey           string   `json:"shardKey"`
	SystemAccountID    string   `json:"systemAccountId"`
	TraceID            string   `json:"traceId"`
	APIKeyID           *string  `json:"apiKeyId"`
	AccountID          *string  `json:"accountId"`
	GroupID            *string  `json:"groupId"`
	Model              *string  `json:"model"`
	TrafficSource      string   `json:"trafficSource"`
	Success            bool     `json:"success"`
	FailureAttribution *string  `json:"failureAttribution"`
	StatusCode         *int     `json:"statusCode"`
	ClientIP           *string  `json:"clientIp"`
	FirstTokenMs       *int     `json:"firstTokenMs"`
	DurationMs         *int     `json:"durationMs"`
	CostUsd            *float64 `json:"costUsd"`
	CreatedAt          string   `json:"createdAt"`
}

// WritePlan mirrors UsageRecordBatchWritePlan: rows grouped per shard plus
// the catalog entries.
type WritePlan struct {
	// RowsByShard preserves the Node Map insertion order (first record's
	// shard first).
	RowsByShard  []ShardRows
	ShardEntries []ShardEntry
	Locations    []UsageRecordShardLocation
}

// ShardRows is one map entry of rowsByShard.
type ShardRows struct {
	Location UsageRecordShardLocation
	Rows     []ShardWriteRow
}

// UsageRecordColumns mirrors postgresUsageRecordColumns (identical to the
// SQLite INSERT column list in usage-record-shards.ts).
var UsageRecordColumns = []string{
	"id",
	"system_account_id",
	"trace_id",
	"traffic_source",
	"client_ip",
	"api_key_id",
	"group_id",
	"account_id",
	"endpoint",
	"provider_code",
	"provider_protocol_profile_id",
	"usage_semantic",
	"model",
	"upstream_model",
	"upstream_response_model",
	"pricing_model",
	"requested_service_tier",
	"effective_service_tier",
	"reported_service_tier",
	"billed_service_tier",
	"requested_reasoning_effort",
	"effective_reasoning_effort",
	"cost_breakdown_snapshot_json",
	"model_mapping_applied",
	"model_mapping_source",
	"source_endpoint_family",
	"upstream_endpoint_family",
	"stream",
	"status_code",
	"success",
	"failure_attribution",
	"first_token_ms",
	"duration_ms",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_read_cost_usd",
	"cache_write_tokens",
	"cache_write_1h_tokens",
	"cache_write_cost_usd",
	"thinking_tokens",
	"input_image_tokens",
	"output_image_tokens",
	"input_audio_tokens",
	"output_audio_tokens",
	"output_image_count",
	"cost_usd",
	"error_code",
	"error_message",
	"request_snapshot_json",
	"response_snapshot_json",
	"account_owner_system_account_id",
	"group_owner_system_account_id",
	"account_access_type",
	"group_access_type",
	"account_authorization_id",
	"account_authorization_source_type",
	"account_authorization_source_team_id",
	"group_authorization_id",
	"group_authorization_source_type",
	"group_authorization_source_team_id",
	"created_at",
}

// ScopeLookup ports the SQLite-mode access lookups
// (usage-record-access-metadata.ts): the usage API key existence check and
// the system-account resolution for records that arrive without one. nil
// functions mean "lookups unavailable", matching a reader-degraded store.
type ScopeLookup struct {
	// APIKeyExists mirrors usageApiKeyExists(apiKeyId, context).
	APIKeyExists func(apiKeyID string) bool
	// SystemAccountIDForAPIKey mirrors systemAccountIdForUsage.
	SystemAccountIDForAPIKey func(apiKeyID string) string
}

// WritePlanOptions mirror the buildUsageRecordBatchWritePlan options.
type WritePlanOptions struct {
	// Postgres mirrors shardLocationMode: 'postgres' (logical shard
	// locations, provided-only access, no api-key existence filter).
	Postgres bool
	// CatalogSnapshotEnabled mirrors the databaseDriver !== 'postgres' guard
	// of usageRecordPricingSnapshotForWrite: catalog snapshots are attempted
	// only off Postgres.
	CatalogSnapshotEnabled bool
	// Catalog is the optional pricing port (may be nil).
	Catalog CatalogPricing
	// Scope carries the SQLite-mode access lookups (may be nil).
	Scope *ScopeLookup
	// ShardCount mirrors runtimeConfig.usageShardCount.
	ShardCount int
	// ShardRoot is the SQLite usage-shard root directory.
	ShardRoot string
}

// BuildWritePlan mirrors buildUsageRecordBatchWritePlan: validate and shape
// every input into shard-routed rows plus catalog entries. The clock supplies
// nowIso() for records without createdAt.
func BuildWritePlan(ctx Ctx, inputs []UsageRecordInput, options WritePlanOptions, clock Clock) (WritePlan, error) {
	plan := WritePlan{}
	shardIndex := map[string]int{}
	for _, input := range inputs {
		createdAt := input.CreatedAt
		if createdAt == "" {
			createdAt = clock.Now().UTC().Format(timeRFC3339Millis)
		} else {
			normalized, err := requiredRFC3339Instant(createdAt, "使用记录 createdAt")
			if err != nil {
				return WritePlan{}, err
			}
			createdAt = normalized
		}
		if !options.Postgres && input.APIKeyID != "" && options.Scope != nil && options.Scope.APIKeyExists != nil {
			if !options.Scope.APIKeyExists(input.APIKeyID) {
				// Node silently skips records whose api key vanished.
				continue
			}
		}
		systemAccountID := input.SystemAccountID
		if systemAccountID == "" {
			if options.Postgres {
				return WritePlan{}, fmt.Errorf("PostgreSQL 使用记录写入必须提供 systemAccountId")
			}
			if options.Scope != nil && options.Scope.SystemAccountIDForAPIKey != nil && input.APIKeyID != "" {
				systemAccountID = options.Scope.SystemAccountIDForAPIKey(input.APIKeyID)
			}
		}
		id := input.ID
		if id == "" {
			generated, err := GenerateUsageRecordID(clock, createdAt, NewRandomUUID(), options.ShardCount)
			if err != nil {
				return WritePlan{}, err
			}
			id = generated
		}
		trafficSource, err := NormalizeUsageRecordTrafficSource(input.TrafficSource)
		if err != nil {
			return WritePlan{}, err
		}
		failureAttribution, err := UsageFailureAttributionForInput(input)
		if err != nil {
			return WritePlan{}, err
		}
		pricingSnapshot := PricingSnapshotForWrite(ctx, input, options.CatalogSnapshotEnabled, options.Catalog)

		row := ShardWriteRow{
			ID: id,
			Params: []any{
				id,
				systemAccountID,
				input.TraceID,
				trafficSource,
				nilableString(input.ClientIP),
				nilableString(input.APIKeyID),
				nilableString(input.GroupID),
				nilableString(input.AccountID),
				nilableString(input.Endpoint),
				nilableString(input.ProviderCode),
				nilableString(input.ProviderProtocolProfileID),
				nilableString(input.UsageSemantic),
				nilableString(input.Model),
				nilableString(input.UpstreamModel),
				nilableString(input.UpstreamResponseModel),
				nilableString(input.PricingModel),
				stringDefault(input.RequestedServiceTier, "default"),
				stringDefault(firstNonEmpty(input.EffectiveServiceTier, input.RequestedServiceTier), "default"),
				nilableString(input.ReportedServiceTier),
				stringDefault(firstNonEmpty(input.BilledServiceTier, input.ReportedServiceTier, input.EffectiveServiceTier, input.RequestedServiceTier), "default"),
				nilableString(input.RequestedReasoningEffort),
				nilableString(input.EffectiveReasoningEffort),
				jsonSnapshotOrNil(pricingSnapshot),
				boolToInt(input.ModelMappingApplied),
				nilableString(input.ModelMappingSource),
				nilableString(input.SourceEndpointFamily),
				nilableString(input.UpstreamEndpointFamily),
				boolToInt(input.Stream),
				nilableInt(input.StatusCode),
				boolToInt(&input.Success),
				nilableString(failureAttribution),
				nilableInt(input.FirstTokenMs),
				nilableInt(input.DurationMs),
				nilableInt(input.InputTokens),
				nilableInt(input.OutputTokens),
				nilableInt(input.CacheReadTokens),
				nilableFloat(input.CacheReadCostUsd),
				nilableInt(input.CacheWriteTokens),
				nilableInt(input.CacheWrite1hTokens),
				nilableFloat(input.CacheWriteCostUsd),
				nilableInt(input.ThinkingTokens),
				nilableInt(input.InputImageTokens),
				nilableInt(input.OutputImageTokens),
				nilableInt(input.InputAudioTokens),
				nilableInt(input.OutputAudioTokens),
				nilableInt(input.OutputImageCount),
				nilableFloat(input.CostUsd),
				nilableString(input.ErrorCode),
				nilableString(input.ErrorMessage),
				jsonSnapshot(input.RequestSnapshot),
				jsonSnapshot(input.ResponseSnapshot),
				nilableString(input.AccountOwnerSystemAccountID),
				nilableString(input.GroupOwnerSystemAccountID),
				nilableString(input.AccountAccessType),
				nilableString(input.GroupAccessType),
				nilableString(input.AccountAuthorizationID),
				nilableString(input.AccountAuthorizationSourceType),
				nilableString(input.AccountAuthorizationSourceTeamID),
				nilableString(input.GroupAuthorizationID),
				nilableString(input.GroupAuthorizationSourceType),
				nilableString(input.GroupAuthorizationSourceTeamID),
				createdAt,
			},
		}
		// Account side effects only for the gateway traffic source
		// (shouldRecordAccountUsageSideEffects).
		if trafficSource == TrafficSourceGateway {
			row.AccountID = input.AccountID
			row.AccountLastUsedAt = createdAt
			if input.Success {
				row.AccountHealthSuccessAt = createdAt
			}
		}

		var location UsageRecordShardLocation
		if options.Postgres {
			resolved, err := UsageRecordLogicalShardLocationForPostgres(id, createdAt, options.ShardCount)
			if err != nil {
				return WritePlan{}, err
			}
			location = resolved
		} else {
			resolved, err := UsageRecordShardLocationForRecord(id, createdAt, options.ShardCount, options.ShardRoot)
			if err != nil {
				return WritePlan{}, err
			}
			location = resolved
		}
		index, exists := shardIndex[location.ShardKey]
		if !exists {
			index = len(plan.RowsByShard)
			shardIndex[location.ShardKey] = index
			plan.RowsByShard = append(plan.RowsByShard, ShardRows{Location: location})
			plan.Locations = append(plan.Locations, location)
		}
		plan.RowsByShard[index].Rows = append(plan.RowsByShard[index].Rows, row)

		plan.ShardEntries = append(plan.ShardEntries, ShardEntry{
			ID:                 id,
			ShardKey:           location.ShardKey,
			SystemAccountID:    systemAccountID,
			TraceID:            input.TraceID,
			APIKeyID:           nilableStringPtr(input.APIKeyID),
			AccountID:          nilableStringPtr(input.AccountID),
			GroupID:            nilableStringPtr(input.GroupID),
			Model:              nilableStringPtr(input.Model),
			TrafficSource:      trafficSource,
			Success:            input.Success,
			FailureAttribution: nilableStringPtr(failureAttribution),
			StatusCode:         input.StatusCode,
			ClientIP:           nilableStringPtr(input.ClientIP),
			FirstTokenMs:       input.FirstTokenMs,
			DurationMs:         input.DurationMs,
			CostUsd:            input.CostUsd,
			CreatedAt:          createdAt,
		})
	}
	return plan, nil
}

// MergeShardWriteResult mirrors mergeUsageRecordShardWriteResult over the
// collected side-effect rows: keep the max ISO value per account.
func MergeShardWriteResult(lastUsedAt map[string]string, healthSuccessAt map[string]string, rows []ShardWriteRow) {
	for _, row := range rows {
		if row.AccountID == "" {
			continue
		}
		if row.AccountLastUsedAt != "" {
			mergeMaxISOValue(lastUsedAt, row.AccountID, row.AccountLastUsedAt)
		}
		if row.AccountHealthSuccessAt != "" {
			mergeMaxISOValue(healthSuccessAt, row.AccountID, row.AccountHealthSuccessAt)
		}
	}
}

func mergeMaxISOValue(target map[string]string, key string, value string) {
	if previous, exists := target[key]; !exists || value > previous {
		target[key] = value
	}
}

// NormalizeUsageRecordTrafficSource mirrors normalizeUsageRecordTrafficSource,
// including the Chinese error copy "使用记录来源无效".
func NormalizeUsageRecordTrafficSource(value string) (string, error) {
	switch value {
	case TrafficSourceGateway,
		TrafficSourceManualAccountTest,
		TrafficSourceAccountHealthCheck,
		TrafficSourceRuntimeRecoveryProb,
		TrafficSourceCooldownRetest,
		TrafficSourceHybridScoring,
		TrafficSourceHybridQualityScore:
		return value, nil
	}
	return "", fmt.Errorf("使用记录来源无效")
}

// UsageFailureAttributionForInput mirrors usageFailureAttributionForInput:
// successes carry no attribution; explicit attributions are normalized;
// failures default to account_upstream with an account, gateway_policy
// without. The invalid-value copy is "使用记录失败归因无效".
func UsageFailureAttributionForInput(input UsageRecordInput) (string, error) {
	if input.Success {
		return "", nil
	}
	if input.FailureAttribution != "" {
		switch input.FailureAttribution {
		case FailureAttributionAccountUpstream,
			FailureAttributionAccountDependency,
			FailureAttributionOpaqueUpstream,
			FailureAttributionGatewayCapacity,
			FailureAttributionGatewayPolicy,
			FailureAttributionDownstreamClosed:
			return input.FailureAttribution, nil
		}
		return "", fmt.Errorf("使用记录失败归因无效")
	}
	if input.AccountID != "" {
		return FailureAttributionAccountUpstream, nil
	}
	return FailureAttributionGatewayPolicy, nil
}

func nilableStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nilableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nilableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nilableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolToInt(value *bool) any {
	if value == nil {
		return 0
	}
	if *value {
		return 1
	}
	return 0
}

// jsonSnapshotOrNil guards the typed-nil case: a nil *CostBreakdown inside
// an any is not == nil, but Node's undefined must persist as NULL.
func jsonSnapshotOrNil(snapshot *CostBreakdown) any {
	if snapshot == nil {
		return nil
	}
	return jsonSnapshot(snapshot)
}

// jsonSnapshot mirrors `value ? JSON.stringify(value) : null`; an object
// without JSON shape degrades like Node would (JSON.stringify of a
// non-serializable value returns undefined → null).
func jsonSnapshot(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return string(encoded)
}
