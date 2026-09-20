// 波次 w16k：cmd/juhe-ai-jobs 剩余未覆盖臂的进程内补齐（目标包覆盖率
// ≥95.0%）。全部进程内直调，无 spawn、无真实外呼；数据与标识一律 w16k-
// 前缀；断言消息中文。PG 分支见 w16k_cmd_run_pg_arms_test.go。
//
// 覆盖清单（对照 w16k_state.cov 零块）：
//   - worker_assembly.go 564/195/198：oauth 家族 Secret 守卫与 wireFamilies
//     失败传播、usage-writer catalog 打开失败传播；
//   - worker_probe_jobs.go 39：探针族 Secret 守卫；
//   - worker_assembly.go 793：balance-detect stats 打开失败臂；
//   - worker_assembly.go 405：usage-stats-aggregation 的 maxBatches 设置
//     越界错误臂；
//   - worker_retention.go 511：缺 system_settings 表的时区降级告警臂；
//   - worker_circuit_jobs.go 158：恢复 resolver 的 LoadAccountForGroup
//     查询错误臂；
//   - worker_health_probe_outbox.go 263：drain env 非法的告警降级臂。
package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

// w16kOpenSQLite 打开测试用 SQLite 连接（WAL 与生产 openSQLite 形状一致），
// 测试结束时关闭。
func w16kOpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		t.Fatalf("配置 %s WAL 失败: %v", path, err)
	}
	return db
}

// w16kSeedProbeReaderTables 在探针核心表之外补齐 LoadAccountForTest 读取链
// 需要的两张辅助表（生产由迁移创建，此处空表即可满足只读查询）。
func w16kSeedProbeReaderTables(t *testing.T, path string) {
	t.Helper()
	seedProbeCoreTables(t, path)
	db := w16kOpenSQLite(t, path)
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS account_supported_models (
			account_id TEXT NOT NULL,
			model TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS account_api_key_runtime_states (
			account_id TEXT NOT NULL,
			key_fingerprint TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("预置 w16k 探针读取辅助表失败: %v", err)
		}
	}
}

// w16kInsertProbeAccount 在探针核心表上插入一个凭据可解密的账户行，
// accountExpiresAt 由调用方给定（合法或非法时间戳）。
func w16kInsertProbeAccount(t *testing.T, db *sql.DB, expiresAt string) {
	t.Helper()
	envelope, err := accountcrypto.EncryptJSON("w16k-secret", map[string]any{"api_key": "w16k-key"})
	if err != nil {
		t.Fatalf("构造 w16k 凭据封套失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts
		(id, system_account_id, name, type, status, account_expires_at, credentials_encrypted)
		VALUES ('w16k-acc', 'w16k-sys', 'w16k-账户', 'api_key', 'active', ?, ?)`, expiresAt, envelope); err != nil {
		t.Fatalf("插入 w16k 账户行失败: %v", err)
	}
}

// TestW16KWireOAuthAndUsageWriterFailArms 锁定三段失败传播链：
//  1. 空 Secret → wireOAuthFamily 在 oauthrefresh.OpenStore 拒绝（564-566）；
//  2. 同样配置走 wireFamilies：settings/taskruns/stats 成功后失败点收敛在
//     oauth 家族（195-197）；
//  3. Secret 合法但 usage catalog 是目录：失败点推进到 usage-writer
//     （198-200）。
func TestW16KWireOAuthAndUsageWriterFailArms(t *testing.T) {
	root := t.TempDir()
	// 1. 空 Secret 直调 wireOAuthFamily。
	empty := w16jNewSilentAssembly(t, workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(root, "w16k-oauth-business.sqlite3"),
	})
	if err := empty.wireOAuthFamily(context.Background()); err == nil || !strings.Contains(err.Error(), "open oauth-refresh store") {
		t.Fatalf("空 Secret 必须使 oauth 家族打开失败: %v", err)
	}
	empty.closeStores()

	familiesConfig := func(catalogPath string, secret string) workerConfig {
		return workerConfig{
			Driver:                 "sqlite",
			BusinessSQLitePath:     filepath.Join(root, "w16k-families-business.sqlite3"),
			StatsSQLitePath:        filepath.Join(root, "w16k-families-stats.sqlite3"),
			TaskRunsSQLitePath:     filepath.Join(root, "w16k-families-taskruns.sqlite3"),
			UsageCatalogSQLitePath: catalogPath,
			UsageShardRoot:         filepath.Join(root, "w16k-families-shards"),
			UsageShardCount:        4,
			Secret:                 secret,
		}
	}
	// 2. 空 Secret 走 wireFamilies，失败传播到 oauth 家族调用点。
	emptyFamilies := w16jNewSilentAssembly(t, familiesConfig(
		filepath.Join(root, "w16k-families-catalog.sqlite3"), ""))
	if err := emptyFamilies.wireFamilies(context.Background()); err == nil || !strings.Contains(err.Error(), "open oauth-refresh store") {
		t.Fatalf("空 Secret 下 wireFamilies 必须在 oauth 家族失败: %v", err)
	}
	emptyFamilies.closeStores()

	// 3. Secret 合法但 catalog 路径是目录：失败推进到 usage-writer。
	catalogDir := filepath.Join(root, "w16k-catalog-dir")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("创建 w16k catalog 目录失败: %v", err)
	}
	catalogBroken := w16jNewSilentAssembly(t, familiesConfig(catalogDir, "w16k-secret"))
	if err := catalogBroken.wireFamilies(context.Background()); err == nil || !strings.Contains(err.Error(), "usage-writer-catalog") {
		t.Fatalf("catalog 目录必须使 wireFamilies 在 usage-writer 失败: %v", err)
	}
	catalogBroken.closeStores()
}

// TestW16KWireProbeFamilyArms 覆盖 wireProbeFamily 的 Secret 守卫臂
// （39-42）。
//
// 行为存疑（按现状断言，登记不修复）：
//  1. worker_probe_jobs.go 的 OpenSpeedFirstStore URL 解析失败臂（167-169）
//     不可达——同一 Redis URL 会先在 wireCircuitFamily 的电路 Redis 解析处
//     失败（错误臂顺序随电路族接线漂移），速度优先 store 的解析错误臂成为
//     死臂；
//  2. worker_probe_jobs.go 80-82（overlay 缺 Redis）与 146-148（传播）不可
//     达——投影族在 SQLite 驱动下被 PG-only 门禁提前登记 disabled，PG 驱动
//     下非空但非法的 Redis URL 又先被电路族解析拒绝。
func TestW16KWireProbeFamilyArms(t *testing.T) {
	root := t.TempDir()
	empty := w16jNewSilentAssembly(t, workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(root, "w16k-probe-empty.sqlite3"),
	})
	if err := empty.wireProbeFamily(context.Background()); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("空 Secret 必须使探针族 NewStore 失败: %v", err)
	}
	empty.closeStores()
}

// TestW16KWireBalanceDetectStatsOpenFailArm 锁定余额探测族 stats 打开失败臂
// （793-796）：task-runs/Secret/business 全部就绪，仅 StatsSQLitePath 指向
// 目录，失败必须收敛在 balance-detect-stats 的 openSQLite。
func TestW16KWireBalanceDetectStatsOpenFailArm(t *testing.T) {
	root := t.TempDir()
	statsDir := filepath.Join(root, "w16k-balance-stats-dir")
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("创建 w16k stats 目录失败: %v", err)
	}
	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(root, "w16k-balance-business.sqlite3"),
		TaskRunsSQLitePath: filepath.Join(root, "w16k-balance-taskruns.sqlite3"),
		StatsSQLitePath:    statsDir,
		Secret:             "w16k-secret",
	})
	if err := assembly.wireTaskRunsFamily(context.Background()); err != nil {
		t.Fatalf("装配 task-runs 家族失败: %v", err)
	}
	if err := assembly.wireBalanceDetectFamily(context.Background()); err == nil || !strings.Contains(err.Error(), "balance-detect-stats") {
		t.Fatalf("stats 路径为目录必须使余额探测族失败: %v", err)
	}
}

// TestW16KStatsAggregationMaxBatchesInvalidArm 锁定 usage-stats-aggregation
// 的 maxBatches 越界错误臂（405-407）：batchSize 合法、
// statsAggregationMaxBatchesPerRun 越上界，任务必须以设置校验错误失败。
func TestW16KStatsAggregationMaxBatchesInvalidArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "w16k-agg-business.sqlite3")
	business := w16kOpenSQLite(t, businessPath)
	if _, err := business.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("建 w16k system_settings 表失败: %v", err)
	}
	if _, err := business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
		VALUES (?, 'statsAggregationBatchSize', '256', '2026-09-20T00:00:00Z'),
		       (?, 'statsAggregationMaxBatchesPerRun', '99999', '2026-09-20T00:00:00Z')`,
		jobssettings.SystemSettingsAccountID, jobssettings.SystemSettingsAccountID); err != nil {
		t.Fatalf("播种 w16k 聚合设置失败: %v", err)
	}
	if err := business.Close(); err != nil {
		t.Fatalf("关闭 w16k 业务库失败: %v", err)
	}

	assembly := w16jNewSilentAssembly(t, workerConfig{
		Driver:                 "sqlite",
		BusinessSQLitePath:     businessPath,
		StatsSQLitePath:        filepath.Join(root, "w16k-agg-stats.sqlite3"),
		UsageCatalogSQLitePath: filepath.Join(root, "w16k-agg-catalog.sqlite3"),
		UsageShardRoot:         filepath.Join(root, "w16k-agg-shards"),
		UsageShardCount:        4,
		Secret:                 "w16k-secret",
	})
	if err := assembly.wireStatsFamily(context.Background()); err != nil {
		t.Fatalf("装配 stats 家族失败: %v", err)
	}
	if err := assembly.wireUsageWriterFamily(context.Background()); err != nil {
		t.Fatalf("装配 usage-writer 家族失败: %v", err)
	}
	err := w16jRunTask(t, assembly, "usage-stats-aggregation", context.Background())
	if err == nil || !strings.Contains(err.Error(), "statsAggregationMaxBatchesPerRun") {
		t.Fatalf("maxBatches 越界必须使聚合任务失败: %v", err)
	}
}

// TestW16KRetentionMissingSettingsTableArm 锁定时区读模型在 system_settings
// 表缺失时的降级告警臂（511-517）：返回默认时区并触发一次 warn。
func TestW16KRetentionMissingSettingsTableArm(t *testing.T) {
	root := t.TempDir()
	db := w16kOpenSQLite(t, filepath.Join(root, "w16k-retention.sqlite3"))
	warned := false
	runtime := &retentionSettingsRuntime{
		db:     db,
		dbMode: jobssettings.SQLite,
		warn: func(event string, fields map[string]any, message string) {
			warned = true
			if event != "background_job_settings_table_missing_default" {
				t.Fatalf("降级告警 event 必须是 background_job_settings_table_missing_default，得到 %s", event)
			}
		},
	}
	timezone, err := runtime.readTimezoneSetting(context.Background())
	if err != nil {
		t.Fatalf("缺表必须降级默认而不是失败: %v", err)
	}
	if timezone != defaultRetentionTimezone() {
		t.Fatalf("缺表必须回落默认时区 %q，得到 %q", defaultRetentionTimezone(), timezone)
	}
	if !warned {
		t.Fatal("缺表降级必须触发一次 warn")
	}
}

// TestW16KRecoveryResolverArms 覆盖账户电路恢复 resolver 的查询错误透传臂
// （worker_circuit_jobs.go 158-160）：groups 表缺失时 LoadAccountForTest
// 成功、LoadAccountForGroup 查询失败。
//
// 行为存疑（按现状断言，登记不修复）：worker_probe_jobs.go 288-290
// （FindAccountForTest 的非法 accountExpiresAt 解析错误臂）不可达——
// proberepo.LoadAccountForTest → deriveEffectiveAvailability 对库中非法
// 时间戳直接 panic（reader.go 的 invariant panic(err)），错误臂上游已不可
// 传导；该 panic 行为本身是共享平台侧的健壮性隐患。
func TestW16KRecoveryResolverArms(t *testing.T) {
	root := t.TempDir()

	// groups 表缺失：LoadAccountForTest 成功后 LoadAccountForGroup 失败。
	resolverPath := filepath.Join(root, "w16k-resolver.sqlite3")
	w16kSeedProbeReaderTables(t, resolverPath)
	resolverDB := w16kOpenSQLite(t, resolverPath)
	w16kInsertProbeAccount(t, resolverDB, "")
	if _, err := resolverDB.Exec(`DROP TABLE groups`); err != nil {
		t.Fatalf("删除 w16k groups 表失败: %v", err)
	}
	store, err := proberepo.NewStore(proberepo.Config{DB: resolverDB, Secret: "w16k-secret"})
	if err != nil {
		t.Fatalf("构造 w16k 探针 store 失败: %v", err)
	}
	resolver := circuitRecoveryTargetResolver{store: store}
	state := opsjobs.CircuitState{Scope: opsjobs.CircuitScope{
		AccountRuntimeKey: "w16k-acc:authorized:w16k-group:w16k-sys:w16k-fp",
	}}
	target, matched, err := resolver.Resolve(context.Background(), state)
	if err == nil || matched || !strings.Contains(err.Error(), "no such table: groups") {
		t.Fatalf("groups 表缺失必须使 Resolve 在 LoadAccountForGroup 查询失败: target=%+v matched=%v err=%v", target, matched, err)
	}
}

// TestW16KOutboxFaceInvalidDrainEnvArm 锁定 outbox 消费面 env 非法降级臂
// （263-265）：JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_DRAIN_LIMIT 非法时
// face 仍装配成功，drain 上限回落默认并触发 warnInvalidEnv。
func TestW16KOutboxFaceInvalidDrainEnvArm(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "w16k-outbox-business.sqlite3")
	fixture := w16dOpenTestSQLite(t, businessPath)
	seedProbeCoreTables(t, businessPath)
	w16dCreateJ1FixtureTables(t, fixture)
	defer fixture.Close()

	redisServer := miniredis.RunT(t)
	env := w16dJ1Env(t, root)
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://" + redisServer.Addr()
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:w16k"
	env[probeOutboxDrainLimitEnvVar] = "w16k-not-a-number"

	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		InstanceID:         "w16k-face",
		BusinessSQLitePath: businessPath,
		RedisStateURL:      env["JUHE_AI_REDIS_STATE_URL"],
		RedisNamespace:     env["JUHE_AI_REDIS_NAMESPACE"],
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)

	face, err := assembly.wireHealthProbeOutboxFace(func(name string) string { return env[name] })
	if err != nil {
		t.Fatalf("非法 drain env 不得使 face 装配失败: %v", err)
	}
	if face.drain == nil {
		t.Fatal("face.drain 必须就绪")
	}
	if face.drain.Limit != defaultProbeOutboxDrainLimit {
		t.Fatalf("非法 drain limit 必须回落默认 %d，得到 %d", defaultProbeOutboxDrainLimit, face.drain.Limit)
	}
}
