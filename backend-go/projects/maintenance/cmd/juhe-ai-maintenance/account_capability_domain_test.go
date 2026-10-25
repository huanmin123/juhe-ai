// 能力域命令 CLI 门禁与端到端测试（进程内直调 runMaintenance 与 result 函数，
// 测试进程纪律：main 是唯一 os.Exit 站点）：
//   - 用法门禁：缺 stage / 非法 stage / migrate 缺停写备份确认 / verify 缺
//     check / dsn 缺失 / 与其他命令互斥，全部退出码 2；
//   - 真实 PG 端到端（pgtestkit 隔离库）：preflight → migrate m1 → postflight
//     → 重复 migrate 只报告完成 → m2-cutover 封存 → 封存后 migrate 拒绝、
//     postflight 仍可用；失败清单路径退出码 3、阶段保留。
// SQLite 形态随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// capabilityDomainTestSecret 是 CLI 测试的解密密钥（进程内固定测试值）。
const capabilityDomainTestSecret = "capability-domain-cli-secret"

// newCapabilityDomainTestDB 打开一个完成 EnsurePostgres 的隔离测试库并预置
// fixtures 依赖的父行（system_accounts / vendors）。
func newCapabilityDomainTestDB(t *testing.T) string {
	t.Helper()
	return pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		for _, seed := range []string{
			`INSERT INTO "juhe_business"."system_accounts"
				(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
				VALUES ('sys-1', 'sys-1', 'sys-1', NULL, 'user', 'active', 'hash', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')
				ON CONFLICT (id) DO NOTHING`,
			`INSERT INTO "juhe_business"."vendors"
				(id, code, name, default_supported_models_json, enabled, created_at, updated_at)
				VALUES ('vendor-gpt', 'gpt', 'gpt', '[]', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')
				ON CONFLICT (code) DO NOTHING`,
		} {
			if _, err := db.Exec(seed); err != nil {
				return err
			}
		}
		return nil
	}).DSN
}

func seedCapabilityDomainAccounts(t *testing.T, dsn string, fixtures map[string][]string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 孤儿/悬空夹具不需要：能力域 fixtures 引用关系只在 accounts 自引用
	// （本夹具不造实例行），父行已在建库时预置。
	for id, modes := range fixtures {
		credentials := map[string]any{"api_key": "sk-" + id}
		if modes != nil {
			credentials["supported_endpoint_modes"] = modes
		}
		sealed, err := accountcrypto.EncryptJSON(capabilityDomainTestSecret, credentials)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO "juhe_business"."accounts"
			(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
			 name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
			VALUES ($1, 'sys-1', 'gpt', 'gpt', 'openai', 'v1', $1, 'api_key', $2, 'm', 'chat_json',
			 '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, id, sealed); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func TestCapabilityDomainUsageGates(t *testing.T) {
	dsn := "postgres://juhe-ai-test.invalid/testdb"
	cases := []struct {
		name string
		argv []string
	}{
		{"migrate 缺 stage", []string{"--migrate-account-capability-domain", "--dsn", dsn, "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"migrate 非法 stage", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m3", "--dsn", dsn, "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"migrate 缺 backup 确认", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--dsn", dsn, "--node-stopped", "--go-stopped"}},
		{"migrate 缺 node-stopped", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--dsn", dsn, "--go-stopped", "--backup-confirmed"}},
		{"migrate 缺 go-stopped", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--dsn", dsn, "--node-stopped", "--backup-confirmed"}},
		{"verify 缺 stage", []string{"--verify-account-capability-domain", "--account-capability-domain-check=preflight", "--dsn", dsn}},
		{"verify 缺 check", []string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--dsn", dsn}},
		{"verify 非法 check", []string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--account-capability-domain-check=midflight", "--dsn", dsn}},
		{"migrate/verify 互斥", []string{"--migrate-account-capability-domain", "--verify-account-capability-domain", "--account-capability-domain-stage=m1"}},
		{"与其他命令互斥", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema"}},
		{"dsn 缺失", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}

func TestCapabilityDomainPostgresEndToEnd(t *testing.T) {
	dsn := newCapabilityDomainTestDB(t)
	seedCapabilityDomainAccounts(t, dsn, map[string][]string{
		"acct-chat":  {"chat_json", "chat_sse"},
		"acct-volc":  {"chat_json", "video_create", "audio_speech"},
		"acct-video": {"video_create", "video_get", "video_content", "video_cancel"},
		"acct-audio": {"audio_speech"},
		"acct-image": {"images_json"},
	})
	common := []string{"--dsn", dsn, "--secret", capabilityDomainTestSecret}
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}

	// preflight：全部行待迁移 → 正常结果，退出码 0。
	if code := runMaintenance(append([]string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--account-capability-domain-check=preflight"}, common...)); code != 0 {
		t.Fatalf("preflight exit = %d, want 0", code)
	}

	// migrate m1：成功完成。
	if code := runMaintenance(append(append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1"}, common...), gates...)); code != 0 {
		t.Fatalf("migrate m1 exit = %d, want 0", code)
	}

	// postflight：通过。
	if code := runMaintenance(append([]string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--account-capability-domain-check=postflight"}, common...)); code != 0 {
		t.Fatalf("postflight exit = %d, want 0", code)
	}

	// 落库域断言。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[string]string{
		"acct-chat":  "chat",
		"acct-volc":  "chat",
		"acct-video": "video",
		"acct-audio": "audio",
		"acct-image": "image",
	}
	for id, domain := range want {
		var got string
		if err := db.QueryRow(`SELECT capability_domain FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != domain {
			t.Fatalf("%s domain = %q, want %q", id, got, domain)
		}
	}
	// 赋域 + 声明裁剪：混合行 acct-volc 裁剪至落域 lane（保留 chat_json，
	// 裁掉 video_create/audio_speech）；纯域内行声明原样保留。
	assertModes := func(id string, wantModes []string) {
		t.Helper()
		var sealed string
		if err := db.QueryRow(`SELECT credentials_encrypted FROM "juhe_business"."accounts" WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		var credentials map[string]any
		if err := accountcrypto.DecryptJSON(capabilityDomainTestSecret, sealed, &credentials); err != nil {
			t.Fatalf("decrypt %s: %v", id, err)
		}
		raw, _ := credentials["supported_endpoint_modes"].([]any)
		got := []string{}
		for _, item := range raw {
			if text, ok := item.(string); ok {
				got = append(got, text)
			}
		}
		if len(got) != len(wantModes) {
			t.Fatalf("%s modes = %v, want %v", id, got, wantModes)
		}
		seen := map[string]bool{}
		for _, mode := range wantModes {
			seen[mode] = true
		}
		for _, mode := range got {
			if !seen[mode] {
				t.Fatalf("%s modes = %v, want %v", id, got, wantModes)
			}
		}
	}
	assertModes("acct-volc", []string{"chat_json"})
	assertModes("acct-chat", []string{"chat_json", "chat_sse"})

	// 重复 migrate m1：只报告完成，退出码 0。
	if code := runMaintenance(append(append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1"}, common...), gates...)); code != 0 {
		t.Fatalf("repeat migrate m1 exit = %d, want 0", code)
	}

	// m2-cutover：封存。
	if code := runMaintenance(append(append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m2-cutover"}, common...), gates...)); code != 0 {
		t.Fatalf("m2-cutover exit = %d, want 0", code)
	}

	// 封存后 migrate 拒绝（运行期拒绝 → 1）。
	if code := runMaintenance(append(append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1"}, common...), gates...)); code != 1 {
		t.Fatalf("migrate after finalize exit = %d, want 1", code)
	}

	// 封存后 postflight 仍可执行（推导对账不适用）。
	if code := runMaintenance(append([]string{"--verify-account-capability-domain", "--account-capability-domain-stage=m2-cutover", "--account-capability-domain-check=postflight"}, common...)); code != 0 {
		t.Fatalf("postflight after finalize exit = %d, want 0", code)
	}
}

// TestCapabilityDomainFailureExitCode 覆盖失败清单路径：退出码 3、阶段保留。
func TestCapabilityDomainFailureExitCode(t *testing.T) {
	dsn := newCapabilityDomainTestDB(t)
	seedCapabilityDomainAccounts(t, dsn, map[string][]string{
		"acct-good": {"chat_json"},
		"acct-bad":  {}, // 缺省 modes → 失败清单
	})
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}
	argv := append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--dsn", dsn, "--secret", capabilityDomainTestSecret}, gates...)
	if code := runMaintenance(argv); code != 3 {
		t.Fatalf("migrate with failures exit = %d, want 3", code)
	}
	// 阶段保留 m1_running：修复后重跑收敛完成。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var valueJSON string
	if err := db.QueryRow(`SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`, capdomain.MigrationSettingKey).Scan(&valueJSON); err != nil {
		t.Fatalf("read record: %v", err)
	}
	record, err := capdomain.ParseRecord(valueJSON)
	if err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if record.Stage != capdomain.StageM1Running {
		t.Fatalf("record stage = %q, want m1_running", record.Stage)
	}
	// 修复后同阶段重跑 → 0。
	if _, err := db.Exec(`UPDATE "juhe_business"."accounts" SET credentials_encrypted = $1 WHERE id = 'acct-bad'`,
		mustSealedModes(t, "acct-bad", []string{"chat_json"})); err != nil {
		t.Fatal(err)
	}
	if code := runMaintenance(argv); code != 0 {
		t.Fatalf("repaired migrate exit = %d, want 0", code)
	}
}

func mustSealedModes(t *testing.T, id string, modes []string) string {
	t.Helper()
	sealed, err := accountcrypto.EncryptJSON(capabilityDomainTestSecret, map[string]any{
		"api_key":                  "sk-" + id,
		"supported_endpoint_modes": modes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
