package accounts

// B4 ⑨：账户列表供应商显示面切 vendors + 行为键（设计 §439/§8）。
//
// fresh DDL 无 providers 表，旧 `LEFT JOIN providers ON code = provider_code`
// 在 fresh 库直接 no such table（列表故障面）。切换后 JOIN 键与投影走
// COALESCE(vendor, provider) 行为键（与 patch.go 行读、本文件过滤/排序列同
// 口径）。本用例在夹具库上钉三种行形态：
//
//  1. 新行（vendor_code='gpt'，provider_code 停写默认 ''）→ 命中 vendors 行，
//     providerName 回显、providerCode 行为键取 vendor；
//  2. legacy 行（vendor_code NULL，provider_code='gpt'）→ 行为键回落旧列，
//     仍命中同一 vendors 行（存量迁移窗口契约）；
//  3. generic 行（vendor_code NULL，provider_code='openai'）→ 'openai' 不在
//     vendors 值域（§439 删除 openai/hybrid 行），JOIN 不中 provider_name 为
//     NULL（响应 omitempty），列表不得报错。

import (
	"context"
	"testing"
)

func TestB4ListProviderNameJoinsVendors(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	now := "2026-09-17T00:00:00.000Z"

	// vendors 'gpt' 行由 newTestEnv 公共种子提供（ven-fixture-gpt，name='GPT'），
	// 此处不重复插入。

	sealed := func(t *testing.T, id string) string {
		t.Helper()
		value, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-b4-" + id})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	seed := func(t *testing.T, id, name string, vendorCode, providerCode any) {
		t.Helper()
		env.exec(t, `INSERT INTO accounts (id, system_account_id, vendor_code, provider_code,
			provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
			credentials_encrypted, credential_mask, health_check_model, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-see***', 'gpt-4o-mini', ?, ?)`,
			id, adminID, vendorCode, providerCode, name, sealed(t, id), now, now)
	}
	seed(t, "acc-b4-vendor", "b4-vendor", "gpt", "")
	seed(t, "acc-b4-legacy", "b4-legacy", nil, "gpt")
	seed(t, "acc-b4-generic", "b4-generic", nil, "openai")

	page, err := env.store.ListPage(context.Background(), scope, ListOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("列表查询应成功（vendors JOIN 不得复现 providers no such table 故障面）：%v", err)
	}
	byID := map[string]ListItem{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	vendor := byID["acc-b4-vendor"]
	if vendor.ID == "" {
		t.Fatal("vendor 行应在列表中")
	}
	if vendor.ProviderName == nil || *vendor.ProviderName != "GPT" {
		t.Fatalf("新行 providerName 应取 vendors.name：%+v", vendor.ProviderName)
	}
	if vendor.ProviderCode != "gpt" {
		t.Fatalf("新行 providerCode 行为键应取 vendor_code：%q", vendor.ProviderCode)
	}
	legacy := byID["acc-b4-legacy"]
	if legacy.ID == "" {
		t.Fatal("legacy 行应在列表中")
	}
	if legacy.ProviderName == nil || *legacy.ProviderName != "GPT" {
		t.Fatalf("legacy 行 providerName 应经行为键回落命中 vendors：%+v", legacy.ProviderName)
	}
	if legacy.ProviderCode != "gpt" {
		t.Fatalf("legacy 行 providerCode 应回落旧列：%q", legacy.ProviderCode)
	}
	generic := byID["acc-b4-generic"]
	if generic.ID == "" {
		t.Fatal("generic 行应在列表中")
	}
	if generic.ProviderName != nil {
		t.Fatalf("generic 行（openai 不在 vendors 值域）providerName 应为 NULL：%+v", generic.ProviderName)
	}
	if generic.ProviderCode != "openai" {
		t.Fatalf("generic 行 providerCode 应回落旧列原值：%q", generic.ProviderCode)
	}
}
