// types.go 是 J1 输出 schema 的留守子集：精确 Key+模型诊断探针执行器闭包
// （Input/APIKeyInput/CredentialEnvelope/ProbeResult、Outcome 四常量、
// Schedule/Eligibility/CooldownFence）已下沉 shared/platform/accounttest/
// exactkeyprobe（成对关系见该包 probe.go 注释），本包经 import 引用；此处
// 仅保留 jobs 拥有的 J1 机制 schema。
package accounthealth

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

const (
	OutcomeStale = "stale"
)

type SourceFence struct {
	StateKey         string `json:"state_key"`
	AccountID        string `json:"account_id"`
	SourceGeneration int64  `json:"source_generation"`
	SourceFenceID    string `json:"source_fence_id"`
	RuntimeKey       string `json:"runtime_key"`
	ProbeGeneration  int64  `json:"probe_generation"`
	ConfigRevision   int64  `json:"config_revision"`
}

type KeyModelFence struct {
	CapabilityHash   string `json:"capability_hash"`
	KeyFingerprint   string `json:"key_fingerprint"`
	DispatchRevision int64  `json:"dispatch_revision"`
	OwnerID          string `json:"owner_id"`
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
	MutateAccount bool           `json:"mutate_account"`
	SourceFence   *SourceFence   `json:"source_fence,omitempty"`
	KeyModelFence *KeyModelFence `json:"key_model_fence,omitempty"`
	sourcePath    string         `json:"-"`
}

type Projection struct {
	TargetAccountID       string                       `json:"target_account_id"`
	TransitionKind        string                       `json:"transition_kind"`
	InputVersion          int64                        `json:"input_version"`
	ConfigRevision        int64                        `json:"config_revision"`
	DispatchRevision      int64                        `json:"dispatch_revision"`
	SourceRevision        *int64                       `json:"source_config_revision,omitempty"`
	ExpectedAccountStatus string                       `json:"expected_account_status"`
	ExpectedCooldownFence *exactkeyprobe.CooldownFence `json:"expected_cooldown_fence,omitempty"`
	Values                map[string]any               `json:"values,omitempty"`
	CooldownFence         *exactkeyprobe.CooldownFence `json:"cooldown_fence,omitempty"`
}

type Outcome struct {
	OutcomeID            string                       `json:"outcome_id"`
	RequestID            string                       `json:"request_id"`
	AccountID            string                       `json:"account_id"`
	Outcome              string                       `json:"outcome"`
	ObservedAt           time.Time                    `json:"observed_at"`
	InputVersion         int64                        `json:"input_version"`
	ConfigRevision       int64                        `json:"config_revision"`
	DispatchRevision     int64                        `json:"dispatch_revision"`
	StatusCode           int                          `json:"status_code,omitempty"`
	ErrorCode            string                       `json:"error_code,omitempty"`
	ErrorMessage         string                       `json:"error_message,omitempty"`
	WinnerIndex          *int                         `json:"winner_index,omitempty"`
	SourceFence          *SourceFence                 `json:"source_fence,omitempty"`
	KeyModelFence        *KeyModelFence               `json:"key_model_fence,omitempty"`
	WinnerKeyFingerprint string                       `json:"winner_key_fingerprint,omitempty"`
	Projection           *Projection                  `json:"projection,omitempty"`
	NextDueAt            *time.Time                   `json:"next_due_at,omitempty"`
	FailureCount         int                          `json:"failure_count,omitempty"`
	FailureStartedAt     *time.Time                   `json:"failure_started_at,omitempty"`
	AccountStatus        string                       `json:"account_status,omitempty"`
	CooldownFence        *exactkeyprobe.CooldownFence `json:"cooldown_fence,omitempty"`
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
	CooldownFence    *exactkeyprobe.CooldownFence
}

type ProbeResult struct {
	Outcome      string
	StatusCode   int
	ErrorCode    string
	ErrorMessage string
}
