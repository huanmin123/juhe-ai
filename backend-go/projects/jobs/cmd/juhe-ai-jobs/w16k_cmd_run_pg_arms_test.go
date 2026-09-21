// 波次 w16k（PG 门禁）：run() 与 J2 manual 桥的剩余 PG 臂补齐。
// 连接串只从 shared.env 读取改写（w12cPGOverrideURL），连接失败 t.Skip；
// 凭据不落日志；数据与标识一律 w16k- 前缀。
//
// 覆盖清单（对照 w16k_state.cov 零块）：
//   - main.go 230：model-recovery 在 J1 PG reader 就绪后 OpenRedisStore
//     解析失败的 fail-fast 臂；
//   - main.go 805：/account-balance/manual 请求缺 trigger 时的 manual 缺省
//     臂（随后走缺失账户错误路径）。
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w16kSeedPGJ1 预置 J1 PG 前置：juhe_jobs schema + w14j 权威种子语句 +
// sys_admin 行（加法幂等；形状漂移按约定 t.Skip，不静默吞掉）。
func w16kSeedPGJ1(t *testing.T, pgURL string) {
	t.Helper()
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w16k: 打开覆盖库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w16k: 覆盖库不可达: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_jobs`); err != nil {
		t.Skipf("w16k: 建立 juhe_jobs schema 失败: %v", err)
	}
	for _, statement := range w14jPGSeedStatements() {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
				continue
			}
			t.Logf("w16k pg seed 跳过: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w16k-test-seed-not-a-login-hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Skipf("w16k: sys_admin seed 失败（共享库形状漂移，跳过）: %v", err)
	}
}

// TestW16KModelRecoveryRedisOpenFailArm 锁定 run() 的 model-recovery
// Redis store 打开失败臂（230-232）：J1 PG store+input 装配成功、reader
// 就绪后，JUHE_AI_REDIS_STATE_URL 非法必须在 OpenRedisStore 处 fail-fast。
func TestW16KModelRecoveryRedisOpenFailArm(t *testing.T) {
	if testing.Short() {
		t.Skip("w16k PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16k: 覆盖库连接串不可用")
	}
	w16kSeedPGJ1(t, pgURL)

	env := w14qPGMainEnv(t, t.TempDir(), pgURL, "w16k-not-a-redis-url", wgFreePort(t))
	w16hApplyEnv(t, env)
	w16hRunArms(t, nil, 1, "open model-recovery Redis store")
}

// TestW16KWithLeaseSkippedAndPartialArms 锁定 workerAssembly.withLease 的两个
// outcome 映射臂（worker_assembly.go 801-808）：
//  1. 租约被他方预占 → RunWithScheduledLease 返回 skipped，任务不得执行；
//  2. 运行中租约行被删 → renewal 失败 → 任务完成后释放未命中 → partial。
func TestW16KWithLeaseSkippedAndPartialArms(t *testing.T) {
	if testing.Short() {
		t.Skip("w16k PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16k: 覆盖库连接串不可用")
	}
	w16kSeedPGJ1(t, pgURL)
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:               "postgres",
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err := assembly.wireTaskRunsFamily(context.Background()); err != nil {
		t.Fatalf("装配 task-runs 家族失败: %v", err)
	}

	// 1. 租约 busy → skipped：预占同名租约后再跑 wrapped 任务。
	acquired, err := assembly.taskRunsStore.TryAcquireScheduledLease(context.Background(), taskruns.ScheduledLeaseAcquireInput{
		JobName: "w16k-lease-busy", OwnerID: "w16k-other-owner", RunID: "w16k-hold", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("预占 w16k 租约失败: %v", err)
	}
	if !acquired.Acquired || acquired.Lease == nil {
		t.Fatalf("预占 w16k 租约必须成功: %+v", acquired)
	}
	t.Cleanup(func() { _, _ = assembly.taskRunsStore.ReleaseScheduledLease(context.Background(), *acquired.Lease) })
	busyTask := assembly.withLease("w16k-lease-busy", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		t.Error("租约被预占时任务不得执行")
		return jobsched.TaskResult{}, nil
	})
	busyResult, err := busyTask(context.Background(), jobsched.TaskContext{})
	if err != nil {
		t.Fatalf("租约 busy 必须跳过而不是失败: %v", err)
	}
	if busyResult.Outcome != jobsched.OutcomeSkipped || !strings.Contains(busyResult.Warning, "lease_busy") {
		t.Fatalf("租约 busy 必须映射 skipped+lease_busy: %+v", busyResult)
	}

	// 2. 任务返回前租约行被删 → 释放未命中 → partial。任务在首个续租 tick
	// （TTL 3s → 1s）之前删行并立即返回，释放 DELETE 落空是确定性的。
	pgDB, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w16k: 打开覆盖库失败: %v", err)
	}
	t.Cleanup(func() { _ = pgDB.Close() })
	leaseKey := taskruns.ScheduledLeaseKey("w16k-lease-partial", "global")
	partialTask := assembly.withLease("w16k-lease-partial", 3*time.Second, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := pgDB.Exec(`DELETE FROM juhe_stats.background_job_leases WHERE lease_key = $1`, leaseKey); err != nil {
			t.Logf("w16k: 删除租约行失败（释放将命中，partial 臂可能不触发）: %v", err)
		}
		return jobsched.TaskResult{}, nil
	})
	partialResult, err := partialTask(context.Background(), jobsched.TaskContext{})
	if err != nil {
		t.Fatalf("租约丢失但任务成功时不得返回错误: %v", err)
	}
	if partialResult.Outcome != jobsched.OutcomePartial || !strings.Contains(partialResult.Warning, "租约释放未命中") {
		t.Fatalf("租约释放未命中必须映射 partial: %+v", partialResult)
	}
}

// TestW16KManualBridgeTriggerDefaultArm 锁定 /account-balance/manual 的
// trigger 缺省臂（805-807）：合法信封不带 trigger 时必须穿过 JSON 解码、
// 尾随检查并执行 Trigger=manual 缺省，随后因账户缺失收敛为 502。
func TestW16KManualBridgeTriggerDefaultArm(t *testing.T) {
	if testing.Short() {
		t.Skip("w16k PG 门禁臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16k: 覆盖库连接串不可用")
	}
	bootstrap, err := accountbalance.OpenStore(accountbalance.StoreConfig{
		Mode: accountbalance.StorePostgres, PostgresURL: pgURL,
	})
	if err != nil {
		t.Skipf("w16k: 打开 J2 store 失败（跳过）: %v", err)
	}
	if err := bootstrap.EnsureSchema(context.Background()); err != nil {
		_ = bootstrap.Close()
		t.Skipf("w16k: J2 schema bootstrap 失败（跳过）: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Logf("w16k: J2 bootstrap 关闭: %v", err)
	}
	service, err := accountbalance.NewService(accountbalance.RuntimeConfig{
		Enabled:              true,
		OwnerID:              "w16k-manual-bridge",
		Store:                accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL},
		BusinessPostgresURL:  pgURL,
		CredentialSecret:     "0123456789abcdef0123456789abcdef",
		InputTTL:             time.Minute,
		PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2,
		InputPostgresMaxOpenConns: 4, InputPostgresMaxIdleConns: 2,
	}, slog.Default())
	if err != nil {
		t.Skipf("w16k: J2 NewService 失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, nil,
		true, func() bool { return true },
		service, "w16k-manual-secret-0123456789abcdef",
		false, func() bool { return true },
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		nil, nil,
		false, func() bool { return true }, func() map[string]any { return nil },
	)
	request := httptest.NewRequest(http.MethodPost, "/account-balance/manual",
		strings.NewReader(`{"input":{"account_id":"w16k-missing","system_account_id":"sys_admin","config_revision":1}}`))
	request.Header.Set("Authorization", "Bearer w16k-manual-secret-0123456789abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("缺省 trigger 的缺失账户请求必须穿过到 RunManual 并返回 502，得到 %d body=%s",
			response.Code, response.Body.String())
	}
}
