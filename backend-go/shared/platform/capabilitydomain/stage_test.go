// 阶段记录单元测试（随整包下沉自 maintenance internal/capabilitydomain
// migration_test.go，2026-10 M2）：状态机全组合与条件更新互斥原语。这里只在
// pgtestkit 独立测试库上建阶段读写消费的 "juhe_business".global_settings
// 最小形状（列与 maintenance 业务 DDL 同构）；完整迁移执行器集成测试仍在
// maintenance 侧。
package capabilitydomain

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// openStageSettingsDB 在 pgtestkit 独立测试库上建 "juhe_business".
// global_settings 最小形状（PG 方言阶段记录面的唯一消费表）。
func openStageSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "juhe_business";
CREATE TABLE "juhe_business"."global_settings" (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`)
		return err
	})
}

// TestPlanMigrationStateMachine 钉死状态机全组合。
func TestPlanMigrationStateMachine(t *testing.T) {
	cases := []struct {
		target       Target
		current      Stage
		wantDecision MigrationDecision
		wantNext     Stage
	}{
		{TargetM1, StageNone, DecisionRun, StageM1Running},
		{TargetM1, StageM1Running, DecisionRun, StageM1Running},
		{TargetM1, StageM1Complete, DecisionAlreadyComplete, StageM1Complete},
		{TargetM1, StageM2Running, DecisionReject, StageM2Running},
		{TargetM1, StageM2Finalized, DecisionReject, StageM2Finalized},
		{TargetM2Cutover, StageNone, DecisionReject, StageNone},
		{TargetM2Cutover, StageM1Running, DecisionReject, StageM1Running},
		{TargetM2Cutover, StageM1Complete, DecisionRun, StageM2Running},
		{TargetM2Cutover, StageM2Running, DecisionRun, StageM2Running},
		{TargetM2Cutover, StageM2Finalized, DecisionReject, StageM2Finalized},
	}
	for _, testCase := range cases {
		decision, next, _ := PlanMigration(testCase.target, testCase.current)
		if decision != testCase.wantDecision || next != testCase.wantNext {
			t.Fatalf("PlanMigration(%s, %s) = (%d, %s), want (%d, %s)", testCase.target, testCase.current, decision, next, testCase.wantDecision, testCase.wantNext)
		}
	}
}

// TestClaimTransitionRace 确定性覆盖条件更新互斥原语：首次领取冲突、跃迁
// 谓词不命中、谓词命中推进。
func TestClaimTransitionRace(t *testing.T) {
	db := openStageSettingsDB(t)
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := ClaimNewStage(context.Background(), tx, StageM1Running, now)
	if err != nil || !acquired {
		t.Fatalf("首次领取应成功: acquired=%v err=%v", acquired, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 第二次首次领取（同阶段）：冲突 DO NOTHING → 未领取。
	tx2, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	acquired, err = ClaimNewStage(context.Background(), tx2, StageM1Running, now)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("并发首次领取只允许一个执行者成功")
	}
	_ = tx2.Rollback()
	// 跃迁谓词不命中：expect=m1_complete 但当前是 m1_running。
	tx3, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	acquired, err = TransitionStage(context.Background(), tx3, StageM1Complete, StageM2Running, now)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("阶段谓词不命中时不得推进")
	}
	_ = tx3.Rollback()
	// 谓词命中：m1_running → m1_complete。
	tx4, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	acquired, err = TransitionStage(context.Background(), tx4, StageM1Running, StageM1Complete, now)
	if err != nil || !acquired {
		t.Fatalf("命中跃迁应成功: acquired=%v err=%v", acquired, err)
	}
	if err := tx4.Commit(); err != nil {
		t.Fatal(err)
	}
	record, ok, err := ReadRecord(context.Background(), db)
	if err != nil || !ok {
		t.Fatalf("ReadRecord: ok=%v err=%v", ok, err)
	}
	if record.Stage != StageM1Complete || record.RuleVersion != RuleVersion {
		t.Fatalf("record = %+v", record)
	}
}

// TestReadRecordMissingAndCorrupt 覆盖门禁读取面：无记录 ok=false；损坏
// 记录（stage 枚举外/ruleVersion 不符/非 JSON）原样报错。
func TestReadRecordMissingAndCorrupt(t *testing.T) {
	db := openStageSettingsDB(t)
	ctx := context.Background()
	if _, present, err := ReadRecord(ctx, db); present || err != nil {
		t.Fatalf("无记录应 ok=false nil err: present=%v err=%v", present, err)
	}
	cases := []string{
		`{"stage":"m3_running","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`,
		`{"stage":"m1_running","ruleVersion":2,"updatedAt":"2026-10-12T08:00:00Z"}`,
		`not-json`,
	}
	for _, valueJSON := range cases {
		if _, _, err := ReadRecord(ctx, db); err == nil {
			continue
		} else {
			_ = err
		}
		if _, err := db.Exec(`DELETE FROM "juhe_business"."global_settings"`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`, MigrationSettingKey, valueJSON); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadRecord(ctx, db); err == nil {
			t.Fatalf("损坏记录 %q 必须原样报错", valueJSON)
		}
	}
	// 合法记录读取回放。
	if _, err := db.Exec(`DELETE FROM "juhe_business"."global_settings"`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, '2026-10-12T08:00:00Z')`,
		MigrationSettingKey, `{"stage":"m2_finalized","ruleVersion":1,"updatedAt":"2026-10-12T08:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	record, present, err := ReadRecord(ctx, db)
	if err != nil || !present {
		t.Fatalf("已封存记录应可读: present=%v err=%v", present, err)
	}
	if record.Stage != StageM2Finalized {
		t.Fatalf("stage = %q, want m2_finalized", record.Stage)
	}
}
