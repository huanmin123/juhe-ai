package accountkeystates

// w11e accountkeystates 错误臂：纯函数分支（观察时间归一、配额恢复窗口、
// 抖动窗口、消息清洗）与 Store 写路径的跳过/非法输入分支。

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"context"
	"strings"
	"testing"
	"time"
)

func TestW11ENormalizeObservedAtArms(t *testing.T) {
	fallback := "2026-09-16T10:00:00.000Z"
	if got := normalizeObservedAt("", fallback); got != fallback {
		t.Fatalf("空值=%q", got)
	}
	if got := normalizeObservedAt("junk", fallback); got != fallback {
		t.Fatalf("非法值=%q", got)
	}
	if got := normalizeObservedAt("2026-09-16T09:00:00.000Z", "junk"); got != "junk" {
		t.Fatalf("非法 fallback=%q", got)
	}
	if got := normalizeObservedAt("2026-09-16T09:00:00.000Z", fallback); got != "2026-09-16T09:00:00.000Z" {
		t.Fatalf("较早观察=%q", got)
	}
	if got := normalizeObservedAt("2026-09-16T11:00:00.000Z", fallback); got != fallback {
		t.Fatalf("较晚观察=%q", got)
	}
}

func TestW11EQuotaAndStatusHelpers(t *testing.T) {
	if quotaModeFromErrorCode(QuotaRecoveryExplicitErrorCode) != "explicit_reset" || quotaModeFromErrorCode(QuotaRecoveryGenericErrorCode) != "generic" || quotaModeFromErrorCode("w11e-other") != "" {
		t.Fatal("配额模式判定错误")
	}
	if normalizeFailureStatus("rate_limited") != "rate_limited" || normalizeFailureStatus("error") != "error" || normalizeFailureStatus("w11e") != "temporary_unavailable" {
		t.Fatal("失败状态归一错误")
	}
	if nextProbeBackoffSeconds(0) != initialProbeBackoffSeconds {
		t.Fatal("初始退避错误")
	}
	if nextProbeBackoffSeconds(maxProbeBackoffSeconds) != maxProbeBackoffSeconds {
		t.Fatal("退避上限错误")
	}
	if normalizeProbeDeferSeconds(0) != initialProbeBackoffSeconds || normalizeProbeDeferSeconds(maxProbeBackoffSeconds+1) != maxProbeBackoffSeconds || normalizeProbeDeferSeconds(120) != 120 {
		t.Fatal("延迟归一错误")
	}
	if sanitizeRuntimeMessage("  a \n b  ") != "a b" {
		t.Fatalf("空白折叠=%q", sanitizeRuntimeMessage("  a \n b  "))
	}
	if sanitizeRuntimeMessage(strings.Repeat("字", 1200)) != strings.Repeat("字", 1000) {
		t.Fatal("1000 截断错误")
	}
	if sanitizeRuntimeMessage("   ") != "上游请求失败" {
		t.Fatalf("空消息=%q", sanitizeRuntimeMessage("   "))
	}
	if normalizeTraceID("  ") != nil {
		t.Fatal("空 trace 必须为 nil")
	}
	if normalizeTraceID("  abc  ") != "abc" {
		t.Fatal("trace trim 错误")
	}
	if got := normalizeTraceID(strings.Repeat("t", 250)); got != strings.Repeat("t", 200) {
		t.Fatalf("trace 截断=%v", got)
	}
	if firstNonEmptyStr("", "", "x", "y") != "x" || firstNonEmptyStr() != "" {
		t.Fatal("firstNonEmptyStr 错误")
	}
}

func TestW11EPassiveJitterHelpers(t *testing.T) {
	cases := []struct {
		intervalMS int64
		want       int64
	}{
		{0, 0},          // 归一为 1ms，half=0
		{10_000, 5_000}, // half
		{100_000, 30_000},
		{3_600_000 - 1, 30_000},
		{3_600_000, 1_800_000},
		{10 * 3_600_000, 30 * 60_000},
		{3 * 24 * 3_600_000, 60 * 60_000},
		{30 * 24 * 3_600_000, 8 * 60 * 60_000},
	}
	for _, tc := range cases {
		if got := passiveJitterWindowMS(tc.intervalMS); got != tc.want {
			t.Fatalf("window(%dms)=%d want=%d", tc.intervalMS, got, tc.want)
		}
	}
	if passiveJitterOffsetMS(0) != 0 {
		t.Fatal("零窗口偏移必须为 0")
	}
	offset := passiveJitterOffsetMS(30_000)
	if offset == 0 || offset > 30_000 || offset < -30_000 {
		t.Fatalf("偏移越界: %d", offset)
	}
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	retryAt := passiveProbeRetryAt(0, func() time.Time { return now })
	parsed, err := time.Parse(rfc3339Milli, retryAt)
	if err != nil || !parsed.After(now) {
		t.Fatalf("retryAt=%q err=%v", retryAt, err)
	}
	if got := passiveProbeNotBeforeAt("junk", func() time.Time { return now }); got != "junk" {
		t.Fatalf("非法 deadline=%q", got)
	}
	past := passiveProbeNotBeforeAt("2026-09-16T09:00:00.000Z", func() time.Time { return now })
	if past != "2026-09-16T09:00:00.000Z" {
		t.Fatalf("过期 deadline=%q", past)
	}
	future := passiveProbeNotBeforeAt("2026-09-16T10:10:00.000Z", func() time.Time { return now })
	parsed, err = time.Parse(rfc3339Milli, future)
	if err != nil || parsed.Before(now.Add(10*time.Minute)) {
		t.Fatalf("未来 deadline=%q err=%v", future, err)
	}
}

func TestW11EQuotaRecoveryStartedAtArms(t *testing.T) {
	observed := "2026-09-16T10:00:00.000Z"
	if got := quotaRecoveryStartedAt("generic", nil, observed, true); got != nil {
		t.Fatalf("breakWindow=%v", got)
	}
	if got := quotaRecoveryStartedAt("explicit_reset", nil, observed, false); got != nil {
		t.Fatalf("explicit_reset=%v", got)
	}
	existing := &runtimeRow{lastErrorCode: QuotaRecoveryGenericErrorCode, status: "rate_limited", recoveryStartedAt: "2026-09-15T08:00:00.000Z"}
	if got := quotaRecoveryStartedAt("generic", existing, observed, false); got != "2026-09-15T08:00:00.000Z" {
		t.Fatalf("沿用既有恢复起点=%v", got)
	}
	noRecovery := &runtimeRow{lastErrorCode: QuotaRecoveryGenericErrorCode, status: "rate_limited"}
	if got := quotaRecoveryStartedAt("generic", noRecovery, observed, false); got != observed {
		t.Fatalf("无既有起点=%v", got)
	}
	otherMode := &runtimeRow{recoveryStartedAt: "2026-09-14T08:00:00.000Z"}
	if got := quotaRecoveryStartedAt("", otherMode, observed, false); got != "2026-09-14T08:00:00.000Z" {
		t.Fatalf("非 generic 沿用=%v", got)
	}
	if got := quotaRecoveryStartedAt("", nil, observed, false); got != observed {
		t.Fatalf("无既有行=%v", got)
	}
	// 既往非 generic 配额模式：直接用观察时间。
	nonQuota := &runtimeRow{lastErrorCode: "other", status: "rate_limited"}
	if got := quotaRecoveryStartedAt("generic", nonQuota, observed, false); got != observed {
		t.Fatalf("非 generic 既往=%v", got)
	}
}

func TestW11EStoreConstructorsAndSQLFragments(t *testing.T) {
	if _, err := NewStore(Config{}); err == nil || !strings.Contains(err.Error(), "业务库句柄") {
		t.Fatalf("nil DB 必须拒绝: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/w11e-keys.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStore(Config{DB: db, Secret: "  "}); err == nil || !strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("空 secret 必须拒绝: %v", err)
	}
	sqliteStore := &Store{postgres: false}
	pgStore := &Store{postgres: true}
	if sqliteStore.writePrefix() != "" || pgStore.writePrefix() != "current_state." {
		t.Fatal("写前缀错误")
	}
	if pgStore.writeColumn("status") != "current_state.status" || sqliteStore.writeColumn("status") != "status" {
		t.Fatal("列引用错误")
	}
	if !strings.Contains(pgStore.updateTarget(), "AS current_state") || sqliteStore.updateTarget() == pgStore.updateTarget() {
		t.Fatal("UPDATE 目标错误")
	}
	// claimCandidates 的非法 now。
	if _, err := sqliteStore.claimCandidates(context.Background(), []ProbeCandidate{{AccountID: "a", KeyFingerprint: "f"}}, 1, "junk"); err == nil {
		t.Fatal("非法 now 必须失败")
	}
	// 空候选直接成功。
	if claimed, err := sqliteStore.claimCandidates(context.Background(), nil, 1, "2026-09-16T10:00:00Z"); err != nil || len(claimed) != 0 {
		t.Fatalf("空候选=%v err=%v", claimed, err)
	}
	// chunkValues。
	if chunkValues(nil, 10) != nil {
		t.Fatal("空切片必须为 nil")
	}
	chunks := chunkValues([]string{"a", "b", "c"}, 2)
	if len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 1 {
		t.Fatalf("分块=%v", chunks)
	}
}

func TestW11ERevalidateGateReasonArms(t *testing.T) {
	if reason, ok := revalidateGateReasonFromGate(nil, 1); ok || reason != ReasonAccountNotFound {
		t.Fatalf("nil 行=%q %t", reason, ok)
	}
	row := &revalidateGateRow{status: "active", schedulable: 1, configRevision: 2}
	if reason, ok := revalidateGateReasonFromGate(row, 1); ok || reason != ReasonConfigRevisionConflict {
		t.Fatalf("revision 冲突=%q", reason)
	}
	row.configRevision = 1
	row.status = "disabled"
	if reason, ok := revalidateGateReasonFromGate(row, 1); ok || reason != ReasonAccountNotActive {
		t.Fatalf("非 active=%q", reason)
	}
	row.status = "active"
	row.schedulable = 0
	if reason, ok := revalidateGateReasonFromGate(row, 1); ok || reason != ReasonAccountUnschedulable {
		t.Fatalf("不可调度=%q", reason)
	}
	row.schedulable = 1
	if reason, ok := revalidateGateReasonFromGate(row, 1); !ok || reason != "" {
		t.Fatalf("通过=%q %t", reason, ok)
	}
	// revalidateGateReason 的 nil 行为同源。
	if reason, ok := revalidateGateReason(nil, 1); ok || reason != ReasonAccountNotFound {
		t.Fatalf("nil 包装=%q", reason)
	}
	wrapped := &revalidateAccountRow{status: "active", schedulable: 1, configRevision: 1}
	if reason, ok := revalidateGateReason(wrapped, 1); !ok || reason != "" {
		t.Fatalf("包装通过=%q %t", reason, ok)
	}
}

func TestW11ESummaryHelperArms(t *testing.T) {
	base := summaryDetailRow{keyFingerprint: "fp-b", keyIndex: 1, lastFailureAt: "2026-09-16T09:00:00.000Z"}
	newer := base
	newer.lastFailureAt = "2026-09-16T10:00:00.000Z"
	if !latestFailureBefore(newer, base) || latestFailureBefore(base, newer) {
		t.Fatal("时间排序错误")
	}
	lowerIndex := base
	lowerIndex.keyIndex = 0
	if !latestFailureBefore(lowerIndex, base) || latestFailureBefore(base, lowerIndex) {
		t.Fatal("keyIndex 平局排序错误")
	}
	sameIndex := base
	sameIndex.keyFingerprint = "fp-a"
	if !latestFailureBefore(sameIndex, base) || latestFailureBefore(base, sameIndex) {
		t.Fatal("指纹平局排序错误")
	}
	if runtimeErrorMessageForResponse("  a \n b ") != "a b" {
		t.Fatal("消息折叠错误")
	}
	if runtimeErrorMessageForResponse(strings.Repeat("m", 300)) != strings.Repeat("m", 240) {
		t.Fatal("消息 240 截断错误")
	}
	if runtimeTraceIdForResponse("  t  ") != "t" {
		t.Fatal("trace trim 错误")
	}
	if runtimeTraceIdForResponse(strings.Repeat("t", 250)) != strings.Repeat("t", 200) {
		t.Fatal("trace 200 截断错误")
	}
	if truncatePrefix("short", 12) != "short" || truncatePrefix("longerthan12", 12) != "longerthan12"[:12] {
		t.Fatal("前缀截断错误")
	}
}

func TestW11EStoreWriteSkipArms(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "w11e-acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	valid := w9dPoolAccount("w11e-acc", fps)
	ctx := context.Background()

	// 非池账户/禁用/非法输入跳过臂。
	ghost := valid
	ghost.SelectedAPIKeyFingerprint = "w11e-ghost"
	if result, err := h.store.RecordFailure(ctx, FailureInput{Account: ghost}); err != nil || result.SkippedReason != "not_api_key_pool_account" {
		t.Fatalf("非池账户=%+v err=%v", result, err)
	}
	// 非法 cooldownUntil。
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, CooldownUntil: "junk"}); err == nil || !strings.Contains(err.Error(), "cooldownUntil") {
		t.Fatalf("非法 cooldown 必须报错: %v", err)
	}
	// fence 无既有行 → stale。
	fence := ExpectedProbeState{Status: "rate_limited", NextProbeAt: "2026-09-16T11:00:00.000Z", StateUpdatedAt: "2026-09-16T10:00:00.000Z", ProbeClaimToken: "tok"}
	if result, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Expected: fence}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("fence 无行=%+v err=%v", result, err)
	}
	// 非法 fence → invalid reason。
	badFence := ExpectedProbeState{NextProbeAt: "junk"}
	if result, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Expected: badFence}); err != nil || result.SkippedReason != "invalid_expected_probe_at" {
		t.Fatalf("非法 fence=%+v err=%v", result, err)
	}
	// 手工恢复必需：error 状态的成功写入被跳过。
	if result, err := h.store.RecordSuccess(ctx, valid, SuccessInput{Expected: ExpectedProbeState{Status: "error"}}); err != nil || result.SkippedReason != "manual_restore_required" {
		t.Fatalf("error 状态=%+v err=%v", result, err)
	}
	// defer 缺 next_probe_at。
	if result, err := h.store.DeferProbe(ctx, valid, DeferInput{}); err != nil || result.SkippedReason != "missing_expected_probe_at" {
		t.Fatalf("缺 probe at=%+v err=%v", result, err)
	}
	// defer 非法围栏。
	if result, err := h.store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: "junk"}); err != nil || result.SkippedReason != "invalid_expected_probe_at" {
		t.Fatalf("非法 probe at=%+v err=%v", result, err)
	}
	// defer 未变更（无既有行且无围栏时的 stale）。
	if result, err := h.store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: "2026-09-16T11:00:00.000Z", DelaySeconds: 60}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("defer stale=%+v err=%v", result, err)
	}
	// 首次失败写入 + 配额窗口臂。
	first, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Status: "rate_limited", ErrorCode: QuotaRecoveryGenericErrorCode, QuotaRecoveryMode: "generic"})
	if err != nil || !first.Changed {
		t.Fatalf("首次失败=%+v err=%v", first, err)
	}
	// 观察时间早于既有 last_attempt_at → CAS 守卫拒绝，未变更且无围栏跳过原因。
	second, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Status: "w11e-status", ObservedAt: "2026-09-16T09:30:00.000Z"})
	if err != nil || second.Changed || second.SkippedReason != "" {
		t.Fatalf("过期观察=%+v err=%v", second, err)
	}
	// 新观察时间的失败正常写入（推进注入时钟使 last_attempt_at 守卫通过）。
	h.advance(time.Second)
	third, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Status: "rate_limited", BreakQuotaRecoveryWindow: true})
	if err != nil || !third.Changed {
		t.Fatalf("第三次失败=%+v err=%v", third, err)
	}
	// 成功写入。
	h.advance(time.Second)
	success, err := h.store.RecordSuccess(ctx, valid, SuccessInput{})
	if err != nil || !success.Changed {
		t.Fatalf("成功写入=%+v err=%v", success, err)
	}
	// AllUnavailable 对缺失账户返回 false。
	unavailable, err := h.store.AllUnavailable(ctx, "w11e-missing")
	if err != nil || unavailable {
		t.Fatalf("缺失账户=%t err=%v", unavailable, err)
	}
}
