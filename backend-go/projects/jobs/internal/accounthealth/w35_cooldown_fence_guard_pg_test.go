package accounthealth

// w35_cooldown_fence_guard_pg_test.go 以真实 PG 权威 schema 复现并锁定 W35
// 生产缺陷的回归契约（2026-10-27 发布后巡检发现，docs/待办与阻塞事项.md W35）：
//
//	business 冷却锚点由 Go RFC3339Nano 文本写入（纳秒尾 .103999859Z），PG 解析
//	到 µs 时按最近值进位为 .104000；投影 ExpectedCooldownFence 以 µs 文本携带
//	同一物理锚点（.103999Z，纳秒尾截断）。修复前的 SQL fence 守卫对两侧分别
//	date_trunc('milliseconds') 再比较：列侧 .104 vs 参数侧 .103，同一物理锚点
//	被永久拒绝——cooldown_success 复活投影每轮 stale（生产 receipt
//	projection_compare_and_set_missed 连发），账户永不复活、冷却复测无限循环
//	（生产两账户 ~8,200 次成功上游调用/天）。
//
// 修复后守卫为 ±2ms 容差窗（毫秒级结构性漂移上限 1ms；generation 相等才是
// 强身份键，Go 侧 fenceMismatchReason 已先行做 floor-ms 等价校验）。本测试
// 以生产字节形状断言复活投影 applied 且账户行复活清锚；负例臂断言真实换锚
// （generation 变化）仍被拒绝。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

const (
	// w35AnchorNanoText 是生产 business 锚点写入形状（Go RFC3339Nano，纳秒尾
	// .103999859 在 PG µs 解析时进位为 .104000——毫秒守卫假阴性的触发前提）。
	w35AnchorNanoText = "2026-10-24T17:08:07.103999859Z"
	// w35AnchorMicroText 是投影 fence 携带的同一物理锚点（µs 文本，纳秒尾截断）。
	w35AnchorMicroText = "2026-10-24T17:08:07.103999Z"
	// w35Generation 与生产冻结账户一致（outcome id 形态）。
	w35Generation = "outcome-7f67d939c05ae48f165558a9fe2ea67e"
	w35AccountID  = "w35-ns-acct"
)

// w35SeedFrozenAccountDB 按生产冻结形态种入账户行：temporary_unavailable +
// 冷却已过期 + 纳秒文本锚点 + input_versions 当前版本 1。
func w35SeedFrozenAccountDB(t *testing.T, db *sql.DB, generation string) {
	t.Helper()
	envelope, err := exactkeyprobe.EncryptV1Envelope("w35-fence-guard-secret", []byte(`{"api_key":"sk-w35"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (
		id, system_account_id, vendor_code, connection_driver_code, oauth_type, protocol_code, protocol_version,
		name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
		schedulable, concurrency_limit, priority, super_priority_enabled, fallback_enabled,
		health_check_model, health_check_endpoint_mode,
		cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation,
		created_at, updated_at, config_revision, dispatch_revision
	) VALUES (
		'`+w35AccountID+`', 'sys_admin', 'deepseek', 'deepseek', NULL, 'openai', 'v1',
		'`+w35AccountID+`', 'api_key', 'temporary_unavailable', $1, 'w35-fp', 'w35-mask',
		1, 0, 0, 0, 0,
		'w35-model', 'chat_json',
		'2026-10-24T17:08:11Z', $2, $3,
		'2026-10-24T17:00:00Z', '2026-10-24T17:08:07Z', 8, 6
	)`, envelope, w35AnchorNanoText, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_jobs_input_versions (account_id, current_version, reserved_at)
		VALUES ('` + w35AccountID + `', 1, '2026-10-24T17:00:00Z') ON CONFLICT (account_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
}

// w35RevivalOutcome 构造与生产 payload 同形的 cooldown_success 复活 outcome
// （fence 时间来自 µs 文本，与锚点纳秒文本同物理锚点）。
func w35RevivalOutcome(outcomeID string) Outcome {
	observationStarted, err := time.Parse(time.RFC3339Nano, w35AnchorMicroText)
	if err != nil {
		panic(err)
	}
	observed := observationStarted.Add(4*time.Minute + 29*time.Second)
	nextDue := observed.Add(time.Hour)
	return Outcome{
		OutcomeID: outcomeID, RequestID: outcomeID + "-request", AccountID: w35AccountID,
		Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: observed,
		InputVersion: 1, ConfigRevision: 8, DispatchRevision: 6, StatusCode: 200,
		AccountStatus: "active", NextDueAt: &nextDue,
		Projection: &Projection{
			TargetAccountID: w35AccountID, TransitionKind: "cooldown_success",
			InputVersion: 1, ConfigRevision: 8, DispatchRevision: 6,
			ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: &exactkeyprobe.CooldownFence{ObservationStartedAt: observationStarted, Generation: w35Generation},
			Values: map[string]any{
				"last_health_check_at":          observed.Format(time.RFC3339Nano),
				"last_health_success_at":        observed.Format(time.RFC3339Nano),
				"last_health_check_status_code": 200,
			},
		},
	}
}

// TestW35CooldownSuccessRevivalSurvivesNanosecondAnchorPrecisionDrift 是主回
// 归：纳秒锚点 × µs fence 必须命中 ±2ms 容差窗，复活投影 applied、账户行复活
// 清锚（修复前同形状每轮 stale/projection_compare_and_set_missed）。
func TestW35CooldownSuccessRevivalSurvivesNanosecondAnchorPrecisionDrift(t *testing.T) {
	db, _ := w9hOpenKitDB(t)
	w35SeedFrozenAccountDB(t, db, w35Generation)
	outcome := w35RevivalOutcome("w35-outcome-revival")
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatal(err)
	}
	now := outcome.ObservedAt.Add(time.Second)
	projector, err := NewOutcomeProjector(&Store{}, OutcomeProjectorConfig{
		Business:         business,
		CredentialSecret: "w35-fence-guard-secret",
		Logger:           nil,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := projector.projectOutcome(context.Background(), outcome)
	if err != nil {
		t.Fatalf("projectOutcome 执行失败: %v", err)
	}
	if result.Disposition != ProjectionApplied {
		t.Fatalf("纳秒锚点 × µs fence 的复活投影应 applied（修复前同形状生产连发 stale），实际 %s reason=%q", result.Disposition, result.Reason)
	}
	var status string
	var cooldownUntil, observation, generation any
	if err := db.QueryRow(`SELECT status, cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation FROM juhe_business.accounts WHERE id = $1`, w35AccountID).Scan(&status, &cooldownUntil, &observation, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("复活后账户应 active，实际 %s", status)
	}
	if cooldownUntil != nil || observation != nil || generation != nil {
		t.Fatalf("复活应清空冷却锚点，实际 cooldown_until=%v observation=%v generation=%v", cooldownUntil, observation, generation)
	}
}

// TestW35FenceGuardStillRejectsGenerationChange 断言容差窗不放松强身份键：
// generation 变化（真实换锚）仍以 cooldown_generation_stale 拒绝。
func TestW35FenceGuardStillRejectsGenerationChange(t *testing.T) {
	db, _ := w9hOpenKitDB(t)
	w35SeedFrozenAccountDB(t, db, "outcome-different-generation")
	outcome := w35RevivalOutcome("w35-outcome-stale")
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatal(err)
	}
	now := outcome.ObservedAt.Add(time.Second)
	projector, err := NewOutcomeProjector(&Store{}, OutcomeProjectorConfig{
		Business:         business,
		CredentialSecret: "w35-fence-guard-secret",
		Logger:           nil,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := projector.projectOutcome(context.Background(), outcome)
	if err != nil {
		t.Fatalf("projectOutcome 执行失败: %v", err)
	}
	if result.Disposition != ProjectionStale || result.Reason != "cooldown_generation_stale" {
		t.Fatalf("generation 变化应 stale/cooldown_generation_stale，实际 %s reason=%q", result.Disposition, result.Reason)
	}
}
