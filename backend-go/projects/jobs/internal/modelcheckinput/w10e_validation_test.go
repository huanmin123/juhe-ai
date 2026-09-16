package modelcheckinput

import (
	"strings"
	"testing"
)

// 覆盖 w9h 未触达的 validate/NewPolicySnapshot 校验臂。

func TestW10EValidationRemainingArms(t *testing.T) {
	// NewPolicySnapshot 内部 validatePolicy(verifyDigest=false) 失败臂。
	if _, err := NewPolicySnapshot("rev", "bogus-profile", true, 70, "fallback", 10); err == nil {
		t.Fatal("invalid policy profile must fail NewPolicySnapshot")
	}
	if _, err := NewPolicySnapshot("rev", "quick", true, 10, "fallback", 10); err == nil {
		t.Fatal("threshold < 40 must fail NewPolicySnapshot")
	}

	// validate() 的 SchemaVersion 臂（214）与 IssuedInput.Verify 的 validate 失败臂（183）。
	issued, err := IssueVersioned(w9hDraft(t), 1)
	if err != nil {
		t.Fatalf("issue err=%v", err)
	}
	badVersion := issued
	badVersion.SchemaVersion = 999
	if err := badVersion.Verify(); err == nil || !strings.Contains(err.Error(), "schema version is invalid") {
		t.Fatalf("bad schema version err=%v", err)
	}

	// 目标账户缺失字段 → validateAccount("target") 失败。
	missingField := w9hDraft(t)
	missingField.Target.ProviderCode = ""
	if _, err := IssueVersioned(missingField, 1); err == nil || !strings.Contains(err.Error(), "target snapshot providerCode is required") {
		t.Fatalf("missing target field err=%v", err)
	}

	// 对比账户缺失字段 → validateAccount("comparison") 失败。
	badComparison := w9hDraft(t)
	cmp := w9hAccount("cmp-1")
	cmp.MappedUpstreamModel = ""
	badComparison.TrustedComparison = true
	badComparison.Comparison = &cmp
	if _, err := IssueVersioned(badComparison, 1); err == nil || !strings.Contains(err.Error(), "comparison snapshot mappedUpstreamModel is required") {
		t.Fatalf("missing comparison field err=%v", err)
	}

	// Policy 无效 → input.Policy.Verify() 失败臂（234）。
	badPolicy := w9hDraft(t)
	badPolicy.Policy.Revision = ""
	if _, err := IssueVersioned(badPolicy, 1); err == nil {
		t.Fatal("invalid policy must fail Issue")
	}
}
