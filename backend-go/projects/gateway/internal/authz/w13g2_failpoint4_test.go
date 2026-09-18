// w13g2 failpoint 第四批：usage 路由 500 分支（canceled ctx 透传）、
// return_group 成功链深层臂、instance 名字梯子数据占用、剩余 sync/
// mutations/downstream 错误臂。
package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// TestW13g2UsageRoute500Arms 覆盖 usage 路由的 500 与参数校验分支：该
// fixture 未建 system_settings 且未注入时区源 → resolveUsageRange 失败 →
// 各 usage 入口的 500 分支（usage_routes.go 231-360）。
func TestW13g2UsageRoute500Arms(t *testing.T) {
	f := newFixture(t)
	deps := &Deps{Store: f.store, Auth: &authsys.Deps{}}
	auth := wdAuthCtx("admin", "admin")

	recorder := httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("team rows 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "user", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("user rows 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "team", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("team summary 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account", auth), "user", false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("user summary 时区失败应 500: %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	detailReq := wdGetReq(t, http.MethodGet, "/?systemAccountId=owner", auth)
	detailReq.SetPathValue("id", "some-id")
	deps.usageDetail(recorder, detailReq, false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("usageDetail 时区失败应 500: %d", recorder.Code)
	}

	// 参数校验分支：teamId 空白（164）、summary 的 grantee 空白（272-278）、
	// resource 无 type（283）、非法日期（286）。
	recorder = httptest.NewRecorder()
	deps.usageRows(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&teamId=", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空白 teamId 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&granteeSystemAccountId=", auth), "user", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("summary 空白 grantee 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceId=x", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("summary resource 缺 type 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.usageSummary(recorder, wdGetReq(t, http.MethodGet, "/?resourceType=account&startDate=x", auth), "team", false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("summary 非法日期应 400: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13g2ReturnGroupSuccessChain 覆盖 ReturnGroupForGrantee 成功链与
// 深层错误臂（return_group.go 100-141）。
func TestW13g2ReturnGroupSuccessChain(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "rg1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_rg13', 'R', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_rg13",
		GranteeType: "system_account", GranteeID: "rg1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	// grant UPDATE 失败（106-110）。
	fp.arm("SET status = 'returned'")
	if _, err := s.ReturnGroupForGrantee(ctx, "grp_rg13", "rg1", "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// sources UPDATE 失败（111-122）。
	fp.arm("grantee_returned")
	if _, err := s.ReturnGroupForGrantee(ctx, "grp_rg13", "rg1", "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// refresh 失败（124-130）。
	fp.arm("trg.expires_at > ?")
	if _, err := s.ReturnGroupForGrantee(ctx, "grp_rg13", "rg1", "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// bindings 前置查询失败（133-135）。
	fp.arm("request_quota_hourly_window_scope_bindings")
	if _, err := s.ReturnGroupForGrantee(ctx, "grp_rg13", "rg1", "owner"); err == nil {
		fp.disarm()
		t.Fatalf("应失败")
	}
	fp.disarm()

	// 成功归还（无 failpoint）。健康输入扇出对 group 资源是空操作，
	// enqueue 错误臂由 w13g2_failpoint2_test.go 的 SyncArms 覆盖。
	grant, err := s.GetGrantForMutation(ctx, nil, created.Item.ID)
	if err != nil || grant == nil {
		t.Fatal(err)
	}
	receipt, err := s.ReturnGroupForGrantee(ctx, "grp_rg13", "rg1", "owner")
	if err != nil || receipt == nil {
		t.Fatalf("归还应成功: %+v %v", receipt, err)
	}
}

// TestW13g2InstanceNameLadder 用数据占用覆盖名字候选梯子
//（instance_provision.go 275-293）：baseName 与 baseName-<short> 依次被占，
//落到 -<short>-2；全部占用则退到时间戳候选。
func TestW13g2InstanceNameLadder(t *testing.T) {
	s, _ := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "ld1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_ld13', 'owner', '源L', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	// 占用 baseName 与 baseName-abcde（ld1 命名空间）。
	for _, name := range []string{"源L", "源L-abcde"} {
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES (?, 'ld1', ?, 'gpt', 'oauth')`,
			"acc_ld13_occ_"+strings.ReplaceAll(name, "-", "_"), name); err != nil {
			t.Fatal(err)
		}
	}
	tx := mustTx(t, db)
	name, err := s.uniqueAuthorizedAccountInstanceName(ctx, tx, "源L", "ld1", "rt_abcde", "")
	if err != nil || name != "源L-abcde-2" {
		tx.Rollback()
		t.Fatalf("梯子应落到 -abcde-2: %q %v", name, err)
	}
	// 占用 -abcde-2 后落到 -abcde-3。
	if _, err := tx.Exec(`INSERT INTO accounts (id, system_account_id, name, provider_code, type) VALUES ('acc_ld13_n2', 'ld1', '源L-abcde-2', 'gpt', 'oauth')`); err != nil {
		t.Fatal(err)
	}
	name, err = s.uniqueAuthorizedAccountInstanceName(ctx, tx, "源L", "ld1", "rt_abcde", "")
	if err != nil || name != "源L-abcde-3" {
		tx.Rollback()
		t.Fatalf("梯子应落到 -abcde-3: %q %v", name, err)
	}
	// 空源名 → 授权账户兜底（259-261）。
	name, err = s.uniqueAuthorizedAccountInstanceName(ctx, tx, "  ", "ld1", "rt_abcde", "")
	if err != nil || !strings.HasPrefix(name, "授权账户") {
		tx.Rollback()
		t.Fatalf("空名应兜底授权账户: %q %v", name, err)
	}
	tx.Rollback()
}

// TestW13g2FailpointSyncArms2 覆盖 sync/downstream/delete_resource 剩余臂。
func TestW13g2FailpointSyncArms2(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "sy1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_sy13', 'S', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_sy13",
		GranteeType: "system_account", GranteeID: "sy1",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("revokeGrantUpdate", func(t *testing.T) {
		fp.arm("SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ?")
		defer fp.disarm()
		if _, err := s.Revoke(ctx, created.Item.ID, created.Item.UpdatedAt, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("deleteResourceUpdate", func(t *testing.T) {
		// 先建一条活动授权供删除清理。
		created2, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_sy13",
			GranteeType: "system_account", GranteeID: "sy1",
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		_ = created2
		tx := mustTx(t, db)
		fp.arm("WHERE g.resource_type = ?")
		defer fp.disarm()
		if err := s.RevokeGrantsForResourceDeleted(ctx, tx, "group", "grp_sy13", "owner", "2026-01-01T00:00:00Z"); err == nil {
			tx.Rollback()
			t.Fatalf("应失败")
		}
		tx.Rollback()
	})

	t.Run("outboxInsert", func(t *testing.T) {
		fp.arm("INSERT INTO account_health_jobs_input_outbox")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc_none",
			GranteeType: "system_account", GranteeID: "sy1",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})

	t.Run("runtimeInsertAndSourceInsert", func(t *testing.T) {
		for _, id := range []string{"sy2", "sy3"} {
			if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
				VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
				t.Fatal(err)
			}
		}
		fp.arm("INSERT INTO resource_authorizations")
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_sy13",
			GranteeType: "system_account", GranteeID: "sy2",
		}, "owner"); err == nil {
			fp.disarm()
			t.Fatalf("应失败")
		}
		fp.disarm()
		fp.arm("INSERT INTO resource_authorization_sources")
		defer fp.disarm()
		if _, err := s.Create(ctx, CreateInput{
			ResourceType: "group", ResourceID: "grp_sy13",
			GranteeType: "system_account", GranteeID: "sy3",
		}, "owner"); err == nil {
			t.Fatalf("应失败")
		}
	})
}

// TestW13g2ListProjectionArms 覆盖 list_projection 的 load 错误臂与
// ListPage/ListItemsPage 的 canceled 分支。
func TestW13g2ListProjectionArms(t *testing.T) {
	s, fp := w13g2FailStore(t)
	db := s.db
	ctx := context.Background()
	for _, id := range []string{"owner", "lp1"} {
		if _, err := db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_lp13', 'L', 'owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, CreateInput{
		ResourceType: "group", ResourceID: "grp_lp13",
		GranteeType: "system_account", GranteeID: "lp1",
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	canceled := w13g2CanceledCtx()
	if _, _, _, err := s.ListPage(canceled, Filters{}, 1, 20); err == nil {
		t.Fatalf("canceled ListPage 应失败")
	}
	if _, _, _, err := s.ListItemsPage(canceled, Filters{}, 1, 20, accessInfo{}); err == nil {
		t.Fatalf("canceled ListItemsPage 应失败")
	}
	fp.arm("account_expires_at FROM accounts WHERE id IN")
	defer fp.disarm()
	if _, err := s.loadAccountLookupRows(ctx, []string{"owner"}); err == nil {
		t.Fatalf("应失败")
	}
}
