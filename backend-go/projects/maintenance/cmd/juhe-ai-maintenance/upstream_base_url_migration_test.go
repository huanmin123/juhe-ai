package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upstreambaseurlmigration"
)

// 上游 Base URL 版本根迁移命令的 CLI 分支测试（进程内直调 runMaintenance，
// 输出捕获复用 wmRunMaintenanceCapture；测试单进程纪律，不孵化子进程）。
// 用法门禁与互斥矩阵在本文件直测；分类/核验与 dry-run 的一致性走真实 PG
// 门控夹具（pgtestkit 独立库，见 w14k_pg_gate_test.go 同款基建）。

func TestUpstreamBaseURLMigrationFormalExecutionRequiresGates(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
	)
	if code != 2 {
		t.Fatalf("缺停写/备份门禁应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--node-stopped --go-stopped --backup-confirmed") {
		t.Fatalf("stderr 应提示三确认门禁: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationDryRunRequiresManifestOut(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("缺 --manifest-out 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--manifest-out") {
		t.Fatalf("stderr 应提示 --manifest-out 必填: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationRequiresDSN(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
	)
	if code != 2 {
		t.Fatalf("缺 --dsn 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--dsn") {
		t.Fatalf("stderr 应提示 --dsn 必填: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationMutuallyExclusive(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root", "-dry-run",
		"-manifest-out", filepath.Join(t.TempDir(), "manifest.json"),
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		"-ensure-schema",
	)
	if code != 2 {
		t.Fatalf("与其他命令组合应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("stderr 应提示互斥: %q", stderr)
	}
}

func TestUpstreamBaseURLMigrationAndVerifyMutuallyExclusive(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root",
		"-verify-upstream-base-url-version-root", "-upstream-base-url-check", "preflight",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("migrate 与 verify 组合应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("stderr 应提示互斥: %q", stderr)
	}
}

func TestUpstreamBaseURLVerifyRequiresCheck(t *testing.T) {
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-verify-upstream-base-url-version-root",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
	)
	if code != 2 {
		t.Fatalf("缺 --upstream-base-url-check 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--upstream-base-url-check=preflight|postflight") {
		t.Fatalf("stderr 应提示 check 参数取值: %q", stderr)
	}
}

// TestUpstreamBaseURLVerifyRequiresSecret 钉住 secret fail-fast 门禁：
// preflight 与 postflight 都解封凭据分类，--secret（或 JUHE_AI_SECRET）缺失
// 或空白必须在开库扫描前以用法错误（退出码 2）拦截，不得静默退化成全量
// credential_decrypt_failed 误报输出。
func TestUpstreamBaseURLVerifyRequiresSecret(t *testing.T) {
	for _, check := range []string{"preflight", "postflight"} {
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-verify-upstream-base-url-version-root",
			"-upstream-base-url-check", check,
			"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		)
		if code != 2 {
			t.Fatalf("%s 缺 --secret 应返回退出码 2: %d", check, code)
		}
		if !strings.Contains(stderr, "--secret") || !strings.Contains(stderr, "JUHE_AI_SECRET") {
			t.Fatalf("%s stderr 应提示 --secret / JUHE_AI_SECRET 必填: %q", check, stderr)
		}
		// 门禁在开库扫描之前：除门禁错误文案外不得有任何 stdout 分类报告
		// （manifest/report JSON），stderr 不得出现 review/failure 行级误报。
		if stdout != "" {
			t.Fatalf("%s 门禁前不得产生扫描报告输出: stdout=%q", check, stdout)
		}
		if strings.Contains(stderr, "review: kind=") || strings.Contains(stderr, "failure: kind=") {
			t.Fatalf("%s 门禁前不得产生凭据行级误报: stderr=%q", check, stderr)
		}
	}
	// 空白 secret 同样拦截（TrimSpace 门禁）。
	code, _, stderr := wmRunMaintenanceCapture(t,
		"-verify-upstream-base-url-version-root",
		"-upstream-base-url-check", "preflight",
		"-dsn", "postgres://user:pass@127.0.0.1:5432/business",
		"-secret", "   ",
	)
	if code != 2 {
		t.Fatalf("空白 --secret 应返回退出码 2: %d", code)
	}
	if !strings.Contains(stderr, "--secret") {
		t.Fatalf("空白 --secret stderr 应提示 --secret 必填: %q", stderr)
	}
}

// upstreamBaseURLVerifyTestSecret 是 PG 夹具凭据信封的封箱密钥（仅测试进程
// 内存使用，不落任何文件）。
const upstreamBaseURLVerifyTestSecret = "upstream-base-url-verify-test-secret"

// countUpstreamBaseURLDecryptFailed 统计凭据复核桶里 credential_decrypt_failed
// 条目数（正确 secret 下必须为 0）。
func countUpstreamBaseURLDecryptFailed(entries []upstreambaseurlmigration.ReviewEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == upstreambaseurlmigration.KindCredential && entry.Reason == string(upstreambaseurlmigration.ReviewDecryptFailed) {
			count++
		}
	}
	return count
}

// TestUpstreamBaseURLVerifyMatchesDryRunClassification 在 pgtestkit 独立库上
// 回归修复：带正确 secret 的夹具（已封凭据信封 + 遗留 base_url 连接）跑
// dry-run / preflight / apply / postflight，凭据分类三处一致且
// credential_decrypt_failed 恒为 0（历史缺陷：verify 传空 Options 使凭据
// 全量误报解密失败）。未授权真实 PG 时按 pgtestkit 授权门 skip。
func TestUpstreamBaseURLVerifyMatchesDryRunClassification(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := bootstrap.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		if _, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: time.Now}); err != nil {
			return err
		}
		return nil
	})
	envelope, err := accountcrypto.EncryptJSON(upstreamBaseURLVerifyTestSecret, map[string]any{
		"base_url": "https://api.deepseek.com",
		"api_key":  "sk-fixture",
	})
	if err != nil {
		t.Fatalf("封夹具凭据失败: %v", err)
	}
	fixtures := []string{
		`INSERT INTO "juhe_business"."system_accounts" (id, username, display_name, password_hash, created_at, updated_at)
		 VALUES ('sys-fixture', 'fixture-upstream-base-url', '上游 Base URL 迁移夹具', 'fixture-not-a-real-hash',
		  '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO "juhe_business"."vendors" (id, code, name, created_at, updated_at)
		 VALUES ('ven-fixture-deepseek', 'deepseek', 'DeepSeek', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		 ON CONFLICT (code) DO NOTHING`,
		`INSERT INTO "juhe_business"."accounts"
		 (id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
		  name, type, credentials_encrypted, created_at, updated_at)
		 VALUES ('acc-fixture', 'sys-fixture', 'deepseek', 'api_key', 'openai', 'v1',
		  'fixture', 'api_key', $1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO "juhe_business"."account_connections"
		 (id, account_id, connection_preset_code, base_url, created_at, updated_at)
		 VALUES ('conn-fixture', 'acc-fixture', 'deepseek_openai_v1', 'https://api.deepseek.com',
		  '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	}
	for i, statement := range fixtures {
		args := []any{}
		if strings.Contains(statement, `"juhe_business"."accounts"`) {
			args = append(args, envelope)
		}
		if _, err := testDB.DB.Exec(statement, args...); err != nil {
			t.Fatalf("夹具语句 %d 执行失败: %v", i, err)
		}
	}

	runVerify := func(check string) (int, string, string) {
		return wmRunMaintenanceCapture(t,
			"-verify-upstream-base-url-version-root",
			"-upstream-base-url-check", check,
			"-dsn", testDB.DSN,
			"-secret", upstreamBaseURLVerifyTestSecret,
		)
	}
	runDryRun := func() upstreambaseurlmigration.Manifest {
		manifestPath := filepath.Join(t.TempDir(), "manifest.json")
		code, _, stderr := wmRunMaintenanceCapture(t,
			"-migrate-upstream-base-url-version-root", "-dry-run",
			"-dsn", testDB.DSN,
			"-manifest-out", manifestPath,
			"-secret", upstreamBaseURLVerifyTestSecret,
		)
		if code != 0 {
			t.Fatalf("dry-run 应 exit 0: exit %d, stderr %.200s", code, stderr)
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("读取 dry-run manifest 失败: %v", err)
		}
		var manifest upstreambaseurlmigration.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("dry-run manifest 不是合法 JSON: %v", err)
		}
		return manifest
	}

	// 迁移前：dry-run 与 preflight 的凭据分类必须一致，解密失败为 0。
	dryRunBefore := runDryRun()
	code, preflightStdout, stderr := runVerify("preflight")
	if code != 0 {
		t.Fatalf("preflight 应 exit 0: exit %d, stderr %.200s", code, stderr)
	}
	var preflight upstreambaseurlmigration.Manifest
	if err := json.Unmarshal([]byte(preflightStdout), &preflight); err != nil {
		t.Fatalf("preflight 输出不是合法 JSON: %v\n%.200s", err, preflightStdout)
	}
	if preflight.Credentials != dryRunBefore.Credentials {
		t.Fatalf("preflight 凭据分类与 dry-run 不一致: preflight=%+v dry-run=%+v", preflight.Credentials, dryRunBefore.Credentials)
	}
	if want := (upstreambaseurlmigration.Counts{Total: 1, Rewritten: 1}); preflight.Credentials != want {
		t.Fatalf("夹具凭据应分类为 1 rewrite（裸 origin 补版本根）: %+v", preflight.Credentials)
	}
	if got := countUpstreamBaseURLDecryptFailed(preflight.Review); got != 0 {
		t.Fatalf("正确 secret 下 preflight 不应有 credential_decrypt_failed: %d 条, review=%+v", got, preflight.Review)
	}
	if preflight.Connections != (upstreambaseurlmigration.Counts{Total: 1, Rewritten: 1}) {
		t.Fatalf("夹具连接应分类为 1 rewrite: %+v", preflight.Connections)
	}

	// 正式执行（三确认门禁齐备）。
	manifestPath := filepath.Join(t.TempDir(), "apply-manifest.json")
	code, _, stderr = wmRunMaintenanceCapture(t,
		"-migrate-upstream-base-url-version-root",
		"-dsn", testDB.DSN,
		"-manifest-out", manifestPath,
		"-secret", upstreamBaseURLVerifyTestSecret,
		"-node-stopped", "-go-stopped", "-backup-confirmed",
	)
	if code != 0 {
		t.Fatalf("apply 应 exit 0: exit %d, stderr %.200s", code, stderr)
	}

	// 迁移后：postflight 就绪、凭据分类与重跑 dry-run 一致，解密失败为 0。
	code, postflightStdout, stderr := runVerify("postflight")
	if code != 0 {
		t.Fatalf("postflight 应 exit 0（就绪）: exit %d, stderr %.200s", code, stderr)
	}
	var postflight upstreambaseurlmigration.VerifyReport
	if err := json.Unmarshal([]byte(postflightStdout), &postflight); err != nil {
		t.Fatalf("postflight 输出不是合法 JSON: %v\n%.200s", err, postflightStdout)
	}
	dryRunAfter := runDryRun()
	if postflight.Credentials != dryRunAfter.Credentials {
		t.Fatalf("postflight 凭据分类与 dry-run 不一致: postflight=%+v dry-run=%+v", postflight.Credentials, dryRunAfter.Credentials)
	}
	if want := (upstreambaseurlmigration.Counts{Total: 1, Unchanged: 1}); postflight.Credentials != want {
		t.Fatalf("迁移后凭据应全部 unchanged: %+v", postflight.Credentials)
	}
	if postflight.PendingCredentials != 0 || !postflight.Ready() {
		t.Fatalf("迁移后 postflight 应就绪: pendingCredentials=%d ready=%v", postflight.PendingCredentials, postflight.Ready())
	}
	if got := countUpstreamBaseURLDecryptFailed(postflight.Review); got != 0 {
		t.Fatalf("正确 secret 下 postflight 不应有 credential_decrypt_failed: %d 条, review=%+v", got, postflight.Review)
	}
}
