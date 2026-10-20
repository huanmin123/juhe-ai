package accounts

// B4 ⑨：账户列表供应商显示面切 vendors（设计 §9.11/§8）。
//
// fresh DDL 无 providers 表，旧 `LEFT JOIN providers ON code = provider_code`
// 在 fresh 库直接 no such table（列表故障面）。供应商解耦后账户身份列只剩
// vendor_code：JOIN 键、过滤列与排序键统一走 `COALESCE(accounts.vendor_code,'')`。
// 本用例在夹具库上钉两种目标行形态：
//
//  1. 真实厂商行（vendor_code='gpt'）→ 命中 vendors 行，providerName 回显、
//     providerCode 取 vendor 值；
//  2. generic_http 行（vendor_code NULL，driver='generic_http'）→ vendors JOIN
//     不中，provider_name 为 NULL（响应 omitempty）、providerCode 为空串，
//     列表不得报错。

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
	seed := func(t *testing.T, id, name string, vendorCode any, driver string) {
		t.Helper()
		env.exec(t, `INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code,
			protocol_code, protocol_version, name, type, status,
			credentials_encrypted, credential_mask, health_check_model, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-see***', 'gpt-4o-mini', ?, ?)`,
			id, adminID, vendorCode, driver, name, sealed(t, id), now, now)
	}
	seed(t, "acc-b4-vendor", "b4-vendor", "gpt", "gpt")
	seed(t, "acc-b4-generic", "b4-generic", nil, "generic_http")

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
		t.Fatalf("真实厂商行 providerName 应取 vendors.name：%+v", vendor.ProviderName)
	}
	if vendor.ProviderCode != "gpt" {
		t.Fatalf("真实厂商行 providerCode 应取 vendor_code：%q", vendor.ProviderCode)
	}
	generic := byID["acc-b4-generic"]
	if generic.ID == "" {
		t.Fatal("generic 行应在列表中")
	}
	if generic.ProviderName != nil {
		t.Fatalf("generic_http 行（vendor NULL）providerName 应为 NULL：%+v", generic.ProviderName)
	}
	if generic.ProviderCode != "" {
		t.Fatalf("generic_http 行 providerCode 应为空串：%q", generic.ProviderCode)
	}
}
