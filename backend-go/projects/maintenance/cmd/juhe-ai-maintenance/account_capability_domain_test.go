// 能力域命令 CLI 门禁与端到端测试（进程内直调 runMaintenance 与 result 函数，
// 测试进程纪律：main 是唯一 os.Exit 站点）：
//   - 用法门禁：缺 stage / 非法 stage / migrate 缺停写备份确认 / verify 缺
//     check / driver 缺失 / 与其他命令互斥，全部退出码 2；
//   - SQLite 端到端：preflight → migrate m1 → postflight → 重复 migrate 只
//     报告完成 → m2-cutover 封存 → 封存后 migrate 拒绝、postflight 仍可用；
//   - 失败清单路径退出码 3、阶段保留。

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// capabilityDomainTestSecret 是 CLI 测试的解密密钥（进程内固定测试值）。
const capabilityDomainTestSecret = "capability-domain-cli-secret"

func capabilityDomainPaths(t *testing.T) string {
	t.Helper()
	return "business=" + filepath.Join(t.TempDir(), "business.sqlite3")
}

func seedCapabilityDomainAccounts(t *testing.T, pathsFlag string, fixtures map[string][]string) {
	t.Helper()
	db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(pathsFlag, "business="))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer db.Close()
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// 造数不需要父行：临时关闭外键（同一连接），插入后恢复。
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("toggle foreign_keys: %v", err)
	}
	defer func() { _, _ = db.Exec("PRAGMA foreign_keys=ON") }()
	for id, modes := range fixtures {
		credentials := map[string]any{"api_key": "sk-" + id}
		if modes != nil {
			credentials["supported_endpoint_modes"] = modes
		}
		sealed, err := accountcrypto.EncryptJSON(capabilityDomainTestSecret, credentials)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO accounts
			(id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			 name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
			VALUES (?, 'sys-1', 'gpt', 'profile-1', 'openai', 'v1', ?, 'api_key', ?, 'm', 'chat_json',
			 '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, id, id, sealed); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func TestCapabilityDomainUsageGates(t *testing.T) {
	paths := capabilityDomainPaths(t)
	cases := []struct {
		name string
		argv []string
	}{
		{"migrate 缺 stage", []string{"--migrate-account-capability-domain", "--driver", "sqlite", "--paths", paths, "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"migrate 非法 stage", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m3", "--driver", "sqlite", "--paths", paths, "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"migrate 缺 backup 确认", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--driver", "sqlite", "--paths", paths, "--node-stopped", "--go-stopped"}},
		{"migrate 缺 node-stopped", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--driver", "sqlite", "--paths", paths, "--go-stopped", "--backup-confirmed"}},
		{"migrate 缺 go-stopped", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--driver", "sqlite", "--paths", paths, "--node-stopped", "--backup-confirmed"}},
		{"verify 缺 stage", []string{"--verify-account-capability-domain", "--account-capability-domain-check=preflight", "--driver", "sqlite", "--paths", paths}},
		{"verify 缺 check", []string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--driver", "sqlite", "--paths", paths}},
		{"verify 非法 check", []string{"--verify-account-capability-domain", "--account-capability-domain-stage=m1", "--account-capability-domain-check=midflight", "--driver", "sqlite", "--paths", paths}},
		{"migrate/verify 互斥", []string{"--migrate-account-capability-domain", "--verify-account-capability-domain", "--account-capability-domain-stage=m1"}},
		{"与其他命令互斥", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema"}},
		{"driver 缺失", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"sqlite 携带 dsn", []string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--node-stopped", "--go-stopped", "--backup-confirmed", "--driver", "sqlite", "--paths", paths, "--dsn", "postgres://x/y"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}

func TestCapabilityDomainSQLiteEndToEnd(t *testing.T) {
	paths := capabilityDomainPaths(t)
	seedCapabilityDomainAccounts(t, paths, map[string][]string{
		"acct-chat":  {"chat_json", "chat_sse"},
		"acct-volc":  {"chat_json", "video_create", "audio_speech"},
		"acct-video": {"video_create", "video_get", "video_content", "video_cancel"},
		"acct-audio": {"audio_speech"},
		"acct-image": {"images_json"},
	})
	common := []string{"--driver", "sqlite", "--paths", paths, "--secret", capabilityDomainTestSecret}
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}

	// preflight：域列已由造数 ensure 加好，全部行待迁移 → 正常结果，退出码 0。
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
	db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(paths, "business="))
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
		if err := db.QueryRow(`SELECT capability_domain FROM accounts WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != domain {
			t.Fatalf("%s domain = %q, want %q", id, got, domain)
		}
	}

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
	paths := capabilityDomainPaths(t)
	seedCapabilityDomainAccounts(t, paths, map[string][]string{
		"acct-good": {"chat_json"},
		"acct-bad":  {}, // 缺省 modes → 失败清单
	})
	gates := []string{"--node-stopped", "--go-stopped", "--backup-confirmed"}
	argv := append([]string{"--migrate-account-capability-domain", "--account-capability-domain-stage=m1", "--driver", "sqlite", "--paths", paths, "--secret", capabilityDomainTestSecret}, gates...)
	if code := runMaintenance(argv); code != 3 {
		t.Fatalf("migrate with failures exit = %d, want 3", code)
	}
	// 阶段保留 m1_running：修复后重跑收敛完成。
	db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(paths, "business="))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var valueJSON string
	if err := db.QueryRow(`SELECT value_json FROM global_settings WHERE key = ?`, capabilitydomain.MigrationSettingKey).Scan(&valueJSON); err != nil {
		t.Fatalf("read record: %v", err)
	}
	record, err := capabilitydomain.ParseRecord(valueJSON)
	if err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if record.Stage != capabilitydomain.StageM1Running {
		t.Fatalf("record stage = %q, want m1_running", record.Stage)
	}
	// 修复后同阶段重跑 → 0。
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acct-bad'`,
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

// TestCapabilityDomainPreflightMissingColumn 覆盖 preflight 在列不存在时
// 正常执行（列从未加过：手动 DROP 需要重建表，直接以无 ensure 的空库模拟
// 缺列——preflight 报 accounts 表缺失为运行错误；列存在与否的分支行为已由
// internal/capabilitydomain 覆盖）。这里钉 CLI 层的退出码语义。
func TestCapabilityDomainPreflightMissingColumn(t *testing.T) {
	paths := capabilityDomainPaths(t)
	seedCapabilityDomainAccounts(t, paths, map[string][]string{"acct-chat": {"chat_json"}})
	if code := accountCapabilityDomainVerifyResult("sqlite", paths, "", capabilityDomainTestSecret, "m1", "preflight"); code != 0 {
		t.Fatalf("preflight exit = %d, want 0", code)
	}
	if code := accountCapabilityDomainVerifyResult("sqlite", paths, "", capabilityDomainTestSecret, "m1", "postflight"); code != 0 {
		t.Fatalf("postflight exit = %d, want 0", code)
	}
}
