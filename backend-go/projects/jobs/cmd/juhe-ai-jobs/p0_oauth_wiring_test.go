package main

// P0 修复回归（组合根装配面）：
//   - OAuth 刷新失败状态存储按运行态部署分支注入：无 JUHE_AI_REDIS_STATE_URL
//     → 内存版默认（oauthFailures=nil）；配置后 → Redis 版 + closer；URL 非法
//     → 装配 fail-fast。
//   - account-availability-schedule-status-sync 注入激活 hook：启用翻转在同一
//     事务内推进 circuit dispatch revision 家族（写 outbox + revision+1）；
//     推进失败 best-effort——只记 warn，不中断同步、状态翻转保持生效。

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
)

const p0WiringScheduleJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`

func TestP0OAuthFailureStateStoreAssemblyBranch(t *testing.T) {
	// 无 Redis 运行态 → nil（refresh job 保持内存版默认）。
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	defer assembly.closeStores()
	store, closer, err := assembly.oauthFailureStateStore()
	if err != nil || store != nil || closer != nil {
		t.Fatalf("no redis url must keep memory default: store=%T closerNil=%v err=%v", store, closer == nil, err)
	}
	// redis-state 部署 → Redis 版（客户端惰性建连，不拨号）。
	redisAssembly := newWorkerAssembly(workerConfig{Driver: "sqlite", RedisStateURL: "redis://127.0.0.1:6379/9"}, slog.Default())
	defer redisAssembly.closeStores()
	redisStore, redisCloser, err := redisAssembly.oauthFailureStateStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := redisStore.(*oauthrefresh.RedisFailureStateStore); !ok {
		t.Fatalf("redis-state branch must inject RedisFailureStateStore, got %T", redisStore)
	}
	if redisCloser == nil {
		t.Fatal("redis client closer missing")
	}
	// 非法 URL → 装配失败（fail-fast，不静默退回内存版）。
	badAssembly := newWorkerAssembly(workerConfig{Driver: "sqlite", RedisStateURL: "://not-a-url"}, slog.Default())
	defer badAssembly.closeStores()
	if _, _, err := badAssembly.oauthFailureStateStore(); err == nil {
		t.Fatal("invalid JUHE_AI_REDIS_STATE_URL must fail the assembly")
	}
}

func TestP0WireOAuthFamilyFailureStoreBranch(t *testing.T) {
	ctx := context.Background()
	// sqlite 无 Redis 运行态：整族装配成功，失败状态保持内存默认。
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		Secret:             "p0-wiring-test-secret",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
	}, slog.Default())
	defer assembly.closeStores()
	if err := assembly.wireOAuthFamily(ctx); err != nil {
		t.Fatal(err)
	}
	if assembly.oauthFailures != nil {
		t.Fatalf("sqlite branch must keep the memory default, got %T", assembly.oauthFailures)
	}
	// redis-state 部署：整族装配注入 Redis 版。
	redisAssembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		Secret:             "p0-wiring-test-secret",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
		RedisStateURL:      "redis://127.0.0.1:6379/9",
	}, slog.Default())
	defer redisAssembly.closeStores()
	if err := redisAssembly.wireOAuthFamily(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := redisAssembly.oauthFailures.(*oauthrefresh.RedisFailureStateStore); !ok {
		t.Fatalf("redis-state assembly must wire the Redis failure store, got %T", redisAssembly.oauthFailures)
	}
}

// p0OpenScheduleSyncDB 建立排期同步 + dispatch 推进所需的最小业务库
// （SQLite；列集合对齐 oauthrefresh 排期同步 SQL 与 accounthealth 家族推进
// SQL 的读取面）。withOutbox=false 模拟推进副作用失败（outbox 表缺失）。
func p0OpenScheduleSyncDB(t *testing.T, withOutbox bool) (*sql.DB, *oauthrefresh.Store, *accounthealth.ProjectionBusinessDB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schedule-sync.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'active',
	availability_schedule_json TEXT,
	availability_schedule_next_check_at TEXT,
	deleted_at TEXT,
	updated_at TEXT NOT NULL DEFAULT '',
	dispatch_revision INTEGER NOT NULL DEFAULT 1,
	authorization_instance_source_account_id TEXT
);
CREATE TABLE account_schedule_status_events (
	event_key TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	status TEXT NOT NULL,
	executed_at TEXT NOT NULL
);
CREATE TABLE account_quality_enforcements (
	account_id TEXT NOT NULL,
	state TEXT NOT NULL,
	action TEXT NOT NULL
);
`
	if withOutbox {
		schema += `
CREATE TABLE account_circuit_outbox (
	event_id TEXT PRIMARY KEY,
	projection_key TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	event_type TEXT NOT NULL,
	account_id TEXT NOT NULL,
	account_runtime_key TEXT NOT NULL,
	circuit_scope_key TEXT,
	incident_id TEXT,
	transition_id TEXT NOT NULL,
	dispatch_revision INTEGER NOT NULL,
	generation TEXT,
	ledger_revision TEXT,
	status TEXT NOT NULL,
	available_at_ms INTEGER NOT NULL,
	attempt_count INTEGER NOT NULL,
	created_at_ms INTEGER NOT NULL,
	updated_at_ms INTEGER NOT NULL
);
`
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	store, err := oauthrefresh.OpenStore(db, oauthrefresh.StoreSQLite, "p0-wiring-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	business, err := accounthealth.NewProjectionBusinessDB(db, false)
	if err != nil {
		t.Fatal(err)
	}
	return db, store, business
}

func p0SeedScheduledAccount(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id, status, availability_schedule_json, updated_at) VALUES (?, ?, ?, ?)`,
		id, status, p0WiringScheduleJSON, "2026-09-04T08:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
}

func TestP0ScheduleActivationHookAdvancesDispatchFamily(t *testing.T) {
	db, store, business := p0OpenScheduleSyncDB(t, true)
	p0SeedScheduledAccount(t, db, "acc-hook-on", "disabled")
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	hook := newAccountScheduleActivationHook(business, slog.Default())
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook)
	if err != nil {
		t.Fatal(err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v", result)
	}
	var revision int
	if err := db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'acc-hook-on'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("dispatch revision=%d, want advanced to 2", revision)
	}
	var eventType, dedupeKey string
	if err := db.QueryRow(`SELECT event_type, dedupe_key FROM account_circuit_outbox WHERE account_id = 'acc-hook-on'`).Scan(&eventType, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if eventType != "dispatch_revision_changed" || !strings.HasPrefix(dedupeKey, "dispatch:") {
		t.Fatalf("outbox row=%q %q", eventType, dedupeKey)
	}
}

func TestP0ScheduleActivationHookFailureDoesNotBreakSync(t *testing.T) {
	// outbox 表缺失 → 家族推进失败 → hook best-effort 吞错记 warn，同步事务
	// 保持：状态翻转生效、结果计 Activated。
	db, store, business := p0OpenScheduleSyncDB(t, false)
	p0SeedScheduledAccount(t, db, "acc-hook-degraded", "disabled")
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	var warnings []string
	logger := slog.New(slog.NewTextHandler(&p0LogSink{lines: &warnings}, nil))
	hook := newAccountScheduleActivationHook(business, logger)
	result, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook)
	if err != nil {
		t.Fatalf("activation side-effect failure must not abort the sync: %v", err)
	}
	if result.Activated != 1 {
		t.Fatalf("result=%+v", result)
	}
	status := ""
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id = 'acc-hook-degraded'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("status=%q, flip must survive the failed side effect", status)
	}
	if len(warnings) == 0 {
		t.Fatal("failed dispatch advance must be logged (best-effort warn)")
	}
}

// p0LogSink 聚合 warn 日志行，供 best-effort 分支断言。
type p0LogSink struct{ lines *[]string }

func (s *p0LogSink) Write(p []byte) (int, error) {
	*s.lines = append(*s.lines, string(p))
	return len(p), nil
}
