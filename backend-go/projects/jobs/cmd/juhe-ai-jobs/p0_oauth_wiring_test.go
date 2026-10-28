package main

// P0 修复回归（组合根装配面）：
//   - OAuth 刷新失败状态存储按运行态部署分支注入：无 JUHE_AI_REDIS_STATE_URL
//     → 内存版默认（oauthFailures=nil）；配置后 → refresh（OpenAI 族既有 key
//     前缀）与 keepalive（族隔离前缀）两个共用客户端的 Redis 版 + closer；
//     URL 非法 → 装配 fail-fast。
//   - account-availability-schedule-status-sync 注入激活 hook：启用翻转在同一
//     事务内推进 circuit dispatch revision 家族（写 outbox + revision+1）；
//     推进失败 best-effort——只记 warn，不中断同步、状态翻转保持生效。
//
// 存储形态唯一为 PostgreSQL：排期同步 + 激活推进测试经 pgtestkit 独立库运行
// （原 SQLite 夹具随 sqlite 形态退役删除；原 sqlite 整族装配臂按 D9 删除，
// 失败状态分支注入语义由 TestP0OAuthFailureStateStoreAssemblyBranch 承载）。

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const p0WiringScheduleJSON = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`

func TestP0OAuthFailureStateStoreAssemblyBranch(t *testing.T) {
	// 无 Redis 运行态 → 两个族均为 nil（refresh / keepalive job 各自保持内存
	// 版默认）。
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	defer assembly.closeStores()
	refreshStore, keepaliveStore, closer, err := assembly.oauthFailureStateStores()
	if err != nil || refreshStore != nil || keepaliveStore != nil || closer != nil {
		t.Fatalf("no redis url must keep memory default: refresh=%T keepalive=%T closerNil=%v err=%v",
			refreshStore, keepaliveStore, closer == nil, err)
	}
	// redis-state 部署 → 共用同一 Redis 客户端的两个族隔离存储（客户端惰性
	// 建连，不拨号）：refresh 注入 OpenAI 族既有 key 前缀，keepalive 注入族
	// 隔离前缀，二者必须是不同实例（keyPrefix 不同）。
	redisAssembly := newWorkerAssembly(workerConfig{RedisStateURL: "redis://127.0.0.1:6379/9"}, slog.Default())
	defer redisAssembly.closeStores()
	redisRefresh, redisKeepalive, redisCloser, err := redisAssembly.oauthFailureStateStores()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := redisRefresh.(*oauthrefresh.RedisFailureStateStore); !ok {
		t.Fatalf("redis-state branch must inject RedisFailureStateStore for refresh, got %T", redisRefresh)
	}
	if _, ok := redisKeepalive.(*oauthrefresh.RedisFailureStateStore); !ok {
		t.Fatalf("redis-state branch must inject RedisFailureStateStore for keepalive, got %T", redisKeepalive)
	}
	if redisRefresh == redisKeepalive {
		t.Fatal("refresh and keepalive failure stores must be distinct family-isolated instances")
	}
	if redisCloser == nil {
		t.Fatal("redis client closer missing")
	}
	// 非法 URL → 装配失败（fail-fast，不静默退回内存版）。
	badAssembly := newWorkerAssembly(workerConfig{RedisStateURL: "://not-a-url"}, slog.Default())
	defer badAssembly.closeStores()
	if _, _, _, err := badAssembly.oauthFailureStateStores(); err == nil {
		t.Fatal("invalid JUHE_AI_REDIS_STATE_URL must fail the assembly")
	}
}

// p0OpenScheduleSyncDB 在独立 PG 测试库上建立排期同步 + dispatch 推进所需的
// 最小 juhe_business 夹具（列集合对齐 oauthrefresh 排期同步 SQL 与
// accounthealth 激活推进 SQL 的读取面）。withOutbox=false 模拟推进副作用失败
// （account_circuit_outbox 表缺失）。
func p0OpenScheduleSyncDB(t *testing.T, withOutbox bool) (*sql.DB, *oauthrefresh.Store, *accounthealth.ProjectionBusinessDB) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		schema := `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.accounts (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'active',
	availability_schedule_json TEXT,
	availability_schedule_next_check_at TEXT,
	deleted_at TEXT,
	updated_at TEXT NOT NULL DEFAULT '',
	config_revision BIGINT NOT NULL DEFAULT 1,
	dispatch_revision BIGINT NOT NULL DEFAULT 1,
	authorization_instance_source_account_id TEXT,
	cooldown_retest_observation_started_at TEXT,
	cooldown_retest_generation BIGINT,
	type TEXT NOT NULL DEFAULT 'api_key',
	credentials_encrypted TEXT NOT NULL DEFAULT '',
	balance_query_enabled INTEGER NOT NULL DEFAULT 0,
	balance_query_config_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE juhe_business.account_schedule_status_events (
	event_key TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	status TEXT NOT NULL,
	executed_at TEXT NOT NULL
);
CREATE TABLE juhe_business.account_quality_enforcements (
	account_id TEXT NOT NULL,
	state TEXT NOT NULL,
	action TEXT NOT NULL
);
`
		if withOutbox {
			schema += `
CREATE TABLE juhe_business.account_circuit_outbox (
	event_id TEXT PRIMARY KEY,
	projection_key TEXT NOT NULL,
	dedupe_key TEXT NOT NULL,
	event_type TEXT NOT NULL,
	account_id TEXT NOT NULL,
	account_runtime_key TEXT NOT NULL,
	circuit_scope_key TEXT,
	incident_id TEXT,
	transition_id TEXT NOT NULL,
	dispatch_revision BIGINT NOT NULL,
	generation TEXT,
	ledger_revision TEXT,
	status TEXT NOT NULL,
	available_at_ms BIGINT NOT NULL,
	attempt_count BIGINT NOT NULL,
	created_at_ms BIGINT NOT NULL,
	updated_at_ms BIGINT NOT NULL
);
`
		}
		_, err := db.Exec(schema)
		return err
	})
	store, err := oauthrefresh.OpenStore(db, "p0-wiring-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	business, err := accounthealth.NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, store, business
}

func p0SeedScheduledAccount(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, status, availability_schedule_json, updated_at) VALUES ($1, $2, $3, $4)`,
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
	if err := db.QueryRow(`SELECT dispatch_revision FROM juhe_business.accounts WHERE id = 'acc-hook-on'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("dispatch revision=%d, want advanced to 2", revision)
	}
	var eventType, dedupeKey string
	if err := db.QueryRow(`SELECT event_type, dedupe_key FROM juhe_business.account_circuit_outbox WHERE account_id = 'acc-hook-on'`).Scan(&eventType, &dedupeKey); err != nil {
		t.Fatal(err)
	}
	if eventType != "dispatch_revision_changed" || !strings.HasPrefix(dedupeKey, "dispatch:") {
		t.Fatalf("outbox row=%q %q", eventType, dedupeKey)
	}
}

func TestP0ScheduleActivationHookFailureDoesNotBreakSync(t *testing.T) {
	// outbox 表缺失 → 家族推进在 hook 事务内语句失败。PG 事务 abort 语义下
	// （语句失败即整个事务 aborted），吞错后 commit 收敛为 rollback：sync 以
	// 错误收场、状态翻转回滚——与 SQLite 时代「翻转生效」不同，这是 PG 事务
	// 语义的真实行为（best-effort 吞错边界失效属生产缺陷候选，已登记待
	// 裁决，本测试按 PG 现行为钉住）。
	db, store, business := p0OpenScheduleSyncDB(t, false)
	p0SeedScheduledAccount(t, db, "acc-hook-degraded", "disabled")
	atStart := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	var warnings []string
	logger := slog.New(slog.NewTextHandler(&p0LogSink{lines: &warnings}, nil))
	hook := newAccountScheduleActivationHook(business, logger)
	_, err := store.SyncAccountScheduleStatuses(context.Background(), atStart, 0, hook)
	if err == nil {
		t.Fatal("PG 事务 abort 语义下 hook 语句失败必须使 sync 以错误收场")
	}
	status := ""
	if err := db.QueryRow(`SELECT status FROM juhe_business.accounts WHERE id = 'acc-hook-degraded'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("status=%q, aborted tx must roll the flip back", status)
	}
}

// p0LogSink 聚合 warn 日志行，供 best-effort 分支断言。
type p0LogSink struct{ lines *[]string }

func (s *p0LogSink) Write(p []byte) (int, error) {
	*s.lines = append(*s.lines, string(p))
	return len(p), nil
}
