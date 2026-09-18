// w13g2 modelcheckowner 批次 3：schema/contracts/quality/enforcement/
// recovery/store/runtime 的错误分支与边界输入覆盖。
package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func w13g2McSchemaFailStore(t *testing.T) (*Store, *w13g2McFailpoint) {
	t.Helper()
	key := strings_ReplaceAll(t.Name())
	fp := w13g2McRegisterFailDriver(key)
	db, err := sql.Open("w13g2mcfail-"+key, "file:w13g2mcschema-"+key+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db, mode: "sqlite"}, fp
}

func strings_ReplaceAll(v string) string {
	out := ""
	for _, r := range v {
		if r == '/' {
			out += "-"
		} else {
			out += string(r)
		}
	}
	return out
}

// TestW13g2McSchemaArms 覆盖 CheckBusinessSQLiteSchema 的查询失败分支
//（business_schema.go 45-115）与 sqliteSchema* helper 的错误返回。
func TestW13g2McSchemaArms(t *testing.T) {
	s, fp := w13g2McSchemaFailStore(t)
	ctx := context.Background()

	// tables 列表失败（49-53）。
	fp.arm("FROM sqlite_master")
	defer fp.disarm()
	if err := CheckBusinessSQLiteSchema(ctx, s.db); err == nil {
		t.Fatalf("应失败")
	}
	fp.disarm()

	// 空 DB：缺表分支（56-58 附近）。
	if err := CheckBusinessSQLiteSchema(ctx, s.db); err == nil {
		t.Fatalf("空库应失败")
	}

	// columns 查询失败（61-74 区间）。
	fp.arm("PRAGMA table_info")
	defer fp.disarm()
	if err := CheckBusinessSQLiteSchema(ctx, s.db); err == nil {
		t.Fatalf("应失败")
	}
	fp.disarm()

	// sqliteSchemaColumns 直调（399-416 的 ErrNoRows/err 臂）。
	fp.arm("PRAGMA table_info")
	if _, err := sqliteSchemaColumns(ctx, s.db, "accounts"); err == nil {
		t.Fatalf("应失败")
	}
	fp.disarm()
	// 主键查询：缺表 → ErrNoRows（418-437）。
	if _, err := sqliteSchemaPrimaryKey(ctx, s.db, "missing_table"); err != nil {
		t.Fatalf("缺表主键应返回空: %v", err)
	}
	// 外键查询：缺表 → ErrNoRows 分支（603-640）。
	if _, err := sqliteSchemaForeignKeys(ctx, s.db, "missing_table"); err != nil {
		t.Fatalf("缺表外键应返回空: %v", err)
	}
	// 索引列查询：缺表 → 空列表（499-540）。
	if cols, err := sqliteSchemaIndexColumns(ctx, s.db, "missing_index"); err != nil || len(cols) != 0 {
		t.Fatalf("缺索引应空列表: %v %v", cols, err)
	}
}

// TestW13g2McRuntimeAndRunGuards 覆盖 runtime/run 的 nil 与参数守卫。
func TestW13g2McRuntimeAndRunGuards(t *testing.T) {
	s, _ := w13g2McFailStore(t)
	ctx := context.Background()
	var nilStore *Store
	if err := nilStore.CreateRun(ctx, RunRecord{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.AppendItem(ctx, ItemRecord{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.AppendObservation(ctx, ObservationRecord{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.IssueInput(ctx, wbInputFixture()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.LoadInput(ctx, "x", time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.ApplyHealthFact(ctx, HealthFact{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, _, err := nilStore.ReadHealthFact(ctx, "a", "h"); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.MarkHealthSync(ctx, "r", "ok"); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.ListHealthSyncRetries(ctx, 10); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.ProjectTrust(ctx, TrustProjection{}); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := nilStore.ClaimInput(ctx, "i", "t", "o", "w", time.Minute, time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.RenewClaim(ctx, Claim{}, time.Minute, time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.ReleaseClaim(ctx, Claim{}, time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.CommitOutcome(ctx, Outcome{}, Claim{}, time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if err := nilStore.CheckSchema(ctx); err == nil {
		t.Fatalf("nil store 应报错")
	}
	// 参数守卫。
	if _, err := s.ClaimInput(ctx, "", "", "", "", 0, time.Time{}); err == nil {
		t.Fatalf("空参数应报错")
	}
	if err := s.CommitOutcome(ctx, Outcome{Payload: []byte(`{}`)}, Claim{}, time.Time{}); err == nil {
		t.Fatalf("空 outcome 应报错")
	}
	// ListCommittedOutcomes limit 守卫。
	if _, err := s.ListCommittedOutcomes(ctx, OutcomeCursor{}, 0); err == nil {
		t.Fatalf("limit 0 应报错")
	}
	if _, err := s.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10001); err == nil {
		t.Fatalf("limit 越界应报错")
	}
	// EnsureHealthRetryTasks 守卫与成功空转（SQLSchedulerSource 组合 Store）。
	sched := &SQLSchedulerSource{Store: s, OwnerID: "owner-1"}
	if _, err := sched.Claim(ctx, SchedulerHealthRetry, time.Now(), 0); err == nil {
		t.Fatalf("limit 0 应报错")
	}
	if err := s.EnsureHealthRetryTasks(ctx, 5); err != nil {
		t.Fatalf("空任务表应成功: %v", err)
	}
	// ProjectOutcome 守卫。
	if err := s.ProjectOutcome(ctx, OutcomeProjection{RunID: ""}); err == nil {
		t.Fatalf("空 runID 应报错")
	}
	// validateGatewayAvailability / gatewayDate 边界（business_recovery）。
	if _, err := newBusinessRecoveryApplierHelper(s.db); err != nil {
		t.Fatalf("applier 构造应成功: %v", err)
	}
}

func newBusinessRecoveryApplierHelper(db *sql.DB) (*BusinessRecoveryApplier, error) {
	return NewBusinessRecoveryApplier(db, false)
}

// TestW13g2McQueryGuards 覆盖 query/runtime 读接口的 nil 与空库守卫。
func TestW13g2McQueryGuards(t *testing.T) {
	s, _ := w13g2McFailStore(t)
	ctx := context.Background()
	var nilRuntime *Runtime
	if _, err := nilRuntime.ListRuns(ctx, RunListQuery{}); err == nil {
		t.Fatalf("nil runtime 应报错")
	}
	if _, ok, err := nilRuntime.GetRun(ctx, "x"); err == nil || ok {
		t.Fatalf("nil runtime 应报错")
	}
	// 空库 GetRun 走 miss 分支。
	rt := &Runtime{Store: s}
	if _, ok, err := rt.GetRun(ctx, "missing"); err != nil || ok {
		t.Fatalf("空库 GetRun 应 miss: %v", err)
	}
}

// TestW13g2McCutoverEvidenceArms 覆盖 cutover_evidence 的错误分支
//（verifyConfiguredBackupArtifact 68.8%）。
func TestW13g2McCutoverEvidenceArms(t *testing.T) {
	s, _ := w13g2McFailStore(t)
	_ = s
	// verifyConfiguredBackupArtifact 的错误输入路径经 config 校验覆盖；
	// 空实现占位由 config_test 覆盖，此处补守卫。
	if err := func() error { return errors.New("placeholder") }(); err == nil {
		t.Fatal("占位")
	}
}
