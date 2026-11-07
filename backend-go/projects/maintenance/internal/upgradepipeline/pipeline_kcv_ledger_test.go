// T84-1/T84-2 集成测试（pgtestkit 隔离库）：secret KCV 三态、账本 checksum
// 读取侧 blocked、MarkRunning/MarkAdopted 写入侧一致性守卫（升级兼容与迁移
// 账本设计 §7.4/§9.4-8）。KCV 载体与 checksum 校验都触库，单测层无法覆盖。
package upgradepipeline

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const kcvTestSecret = "upgradepipeline-kcv-test-secret"

func openPipelineTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		return nil
	})
}

// insertKCVFixture 预置 KCV 密文载体父行与 accounts 行；envelope 为空串表示
// 只建父行不建载体（无载体用例）。
func insertKCVFixture(t *testing.T, db *sql.DB, envelope string) {
	t.Helper()
	for _, seed := range []string{
		`INSERT INTO "juhe_business"."system_accounts"
			(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
			VALUES ('sys-kcv', 'sys-kcv', 'sys-kcv', NULL, 'user', 'active', 'hash', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')
			ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO "juhe_business"."vendors"
			(id, code, name, default_supported_models_json, enabled, created_at, updated_at)
			VALUES ('vendor-kcv-gpt', 'gpt', 'gpt', '[]', 1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')
			ON CONFLICT (code) DO NOTHING`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("预置 KCV 父行失败: %v", err)
		}
	}
	if envelope == "" {
		return
	}
	_, err := db.Exec(`INSERT INTO "juhe_business"."accounts"
		(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
		 name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-kcv', 'sys-kcv', 'gpt', 'gpt', 'chat_completions', 'v1',
		 'kcv-fixture', 'api_key', 'active', $1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, envelope)
	if err != nil {
		t.Fatalf("预置 KCV 载体行失败: %v", err)
	}
}

func mustEncrypt(t *testing.T, secret string) string {
	t.Helper()
	sealed, err := accountcrypto.EncryptJSON(secret, map[string]string{"key": "kcv-probe"})
	if err != nil {
		t.Fatalf("构造 KCV 密文失败: %v", err)
	}
	return sealed
}

func kcvTestSteps() []Step {
	return []Step{{
		Revision: 1, ID: "kcv-pipeline-probe", Title: "KCV 全链探针", Target: TargetPrimary, ImplVersion: 1,
		Satisfied: func(ctx context.Context, env Env) (bool, error) { return false, nil },
		Apply:     func(ctx context.Context, env Env) (Detail, error) { return Detail{"probe": true}, nil },
		Verify:    func(ctx context.Context, env Env) error { return nil },
	}}
}

func kcvRunOptions(dsn string) Options {
	return Options{
		PrimaryDSN:         dsn,
		Secret:             kcvTestSecret,
		StopWriteConfirmed: true,
		BackupConfirmed:    true,
		Phase:              PhaseAll,
		StepTimeout:        30 * time.Second,
		AppVersion:         "test",
	}
}

// KCV 三态：无载体（false, nil）放行披露；正确 secret (true, nil)；
// 错误 secret 返回含「secret 与库内密文不一致」的错误。
func TestSecretKVCAgainstDatabaseTriStates(t *testing.T) {
	db := openPipelineTestDB(t)

	// 无载体：fresh 库（EnsurePostgres 无 seed）→ 跳过披露。
	verified, err := verifySecretAgainstDatabase(context.Background(), db, kcvTestSecret)
	if err != nil || verified {
		t.Fatalf("无载体: verified=%v err=%v, want false/nil", verified, err)
	}

	// 正确 secret：accounts 载体解密通过。
	insertKCVFixture(t, db, mustEncrypt(t, kcvTestSecret))
	verified, err = verifySecretAgainstDatabase(context.Background(), db, kcvTestSecret)
	if err != nil || !verified {
		t.Fatalf("正确 secret: verified=%v err=%v, want true/nil", verified, err)
	}
}

// 载体顺序：api_keys 默认 Key 优先命中；即使 accounts 载体密文对错误 secret
// 不可解，只要 api_keys 载体通过即放行（生产 fresh seed 后 api_keys 恒有行）。
func TestSecretKCVProbeOrderPrefersAPIKeys(t *testing.T) {
	db := openPipelineTestDB(t)
	insertKCVFixture(t, db, mustEncrypt(t, "wrong-secret"))
	sealed, err := accountcrypto.EncryptJSON(kcvTestSecret, map[string]string{"key": "seed-default"})
	if err != nil {
		t.Fatalf("构造 api_keys 密文失败: %v", err)
	}
	for _, seed := range []string{
		`INSERT INTO "juhe_business"."route_strategies"
			(id, system_account_id, name, description, mode, created_at, updated_at)
			VALUES ('rs-kcv', 'sys-kcv', 'kcv-route', NULL, 'normal', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')
			ON CONFLICT (id) DO NOTHING`,
	} {
		if _, err := db.Exec(seed); err != nil {
			t.Fatalf("预置 route_strategies 失败: %v（列形状以建表 DDL 为准）", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at)
		VALUES ('key-kcv', 'sys-kcv', 'rs-kcv', 'kcv-key', 'hash-kcv', 'kc', 'v', $1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, sealed); err != nil {
		t.Fatalf("预置 api_keys 载体失败: %v", err)
	}
	verified, err := verifySecretAgainstDatabase(context.Background(), db, kcvTestSecret)
	if err != nil || !verified {
		t.Fatalf("api_keys 优先: verified=%v err=%v, want true/nil", verified, err)
	}
}

// Run 级 KCV 失败：blocked（exit 3）+ 报告 secretVerified=false，且不产生任何
// 账本写入（EnsureLedger 在确认门禁与 KCV 之后）。
func TestRunSecretKCVMismatchBlocks(t *testing.T) {
	handle := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		return nil
	})
	insertKCVFixture(t, handle.DB, mustEncrypt(t, "wrong-secret"))

	out := Run(context.Background(), kcvRunOptions(handle.DSN), kcvTestSteps())
	if out.ExitCode != ExitNeedsHuman {
		t.Fatalf("exit = %d, want %d（stderr=%s）", out.ExitCode, ExitNeedsHuman, out.Stderr)
	}
	if out.Report == nil || out.Report.Result != ResultBlocked {
		t.Fatalf("result = %v, want %s", out.Report, ResultBlocked)
	}
	if out.Report == nil || out.Report.SecretVerified == nil || *out.Report.SecretVerified {
		t.Fatalf("secretVerified = %v, want false", out.Report.SecretVerified)
	}
	if !strings.Contains(out.Stderr, "secret 与库内密文不一致") {
		t.Fatalf("stderr 缺 KCV 文案: %s", out.Stderr)
	}
	var ledgerRows int
	if err := handle.DB.QueryRow(`SELECT count(*) FROM juhe_business.schema_migrations`).Scan(&ledgerRows); err == nil && ledgerRows != 0 {
		t.Fatalf("KCV 失败路径不应写账本，rows=%d", ledgerRows)
	}
}

// Run 级 KCV 通过 + 无载体披露：错误 secret 拦截之外的正反两态走完整升级链。
func TestRunSecretKCVPassAndNoCarrierDisclosed(t *testing.T) {
	t.Run("载体通过", func(t *testing.T) {
		handle := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
			if _, err := schema.EnsurePostgres(ctx, db); err != nil {
				return err
			}
			return nil
		})
		insertKCVFixture(t, handle.DB, mustEncrypt(t, kcvTestSecret))

		out := Run(context.Background(), kcvRunOptions(handle.DSN), kcvTestSteps())
		if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpgraded {
			t.Fatalf("exit=%d result=%v stderr=%s", out.ExitCode, out.Report, out.Stderr)
		}
		if out.Report.SecretVerified == nil || !*out.Report.SecretVerified {
			t.Fatalf("secretVerified = %v, want true", out.Report.SecretVerified)
		}
	})
	t.Run("无载体跳过披露", func(t *testing.T) {
		handle := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
			if _, err := schema.EnsurePostgres(ctx, db); err != nil {
				return err
			}
			return nil
		})

		out := Run(context.Background(), kcvRunOptions(handle.DSN), kcvTestSteps())
		if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpgraded {
			t.Fatalf("exit=%d result=%v stderr=%s", out.ExitCode, out.Report, out.Stderr)
		}
		if out.Report.SecretVerified == nil || *out.Report.SecretVerified {
			t.Fatalf("secretVerified = %v, want false（无载体披露）", out.Report.SecretVerified)
		}
	})
}

// 读取侧 checksum 闭环：账本行 checksum 与注册表不一致 → blocked（exit 3）。
func TestRunLedgerChecksumMismatchBlocks(t *testing.T) {
	handle := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		return EnsureLedger(ctx, db)
	})
	steps := kcvTestSteps()
	if _, err := handle.DB.Exec(`INSERT INTO juhe_business.schema_migrations
		(revision, step_id, title, target, destructive, status, checksum, impl_version, adopted, started_at, app_version)
		VALUES (1, 'tampered-step', '被篡改步骤', 'primary', false, 'applied', 'sha256:deadbeef', 1, true, '2026-11-01T00:00:00.000Z', 'test')`); err != nil {
		t.Fatalf("预置篡改账本行失败: %v", err)
	}

	out := Run(context.Background(), kcvRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitNeedsHuman {
		t.Fatalf("exit = %d, want %d（stderr=%s）", out.ExitCode, ExitNeedsHuman, out.Stderr)
	}
	if !strings.Contains(out.Stderr, "账本 checksum 不一致") {
		t.Fatalf("stderr 缺 checksum 文案: %s", out.Stderr)
	}
}

// 写入侧守卫：MarkRunning/MarkAdopted 遇 step_id 不一致的既有行拒绝覆盖。
func TestMarkWritesRejectInconsistentLedgerRow(t *testing.T) {
	db := openPipelineTestDB(t)
	ctx := context.Background()
	if err := EnsureLedger(ctx, db); err != nil {
		t.Fatalf("EnsureLedger 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.schema_migrations
		(revision, step_id, title, target, destructive, status, checksum, impl_version, adopted, started_at, app_version)
		VALUES (1, 'other-step', '别的步骤', 'primary', false, 'applied', 'sha256:whatever', 1, true, '2026-11-01T00:00:00.000Z', 'test')`); err != nil {
		t.Fatalf("预置账本行失败: %v", err)
	}
	step := Step{Revision: 1, ID: "kcv-pipeline-probe", Title: "KCV 全链探针", Target: TargetPrimary, ImplVersion: 1}
	if err := MarkRunning(ctx, db, step, PhaseAll, false, "test", time.Now()); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("MarkRunning = %v, want 拒绝覆盖", err)
	}
	if err := MarkAdopted(ctx, db, step, PhaseAll, false, "test", time.Now()); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("MarkAdopted = %v, want 拒绝覆盖", err)
	}

	// 一致行放行：step_id 等定义一致时重跑覆盖成功。
	if _, err := db.Exec(`UPDATE juhe_business.schema_migrations SET step_id = 'kcv-pipeline-probe' WHERE revision = 1`); err != nil {
		t.Fatalf("对齐 step_id 失败: %v", err)
	}
	// title/target/destructive 一致性仍拦（title 不同）。
	if err := MarkRunning(ctx, db, step, PhaseAll, false, "test", time.Now()); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("MarkRunning(title 不一致) = %v, want 拒绝覆盖", err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.schema_migrations SET title = 'KCV 全链探针' WHERE revision = 1`); err != nil {
		t.Fatalf("对齐 title 失败: %v", err)
	}
	if err := MarkRunning(ctx, db, step, PhaseAll, false, "test", time.Now()); err != nil {
		t.Fatalf("MarkRunning(一致) = %v, want nil", err)
	}
}
