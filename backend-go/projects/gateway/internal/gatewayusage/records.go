package gatewayusage

import (
	"reflect"
	"strings"
	"time"
)

// UsageRecordInput mirrors UsageRecordInput
// (backend/src/storage/usage-records.repository.ts) field for field. Empty
// strings and nil pointers mean the Node undefined fields; Success, TraceID
// and TrafficSource are always set by the record builders.
type UsageRecordInput struct {
	ID                               string `json:"id,omitempty"`
	SystemAccountID                  string `json:"systemAccountId,omitempty"`
	TraceID                          string `json:"traceId"`
	TrafficSource                    string `json:"trafficSource"`
	ClientIP                         string `json:"clientIp,omitempty"`
	APIKeyID                         string `json:"apiKeyId,omitempty"`
	GroupID                          string `json:"groupId,omitempty"`
	AccountID                        string `json:"accountId,omitempty"`
	AccountOwnerSystemAccountID      string `json:"accountOwnerSystemAccountId,omitempty"`
	GroupOwnerSystemAccountID        string `json:"groupOwnerSystemAccountId,omitempty"`
	AccountAccessType                string `json:"accountAccessType,omitempty"`
	GroupAccessType                  string `json:"groupAccessType,omitempty"`
	AccountAuthorizationID           string `json:"accountAuthorizationId,omitempty"`
	AccountAuthorizationSourceType   string `json:"accountAuthorizationSourceType,omitempty"`
	AccountAuthorizationSourceTeamID string `json:"accountAuthorizationSourceTeamId,omitempty"`
	GroupAuthorizationID             string `json:"groupAuthorizationId,omitempty"`
	GroupAuthorizationSourceType     string `json:"groupAuthorizationSourceType,omitempty"`
	GroupAuthorizationSourceTeamID   string `json:"groupAuthorizationSourceTeamId,omitempty"`
	Endpoint                         string `json:"endpoint,omitempty"`
	ProviderCode                     string `json:"providerCode,omitempty"`
	ProviderProtocolProfileID        string `json:"providerProtocolProfileId,omitempty"`
	UsageSemantic                    string `json:"usageSemantic,omitempty"`
	Model                            string `json:"model,omitempty"`
	UpstreamModel                    string `json:"upstreamModel,omitempty"`
	UpstreamResponseModel            string `json:"upstreamResponseModel,omitempty"`
	PricingModel                     string `json:"pricingModel,omitempty"`
	RequestedServiceTier             string `json:"requestedServiceTier,omitempty"`
	EffectiveServiceTier             string `json:"effectiveServiceTier,omitempty"`
	ReportedServiceTier              string `json:"reportedServiceTier,omitempty"`
	BilledServiceTier                string `json:"billedServiceTier,omitempty"`
	RequestedReasoningEffort         string `json:"requestedReasoningEffort,omitempty"`
	EffectiveReasoningEffort         string `json:"effectiveReasoningEffort,omitempty"`
	PricingSnapshot                  any    `json:"pricingSnapshot,omitempty"`
	ModelMappingApplied              *bool  `json:"modelMappingApplied,omitempty"`
	ModelMappingSource               string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily             string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily           string `json:"upstreamEndpointFamily,omitempty"`
	Stream                           *bool  `json:"stream,omitempty"`
	StatusCode                       *int   `json:"statusCode,omitempty"`
	Success                          bool   `json:"success"`
	FailureAttribution               string `json:"failureAttribution,omitempty"`
	FirstTokenMs                     *int   `json:"firstTokenMs,omitempty"`
	DurationMs                       *int   `json:"durationMs,omitempty"`
	InputTokens                      *int   `json:"inputTokens,omitempty"`
	OutputTokens                     *int   `json:"outputTokens,omitempty"`
	CacheReadTokens                  *int   `json:"cacheReadTokens,omitempty"`
	CacheReadCostUsd                 *float64 `json:"cacheReadCostUsd,omitempty"`
	CacheWriteTokens                 *int   `json:"cacheWriteTokens,omitempty"`
	CacheWrite1hTokens               *int   `json:"cacheWrite1hTokens,omitempty"`
	CacheWriteCostUsd                *float64 `json:"cacheWriteCostUsd,omitempty"`
	ThinkingTokens                   *int   `json:"thinkingTokens,omitempty"`
	InputImageTokens                 *int   `json:"inputImageTokens,omitempty"`
	OutputImageTokens                *int   `json:"outputImageTokens,omitempty"`
	InputAudioTokens                 *int   `json:"inputAudioTokens,omitempty"`
	OutputAudioTokens                *int   `json:"outputAudioTokens,omitempty"`
	OutputImageCount                 *int   `json:"outputImageCount,omitempty"`
	CostUsd                          *float64 `json:"costUsd,omitempty"`
	ErrorCode                        string `json:"errorCode,omitempty"`
	ErrorMessage                     string `json:"errorMessage,omitempty"`
	RequestSnapshot                  any    `json:"requestSnapshot,omitempty"`
	ResponseSnapshot                 any    `json:"responseSnapshot,omitempty"`
	CreatedAt                        string `json:"createdAt,omitempty"`
}

// UsageFailureAttribution mirrors the Node union.
type UsageFailureAttribution = string

// Failure attribution values (usage-records.repository.ts).
const (
	FailureAttributionAccountUpstream   UsageFailureAttribution = "account_upstream"
	FailureAttributionAccountDependency UsageFailureAttribution = "account_dependency"
	FailureAttributionOpaqueUpstream    UsageFailureAttribution = "opaque_upstream"
	FailureAttributionGatewayCapacity   UsageFailureAttribution = "gateway_capacity"
	FailureAttributionGatewayPolicy     UsageFailureAttribution = "gateway_policy"
	FailureAttributionDownstreamClosed  UsageFailureAttribution = "downstream_closed"
)

// Usage access scope discriminator values.
const (
	AccountAccessTypeOwner            = "owner"
	AccountAccessTypeAccountAuthorized = "account_authorized"
	AccountAccessTypeGroupAuthorized   = "group_authorized"
	GroupAccessTypeOwner              = "owner"
	GroupAccessTypeAuthorized         = "authorized"
)

// AuthorizationSourceTypeManual / AuthorizationSourceTypeTeam mirror
// ResourceAuthorizationSourceType.
const (
	AuthorizationSourceTypeManual = "manual"
	AuthorizationSourceTypeTeam   = "team"
)

// Snapshot bounding limits (record-queue.service.ts).
const (
	usageSnapshotMaxBytes       = 64 * 1024
	usageSnapshotMaxStringBytes = 16 * 1024
	usageSnapshotMaxArrayItems  = 50
	usageSnapshotMaxObjectKeys  = 80
	usageSnapshotMaxDepth       = 6
)

type snapshotBoundContext struct {
	depth     int
	bytes     int
	truncated bool
	seen      *identitySet
}

// NormalizeUsageRecordInput mirrors normalizeUsageRecordInput
// (record-queue.service.ts): stable id/createdAt, bounded snapshots and the
// account/group scope integrity rules. The id factory and clock come from
// the service ports so the shard-id format stays with the writer slice.
func NormalizeUsageRecordInput(input UsageRecordInput, clock Clock, idFactory UsageRecordIDFactory) (UsageRecordInput, error) {
	createdAt := input.CreatedAt
	if createdAt == "" {
		createdAt = clock.Now().UTC().Format(timeRFC3339Millis)
	} else {
		normalized, err := requiredRFC3339Instant(createdAt, "使用记录 createdAt")
		if err != nil {
			return UsageRecordInput{}, err
		}
		createdAt = normalized
	}
	normalized := input
	normalized.CreatedAt = createdAt
	if normalized.ID == "" && idFactory != nil {
		normalized.ID = idFactory.GenerateUsageRecordID(createdAt)
	}
	normalized.RequestSnapshot = BoundUsageRecordSnapshot(input.RequestSnapshot)
	normalized.ResponseSnapshot = BoundUsageRecordSnapshot(input.ResponseSnapshot)

	// Account scope integrity.
	accountID := normalizedUsageScopeValue(input.AccountID)
	accountOwnerSystemAccountID := normalizedUsageScopeValue(input.AccountOwnerSystemAccountID)
	accountAccessType := normalizedAccountAccessType(input.AccountAccessType)
	accountAuthorizationID := ""
	if accountAccessType == AccountAccessTypeAccountAuthorized {
		accountAuthorizationID = normalizedUsageScopeValue(input.AccountAuthorizationID)
	}
	accountAuthorizationSourceType := normalizedAuthorizationSourceType(input.AccountAuthorizationSourceType)
	accountAuthorizationSourceTeamID := normalizedUsageScopeValue(input.AccountAuthorizationSourceTeamID)
	if accountID == "" || accountOwnerSystemAccountID == "" || accountAccessType == "" ||
		(accountAccessType == AccountAccessTypeAccountAuthorized && accountAuthorizationID == "") ||
		(accountAuthorizationID != "" && !authorizationSourcePairIsValid(accountAuthorizationSourceType, accountAuthorizationSourceTeamID)) {
		normalized.AccountID = ""
		normalized.AccountOwnerSystemAccountID = ""
		normalized.AccountAccessType = ""
		normalized.AccountAuthorizationID = ""
		normalized.AccountAuthorizationSourceType = ""
		normalized.AccountAuthorizationSourceTeamID = ""
	} else {
		normalized.AccountID = accountID
		normalized.AccountOwnerSystemAccountID = accountOwnerSystemAccountID
		normalized.AccountAccessType = accountAccessType
		normalized.AccountAuthorizationID = accountAuthorizationID
		if accountAuthorizationID != "" {
			normalized.AccountAuthorizationSourceType = accountAuthorizationSourceType
			normalized.AccountAuthorizationSourceTeamID = accountAuthorizationSourceTeamID
		} else {
			normalized.AccountAuthorizationSourceType = ""
			normalized.AccountAuthorizationSourceTeamID = ""
		}
	}

	// Group scope integrity.
	groupID := normalizedUsageScopeValue(input.GroupID)
	groupOwnerSystemAccountID := normalizedUsageScopeValue(input.GroupOwnerSystemAccountID)
	groupAccessType := normalizedGroupAccessType(input.GroupAccessType)
	groupAuthorizationID := ""
	if groupAccessType == GroupAccessTypeAuthorized {
		groupAuthorizationID = normalizedUsageScopeValue(input.GroupAuthorizationID)
	}
	groupAuthorizationSourceType := normalizedAuthorizationSourceType(input.GroupAuthorizationSourceType)
	groupAuthorizationSourceTeamID := normalizedUsageScopeValue(input.GroupAuthorizationSourceTeamID)
	if groupID == "" || groupOwnerSystemAccountID == "" || groupAccessType == "" ||
		(groupAccessType == GroupAccessTypeAuthorized && groupAuthorizationID == "") ||
		(groupAuthorizationID != "" && !authorizationSourcePairIsValid(groupAuthorizationSourceType, groupAuthorizationSourceTeamID)) {
		normalized.GroupID = ""
		normalized.GroupOwnerSystemAccountID = ""
		normalized.GroupAccessType = ""
		normalized.GroupAuthorizationID = ""
		normalized.GroupAuthorizationSourceType = ""
		normalized.GroupAuthorizationSourceTeamID = ""
	} else {
		normalized.GroupID = groupID
		normalized.GroupOwnerSystemAccountID = groupOwnerSystemAccountID
		normalized.GroupAccessType = groupAccessType
		normalized.GroupAuthorizationID = groupAuthorizationID
		if groupAuthorizationID != "" {
			normalized.GroupAuthorizationSourceType = groupAuthorizationSourceType
			normalized.GroupAuthorizationSourceTeamID = groupAuthorizationSourceTeamID
		} else {
			normalized.GroupAuthorizationSourceType = ""
			normalized.GroupAuthorizationSourceTeamID = ""
		}
	}

	// A group_authorized account requires a fully resolved authorized group.
	if normalized.AccountAccessType == AccountAccessTypeGroupAuthorized &&
		(normalized.GroupID == "" || normalized.GroupAccessType != GroupAccessTypeAuthorized || normalized.GroupAuthorizationID == "") {
		normalized.AccountID = ""
		normalized.AccountOwnerSystemAccountID = ""
		normalized.AccountAccessType = ""
		normalized.AccountAuthorizationID = ""
		normalized.AccountAuthorizationSourceType = ""
		normalized.AccountAuthorizationSourceTeamID = ""
	}
	return normalized, nil
}

func normalizedUsageScopeValue(value string) string {
	return strings.TrimSpace(value)
}

func normalizedAccountAccessType(value string) string {
	switch value {
	case AccountAccessTypeOwner, AccountAccessTypeAccountAuthorized, AccountAccessTypeGroupAuthorized:
		return value
	}
	return ""
}

func normalizedGroupAccessType(value string) string {
	switch value {
	case GroupAccessTypeOwner, GroupAccessTypeAuthorized:
		return value
	}
	return ""
}

func normalizedAuthorizationSourceType(value string) string {
	switch value {
	case AuthorizationSourceTypeManual, AuthorizationSourceTypeTeam:
		return value
	}
	return ""
}

func authorizationSourcePairIsValid(sourceType, sourceTeamID string) bool {
	if sourceType == AuthorizationSourceTypeTeam {
		return sourceTeamID != ""
	}
	return sourceTeamID == ""
}

// BoundUsageRecordSnapshot mirrors boundUsageRecordSnapshot: bound a
// JSON-like snapshot to the persisted size budget, marking truncations
// inline exactly like the Node walk.
func BoundUsageRecordSnapshot(value any) any {
	if value == nil {
		return nil
	}
	return boundSnapshotValue(value, &snapshotBoundContext{seen: newIdentitySet()})
}

func boundSnapshotValue(value any, context *snapshotBoundContext) any {
	if context.bytes >= usageSnapshotMaxBytes {
		context.truncated = true
		return "[truncated]"
	}
	switch typed := value.(type) {
	case nil:
		context.bytes += 8
		return nil
	case string:
		return boundSnapshotString(typed, context)
	case bool, int, int64, float64, uint64:
		context.bytes += 8
		return value
	case []byte:
		counted := len(typed)
		if counted > usageSnapshotMaxStringBytes {
			counted = usageSnapshotMaxStringBytes
		}
		context.bytes += counted
		buffer := NewOrderedObject()
		buffer.Set("_buffer", true)
		buffer.Set("bytes", len(typed))
		buffer.Set("truncated", len(typed) > usageSnapshotMaxStringBytes)
		return buffer
	case time.Time:
		return boundSnapshotString(typed.UTC().Format(timeRFC3339Millis), context)
	case []any:
		return boundSnapshotArray(typed, context)
	case *OrderedObject:
		if context.seen.add(value) {
			return "[circular]"
		}
		if context.depth >= usageSnapshotMaxDepth {
			context.truncated = true
			return "[depth_truncated]"
		}
		output := NewOrderedObject()
		visited := 0
		truncatedByKeyLimit := false
		for _, key := range typed.Keys() {
			if visited >= usageSnapshotMaxObjectKeys {
				truncatedByKeyLimit = true
				break
			}
			context.bytes += boundedStringByteLength(key, usageSnapshotMaxBytes-context.bytes) + 4
			context.depth++
			output.Set(key, boundSnapshotValue(typed.Get(key), context))
			context.depth--
			visited++
			if context.bytes >= usageSnapshotMaxBytes {
				break
			}
		}
		if truncatedByKeyLimit || context.truncated || context.bytes >= usageSnapshotMaxBytes {
			output.Set("_truncated", true)
		}
		return output
	case map[string]any:
		if context.seen.add(value) {
			return "[circular]"
		}
		if context.depth >= usageSnapshotMaxDepth {
			context.truncated = true
			return "[depth_truncated]"
		}
		output := NewOrderedObject()
		visited := 0
		truncatedByKeyLimit := false
		for _, key := range sortedMapKeys(typed) {
			if visited >= usageSnapshotMaxObjectKeys {
				truncatedByKeyLimit = true
				break
			}
			context.bytes += boundedStringByteLength(key, usageSnapshotMaxBytes-context.bytes) + 4
			context.depth++
			output.Set(key, boundSnapshotValue(typed[key], context))
			context.depth--
			visited++
			if context.bytes >= usageSnapshotMaxBytes {
				break
			}
		}
		if truncatedByKeyLimit || context.truncated || context.bytes >= usageSnapshotMaxBytes {
			output.Set("_truncated", true)
		}
		return output
	default:
		if bounded, ok := boundSnapshotStruct(value, context); ok {
			return bounded
		}
		return boundSnapshotString(displayString(value), context)
	}
}

// boundSnapshotStruct walks typed struct snapshots (UsageRequestSnapshot /
// UsageResponseSnapshot) with their JSON names in declaration order, the
// equivalent of the Node plain objects. Nil pointer/interface fields are
// skipped like undefined properties. Returns false for non-structs.
func boundSnapshotStruct(value any, context *snapshotBoundContext) (any, bool) {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	if context.seen.add(value) {
		return "[circular]", true
	}
	if context.depth >= usageSnapshotMaxDepth {
		context.truncated = true
		return "[depth_truncated]", true
	}
	if context.bytes >= usageSnapshotMaxBytes {
		context.truncated = true
		return "[truncated]", true
	}
	output := NewOrderedObject()
	rt := rv.Type()
	for index := 0; index < rt.NumField(); index++ {
		field := rt.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name, keep := jsonFieldName(field)
		if !keep {
			continue
		}
		fieldValue := rv.Field(index)
		if (fieldValue.Kind() == reflect.Ptr || fieldValue.Kind() == reflect.Interface) && fieldValue.IsNil() {
			continue
		}
		if context.bytes >= usageSnapshotMaxBytes {
			context.truncated = true
			output.Set("_truncated", true)
			return output, true
		}
		context.bytes += boundedStringByteLength(name, usageSnapshotMaxBytes-context.bytes) + 4
		context.depth++
		output.Set(name, boundSnapshotValue(exportFieldValue(fieldValue), context))
		context.depth--
	}
	if context.truncated || context.bytes >= usageSnapshotMaxBytes {
		output.Set("_truncated", true)
	}
	return output, true
}

func boundSnapshotArray(items []any, context *snapshotBoundContext) any {
	if context.seen.add(items) {
		return "[circular]"
	}
	if context.depth >= usageSnapshotMaxDepth {
		context.truncated = true
		return "[depth_truncated]"
	}
	output := make([]any, 0, len(items))
	for index := 0; index < len(items) && index < usageSnapshotMaxArrayItems; index++ {
		context.depth++
		output = append(output, boundSnapshotValue(items[index], context))
		context.depth--
		if context.bytes >= usageSnapshotMaxBytes {
			break
		}
	}
	if len(items) > len(output) {
		context.truncated = true
		output = append(output, "["+itoa(len(items)-len(output))+" items truncated]")
	}
	return output
}

func boundSnapshotString(value string, context *snapshotBoundContext) string {
	remaining := usageSnapshotMaxBytes - context.bytes
	if remaining < 0 {
		remaining = 0
	}
	limit := usageSnapshotMaxStringBytes
	if remaining < limit {
		limit = remaining
	}
	bytes := boundedStringByteLength(value, limit+1)
	if bytes <= limit {
		context.bytes += bytes
		return value
	}
	context.truncated = true
	suffix := "...[truncated " + itoa(bytes-limit) + " bytes]"
	prefixBytes := limit - len(suffix)
	if prefixBytes < 0 {
		prefixBytes = 0
	}
	truncated := sliceStringByUTF8Bytes(value, prefixBytes)
	context.bytes += boundedStringByteLength(truncated, prefixBytes) + len(suffix)
	return truncated + suffix
}

// EstimateUsageRecordBytes mirrors estimateUsageRecordBytes:
// estimateJsonLikeBytes(input, maxBytes=queueMax+1) + 256. The queue byte
// budget is supplied by the queue config (Node
// runtimeConfig.background.usageRecordQueueMaxMb).
func EstimateUsageRecordBytes(input UsageRecordInput, queueMaxBytes int) int {
	return EstimateJSONLikeBytes(input, EstimateJSONLikeBytesOptions{
		MaxBytes: queueMaxBytes + 1,
		MaxNodes: 0,
	}) + 256
}
