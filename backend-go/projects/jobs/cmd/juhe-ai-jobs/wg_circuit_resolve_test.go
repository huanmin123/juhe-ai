package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

// wgSeedRecoveryAccount 为电路恢复解析器写入可命中的账户/分组/绑定行
// （凭据必须是 V1 封套：proberepo 解密失败按候选缺失处理）。
func wgSeedRecoveryAccount(t *testing.T, store *proberepoStoreHandle) string {
	t.Helper()
	// seedProbeCoreTables 的账户形状缺 LoadAccountForGroup 读取的代理列。
	if _, err := store.db.Exec(`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`); err != nil {
		t.Fatal(err)
	}
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-rt", "base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO groups (id, system_account_id, provider_code) VALUES ('g-rt', 'sys-rt', 'openai')`,
		`INSERT INTO group_accounts (group_id, system_account_id, account_id) VALUES ('g-rt', 'sys-rt', 'acc-rt')`,
		`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code, credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('acc-rt', 'sys-rt', 'rt', 'api_key', 'active', 1, 'openai', ?, 1, 2)`,
	}
	for index, statement := range statements {
		var execErr error
		if index == len(statements)-1 {
			_, execErr = store.db.Exec(statement, envelope)
		} else {
			_, execErr = store.db.Exec(statement)
		}
		if execErr != nil {
			t.Fatal(execErr)
		}
	}
	return "acc-rt"
}

// proberepoStoreHandle 持有 fixture 的底层句柄（proberepo.Store 不导出 db）。
type proberepoStoreHandle struct {
	db *sql.DB
}

// TestCircuitRecoveryResolverPositivePath 覆盖恢复目标解析的正路径：
// owner 身份 → 账户读取 → 分组候选 → dispatch revision 围栏 → 探针闭包。
func TestCircuitRecoveryResolverPositivePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	seedProbeCoreTables(t, path)
	db, err := sqlOpenSQLiteFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	probeStore, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatal(err)
	}
	if err := probeStore.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	handle := &proberepoStoreHandle{db: db}
	runtimeKey := wgSeedRecoveryAccount(t, handle)

	resolver := circuitRecoveryTargetResolver{store: probeStore}
	target, ok, err := resolver.Resolve(context.Background(), opsjobs.CircuitState{
		Scope: opsjobs.CircuitScope{AccountRuntimeKey: runtimeKey},
	})
	if err != nil || !ok {
		t.Fatalf("种子账户必须解析出恢复目标: %v %v", ok, err)
	}
	if target.DispatchRevision != "2" {
		t.Fatalf("dispatch revision 围栏必须来自账户行: %q", target.DispatchRevision)
	}
	if target.Probe == nil {
		t.Fatal("恢复目标必须携带探针闭包")
	}
	// ctx 已取消 → 闭包返回 unknown/canceled（不发起上游诊断）。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, probeErr := target.Probe(canceled)
	if probeErr != nil {
		t.Fatalf("取消的探针不得报错: %v", probeErr)
	}
	if outcome.Kind != opsjobs.ProbeOutcomeUnknown || outcome.FailureKind != opsjobs.ProbeFailureCanceled {
		t.Fatalf("取消的探针必须分类 unknown/canceled: %+v", outcome)
	}
}

// TestWireListProjectionFamilyDisabledChain 覆盖列表投影族在 PG 语义下的
// 逐项 fail closed 登记（登记分支不需要真实 PG 连接）。
func TestWireListProjectionFamilyDisabledChain(t *testing.T) {
	base := workerConfig{
		Driver:                "postgres",
		InstanceID:            "wg-lp",
		WorkerRole:            "worker",
		ListProjectionEnabled: true,
		Secret:                wgBalanceSecret,
	}
	// 非法 namespace → disabled。
	assembly := newWorkerAssembly(func() workerConfig {
		config := base
		config.RedisStateURL = "redis://127.0.0.1:1"
		config.RedisNamespace = "bad namespace!!"
		return config
	}(), nil)
	if err := assembly.wireListProjectionFamily(context.Background(), &businessDB{db: nil}, nil); err != nil {
		t.Fatalf("登记分支不得报错: %v", err)
	}
	// 合法 namespace 但缺探针族 → disabled。
	assembly2 := newWorkerAssembly(func() workerConfig {
		config := base
		config.RedisStateURL = "redis://127.0.0.1:1"
		config.RedisNamespace = "juhe-ai:wg-test"
		return config
	}(), nil)
	if err := assembly2.wireListProjectionFamily(context.Background(), &businessDB{db: nil}, nil); err != nil {
		t.Fatalf("登记分支不得报错: %v", err)
	}
	// 探针族就位但 OAuth 族缺席 → disabled。
	assembly3 := newWorkerAssembly(func() workerConfig {
		config := base
		config.RedisStateURL = "redis://127.0.0.1:1"
		config.RedisNamespace = "juhe-ai:wg-test"
		return config
	}(), nil)
	if err := assembly3.wireListProjectionFamily(context.Background(), &businessDB{db: nil}, (*proberepo.Store)(nil)); err != nil {
		t.Fatalf("登记分支不得报错: %v", err)
	}
	for _, assembly := range []*workerAssembly{assembly, assembly2, assembly3} {
		found := false
		for _, disabled := range assembly.disabledJobs {
			if disabled.JobName == "account-list-availability-projection-maintenance" {
				found = true
			}
		}
		if !found {
			t.Fatalf("依赖缺失时必须登记 disabled: %+v", assembly.disabledJobs)
		}
		if len(assembly.wiredJobs) != 0 {
			t.Fatalf("依赖缺失时不得接线: %v", assembly.wiredJobs)
		}
	}
}

// TestWireBalanceDetectFamilyDisabledBranches 覆盖余额族装配的 fail closed
// 登记（缺 stats 路径 / 缺密钥 / 缺租约存储）。
func TestWireBalanceDetectFamilyDisabledBranches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(config *workerConfig)
	}{
		{"缺 stats 路径", func(config *workerConfig) { config.StatsSQLitePath = "" }},
		{"缺密钥", func(config *workerConfig) { config.Secret = "" }},
		{"缺租约存储", func(config *workerConfig) {}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			config := workerConfig{
				Driver:               "sqlite",
				StatsSQLitePath:      filepath.Join(t.TempDir(), "stats.sqlite3"),
				BusinessSQLitePath:   filepath.Join(t.TempDir(), "business.sqlite3"),
				Secret:               wgBalanceSecret,
				BalanceDetectEnabled: true,
			}
			item.mutate(&config)
			assembly := newWorkerAssembly(config, nil)
			if err := assembly.wireBalanceDetectFamily(context.Background()); err != nil {
				t.Fatalf("登记分支不得报错: %v", err)
			}
			found := false
			for _, disabled := range assembly.disabledJobs {
				if disabled.JobName == "account-balance-auto-detect-recovery" {
					found = true
				}
			}
			if !found {
				t.Fatalf("必须登记 disabled: %+v", assembly.disabledJobs)
			}
		})
	}
}

// TestWireFamiliesFailsClosedOnBrokenSQLite 覆盖 wireFamilies 的错误传播
// （业务库路径是目录 → openSQLite 失败 → 装配失败）。
func TestWireFamiliesFailsClosedOnBrokenSQLite(t *testing.T) {
	root := t.TempDir()
	config := workerConfig{
		Driver:                      "sqlite",
		BusinessSQLitePath:          root, // 目录不是合法 SQLite 文件
		TaskRunsEnabled:             false,
		StatsEnabled:                false,
		OAuthEnabled:                false,
		UsageWriterEnabled:          false,
		BalanceDetectEnabled:        false,
		RetentionEnabled:            false,
		ProbeEnabled:                false,
		CodexContextStateShardCount: 1,
	}
	if _, err := buildWorkerAssembly(config, nil); err == nil {
		t.Fatal("业务库非法时装配必须失败")
	}
}

// TestAcquirePoolWithInvalidURL 覆盖 acquirePool 的登记路径（pgx 惰性连接，
// 句柄登记后由 closeStores 释放）。
func TestAcquirePoolWithInvalidURL(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{Driver: "postgres", PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 1}, nil)
	defer assembly.closeStores()
	handle, err := assembly.acquirePool("postgres://wg:secret@127.0.0.1:5432/db?sslmode=disable", "wg-label")
	if err != nil {
		// sql.Open 对非法 DSN 可能直接报错：错误路径同样覆盖。
		if len(assembly.pools) != 0 {
			t.Fatal("失败的池不得登记")
		}
		return
	}
	if handle == nil || len(assembly.pools) != 1 {
		t.Fatal("成功的池必须登记到 pools")
	}
}

// TestOpenBusinessDBPostgresPath 覆盖 openBusinessDB 的 PG 分支。
func TestOpenBusinessDBPostgresPath(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{
		Driver:               "postgres",
		PostgresURL:          "postgres://wg:secret@127.0.0.1:5432/db?sslmode=disable",
		PostgresMaxOpenConns: 2,
		PostgresMaxIdleConns: 1,
	}, nil)
	defer assembly.closeStores()
	business, err := openBusinessDB(assembly, "wg-pg-business")
	if err != nil {
		t.Fatalf("pgx 惰性打开必须成功: %v", err)
	}
	if !business.postgres {
		t.Fatal("PG 分支必须标记 postgres")
	}
	if err := business.close(); err != nil {
		t.Fatalf("PG 句柄关闭: %v", err)
	}
}

// TestWireUsageSpoolDrainBranches 覆盖 spool drain 的未接线与缺目录分支。
func TestWireUsageSpoolDrainBranches(t *testing.T) {
	// writer 未装配 → 静默返回。
	assembly := newWorkerAssembly(workerConfig{UsageWriterEnabled: false}, nil)
	if err := assembly.wireUsageSpoolDrain(); err != nil {
		t.Fatalf("未装配必须静默返回: %v", err)
	}
	// 目录缺失（PG 模式未配置 env）→ 显式 warn 登记不报错。
	assembly2 := newWorkerAssembly(workerConfig{
		UsageWriterEnabled:  true,
		UsageSpoolDirectory: "",
	}, assemblyTestLogger(t))
	assembly2.writer = usagewriter.NewWriter(usagewriter.Config{}, nil, nil)
	if err := assembly2.wireUsageSpoolDrain(); err != nil {
		t.Fatalf("缺目录必须 warn 而非报错: %v", err)
	}
	if assembly2.usageSpoolDrain != nil {
		t.Fatal("缺目录时 drain 不得接线")
	}
}

// TestRetentionTimezonePGFailureFallsBack 覆盖 PG 模式下时区读取失败的
// 回落分支（SQLite 连接执行 PG 限定查询必然失败，恰好驱动该分支）。
func TestRetentionTimezonePGFailureFallsBack(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "biz.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := newRetentionSettingsRuntime(db, true, assemblyTestLogger(t))
	name, err := runtime.timezoneName(context.Background())
	if err != nil || name == "" {
		t.Fatalf("PG 读失败必须回落默认时区: %q %v", name, err)
	}
	// warnOnce 只告警一次：再次触发不再进入回调。
	runtime.warnOnce("wg_event", "消息", errors.New("x"))
}

// TestFlushLoopProcessesSuccessfulJobs 覆盖 flush 循环的成功批次处理路径。
func TestFlushLoopProcessesSuccessfulJobs(t *testing.T) {
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 64 * 1024})
	// api_key_related_cleanup 对不存在的目标执行成功（零删除）。
	queue.enqueue(wgMaintenanceJob("api_key_related_cleanup", "wg-none"))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		assembly.retention.flushLoop(stop, queue)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && queue.size() != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flush 循环未按停机信号退出")
	}
	if queue.size() != 0 {
		t.Fatalf("成功任务必须出队: %d", queue.size())
	}
}

// TestWireScheduleSettingsAndFaceBranches 覆盖调度设置源与 outbox face 的
// 剩余分支。
func TestWireScheduleSettingsAndFaceBranches(t *testing.T) {
	// 缺业务库路径 → 显式 warn 登记不报错。
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, assemblyTestLogger(t))
	if err := assembly.wireScheduleSettings(); err != nil {
		t.Fatalf("缺路径必须 warn 而非报错: %v", err)
	}
	if assembly.scheduleIntervals != nil {
		t.Fatal("缺路径时设置源不得装配")
	}
	// getenv 为 nil 时 wireHealthProbeOutboxFace 回落 os.Getenv；恒开终态下
	// drain 与 pruner 恒装配（无 J1 门控缺席路径）。
	workspace := t.TempDir()
	faceAssembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(workspace, "business.sqlite3"),
	}, assemblyTestLogger(t))
	face, err := faceAssembly.wireHealthProbeOutboxFace(nil)
	if err != nil {
		t.Fatalf("恒开 outbox face 必须可装配: %v", err)
	}
	defer faceAssembly.closeStores()
	if face == nil || face.pruner == nil || face.drain == nil {
		t.Fatalf("恒开 face 形态错误: %+v", face)
	}
}

// TestSettingsModeAndProbeSettingsSuccess 覆盖设置模式映射与探针设置源
// 的成功读取路径。
func TestSettingsModeAndProbeSettingsSuccess(t *testing.T) {
	if settingsMode(true) != jobssettings.Postgres || settingsMode(false) != jobssettings.SQLite {
		t.Fatal("settingsMode 映射错误")
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "biz.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'accountQualityWindowMinutes', '15', '2026-09-10T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	assembly := newWorkerAssembly(workerConfig{Driver: "sqlite"}, nil)
	settings := assembly.probeSettingsSource(&businessDB{db: db})
	if value := settings("accountQualityWindowMinutes", 5, 120); value != 15 {
		t.Fatalf("显式设置必须生效: %d", value)
	}
}

// TestLoadWorkerConfigShutdownFlushBadInt 覆盖停机冲刷批次的非整数分支。
func TestLoadWorkerConfigShutdownFlushBadInt(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	env["JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES"] = "abc"
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil {
		t.Fatal("非整数必须报错")
	}
}

// TestProbeFamilyInvalidNamespaceDisabled 覆盖探针/速度优先族在 Redis
// namespace 非法时的登记分支。
func TestProbeFamilyInvalidNamespaceDisabled(t *testing.T) {
	redisServer := miniredis.RunT(t)
	dir := t.TempDir()
	wgSeedAllJobsFixture(t, dir)
	env := wgAllJobsAssemblyEnv(t, dir, "redis://"+redisServer.Addr())
	env["JUHE_AI_REDIS_NAMESPACE"] = "bad namespace!!"
	assembly, err := buildWorkerAssembly(loadWorkerConfigOrFatal(t, env), nil)
	if err != nil {
		t.Fatalf("buildWorkerAssembly: %v", err)
	}
	defer assembly.closeStores()
	for _, job := range []string{"normal-route-speed-first-recovery-probe", "account-circuit-control-plane-maintenance"} {
		wired := false
		for _, name := range assembly.wiredJobs {
			if name == job {
				wired = true
			}
		}
		if wired {
			t.Fatalf("%s 在非法 namespace 下不得接线", job)
		}
	}
	found := false
	for _, disabled := range assembly.disabledJobs {
		if disabled.JobName == "normal-route-speed-first-recovery-probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("速度优先恢复探针必须登记 disabled: %+v", assembly.disabledJobs)
	}
}
