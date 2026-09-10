package accounthealth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// wgFakeStatsMarker 是 GroupStatsDirtyMarker 的脚本化 Mock（可回放调用与
// 注入失败）。
type wgFakeStatsMarker struct {
	calls  int
	err    error
	reason string
}

func (m *wgFakeStatsMarker) MarkAllGroupAccountStatsDirty(_ context.Context, reason string, _ time.Time) error {
	m.calls++
	m.reason = reason
	return m.err
}

// TestCompareProjectionCursor 覆盖游标比较的三种偏序。
func TestCompareProjectionCursor(t *testing.T) {
	early := &OutcomeCursor{ObservedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), OutcomeID: "a"}
	late := &OutcomeCursor{ObservedAt: time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC), OutcomeID: "a"}
	sameTimeHigherID := &OutcomeCursor{ObservedAt: early.ObservedAt, OutcomeID: "b"}
	if compareProjectionCursor(early, late) >= 0 {
		t.Fatal("早时间必须小于晚时间")
	}
	if compareProjectionCursor(late, early) <= 0 {
		t.Fatal("晚时间必须大于早时间")
	}
	if compareProjectionCursor(early, sameTimeHigherID) >= 0 {
		t.Fatal("同时间按 outcomeId 字典序")
	}
	if compareProjectionCursor(early, early) != 0 {
		t.Fatal("相同游标必须判等")
	}
}

// TestNormalizeProjectionCursorRejects 覆盖游标归一化的拒绝分支。
func TestNormalizeProjectionCursorRejects(t *testing.T) {
	if _, err := normalizeProjectionCursor("2026-09-10T00:00:00Z", "  "); err == nil {
		t.Fatal("缺 outcomeId 必须报错")
	}
	if _, err := normalizeProjectionCursor("2026-09-10T00:00:00Z", string(make([]byte, 4097))); err == nil {
		t.Fatal("超长 outcomeId 必须报错")
	}
	if _, err := normalizeProjectionCursor("not-a-time", "id"); err == nil {
		t.Fatal("非法 observedAt 必须报错")
	}
	cursor, err := normalizeProjectionCursor(" 2026-09-10T00:00:00.500Z ", " id ")
	if err != nil || cursor.ObservedAtText != "2026-09-10T00:00:00.5Z" && cursor.OutcomeID != "id" {
		if err != nil {
			t.Fatalf("合法游标必须通过: %v", err)
		}
	}
}

// TestProjectionDBTimeScan 覆盖可空时间列扫描器的类型分支。
func TestProjectionDBTimeScan(t *testing.T) {
	var value projectionDBTime
	if err := value.Scan(nil); err != nil || value.Valid {
		t.Fatalf("NULL 必须保持无效: %v %v", value.Valid, err)
	}
	stamp := time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)
	scanner := projectionDBTime{}
	if err := scanner.Scan(stamp); err != nil || !scanner.Valid || !scanner.Time.Equal(stamp) {
		t.Fatalf("time.Time 直扫: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan("2026-09-10T08:30:00Z"); err != nil || !scanner.Valid || scanner.RawText != "2026-09-10T08:30:00Z" {
		t.Fatalf("文本扫描必须保留原文: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan([]byte("2026-09-10T08:30:00Z")); err != nil || !scanner.Valid {
		t.Fatalf("字节串扫描: %+v %v", scanner, err)
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan("garbage"); err == nil {
		t.Fatal("非法文本必须报错")
	}
	scanner = projectionDBTime{}
	if err := scanner.Scan(42); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
}

// wgFenceMismatchCase 构造 fenceMismatchReason 真值表用例的账户与 outcome。
func wgFenceMismatchCase(t *testing.T) (*projectionFixture, *OutcomeProjector) {
	t.Helper()
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{})
	return fixture, fixture.projector
}

// TestFenceMismatchReasonTruthTable 锁定 stale 判定的完整真值表（reason
// 文本与归档逐字一致）。
func TestFenceMismatchReasonTruthTable(t *testing.T) {
	fixture, projector := wgFenceMismatchCase(t)
	ctx := context.Background()
	observed := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

	// wgFenceProjection 每次返回全新投影（用例间不得共享可变指针）。
	wgFenceProjection := func() *Projection {
		return &Projection{
			TargetAccountID:       "acct-1",
			TransitionKind:        "health_failure",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "active",
			ExpectedCooldownFence: &CooldownFence{ObservationStartedAt: observed, Generation: "gen-1"},
		}
	}
	baseProjection := wgFenceProjection()
	seedFence := func(sourceID any, observation any, generation any) {
		t.Helper()
		if _, err := fixture.business.Exec(`UPDATE accounts SET authorization_instance_source_account_id = ?, cooldown_retest_observation_started_at = ?, cooldown_retest_generation = ? WHERE id = 'acct-1'`, sourceID, observation, generation); err != nil {
			t.Fatal(err)
		}
	}
	resetAccount := func() {
		t.Helper()
		seedFence(nil, nil, nil)
	}
	runReason := func(t *testing.T, outcome Outcome) string {
		t.Helper()
		tx, err := fixture.business.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		account, err := projector.findAccountFence(ctx, tx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		reason, err := projector.fenceMismatchReason(ctx, tx, account, outcome)
		if err != nil {
			t.Fatal(err)
		}
		return reason
	}
	// 账户缺失。
	missing := baseProjection
	missingOutcome := Outcome{AccountID: "acct-1", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, Projection: missing}
	tx, err := fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	reason, err := projector.fenceMismatchReason(ctx, tx, nil, missingOutcome)
	_ = tx.Rollback()
	if err != nil || reason != "account_missing_or_deleted" {
		t.Fatalf("缺失账户 reason=%q err=%v", reason, err)
	}

	// 版本缺失（schema 有 accounts 但本用例清空 input_versions 行）。
	resetAccount()
	if _, err := fixture.business.Exec(`DELETE FROM account_health_jobs_input_versions WHERE account_id = 'acct-1'`); err != nil {
		t.Fatal(err)
	}
	if got := runReason(t, missingOutcome); got != "input_version_stale" {
		t.Fatalf("缺版本行 reason=%q", got)
	}
	// 重建 input_versions 行（accounts 行仍保留）。
	if _, err := fixture.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES ('acct-1', 1, 1, '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// 版本不匹配。
	staleVersion := missingOutcome
	staleVersion.InputVersion = 9
	if got := runReason(t, staleVersion); got != "input_version_stale" {
		t.Fatalf("版本不匹配 reason=%q", got)
	}
	// config revision 不匹配。
	staleConfig := missingOutcome
	staleConfig.ConfigRevision = 6
	if got := runReason(t, staleConfig); got != "config_revision_stale" {
		t.Fatalf("config 不匹配 reason=%q", got)
	}
	// dispatch revision 不匹配。
	staleDispatch := missingOutcome
	staleDispatch.DispatchRevision = 8
	if got := runReason(t, staleDispatch); got != "dispatch_revision_stale" {
		t.Fatalf("dispatch 不匹配 reason=%q", got)
	}
	// 期望状态不匹配。
	staleStatus := missingOutcome
	staleStatus.Projection.ExpectedAccountStatus = "cooldown"
	if got := runReason(t, staleStatus); got != "expected_account_status_stale" {
		t.Fatalf("状态不匹配 reason=%q", got)
	}
	// source revision 断言但账户无 source（全新投影，避免用例间指针共享）。
	withSource := Outcome{AccountID: "acct-1", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, Projection: wgFenceProjection()}
	withSource.Projection.SourceRevision = int64Pointer(3)
	if got := runReason(t, withSource); got != "source_account_missing" {
		t.Fatalf("缺 source reason=%q", got)
	}
	// source 指向不存在的账户。
	seedFence("acct-ghost", nil, nil)
	if got := runReason(t, withSource); got != "source_account_missing_or_deleted" {
		t.Fatalf("source 缺失 reason=%q", got)
	}
	// source 存在但 revision 不一致（先建行，再改 revision）。
	if _, err := fixture.business.Exec(`INSERT INTO accounts (id, status, config_revision, dispatch_revision, created_at, updated_at) VALUES ('acct-src', 'active', 9, 1, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	wgSeedProjectionSourceAccount(t, fixture, 9)
	seedFence("acct-src", nil, nil)
	if got := runReason(t, withSource); got != "source_config_revision_stale" {
		t.Fatalf("source revision 不匹配 reason=%q", got)
	}
	// source revision 一致 → 进入 cooldown 断言。
	wgSeedProjectionSourceAccount(t, fixture, 3)
	if got := runReason(t, withSource); got != "cooldown_observation_stale" {
		t.Fatalf("observation 不匹配 reason=%q", got)
	}
	// observation 匹配、generation 不匹配。
	seedFence("acct-src", observed.Format(time.RFC3339Nano), "gen-other")
	if got := runReason(t, withSource); got != "cooldown_generation_stale" {
		t.Fatalf("generation 不匹配 reason=%q", got)
	}
	// 全部匹配 → 空 reason。
	seedFence("acct-src", observed.Format(time.RFC3339Nano), "gen-1")
	if got := runReason(t, withSource); got != "" {
		t.Fatalf("全部匹配 reason=%q", got)
	}
	// 无 SourceRevision 断言但账户带 source → source_config_revision_missing。
	noSourceAssertion := Outcome{AccountID: "acct-1", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, Projection: wgFenceProjection()}
	seedFence("acct-src", nil, nil)
	if got := runReason(t, noSourceAssertion); got != "source_config_revision_missing" {
		t.Fatalf("多出的 source reason=%q", got)
	}
}

// TestLockDispatchFamilyRoot 覆盖家族根锁定的三种形态。
func TestLockDispatchFamilyRoot(t *testing.T) {
	fixture, projector := wgFenceMismatchCase(t)
	ctx := context.Background()
	// 授权实例 → source 为根。
	if _, err := fixture.business.Exec(`INSERT INTO accounts (id, status, config_revision, dispatch_revision, created_at, updated_at, authorization_instance_source_account_id) VALUES ('acct-child', 'active', 2, 2, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'acct-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES ('acct-child', 1, 1, '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := projector.lockDispatchFamilyRoot(ctx, tx, "acct-child")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if root != "acct-1" {
		t.Fatalf("家族根必须是 source: %q", root)
	}
	// 无 source → 自身为根。
	tx, err = fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err = projector.lockDispatchFamilyRoot(ctx, tx, "acct-1")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if root != "acct-1" {
		t.Fatalf("独立账户的根是自身: %q", root)
	}
	// 账户缺失 → 空根。
	tx, err = fixture.business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err = projector.lockDispatchFamilyRoot(ctx, tx, "acct-missing")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if root != "" {
		t.Fatalf("缺失账户必须返回空根: %q", root)
	}
}

// TestMarkGroupStatsDirtyMarkerPaths 覆盖 group stats 脏标记的缺席、成功
// 与失败分支。
func TestMarkGroupStatsDirtyMarkerPaths(t *testing.T) {
	fixture := newProjectionFixture(t)
	// stats 缺席 → 静默跳过。
	fixture.projector.markGroupStatsDirty(context.Background(), "acct-1")
	// 注入 marker：成功调用一次。
	marker := &wgFakeStatsMarker{}
	fixture.projector.stats = marker
	fixture.projector.markGroupStatsDirty(context.Background(), "acct-1")
	if marker.calls != 1 || marker.reason != "j1_account_health_projection" {
		t.Fatalf("marker 必须被调用: %+v", marker)
	}
	// marker 失败 → 仅告警不传播。
	marker.err = errors.New("stats 写失败")
	fixture.projector.markGroupStatsDirty(context.Background(), "acct-1")
	if marker.calls != 2 {
		t.Fatalf("失败后不得中断: %d", marker.calls)
	}
}

// TestNullableDBTimeScan 覆盖 store 侧可空时间扫描器的类型分支。
func TestNullableDBTimeScan(t *testing.T) {
	var value nullableDBTime
	if err := value.Scan(nil); err != nil || value.Valid {
		t.Fatalf("NULL 必须保持无效: %v %v", value.Valid, err)
	}
	stamp := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	scanner := nullableDBTime{}
	if err := scanner.Scan(stamp); err != nil || !scanner.Valid || !scanner.Time.Equal(stamp) {
		t.Fatalf("time.Time 直扫: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan("2026-09-10T09:00:00Z"); err != nil || !scanner.Valid {
		t.Fatalf("文本扫描: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan([]byte("2026-09-10T09:00:00Z")); err != nil || !scanner.Valid {
		t.Fatalf("字节串扫描: %+v %v", scanner, err)
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan("garbage"); err == nil {
		t.Fatal("非法文本必须报错")
	}
	scanner = nullableDBTime{}
	if err := scanner.Scan(42); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
}

// wgSeedProjectionSourceAccount 以指定 config_revision 重建 source 账户行。
func wgSeedProjectionSourceAccount(t *testing.T, fixture *projectionFixture, revision int64) {
	t.Helper()
	if _, err := fixture.business.Exec(`UPDATE accounts SET config_revision = ? WHERE id = 'acct-src'`, revision); err != nil {
		t.Fatal(err)
	}
}
