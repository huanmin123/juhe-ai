package modelcheckinput

import (
	"strings"
	"testing"
	"time"
)

// w14i_marshal_arms_test.go 覆盖剩余可测臂：Payload 的 Verify 失败臂、
// validate 的 Policy.Verify 失败臂，以及超 RFC3339 年份（>9999）导致的
// digest json.Marshal 失败臂（issue/Verify/digest 共 3 处）。
//
// w14i 波次不可达清单（沿用 w12a 登记，均已核对）：
//   - NewPolicySnapshot 的 policyDigest 错误臂（input.go 63-65）：
//     PolicySnapshot 仅含 string/bool/int 字段，json.Marshal 恒成功。
//   - IdentityKey 的 marshal 错误臂（input.go 175-177）：identity 结构仅含
//     string 字段，json.Marshal 恒成功。
//   - validatePolicy 的 policyDigest 错误臂（input.go 329-331）与
//     policyDigest 自身的 marshal 错误臂（input.go 348-350）：同上，纯值
//     结构不可失败，属防御守卫。

func w14iMarshalBoomDraft(issuedAt time.Time) Draft {
	draft := validDraft(issuedAt)
	return draft
}

func TestW14iPayloadRejectsDigestMismatch(t *testing.T) {
	// 契约：Payload 输出前必须重新校验摘要，篡改后的输入不得序列化。
	issued, err := Issue(validDraft(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	issued.InputDigest = "w14i-wrong-digest"
	if _, err := issued.Payload(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Payload 应在摘要校验处失败: %v", err)
	}
}

func TestW14iIssueRejectsTamperedPolicyDigest(t *testing.T) {
	// 契约：draft 携带的策略快照摘要被篡改时，issue 的 validate 阶段必须
	// 经 Policy.Verify fail-closed。
	draft := w14iMarshalBoomDraft(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	draft.Policy.Digest = "w14i-tampered-policy-digest"
	if _, err := Issue(draft); err == nil || !strings.Contains(err.Error(), "policy snapshot digest mismatch") {
		t.Fatalf("篡改策略摘要应报错: %v", err)
	}
}

func TestW14iIssueFailsWhenDeadlineYearExceedsRFC3339(t *testing.T) {
	// 契约：IssuedAt/DeadlineAt 年份超出 RFC3339 可表示范围时 digest 序列化
	// 失败，issue 不得产出半构造输入。
	draft := w14iMarshalBoomDraft(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	draft.DeadlineAt = time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Issue(draft); err == nil || !strings.Contains(err.Error(), "marshal model check input digest") {
		t.Fatalf("超范围年份应使 digest 失败: %v", err)
	}
}

func TestW14iVerifyFailsWhenIssueYearExceedsRFC3339(t *testing.T) {
	// 契约：Validate 通过但时间字段无法序列化时，Verify 应上抛 digest 错误
	// 而不是误报 mismatch。
	issued, err := Issue(validDraft(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	issued.IssuedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	issued.DeadlineAt = time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := issued.Verify(); err == nil || !strings.Contains(err.Error(), "marshal model check input digest") {
		t.Fatalf("超范围年份 Verify 应上抛 digest 错误: %v", err)
	}
}
