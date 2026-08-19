package accounthealth

import "time"

const (
	OutcomeSuccess        = "complete_success"
	OutcomeNeutral        = "framing_complete_neutral"
	OutcomeUpstreamFailed = "upstream_failure"
	OutcomeTaskFailed     = "probe_task_failure"
	OutcomeStale          = "stale"
)

type CredentialEnvelope struct {
	Kind       string `json:"kind"`
	Ciphertext string `json:"ciphertext"`
}

type APIKeyInput struct {
	Index       int                `json:"index"`
	Fingerprint string             `json:"fingerprint"`
	Credential  CredentialEnvelope `json:"credential"`
}

// Input is the immutable, signed snapshot consumed by jobs. It intentionally
// contains encrypted credential material only; the decrypted values never
// leave the direct-probe call.
type Input struct {
	AccountID            string              `json:"account_id"`
	InputVersion         int64               `json:"input_version"`
	ConfigRevision       int64               `json:"config_revision"`
	DispatchRevision     int64               `json:"dispatch_revision"`
	Provider             string              `json:"provider"`
	Type                 string              `json:"type"`
	EndpointMode         string              `json:"endpoint_mode"`
	HealthModel          string              `json:"health_model"`
	BaseURL              string              `json:"base_url"`
	KeySetFingerprint    string              `json:"key_set_fingerprint,omitempty"`
	APIKeys              []APIKeyInput       `json:"api_keys,omitempty"`
	OAuthAccess          *CredentialEnvelope `json:"oauth_access,omitempty"`
	OAuthExpiresAt       *time.Time          `json:"oauth_expires_at,omitempty"`
	OAuthAccountID       string              `json:"oauth_account_id,omitempty"`
	Proxy                *CredentialEnvelope `json:"proxy,omitempty"`
	IssuedAt             time.Time           `json:"issued_at"`
	ExpiresAt            time.Time           `json:"expires_at"`
	TLSPolicyVersion     string              `json:"tls_policy_version"`
	AllowInsecureBaseURL bool                `json:"allow_insecure_base_url"`
	Eligibility          Eligibility         `json:"eligibility"`
	Cooldown             *CooldownFence      `json:"cooldown_fence,omitempty"`
	Schedule             Schedule            `json:"schedule"`
}

// Eligibility is a Node business-state snapshot, not a task decision.  Go
// refuses to probe as soon as this immutable snapshot is expired or declares
// the account unavailable; it never opens the Node business SQLite file.
type Eligibility struct {
	AccountStatus                              string     `json:"account_status"`
	Schedulable                                bool       `json:"schedulable"`
	BoundGroup                                 bool       `json:"bound_group"`
	AuthorizationEligible                      bool       `json:"authorization_eligible"`
	SourceConfigRevision                       *int64     `json:"source_config_revision,omitempty"`
	CooldownUntil                              *time.Time `json:"cooldown_until,omitempty"`
	TemporaryUnavailableContinuousProbeEnabled *bool      `json:"temporary_unavailable_continuous_probe_enabled,omitempty"`
}

// Schedule is frozen with each input version.  Changing a policy requires a
// new input version, which makes pending work visibly stale instead of
// applying a new policy halfway through an old account configuration.
type Schedule struct {
	HealthIntervalMS         int64 `json:"health_interval_ms"`
	HealthJitterMS           int64 `json:"health_jitter_ms"`
	FailureThreshold         int   `json:"failure_threshold"`
	FailureRetryMS           int64 `json:"failure_retry_ms"`
	CooldownNeutralBaseMS    int64 `json:"cooldown_neutral_base_ms"`
	CooldownNeutralMaxMS     int64 `json:"cooldown_neutral_max_ms"`
	CooldownFailureBackoffMS int64 `json:"cooldown_failure_backoff_ms"`
	MaxPauseMinutes          int   `json:"max_pause_minutes,omitempty"`
	MaxRecoveryHours         int   `json:"max_recovery_hours,omitempty"`
}

type CooldownFence struct {
	ObservationStartedAt time.Time `json:"observation_started_at"`
	Generation           string    `json:"generation"`
	SourceConfigRevision *int64    `json:"source_config_revision,omitempty"`
}

type SourceFence struct {
	StateKey         string `json:"state_key"`
	AccountID        string `json:"account_id"`
	SourceGeneration int64  `json:"source_generation"`
	SourceFenceID    string `json:"source_fence_id"`
	RuntimeKey       string `json:"runtime_key"`
	ProbeGeneration  int64  `json:"probe_generation"`
	ConfigRevision   int64  `json:"config_revision"`
}

type ProbeRequest struct {
	RequestID        string    `json:"request_id"`
	AccountID        string    `json:"account_id"`
	Reason           string    `json:"reason"`
	InputVersion     int64     `json:"input_version"`
	ConfigRevision   int64     `json:"config_revision"`
	DispatchRevision int64     `json:"dispatch_revision"`
	Deadline         time.Time `json:"deadline"`
	// MutateAccount is true for activation/configuration work. Source-fenced
	// Gateway confirmation requests set it false; only a typed upstream failure
	// may later receive mutation authority under the frozen source rule.
	MutateAccount bool         `json:"mutate_account"`
	SourceFence   *SourceFence `json:"source_fence,omitempty"`
	sourcePath    string       `json:"-"`
}

type Projection struct {
	TargetAccountID       string         `json:"target_account_id"`
	TransitionKind        string         `json:"transition_kind"`
	InputVersion          int64          `json:"input_version"`
	ConfigRevision        int64          `json:"config_revision"`
	DispatchRevision      int64          `json:"dispatch_revision"`
	SourceRevision        *int64         `json:"source_config_revision,omitempty"`
	ExpectedAccountStatus string         `json:"expected_account_status"`
	ExpectedCooldownFence *CooldownFence `json:"expected_cooldown_fence,omitempty"`
	Values                map[string]any `json:"values,omitempty"`
	CooldownFence         *CooldownFence `json:"cooldown_fence,omitempty"`
}

type Outcome struct {
	OutcomeID        string         `json:"outcome_id"`
	RequestID        string         `json:"request_id"`
	AccountID        string         `json:"account_id"`
	Outcome          string         `json:"outcome"`
	ObservedAt       time.Time      `json:"observed_at"`
	InputVersion     int64          `json:"input_version"`
	ConfigRevision   int64          `json:"config_revision"`
	DispatchRevision int64          `json:"dispatch_revision"`
	StatusCode       int            `json:"status_code,omitempty"`
	ErrorCode        string         `json:"error_code,omitempty"`
	ErrorMessage     string         `json:"error_message,omitempty"`
	WinnerIndex      *int           `json:"winner_index,omitempty"`
	SourceFence      *SourceFence   `json:"source_fence,omitempty"`
	Projection       *Projection    `json:"projection,omitempty"`
	NextDueAt        *time.Time     `json:"next_due_at,omitempty"`
	FailureCount     int            `json:"failure_count,omitempty"`
	FailureStartedAt *time.Time     `json:"failure_started_at,omitempty"`
	AccountStatus    string         `json:"account_status,omitempty"`
	CooldownFence    *CooldownFence `json:"cooldown_fence,omitempty"`
}

type CurrentState struct {
	AccountID        string
	OutcomeID        string
	Outcome          string
	ObservedAt       time.Time
	InputVersion     int64
	ConfigRevision   int64
	DispatchRevision int64
	StatusCode       int
	ErrorCode        string
	ErrorMessage     string
	NextDueAt        *time.Time
	FailureCount     int
	FailureStartedAt *time.Time
	AccountStatus    string
	CooldownFence    *CooldownFence
}

type ProbeResult struct {
	Outcome      string
	StatusCode   int
	ErrorCode    string
	ErrorMessage string
}
