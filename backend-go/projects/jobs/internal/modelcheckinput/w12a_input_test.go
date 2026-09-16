package modelcheckinput

import (
	"strings"
	"testing"
	"time"
)

// w12a_input_test.go 覆盖 Policy 快照摘要篡改与 schedule 触发器校验分支；
// json.Marshal 错误分支（对纯数据结构不可达）另行登记。

func TestW12aPolicyDigestTamperFailsValidation(t *testing.T) {
	policy, err := NewPolicySnapshot("rev-1", "quick", true, 70, "fallback", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Verify(); err != nil {
		t.Fatalf("合法快照应通过: %v", err)
	}
	tampered := policy
	tampered.Digest = "w12a-wrong-digest"
	if err := tampered.Verify(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("摘要篡改应报错: %v", err)
	}
}

func TestW12aIssueRejectsScheduledWithoutScheduleID(t *testing.T) {
	base := validDraft(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	base.Trigger = TriggerScheduled
	base.ScheduleID = ""
	if _, err := Issue(base); err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("scheduled 缺 scheduleID 应报错: %v", err)
	}
}
