package modelcheckowner

// W1 owner fault edges deliberately exercise the public Business owner
// boundaries with the shared w14m SQLite failure driver.  These tests keep
// the fault injection local to one subtest and verify the durable facts after
// the failpoint is disarmed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
)

func w1OwnerBusinessRunNowDDL() []string {
	return []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,system_account_id TEXT,name TEXT,vendor_code TEXT,connection_driver_code TEXT,config_revision INTEGER,dispatch_revision INTEGER,authorization_instance_source_account_id TEXT,deleted_at TEXT,authorization_instance_authorization_id TEXT,status TEXT,health_check_model TEXT,availability_schedule_json TEXT,schedulable INTEGER,fallback_enabled INTEGER,super_priority_enabled INTEGER,last_error_code TEXT,last_error_message TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,custom_question_ids TEXT,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT,updated_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY,revision INTEGER,profile TEXT,manual_enforcement_enabled INTEGER,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled BOOLEAN NOT NULL DEFAULT FALSE,custom_question_ids TEXT)`,
		`CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,trigger_run_id TEXT,config_source TEXT,config_source_id TEXT,policy_revision INTEGER,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_model TEXT,account_config_revision INTEGER,before_status TEXT,after_status TEXT,fallback_was_enabled INTEGER,super_priority_was_enabled INTEGER,started_at TEXT,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,last_recovery_run_id TEXT,cleared_at TEXT,created_at TEXT,updated_at TEXT)`,
	}
}

// w1IgnoreUpdateTriggerDDL 生成"静默忽略 UPDATE"的 PG 触发器：BEFORE 行
// 触发器返回 NULL 即取消该行更新，等价于 SQLite 的 RAISE(IGNORE)。
func w1IgnoreUpdateTriggerDDL(name, table, column, when string) []string {
	return []string{
		`CREATE OR REPLACE FUNCTION ` + name + `() RETURNS trigger AS $$ BEGIN RETURN NULL; END; $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER ` + name + ` BEFORE UPDATE OF ` + column + ` ON ` + table + ` FOR EACH ROW ` + when + ` EXECUTE FUNCTION ` + name + `()`,
	}
}

func w1OwnerSeedPenaltyAccount(t *testing.T, db *sql.DB, status string, revision int, availability string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,vendor_code,config_revision,dispatch_revision,deleted_at,authorization_instance_authorization_id,status,availability_schedule_json,schedulable,fallback_enabled,super_priority_enabled,last_error_code,last_error_message) VALUES ('acct','sys','gpt',?,7,NULL,NULL,?,?, 1,1,0,'model_quality_failed','')`, revision, status, availability); err != nil {
		t.Fatal(err)
	}
}

func w1OwnerSeedPenalty(t *testing.T, db *sql.DB, action, source, sourceID, before string, revision int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,updated_at) VALUES ('acct','sys','enf-1',1,'active',?,'run-old',?,?,1,'quick',70,10,?,?,?,?,?,?)`, action, source, sourceID, revision, before, action, 0, 1, ""); err != nil {
		t.Fatal(err)
	}
}

func w1OwnerRestoreState(t *testing.T, db *sql.DB) (status string, revision, fallback, priority int, enforcement string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status,config_revision,fallback_enabled,super_priority_enabled FROM accounts WHERE id='acct'`).Scan(&status, &revision, &fallback, &priority); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&enforcement); err != nil {
		t.Fatal(err)
	}
	return
}

func TestW1BusinessRecoveryFaultEdges(t *testing.T) {
	ctx := context.Background()
	input := SchedulePenaltyRestoreInput{AccountID: "acct", SystemAccountID: "sys", ScheduleID: "sch", RunID: "run-new", OccurredAt: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}

	t.Run("invalid input and begin/query/scan/iterate failures", func(t *testing.T) {
		applier, err := NewBusinessRecoveryApplier(wbOpenPGDB(t, w1OwnerBusinessRunNowDDL()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applier.RestoreSchedulePenalties(ctx, SchedulePenaltyRestoreInput{}); err == nil || !strings.Contains(err.Error(), "input is invalid") {
			t.Fatalf("invalid restore input err=%v", err)
		}

		for _, tc := range []struct {
			name string
			arm  func(*w14mFailpoint)
			want string
		}{
			{name: "begin", arm: func(fp *w14mFailpoint) { fp.failBeginsFrom = 1 }, want: "begin J3b schedule penalty restore"},
			{name: "query", arm: func(fp *w14mFailpoint) { fp.arm("FROM account_quality_enforcements") }, want: "read J3b schedule penalty rows"},
			{name: "scan", arm: func(fp *w14mFailpoint) { fp.armScan("FROM account_quality_enforcements") }, want: "scan J3b schedule penalty row"},
			{name: "iterate", arm: func(fp *w14mFailpoint) { fp.armNextErr("FROM account_quality_enforcements") }, want: "iterate J3b schedule penalty rows"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
				defer fp.disarm()
				applier, err := NewBusinessRecoveryApplier(db)
				if err != nil {
					t.Fatal(err)
				}
				tc.arm(fp)
				_, err = applier.RestoreSchedulePenalties(ctx, input)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("fault=%s err=%v", tc.name, err)
				}
				if !strings.Contains(err.Error(), "w14m") && tc.name != "scan" && tc.name != "iterate" {
					t.Fatalf("fault=%s must preserve injected cause: %v", tc.name, err)
				}
			})
		}
	})

	t.Run("missing account, availability, unknown action and CAS skip preserve facts", func(t *testing.T) {
		t.Run("missing account is skipped", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			outcome, err := applier.RestoreSchedulePenalties(ctx, input)
			if err != nil || outcome.Restored != 0 || outcome.Skipped != 1 {
				t.Fatalf("missing account outcome=%+v err=%v", outcome, err)
			}
			var state string
			if err := db.QueryRow(`SELECT state FROM account_quality_enforcements WHERE account_id='acct'`).Scan(&state); err != nil || state != "active" {
				t.Fatalf("missing account enforcement state=%q err=%v", state, err)
			}
		})
		t.Run("invalid availability rolls back", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "quality_isolated", 6, `{ "enabled":`)
			w1OwnerSeedPenalty(t, db, "quality_isolate", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			_, err := applier.RestoreSchedulePenalties(ctx, input)
			if err == nil || !strings.Contains(err.Error(), "evaluate J3b schedule restore availability") {
				t.Fatalf("invalid availability err=%v", err)
			}
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "quality_isolated" || revision != 6 || state != "active" {
				t.Fatalf("availability error must rollback status=%q rev=%d enforcement=%q", status, revision, state)
			}
		})
		t.Run("unknown action is skipped", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "active", 6, "")
			w1OwnerSeedPenalty(t, db, "mystery", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			outcome, err := applier.RestoreSchedulePenalties(ctx, input)
			if err != nil || outcome.Restored != 0 || outcome.Skipped != 1 {
				t.Fatalf("unknown action outcome=%+v err=%v", outcome, err)
			}
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "active" || revision != 6 || state != "active" {
				t.Fatalf("unknown action changed facts status=%q rev=%d enforcement=%q", status, revision, state)
			}
		})
		t.Run("status CAS skip does not restore", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "active", 6, "")
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			outcome, err := applier.RestoreSchedulePenalties(ctx, input)
			if err != nil || outcome.Restored != 0 || outcome.Skipped != 1 {
				t.Fatalf("CAS skip outcome=%+v err=%v", outcome, err)
			}
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "active" || revision != 6 || state != "active" {
				t.Fatalf("CAS skip changed facts status=%q rev=%d enforcement=%q", status, revision, state)
			}
		})
	})

	t.Run("all action update failures preserve account and enforcement", func(t *testing.T) {
		for _, tc := range []struct {
			name, action, status, pattern string
		}{
			{name: "quality isolate", action: "quality_isolate", status: "quality_isolated", pattern: "UPDATE accounts SET status"},
			{name: "disable", action: "disable", status: "disabled", pattern: "UPDATE accounts SET status"},
			{name: "fallback", action: "fallback", status: "active", pattern: "UPDATE accounts SET fallback_enabled"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
				defer fp.disarm()
				w1OwnerSeedPenaltyAccount(t, db, tc.status, 6, "")
				w1OwnerSeedPenalty(t, db, tc.action, "schedule", "sch", "active", 6)
				applier, _ := NewBusinessRecoveryApplier(db)
				fp.arm(tc.pattern)
				_, err := applier.RestoreSchedulePenalties(ctx, input)
				if err == nil || !strings.Contains(err.Error(), "w14m") {
					t.Fatalf("action=%s err=%v", tc.action, err)
				}
				fp.disarm()
				status, revision, fallback, priority, state := w1OwnerRestoreState(t, db)
				if status != tc.status || revision != 6 || fallback != 1 || priority != 0 || state != "active" {
					t.Fatalf("action=%s partial mutation status=%q rev=%d fallback=%d priority=%d state=%q", tc.action, status, revision, fallback, priority, state)
				}
			})
		}
	})

	t.Run("enforcement clear failure and commit failure roll back", func(t *testing.T) {
		t.Run("clear enforcement error", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "disabled", 6, "")
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			fp.arm("UPDATE account_quality_enforcements SET state='cleared'")
			_, err := applier.RestoreSchedulePenalties(ctx, input)
			if err == nil || !strings.Contains(err.Error(), "enforcement CAS failed") {
				t.Fatalf("clear failure err=%v", err)
			}
			fp.disarm()
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "disabled" || revision != 6 || state != "active" {
				t.Fatalf("clear failure must rollback status=%q rev=%d state=%q", status, revision, state)
			}
		})
		t.Run("commit error", func(t *testing.T) {
			db, fp := w14mFailDB(t, w1OwnerBusinessRunNowDDL())
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "disabled", 6, "")
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			fp.failCommitsFrom = 1
			_, err := applier.RestoreSchedulePenalties(ctx, input)
			if err == nil || !strings.Contains(err.Error(), "commit J3b schedule penalty restore") || !strings.Contains(err.Error(), "w14m") {
				t.Fatalf("commit failure err=%v", err)
			}
			fp.disarm()
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "disabled" || revision != 6 || state != "active" {
				t.Fatalf("commit failure must rollback status=%q rev=%d state=%q", status, revision, state)
			}
		})
	})

	t.Run("ignored action and enforcement CAS updates never leave partial state", func(t *testing.T) {
		t.Run("account action RAISE IGNORE counts as skipped", func(t *testing.T) {
			ddl := append(w1OwnerBusinessRunNowDDL(), w1IgnoreUpdateTriggerDDL("w1_ignore_account_restore", "accounts", "status", "")...)
			db, fp := w14mFailDB(t, ddl)
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "disabled", 6, "")
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			outcome, err := applier.RestoreSchedulePenalties(ctx, input)
			if err != nil || outcome.Restored != 0 || outcome.Skipped != 1 {
				t.Fatalf("ignored account CAS outcome=%+v err=%v", outcome, err)
			}
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "disabled" || revision != 6 || state != "active" {
				t.Fatalf("ignored action must preserve state=%q revision=%d enforcement=%q", status, revision, state)
			}
		})
		t.Run("enforcement clear RAISE IGNORE rolls account back", func(t *testing.T) {
			ddl := append(w1OwnerBusinessRunNowDDL(), w1IgnoreUpdateTriggerDDL("w1_ignore_enforcement_clear", "account_quality_enforcements", "state", "WHEN (NEW.state='cleared')")...)
			db, fp := w14mFailDB(t, ddl)
			defer fp.disarm()
			w1OwnerSeedPenaltyAccount(t, db, "disabled", 6, "")
			w1OwnerSeedPenalty(t, db, "disable", "schedule", "sch", "active", 6)
			applier, _ := NewBusinessRecoveryApplier(db)
			_, err := applier.RestoreSchedulePenalties(ctx, input)
			if err == nil || !strings.Contains(err.Error(), "enforcement CAS failed") {
				t.Fatalf("ignored enforcement CAS err=%v", err)
			}
			status, revision, _, _, state := w1OwnerRestoreState(t, db)
			if status != "disabled" || revision != 6 || state != "active" {
				t.Fatalf("ignored enforcement CAS must rollback status=%q revision=%d state=%q", status, revision, state)
			}
		})
	})
}

func w1OwnerSchedulerBusinessDDL() []string { return w1OwnerBusinessRunNowDDL() }

func w1OwnerSeedSchedule(t *testing.T, db *sql.DB, leaseOwner, leaseUntil string) {
	t.Helper()
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO accounts (id,system_account_id,vendor_code,config_revision,dispatch_revision,deleted_at,authorization_instance_authorization_id,status,health_check_model,availability_schedule_json,schedulable,fallback_enabled,super_priority_enabled) VALUES ('acct','sys','gpt',4,7,NULL,NULL,'active','gpt-6-luna','', 1,0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,custom_question_ids,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,updated_at) VALUES ('sch',1,'sys','acct','gpt-6-luna',30,'quick',70,'fallback',10,'[]', 1,?,?,?,NULL,NULL,NULL,'')`, now.Add(-time.Minute).Format(time.RFC3339Nano), leaseOwner, leaseUntil); err != nil {
		t.Fatal(err)
	}
}

func TestW1BusinessSchedulerFaultEdges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

	t.Run("public contract and input gates", func(t *testing.T) {
		db, fp := w14mFailDB(t, w1OwnerSchedulerBusinessDDL())
		defer fp.disarm()
		store := schedulerStoreFixture(t)
		defer store.db.Close()
		source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "owner"}
		if err := source.CheckContract(ctx); err != nil {
			t.Fatalf("contract check err=%v", err)
		}
		fp.arm("SELECT id,system_account_id,vendor_code")
		if err := source.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "verify J3b Business scheduler table accounts") || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("contract query error=%v", err)
		}
		fp.disarm()
		if _, err := (&BusinessSchedulerSource{}).Claim(ctx, SchedulerScheduled, now, 1); err == nil {
			t.Fatal("uninitialized scheduler must fail")
		}
		if _, err := source.Claim(ctx, SchedulerKind("bogus"), now, 1); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported kind err=%v", err)
		}
		if _, err := source.Claim(ctx, SchedulerScheduled, now, 0); err == nil {
			t.Fatal("zero limit must fail")
		}
	})

	t.Run("schedule claim EOF, lease failure, and raise ignore", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			mode string
			want string
		}{
			{name: "EOF", mode: "eof", want: "claim J3b schedules"},
			{name: "lease update error", mode: "error", want: "w14m"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db, fp := w14mFailDB(t, w1OwnerSchedulerBusinessDDL())
				defer fp.disarm()
				store := schedulerStoreFixture(t)
				defer store.db.Close()
				w1OwnerSeedSchedule(t, db, "", "")
				source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "owner"}
				if tc.mode == "eof" {
					fp.armNextErr("FROM model_quality_schedules")
				} else {
					fp.arm("UPDATE model_quality_schedules SET lease_owner")
				}
				tasks, err := source.Claim(ctx, SchedulerScheduled, now, 5)
				if err == nil || (tc.mode == "eof" && !errors.Is(err, errW14mInjected)) || (tc.mode != "eof" && !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("claim fault=%s tasks=%+v err=%v", tc.name, tasks, err)
				}
				fp.disarm()
				var owner sql.NullString
				if err := db.QueryRow(`SELECT lease_owner FROM model_quality_schedules WHERE id='sch'`).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				if owner.Valid && owner.String != "" {
					t.Fatalf("failed claim must leave lease empty: %q", owner.String)
				}
			})
		}
		t.Run("PG trigger ignoring the update leaves no task or lease", func(t *testing.T) {
			db, fp := w14mFailDB(t, append(w1OwnerSchedulerBusinessDDL(), w1IgnoreUpdateTriggerDDL("w1_ignore_schedule_lease", "model_quality_schedules", "lease_owner", "")...))
			defer fp.disarm()
			store := schedulerStoreFixture(t)
			defer store.db.Close()
			w1OwnerSeedSchedule(t, db, "", "")
			source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "owner"}
			tasks, err := source.Claim(ctx, SchedulerScheduled, now, 5)
			if err != nil || len(tasks) != 0 {
				t.Fatalf("RAISE IGNORE claim tasks=%+v err=%v", tasks, err)
			}
			var owner sql.NullString
			if err := db.QueryRow(`SELECT lease_owner FROM model_quality_schedules WHERE id='sch'`).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if owner.Valid && owner.String != "" {
				t.Fatalf("RAISE IGNORE must leave lease empty: %q", owner.String)
			}
		})
	})

	t.Run("completion and lookup contracts preserve raw storage errors", func(t *testing.T) {
		db, fp := w14mFailDB(t, w1OwnerSchedulerBusinessDDL())
		defer fp.disarm()
		store := schedulerStoreFixture(t)
		defer store.db.Close()
		w1OwnerSeedSchedule(t, db, "owner", time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339Nano))
		source := &BusinessSchedulerSource{Business: db, Store: store, OwnerID: "owner"}
		payload := ScheduledPayload{OwnerID: "owner", ScheduleID: "sch", ScheduleRevision: 1, IntervalMinutes: 5}
		if err := source.CompleteScheduled(ctx, payload, RunResult{RunID: "run-1", Status: "bogus"}); err != nil {
			t.Fatalf("invalid completion status should be failed, err=%v", err)
		}
		var status string
		if err := db.QueryRow(`SELECT last_run_status FROM model_quality_schedules WHERE id='sch'`).Scan(&status); err != nil || status != "failed" {
			t.Fatalf("invalid status stored=%q err=%v", status, err)
		}
		if err := source.CompleteScheduled(ctx, ScheduledPayload{}, RunResult{}); err == nil || !strings.Contains(err.Error(), "completion input is invalid") {
			t.Fatalf("invalid completion input err=%v", err)
		}
		w1OwnerSeedPenalty(t, db, "quality_isolate", "schedule", "sch", "active", 4)
		fp.arm("SELECT mqs.id")
		if _, _, err := source.ScheduleForRunNow(ctx, "sys", "sch"); err == nil || !strings.Contains(err.Error(), "read J3b run-now schedule") || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("schedule lookup query error=%v", err)
		}
		fp.disarm()
		if _, _, err := source.ScheduleForRunNow(ctx, "", "sch"); err == nil || !strings.Contains(err.Error(), "lookup input is invalid") {
			t.Fatalf("schedule lookup invalid err=%v", err)
		}
		if _, err := source.AccountRecoveryLeaseActive(ctx, "", now); err == nil || !strings.Contains(err.Error(), "lease lookup input is invalid") {
			t.Fatalf("recovery lease invalid err=%v", err)
		}
		fp.arm("SELECT 1 FROM account_quality_enforcements")
		if _, err := source.AccountRecoveryLeaseActive(ctx, "acct", now); err == nil || !strings.Contains(err.Error(), "read J3b run-now recovery lease") || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("recovery lease query error=%v", err)
		}
		fp.disarm()
		if err := source.CompleteRunNow(ctx, "", 0, true, 0, "", "", time.Time{}); err == nil || !strings.Contains(err.Error(), "completion input is invalid") {
			t.Fatalf("run-now completion invalid err=%v", err)
		}
		fp.arm("UPDATE model_quality_schedules SET last_run_id")
		if err := source.CompleteRunNow(ctx, "sch", 1, true, 5, "run-2", "bogus", now); err == nil || !strings.Contains(err.Error(), "complete J3b run-now schedule") || !strings.Contains(err.Error(), "w14m") {
			t.Fatalf("run-now completion storage error=%v", err)
		}
		fp.disarm()
	})
}

func TestW1HTTPRunNowFaultEdges(t *testing.T) {
	request := func(handler *HTTPHandler, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return recorder
	}

	t.Run("all scope rejects single and batch", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "unused", onStarted: true}))
		handler.AllowCrossAccount = true
		for _, path := range []string{"/quality-schedules/sch/run-now", "/quality-schedules/run-now-batch"} {
			response := request(handler, path, `{"items":[{"scheduleId":"sch"}]}`)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "具体系统账户") {
				t.Fatalf("all scope path=%s status=%d body=%s", path, response.Code, response.Body.String())
			}
		}
	})

	t.Run("batch nil owner, over-limit and revision zero", func(t *testing.T) {
		nilHandler := newTestHTTPHandler()
		response := request(nilHandler, "/quality-schedules/run-now-batch?systemAccountId=sys-1", `{"items":[{"scheduleId":"sch"}]}`)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("nil run-now owner status=%d body=%s", response.Code, response.Body.String())
		}

		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "unused", onStarted: true}))
		var builder strings.Builder
		builder.WriteString(`{"items":[`)
		for i := 0; i < 101; i++ {
			if i > 0 {
				builder.WriteByte(',')
			}
			fmt.Fprintf(&builder, `{"scheduleId":"sch-%d"}`, i)
		}
		builder.WriteString(`]}`)
		response = request(handler, "/quality-schedules/run-now-batch?systemAccountId=sys-1", builder.String())
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "最多受理 100") {
			t.Fatalf("over-limit status=%d body=%s", response.Code, response.Body.String())
		}

		w1OwnerSeedPenaltyAccount(t, db, "active", 4, "")
		if _, err := db.Exec(`UPDATE accounts SET system_account_id='sys-1' WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at) VALUES ('sch',1,'sys-1','acct','gpt-6-luna',30,'quick',70,'fallback',10, 1,?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		response = request(handler, "/quality-schedules/sch/run-now?systemAccountId=sys-1", `{"revision":0}`)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "revision") {
			t.Fatalf("revision zero status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("closed Business database exposes original storage error", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, &fakeRunNowRunner{runID: "unused", onStarted: true}))
		_ = db.Close()
		response := request(handler, "/quality-schedules/sch/run-now?systemAccountId=sys-1", `{}`)
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "database is closed") {
			t.Fatalf("closed db status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("registry conflict returns retry and active summary", func(t *testing.T) {
		db := wbRunNowBusinessFixture(t)
		runner := &fakeRunNowRunner{runID: "unused", onStarted: true}
		handler := wbNewRunNowHTTPHandler(t, wbRunNowServiceForHTTP(t, db, runner))
		w1OwnerSeedPenaltyAccount(t, db, "active", 4, "")
		if _, err := db.Exec(`UPDATE accounts SET system_account_id='sys-1' WHERE id='acct'`); err != nil {
			t.Fatal(err)
		}
	if _, err := db.Exec(`INSERT INTO model_quality_schedules (id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,enabled,next_run_at) VALUES ('sch',1,'sys-1','acct','gpt-6-luna',30,'quick',70,'fallback',10, 1,?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		handle, acquired, _ := handler.RunNow.Active.TryStart(context.Background(), scheduleRunNowTargetKey("acct"), modelcheckactive.Summary{RunID: "existing", TargetID: "acct", Model: "gpt-6-luna"})
		if !acquired {
			t.Fatal("test must acquire registry slot")
		}
		defer handle.Finish()
		response := request(handler, "/quality-schedules/sch/run-now?systemAccountId=sys-1", `{}`)
		if response.Code != http.StatusConflict || response.Header().Get("Retry-After") != "1" {
			t.Fatalf("conflict status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
		}
		body := response.Body.String()
		if !strings.Contains(body, `"active"`) || !strings.Contains(body, `"runId":"existing"`) || !strings.Contains(body, `"code":"active"`) {
			t.Fatalf("conflict active summary missing: %s", body)
		}
	})
}
