// authz_gaps_test.go：授权面补齐切片的覆盖 —— keyword 名称维度检索、写路径
// 存储错误 500 分流、{id}/usage 读路径不内联 sweep、admin 详情 owner scope
// 强制与 admin 列表全量语义。
package authz

import (
	"context"
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

// TestGapKeywordMatchesNameDimensions keyword 前缀检索按设计 :330 命中资源
// 名称、被授权用户展示名、被授权团队名称与资源归属人展示名。
func TestGapKeywordMatchesNameDimensions(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "张三", "active")
	f.seedAccount(t, "李四", "active")
	f.seedTeamWithMember(t, "team_alpha", "李四")
	f.seedProvisionSource(t, "acc_prod", "张三", "生产账户-01", "gpt")
	f.seedGroup(t, "grp_prod", "张三")

	direct, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc_prod",
		GranteeType: "system_account", GranteeUsername: "李四",
	}, "张三", false, "张三")
	if err != nil {
		t.Fatal(err)
	}
	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_prod",
		GranteeType: "team", TeamID: "team_alpha",
	}, "张三", false, "张三")
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
	assert("生产账", direct.GrantID)
	// 被授权用户展示名/用户名前缀。
	assert("李四", direct.GrantID)
	// 资源归属人展示名/用户名前缀（两行归属人都是张三）。
	assert("张三", direct.GrantID, teamOffer.GrantID)
	// 被授权团队名称（system_teams.name = "Team team_alpha"）前缀。
	assert("Team team_a", teamOffer.GrantID)
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
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_500",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	admin := &authsys.AuthContext{SystemAccountID: "admin", Role: "admin"}

	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}

	// POST /authorizations（admin 面带授权人）。
	createBody := `{"resourceType":"group","resourceId":"grp_500","granteeType":"system_account","granteeUsername":"grantee"}`
	createReq := httptest.NewRequest(http.MethodPost, "/authorizations?systemAccountId=owner", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Content-Length", itoa(len(createBody)))
	createReq = createReq.WithContext(authsys.WithAuthContext(createReq.Context(), admin))
	rec := httptest.NewRecorder()
	deps.create(rec, createReq)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "授权操作失败") {
		t.Fatalf("create storage error = %d %s", rec.Code, rec.Body.String())
	}

	// DELETE /authorizations/{id}。
	revokeReq := httptest.NewRequest(http.MethodDelete, "/authorizations/"+created.GrantID, strings.NewReader(`{}`))
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeReq.Header.Set("Content-Length", itoa(len(`{}`)))
	revokeReq.SetPathValue("id", created.GrantID)
	revokeReq = revokeReq.WithContext(authsys.WithAuthContext(revokeReq.Context(), admin))
	rec = httptest.NewRecorder()
	deps.revoke(rec, revokeReq)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "授权操作失败") {
		t.Fatalf("revoke storage error = %d %s", rec.Code, rec.Body.String())
	}

	// PATCH /authorizations/{id}：更新面只允许备注/到期时间，这里用 remark 走到
	// 存储错误。
	patchBody := `{"remark":"备注"}`
	patchReq := httptest.NewRequest(http.MethodPatch, "/authorizations/"+created.GrantID, strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Content-Length", itoa(len(patchBody)))
	patchReq.SetPathValue("id", created.GrantID)
	patchReq = patchReq.WithContext(authsys.WithAuthContext(patchReq.Context(), admin))
	rec = httptest.NewRecorder()
	deps.patch(rec, patchReq, false)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "授权操作失败") {
		t.Fatalf("patch storage error = %d %s", rec.Code, rec.Body.String())
	}
}

// TestGapUsageDetailDoesNotSweepInline {id}/usage 读路径不再内联执行到期
// 翻转：已到期的授权在响应后仍保持原状态（翻转由 jobs 每分钟任务与
// authz-expiry-runtime-sync reconcile 承担）。
func TestGapUsageDetailDoesNotSweepInline(t *testing.T) {
	f := newUsageFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_sweep", "owner")
	grantID, _ := f.createAcceptedDirectOffer(t, "group", "grp_sweep", "grantee")
	past := "2020-01-01T00:00:00.000Z"
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants SET expires_at = ? WHERE id = ?`), past, grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorizations SET expires_at = ?`), past); err != nil {
		t.Fatal(err)
	}

	deps := &Deps{Store: f.store}
	req := httptest.NewRequest(http.MethodGet, "/authorizations/"+grantID+"/usage", nil)
	req.SetPathValue("id", grantID)
	req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: "admin", Role: "admin"}))
	rec := httptest.NewRecorder()
	deps.usageDetail(rec, req, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage detail = %d %s", rec.Code, rec.Body.String())
	}
	var grantStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_grants WHERE id = ?`), grantID).Scan(&grantStatus); err != nil {
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
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_scope",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	if rec := gapDetailRequest(t, deps, created.GrantID, "systemAccountId=owner", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("scoped owner detail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := gapDetailRequest(t, deps, created.GrantID, "systemAccountId=other", "admin", "admin", false); rec.Code != http.StatusNotFound {
		t.Fatalf("scoped mismatch detail = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec := gapDetailRequest(t, deps, created.GrantID, "", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("unscoped admin detail = %d %s", rec.Code, rec.Body.String())
	}
	// "all" 过滤值与无过滤同义（accessFor 归一）。
	if rec := gapDetailRequest(t, deps, created.GrantID, "systemAccountId=all", "admin", "admin", false); rec.Code != http.StatusOK {
		t.Fatalf("all-scoped admin detail = %d %s", rec.Code, rec.Body.String())
	}
}

// TestGapAdminListIgnoresLegacyDirectionScope 管理面列表是“全部业务记录”视图：
// 不再按 ?systemAccountId / ?direction 派生归属视角，所有合法查询返回同一批
// offer；status 过滤仍然生效。
func TestGapAdminListIgnoresLegacyDirectionScope(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_a", "owner")
	f.seedGroup(t, "grp_b", "owner")
	f.seedTeamWithMember(t, "team_dir", "grantee")
	direct, _ := f.createAcceptedDirectOffer(t, "group", "grp_a", "grantee")
	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_b",
		GranteeType: "team", TeamID: "team_dir",
	}, "owner", false, "owner")
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
			id, _ := item["grantId"].(string)
			if id == "" {
				id, _ = item["id"].(string)
			}
			if id == "" {
				t.Fatalf("offer row missing grantId: %v", item)
			}
			got[id] = true
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
	// 管理面不按 systemAccountId / direction 派生视角：所有查询都返回完整
	// 业务记录集合。
	expect(list("systemAccountId=owner&direction=outbound"), direct, teamOffer.GrantID)
	expect(list("systemAccountId=owner&direction=inbound"), direct, teamOffer.GrantID)
	expect(list("systemAccountId=grantee&direction=inbound"), direct, teamOffer.GrantID)
	expect(list("systemAccountId=grantee&direction=outbound"), direct, teamOffer.GrantID)
	expect(list("systemAccountId=grantee"), direct, teamOffer.GrantID)
	expect(list("direction=inbound"), direct, teamOffer.GrantID)
	// status 过滤仍按 grant 状态生效：已领取的直接授权为 active，未领取的
	// 团队 offer 为 pending。
	expect(list("status=active"), direct)
	expect(list("status=pending"), teamOffer.GrantID)
	expect(list("status=expired"))
}
