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
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// w16kInsertProbeAccount 在探针核心表上插入一个凭据可解密的账户行，
// accountExpiresAt 由调用方给定（合法或非法时间戳）。
func w16kInsertProbeAccount(t *testing.T, db *sql.DB, expiresAt string) {
	t.Helper()
	envelope, err := accountcrypto.EncryptJSON(wgBalanceSecret, map[string]any{"api_key": "w16k-key"})
	if err != nil {
		t.Fatalf("构造 w16k 凭据封套失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, account_expires_at, credentials_encrypted)
		VALUES ('w16k-acc', 'w16k-sys', 'w16k-账户', 'api_key', 'active', ?, ?)`, expiresAt, envelope); err != nil {
		t.Fatalf("插入 w16k 账户行失败: %v", err)
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
	// PG 唯一形态：删 juhe_business.groups 驱动查询错误臂（原 sqlite
	// "no such table" 文件夹具随 sqlite 形态退役删除）。
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(probeCoreTablesPGDDL)
		return err
	})
	store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: wgBalanceSecret})
	if err != nil {
		t.Fatalf("构造 w16k 探针 store 失败: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("w16k 探针 store schema 初始化失败: %v", err)
	}
	w16kInsertProbeAccount(t, db, "")
	if _, err := db.Exec(`DROP TABLE IF EXISTS juhe_business.groups`); err != nil {
		t.Fatalf("删除 w16k groups 表失败: %v", err)
	}
	resolver := circuitRecoveryTargetResolver{store: store}
	state := opsjobs.CircuitState{Scope: opsjobs.CircuitScope{
		AccountRuntimeKey: "w16k-acc:authorized:w16k-group:w16k-sys:w16k-fp",
	}}
	target, matched, err := resolver.Resolve(context.Background(), state)
	if err == nil || matched || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("groups 表缺失必须使 Resolve 在 LoadAccountForGroup 查询失败: target=%+v matched=%v err=%v", target, matched, err)
	}
}
