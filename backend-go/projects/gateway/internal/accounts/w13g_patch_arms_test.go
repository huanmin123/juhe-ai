package accounts

// w13g Patch 可达错误臂补测：代理解析、凭据信封、余额配置、标签、分组绑定
// 与探活开关传播。
//
// 不可达登记（w13g）：
// - patch.go:559-561 / 606-608 / 663-665 / 715-717 / 751-753 / 970-972 /
//   1016-1018 事务内卫星写与加密错误臂：事务持有唯一连接（SetMaxOpenConns(1)），
//   存活期间无法注入 DDL，EncryptJSON 对已验证形状恒成功。
// - patch.go:1159-1163 CAS affected != 1：revision 在同一事务内先读后写，
//   单连接内无法在两步之间篡改行。
// - patch.go:526-529 / 533-535 models Scan/Err、1055-1057 tags Scan 臂：
//   单连接下 Next/Scan/Err 之间无故障注入口。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW13GPatchProxyArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-px", adminID, "w13g-px", "active")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w13g-off', ?, '停用代理', 'http', '192.0.2.1', 8080, 0, ?, ?)`, adminID, now, now)
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 不存在的代理。
	missing := "proxy-w13g-none"
	if _, err := env.store.Patch(context.Background(), "acc-w13g-px", PatchInput{
		ExpectedConfigRevision: 1, ProxyProfileID: &missing, ProxyProfileIDPresent: true,
	}, admin); err == nil || !strings.Contains(err.Error(), "代理不存在") {
		t.Fatalf("缺失代理：%v", err)
	}
	// 停用的代理。
	disabled := "proxy-w13g-off"
	if _, err := env.store.Patch(context.Background(), "acc-w13g-px", PatchInput{
		ExpectedConfigRevision: 1, ProxyProfileID: &disabled, ProxyProfileIDPresent: true,
	}, admin); err == nil || !strings.Contains(err.Error(), "代理不存在") {
		t.Fatalf("停用代理：%v", err)
	}
	// 代理清空（nil 值）→ 成功移除。
	_, err := env.store.Patch(context.Background(), "acc-w13g-px", PatchInput{
		ExpectedConfigRevision: 1, ProxyProfileIDPresent: true,
	}, admin)
	if err != nil {
		t.Fatalf("清空代理：%v", err)
	}
}

func TestW13GPatchCredentialEnvelopeArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-ce", adminID, "w13g-ce", "active")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 存量凭据信封损坏 + 请求支持模型（需要 resolveFinalCredentials）。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = 'acc-w13g-ce'`)
	if _, err := env.store.Patch(context.Background(), "acc-w13g-ce", PatchInput{
		ExpectedConfigRevision: 1, SupportedModels: []string{"gpt-4o-mini"}, SupportedModelsPresent: true,
	}, admin); err == nil {
		t.Fatal("坏信封应报错")
	}
}

func TestW13GPatchBalanceAndTagsArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-bal", adminID, "w13g-bal", "active")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-w13g-bal', ?, '平衡标签', ?, ?)`, adminID, now, now)
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 非法余额配置 canonical。
	badConfig := "{not-json"
	enabled := true
	if _, err := env.store.Patch(context.Background(), "acc-w13g-bal", PatchInput{
		ExpectedConfigRevision: 1, BalanceQueryEnabled: &enabled,
		BalanceQueryConfigCanonical: &badConfig, BalanceQueryConfigPresent: true,
	}, admin); err == nil {
		t.Fatal("坏余额配置应报错")
	}
	// 合法余额配置 + 标签替换。
	goodConfig := `{"enabled":true}`
	result, err := env.store.Patch(context.Background(), "acc-w13g-bal", PatchInput{
		ExpectedConfigRevision: 1, BalanceQueryEnabled: &enabled,
		BalanceQueryConfigCanonical: &goodConfig, BalanceQueryConfigPresent: true,
		Tags: []string{"平衡标签"}, TagsPresent: true,
	}, admin)
	if err != nil || result == nil {
		t.Fatalf("余额+标签补丁：%v %v", result, err)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_tag_bindings WHERE account_id = 'acc-w13g-bal'`) != 1 {
		t.Fatal("标签应替换")
	}

	// account_tags 表缺失 → 标签读取错误。
	env.exec(t, `DROP TABLE account_tags`)
	if _, err := env.store.Patch(context.Background(), "acc-w13g-bal", PatchInput{
		ExpectedConfigRevision: 2, Tags: []string{"x"}, TagsPresent: true,
	}, admin); err == nil {
		t.Fatal("account_tags 缺失应报错")
	}
}

func TestW13GPatchGroupAndProbeArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-grp", adminID, "w13g-grp", "active")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 分组切换到缺失分组 → 断言失败。
	missingGroup := "grp-w13g-none"
	if _, err := env.store.Patch(context.Background(), "acc-w13g-grp", PatchInput{
		ExpectedConfigRevision: 1, GroupID: &missingGroup, GroupIDPresent: true,
	}, admin); err == nil {
		t.Fatal("缺失分组应报错")
	}

	// 探活开关翻转 + 授权实例存在 → 名称唯一性阶梯与传播链。
	env.seedAccount(t, "acc-w13g-grp-src", adminID, "w13g-grp-src", "active")
	env.seedAccount(t, "acc-w13g-grp-inst", adminID, "w13g-grp-inst", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-w13g-grp',
		authorization_instance_source_account_id = 'acc-w13g-grp-src' WHERE id = 'acc-w13g-grp-inst'`)
	probeOff := false
	if _, err := env.store.Patch(context.Background(), "acc-w13g-grp-src", PatchInput{
		ExpectedConfigRevision: 1, TemporaryUnavailableContinuousProbeEnabled: &probeOff,
	}, admin); err != nil {
		t.Fatalf("探活开关翻转：%v", err)
	}
}
