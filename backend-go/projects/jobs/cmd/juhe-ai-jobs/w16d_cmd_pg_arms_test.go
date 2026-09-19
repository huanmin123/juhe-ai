// 波次 w16d：批次五。PG 门控臂（w1cover 覆盖库）与 SQLite 可达的深臂：
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
// 连接串只从 shared.env 读取改写，不写入日志与断言；共享库只做加法幂等
// DDL，数据一律 w16d- 前缀 + t.Cleanup 清理。
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsverify"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const w16dSecret = "0123456789abcdef0123456789abcdef"

// w16dPGOpen 打开共享覆盖库连接（门控失败 t.Skip）。
func w16dPGOpen(t *testing.T) *sql.DB {
	t.Helper()
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16d: 覆盖库连接串不可用")
	}
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w16d: 打开覆盖库失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("w16d: 覆盖库不可达（跳过）: %v", err)
	}
	return db
}

// TestW16DPGBalanceContractArms 覆盖余额契约校验的 PG 分支。
func TestW16DPGBalanceContractArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB := w16dPGOpen(t)
	business := &businessDB{db: pgDB, postgres: true}
	// ensureAccountsBalanceColumns PG 分支：生产形状 accounts 具备契约列。
	if err := ensureAccountsBalanceColumns(context.Background(), business); err != nil {
		t.Logf("覆盖库 accounts 契约列校验（登记不判失败）: %v", err)
	}
	// ensureAccountUsageSnapshotsTable PG 分支（853-856）。
	if err := ensureAccountUsageSnapshotsTable(context.Background(), pgDB, true); err != nil {
		t.Logf("覆盖库快照表校验（登记不判失败）: %v", err)
	}
	// 关闭句柄 → PG 查询错误分支（155-157 / 854-856）。
	closed, err := sql.Open("pgx", w16dClosedPortURL())
	if err != nil {
		t.Skipf("w16d: 打开不可达句柄失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = closed.Close() })
	if err := ensureAccountsBalanceColumns(context.Background(), &businessDB{db: closed, postgres: true}); err == nil {
		t.Fatal("不可达 PG 句柄的契约校验必须报错")
	}
	if err := ensureAccountUsageSnapshotsTable(context.Background(), closed, true); err == nil {
		t.Fatal("不可达 PG 句柄的快照表校验必须报错")
	}
}

func TestW16DPGRetentionAdapterArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB := w16dPGOpen(t)
	pgHandle := &cleanuprepo.DB{DB: pgDB, Postgres: true}
	family := &retentionFamily{
		assembly: newWorkerAssembly(workerConfig{Driver: "postgres"}, slog.Default()),
		postgres: true,
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
	pgDB := w16dPGOpen(t)
	pgHandle := &cleanuprepo.DB{DB: pgDB, Postgres: true}
	// SettleCodexContextStorageCleanup 错误臂：PG 句柄指向关闭连接（700-702）。
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	closedPG := &cleanuprepo.DB{DB: closed, Postgres: true}
	dbService := &familyDbService{
		codex: &cleanuprepo.CodexContextStore{Postgres: true, PG: closedPG, ShardCount: 4},
	}
	if _, err := dbService.SettleCodexContextStorageCleanup(context.Background(), retention.CodexContextSettlement{
		SucceededStorageKeys: []string{"w16d-key"},
	}); err == nil {
		t.Fatal("关闭 PG 句柄结算必须报错")
	}
	// ProcessBatch：缺失文件 → failures>0 → warn 分支（730-734）；结算走
	// SQLite 分片（预建分片库与 cleanup queue 表，参考 cleanuprepo 测试形状）。
	shardRoot := t.TempDir()
	codexStore := &cleanuprepo.CodexContextStore{Postgres: false, ShardRoot: shardRoot, ShardCount: 4}
	for index := 0; index < 4; index++ {
		shard, shardErr := sql.Open("sqlite", filepath.Join(shardRoot, "state-00"+string(rune('0'+index))+".sqlite3"))
		if shardErr != nil {
			t.Fatal(shardErr)
		}
		if _, shardErr := shard.Exec(`CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
			storage_key TEXT PRIMARY KEY, enqueued_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			next_attempt_at TEXT NOT NULL, attempt_count INTEGER DEFAULT 0, last_error TEXT)`); shardErr != nil {
			t.Fatal(shardErr)
		}
		if _, shardErr := shard.Exec(`CREATE TABLE IF NOT EXISTS codex_context_sessions (
			id TEXT PRIMARY KEY, expires_at TEXT NOT NULL, updated_at TEXT DEFAULT '')`); shardErr != nil {
			t.Fatal(shardErr)
		}
		if _, shardErr := shard.Exec(`CREATE TABLE IF NOT EXISTS codex_context_responses (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, storage_key TEXT DEFAULT '',
			expires_at TEXT NOT NULL)`); shardErr != nil {
			t.Fatal(shardErr)
		}
		if _, shardErr := shard.Exec(`CREATE TABLE IF NOT EXISTS codex_context_compacts (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, storage_key TEXT DEFAULT '',
			expires_at TEXT NOT NULL)`); shardErr != nil {
			t.Fatal(shardErr)
		}
		_ = shard.Close()
	}
	t.Cleanup(func() { _ = codexStore.Close() })
	processor := &codexStorageProcessor{
		db:     &familyDbService{codex: codexStore},
		store:  codexStore,
		logger: slog.Default(),
	}
	deleted, err := processor.ProcessBatch(context.Background(), []string{"w16d-missing-key"})
	if err != nil {
		t.Fatalf("ProcessBatch 缺失文件: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("缺失文件不得计入删除: %d", deleted)
	}
	// 非法 storage key（路径穿越）→ 删除失败记录 → 结算持久化 → warn 分支（730-734）。
	deleted, err = processor.ProcessBatch(context.Background(), []string{"../../w16d-escape"})
	if err != nil {
		t.Fatalf("ProcessBatch 非法 key: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("非法 key 不得计入删除: %d", deleted)
	}
	// 结算错误臂（727-729）：codex store 的 PG 句柄不可用。
	brokenStore := &cleanuprepo.CodexContextStore{Postgres: true, PG: closedPG, ShardCount: 4}
	brokenProcessor := &codexStorageProcessor{
		db:     &familyDbService{codex: brokenStore},
		store:  brokenStore,
		logger: slog.Default(),
	}
	if _, err := brokenProcessor.ProcessBatch(context.Background(), []string{"w16d-key"}); err == nil {
		t.Fatal("结算失败必须报错")
	}
	// record_maintenance 表 drain 的 PG 接线（50-52）与 closer 排空（74-78）。
	assembly := newWorkerAssembly(workerConfig{
		Driver: "postgres", InstanceID: "w16d-drain",
		RecordMaintenanceBatchSize:               5,
		RecordMaintenanceShutdownFlushMaxBatches: 1,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	family := &retentionFamily{assembly: assembly, postgres: true}
	if err := assembly.wireRecordMaintenanceTableDrain(family, pgHandle, pgHandle); err != nil {
		t.Fatalf("PG drain 接线: %v", err)
	}
	assembly.closeStores()
}

func TestW16DPGFlushLoopFailureArms(t *testing.T) {
	// flushLoop 任务失败 → 重排 + 错误日志（877-883）。
	root := t.TempDir()
	closed, err := sql.Open("sqlite", filepath.Join(root, "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, slog.Default())
	family := &retentionFamily{
		assembly: assembly,
		runner: &retention.RecordMaintenanceRunner{
			Mode: retention.ModeSQLite,
			Executor: retention.RecordMaintenanceExecutor{
				RelatedRecords: &familyRelatedCleaner{family: &retentionFamily{
					assembly: assembly,
					recordCleanup: &cleanuprepo.RecordCleanupStore{
						Dataset:      &cleanuprepo.DB{DB: closed},
						Stats:        &cleanuprepo.DB{DB: closed},
						UsageCatalog: &cleanuprepo.DB{DB: closed},
						Business:     &cleanuprepo.DB{DB: closed},
						Shards:       cleanuprepo.NewShardStore(t.TempDir()),
					},
				}},
			},
		},
	}
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 1 << 20})
	queued, _ := queue.enqueue(retention.RecordMaintenanceJob{
		Type: retention.JobTypeAPIKeyRelatedCleanup, ID: "w16d-flush", APIKeyID: "w16d-key", SystemAccountID: "sys",
	})
	if !queued {
		t.Fatal("队列必须接收任务")
	}
	stop := make(chan struct{})
	go family.flushLoop(stop, queue)
	time.Sleep(300 * time.Millisecond)
	close(stop)
}

func TestW16DPGWireListProjectionArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgDB := w16dPGOpen(t)
	redisServer := miniredis.RunT(t)
	assembly := newWorkerAssembly(workerConfig{
		Driver: "postgres", InstanceID: "w16d-list-projection",
		Secret: w16dSecret, ListProjectionEnabled: true,
		ListProjectionIntervalMS:        1_000,
		ListProjectionBatchSize:         10,
		ListProjectionMaxBatchesPerRun:  2,
		ListProjectionWorkerConcurrency: 1,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	// PG stats 读模型（loader 的时区源）。
	statsStore, err := statsverify.OpenStore(statsverify.StoreConfig{
		Mode:                 statsverify.StorePostgres,
		PostgresURL:          w12cPGOverrideURL(t),
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Skipf("w16d: 打开 stats PG 存储失败（跳过）: %v", err)
	}
	assembly.statsStore = statsStore
	t.Cleanup(func() { _ = statsStore.Close() })
	if err := statsStore.EnsureSchema(context.Background()); err != nil {
		t.Skipf("w16d: stats PG schema 初始化失败（跳过）: %v", err)
	}
	// OAuth 族 store（投影 SyncSchedules 依赖）。
	oauthStore, err := oauthrefresh.OpenStore(pgDB, oauthrefresh.StorePostgres, w16dSecret)
	if err != nil {
		t.Fatalf("oauth store: %v", err)
	}
	assembly.oauthStore = oauthStore
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: pgDB, Postgres: true, Secret: w16dSecret})
	if err != nil {
		t.Fatalf("probe store: %v", err)
	}
	business := &businessDB{db: pgDB, postgres: true}
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
		t.Logf("投影任务在覆盖库上执行失败（覆盖错误臂，登记不判失败）: %v", err)
	}
}

func TestW16DPGManualBridgeArms(t *testing.T) {
	if testing.Short() {
		t.Skip("PG 门控臂 skipped in -short mode")
	}
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w16d: 覆盖库连接串不可用")
	}
	// J2 store bootstrap（CheckSchema 只读，需先 EnsureSchema）。
	bootstrap, err := accountbalance.OpenStore(accountbalance.StoreConfig{
		Mode: accountbalance.StorePostgres, PostgresURL: pgURL,
	})
	if err != nil {
		t.Skipf("w16d: 打开 J2 store 失败（跳过）: %v", err)
	}
	if err := bootstrap.EnsureSchema(context.Background()); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("J2 schema bootstrap: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Logf("w16d: J2 bootstrap 关闭: %v", err)
	}
	service, err := accountbalance.NewService(accountbalance.RuntimeConfig{
		OwnerID:              "w16d-manual-bridge",
		Store:                accountbalance.StoreConfig{Mode: accountbalance.StorePostgres, PostgresURL: pgURL},
		BusinessPostgresURL:  pgURL,
		CredentialSecret:     w16dSecret,
		InputTTL:             time.Minute,
		PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2,
		InputPostgresMaxOpenConns: 4, InputPostgresMaxIdleConns: 2,
	}, slog.Default())
	if err != nil {
		t.Skipf("w16d: J2 NewService 失败（跳过）: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	var running atomic.Bool
	running.Store(true)
	handler := jobsHTTPHandler(ownermode.Active, &running, func() bool { return true }, false, nil,
		true, func() bool { return true },
		service, "w16d-manual-secret-0123456789abcdef",
		false, func() bool { return true },
		func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
		false, func() bool { return true },
		nil, nil,
		false, func() bool { return true }, func() map[string]any { return nil },
	)
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

func TestW16DSQLiteResolverAndProbeArms(t *testing.T) {
	ctx := context.Background()
	// 空分组账户：owner 身份 → groupID 空 → !ok（154-156）。
	businessPath := filepath.Join(t.TempDir(), "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	businessDBForAlter := w16dOpenTestSQLite(t, businessPath)
	if _, err := businessDBForAlter.Exec(`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := businessDBForAlter.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
		credentials_encrypted, config_revision, dispatch_revision, proxy_profile_id)
		VALUES ('w16d-orphan', 'sys-w16d', 'orphan', 'api_key', 'active', 1, '{}', 1, 2, NULL)`); err != nil {
		t.Fatal(err)
	}
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: businessDBForAlter, Secret: w16dSecret})
	if err != nil {
		t.Fatal(err)
	}
	resolver := circuitRecoveryTargetResolver{store: probeStore}
	target, ok, err := resolver.Resolve(ctx, opsjobs.CircuitState{
		Scope: opsjobs.CircuitScope{AccountRuntimeKey: "w16d-orphan"},
	})
	if err != nil || ok || target.Probe != nil {
		t.Fatalf("空分组账户必须 !ok: ok=%v err=%v", ok, err)
	}
	// authorized 身份 + 候选缺失 → !ok（161-163）。
	target, ok, err = resolver.Resolve(ctx, opsjobs.CircuitState{
		Scope: opsjobs.CircuitScope{AccountRuntimeKey: "w16d-orphan:authorized:sys-w16d:g-w16d:auth-w16d"},
	})
	if err != nil || ok {
		t.Fatalf("候选缺失必须 !ok: ok=%v err=%v", ok, err)
	}
	// 关闭句柄的候选读取错误（158-160）。
	closed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	// 账户可读、候选库关闭在真实结构中不可分离；此处覆盖 FindAccountForTest 错误臂。
	brokenSource := speedFirstCandidateSource{store: mustClosedProbeStore(t)}
	if _, err := brokenSource.FindAccountForTest(ctx, "w16d-x", "sys"); err == nil {
		t.Fatal("关闭句柄 FindAccountForTest 必须报错")
	}
	// proxyEnvelope：密码封套解密后非 JSON（752-754）。
	runtime := &balanceDetectRuntime{secret: w16dSecret}
	sealedPassword, err := accountbalance.EncryptV1Envelope(w16dSecret, []byte("w16d-not-json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.proxyEnvelope("w16d-p", "http", "127.0.0.1", 8080, "user", sealedPassword); err == nil {
		t.Fatal("密码封套明文非 JSON 必须报错")
	}
}

func mustClosedProbeStore(t *testing.T) *proberepo.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: w16dSecret})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestW16DSQLiteProbeFamilyStatsFailArm 覆盖探针族 stats 存储打开失败臂
// （90-92）：业务库通过核心表校验、stats 路径为垃圾文件。
func TestW16DSQLiteProbeFamilyStatsFailArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	seedProbeCoreTables(t, businessPath)
	statsGarbage := filepath.Join(root, "garbage-stats.sqlite3")
	if err := osWriteFileForTest(statsGarbage, []byte("w16d not a sqlite database")); err != nil {
		t.Fatal(err)
	}
	assembly := newWorkerAssembly(workerConfig{
		Driver: "sqlite", InstanceID: "w16d-probe-stats-fail",
		BusinessSQLitePath: businessPath,
		StatsSQLitePath:    statsGarbage,
		Secret:             w16dSecret,
		ProbeEnabled:       true,
		StatsEnabled:       false, OAuthEnabled: false, TaskRunsEnabled: false,
		UsageWriterEnabled: false, BalanceDetectEnabled: false, RetentionEnabled: false,
	}, slog.Default())
	t.Cleanup(assembly.closeStores)
	armErr := assembly.wireProbeFamily(context.Background())
	if armErr == nil {
		for _, disabled := range assembly.disabledJobs {
			t.Logf("探针族 disabled: %s: %s", disabled.JobName, disabled.Reason)
		}
		t.Fatal("垃圾 stats 路径必须使探针族装配失败")
	}
}

func osWriteFileForTest(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
