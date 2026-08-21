package proxylatency

import (
	"errors"
	"time"
)

const (
	OutcomeSuccess          = "complete_success"
	OutcomeNeutral          = "framing_complete_neutral"
	OutcomeUpstreamFailure  = "upstream_failure"
	OutcomeProbeTaskFailure = "probe_task_failure"
)

type ItemStatus string

const (
	ItemPassed  ItemStatus = "passed"
	ItemWarning ItemStatus = "warning"
	ItemFailed  ItemStatus = "failed"
	ItemUnknown ItemStatus = "unknown"
)

type OverallStatus string

const (
	OverallPassed  OverallStatus = "passed"
	OverallWarning OverallStatus = "warning"
	OverallFailed  OverallStatus = "failed"
	OverallUnknown OverallStatus = "unknown"
)

type Trigger string

const (
	TriggerPeriodic Trigger = "periodic"
	TriggerManual   Trigger = "manual"
)

// CredentialEnvelope keeps Node-owned secret material opaque at the jobs
// input boundary. Its ciphertext must never be rendered in diagnostics.
type CredentialEnvelope struct {
	Kind       string `json:"kind"`
	Ciphertext string `json:"ciphertext"`
}

// Target is a frozen enabled-provider endpoint selected with a proxy
// candidate. URLs are configuration values, never response data.
type Target struct {
	Provider  string `json:"provider"`
	ProfileID string `json:"profile_id"`
	URL       string `json:"url"`
}

// InputDraft is a read-only business snapshot. It intentionally has no JSON
// representation: only the jobs Store may issue a serializable request with
// a durable request ID and input version.
type InputDraft struct {
	ProxyID        string
	ConfigRevision string
	Trigger        Trigger
	IssuedAt       time.Time
	ExpiresAt      time.Time
	PolicyVersion  string
	ProxyType      string
	ProxyHost      string
	ProxyPort      int
	ProxyUsername  string
	ProxyPassword  *CredentialEnvelope
	Targets        []Target
}

func (InputDraft) MarshalJSON() ([]byte, error) {
	return nil, errors.New("J3a input draft 必须先由 jobs Store 签发")
}

// IssuedInput is the immutable request identity the executor may consume.
// The encrypted envelope is never copied into outcomes or diagnostics.
type IssuedInput struct {
	RequestID      string              `json:"request_id"`
	ProxyID        string              `json:"proxy_id"`
	InputVersion   int64               `json:"input_version"`
	ConfigRevision string              `json:"config_revision"`
	Trigger        Trigger             `json:"trigger"`
	IssuedAt       time.Time           `json:"issued_at"`
	ExpiresAt      time.Time           `json:"expires_at"`
	PolicyVersion  string              `json:"policy_version"`
	ProxyType      string              `json:"proxy_type"`
	ProxyHost      string              `json:"proxy_host"`
	ProxyPort      int                 `json:"proxy_port"`
	ProxyUsername  string              `json:"proxy_username,omitempty"`
	ProxyPassword  *CredentialEnvelope `json:"proxy_password,omitempty"`
	Targets        []Target            `json:"targets"`
}

// ProbeRequest has one explicit proxy URL. An empty proxy is rejected so a
// failed proxy configuration can never turn into a direct upstream request.
type ProbeRequest struct {
	TargetURL string
	ProxyURL  string
	Timeout   time.Duration
}

// ItemResult deliberately retains only diagnostic metadata. It never carries
// response headers, bodies, target URLs, proxy URLs, or proxy credentials.
type ItemResult struct {
	Provider   string     `json:"provider"`
	ProfileID  string     `json:"profile_id"`
	Status     ItemStatus `json:"status"`
	Outcome    string     `json:"outcome"`
	HTTPStatus int        `json:"http_status,omitempty"`
	LatencyMS  int64      `json:"latency_ms,omitempty"`
	ErrorCode  string     `json:"error_code,omitempty"`
}

// Outcome is the immutable jobs-store record. The input identity is kept as
// version and revision only; secrets and connection settings are not accepted.
type Outcome struct {
	OutcomeID       string        `json:"outcome_id"`
	RequestID       string        `json:"request_id"`
	ProxyID         string        `json:"proxy_id"`
	ObservedAt      time.Time     `json:"observed_at"`
	InputVersion    int64         `json:"input_version"`
	ConfigRevision  string        `json:"config_revision"`
	Trigger         Trigger       `json:"trigger"`
	OwnerFenceToken int64         `json:"owner_fence_token"`
	ProxyFenceToken int64         `json:"proxy_fence_token"`
	OverallStatus   OverallStatus `json:"overall_status"`
	Items           []ItemResult  `json:"items"`
	// executionClaimToken is an internal jobs-store fence. It is never
	// serialized into the outcome payload.
	executionClaimToken string
}

func SummarizeItems(items []ItemResult) OverallStatus {
	passed := false
	unknown := false
	warning := false
	for _, item := range items {
		switch item.Status {
		case ItemFailed:
			return OverallFailed
		case ItemWarning:
			warning = true
		case ItemPassed:
			passed = true
		case ItemUnknown:
			unknown = true
		}
	}
	if warning || (passed && unknown) {
		return OverallWarning
	}
	if passed {
		return OverallPassed
	}
	return OverallUnknown
}
