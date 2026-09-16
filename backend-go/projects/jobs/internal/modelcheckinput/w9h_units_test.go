package modelcheckinput

import (
	"strings"
	"testing"
	"time"
)

func w9hPolicy(t *testing.T) PolicySnapshot {
	t.Helper()
	policy, err := NewPolicySnapshot("w9h-policy", "quick", true, 70, "fallback", 10)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func w9hAccount(id string) AccountSnapshot {
	return AccountSnapshot{
		ID: id, ConfigRevision: "1", ProviderCode: "openai", ProtocolProfileID: "profile",
		ProtocolProfileRevision: "2", EndpointFingerprint: "fp", CredentialEnvelopeRef: "env",
		MappedUpstreamModel:       "gpt-w9h-upstream",
		ProxyConfigurationVersion: "1",
	}
}

func w9hDraft(t *testing.T) Draft {
	t.Helper()
	return Draft{
		InputID: "input-w9h", SystemAccountID: "sys_admin", ActorSystemAccountID: "actor",
		Target: w9hAccount("acc-1"), Model: "gpt-w9h", Profile: "quick",
		Trigger: TriggerManual, Policy: w9hPolicy(t), ProbeSetVersion: "probe-set-w9h",
		IssuedAt:   time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		DeadlineAt: time.Date(2026, 9, 6, 12, 1, 0, 0, time.UTC),
	}
}

func TestW9HNewPolicySnapshotValidation(t *testing.T) {
	if _, err := NewPolicySnapshot("", "quick", true, 70, "fallback", 10); err == nil {
		t.Fatal("empty revision must fail")
	}
	valid, err := NewPolicySnapshot("rev", "full", false, 95, "disable", 60)
	if err != nil {
		t.Fatalf("valid policy err=%v", err)
	}
	if err := valid.Verify(); err != nil {
		t.Fatalf("verify err=%v", err)
	}
	// 篡改 digest → Verify 失败。
	tampered := valid
	tampered.Digest = "deadbeef"
	if err := tampered.Verify(); err == nil {
		t.Fatal("tampered digest must fail")
	}
	// 篡改阈值 → Verify 失败（digest 锚定）。
	tampered = valid
	tampered.PenaltyThreshold = 1
	if err := tampered.Verify(); err == nil {
		t.Fatal("tampered threshold must fail")
	}
}

func TestW9HIssueAndValidateArms(t *testing.T) {
	draft := w9hDraft(t)
	// version 必须 > 0。
	if _, err := IssueVersioned(draft, 0); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("version err=%v", err)
	}
	issued, err := IssueVersioned(draft, 3)
	if err != nil {
		t.Fatalf("issue err=%v", err)
	}
	if err := issued.Verify(); err != nil {
		t.Fatalf("verify err=%v", err)
	}
	// 身份键：同一身份重试共享序列；不同模型身份不同。
	key, err := issued.IdentityKey()
	if err != nil || key == "" {
		t.Fatalf("identity key=%q err=%v", key, err)
	}
	otherModel := w9hDraft(t)
	otherModel.Model = "gpt-other"
	otherIssued, err := IssueVersioned(otherModel, 1)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := otherIssued.IdentityKey()
	if err != nil || otherKey == key {
		t.Fatalf("other key=%q err=%v", otherKey, err)
	}
	// Payload 往返需要 Verify 通过。
	payload, err := issued.Payload()
	if err != nil || len(payload) == 0 {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	// 摘要不匹配 → Verify 失败。
	mismatch := issued
	mismatch.InputDigest = "bogus"
	if err := mismatch.Verify(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mismatch err=%v", err)
	}
	// 触发器与 schedule 交叉臂。
	scheduled := w9hDraft(t)
	scheduled.Trigger = TriggerScheduled
	if _, err := IssueVersioned(scheduled, 1); err == nil || !strings.Contains(err.Error(), "requires scheduleId") {
		t.Fatalf("scheduled without id err=%v", err)
	}
	scheduled.ScheduleID = "sched-1"
	if _, err := IssueVersioned(scheduled, 1); err != nil {
		t.Fatalf("scheduled with id err=%v", err)
	}
	manual := w9hDraft(t)
	manual.ScheduleID = "sched-1"
	if _, err := IssueVersioned(manual, 1); err == nil || !strings.Contains(err.Error(), "must not include scheduleId") {
		t.Fatalf("manual with id err=%v", err)
	}
	badTrigger := w9hDraft(t)
	badTrigger.Trigger = "bogus"
	if _, err := IssueVersioned(badTrigger, 1); err == nil || !strings.Contains(err.Error(), "trigger is invalid") {
		t.Fatalf("trigger err=%v", err)
	}
	// 可信对照不一致：TrustedComparison=true 但 Comparison 为 nil。
	inconsistent := w9hDraft(t)
	inconsistent.TrustedComparison = true
	if _, err := IssueVersioned(inconsistent, 1); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("inconsistent err=%v", err)
	}
	// 缺少必需字段。
	empty := w9hDraft(t)
	empty.Model = ""
	if _, err := IssueVersioned(empty, 1); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("missing model err=%v", err)
	}
	badProfile := w9hDraft(t)
	badProfile.Profile = "deep"
	if _, err := IssueVersioned(badProfile, 1); err == nil || !strings.Contains(err.Error(), "profile is invalid") {
		t.Fatalf("profile err=%v", err)
	}
	// 对照账户校验：对照存在但字段缺失。
	badComparison := w9hDraft(t)
	badComparison.TrustedComparison = true
	comparison := w9hAccount("acc-2")
	comparison.ProviderCode = ""
	badComparison.Comparison = &comparison
	if _, err := IssueVersioned(badComparison, 1); err == nil {
		t.Fatal("incomplete comparison must fail")
	}
}
