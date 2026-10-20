package modelcheckowner

// w15b PostgreSQL 门禁回归(BUG-0308):J3b 计划/恢复领取在 pgx 下必须先把
// 流式 SELECT 读完并关闭 rows,再执行同事务租约 UPDATE。修复前的写法在
// "存在到期行"时 100% 得到 driver: bad connection(生产 2026-10-11 起定时
// 检测全量失效的直接根因);SQLite 全量缓冲无此约束,故本契约只能由真实
// pgx + PostgreSQL 钉死。
//
// 隔离铁律（与 auditlog w10b 先例一致）：
//   - 只触碰一次性子库 juhe_ai_sub2api_dev_j3bclaim（本测试 DROP 后重建，
//     WITH (FORCE)），主库一个字节不动；绝不 DROP 共享对象。
//   - 连接配置只从 .local/project-resources/dev/env/shared.env 读取，走
//     DEV_POSTGRES_DIRECT_PORT 直连（生产 gateway 同款直连语义）；任何输出
//     不得携带连接串或密码。
//   - env 缺失或 PG 不可达一律 t.Skip。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	w15bCoverDB  = "juhe_ai_sub2api_dev_j3bclaim"
	w15bEnvPath  = "../../../../../.local/project-resources/dev/env/shared.env"
	w15bOwnerID  = "w15b-pg-owner"
	w15bLease    = 6 * time.Minute
	w15bPgBudget = 30 * time.Second
)

func w15bSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(w15bEnvPath))
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

// w15bCoverPostgresDB 每次运行重建一次性子库并落最小业务 schema（列集与
// CheckContract 契约一致），返回应用直连句柄。
func w15bCoverPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	env := w15bSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	directPort := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || directPort == "" || adminUser == "" || adminPass == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, directPort))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w15bPgBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+w15bCoverDB+` WITH (FORCE)`); err != nil {
		t.Fatalf("清理一次性子库失败: %v", err)
	}
	owner := env["DEV_POSTGRES_APP_USERNAME"]
	if owner == "" {
		owner = adminUser
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+w15bCoverDB+` OWNER `+owner); err != nil {
		t.Fatalf("创建一次性子库失败: %v", err)
	}
	parsed, err := url.Parse(appURL)
	if err != nil {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）: %v", err)
	}
	parsed.Host = host + ":" + directPort
	parsed.Path = "/" + w15bCoverDB
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatalf("打开一次性子库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	w15bSchema := []string{
		`CREATE SCHEMA juhe_business`,
		`CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY,
			system_account_id text,
			provider_code text,
			config_revision int,
			dispatch_revision int,
			authorization_instance_source_account_id text,
			deleted_at text,
			authorization_instance_authorization_id text,
			status text,
			health_check_model text,
			vendor_code text,
			availability_schedule_json text,
			schedulable int,
			fallback_enabled int,
			super_priority_enabled int,
			last_error_code text,
			last_error_message text,
			updated_at text)`,
		`CREATE TABLE juhe_business.model_quality_schedules (
			id text PRIMARY KEY,
			revision int,
			system_account_id text,
			account_id text,
			model text,
			interval_minutes int,
			profile text,
			penalty_threshold int,
			penalty_action text,
			recovery_interval_minutes int,
			timeout_penalty_enabled boolean NOT NULL DEFAULT false,
			custom_question_ids text,
			enabled int,
			next_run_at text,
			lease_owner text,
			lease_until text,
			last_run_id text,
			last_run_at text,
			last_run_status text,
			updated_at text)`,
		`CREATE TABLE juhe_business.account_quality_enforcements (
			account_id text,
			system_account_id text,
			enforcement_id text,
			generation int,
			state text,
			action text,
			trigger_run_id text,
			config_source text,
			config_source_id text,
			policy_revision int,
			profile text,
			penalty_threshold int,
			recovery_interval_minutes int,
			recovery_model text,
			account_config_revision int,
			recovery_due_at text,
			recovery_lease_owner text,
			recovery_lease_until text,
			last_recovery_run_id text,
			cleared_at text,
			updated_at text)`,
		`CREATE TABLE juhe_business.model_quality_policies (
			system_account_id text,
			custom_question_ids text)`,
	}
	for _, statement := range w15bSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("落最小业务 schema 失败: %v", err)
		}
	}
	return db
}

func w15bScheduleRow(t *testing.T, db *sql.DB, id, accountID, nextRunAt string, enabled int) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO juhe_business.model_quality_schedules
		(id, revision, system_account_id, account_id, model, interval_minutes, profile, penalty_threshold, penalty_action, recovery_interval_minutes, enabled, next_run_at, updated_at)
		VALUES ($1, 1, 'sys-w15b', $2, 'gpt-w15b', 10, 'quick', 70, 'quality_isolate', 10, $3, $4, $5)`,
		id, accountID, enabled, nextRunAt, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("种子计划行失败: %v", err)
	}
}

func TestW15BClaimScheduledDueRowsUnderPgx(t *testing.T) {
	db := w15bCoverPostgresDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute).Format(time.RFC3339Nano)
	future := now.Add(10 * time.Minute).Format(time.RFC3339Nano)
	for _, account := range []struct{ id, status string }{
		{"acc-w15b-active", "active"},
		{"acc-w15b-paused", "paused"},
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, provider_code, config_revision, dispatch_revision, deleted_at, status, updated_at)
			VALUES ($1, 'sys-w15b', 'openai', 1, 1, NULL, $2, $3)`, account.id, account.status, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("种子账户失败: %v", err)
		}
	}
	w15bScheduleRow(t, db, "s-due-1", "acc-w15b-active", past, 1)
	w15bScheduleRow(t, db, "s-due-2", "acc-w15b-active", past, 1)
	w15bScheduleRow(t, db, "s-future", "acc-w15b-active", future, 1)
	w15bScheduleRow(t, db, "s-paused", "acc-w15b-paused", past, 1)

	source := &BusinessSchedulerSource{Business: db, Postgres: true, Store: &Store{mode: "postgres", schema: "juhe_j3b"}, OwnerID: w15bOwnerID, Lease: w15bLease}
	tasks, err := source.Claim(ctx, SchedulerScheduled, now, 10)
	if err != nil {
		t.Fatalf("领取到期计划失败(修复前此处为 driver: bad connection,即 BUG-0308): %v", err)
	}
	claimed := map[string]bool{}
	for _, task := range tasks {
		if task.Kind != SchedulerScheduled {
			t.Fatalf("任务 kind 错误: %s", task.Kind)
		}
		claimed[task.ID] = true
		var payload ScheduledPayload
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			t.Fatalf("解析 payload 失败: %v", err)
		}
		if payload.ScheduleID == "" || payload.OwnerID != w15bOwnerID {
			t.Fatalf("payload 缺少 ScheduleID/OwnerID: %+v", payload)
		}
	}
	if !claimed["schedule:s-due-1:1"] || !claimed["schedule:s-due-2:1"] {
		t.Fatalf("到期计划未被全部领取: %v", claimed)
	}
	if len(tasks) != 2 {
		t.Fatalf("应恰好领取 2 条(未来/停用账户计划不得领取),实际 %d: %v", len(tasks), claimed)
	}
	for _, id := range []string{"s-due-1", "s-due-2"} {
		var leaseOwner string
		var leaseUntil sql.NullString
		if err := db.QueryRow(`SELECT lease_owner, lease_until FROM juhe_business.model_quality_schedules WHERE id=$1`, id).Scan(&leaseOwner, &leaseUntil); err != nil {
			t.Fatalf("读取租约失败: %v", err)
		}
		if leaseOwner != w15bOwnerID || !leaseUntil.Valid {
			t.Fatalf("计划 %s 租约未落库: owner=%q until=%v", id, leaseOwner, leaseUntil)
		}
	}
	for _, id := range []string{"s-future", "s-paused"} {
		var leaseOwner sql.NullString
		if err := db.QueryRow(`SELECT lease_owner FROM juhe_business.model_quality_schedules WHERE id=$1`, id).Scan(&leaseOwner); err != nil {
			t.Fatalf("读取租约失败: %v", err)
		}
		if leaseOwner.Valid {
			t.Fatalf("不应领取的计划 %s 被写了租约: %v", id, leaseOwner.String)
		}
	}
	// 已领租约的行在租约期内不得二次领取(零到期行路径,修复前也不得报错)。
	repeat, err := source.Claim(ctx, SchedulerScheduled, now, 10)
	if err != nil {
		t.Fatalf("零到期行领取失败: %v", err)
	}
	if len(repeat) != 0 {
		t.Fatalf("租约期内重复领取: %d", len(repeat))
	}
}

func TestW15BClaimQualityRecoveryDueRowsUnderPgx(t *testing.T) {
	db := w15bCoverPostgresDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute).Format(time.RFC3339Nano)
	future := now.Add(10 * time.Minute).Format(time.RFC3339Nano)
	stamp := now.Format(time.RFC3339Nano)
	for _, account := range []struct{ id, status string }{
		{"acc-w15b-iso", "quality_isolated"},
		{"acc-w15b-ok", "active"},
	} {
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, provider_code, vendor_code, config_revision, dispatch_revision, deleted_at, status, health_check_model, updated_at)
			VALUES ($1, 'sys-w15b', 'openai', 'gpt', 3, 5, NULL, $2, 'gpt-w15b-health', $3)`, account.id, account.status, stamp); err != nil {
			t.Fatalf("种子账户失败: %v", err)
		}
	}
	enforcements := []struct {
		id      string
		account string
		dueAt   string
	}{
		{"enf-due", "acc-w15b-iso", past},
		{"enf-future", "acc-w15b-iso", future},
		{"enf-active-account", "acc-w15b-ok", past},
	}
	for _, enforcement := range enforcements {
		if _, err := db.Exec(`INSERT INTO juhe_business.account_quality_enforcements
			(account_id, system_account_id, enforcement_id, generation, state, action, config_source, config_source_id, policy_revision, profile, penalty_threshold, recovery_interval_minutes, recovery_due_at, account_config_revision, updated_at)
			VALUES ($1, 'sys-w15b', $2, 2, 'active', 'quality_isolate', '', '', 1, 'quick', 70, 10, $3, 3, $4)`,
			enforcement.account, enforcement.id, enforcement.dueAt, stamp); err != nil {
			t.Fatalf("种子处罚行失败: %v", err)
		}
	}

	source := &BusinessSchedulerSource{Business: db, Postgres: true, Store: &Store{mode: "postgres", schema: "juhe_j3b"}, OwnerID: w15bOwnerID, Lease: w15bLease}
	tasks, err := source.Claim(ctx, SchedulerQualityRecovery, now, 10)
	if err != nil {
		t.Fatalf("领取到期恢复失败(修复前此处为 driver: bad connection,即 BUG-0308): %v", err)
	}
	if len(tasks) != 1 || tasks[0].Kind != SchedulerQualityRecovery || tasks[0].ID != "recovery:acc-w15b-iso:enf-due:2" {
		t.Fatalf("应恰好领取 enf-due 一条,实际: %+v", tasks)
	}
	var payload ScheduledPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatalf("解析 payload 失败: %v", err)
	}
	if payload.Model != "gpt-w15b-health" || payload.EnforcementID != "enf-due" || payload.Generation != 2 {
		t.Fatalf("恢复 payload 字段错误: %+v", payload)
	}
	var leaseOwner sql.NullString
	if err := db.QueryRow(`SELECT recovery_lease_owner FROM juhe_business.account_quality_enforcements WHERE enforcement_id=$1`, "enf-due").Scan(&leaseOwner); err != nil {
		t.Fatalf("读取恢复租约失败: %v", err)
	}
	if !leaseOwner.Valid || leaseOwner.String != w15bOwnerID {
		t.Fatalf("恢复租约未落库: %v", leaseOwner)
	}
}
