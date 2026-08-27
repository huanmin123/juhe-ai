// Package modelcheckquality owns the credential-free J3b quality-decision
// rules. Business enforcement, recovery and health synchronization are
// separate projectors; a decision with incomplete evidence must never cause a
// state mutation.
package modelcheckquality

import (
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
)

type Evidence struct {
	// Formed is true only after the trust/identity/Juice projector has
	// completed its own durable evidence contract for this run.
	Formed                   bool
	MappingStatus            string
	Gpt56JuiceStrongRepeated bool
}

type Decision struct {
	TriggerKind      modelcheckinput.Trigger `json:"triggerKind"`
	Triggered        bool                    `json:"triggered"`
	HardFailure      bool                    `json:"hardFailure"`
	Threshold        int                     `json:"threshold"`
	Score            int                     `json:"score"`
	ConfiguredAction string                  `json:"configuredAction"`
	Result           string                  `json:"result"`
	ReasonCodes      []string                `json:"reasonCodes"`
	Message          string                  `json:"message"`
	DecidedAt        time.Time               `json:"decidedAt"`
}

// Fact is the durable, versioned quality decision. The decision is kept
// together with the immutable evidence identities so a later retry cannot
// silently apply a decision to a different terminal outcome or policy.
type Fact struct {
	Version        int      `json:"version"`
	OutcomeDigest  string   `json:"outcomeDigest"`
	PolicyDigest   string   `json:"policyDigest"`
	EvidenceDigest string   `json:"evidenceDigest"`
	Decision       Decision `json:"decision"`
}

func NewFact(decision Decision, outcomeDigest, policyDigest, evidenceDigest string) Fact {
	return Fact{Version: 1, OutcomeDigest: strings.TrimSpace(outcomeDigest), PolicyDigest: strings.TrimSpace(policyDigest), EvidenceDigest: strings.TrimSpace(evidenceDigest), Decision: decision}
}

// Decide mirrors Node's quality gate. It deliberately produces only a
// decision fact: another Go-owned projector must apply enforcement with CAS
// after this fact and its supporting observations are durable.
func Decide(trigger modelcheckinput.Trigger, policy modelcheckinput.PolicySnapshot, summary modelcheckprobe.SummaryResult, completed bool, evidence Evidence, decidedAt time.Time) Decision {
	decision := Decision{
		TriggerKind:      trigger,
		Threshold:        policy.PenaltyThreshold,
		Score:            summary.Score,
		ConfiguredAction: policy.PenaltyAction,
		Result:           "not_triggered",
		DecidedAt:        decidedAt.UTC(),
	}
	if !evidence.Formed {
		decision.ReasonCodes = []string{"quality_evidence_not_formed"}
		decision.Message = "未形成质量判定证据，本次不执行质量处罚、质量隔离/降级或健康统计失败写入"
		return decision
	}
	unavailable := completed && summary.Level == "unavailable"
	hardFailure := strings.TrimSpace(evidence.MappingStatus) == "undeclared_mismatch" || evidence.Gpt56JuiceStrongRepeated
	qualityFailed := completed && !unavailable && (hardFailure || summary.Score < policy.PenaltyThreshold)
	decision.HardFailure = hardFailure
	decision.Triggered = qualityFailed
	switch {
	case !completed:
		decision.Message = "检测未正常完成，不执行质量处罚"
	case unavailable:
		decision.ReasonCodes = []string{"quality_evidence_unavailable"}
		decision.Message = "未形成有效质量证据，本次不执行质量处罚"
	case hardFailure:
		decision.ReasonCodes = []string{"hard_quality_conflict"}
		decision.Message = "检测命中硬失败质量证据，等待 Go 质量 projector 按冻结策略执行"
	case qualityFailed:
		decision.ReasonCodes = []string{"score_below_threshold"}
		decision.Message = "质量判定不达标，等待 Go 质量 projector 按冻结策略执行"
	default:
		decision.Message = "质量达标，未触发处罚"
	}
	return decision
}
