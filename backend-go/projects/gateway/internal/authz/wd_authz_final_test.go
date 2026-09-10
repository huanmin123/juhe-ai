package authz

// authz 第四波补充（wd_ 前缀，独占新增）：列表查询整数解析的 zod 文案矩阵、
// 账户资源（含到期与实例名回退）的列表投影、使用明细路由的参数校验分支。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWdListParserIntegerContract(t *testing.T) {
	f := newFixture(t)
	deps := &Deps{Store: f.store}
	auth := wdAuthCtx("admin", "admin")
	cases := []struct {
		name    string
		query   string
		message string
	}{
		{"page 非数字", "/?page=abc", ""},
		{"page 小数", "/?page=1.5", ""},
		{"page 空值", "/?page=", "页码必须大于 0"},
		{"page 为零", "/?page=0", "页码必须大于 0"},
		{"pageSize 非数字", "/?pageSize=abc", ""},
		{"pageSize 超上限", "/?pageSize=501", "每页最多 500 条"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			deps.list(recorder, wdGetReq(t, http.MethodGet, tc.query, auth), false)
			if tc.message != "" && (recorder.Code != http.StatusBadRequest || decodeWd(recorder) != tc.message) || (tc.message == "" && recorder.Code != http.StatusBadRequest) {
				t.Fatalf("%s 应 400/%s: %d %s", tc.name, tc.message, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWdUsageDetailParserContract(t *testing.T) {
	f := newUsageFixture(t)
	deps := &Deps{Store: f.store}
	auth := wdAuthCtx("admin", "admin")
	request := func(query string) *http.Request {
		request := wdGetReq(t, http.MethodGet, query, auth)
		request.SetPathValue("id", "any")
		return request
	}
	cases := []struct {
		name    string
		query   string
		message string
	}{
		{"systemAccountId 空值", "/?systemAccountId=", "系统账号 ID 不能为空"},
		{"开始日期格式", "/?startDate=20260904", "开始日期格式应为 YYYY-MM-DD"},
		{"结束日期格式", "/?endDate=x", "结束日期格式应为 YYYY-MM-DD"},
		{"page 非数字", "/?page=oops", ""},
		{"pageSize 非数字", "/?pageSize=oops", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			deps.usageDetail(recorder, request(tc.query), false)
			if tc.message != "" && (recorder.Code != http.StatusBadRequest || decodeWd(recorder) != tc.message) || (tc.message == "" && recorder.Code != http.StatusBadRequest) {
				t.Fatalf("%s 应 400/%s: %d %s", tc.name, tc.message, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWdListAccountResourceProjection(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedTeamWithMember(t, "team_acc", "grantee")
	// 授权资源账户带到期时间；另一条经实例克隆命名。
	f.execResourceAccount(t, "acc_exp", "owner")
	if _, err := f.db.Exec(`UPDATE accounts SET account_expires_at = '2027-01-01T00:00:00.000Z' WHERE id = 'acc_exp'`); err != nil {
		t.Fatal(err)
	}
	f.seedGroup(t, "grp_acc", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc_exp",
		GranteeType: "system_account", GranteeID: "grantee",
		Remark: ptrString("带备注"), ExpiresAt: func() *string { v := "2026-12-31T00:00:00Z"; return &v }(),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_acc",
		GranteeType: "team", GranteeID: "team_acc",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_ = teamGrant
	items, total, _, err := f.store.ListItemsPage(context.Background(), Filters{Status: "all"}, 1, 50,
		accessInfo{ViewerID: "admin", IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = total
	byID := map[string]ListItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	accountItem := byID[created.Item.ID]
	if accountItem.ResourceName == nil || *accountItem.ResourceName != "Account acc_exp" {
		t.Fatalf("账户资源名投影错误: %#v", accountItem.ResourceName)
	}
	if accountItem.ResourceAccountExpiresAt == nil || *accountItem.ResourceAccountExpiresAt != "2027-01-01T00:00:00.000Z" {
		t.Fatalf("账户到期投影错误: %#v", accountItem.ResourceAccountExpiresAt)
	}
	if accountItem.Remark == nil || *accountItem.Remark != "带备注" {
		t.Fatalf("备注投影错误: %#v", accountItem.Remark)
	}
	if accountItem.ExpiresAt == nil {
		t.Fatalf("授权到期投影缺失: %#v", accountItem.ExpiresAt)
	}
	// 管理者可见 limits 键位（无 limits → nil 保留键位由 JSON 层处理）。
	if accountItem.EffectiveSourceType != "manual" {
		t.Fatalf("直授生效来源错误: %q", accountItem.EffectiveSourceType)
	}
}
