package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wgOutcomeBase 构造通过幂等/字段校验的最小 outcome。
func wgOutcomeBase(requestID string) Outcome {
	return Outcome{
		OutcomeID:        "oid-" + requestID,
		RequestID:        requestID,
		AccountID:        "acct-store",
		Outcome:          OutcomeUpstreamFailed,
		ObservedAt:       time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		InputVersion:     1,
		ConfigRevision:   2,
		DispatchRevision: 3,
	}
}

// wgStoreLease 获取一个测试租约并登记释放。
func wgStoreLease(t *testing.T, store *Store, owner string) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), owner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("获取租约: %v %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })
	return lease
}

// TestAppendOutcomeValidationContract 覆盖 outcome 字段与投影契约校验。
func TestAppendOutcomeValidationContract(t *testing.T) {
	store := wgNewStore(t)
	lease := wgStoreLease(t, store, "wg-validate")
	ctx := context.Background()
	// 缺幂等字段。
	incomplete := wgOutcomeBase("req-x")
	incomplete.OutcomeID = ""
	if _, err := store.AppendOutcome(ctx, lease, incomplete); err == nil {
		t.Fatal("缺 OutcomeID 必须报错")
	}
	// 投影 fence 不一致。
	mismatched := wgOutcomeBase("req-x")
	mismatched.Projection = &Projection{TargetAccountID: "other", InputVersion: 1, ConfigRevision: 2, DispatchRevision: 3, ExpectedAccountStatus: "active"}
	if _, err := store.AppendOutcome(ctx, lease, mismatched); err == nil {
		t.Fatal("投影 fence 不一致必须报错")
	}
	// 缺 expected status。
	noStatus := wgOutcomeBase("req-x")
	noStatus.Projection = &Projection{TargetAccountID: "acct-store", InputVersion: 1, ConfigRevision: 2, DispatchRevision: 3}
	if _, err := store.AppendOutcome(ctx, lease, noStatus); err == nil {
		t.Fatal("缺 expected account status 必须报错")
	}
	// expected fence 无效。
	badFence := wgOutcomeBase("req-x")
	badFence.Projection = &Projection{
		TargetAccountID: "acct-store", InputVersion: 1, ConfigRevision: 2, DispatchRevision: 3,
		ExpectedAccountStatus: "active",
		ExpectedCooldownFence: &CooldownFence{Generation: "g"},
	}
	if _, err := store.AppendOutcome(ctx, lease, badFence); err == nil {
		t.Fatal("无效 expected fence 必须报错")
	}
	// cooldown source revision 不一致。
	revision := int64(5)
	fenceMismatch := wgOutcomeBase("req-x")
	fenceMismatch.Projection = &Projection{
		TargetAccountID: "acct-store", InputVersion: 1, ConfigRevision: 2, DispatchRevision: 3,
		ExpectedAccountStatus: "active",
		ExpectedCooldownFence: &CooldownFence{ObservationStartedAt: time.Now(), Generation: "g", SourceConfigRevision: &revision},
		SourceRevision:        int64Pointer(6),
	}
	if _, err := store.AppendOutcome(ctx, lease, fenceMismatch); err == nil {
		t.Fatal("cooldown source revision 不一致必须报错")
	}
}

// TestAppendOutcomeRoundTripAndDuplicate 覆盖写入、当前状态推进、幂等重复
// 与租约丢失分支。
func TestAppendOutcomeRoundTripAndDuplicate(t *testing.T) {
	store := wgNewStore(t)
	lease := wgStoreLease(t, store, "wg-append")
	ctx := context.Background()
	outcome := wgOutcomeBase("req-append-1")
	inserted, err := store.AppendOutcome(ctx, lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("首次写入必须成功: %v %v", inserted, err)
	}
	// 幂等重复：相同 request → false，不报错。
	duplicate := outcome
	duplicate.OutcomeID = "oid-other"
	inserted, err = store.AppendOutcome(ctx, lease, duplicate)
	if err != nil || inserted {
		t.Fatalf("重复写入必须幂等: %v %v", inserted, err)
	}
	// 当前状态被推进。
	state, found, err := store.LoadCurrentState(ctx, "acct-store")
	if err != nil || !found {
		t.Fatalf("当前状态必须存在: %v %v", found, err)
	}
	if state.OutcomeID != "oid-req-append-1" || state.InputVersion != 1 {
		t.Fatalf("当前状态内容错误: %+v", state)
	}
	// HasRequest。
	if found, err := store.HasRequest(ctx, "req-append-1"); err != nil || !found {
		t.Fatalf("HasRequest 必须命中: %v %v", found, err)
	}
	if found, err := store.HasRequest(ctx, "req-missing"); err != nil || found {
		t.Fatalf("缺失 request 必须返回 false: %v %v", found, err)
	}
	if _, err := store.HasRequest(ctx, " "); err == nil {
		t.Fatal("空 request 必须报错")
	}
	// 租约丢失：释放后追加必须失败。
	lost := wgOutcomeBase("req-lost")
	lostStore := wgNewStore(t)
	lostLease := wgStoreLease(t, lostStore, "wg-lost")
	if err := lostStore.ReleaseOwnerLease(ctx, lostLease); err != nil {
		t.Fatal(err)
	}
	if _, err := lostStore.AppendOutcome(ctx, lostLease, lost); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("租约丢失必须失败: %v", err)
	}
}

// TestAppendOutcomeDirectInputSuppression 覆盖坏输入抑制的写入/刷新/读取。
func TestAppendOutcomeDirectInputSuppression(t *testing.T) {
	store := wgNewStore(t)
	lease := wgStoreLease(t, store, "wg-suppress")
	ctx := context.Background()
	nextDue := time.Now().UTC().Add(10 * time.Minute)
	invalid := wgOutcomeBase("req-invalid-1")
	invalid.ErrorCode = "direct_input_invalid"
	invalid.ErrorMessage = "字段缺失"
	invalid.NextDueAt = &nextDue
	inserted, err := store.AppendOutcome(ctx, lease, invalid)
	if err != nil || !inserted {
		t.Fatalf("抑制 outcome 必须写入: %v %v", inserted, err)
	}
	suppressions, err := store.LoadDirectInputSuppressions(ctx, time.Now())
	if err != nil || len(suppressions) != 1 {
		t.Fatalf("必须读到一条抑制: %+v %v", suppressions, err)
	}
	if suppressions[0].AccountID != "acct-store" || suppressions[0].InputVersion != 1 {
		t.Fatalf("抑制围栏错误: %+v", suppressions[0])
	}
	// 抑制在到期后不再返回。
	empty, err := store.LoadDirectInputSuppressions(ctx, nextDue.Add(time.Minute))
	if err != nil || len(empty) != 0 {
		t.Fatalf("到期抑制必须过滤: %v %v", empty, err)
	}
	// 幂等重复 + NextDueAt → 只刷新抑制窗口。
	refreshed := invalid
	refreshed.OutcomeID = "oid-invalid-1-refresh"
	refreshed.NextDueAt = &nextDue
	if _, err := store.AppendOutcome(ctx, lease, refreshed); err != nil {
		t.Fatalf("重复刷新必须成功: %v", err)
	}
	suppressions, err = store.LoadDirectInputSuppressions(ctx, time.Now())
	if err != nil || len(suppressions) != 1 {
		t.Fatalf("抑制仍为一条: %v %v", suppressions, err)
	}
}

// TestAppendOutcomeCooldownProjectionPersistsPayload 覆盖带 expected fence
// 的冷却投影（payload 重写 + 当前状态 CAS）。
func TestAppendOutcomeCooldownProjectionPayload(t *testing.T) {
	store := wgNewStore(t)
	lease := wgStoreLease(t, store, "wg-coolproj")
	ctx := context.Background()
	observed := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
	revision := int64(2)
	fence := &CooldownFence{ObservationStartedAt: observed, Generation: "gen-cool", SourceConfigRevision: &revision}
	outcome := wgOutcomeBase("req-cool-1")
	outcome.Outcome = OutcomeSuccess
	outcome.Projection = &Projection{
		TargetAccountID:       "acct-store",
		InputVersion:          1,
		ConfigRevision:        2,
		DispatchRevision:      3,
		TransitionKind:        "cooldown_success",
		ExpectedAccountStatus: "temporary_unavailable",
		ExpectedCooldownFence: fence,
		CooldownFence:         fence,
		SourceRevision:        int64Pointer(2),
		Values:                map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano)},
	}
	outcome.CooldownFence = fence
	inserted, err := store.AppendOutcome(ctx, lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("冷却投影 outcome 必须写入: %v %v", inserted, err)
	}
	state, found, err := store.LoadCurrentState(ctx, "acct-store")
	if err != nil || !found || state.CooldownFence == nil {
		t.Fatalf("冷却 fence 必须进入当前状态: %+v %v %v", state, found, err)
	}
}

// TestKeyCursorRoundTrip 覆盖 Key 游标的参数校验与读写闭环。
func TestKeyCursorRoundTrip(t *testing.T) {
	store := wgNewStore(t)
	lease := wgStoreLease(t, store, "wg-cursor")
	ctx := context.Background()
	if _, _, err := store.LoadKeyCursor(ctx, "", "purpose", "fp"); err == nil {
		t.Fatal("空参数查询必须报错")
	}
	if index, found, err := store.LoadKeyCursor(ctx, "acct", "purpose", "fp"); err != nil || found || index != 0 {
		t.Fatalf("缺失游标: %d %v %v", index, found, err)
	}
	if err := store.SaveKeyCursor(ctx, lease, "acct", "purpose", "fp", -1); err == nil {
		t.Fatal("负 index 必须报错")
	}
	if err := store.SaveKeyCursor(ctx, lease, "acct", "purpose", "fp", 4); err != nil {
		t.Fatalf("保存游标: %v", err)
	}
	index, found, err := store.LoadKeyCursor(ctx, "acct", "purpose", "fp")
	if err != nil || !found || index != 4 {
		t.Fatalf("游标读回: %d %v %v", index, found, err)
	}
	// 不同指纹互不干扰。
	if _, found, _ := store.LoadKeyCursor(ctx, "acct", "purpose", "other"); found {
		t.Fatal("不同指纹不得命中")
	}
}

// TestReaderErrorPathsFailClosed 覆盖 PG 直读器在非 PG 句柄上的 fail closed
// （契约预检与候选读取必须显式报错，不静默降级）。
func TestReaderErrorPathsFailClosed(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "biz.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reader, err := NewPostgresDirectInputReader(db, "reader-secret", time.Hour, func() time.Time { return time.Unix(0, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reader.CheckContract(ctx); err == nil {
		t.Fatal("取消的契约预检必须报错")
	}
	if _, err := reader.LoadDue(ctx, 10); err == nil {
		t.Fatal("LoadDue 必须显式报错")
	}
	if _, err := reader.LoadAccount(ctx, "acct"); err == nil {
		t.Fatal("LoadAccount 必须显式报错")
	}
	if _, err := reader.LoadAccountWithFailures(ctx, "acct"); err == nil {
		t.Fatal("LoadAccountWithFailures 必须显式报错")
	}
}

// TestParseDirectBoolMatrix 覆盖布尔列解析。
func TestParseDirectBoolMatrix(t *testing.T) {
	if value, err := parseDirectBool(sql.NullString{}); err != nil || value {
		t.Fatalf("NULL 必须为 false: %v %v", value, err)
	}
	if value, err := parseDirectBool(sql.NullString{Valid: true, String: "1"}); err != nil || !value {
		t.Fatalf("1 必须为 true: %v %v", value, err)
	}
	if value, err := parseDirectBool(sql.NullString{Valid: true, String: "0"}); err != nil || value {
		t.Fatalf("0 必须为 false: %v %v", value, err)
	}
	if _, err := parseDirectBool(sql.NullString{Valid: true, String: "yes"}); err == nil {
		t.Fatal("非法布尔必须报错")
	}
}

// TestParseNullableAndRequiredDirectTime 覆盖时间解析助手。
func TestParseNullableAndRequiredDirectTime(t *testing.T) {
	if value, err := parseNullableDirectTime(sql.NullString{}); err != nil || value != nil {
		t.Fatalf("NULL 必须为 nil: %v %v", value, err)
	}
	if value, err := parseNullableDirectTime(sql.NullString{Valid: true, String: " "}); err != nil || value != nil {
		t.Fatalf("空白必须为 nil: %v %v", value, err)
	}
	if _, err := parseNullableDirectTime(sql.NullString{Valid: true, String: "bad"}); err == nil {
		t.Fatal("非法时间必须报错")
	}
	parsed, err := parseRequiredDirectTime(sql.NullString{Valid: true, String: "2026-09-10T00:00:00Z"}, "expires")
	if err != nil || parsed.IsZero() {
		t.Fatalf("必填时间必须解析: %v %v", parsed, err)
	}
	if _, err := parseRequiredDirectTime(sql.NullString{}, "expires"); err == nil || !strings.Contains(err.Error(), "expires") {
		t.Fatalf("缺必填时间必须报错并带字段名: %v", err)
	}
}
