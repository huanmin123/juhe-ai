// authz_gaps_test.go：授权面补齐切片的覆盖 —— 详情读路径 limits 回显、
// keyword 名称维度检索、写路径存储错误 500 分流、{id}/usage 读路径去内联
// sweep、admin 详情 owner scope 强制与 admin 列表 direction 语义。
package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// gapDetailRequest 以指定身份与查询串调用 admin / my-* 详情 handler。
func gapDetailRequest(t *testing.T, deps *Deps, id, query, accountID, role string, selfOnly bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/detail/"+id+"?"+query, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: accountID, Role: role}))
	rec := httptest.NewRecorder()
	deps.find(rec, req, selfOnly)
	return rec
}

// TestGapFindDetailEchoesLimits 详情读路径（GET /authorizations/{id} 与
// GET /my-authorizations/{id} 背后的 store.Find）必须回显规范化 limits
// （BUG-0175 D-126：列表行已带 limits，详情不再缺失）。
func TestGapFindDetailEchoesLimits(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_lim", "owner")
	limits := `{"daily":{"enabled":true,"limit":7.5}}`
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_lim",
		GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: &limits,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := f.store.Find(context.Background(), created.Item.ID)
	if err != nil || summary == nil {
		t.Fatalf("find: %v %v", summary, err)
	}
	if summary.Limits == nil {
		t.Fatalf("detail read must echo limits: %+v", summary)
	}
	encoded, err := json.Marshal(summary.Limits)
	if err != nil || !strings.Contains(string(encoded), `"limit":7.5`) {
		t.Fatalf("limits echo = %s err=%v", encoded, err)
	}

	// 路由级：admin 详情响应携带 limits。
	deps := &Deps{Store: f.store}
	rec := gapDetailRequest(t, deps, created.Item.ID, "", "admin", "admin", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin detail = %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Limits any `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Limits == nil {
		t.Fatalf("detail response must carry limits: %s", rec.Body.String())
	}
}

// TestGapKeywordMatchesNameDimensions keyword 前缀检索按设计 :330 命中资源
// 名称、被授权用户展示名、被授权团队名称与资源归属人展示名。
func TestGapKeywordMatchesNameDimensions(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "张三", "active")
	f.seedAccount(t, "李四", "active")
	f.seedTeamWithMember(t, "team_alpha", "李四")
	f.seedNamedAccount(t, "acc_prod", "张三", "生产账户-01")
	f.seedGroup(t, "grp_prod", "张三")

	direct, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc_prod",
		GranteeType: "system_account", GranteeID: "李四",
	}, "张三")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_prod",
		GranteeType: "team", GranteeID: "team_alpha",
	}, "张三")
	if err != nil {
		t.Fatal(err)
	}
	viewer := accessInfo{ViewerID: "admin", IsAdmin: true}
	search := func(keyword string) map[string]bool {
		t.Helper()
		items, _, _, err := f.store.ListItemsPage(context.Background(),
			Filters{Keyword: keyword, Status: "all"}, 1, 50, viewer)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, item := range items {
			got[item.ID] = true
		}
		return got
	}
	assert := func(keyword string, want ...string) {
		t.Helper()
		got := search(keyword)
		if len(got) != len(want) {
			t.Fatalf("keyword %q = %v, want %v", keyword, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("keyword %q missing %s: %v", keyword, id, got)
			}
		}
	}
	// 资源名称（accounts.name）前缀。
	assert("生产账", direct.Item.ID)
	// 被授权用户展示名/用户名前缀。
	assert("李四", direct.Item.ID)
	// 资源归属人展示名/用户名前缀（两行归属人都是张三）。
	assert("张三", direct.Item.ID, teamGrant.Item.ID)
	// 被授权团队名称（system_teams.name = "Team team_alpha"）前缀。
	assert("Team team_a", teamGrant.Item.ID)
	// 无命中保持空结果。
	assert("不存在的名字")
}

// TestGapWritePathStorageErrorsReturn500 写路径的未知存储错误走 500 通道
// （领域错误仍保持 400/409，由既有测试覆盖）。
func TestGapWritePathStorageErrorsReturn500(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_500", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_500",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	admin := &authsys.AuthContext{SystemAccountID: "admin", Role: "admin"}

	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}

	// POST /authorizations（admin 面带授权人）。
	createBody := `{"resourceType":"group","resourceId":"grp_500","granteeType":"system_account","granteeId":"grantee"}`
	createReq := httptest.NewRequest(http.MethodPost, "/authorizations?systemAccountId=owner", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Content-Length", itoaGap(len(createBody)))
	createReq = createReq.WithContext(authsys.WithAuthContext(createReq.Context(), admin))
	rec := httptest.NewRecorder()
	deps.create(rec, createReq)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "创建授权失败") {
		t.Fatalf("create storage error = %d %s", rec.Code, rec.Body.String())
	}

	// DELETE /authorizations/{id}。
	revokeBody := `{"expectedUpdatedAt":"` + created.Item.UpdatedAt + `"}`
	revokeReq := httptest.NewRequest(http.MethodDelete, "/authorizations/"+created.Item.ID, strings.NewReader(revokeBody))
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeReq.Header.Set("Content-Length", itoaGap(len(revokeBody)))
	revokeReq.SetPathValue("id", created.Item.ID)
	revokeReq = revokeReq.WithContext(authsys.WithAuthContext(revokeReq.Context(), admin))
	rec = httptest.NewRecorder()
	deps.revoke(rec, revokeReq)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "回收授权失败") {
		t.Fatalf("revoke storage error = %d %s", rec.Code, rec.Body.String())
	}

	// PATCH /authorizations/{id}。
	patchBody := `{"expectedUpdatedAt":"` + created.Item.UpdatedAt + `","status":"paused"}`
	patchReq := httptest.NewRequest(http.MethodPatch, "/authorizations/"+created.Item.ID, strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Content-Length", itoaGap(len(patchBody)))
	patchReq.SetPathValue("id", created.Item.ID)
	patchReq = patchReq.WithContext(authsys.WithAuthContext(patchReq.Context(), admin))
	rec = httptest.NewRecorder()
	deps.patch(rec, patchReq, false)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "修改授权失败") {
		t.Fatalf("patch storage error = %d %s", rec.Code, rec.Body.String())
	}

	// DELETE /authorizations/{id}/return。
	returnReq := httptest.NewRequest(http.MethodDelete, "/authorizations/"+created.Item.ID+"/return", strings.NewReader(revokeBody))
	returnReq.Header.Set("Content-Type", "application/json")
	returnReq.Header.Set("Content-Length", itoaGap(len(revokeBody)))
	returnReq.SetPathValue("id", created.Item.ID)
	returnReq = returnReq.WithContext(authsys.WithAuthContext(returnReq.Context(), admin))
	rec = httptest.NewRecorder()
	deps.returnValue(rec, returnReq, false)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "归还授权使用权失败") {
		t.Fatalf("return storage error = %d %s", rec.Code, rec.Body.String())
	}
}

// itoaGap is the test-local integer formatter for Content-Length headers.
func itoaGap(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// TestGapUsageDetailDoesNotSweepInline {id}/usage 读路径不再内联执行过期
// sweep：已到期的授权在响应后仍保持原状态（sweep 由 jobs 每分钟任务与
// authz-expiry-runtime-sync reconcile 承担）。
func TestGapUsageDetailDoesNotSweepInline(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_sweep", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_sweep",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	past := "2020-01-01T00:00:00.000Z"
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET expires_at = ? WHERE id = ?`, past, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorizations SET expires_at = ?`, past); err != nil {
		t.Fatal(err)
	}

	deps := &Deps{Store: f.store}
	req := httptest.NewRequest(http.MethodGet, "/authorizations/"+created.Item.ID+"/usage", nil)
	req.SetPathValue("id", created.Item.ID)
	req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: "admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	deps.usageDetail(rec, req, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage detail = %d %s", rec.Code, rec.Body.String())
	}
	var grantStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusActive {
		t.Fatalf("read path must not expire the grant inline: %s", grantStatus)
	}
}

// TestGapAdminDetailEnforcesOwnerScope admin 详情带 ?systemAccountId=X 时强制
// 资源归属过滤：不匹配返回 404，与 {id}/usage 的口径一致；无过滤保持原契约。
func TestGapAdminDetailEnforcesOwnerScope(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "other", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_scope", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_scope",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	if rec := gapDetailRequest(t, deps, created.Item.ID, "systemAccountId=owner", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("scoped owner detail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := gapDetailRequest(t, deps, created.Item.ID, "systemAccountId=other", "admin", "admin", false); rec.Code != http.StatusNotFound {
		t.Fatalf("scoped mismatch detail = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec := gapDetailRequest(t, deps, created.Item.ID, "", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("unscoped admin detail = %d %s", rec.Code, rec.Body.String())
	}
	// "all" 过滤值与无过滤同义（accessFor 归一）。
	if rec := gapDetailRequest(t, deps, created.Item.ID, "systemAccountId=all", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("all-scoped admin detail = %d %s", rec.Code, rec.Body.String())
	}
}

// TestGapAdminListDirectionSemantics admin 列表带 systemAccountId 时 direction
// 生效：outbound 只看归属人的资源，inbound 复用 self 面条件（直授给该账号或
// 该账号是活跃团队成员的团队授权）；无 systemAccountId 时保持历史忽略。
func TestGapAdminListDirectionSemantics(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_a", "owner")
	f.seedGroup(t, "grp_b", "owner")
	f.seedTeamWithMember(t, "team_dir", "grantee")
	direct, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_a",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	teamGrant, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_b",
		GranteeType: "team", GranteeID: "team_dir",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	list := func(query string) map[string]bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/authorizations?"+query, nil)
		req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: "admin", Role: "admin"}))
		rec := httptest.NewRecorder()
		deps.list(rec, req, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %q = %d %s", query, rec.Code, rec.Body.String())
		}
		items := decodeListItems(t, rec)
		got := map[string]bool{}
		for _, item := range items {
			got[item["id"].(string)] = true
		}
		return got
	}
	expect := func(got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("items = %v, want %v", got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("items missing %s: %v", id, got)
			}
		}
	}
	// 归属人视角：outbound 两行，inbound 空。
	expect(list("systemAccountId=owner&direction=outbound"), direct.Item.ID, teamGrant.Item.ID)
	expect(list("systemAccountId=owner&direction=inbound"))
	// 被授权视角：inbound 命中直授 + 团队授权，outbound 空。
	expect(list("systemAccountId=grantee&direction=inbound"), direct.Item.ID, teamGrant.Item.ID)
	expect(list("systemAccountId=grantee&direction=outbound"))
	// 无 direction：沿用默认范围口径（归属 ∪ 被授 ∪ 团队成员）。
	expect(list("systemAccountId=grantee"), direct.Item.ID, teamGrant.Item.ID)
	// 无 systemAccountId：direction 保持历史忽略。
	expect(list("direction=inbound"), direct.Item.ID, teamGrant.Item.ID)
}
