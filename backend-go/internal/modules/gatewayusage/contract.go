package gatewayusage

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type TrafficSource string

const (
	TrafficSourceGateway              TrafficSource = "gateway"
	TrafficSourceManualAccountTest    TrafficSource = "manual_account_test"
	TrafficSourceAccountHealthCheck   TrafficSource = "account_health_check"
	TrafficSourceRuntimeRecoveryProbe TrafficSource = "runtime_recovery_probe"
	TrafficSourceCooldownRetest       TrafficSource = "cooldown_retest"
	TrafficSourceHybridScoring        TrafficSource = "hybrid_scoring"
	TrafficSourceHybridQualityScoring TrafficSource = "hybrid_quality_scoring"
)

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

type FailureAttribution string

const (
	FailureAttributionAccountUpstream   FailureAttribution = "account_upstream"
	FailureAttributionAccountDependency FailureAttribution = "account_dependency"
	FailureAttributionGatewayCapacity   FailureAttribution = "gateway_capacity"
	FailureAttributionGatewayPolicy     FailureAttribution = "gateway_policy"
	FailureAttributionClientLifecycle   FailureAttribution = "client_lifecycle"
)

type AccountAccessType string

const (
	AccountAccessOwner             AccountAccessType = "owner"
	AccountAccessAccountAuthorized AccountAccessType = "account_authorized"
	AccountAccessGroupAuthorized   AccountAccessType = "group_authorized"
)

type GroupAccessType string

const (
	GroupAccessOwner      GroupAccessType = "owner"
	GroupAccessAuthorized GroupAccessType = "authorized"
)

type AuthorizationSource string

const (
	AuthorizationSourceManual AuthorizationSource = "manual"
	AuthorizationSourceTeam   AuthorizationSource = "team"
)

type AuthorizationRef struct {
	ID           string
	Source       AuthorizationSource
	SourceTeamID string
}

type AccountScope struct {
	ID                   string
	OwnerSystemAccountID string
	AccessType           AccountAccessType
	Authorization        *AuthorizationRef
}

type GroupScope struct {
	ID                   string
	OwnerSystemAccountID string
	AccessType           GroupAccessType
	Authorization        *AuthorizationRef
}

// UsageFacts contains provider-independent measurements observed by protocol adapters.
// A nil numeric pointer means the provider did not report that measurement; zero is a
// real observation and is deliberately preserved.
type UsageFacts struct {
	RequestedServiceTier     string
	EffectiveServiceTier     string
	ReportedServiceTier      string
	BilledServiceTier        string
	RequestedReasoningEffort string
	EffectiveReasoningEffort string
	InputTokens              *int64
	OutputTokens             *int64
	CacheReadTokens          *int64
	CacheWriteTokens         *int64
	CacheWrite1hTokens       *int64
	ThinkingTokens           *int64
	InputImageTokens         *int64
	OutputImageTokens        *int64
	InputAudioTokens         *int64
	OutputAudioTokens        *int64
	OutputImageCount         *int64
	CacheReadCostUSD         *float64
	CacheWriteCostUSD        *float64
	CostUSD                  *float64
}

// Merge applies newly observed facts without erasing values absent from next.
func (u UsageFacts) Merge(next UsageFacts) UsageFacts {
	mergeString(&u.RequestedServiceTier, next.RequestedServiceTier)
	mergeString(&u.EffectiveServiceTier, next.EffectiveServiceTier)
	mergeString(&u.ReportedServiceTier, next.ReportedServiceTier)
	mergeString(&u.BilledServiceTier, next.BilledServiceTier)
	mergeString(&u.RequestedReasoningEffort, next.RequestedReasoningEffort)
	mergeString(&u.EffectiveReasoningEffort, next.EffectiveReasoningEffort)
	mergeInt64(&u.InputTokens, next.InputTokens)
	mergeInt64(&u.OutputTokens, next.OutputTokens)
	mergeInt64(&u.CacheReadTokens, next.CacheReadTokens)
	mergeInt64(&u.CacheWriteTokens, next.CacheWriteTokens)
	mergeInt64(&u.CacheWrite1hTokens, next.CacheWrite1hTokens)
	mergeInt64(&u.ThinkingTokens, next.ThinkingTokens)
	mergeInt64(&u.InputImageTokens, next.InputImageTokens)
	mergeInt64(&u.OutputImageTokens, next.OutputImageTokens)
	mergeInt64(&u.InputAudioTokens, next.InputAudioTokens)
	mergeInt64(&u.OutputAudioTokens, next.OutputAudioTokens)
	mergeInt64(&u.OutputImageCount, next.OutputImageCount)
	mergeFloat64(&u.CacheReadCostUSD, next.CacheReadCostUSD)
	mergeFloat64(&u.CacheWriteCostUSD, next.CacheWriteCostUSD)
	mergeFloat64(&u.CostUSD, next.CostUSD)
	return u
}

type RequestFacts struct {
	TraceID                   string
	TrafficSource             TrafficSource
	ClientIP                  string
	SystemAccountID           string
	APIKeyID                  string
	Account                   *AccountScope
	Group                     *GroupScope
	Endpoint                  string
	ProviderCode              string
	ProviderProtocolProfileID string
	UsageSemantic             string
	Model                     string
	UpstreamModel             string
	PricingModel              string
	ModelMappingApplied       bool
	ModelMappingSource        string
	SourceEndpointFamily      string
	UpstreamEndpointFamily    string
	Stream                    bool
	StartedAt                 time.Time
	Usage                     UsageFacts
	RequestSnapshot           any
}

type TerminalFacts struct {
	Outcome            Outcome
	CompletedAt        time.Time
	StatusCode         *int
	FirstToken         *time.Duration
	Usage              UsageFacts
	FailureAttribution FailureAttribution
	ErrorCode          string
	ErrorMessage       string
	ResponseSnapshot   any
}

// FinalRecord is a detached, protocol-independent hand-off contract. Queue and
// persistence adapters consume this value later; finalization itself has no side effects.
type FinalRecord struct {
	TraceID                   string
	TrafficSource             TrafficSource
	ClientIP                  string
	SystemAccountID           string
	APIKeyID                  string
	Account                   *AccountScope
	Group                     *GroupScope
	Endpoint                  string
	ProviderCode              string
	ProviderProtocolProfileID string
	UsageSemantic             string
	Model                     string
	UpstreamModel             string
	PricingModel              string
	ModelMappingApplied       bool
	ModelMappingSource        string
	SourceEndpointFamily      string
	UpstreamEndpointFamily    string
	Stream                    bool
	StartedAt                 time.Time
	CompletedAt               time.Time
	Duration                  time.Duration
	StatusCode                *int
	Outcome                   Outcome
	FirstToken                *time.Duration
	Usage                     UsageFacts
	FailureAttribution        FailureAttribution
	ErrorCode                 string
	ErrorMessage              string
	RequestSnapshot           Snapshot
	ResponseSnapshot          Snapshot
}

func Finalize(request RequestFacts, terminal TerminalFacts) (FinalRecord, error) {
	request = normalizeRequestFacts(request)
	terminal = defaultTerminalFacts(request, terminal)
	if err := validateRequestFacts(request); err != nil {
		return FinalRecord{}, err
	}
	if err := validateTerminalFacts(request, terminal); err != nil {
		return FinalRecord{}, err
	}

	usage := resolveUsageFacts(request.Usage.Merge(terminal.Usage))
	record := FinalRecord{
		TraceID: request.TraceID, TrafficSource: request.TrafficSource,
		ClientIP: request.ClientIP, SystemAccountID: request.SystemAccountID,
		APIKeyID: request.APIKeyID, Account: cloneAccountScope(request.Account),
		Group: cloneGroupScope(request.Group), Endpoint: request.Endpoint,
		ProviderCode: request.ProviderCode, ProviderProtocolProfileID: request.ProviderProtocolProfileID,
		UsageSemantic: request.UsageSemantic, Model: request.Model,
		UpstreamModel: request.UpstreamModel, PricingModel: request.PricingModel,
		ModelMappingApplied: request.ModelMappingApplied, ModelMappingSource: request.ModelMappingSource,
		SourceEndpointFamily: request.SourceEndpointFamily, UpstreamEndpointFamily: request.UpstreamEndpointFamily,
		Stream: request.Stream, StartedAt: request.StartedAt, CompletedAt: terminal.CompletedAt,
		Duration: terminal.CompletedAt.Sub(request.StartedAt), StatusCode: cloneInt(terminal.StatusCode),
		Outcome: terminal.Outcome, FirstToken: cloneDuration(terminal.FirstToken), Usage: cloneUsageFacts(usage),
		FailureAttribution: terminal.FailureAttribution, ErrorCode: terminal.ErrorCode,
		ErrorMessage: terminal.ErrorMessage,
	}
	if capturesUsageSnapshots(request.TrafficSource) {
		record.RequestSnapshot = SanitizeSnapshot(request.RequestSnapshot)
		record.ResponseSnapshot = SanitizeSnapshot(terminal.ResponseSnapshot)
	}
	return record, nil
}

func normalizeRequestFacts(value RequestFacts) RequestFacts {
	value.TraceID = strings.TrimSpace(value.TraceID)
	value.ClientIP = strings.TrimSpace(value.ClientIP)
	value.SystemAccountID = strings.TrimSpace(value.SystemAccountID)
	value.APIKeyID = strings.TrimSpace(value.APIKeyID)
	value.Endpoint = strings.TrimSpace(value.Endpoint)
	value.ProviderCode = strings.TrimSpace(value.ProviderCode)
	value.ProviderProtocolProfileID = strings.TrimSpace(value.ProviderProtocolProfileID)
	value.UsageSemantic = strings.TrimSpace(value.UsageSemantic)
	value.Model = strings.TrimSpace(value.Model)
	value.UpstreamModel = strings.TrimSpace(value.UpstreamModel)
	value.PricingModel = strings.TrimSpace(value.PricingModel)
	value.ModelMappingSource = strings.TrimSpace(value.ModelMappingSource)
	value.SourceEndpointFamily = strings.TrimSpace(value.SourceEndpointFamily)
	value.UpstreamEndpointFamily = strings.TrimSpace(value.UpstreamEndpointFamily)
	value.Group = normalizeGroupScope(value.Group)
	if validateGroupScope(value.Group) != nil {
		value.Group = nil
	}
	value.Account = normalizeAccountScope(value.Account)
	if validateAccountScope(value.Account, value.Group) != nil {
		value.Account = nil
	}
	return value
}

func defaultTerminalFacts(request RequestFacts, value TerminalFacts) TerminalFacts {
	if value.Outcome == OutcomeFailed && value.FailureAttribution == "" {
		value.FailureAttribution = FailureAttributionGatewayPolicy
		if request.Account != nil {
			value.FailureAttribution = FailureAttributionAccountUpstream
		}
	}
	return value
}

func resolveUsageFacts(value UsageFacts) UsageFacts {
	if value.RequestedServiceTier == "" {
		value.RequestedServiceTier = "default"
	}
	if value.EffectiveServiceTier == "" {
		value.EffectiveServiceTier = value.RequestedServiceTier
	}
	if value.BilledServiceTier == "" {
		value.BilledServiceTier = value.ReportedServiceTier
		if value.BilledServiceTier == "" {
			value.BilledServiceTier = value.EffectiveServiceTier
		}
	}
	return value
}

func normalizeAccountScope(value *AccountScope) *AccountScope {
	if value == nil {
		return nil
	}
	result := *value
	result.ID = strings.TrimSpace(result.ID)
	result.OwnerSystemAccountID = strings.TrimSpace(result.OwnerSystemAccountID)
	result.Authorization = normalizeAuthorizationRef(result.Authorization)
	if result.AccessType != AccountAccessAccountAuthorized {
		result.Authorization = nil
	}
	return &result
}

func normalizeGroupScope(value *GroupScope) *GroupScope {
	if value == nil {
		return nil
	}
	result := *value
	result.ID = strings.TrimSpace(result.ID)
	result.OwnerSystemAccountID = strings.TrimSpace(result.OwnerSystemAccountID)
	result.Authorization = normalizeAuthorizationRef(result.Authorization)
	if result.AccessType != GroupAccessAuthorized {
		result.Authorization = nil
	}
	return &result
}

func normalizeAuthorizationRef(value *AuthorizationRef) *AuthorizationRef {
	if value == nil {
		return nil
	}
	result := *value
	result.ID = strings.TrimSpace(result.ID)
	result.SourceTeamID = strings.TrimSpace(result.SourceTeamID)
	return &result
}

func validateRequestFacts(value RequestFacts) error {
	if value.TraceID == "" {
		return fmt.Errorf("gateway usage trace ID is required")
	}
	if !value.TrafficSource.valid() {
		return fmt.Errorf("invalid gateway usage traffic source %q", value.TrafficSource)
	}
	if value.SystemAccountID == "" {
		return fmt.Errorf("gateway usage system account ID is required")
	}
	if value.Endpoint == "" {
		return fmt.Errorf("gateway usage endpoint is required")
	}
	if value.StartedAt.IsZero() {
		return fmt.Errorf("gateway usage start time is required")
	}
	return nil
}

func validateTerminalFacts(request RequestFacts, value TerminalFacts) error {
	if value.Outcome != OutcomeSucceeded && value.Outcome != OutcomeFailed {
		return fmt.Errorf("invalid terminal outcome %q", value.Outcome)
	}
	if value.CompletedAt.IsZero() {
		return fmt.Errorf("gateway usage completion time is required")
	}
	if value.CompletedAt.Before(request.StartedAt) {
		return fmt.Errorf("gateway usage completion time precedes start time")
	}
	if value.StatusCode != nil && (*value.StatusCode < 100 || *value.StatusCode > 599) {
		return fmt.Errorf("gateway usage status code must be between 100 and 599")
	}
	if value.FirstToken != nil && *value.FirstToken < 0 {
		return fmt.Errorf("gateway usage first-token duration must be non-negative")
	}
	if value.FirstToken != nil && *value.FirstToken > value.CompletedAt.Sub(request.StartedAt) {
		return fmt.Errorf("gateway usage first-token duration exceeds total duration")
	}
	if value.Outcome == OutcomeSucceeded {
		if value.FailureAttribution != "" {
			return fmt.Errorf("successful terminal facts cannot contain failure attribution")
		}
	} else if !value.FailureAttribution.valid() {
		return fmt.Errorf("failed terminal facts require failure attribution")
	}
	return validateUsageFacts(request.Usage.Merge(value.Usage))
}

func validateUsageFacts(value UsageFacts) error {
	capabilities := []struct {
		name  string
		value string
	}{
		{"requested service tier", value.RequestedServiceTier},
		{"effective service tier", value.EffectiveServiceTier},
		{"reported service tier", value.ReportedServiceTier},
		{"billed service tier", value.BilledServiceTier},
		{"requested reasoning effort", value.RequestedReasoningEffort},
		{"effective reasoning effort", value.EffectiveReasoningEffort},
	}
	for _, capability := range capabilities {
		if capability.value != "" && !capabilityTokenPattern.MatchString(capability.value) {
			return fmt.Errorf("gateway usage %s is invalid", capability.name)
		}
	}
	measurements := []struct {
		name  string
		value *int64
	}{
		{"input tokens", value.InputTokens}, {"output tokens", value.OutputTokens},
		{"cache read tokens", value.CacheReadTokens}, {"cache write tokens", value.CacheWriteTokens},
		{"one-hour cache write tokens", value.CacheWrite1hTokens}, {"thinking tokens", value.ThinkingTokens},
		{"input image tokens", value.InputImageTokens}, {"output image tokens", value.OutputImageTokens},
		{"input audio tokens", value.InputAudioTokens}, {"output audio tokens", value.OutputAudioTokens},
		{"output image count", value.OutputImageCount},
	}
	for _, measurement := range measurements {
		if measurement.value != nil && *measurement.value < 0 {
			return fmt.Errorf("gateway usage %s must be non-negative", measurement.name)
		}
	}
	costs := []struct {
		name  string
		value *float64
	}{
		{"cache read cost USD", value.CacheReadCostUSD},
		{"cache write cost USD", value.CacheWriteCostUSD},
		{"cost USD", value.CostUSD},
	}
	for _, cost := range costs {
		if cost.value != nil && (*cost.value < 0 || math.IsNaN(*cost.value) || math.IsInf(*cost.value, 0)) {
			return fmt.Errorf("gateway usage %s must be finite and non-negative", cost.name)
		}
	}
	return nil
}

func validateAccountScope(value *AccountScope, group *GroupScope) error {
	if value == nil {
		return nil
	}
	if value.ID == "" || value.OwnerSystemAccountID == "" {
		return fmt.Errorf("gateway usage account scope requires ID and owner")
	}
	switch value.AccessType {
	case AccountAccessOwner:
		if value.Authorization != nil {
			return fmt.Errorf("owner account scope cannot contain authorization")
		}
	case AccountAccessAccountAuthorized:
		if err := validateAuthorizationRef(value.Authorization); err != nil {
			return fmt.Errorf("account-authorized account scope: %w", err)
		}
	case AccountAccessGroupAuthorized:
		if value.Authorization != nil {
			return fmt.Errorf("group-authorized account scope cannot contain account authorization")
		}
		if group == nil || group.AccessType != GroupAccessAuthorized || group.Authorization == nil {
			return fmt.Errorf("group-authorized account scope requires an authorized group scope")
		}
	default:
		return fmt.Errorf("invalid gateway usage account access type %q", value.AccessType)
	}
	return nil
}

func validateGroupScope(value *GroupScope) error {
	if value == nil {
		return nil
	}
	if value.ID == "" || value.OwnerSystemAccountID == "" {
		return fmt.Errorf("gateway usage group scope requires ID and owner")
	}
	switch value.AccessType {
	case GroupAccessOwner:
		if value.Authorization != nil {
			return fmt.Errorf("owner group scope cannot contain authorization")
		}
	case GroupAccessAuthorized:
		if err := validateAuthorizationRef(value.Authorization); err != nil {
			return fmt.Errorf("authorized group scope: %w", err)
		}
	default:
		return fmt.Errorf("invalid gateway usage group access type %q", value.AccessType)
	}
	return nil
}

func validateAuthorizationRef(value *AuthorizationRef) error {
	if value == nil || value.ID == "" {
		return fmt.Errorf("authorization ID is required")
	}
	switch value.Source {
	case "", AuthorizationSourceManual:
		if value.SourceTeamID != "" {
			return fmt.Errorf("manual authorization cannot contain source team ID")
		}
	case AuthorizationSourceTeam:
		if value.SourceTeamID == "" {
			return fmt.Errorf("team authorization requires source team ID")
		}
	default:
		return fmt.Errorf("invalid authorization source %q", value.Source)
	}
	return nil
}

func capturesUsageSnapshots(value TrafficSource) bool {
	return value != TrafficSourceAccountHealthCheck &&
		value != TrafficSourceRuntimeRecoveryProbe &&
		value != TrafficSourceCooldownRetest
}

func (value TrafficSource) valid() bool {
	switch value {
	case TrafficSourceGateway, TrafficSourceManualAccountTest, TrafficSourceAccountHealthCheck,
		TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest,
		TrafficSourceHybridScoring, TrafficSourceHybridQualityScoring:
		return true
	default:
		return false
	}
}

func (value FailureAttribution) valid() bool {
	switch value {
	case FailureAttributionAccountUpstream, FailureAttributionAccountDependency,
		FailureAttributionGatewayCapacity, FailureAttributionGatewayPolicy,
		FailureAttributionClientLifecycle:
		return true
	default:
		return false
	}
}

func cloneAccountScope(value *AccountScope) *AccountScope {
	if value == nil {
		return nil
	}
	result := *value
	result.Authorization = cloneAuthorizationRef(value.Authorization)
	return &result
}

func cloneGroupScope(value *GroupScope) *GroupScope {
	if value == nil {
		return nil
	}
	result := *value
	result.Authorization = cloneAuthorizationRef(value.Authorization)
	return &result
}

func cloneAuthorizationRef(value *AuthorizationRef) *AuthorizationRef {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneUsageFacts(value UsageFacts) UsageFacts {
	value.InputTokens = cloneInt64(value.InputTokens)
	value.OutputTokens = cloneInt64(value.OutputTokens)
	value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
	value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
	value.CacheWrite1hTokens = cloneInt64(value.CacheWrite1hTokens)
	value.ThinkingTokens = cloneInt64(value.ThinkingTokens)
	value.InputImageTokens = cloneInt64(value.InputImageTokens)
	value.OutputImageTokens = cloneInt64(value.OutputImageTokens)
	value.InputAudioTokens = cloneInt64(value.InputAudioTokens)
	value.OutputAudioTokens = cloneInt64(value.OutputAudioTokens)
	value.OutputImageCount = cloneInt64(value.OutputImageCount)
	value.CacheReadCostUSD = cloneFloat64(value.CacheReadCostUSD)
	value.CacheWriteCostUSD = cloneFloat64(value.CacheWriteCostUSD)
	value.CostUSD = cloneFloat64(value.CostUSD)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func mergeString(current *string, next string) {
	if next != "" {
		*current = next
	}
}

func mergeInt64(current **int64, next *int64) {
	if next != nil {
		value := *next
		*current = &value
	}
}

func mergeFloat64(current **float64, next *float64) {
	if next != nil {
		value := *next
		*current = &value
	}
}

var capabilityTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
