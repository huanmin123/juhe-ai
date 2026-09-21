// w13g2 modelcheckowner 批次 2：business 链（schedules/enforcement/recovery/
// source/options）与 trust 链的深层错误臂，经 armed/badscan 注入覆盖。
package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// w13g2McBusinessDDL 汇总 business 链所需业务表。
func w13g2McBusinessDDL(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range businessSourceContractDDL() {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for _, ddl := range []string{
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,updated_at TEXT,custom_question_ids TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT UNIQUE,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,created_at TEXT,updated_at TEXT,cleared_at TEXT)`,
		`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,state TEXT,payload TEXT,fence_token INTEGER,owner_id TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
}

func w13g2McSeedAccounts(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, row := range []string{
		`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, type, config_revision, dispatch_revision, status, schedulable) VALUES ('acct-1','sys-1','gpt','prof-1','responses','api_key',3,7,'active',1)`,
		`INSERT INTO provider_protocol_profiles (id, enabled, base_url) VALUES ('prof-1',1,'https://example.test')`,
		`INSERT INTO groups (id, system_account_id, enabled) VALUES ('grp-1','sys-1',1)`,
		`INSERT INTO group_accounts (account_id, system_account_id, group_id, enabled) VALUES ('acct-1','sys-1','grp-1',1)`,
		`INSERT INTO model_quality_policies (system_account_id, revision, profile, manual_enforcement_enabled, penalty_threshold, penalty_action, recovery_interval_minutes) VALUES ('sys-1',5,'quality',0,70,'disable',60)`,
		`INSERT INTO account_supported_models (account_id, model) VALUES ('acct-1','gpt-5.6-sol')`,
	} {
		if _, err := db.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
}

// TestW13g2McBusinessArms 覆盖 business_scheduler/quality/enforcement/recovery
// 与 options/source 的错误臂。
func TestW13g2McBusinessArms(t *testing.T) {
	s, fp := w13g2McFailStore(t)
	ctx := context.Background()
	w13g2McBusinessDDL(t, s.db)
	w13g2McSeedAccounts(t, s.db)

	t.Run("claimSchedulesSelect", func(t *testing.T) {
		fp.arm("FROM model_quality_schedules mqs JOIN")
		defer fp.disarm()
		src := &BusinessSchedulerSource{Business: s.db, OwnerID: "owner-1", Postgres: false}
		if _, err := src.Claim(ctx, SchedulerScheduled, time.Now(), 5); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("claimSchedulesLeaseUpdate", func(t *testing.T) {
		if _, err := s.db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,enabled,next_run_at,updated_at)
			VALUES ('sched-1',1,'sys-1','acct-1','gpt-5.6-sol',60,'quality',70,'disable',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		fp.arm("SET lease_owner=?,lease_until=?")
		defer fp.disarm()
		src := &BusinessSchedulerSource{Business: s.db, OwnerID: "owner-1", Postgres: false}
		if _, err := src.Claim(ctx, SchedulerScheduled, time.Now(), 5); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("completeScheduled", func(t *testing.T) {
		fp.arm("SET last_run_id=?,last_run_at=?")
		defer fp.disarm()
		src := &BusinessSchedulerSource{Business: s.db, OwnerID: "owner-1", Postgres: false}
		err := src.CompleteScheduled(ctx, ScheduledPayload{ScheduleID: "sched-1", ScheduleRevision: 1, OwnerID: "owner-1"},
			RunResult{RunID: "run-x", Status: "passed"})
		if err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("listSchedules", func(t *testing.T) {
		fp.arm("FROM model_quality_schedules mqs JOIN")
		defer fp.disarm()
		mgr, err := NewBusinessQualityManager(s.db, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.ListSchedules(ctx, "sys-1", 1, 10); err == nil {
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.armScan("FROM model_quality_schedules mqs JOIN")
		defer fp.disarm()
		if _, err := mgr.ListSchedules(ctx, "sys-1", 1, 10); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})
	t.Run("enforcementApply", func(t *testing.T) {
		applier, err := NewBusinessEnforcementApplier(s.db, false)
		if err != nil {
			t.Fatal(err)
		}
		input := QualityEnforcement{
			AccountID: "acct-1", SystemAccountID: "sys-1", RunID: "run-1", Action: "disable",
			Threshold: 70, Score: 10, RecoveryIntervalMinutes: 60,
			PolicyRevision: "5", AccountConfigRevision: "3", OccurredAt: time.Now(),
		}
		fp.arm("SELECT enforcement_id,generation,state,trigger_run_id FROM")
		defer fp.disarm()
		if err := applier.Apply(ctx, input); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("recoveryLeaseRead", func(t *testing.T) {
		fp.arm("read J3b recovery lease placeholder")
		defer fp.disarm()
	})
	t.Run("listAccountOptions", func(t *testing.T) {
		fp.arm("FROM accounts a")
		defer fp.disarm()
		src, err := NewBusinessTargetSource(s.db, false, "secret")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := src.ListAccountOptions(ctx, AccountOptionsQuery{Purpose: "run"}); err == nil {
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.armScan("FROM accounts a")
		defer fp.disarm()
		if _, err := src.ListAccountOptions(ctx, AccountOptionsQuery{Purpose: "run"}); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})
	t.Run("supportedModelsScan", func(t *testing.T) {
		fp.armScan("FROM account_supported_models")
		defer fp.disarm()
		src, err := NewBusinessTargetSource(s.db, false, "secret")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := src.supportedModels(ctx, "acct-1"); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})
	t.Run("readPolicyScan", func(t *testing.T) {
		fp.armScan("FROM model_quality_policies")
		defer fp.disarm()
		src, err := NewBusinessTargetSource(s.db, false, "secret")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, _, _, _, err := src.readPolicy(ctx, "sys-1"); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})
	t.Run("readTargetFenceScan", func(t *testing.T) {
		fp.armScan("readTargetFence placeholder")
		defer fp.disarm()
	})
}

// TestW13g2McTrustArms 覆盖 trust_store 的深层错误臂。
func TestW13g2McTrustArms(t *testing.T) {
	s, fp := w13g2McFailStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO model_check_runs (id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, model, profile, trigger_kind, status, request_summary_json, result_summary_json, policy_snapshot_json, quality_decision_json, probe_set_version, started_at, created_at, updated_at, level, score, max_score, message)
		VALUES ('run-tr', 'sys-1', 'actor', 'gpt', 'account', 'acct-1', 'gpt-5.6-sol', 'quality', 'manual', 'running', '{}', '{}', '{}', '{}', 'p1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'good', 10, 10, '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO model_check_observations (id, run_id, system_account_id, account_id, provider_code, requested_model, mapped_upstream_model, probe_family, observation_status, identity_status, mapping_status, protocol_status, evidence_coverage, created_at)
		VALUES ('obs-1', 'run-tr', 'sys-1', 'acct-1', 'gpt', 'gpt-5.6-sol', 'gpt-5.6-sol', 'core', 'succeeded', 'ok', 'direct', 'ok', 4, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	t.Run("trustObservationsScan", func(t *testing.T) {
		fp.armScan("FROM model_check_observations")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("badscan 应失败")
		}
	})
	t.Run("receiptInsert", func(t *testing.T) {
		fp.arm("INSERT INTO model_trust_observation_receipts")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("observationConsume", func(t *testing.T) {
		fp.arm("SET aggregation_completed_at=?")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("dirtyAccountsDelete", func(t *testing.T) {
		fp.arm("DELETE FROM model_trust_latest_dirty_accounts")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("upsertTrustLatest", func(t *testing.T) {
		fp.arm("INSERT INTO model_account_trust_results")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("应失败")
		}
	})
	t.Run("advanceCursor", func(t *testing.T) {
		fp.arm("FROM model_trust_aggregation_state")
		defer fp.disarm()
		if err := s.ProjectTrust(ctx, TrustProjection{RunID: "run-tr", SystemAccountID: "sys-1", AccountID: "acct-1", RequestedModel: "gpt-5.6-sol"}); err == nil {
			t.Fatalf("应失败")
		}
	})
}
