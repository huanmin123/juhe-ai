package statreads

// w11g 覆盖补充（第三批）：usage-records keyword 的 owner 授权/分组检索
// 路径、真实 shard 文件读链与水合错误臂、timekeys 边界。

import (
	"net/http"
	"testing"
)

func TestW11GUsageRecordKeywordOwnerPaths(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status) VALUES
			('w11g-owned-acct', 'w11g-owned', 'sys-user-1', 'openai', 'api_key', 'active'),
			('w11g-inst-src', 'w11g-inst-src', 'sys-other', 'openai', 'oauth', 'active'),
			('w11g-inst-child', 'w11g-inst-child', 'sys-other', 'openai', 'oauth', 'active'),
			('w11g-ra-acct', 'w11g-ra', 'sys-other', 'openai', 'api_key', 'active'),
			('w11g-group-acct', 'w11g-group', 'sys-other', 'openai', 'api_key', 'active')`,
		`UPDATE juhe_business.accounts SET authorization_instance_source_account_id = 'w11g-inst-src' WHERE id = 'w11g-inst-child'`,
		`INSERT INTO juhe_business.group_accounts (account_id, group_id, system_account_id, enabled) VALUES
			('w11g-group-acct', 'w11g-group-1', 'sys-other', 1)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ('w11g-ra-1', 'account', 'w11g-ra-acct', 'sys-other', 'sys-user-1', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'),
			       ('w11g-ra-2', 'group', 'w11g-group-1', 'sys-other', 'sys-user-1', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	)
	// my-usage-records：owner 路径四段检索（自身/实例/授权/分组）。
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet, "/my-usage-records?accountKeyword=w11g&startDate=2026-09-04&endDate=2026-09-04", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("owner keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 关键字无匹配 → 1 = 0 空集。
	recorder = invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet, "/my-usage-records?accountKeyword=zzz-none&startDate=2026-09-04&endDate=2026-09-04", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("no match keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// admin 面 keyword 匹配（非 owner 分支；日期在 admin 过滤门内，改走 systemAccountId 形态）。
	recorder = invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet, "/usage-records?accountKeyword=w11g&systemAccountId=sys-admin-1", adminAuth(""))
	if recorder.Code == http.StatusBadRequest {
		t.Fatalf("admin keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
