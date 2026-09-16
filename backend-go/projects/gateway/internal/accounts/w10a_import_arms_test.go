package accounts

// w10a import 臂攻坚：executeImportPlan 的代理创建失败链（含未解析代理引用
// 的账户失败臂）与账户重复名的 failed/skip 双臂。夹具沿用 w2_import_source
// 的 sub2api 文档契约；代理名称唯一索引与生产 schema 对齐后注入冲突。

import (
	"context"
	"strings"
	"testing"
)

// w10aImportEnv 构造 sub2api 导入链环境：openai 兼容供应商 + 名称唯一索引。
func w10aImportEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	// 生产 schema（maintenance/internal/schema/sqlite_schema.go）带代理名称
	// 唯一索引；测试夹具补齐以对齐冲突语义。
	if _, err := env.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique
		ON proxy_profiles(name)`); err != nil {
		t.Fatal(err)
	}
	adminID := env.login(t, "root", "root-pass", "super_admin")
	return env, adminID
}

func TestW10AImportProxyFailedArm(t *testing.T) {
	env, adminID := w10aImportEnv(t)
	now := "2026-09-16T00:00:00.000Z"
	// 预置同名停用代理：计划期查不到（enabled=1 过滤），创建期命中唯一索引，
	// 复用回退也查不到 → 代理失败臂。
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port,
		enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w10a-dup', ?, '来源代理', 'socks5', 'proxy.example.com', 1080,
		0, 'unknown', ?, ?)`, adminID, now, now)

	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"proxies": []any{
			map[string]any{"proxy_key": "p1", "name": "来源代理", "protocol": "http",
				"host": "proxy.example.com", "port": float64(8080), "status": "active"},
		},
		"accounts": []any{
			map[string]any{"name": "w10a代理失败账户", "platform": "OpenAI", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-w10a-1", "base_url": "https://api.openai.com/v1"},
				"proxy_key":   "p1"},
		},
	}
	result, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api,
		ImportOptions{}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport 错误：%v", err)
	}
	if !result.Imported {
		t.Fatalf("执行应完成：%+v", result.Summary)
	}
	if result.Summary.Proxies.Failed != 1 {
		t.Fatalf("代理失败计数不一致：%+v", result.Summary)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionFailed {
		t.Fatalf("代理项应失败：%+v", result.Proxies)
	}
	joined := ""
	for _, item := range result.Proxies {
		if item.Messages != nil {
			for _, message := range item.Messages {
				joined += message + "|"
			}
		}
	}
	if !strings.Contains(joined, "代理名称已存在：来源代理") {
		t.Fatalf("代理失败消息缺失：%s", joined)
	}
	// 代理失败 → 引用它的账户在任何写入前失败。
	if result.Summary.Accounts.Failed != 1 {
		t.Fatalf("账户失败计数不一致：%+v", result.Summary)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed {
		t.Fatalf("账户项应失败：%+v", result.Accounts)
	}
	message := ""
	if result.Accounts[0].Messages != nil {
		message = strings.Join(result.Accounts[0].Messages, "|")
	}
	if !strings.Contains(message, "代理创建失败，账户未导入：sub2api-proxy-1") {
		t.Fatalf("账户失败消息缺失：%s", message)
	}
	if count := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = 'w10a代理失败账户'`); count != 0 {
		t.Fatalf("失败的账户不应落库：%d", count)
	}
}

func TestW10AImportDuplicateAccountSkipAndFail(t *testing.T) {
	env, adminID := w10aImportEnv(t)
	// 预置同名账户：INSERT 命中 idx_accounts_owner_name_unique。
	env.seedM11Account(t, "acc-w10a-dup", adminID, "w10a重复名账户", "api_key", "active",
		Credentials{"api_key": "sk-existing"})

	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"accounts": []any{
			map[string]any{"name": "w10a重复名账户", "platform": "OpenAI", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-w10a-2", "base_url": "https://api.openai.com/v1"}},
		},
	}

	// 未开启 skipDuplicates → failed 臂。
	result, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api,
		ImportOptions{}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport 错误：%v", err)
	}
	if result.Summary.Accounts.Failed != 1 {
		t.Fatalf("failed 臂计数不一致：%+v", result.Summary)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed {
		t.Fatalf("账户项应失败：%+v", result.Accounts)
	}
	message := ""
	if result.Accounts[0].Messages != nil {
		message = strings.Join(result.Accounts[0].Messages, "|")
	}
	if !strings.Contains(message, "同一用户下账户名称已存在：w10a重复名账户") {
		t.Fatalf("重复名消息缺失：%s", message)
	}

	// 开启 skipDuplicates：skip 臂数学不可达（Create 已把 UNIQUE 冲突预转为
	// ConflictError，import.go:1654 的 duplicateAccountNameError 不再命中原始
	// 约束文本），按已文档化的当前行为（w2_import_test.go）断言 failed。
	skipOptions := ImportOptions{SkipDuplicates: boolPtrW10(true)}
	result, err = env.store.ExecuteImport(context.Background(), document, importSourceSub2Api,
		skipOptions, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport skip 错误：%v", err)
	}
	if result.Summary.Accounts.Failed != 1 {
		t.Fatalf("skip 配置下当前行为为 failed：%+v", result.Summary)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed {
		t.Fatalf("账户项应按当前行为失败：%+v", result.Accounts)
	}
	message = ""
	if result.Accounts[0].Messages != nil {
		message = strings.Join(result.Accounts[0].Messages, "|")
	}
	if !strings.Contains(message, "同一用户下账户名称已存在") {
		t.Fatalf("失败消息缺失：%s", message)
	}
}

func boolPtrW10(value bool) *bool { return &value }
