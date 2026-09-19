package main

// w1_compose_final_test.go —— compose.go 剩余未覆盖语句的收官批次（w1uFinal
// 前缀，TestW1UComposeFinal* 入口）。
//
// 目标行以 %TEMP%/w4-base-full.out（全量包测试 + coverprofile 的新鲜基线，
// compose.go 560 语句 / 83 语句零计数）为准，逐块 rg 对照当前源码核过可达性：
//
//	直接驱动（本文件）：
//	  899  GoRuntimeMetrics 开启且 store schema 校验失败（垃圾 sqlite 文件）
//	真实 dev PG 门禁（复用 w1g2CoverPostgres / w1g2OpenApp）：
//	  394  PG 模式缺 F3 audit 数据集连接池守卫臂
//	  995  链条开启且 spool 目录缺失的组合根 fail-fast 臂
//	  （增量）在临时子库补建 juhe_jobs 余额租约四表，使
//	  wireInProcessBalanceAndCatalogRefresh 的 OpenStore→CheckSchema→
//	  NewRunner→SetManualBalanceRefresher 成功路径首次真实跑通
//	  （compose_account_balance_refresh.go；此前 PG 组合根该 wire 只能走
//	  CheckSchema 降级臂）。
//
// 隔离铁律：只触碰临时子库 juhe_ai_sub2api_dev_w1cover 与（如需）Redis
// namespace juhe-ai:dev:w1cover；凭据只从 dev env 读取（w1pgEnvFile），
// env 缺失或 PG 不可达一律 t.Skip；输出不携带任何连接串/密码。
//
// 明确跳过的零计数块（死代码/不可注入，详见最终报告）：
//   - crypto/rand 与 sql.Open 的错误臂（216/283/324/348/367/405/423/440/593/1529）：
//     惰性打开或系统熵源不可注入；
//   - SQLite 缺路径臂（320/344/433/589/436）与 configure 臂（328/352/597）：
//     ensureGatewaySQLiteStoragePreflight 对同一批路径先行校验/建库；
//   - store 构造器错误臂（458..877 区间）：构造均为惰性结构初始化，同一
//     compose 调用内首个共享库失败即短路，后续构造器不可达；
//   - 489/493（runtime-log 保留天数闭包的读错误/解析失败臂）与
//     1440/1444/1446/1449/1451/1457..1477（ratelimitSettingsProvider 闭包的
//     nil/非 float64/解析失败/越界臂）与 1582（aiAccountLimitSettingsAdapter
//     default 臂）：settings.Store.Load 在读取侧逐行 normalizeSystemSetting
//     （store.go loadFromDatabase），非法值在 Load 即整包报错，闭包永远只
//     见到已归一化的合法 float64；IP 限流中间件又先于路由 Load 并 500 短路，
//     且 60s 快照缓存使“请求后改库”必然失效；
//   - 1578/1580（adapter int64/int 分支）：快照解码只产生 float64；
//   - 904/1015/1029/1044/1137/1148/1161/1168/1039/1040：受恒真守卫、更早
//     fail-fast 或仅活链派发触发的闭包保护（详见最终报告）。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

// ---------------------------------------------------------------------------
// compose.go:899 —— GoRuntimeMetrics 开启且 EnsureReady schema 校验失败
// ---------------------------------------------------------------------------

func TestW1UComposeFinalGoMetricsEnsureReadyArm(t *testing.T) {
	stack := w1oNewComposeStack(t)
	// 失败臂不回收已打开的 SQLite 句柄（Windows 句柄锁会破坏 t.TempDir
	// 清理），与其他失败臂用例一致改走手管目录 + 尽力清理。
	w1oRedirectDatabasePaths(t, stack)
	garbage := filepath.Join(t.TempDir(), "w1u-final-gometrics.sqlite3")
	if err := os.WriteFile(garbage, []byte("w1u-final: this is not a sqlite database"), 0o644); err != nil {
		t.Fatalf("写入垃圾 metrics 文件失败: %v", err)
	}
	stack.cfg.GoRuntimeMetrics = gometrics.Config{
		Enabled:       true,
		Store:         gometrics.DialectSQLite,
		DatabasePath:  garbage,
		Service:       "juhe-ai",
		Role:          "gateway",
		Interval:      time.Second,
		RetentionDays: 30,
	}
	composed, err := stack.compose(t)
	if err == nil {
		if composed != nil {
			composed.Shutdown()
		}
		t.Fatal("垃圾 Go metrics 库必须使组合根 fail-fast，实际 err=nil")
	}
	if composed != nil {
		t.Fatalf("失败臂不得返回组合根")
	}
	if !strings.Contains(err.Error(), "verify Go runtime metrics schema") {
		t.Fatalf("错误 = %v，want 包含 %q", err, "verify Go runtime metrics schema")
	}
}

// ---------------------------------------------------------------------------
// compose.go:394 / 995 —— PG 模式守卫臂 + juhe_jobs 四表驱动的余额 wire 成功路径
// ---------------------------------------------------------------------------

// w1uFinalBalanceJobsSchema 复刻 shared accountbalance store.go
// balancePostgresSchema 的预置 DDL（juhe_jobs schema 由外部 bootstrap 创建、
// 只校验不建——checkPostgresSchema 的 owner/四表/列契约）。以临时子库应用
// 角色连接执行，保证 schema owner == current_user。
var w1uFinalBalanceJobsSchema = []string{
	`CREATE SCHEMA IF NOT EXISTS juhe_jobs`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_owner_leases (
	   lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL,
	   lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_account_leases (
	   account_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL,
	   lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_snapshots (
	   account_id TEXT PRIMARY KEY, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL,
	   trigger TEXT NOT NULL, snapshot_json JSONB NOT NULL, next_refresh_at TIMESTAMPTZ,
	   updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_outcomes (
	   outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL,
	   input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, trigger TEXT NOT NULL,
	   observed_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL,
	   committed BOOLEAN NOT NULL DEFAULT FALSE)`,
	`CREATE INDEX IF NOT EXISTS idx_account_balance_outcomes_account
	   ON juhe_jobs.account_balance_outcomes(account_id, observed_at)`,
}

// w1uFinalEnsureBalanceJobsSchema 在临时子库幂等补建 juhe_jobs 余额租约四表。
func w1uFinalEnsureBalanceJobsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range w1uFinalBalanceJobsSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("补建 juhe_jobs 余额契约 DDL 失败: %v, statement=%s", err, statement)
		}
	}
}

// w1uFinalPGComposeFixture 聚合 PG 组合根三套依赖：F4 操作日志 store+租约、
// F3 审计 store+租约+producer、共享 pgpool registry。auditConfig 携带有效
// PG 池（子测试按需拷贝并置 nil 驱动 394 守卫臂）。
type w1uFinalPGComposeFixture struct {
	tempAppURL     string
	pools          *pgpool.Registry
	operationStore operationlog.Store
	operationLease *operationlog.LeaseKeeper
	auditConfig    auditlog.Config
	auditProducer  *auditlog.Producer
	cfg            runtimeConfig
}

func w1uFinalNewPGComposeFixture(t *testing.T) *w1uFinalPGComposeFixture {
	t.Helper()
	tempAppURL := w1g2CoverPostgres(t)
	// 任务增量：临时子库补建 juhe_jobs 四表，使 PG 组合根的进程内余额
	// 手动刷新 wire 走成功路径（OpenStore → CheckSchema → NewRunner）。
	jobsDB := w1g2OpenApp(t, tempAppURL)
	w1uFinalEnsureBalanceJobsSchema(t, jobsDB)

	root := t.TempDir()
	for _, name := range []string{"audit-blobs", "audit-hot", "chat-assets", "usage-spool"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("创建运行时目录 %s 失败: %v", name, err)
		}
	}

	// ---- F4 操作日志（PG 模式，镜像 TestW1G2 装配） ----
	operationConfig := operationlog.Config{
		Enabled:              true,
		Mode:                 operationlog.ModePostgres,
		InstanceID:           "w1u-final-pg",
		PostgresURL:          tempAppURL,
		PostgresMaxOpenConns: 8,
		PostgresMaxIdleConns: 2,
		OwnerLease:           30 * time.Second,
		RetentionInterval:    time.Minute,
		RetentionDays:        365,
		RetentionBatchSize:   100,
	}
	operationStore, err := operationlog.OpenStore(operationConfig)
	if err != nil {
		t.Fatalf("打开 F4 PG 操作日志 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	if err := operationStore.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("初始化 F4 PG 操作日志 schema 失败: %v", err)
	}
	operationLease, ok, err := operationlog.StartLeaseKeeper(context.Background(), operationStore, operationConfig.InstanceID, 30*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("启动 F4 PG 操作日志租约失败: ok=%v err=%v", ok, err)
	}
	t.Cleanup(operationLease.Close)

	// ---- F3 审计（PG 模式，镜像 TestW1G2 装配） ----
	pools := pgpool.NewRegistry()
	t.Cleanup(func() { _ = pools.Close() })
	auditPooled, err := pools.Acquire(tempAppURL, "gateway-store", 8, 2)
	if err != nil {
		t.Fatalf("打开 F3 审计共享池失败: %v", err)
	}
	auditConfig := auditlog.Config{
		Mode:                     auditlog.ModePostgres,
		InstanceID:               "w1u-final-pg",
		PostgresURL:              tempAppURL,
		PostgresPool:             auditPooled,
		PayloadBlobDirectory:     filepath.Join(root, "audit-blobs"),
		HotSearchDirectory:       filepath.Join(root, "audit-hot"),
		OwnerLease:               30 * time.Second,
		RetentionInterval:        time.Minute,
		RetentionBatchSize:       100,
		SuccessHotRetentionHours: 1,
		SuccessSampleRate:        0.1,
		SuccessRetentionDays:     3,
		ProblemRetentionDays:     7,
	}
	auditStore, err := auditlog.OpenStore(auditConfig)
	if err != nil {
		t.Fatalf("打开 F3 PG 审计 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = auditStore.Close() })
	if err := auditStore.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("初始化 F3 PG 审计 schema 失败: %v", err)
	}
	auditKeeper, ok, err := auditlog.StartLeaseKeeper(context.Background(), auditStore, auditConfig.InstanceID, 30*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("启动 F3 PG 审计租约失败: ok=%v err=%v", ok, err)
	}
	t.Cleanup(auditKeeper.Close)
	auditProducer := auditlog.NewProducer(auditStore, auditKeeper.Lease(), auditConfig, producerLogger{})

	cfg := runtimeConfig{
		// standalone + memory 驱动：performance 模式的热质量运行时强制要求
		// redis runtime state（composeChainRuntimeServices 守卫），与组合根
		// 守卫臂无关；SQLite 变体测试已证明 standalone/memory 链条可完整装配。
		RuntimeMode:                   "standalone",
		DatabaseDriver:                "postgres",
		PostgresURL:                   tempAppURL,
		BusinessPostgresURL:           tempAppURL,
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "memory",
		RedisNamespace:                w1g2RedisNamespace,
		Secret:                        "w1u-final-compose-secret",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
		UsageSpoolDirectory:           filepath.Join(root, "usage-spool"),
		ChatAssetsRoot:                filepath.Join(root, "chat-assets"),
		ChatMaxTurnsPerConversation:   50,
		ChatRetentionDays:             3,
		ChatToolEnvironment:           "development",
		BusinessOwner:                 "gateway",
		BusinessHandoffConfirmed:      true,
		BusinessNodeWriterStopped:     true,
		BusinessSchemaReady:           true,
		BusinessOwnerEpoch:            "epoch-w1u-final",
		SystemAPIEnabled:              true,
		ChainEnabled:                  true,
		CaptchaDisabled:               true,
		AuditLogEnabled:               true,
	}
	return &w1uFinalPGComposeFixture{
		tempAppURL:     tempAppURL,
		pools:          pools,
		operationStore: operationStore,
		operationLease: operationLease,
		auditConfig:    auditConfig,
		auditProducer:  auditProducer,
		cfg:            cfg,
	}
}

func TestW1UComposeFinalPostgresAuditPoolAndSpoolArms(t *testing.T) {
	fixture := w1uFinalNewPGComposeFixture(t)

	t.Run("缺F3审计数据集连接池", func(t *testing.T) {
		auditConfigWithoutPool := fixture.auditConfig
		auditConfigWithoutPool.PostgresPool = nil
		composed, err := composeSystemAPI(fixture.cfg, fixture.pools, fixture.operationStore, fixture.operationLease, fixture.auditProducer, auditConfigWithoutPool, composeTestOwnerHealth())
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("PG 模式缺 F3 audit 池必须 fail-fast，实际 err=nil")
		}
		if composed != nil {
			t.Fatal("失败臂不得返回组合根")
		}
		if !strings.Contains(err.Error(), "postgres 模式缺少 F3 audit 数据集连接池") {
			t.Fatalf("错误 = %v，want 包含 %q", err, "postgres 模式缺少 F3 audit 数据集连接池")
		}
	})

	t.Run("链条缺spool目录", func(t *testing.T) {
		cfg := fixture.cfg
		cfg.UsageSpoolDirectory = ""
		cfg.StatsDatabasePath = ""
		composed, err := composeSystemAPI(cfg, fixture.pools, fixture.operationStore, fixture.operationLease, fixture.auditProducer, fixture.auditConfig, composeTestOwnerHealth())
		if err == nil {
			if composed != nil {
				composed.Shutdown()
			}
			t.Fatal("链条开启且 spool 目录缺失必须 fail-fast，实际 err=nil")
		}
		if composed != nil {
			t.Fatal("失败臂不得返回组合根")
		}
		if !strings.Contains(err.Error(), "AI 网关链缺少用量 spool 目录") {
			t.Fatalf("错误 = %v，want 包含 %q", err, "AI 网关链缺少用量 spool 目录")
		}
	})

	t.Run("完整成功加余额wire", func(t *testing.T) {
		composed, err := composeSystemAPI(fixture.cfg, fixture.pools, fixture.operationStore, fixture.operationLease, fixture.auditProducer, fixture.auditConfig, composeTestOwnerHealth())
		if err != nil {
			t.Fatalf("juhe_jobs 四表就绪后 PG 组合根必须组装成功（含进程内余额刷新 wire 成功路径）: %v", err)
		}
		defer composed.Shutdown()
		if !composed.pgDialect || composed.Kernel == nil || composed.Bus == nil || composed.DB == nil {
			t.Fatalf("PG 组合根关键字段缺失: pgDialect=%v Kernel=%v Bus=%v DB=%v", composed.pgDialect, composed.Kernel != nil, composed.Bus != nil, composed.DB != nil)
		}
		if composed.chain == nil || composed.chainServices == nil || composed.chatDB == nil {
			t.Fatalf("链条开启时 chain/chainServices/chatDB 必须装配: chain=%v services=%v chat=%v", composed.chain != nil, composed.chainServices != nil, composed.chatDB != nil)
		}
		if composed.statsDB != composed.db {
			t.Fatal("PG 模式 statsDB 必须别名共享业务池句柄")
		}
		var one int
		if err := composed.DB.QueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("业务 PG 句柄 Query(SELECT 1) 失败: one=%d err=%v", one, err)
		}
		// /health 免鉴权契约保持 200。
		server := httptest.NewServer(composed.Kernel)
		defer server.Close()
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get(server.URL + "/__aisys__/api/health")
		if err != nil {
			t.Fatalf("GET /health 失败: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /health 期望 200，实际 %d", response.StatusCode)
		}
	})
}
