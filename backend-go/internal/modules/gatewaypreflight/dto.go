package gatewaypreflight

import (
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type DecisionCode string

const (
	DecisionReady                    DecisionCode = "ready"
	DecisionInvalidAPIKeyFormat      DecisionCode = "invalid_api_key_format"
	DecisionAPIKeyNotFound           DecisionCode = "api_key_not_found"
	DecisionAPIKeyDisabled           DecisionCode = "api_key_disabled"
	DecisionAPIKeyExpired            DecisionCode = "api_key_expired"
	DecisionSystemAccountDisabled    DecisionCode = "system_account_disabled"
	DecisionRouteStrategyDisabled    DecisionCode = "route_strategy_disabled"
	DecisionNoActiveBindings         DecisionCode = "no_active_bindings"
	DecisionQuotaSnapshotMissing     DecisionCode = "quota_snapshot_missing"
	DecisionQuotaSnapshotIncomplete  DecisionCode = "quota_snapshot_incomplete"
	DecisionQuotaSnapshotUnavailable DecisionCode = "quota_snapshot_unavailable"
	DecisionQuotaExceeded            DecisionCode = "quota_exceeded"
)

type Decision struct {
	code    DecisionCode
	message string
}

func newDecision(code DecisionCode) Decision {
	message := ""
	if code == DecisionQuotaExceeded {
		message = "额度已用完，请联系管理员提升额度"
	}
	return Decision{code: code, message: message}
}

func (d Decision) Code() DecisionCode { return d.code }
func (d Decision) Allowed() bool      { return d.code == DecisionReady }
func (d Decision) Message() string    { return d.message }

type Result struct {
	decision Decision
	apiKey   *APIKey
	settings *Settings
	bindings []Binding
}

func (r Result) Decision() Decision { return r.decision }

func (r Result) APIKey() (APIKey, bool) {
	if r.apiKey == nil {
		return APIKey{}, false
	}
	return *r.apiKey, true
}

func (r Result) Settings() (Settings, bool) {
	if r.settings == nil {
		return Settings{}, false
	}
	return *r.settings, true
}

func (r Result) Bindings() []Binding {
	return append([]Binding(nil), r.bindings...)
}

type APIKey struct {
	id                      string
	systemAccountID         string
	status                  string
	expiresAt               *time.Time
	quotaLimits             port.ManagementRequestQuotaLimits
	imageGenerationEnabled  bool
	routeStrategyID         string
	routeStrategyStatus     string
	routeStrategyMode       string
	routeStrategyConfigJSON string
}

func (k APIKey) ID() string              { return k.id }
func (k APIKey) SystemAccountID() string { return k.systemAccountID }
func (k APIKey) Status() string          { return k.status }
func (k APIKey) ExpiresAt() *time.Time   { return cloneTimePtr(k.expiresAt) }
func (k APIKey) QuotaLimits() port.ManagementRequestQuotaLimits {
	return cloneQuotaLimits(k.quotaLimits)
}
func (k APIKey) ImageGenerationEnabled() bool    { return k.imageGenerationEnabled }
func (k APIKey) RouteStrategyID() string         { return k.routeStrategyID }
func (k APIKey) RouteStrategyStatus() string     { return k.routeStrategyStatus }
func (k APIKey) RouteStrategyMode() string       { return k.routeStrategyMode }
func (k APIKey) RouteStrategyConfigJSON() string { return k.routeStrategyConfigJSON }

type Binding struct {
	id              string
	apiKeyID        string
	systemAccountID string
	groupID         string
	priority        int
	weight          int
	status          string
	providerCode    string
	groupEnabled    bool
	createdAt       time.Time
}

func newBinding(row port.GatewayPreflightBindingRecord) Binding {
	return Binding{id: row.ID, apiKeyID: row.APIKeyID, systemAccountID: row.SystemAccountID, groupID: row.GroupID, priority: row.Priority, weight: row.Weight, status: row.Status, providerCode: row.ProviderCode, groupEnabled: row.GroupEnabled, createdAt: row.CreatedAt}
}

func (b Binding) ID() string              { return b.id }
func (b Binding) APIKeyID() string        { return b.apiKeyID }
func (b Binding) SystemAccountID() string { return b.systemAccountID }
func (b Binding) GroupID() string         { return b.groupID }
func (b Binding) Priority() int           { return b.priority }
func (b Binding) Weight() int             { return b.weight }
func (b Binding) Status() string          { return b.status }
func (b Binding) ProviderCode() string    { return b.providerCode }
func (b Binding) GroupEnabled() bool      { return b.groupEnabled }
func (b Binding) CreatedAt() time.Time    { return b.createdAt }

type Settings struct {
	gatewayTextRawBodyLimitMegabytes           int
	defaultTemporaryUnschedulableMinutes       int
	temporaryUnschedulableRetryIntervalSeconds int
	temporaryUnschedulableRetryAttempts        int
	streamCircuitBreakerEnabled                bool
	textFirstResponseTimeoutSeconds            int
	textStreamIdleTimeoutSeconds               int
	textUncommittedAttemptMaxLifetimeSeconds   int
	imageFirstResponseTimeoutSeconds           int
	imageStreamIdleTimeoutSeconds              int
	imageUncommittedAttemptMaxLifetimeSeconds  int
	noAvailableAccountWaitTimeoutSeconds       int
	streamFailureThresholdCount                int
	streamFailureThresholdWindowMinutes        int
}

func (s Settings) GatewayTextRawBodyLimitMegabytes() int { return s.gatewayTextRawBodyLimitMegabytes }
func (s Settings) DefaultTemporaryUnschedulableMinutes() int {
	return s.defaultTemporaryUnschedulableMinutes
}
func (s Settings) TemporaryUnschedulableRetryIntervalSeconds() int {
	return s.temporaryUnschedulableRetryIntervalSeconds
}
func (s Settings) TemporaryUnschedulableRetryAttempts() int {
	return s.temporaryUnschedulableRetryAttempts
}
func (s Settings) StreamCircuitBreakerEnabled() bool    { return s.streamCircuitBreakerEnabled }
func (s Settings) TextFirstResponseTimeoutSeconds() int { return s.textFirstResponseTimeoutSeconds }
func (s Settings) TextStreamIdleTimeoutSeconds() int    { return s.textStreamIdleTimeoutSeconds }
func (s Settings) TextUncommittedAttemptMaxLifetimeSeconds() int {
	return s.textUncommittedAttemptMaxLifetimeSeconds
}
func (s Settings) ImageFirstResponseTimeoutSeconds() int { return s.imageFirstResponseTimeoutSeconds }
func (s Settings) ImageStreamIdleTimeoutSeconds() int    { return s.imageStreamIdleTimeoutSeconds }
func (s Settings) ImageUncommittedAttemptMaxLifetimeSeconds() int {
	return s.imageUncommittedAttemptMaxLifetimeSeconds
}
func (s Settings) NoAvailableAccountWaitTimeoutSeconds() int {
	return s.noAvailableAccountWaitTimeoutSeconds
}
func (s Settings) StreamFailureThresholdCount() int { return s.streamFailureThresholdCount }
func (s Settings) StreamFailureThresholdWindowMinutes() int {
	return s.streamFailureThresholdWindowMinutes
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneQuotaLimits(value port.ManagementRequestQuotaLimits) port.ManagementRequestQuotaLimits {
	result := port.ManagementRequestQuotaLimits{}
	if value.Hourly != nil {
		item := *value.Hourly
		result.Hourly = &item
	}
	if value.Daily != nil {
		item := *value.Daily
		result.Daily = &item
	}
	if value.Weekly != nil {
		item := *value.Weekly
		result.Weekly = &item
	}
	if value.Monthly != nil {
		item := *value.Monthly
		result.Monthly = &item
	}
	if value.Total != nil {
		item := *value.Total
		result.Total = &item
	}
	return result
}

func apiKeyFromRecord(row port.GatewayPreflightAPIKeyRecord) APIKey {
	config := ""
	if row.RouteStrategyConfigJSON != nil {
		config = *row.RouteStrategyConfigJSON
	}
	return APIKey{id: row.ID, systemAccountID: row.SystemAccountID, status: row.APIKeyStatus, expiresAt: cloneTimePtr(row.ExpiresAt), quotaLimits: cloneQuotaLimits(row.QuotaLimits), imageGenerationEnabled: row.SystemAccountImageGenerationEnabled, routeStrategyID: row.RouteStrategyID, routeStrategyStatus: row.RouteStrategyStatus, routeStrategyMode: row.RouteStrategyMode, routeStrategyConfigJSON: config}
}

func settingsFromRecord(row port.GatewayPreflightSettingsRecord) Settings {
	return Settings{gatewayTextRawBodyLimitMegabytes: row.GatewayTextRawBodyLimitMegabytes, defaultTemporaryUnschedulableMinutes: row.DefaultTemporaryUnschedulableMinutes, temporaryUnschedulableRetryIntervalSeconds: row.TemporaryUnschedulableRetryIntervalSeconds, temporaryUnschedulableRetryAttempts: row.TemporaryUnschedulableRetryAttempts, streamCircuitBreakerEnabled: true, textFirstResponseTimeoutSeconds: row.TextFirstResponseTimeoutSeconds, textStreamIdleTimeoutSeconds: row.TextStreamIdleTimeoutSeconds, textUncommittedAttemptMaxLifetimeSeconds: row.TextUncommittedAttemptMaxLifetimeSeconds, imageFirstResponseTimeoutSeconds: row.ImageFirstResponseTimeoutSeconds, imageStreamIdleTimeoutSeconds: row.ImageStreamIdleTimeoutSeconds, imageUncommittedAttemptMaxLifetimeSeconds: row.ImageUncommittedAttemptMaxLifetimeSeconds, noAvailableAccountWaitTimeoutSeconds: row.NoAvailableAccountWaitTimeoutSeconds, streamFailureThresholdCount: row.StreamFailureThresholdCount, streamFailureThresholdWindowMinutes: row.StreamFailureThresholdWindowMinutes}
}
