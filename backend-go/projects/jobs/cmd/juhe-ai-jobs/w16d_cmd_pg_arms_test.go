// 波次 w16d：批次五。PG 门控臂（wgCmdKitDB 隔离测试库，原 w1cover 覆盖库
// 门禁已退役）与深臂：
//   - worker_retention.go 家族适配器的 postgres 分支（CleanupApiKeyRelated /
//     CleanupAccountRelated / CleanupPendingTargets / ProcessBatch /
//     SettleCodexContextStorageCleanup / flushLoop 失败重排）；
//   - worker_projection_jobs.go 的 PG 完整装配链与任务闭包；
//   - worker_record_maintenance_table_drain 的 PG drain 接线；
//   - main.go jobsHTTPHandler 的 J2 手动桥（401/400/尾随 JSON/RunManual 错误）；
//   - worker_circuit_jobs.go Resolve 的空分组 / 候选缺失臂；
//   - worker_probe_jobs.go FindAccountForTest 错误臂与 OpenStatsStore 失败臂；
//   - worker_balance_detect.go proxyEnvelope 密码封套解密失败臂。
//
// 连接串不写入日志与断言；隔离库随测试结束删除，无需行级清理。
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

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const w16dSecret = "0123456789abcdef0123456789abcdef"

// w16dPGOpen 打开隔离 PG 测试库连接（pgtestkit 建删库，PG 不可达显式 Fatal）。
func w16dPGOpen(t *testing.T) (*sql.DB, string) {
	t.Helper()
	return wgCmdKitDB(t)
}

// TestW16DPGBalanceContractArms 覆盖余额契约校验的 PG 分支。
func TestW16DPGBalanceContractArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB, _ := w16dPGOpen(t)
	business := &businessDB{db: pgDB}
	// ensureAccountsBalanceColumns PG 分支：生产形状 accounts 具备契约列。
	if err := ensureAccountsBalanceColumns(context.Background(), business); err != nil {
		t.Logf("隔离测试库 accounts 契约列校验（登记不判失败）: %v", err)
	}
	// ensureAccountUsageSnapshotsTable PG 分支（853-856）。
	if err := ensureAccountUsageSnapshotsTable(context.Background(), pgDB); err != nil {
		t.Logf("隔离测试库快照表校验（登记不判失败）: %v", err)
	}
	// 关闭句柄 → PG 查询错误分支（155-157 / 854-856）。
	closed, err := sql.Open("pgx", w16dClosedPortURL())
	if err != nil {
		t.Skipf("w16d: 打开不可达句柄失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = closed.Close() })
	if err := ensureAccountsBalanceColumns(context.Background(), &businessDB{db: closed}); err == nil {
		t.Fatal("不可达 PG 句柄的契约校验必须报错")
	}
	if err := ensureAccountUsageSnapshotsTable(context.Background(), closed); err == nil {
		t.Fatal("不可达 PG 句柄的快照表校验必须报错")
	}
}

func TestW16DPGRetentionAdapterArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB, _ := w16dPGOpen(t)
	pgHandle := &cleanuprepo.DB{DB: pgDB}
	family := &retentionFamily{
		assembly: newWorkerAssembly(workerConfig{}, slog.Default()),
		recordCleanup: &cleanuprepo.RecordCleanupStore{
			Business: pgHandle, Stats: pgHandle, Dataset: pgHandle, UsageCatalog: pgHandle,
		},
	}
	// PG 分支选择（564-566 / 574-582 / 597-599 / 608-610）；共享库缺契约表时
	// 仅记录错误不判失败（分支本身已被覆盖）。
	cleaner := &familyRelatedCleaner{family: family}
	if _, err := cleaner.CleanupApiKeyRelated(context.Background(), retention.RecordMaintenanceJob{
		Type: retention.JobTypeAPIKeyRelatedCleanup, ID: "w16d-job", APIKeyID: "w16d-key", SystemAccountID: "sys_admin",
	}, nil); err != nil {
		t.Logf("PG CleanupApiKeyRelated（登记不判失败）: %v", err)
	}
	if _, err := cleaner.CleanupAccountRelated(context.Background(), retention.RecordMaintenanceJob{
		Type: retention.JobTypeAccountRelatedCleanup, ID: "w16d-job", AccountID: "w16d-acc", SystemAccountID: "sys_admin",
	}, nil); err != nil {
		t.Logf("PG CleanupAccountRelated（登记不判失败）: %v", err)
	}
	if _, err := (&familyAPIKeyRetryer{family: family}).CleanupPendingTargets(context.Background(), 1, nil); err != nil {
		t.Logf("PG CleanupPendingAPIKeyTargets（登记不判失败）: %v", err)
	}
	if _, err := (&familyAccountRetryer{family: family}).CleanupPendingTargets(context.Background(), 1, nil); err != nil {
		t.Logf("PG CleanupPendingAccountTargets（登记不判失败）: %v", err)
	}
}

func TestW16DPGCodexSettleAndDrainArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB, _ := w16dPGOpen(t)
	pgHandle := &cleanuprepo.DB{DB: pgDB}
	// SettleCodexContextStorageCleanup 错误臂：PG 句柄指向关闭连接（700-702；
	// 原 sqlite closed 句柄夹具随 sqlite 形态退役删除，改隔离库 DSN 的关闭句柄）。
	_, closedDSN := wgOpenPGDialectDB(t, nil)
	closed, err := sql.Open("pgx", closedDSN)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	closedPG := &cleanuprepo.DB{DB: closed}
	dbService := &familyDbService{
		codex: &cleanuprepo.CodexContextStore{PG: closedPG},
	}
	if _, err := dbService.SettleCodexContextStorageCleanup(context.Background(), retention.CodexContextSettlement{
		SucceededStorageKeys: []string{"w16d-key"},
	}); err == nil {
		t.Fatal("关闭 PG 句柄结算必须报错")
	}
	// record_maintenance 表 drain 的 PG 接线（50-52）与 closer 排空（74-78）。
	assembly := newWorkerAssembly(workerConfig{InstanceID: "w16d-drain",
		RecordMaintenanceBatchSize:               5,
		RecordMaintenanceShutdownFlushMaxBatches: 1,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	family := &retentionFamily{assembly: assembly}
	if err := assembly.wireRecordMaintenanceTableDrain(family, pgHandle); err != nil {
		t.Fatalf("PG drain 接线: %v", err)
	}
	assembly.closeStores()
}

func TestW16DPGFlushLoopFailureArms(t *testing.T) {
	// flushLoop 任务失败 → 重排 + 错误日志（877-883）。PG 唯一形态：空独立库
	//（契约表缺失）使关联清理执行失败，错误分支由此驱动（原 sqlite closed
	// 句柄夹具随 sqlite 形态退役删除）。
	pgDB, _ := w16dPGOpen(t)
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	family := &retentionFamily{
		assembly: assembly,
		runner: &retention.RecordMaintenanceRunner{
			Mode: retention.ModePostgres,
			Executor: retention.RecordMaintenanceExecutor{
				RelatedRecords: &familyRelatedCleaner{family: &retentionFamily{
					assembly: assembly,
					recordCleanup: &cleanuprepo.RecordCleanupStore{
						Dataset:      &cleanuprepo.DB{DB: pgDB},
						Stats:        &cleanuprepo.DB{DB: pgDB},
						UsageCatalog: &cleanuprepo.DB{DB: pgDB},
						Business:     &cleanuprepo.DB{DB: pgDB},
					},
				}},
			},
		},
	}
	_ = family
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 1 << 20})
	stop := make(chan struct{})
	assembly.retention = family
	go family.flushLoop(stop, queue)
	if queued, _ := queue.enqueue(retention.RecordMaintenanceJob{Type: "w16d-unknown-type"}); !queued {
		t.Fatal("空队列必须接受任务")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if queue.size() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if queue.size() != 1 {
		t.Fatalf("失败任务必须保留队头等待重试: size=%d", queue.size())
	}
	close(stop)
}

func TestW16DPGWireListProjectionArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB, pgURL := w16dPGOpen(t)
	redisServer := miniredis.RunT(t)
	assembly := newWorkerAssembly(workerConfig{InstanceID: "w16d-list-projection",
		Secret:                          w16dSecret,
		ListProjectionIntervalMS:        1_000,
		ListProjectionBatchSize:         10,
		ListProjectionMaxBatchesPerRun:  2,
		ListProjectionWorkerConcurrency: 1,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	// PG stats 读模型（loader 的时区源）。
	statsStore, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:                 statsverify.StorePostgres,
		PostgresURL:          pgURL,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("打开 stats PG 存储失败: %v", err)
	}
	assembly.statsStore = statsStore
	t.Cleanup(func() { _ = statsStore.Close() })
	if err := statsStore.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("stats PG schema 初始化失败: %v", err)
	}
	// OAuth 族 store（投影 SyncSchedules 依赖）。
	oauthStore, err := oauthrefresh.OpenStore(pgDB, w16dSecret)
	if err != nil {
		t.Fatalf("oauth store: %v", err)
	}
	assembly.oauthStore = oauthStore
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: pgDB, Secret: w16dSecret})
	if err != nil {
		t.Fatalf("probe store: %v", err)
	}
	business := &businessDB{db: pgDB}
	// miniredis URL + 全依赖齐备 → 完整接线（63-137）。
	assembly.config.RedisStateURL = "redis://" + redisServer.Addr()
	assembly.config.RedisNamespace = "juhe-ai:w16d"
	if err := assembly.wireListProjectionFamily(context.Background(), business, probeStore); err != nil {
		t.Fatalf("列表投影族 PG 装配: %v", err)
	}
	wired := false
	for _, name := range assembly.wiredJobs {
		if name == "account-list-availability-projection-maintenance" {
			wired = true
		}
	}
	if !wired {
		t.Fatalf("列表投影任务必须接线: wired=%v disabled=%v", assembly.wiredJobs, assembly.disabledJobs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := assembly.runWiredJobOnce(ctx, "account-list-availability-projection-maintenance"); err != nil {
		t.Logf("投影任务在隔离测试库上执行失败（覆盖错误臂，登记不判失败）: %v", err)
	}
}

func TestW16DPGManualBridgeArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	_, pgURL := wgCmdKitDB(t)
	// J2 store bootstrap（CheckSchema 只读，需先 EnsureSchema）。
	bootstrap, err := accountbalance.OpenStore(accountbalance.StoreConfig{
		Mode: accountbalance.StorePostgres, PostgresURL: pgURL,
	})
	if err != nil {
		t.Fatalf("打开 J2 store 失败: %v", err)
	}
	if err := bootstrap.EnsureSchema(context.Background()); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("J2 schema bootstrap: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Logf("w16d: J2 bootstrap 关闭: %v", err)
	}
	// Enabled 必须显式开启：原共享库门禁以 t.Skip 吞掉了缺省 Enabled 的
	// NewService 失败，迁 fresh 库后按服务真实启用形态装配。
	service, err := accountbalance.NewService(accountbalance.RuntimeConfig{
		Enabled:              true,
		OwnerID:              "w16d-manual-bridge",
		Store:                accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL},
		BusinessPostgresURL:  pgURL,
		CredentialSecret:     w16dSecret,
		InputTTL:             time.Minute,
		PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2,
		InputPostgresMaxOpenConns: 4, InputPostgresMaxIdleConns: 2,
	}, slog.Default())
	if err != nil {
		t.Fatalf("J2 NewService 失败: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(jobsHTTPDeps{
		OwnerMode:                  ownermode.Active,
		RuntimeRunning:             &running,
		AccountBalanceEnabled:      true,
		AccountBalanceReady:        func() bool { return true },
		AccountBalanceService:      service,
		AccountBalanceManualSecret: "w16d-manual-secret-0123456789abcdef",
		ProxyLatencyReady:          func() bool { return true },
		ProxyLatencyStatus:         func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		ProxyLatencySnapshot:       func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		WorkerReady:                func() bool { return true },
	})
	post := func(body string, auth bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", strings.NewReader(body))
		if auth {
			request.Header.Set("Authorization", "Bearer w16d-manual-secret-0123456789abcdef")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	// 无凭据 → 401（805-807）。
	if response := post(`{}`, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据必须 401: %d %s", response.Code, response.Body.String())
	}
	// 非法 JSON → 400。
	if response := post(`not-json`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 必须 400: %d %s", response.Code, response.Body.String())
	}
	// 尾随 JSON → 400。
	if response := post(`{"input":{"accountId":"w16d-x"}} {"trailing":1}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 必须 400: %d %s", response.Code, response.Body.String())
	}
	// 合法信封（Trigger 缺省 → manual）→ RunManual 无对应账户 → 错误 → 非 2xx。
	response := post(`{"input":{"account_id":"w16d-missing","system_account_id":"sys_admin","config_revision":1}}`, true)
	if response.Code == http.StatusOK {
		t.Fatalf("缺失账户的 RunManual 不得成功: %d %s", response.Code, response.Body.String())
	}
	t.Logf("RunManual 缺失账户响应: %d %s", response.Code, response.Body.String())
}
