package accounthealth

import (
	"context"
	"encoding/json"
	"path/filepath"
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

// TestW12dValidateProjectionTruthTable 逐条锁定 validateProjection 的拒绝分支。
func TestW12dValidateProjectionTruthTable(t *testing.T) {
	base := Outcome{
		OutcomeID: "w12d-outcome", RequestID: "w12d-request", AccountID: "w12d-acc",
		Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow,
		InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(time.Hour)),
		Projection: &Projection{
			TargetAccountID: "w12d-acc", TransitionKind: "activation_success",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "pending_test",
		},
	}
	fence := &CooldownFence{ObservationStartedAt: projectionFixtureNow, Generation: "gen-w12d"}
	cases := []struct {
		name     string
		mutate   func(*Outcome)
		dispose  ProjectionDisposition
		reason   string
		terminal bool
	}{
		{
			name: "no projection", terminal: true, dispose: ProjectionIgnored, reason: "outcome_has_no_account_projection",
			mutate: func(o *Outcome) { o.Projection = nil },
		},
		{
			name: "unknown transition", terminal: true, dispose: ProjectionRejected, reason: "projection_transition_not_allowed",
			mutate: func(o *Outcome) { o.Projection.TransitionKind = "teleport" },
		},
		{
			name: "top level fence mismatch", terminal: true, dispose: ProjectionRejected, reason: "projection_top_level_fence_mismatch",
			mutate: func(o *Outcome) { o.Projection.TargetAccountID = "other" },
		},
		{
			name: "expected status missing", terminal: true, dispose: ProjectionRejected, reason: "projection_expected_account_status_missing",
			mutate: func(o *Outcome) { o.Projection.ExpectedAccountStatus = " " },
		},
		{
			name: "value key not allowed", terminal: true, dispose: ProjectionRejected, reason: "projection_value_not_allowed:rogue",
			mutate: func(o *Outcome) { o.Projection.Values = map[string]any{"rogue": 1} },
		},
		{
			name: "activation expected status invalid", terminal: true, dispose: ProjectionRejected, reason: "projection_activation_expected_status_invalid",
			mutate: func(o *Outcome) { o.Projection.ExpectedAccountStatus = "active" },
		},
		{
			name: "health success expected status invalid", terminal: true, dispose: ProjectionRejected, reason: "projection_active_expected_status_invalid",
			mutate: func(o *Outcome) {
				o.Projection.TransitionKind = "health_success"
				o.Projection.ExpectedAccountStatus = "pending_test"
			},
		},
		{
			name: "temporary_unavailable expected status invalid", terminal: true, dispose: ProjectionRejected, reason: "projection_active_expected_status_invalid",
			mutate: func(o *Outcome) {
				o.Projection.TransitionKind = "temporary_unavailable"
				o.Projection.ExpectedAccountStatus = "pending_test"
			},
		},
		{
			name: "health failure expected status invalid", terminal: true, dispose: ProjectionRejected, reason: "projection_health_failure_expected_status_invalid",
			mutate: func(o *Outcome) {
				o.Projection.TransitionKind = "health_failure"
				o.Projection.ExpectedAccountStatus = "active_x"
			},
		},
		{
			name: "cooldown expected status invalid", terminal: true, dispose: ProjectionRejected, reason: "projection_cooldown_expected_status_invalid",
			mutate: func(o *Outcome) {
				o.Projection.TransitionKind = "cooldown_success"
				o.Projection.ExpectedAccountStatus = "active"
			},
		},
		{
			name: "next due missing", terminal: true, dispose: ProjectionRejected, reason: "projection_next_due_missing",
			mutate: func(o *Outcome) { o.NextDueAt = nil },
		},
		{
			name: "activation error next due optional", terminal: false,
			mutate: func(o *Outcome) {
				o.NextDueAt = nil
				o.Outcome = OutcomeNeutral
				o.Projection.TransitionKind = "activation_error"
			},
		},
		{
			name: "output cooldown fence missing", terminal: true, dispose: ProjectionRejected, reason: "projection_output_cooldown_fence_missing",
			mutate: func(o *Outcome) {
				o.Projection.TransitionKind = "temporary_unavailable"
				o.Projection.ExpectedAccountStatus = "active"
			},
		},
		{
			name: "expected cooldown fence missing", terminal: true, dispose: ProjectionRejected, reason: "projection_expected_cooldown_fence_missing",
			mutate: func(o *Outcome) {
				o.Outcome = OutcomeUpstreamFailed
				o.Projection.TransitionKind = "cooldown_failure"
				o.Projection.ExpectedAccountStatus = "temporary_unavailable"
				o.Projection.CooldownFence = fence
			},
		},
		{
			name: "cooldown fence mismatch", terminal: true, dispose: ProjectionRejected, reason: "projection_cooldown_fence_mismatch",
			mutate: func(o *Outcome) {
				o.Outcome = OutcomeUpstreamFailed
				o.Projection.TransitionKind = "cooldown_defer"
				o.Projection.ExpectedAccountStatus = "temporary_unavailable"
				o.Projection.ExpectedCooldownFence = fence
				o.Projection.CooldownFence = &CooldownFence{ObservationStartedAt: projectionFixtureNow.Add(time.Second), Generation: "gen-other"}
			},
		},
		{
			name: "expected cooldown source fence mismatch", terminal: true, dispose: ProjectionRejected, reason: "projection_expected_cooldown_source_fence_mismatch",
			mutate: func(o *Outcome) {
				o.Outcome = OutcomeUpstreamFailed
				o.Projection.TransitionKind = "cooldown_error"
				o.Projection.ExpectedAccountStatus = "temporary_unavailable"
				o.Projection.SourceRevision = ptrInt64(3)
				o.Projection.ExpectedCooldownFence = fence
				o.Projection.CooldownFence = fence
			},
		},
		{
			name: "output cooldown source fence mismatch", terminal: true, dispose: ProjectionRejected, reason: "projection_output_cooldown_source_fence_mismatch",
			mutate: func(o *Outcome) {
				// 该分支在 cooldown_ 前缀 transition 下被同 fence 检查短路；
				// 改用非 cooldown transition 携带输出 fence，仅输出 fence 的
				// 源 revision 漂移。
				o.Projection.TransitionKind = "health_success"
				o.Projection.ExpectedAccountStatus = "active"
				o.Projection.SourceRevision = ptrInt64(3)
				o.Projection.CooldownFence = &CooldownFence{ObservationStartedAt: projectionFixtureNow, Generation: "gen-w12d", SourceConfigRevision: ptrInt64(4)}
			},
		},
		{
			name: "outcome transition mismatch", terminal: true, dispose: ProjectionRejected, reason: "projection_outcome_transition_mismatch",
			mutate: func(o *Outcome) {
				o.Outcome = OutcomeNeutral
				o.Projection.TransitionKind = "health_success"
				o.Projection.ExpectedAccountStatus = "active"
			},
		},
		{
			name: "valid cooldown defer", terminal: false,
			mutate: func(o *Outcome) {
				o.Outcome = OutcomeNeutral
				o.Projection.TransitionKind = "cooldown_defer"
				o.Projection.ExpectedAccountStatus = "temporary_unavailable"
				o.Projection.ExpectedCooldownFence = fence
				o.Projection.CooldownFence = fence
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 深拷贝 Projection，避免用例间共享指针互相污染。
			projection := *base.Projection
			outcome := base
			outcome.Projection = &projection
			if tc.mutate != nil {
				tc.mutate(&outcome)
			}
			validation := validateProjection(outcome)
			if validation.terminal != tc.terminal {
				t.Fatalf("terminal=%t validation=%+v", validation.terminal, validation)
			}
			if !tc.terminal {
				return
			}
			if validation.disposition != tc.dispose || validation.reason != tc.reason {
				t.Fatalf("disposition=%q reason=%q want %q/%q", validation.disposition, validation.reason, tc.dispose, tc.reason)
			}
		})
	}
}

// TestW12dProjectionPureHelpers 覆盖剩余纯函数分支。
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

// TestW12dListProjectedOutcomesArms 覆盖 limit 校验与 payload 类型分支。
func TestW12dListProjectedOutcomesArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	if _, err := fixture.store.listProjectedOutcomes(context.Background(), nil, 0); err == nil {
		t.Fatal("limit<1 必须报错")
	}
	outcome := Outcome{OutcomeID: "w12d-list", RequestID: "w12d-list-r", AccountID: "w12d-acc", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7}
	payload, _ := json.Marshal(outcome)
	if _, err := fixture.store.db.Exec(`INSERT INTO account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, payload)
VALUES ('w12d-list', 'r', 'a', 'success', '2026-09-06T12:00:00Z', 1, 5, 7, NULL, NULL, NULL, ?)`, string(payload)); err != nil {
		t.Fatal(err)
	}
	page, err := fixture.store.listProjectedOutcomes(context.Background(), nil, 10)
	if err != nil || len(page) != 1 || page[0].Outcome.OutcomeID != "w12d-list" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page[0].StorageObservedAt.IsZero() {
		t.Fatal("observed time must resolve")
	}
	// payload 列类型分支：数字 payload 走解码失败路径（default 类型分支为
	// PG/驱动差异守卫，SQLite 驱动不可达）。
	if _, err := fixture.store.db.Exec(`UPDATE account_health_outcomes SET payload = 3 WHERE outcome_id = 'w12d-list'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.listProjectedOutcomes(context.Background(), nil, 10); err == nil {
		t.Fatalf("non json payload must fail")
	}
}

// TestW12dLoadCursorArms 覆盖游标损坏与重锚定分支。
func TestW12dLoadCursorArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	// 半游标（observed_at 有值、outcome_id NULL）受 fixture CHECK 约束保护，
	// 无法注入；该损坏检测分支登记为防御性不可达（DB CHECK 是第一道防线）。
	// 全 NULL 游标视同缺失。
	if _, err := fixture.business.Exec(`INSERT INTO account_health_projection_cursors (consumer_key, observed_at, outcome_id, updated_at) VALUES (?, NULL, NULL, '2026-09-06T12:00:00Z')`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	cursor, err := fixture.projector.loadCursor(ctx)
	if err != nil || cursor != nil {
		t.Fatalf("null cursor: %+v %v", cursor, err)
	}
	if _, err := fixture.business.Exec(`DELETE FROM account_health_projection_cursors WHERE consumer_key = ?`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	// 合法游标 + 存储 outcome 缺失 → 保持原游标。
	if _, err := fixture.business.Exec(`INSERT INTO account_health_projection_cursors (consumer_key, observed_at, outcome_id, updated_at) VALUES (?, '2026-09-06T12:00:00Z', 'w12d-gone', '2026-09-06T12:00:00Z')`, DefaultProjectionConsumerKey); err != nil {
		t.Fatal(err)
	}
	cursor, err = fixture.projector.loadCursor(ctx)
	if err != nil || cursor == nil || cursor.OutcomeID != "w12d-gone" {
		t.Fatalf("keep cursor: %+v %v", cursor, err)
	}
}

// TestW12dAdvanceCursorArms 覆盖游标推进的首次/越过/同位规范化分支。
func TestW12dAdvanceCursorArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	late := &OutcomeCursor{ObservedAt: projectionFixtureNow.Add(time.Minute), ObservedAtText: "2026-09-06T12:01:00Z", OutcomeID: "w12d-late"}
	early := &OutcomeCursor{ObservedAt: projectionFixtureNow, ObservedAtText: "2026-09-06T12:00:00Z", OutcomeID: "w12d-early"}
	// 首次写入。
	advanced, err := fixture.projector.advanceCursor(ctx, late)
	if err != nil || !advanced {
		t.Fatalf("first advance: %t %v", advanced, err)
	}
	// 越过旧游标 → 拒绝推进。
	advanced, err = fixture.projector.advanceCursor(ctx, early)
	if err != nil || advanced {
		t.Fatalf("stale advance must be rejected: %t %v", advanced, err)
	}
	// 同 outcome 同时刻、文本精度不同 → 一次性规范化更新。
	canonical := &OutcomeCursor{ObservedAt: late.ObservedAt, ObservedAtText: "2026-09-06T12:01:00.000Z", OutcomeID: "w12d-late"}
	advanced, err = fixture.projector.advanceCursor(ctx, canonical)
	if err != nil || !advanced {
		t.Fatalf("canonical precision advance: %t %v", advanced, err)
	}
	var observedAt, outcomeID string
	if err := fixture.business.QueryRow(`SELECT observed_at, outcome_id FROM account_health_projection_cursors WHERE consumer_key = ?`, DefaultProjectionConsumerKey).Scan(&observedAt, &outcomeID); err != nil {
		t.Fatal(err)
	}
	if observedAt != "2026-09-06T12:01:00.000Z" || outcomeID != "w12d-late" {
		t.Fatalf("canonical cursor: %q %q", observedAt, outcomeID)
	}
}

// TestW12dProjectOutcomeExistingReceipt 命中既有 receipt 时必须幂等返回。
func TestW12dProjectOutcomeExistingReceipt(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()
	fixture.seedAccount(t, map[string]any{"id": "w12d-acc", "status": "active", "config_revision": int64(5), "dispatch_revision": int64(7)})
	w12dSeedInputVersion(t, fixture, "w12d-acc", 1)
	if _, err := fixture.business.Exec(`INSERT INTO account_health_projection_receipts (outcome_id, account_id, input_version, disposition, reason, applied_at) VALUES ('w12d-receipted', 'w12d-acc', 1, 'stale', 'config_revision_stale', '2026-09-06T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{OutcomeID: "w12d-receipted", RequestID: "r", AccountID: "w12d-acc", Outcome: OutcomeUpstreamFailed, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7}
	result, err := fixture.projector.projectOutcome(ctx, outcome)
	if err != nil {
		t.Fatalf("projectOutcome: %v", err)
	}
	if result.Disposition != ProjectionStale || result.Reason != "config_revision_stale" {
		t.Fatalf("receipt replay: %+v", result)
	}
}

// TestW12dProjectOutcomeTerminalReceipt 无投影 outcome 落 ignored receipt 并推进游标。
func TestW12dProjectOutcomeTerminalReceipt(t *testing.T) {
	fixture := newProjectionFixture(t)
	outcome := Outcome{OutcomeID: "w12d-ignored", RequestID: "r", AccountID: "w12d-acc", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("processed=%d", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "w12d-ignored")
	if disposition != "ignored" || reason != "outcome_has_no_account_projection" {
		t.Fatalf("receipt=%s/%s", disposition, reason)
	}
}

// TestW12dCASGuardSourceRevision 覆盖 CAS UPDATE 的 SourceRevision EXISTS 守卫。
func TestW12dCASGuardSourceRevision(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"id": "w12d-src", "status": "active", "config_revision": int64(5), "dispatch_revision": int64(7)})
	// 直接插授权实例行（seedAccount 会重复写 acct-1 的 input version 行）。
	if _, err := fixture.business.Exec(`INSERT INTO accounts (id, status, config_revision, dispatch_revision, authorization_instance_source_account_id, created_at, updated_at) VALUES ('w12d-inst', 'pending_test', 5, 7, 'w12d-src', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	w12dSeedInputVersion(t, fixture, "w12d-src", 1)
	w12dSeedInputVersion(t, fixture, "w12d-inst", 1)
	outcome := Outcome{OutcomeID: "w12d-cas-src", RequestID: "r", AccountID: "w12d-inst", Outcome: OutcomeSuccess, ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(time.Hour)),
		Projection: &Projection{
			TargetAccountID: "w12d-inst", TransitionKind: "activation_success",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "pending_test", SourceRevision: ptrInt64(5),
		}}
	fixture.insertOutcome(projectionFixtureNow, outcome)
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("processed=%d", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "w12d-cas-src")
	if disposition != "applied" || reason != "" {
		t.Fatalf("receipt=%s/%s", disposition, reason)
	}
	var revision int64
	if err := fixture.business.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w12d-inst'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 8 {
		t.Fatalf("authorization instance must advance self only: revision=%d", revision)
	}
	// 授权实例账户不推进源账户。
	if err := fixture.business.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w12d-src'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("source must stay: revision=%d", revision)
	}
}

// TestW12dApplyProjectionUpdateArms 直接驱动 CAS 更新臂：守卫缺失报错、
// guard 不匹配时 RowsAffected=0 视为 stale。
func TestW12dApplyProjectionUpdateArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"id": "w12d-cas", "status": "active", "config_revision": int64(5), "dispatch_revision": int64(7)})
	w12dSeedInputVersion(t, fixture, "w12d-cas", 1)
	ctx := context.Background()
	// (a) 输出 cooldown fence 缺失的防御守卫（validateProjection 已拒绝该形态）。
	tx, err := fixture.business.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	missingFence := Outcome{
		OutcomeID: "w12d-guard", RequestID: "r", AccountID: "w12d-cas", Outcome: OutcomeNeutral,
		ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		Projection: &Projection{TargetAccountID: "w12d-cas", TransitionKind: "temporary_unavailable", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, ExpectedAccountStatus: "active"},
	}
	account, err := fixture.projector.findAccountFence(ctx, tx, "w12d-cas")
	if err != nil || account == nil {
		t.Fatalf("fence: %+v %v", account, err)
	}
	_, err = fixture.projector.applyProjectionUpdate(ctx, tx, missingFence, account, activationProjectionPlan{status: "temporary_unavailable"})
	if err == nil || !strings.Contains(err.Error(), "缺少输出 cooldown fence") {
		t.Fatalf("missing fence guard: %v", err)
	}
	// (b) fence 期望与库内行不一致 → CAS miss。
	staleOutcome := Outcome{
		OutcomeID: "w12d-cas-miss", RequestID: "r", AccountID: "w12d-cas", Outcome: OutcomeUpstreamFailed,
		ObservedAt: projectionFixtureNow, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(projectionFixtureNow.Add(10 * time.Minute)),
		Projection: &Projection{
			TargetAccountID: "w12d-cas", TransitionKind: "health_failure",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "pending_test", // 库内实际 active → guard 失败
		},
	}
	applied, err := fixture.projector.applyProjectionUpdate(ctx, tx, staleOutcome, account, activationProjectionPlan{status: "active"})
	if err != nil || applied {
		t.Fatalf("cas miss: applied=%t err=%v", applied, err)
	}
	// (c) 全 fence 匹配 → CAS 命中。
	account, err = fixture.projector.findAccountFence(ctx, tx, "w12d-cas")
	if err != nil || account == nil {
		t.Fatalf("fence reload: %+v %v", account, err)
	}
	staleOutcome.Projection.ExpectedAccountStatus = "active"
	applied, err = fixture.projector.applyProjectionUpdate(ctx, tx, staleOutcome, account, activationProjectionPlan{status: "active"})
	if err != nil || !applied {
		t.Fatalf("cas hit: applied=%t err=%v", applied, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestW12dReplayAndFamilyArms 覆盖 outbox replay 幂等、身份冲突与家族推进错误臂。
func TestW12dReplayAndFamilyArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"id": "w12d-fam", "status": "temporary_unavailable", "config_revision": int64(5), "dispatch_revision": int64(7), "cooldown_until": "2026-09-06T13:00:00Z", "cooldown_retest_observation_started_at": "2026-09-06T12:00:00Z", "cooldown_retest_generation": "gen-w12d", "cooldown_retest_failure_count": int64(1)})
	w12dSeedInputVersion(t, fixture, "w12d-fam", 1)
	// 预置两条 replay 行：dedupe 相同 + 身份一致（幂等命中）、身份不一致（冲突）。
	_, err := fixture.business.Exec(`INSERT INTO account_circuit_outbox (event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key, circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
VALUES ('w12d-event-a', 'account_circuit_runtime_v1', 'dispatch:dispatch-w12d-t1', 'dispatch_revision_changed', 'w12d-fam', 'w12d-fam', NULL, NULL, 't-a', 7, NULL, NULL, 'pending', 1, 0, 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.business.Exec(`INSERT INTO account_circuit_outbox (event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key, circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms)
VALUES ('w12d-event-b', 'account_circuit_runtime_v1', 'dispatch:dispatch-w12d-t2', 'incident_changed', 'w12d-fam', 'w12d-fam', 'scope', 'inc-1', 't-b', 7, 1, 1, 'pending', 1, 0, 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.business.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// replay 身份一致 → 幂等命中不推进。
	if err := fixture.projector.advanceDispatchRevision(context.Background(), tx, "w12d-fam", "dispatch-w12d-t1"); err != nil {
		t.Fatalf("replay hit: %v", err)
	}
	// replay 身份冲突 → 报错。
	err = fixture.projector.advanceDispatchRevision(context.Background(), tx, "w12d-fam", "dispatch-w12d-t2")
	if err == nil || !strings.Contains(err.Error(), "replay identity conflict") {
		t.Fatalf("replay conflict: %v", err)
	}
	// advanceFamilyOrSelfDispatch / advanceDispatchRevisionFamily 的 ErrNoRows 分支。
	err = fixture.projector.advanceFamilyOrSelfDispatch(context.Background(), tx, "w12d-missing", "t")
	if err == nil || !strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("family self missing: %v", err)
	}
	err = fixture.projector.advanceDispatchRevisionFamily(context.Background(), tx, "w12d-missing", "t")
	if err == nil || !strings.Contains(err.Error(), "AI 账户不存在") {
		t.Fatalf("family root missing: %v", err)
	}
	// 家族根存在 → 推进自身 revision（无实例行）。
	if err := fixture.projector.advanceDispatchRevisionFamily(context.Background(), tx, "w12d-fam", "t-x"); err != nil {
		t.Fatalf("family advance: %v", err)
	}
	var revision int64
	if err := tx.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'w12d-fam'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 8 {
		t.Fatalf("revision=%d", revision)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestW12dProjectorConfigArms 覆盖构造器校验臂与默认值。
func TestW12dProjectorConfigArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-cfg.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := NewOutcomeProjector(nil, OutcomeProjectorConfig{}); err == nil {
		t.Fatal("nil store must fail")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{}); err == nil {
		t.Fatal("nil business must fail")
	}
	if handle, err := NewProjectionBusinessDB(nil, false); err == nil || handle != nil {
		t.Fatal("nil db handle must fail")
	}
	business, err := NewProjectionBusinessDB(store.db, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business}); err == nil {
		t.Fatal("missing secret must fail")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business, CredentialSecret: "s", PollInterval: 50 * time.Millisecond}); err == nil {
		t.Fatal("poll too small must fail")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business, CredentialSecret: "s", PollInterval: 2 * time.Minute}); err == nil {
		t.Fatal("poll too large must fail")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business, CredentialSecret: "s", BatchSize: 2000}); err == nil {
		t.Fatal("batch too large must fail")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business, CredentialSecret: "s", ConsumerKey: strings.Repeat("k", 201)}); err == nil {
		t.Fatal("consumer key too long must fail")
	}
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: business, CredentialSecret: "s", PollInterval: 0, BatchSize: 0})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if projector.poll != DefaultProjectionPollInterval || projector.batch != DefaultProjectionBatchSize {
		t.Fatalf("defaults poll=%s batch=%d", projector.poll, projector.batch)
	}
}

// w12dSeedInputVersion 为 seedAccount 建的账户补 input version 行（fixture
// 自身只写 acct-1 固定 id）。
func w12dSeedInputVersion(t *testing.T, fixture *projectionFixture, accountID string, version int64) {
	t.Helper()
	if _, err := fixture.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES (?, 1, ?, '2026-09-01T00:00:00Z')
ON CONFLICT (account_id, input_version) DO UPDATE SET current_version = excluded.current_version`, accountID, version); err != nil {
		t.Fatalf("写入 w12d input version 失败: %v", err)
	}
}

func ptrInt64(value int64) *int64 { return &value }
