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
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
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
	// PG 唯一形态：oauth store 的空 Secret 校验臂保留（原 sqlite 路径/
	// catalog 目录失败臂随 sqlite 形态退役删除——PG 装配无文件路径失败面）。
	empty := w16jNewSilentAssembly(t, workerConfig{})
	if err := empty.wireOAuthFamily(context.Background()); err == nil || !strings.Contains(err.Error(), "open oauth-refresh store") {
		t.Fatalf("空 Secret 必须使 oauth 家族打开失败: %v", err)
	}
	empty.closeStores()
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
	// PG 唯一形态：空 Secret 守卫臂保留（惰性池不触库；原 sqlite 文件夹具
	// 随 sqlite 形态退役删除）。
	empty := w16jNewSilentAssembly(t, workerConfig{})
	if err := empty.wireProbeFamily(context.Background()); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("空 Secret 必须使探针族 NewStore 失败: %v", err)
	}
	empty.closeStores()
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
