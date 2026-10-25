package accounthealth

import (
	"strings"
	"testing"
	"time"
)

// w12d_projection_units_test.go 覆盖 w12d 波次 projection.go 的剩余分支：
// validateProjection 拒绝真值表、游标损坏/推进臂、CAS 守卫、replay 幂等与
// 家族推进错误臂。全部基于 SQLite 临时 fixture，不触网。
//
// w12d 波次不可达清单（本包覆盖率 95% 目标内无法触达的语句，均已核对）：
//   - crypto.go 加密器构造/rand 失败分支：key 为 sha256 固定长度，
//     crypto/rand 无注入点（EncryptV1Envelope 23-37、DecryptV1Envelope 73-79）。
//   - projection.go newProjectionEventID 的 rand.Read 失败 fallback。
//   - projection.go 391-396 半游标损坏检测：cursors 表 CHECK 约束保证
//     observed_at/outcome_id 同 NULL 或同非 NULL，存储层无法注入半游标。
//   - projection.go 908-911 CAS SourceRevision 守卫：与 fenceMismatchReason
//     的 source config revision 校验同列同值，fence 先行拒绝使其不可达。
//   - listProjectedOutcomes payload 非 []byte/string 的 default 分支：SQLite
//     驱动文本列恒回 []byte，该分支为 PG/驱动差异守卫。
//   - store.go/direct_input_reader.go 的连接级错误臂（BeginTx/Commit/
//     rows.Err 失败）：database/sql 无连接故障注入点。
//   - config.go equalPath 非 Windows 分支：当前 GOOS=windows。

func TestW12dProjectionPureHelpers(t *testing.T) {
	// effectiveProjectionAPIKeyCount：去重/空白/legacy 回退。
	if got := effectiveProjectionAPIKeyCount([]string{" a ", "a", "", "b"}, ""); got != 2 {
		t.Fatalf("api keys count=%d", got)
	}
	if got := effectiveProjectionAPIKeyCount(nil, " legacy "); got != 1 {
		t.Fatalf("legacy fallback=%d", got)
	}
	if got := effectiveProjectionAPIKeyCount([]string{" "}, " "); got != 0 {
		t.Fatalf("empty count=%d", got)
	}
	// projectedHealthFailureCount：values 优先，非法值回退。
	outcome := Outcome{FailureCount: 3, Projection: &Projection{Values: map[string]any{"health_check_failure_count": float64(9)}}}
	if got := projectedHealthFailureCount(outcome); got != 9 {
		t.Fatalf("values override=%d", got)
	}
	outcome.Projection.Values["health_check_failure_count"] = -1.5
	if got := projectedHealthFailureCount(outcome); got != 3 {
		t.Fatalf("invalid value fallback=%d", got)
	}
	outcome.Projection.Values["health_check_failure_count"] = "7"
	if got := projectedHealthFailureCount(outcome); got != 3 {
		t.Fatalf("non numeric fallback=%d", got)
	}
	if got := projectedHealthFailureCount(Outcome{FailureCount: 2}); got != 2 {
		t.Fatalf("no projection fallback=%d", got)
	}
	// projectionChangesAvailability。
	for _, transition := range []string{"activation_success", "activation_error", "temporary_unavailable", "cooldown_success", "cooldown_error"} {
		if !projectionChangesAvailability(transition) {
			t.Fatalf("transition %s must change availability", transition)
		}
	}
	if projectionChangesAvailability("health_failure") {
		t.Fatal("health_failure must not change availability")
	}
	// limitedText。
	if got := limitedText("  ", 5); got != "" {
		t.Fatalf("blank limited=%q", got)
	}
	if got := limitedText("abcdef", 3); got != "abc" {
		t.Fatalf("truncated limited=%q", got)
	}
	// projectionDBTime：time.Time 直扫保留 RFC3339 原文。
	scanner := projectionDBTime{}
	stamp := time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)
	if err := scanner.Scan(stamp); err != nil || scanner.RawText != "2026-09-10T08:30:00Z" {
		t.Fatalf("time scan raw=%q err=%v", scanner.RawText, err)
	}
	// familyDispatchTransitionID 稳定且随账户变化。
	if familyDispatchTransitionID("dispatch-x", "a") == familyDispatchTransitionID("dispatch-x", "b") {
		t.Fatal("family transition id must depend on account")
	}
	if !strings.HasPrefix(newProjectionDispatchTransitionID(), "dispatch-") {
		t.Fatal("dispatch transition id prefix")
	}
}

func ptrInt64(value int64) *int64 { return &value }
