package accounts

// w13g 路由与调度面补测：authorized-dispatch 直调矩阵、force-activate /
// lock-config / remove 路由错误臂、FindEditBasicDetail。
//
// 不可达登记（w13g）：
// - routes.go:513-516 lockConfig 与 routes.go:570-573 remove 的 auth==nil 臂：
//   admin/self 中间件在最外层拦截匿名请求，处理器内 AuthContextFrom 恒非 nil。
// - m11_routes.go:430-433 forceActivate 的 auth==nil 臂：同上（m11Guarded
//   的 session/admin 中间件在外层）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW13GUpdateAuthorizedDispatchDirectArms(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "disp-member-w13g", "disp-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-disp-src", "d")
	memberAccess := AccessScope{ViewerID: tmMember}

	// 空 grantee / 空 ID / 缺失行 → (nil,nil)。
	if out, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, AccessScope{}); err != nil || out != nil {
		t.Fatalf("空 grantee：%v %v", out, err)
	}
	if out, err := store.UpdateAuthorizedDispatch(context.Background(), " ",
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, memberAccess); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	if out, err := store.UpdateAuthorizedDispatch(context.Background(), "acc-w13g-none",
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, memberAccess); err != nil || out != nil {
		t.Fatalf("缺失行：%v %v", out, err)
	}

	// 失败列齐全的实例：clearFailureState 清空全部失败列。
	env.exec(t, `UPDATE accounts SET cooldown_until = ?, last_error_code = 'e', last_error_message = 'm',
		last_error_trace_id = 'tr', cooldown_retest_failure_count = 2,
		cooldown_retest_observation_started_at = ?, cooldown_retest_generation = 'g',
		cooldown_retest_last_at = ?, cooldown_retest_last_status_code = 429,
		stream_failure_count = 1, stream_failure_window_started_at = ?
		WHERE id = ?`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00Z", instanceID)
	result, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID, AuthorizedDispatchInput{
		ExpectedConfigRevision: 1, ClearFailureState: true,
	}, memberAccess)
	if err != nil || result == nil {
		t.Fatalf("clearFailure 重置：%v %v", result, err)
	}
	var count int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ? AND cooldown_until IS NULL
		AND last_error_code IS NULL AND last_error_message IS NULL AND last_error_trace_id IS NULL
		AND cooldown_retest_failure_count = 0 AND cooldown_retest_observation_started_at IS NULL
		AND cooldown_retest_generation IS NULL AND cooldown_retest_last_at IS NULL
		AND cooldown_retest_last_status_code IS NULL AND stream_failure_count = 0
		AND stream_failure_window_started_at IS NULL`, instanceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("失败列应全部清空")
	}

	// revision 冲突。
	if _, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID, AuthorizedDispatchInput{
		ExpectedConfigRevision: 99, ClearFailureState: true,
	}, memberAccess); err == nil || !strings.Contains(err.Error(), "并发变更") {
		t.Fatalf("revision 冲突：%v", err)
	}

	// 源账户停用 → active 恢复拒绝。
	env.exec(t, `UPDATE accounts SET status = 'disabled' WHERE id = 'acc-w13g-disp-src'`)
	status := "active"
	if _, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID, AuthorizedDispatchInput{
		ExpectedConfigRevision: 2, Status: &status,
	}, memberAccess); err == nil || !strings.Contains(err.Error(), "授权方") {
		t.Fatalf("源停用拒绝：%v", err)
	}

	// closed db → BeginTx 失败。
	t.Run("closed-db", func(t *testing.T) {
		env2 := newTestEnv(t)
		env2.login(t, "root", "root-pass", "super_admin")
		env2.db.Close()
		if _, err := env2.store.UpdateAuthorizedDispatch(context.Background(), "acc-x",
			AuthorizedDispatchInput{ExpectedConfigRevision: 1},
			AccessScope{ViewerID: "someone"}); err == nil {
			t.Fatal("db 关闭应报错")
		}
	})
}

func TestW13GUpdateAuthorizedDispatchRowArms(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "disp2-member-w13g", "disp2-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-disp2-src", "e")
	memberAccess := AccessScope{ViewerID: tmMember}

	// accounts 表缺失 → 行读取错误。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, memberAccess); err == nil {
		t.Fatal("accounts 缺失应报错")
	}
}

func TestW13GUpdateAuthorizedDispatchBindingArms(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "disp3-member-w13g", "disp3-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-disp3-src", "g")
	memberAccess := AccessScope{ViewerID: tmMember}

	// group_accounts 表缺失 → binding 读取错误。
	env.exec(t, `DROP TABLE group_accounts`)
	if _, err := store.UpdateAuthorizedDispatch(context.Background(), instanceID,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, memberAccess); err == nil {
		t.Fatal("group_accounts 缺失应报错")
	}
}

func TestW13GUpdateAuthorizedDispatchStateArms(t *testing.T) {
	env, _, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "disp4-member-w13g", "disp4-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)

	// 无绑定的实例 → binding nil → (nil,nil)。
	unbound, _, _, _ := w13gSeedAuthInstance(t, env, ownerID, tmMember, "ub", "active", "rate_limited", false)
	if out, err := store.UpdateAuthorizedDispatch(context.Background(), unbound,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, AccessScope{ViewerID: tmMember}); err != nil || out != nil {
		t.Fatalf("无绑定实例：%v %v", out, err)
	}

	// revoked 授权 → (nil,nil)。
	revoked, _, authID, _ := w13gSeedAuthInstance(t, env, ownerID, tmMember, "rv", "active", "rate_limited", true)
	env.exec(t, `UPDATE resource_authorizations SET status = 'revoked' WHERE id = ?`, authID)
	if out, err := store.UpdateAuthorizedDispatch(context.Background(), revoked,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, AccessScope{ViewerID: tmMember}); err != nil || out != nil {
		t.Fatalf("revoked 实例：%v %v", out, err)
	}

	// 无变化输入（仅 revision）→ 返回空变更结果。
	noChange, _, _, _ := w13gSeedAuthInstance(t, env, ownerID, tmMember, "nc", "active", "rate_limited", true)
	out, err := store.UpdateAuthorizedDispatch(context.Background(), noChange,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1}, AccessScope{ViewerID: tmMember})
	if err != nil || out == nil || len(out.Changes) != 0 {
		t.Fatalf("无变化输入：%v %v", out, err)
	}

	// 恢复 active 分支：effects 接线时 RuntimeRestoreRequired 触发运行时清理。
	effects := &fakeRuntimeEffects{}
	store.SetRuntimeResetEffects(effects)
	activate := "active"
	out, err = store.UpdateAuthorizedDispatch(context.Background(), noChange,
		AuthorizedDispatchInput{ExpectedConfigRevision: 1, Status: &activate}, AccessScope{ViewerID: tmMember})
	if err != nil || out == nil {
		t.Fatalf("恢复分支：%v %v", out, err)
	}
	if !out.RuntimeRestoreRequired {
		t.Fatalf("恢复应要求运行时恢复：%+v", out)
	}
}

func TestW13GParseAuthorizedDispatchBodyMatrix(t *testing.T) {
	cases := []map[string]any{
		{"bogus": 1},
		{},
		{"expectedConfigRevision": 0},
		{"expectedConfigRevision": 1, "status": 1},
		{"expectedConfigRevision": 1, "status": "bogus"},
		{"expectedConfigRevision": 1, "priority": "x"},
		{"expectedConfigRevision": 1, "priority": -1},
		{"expectedConfigRevision": 1, "superPriorityEnabled": "yes"},
		{"expectedConfigRevision": 1, "fallbackEnabled": 1},
		{"expectedConfigRevision": 1, "clearFailureState": "yes"},
		{"expectedConfigRevision": 1, "clearFailureState": false},
		{"expectedConfigRevision": 1},
	}
	for _, body := range cases {
		if _, message := parseAuthorizedDispatchBody(body); message == "" {
			t.Fatalf("应拒绝：%v", body)
		}
	}
	parsed, message := parseAuthorizedDispatchBody(map[string]any{
		"expectedConfigRevision": float64(2), "status": "disabled", "priority": float64(3),
		"superPriorityEnabled": true, "fallbackEnabled": false, "clearFailureState": true,
	})
	if message != "" || parsed.ExpectedConfigRevision != 2 || parsed.Status == nil || *parsed.Status != "disabled" ||
		parsed.Priority == nil || *parsed.Priority != 3 || parsed.SuperPriorityEnabled == nil ||
		!*parsed.SuperPriorityEnabled || parsed.FallbackEnabled == nil || *parsed.FallbackEnabled ||
		!parsed.ClearFailureState {
		t.Fatalf("合法负载：%+v %q", parsed, message)
	}
}

func TestW13GFindEditBasicDetailArms(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "edit-member-w13g", "edit-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-eb-src", "h")
	adminAccess := AccessScope{ViewerID: ownerID, IsAdmin: true}

	// 授权实例 → 403（凭据面拒绝）。
	if _, err := store.FindEditBasicDetail(context.Background(), instanceID, adminAccess); err == nil ||
		!strings.Contains(err.Error(), "凭据") {
		t.Fatalf("实例编辑应拒绝：%v", err)
	}
	// accounts 表缺失。
	env.exec(t, `DROP TABLE accounts`)
	if _, err := store.FindEditBasicDetail(context.Background(), instanceID, adminAccess); err == nil {
		t.Fatal("accounts 缺失应报错")
	}
}

func TestW13GFindEditBasicDetailOwnerArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-eb1", adminID, "w13g-eb1", "active")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 坏凭据信封。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'not-sealed' WHERE id = 'acc-w13g-eb1'`)
	if _, err := env.store.FindEditBasicDetail(context.Background(), "acc-w13g-eb1", admin); err == nil {
		t.Fatal("坏凭据应报错")
	}
	// supported models 表缺失。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = '' WHERE id = 'acc-w13g-eb1'`)
	env.exec(t, `DROP TABLE account_supported_models`)
	if _, err := env.store.FindEditBasicDetail(context.Background(), "acc-w13g-eb1", admin); err == nil {
		t.Fatal("supported models 缺失应报错")
	}
}

func TestW13GForceActivateRouteArms(t *testing.T) {
	// 变更守卫按 {accountId, acknowledged} 指纹做失败 TTL 去重，子场景各自
	// 使用独立账户。
	scenarios := []struct {
		name    string
		account string
		run     func(t *testing.T, env *testEnv, base string)
	}{
		{"blank-scope", "acc-w13g-fa-a", func(t *testing.T, env *testEnv, base string) {
			if code, _ := env.do(t, http.MethodPost, base+"?systemAccountId=%20", `{}`); code != http.StatusBadRequest {
				t.Fatalf("空白作用域应 400：%d", code)
			}
		}},
		{"missing-ack", "acc-w13g-fa-b", func(t *testing.T, env *testEnv, base string) {
			if code, payload := env.do(t, http.MethodPost, base, `{}`); code != http.StatusBadRequest ||
				payload["message"] != "请先确认账户当前可用并接受人工恢复风险" {
				t.Fatalf("缺确认应 400：%d %v", code, payload)
			}
		}},
		{"activate", "acc-w13g-fa-c", func(t *testing.T, env *testEnv, base string) {
			code, payload := env.do(t, http.MethodPost, base, `{"acknowledgedAccountAvailable":true}`)
			if code != http.StatusOK {
				t.Fatalf("恢复应 200：%d %v", code, payload)
			}
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := newTestEnv(t)
			adminID := env.login(t, "root", "root-pass", "super_admin")
			env.seedProviderAndDefaultGroup(t, adminID)
			env.seedAccount(t, scenario.account, adminID, "w13g-fa", "pending_test")
			scenario.run(t, env, "/__aisys__/api/accounts/"+scenario.account+"/force-activate")
		})
	}
	t.Run("missing-account", func(t *testing.T) {
		env := newTestEnv(t)
		env.login(t, "root", "root-pass", "super_admin")
		if code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13g-none/force-activate",
			`{"acknowledgedAccountAvailable":true}`); code != http.StatusNotFound {
			t.Fatalf("缺失账户应 404：%d", code)
		}
	})
}

func TestW13GLockConfigRouteArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-lc", adminID, "w13g-lc", "active")
	base := "/__aisys__/api/accounts/acc-w13g-lc/lock-config"

	// 坏 JSON → 400。
	if code, _ := env.doRaw(t, http.MethodPost, base, "{not-json"); code != http.StatusBadRequest {
		t.Fatal("坏 JSON 应 400")
	}
	// 缺 expectedConfigRevision → 400。
	if code, _ := env.do(t, http.MethodPost, base, `{"lockDeathTimeoutSeconds":600}`); code != http.StatusBadRequest {
		t.Fatal("缺版本应 400")
	}
	// 缺失账户 → 404。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13g-none/lock-config",
		`{"expectedConfigRevision":1,"lockDeathTimeoutSeconds":600}`); code != http.StatusNotFound {
		t.Fatalf("缺失账户应 404：%d %v", code, payload)
	}
	// 合法锁死配置。
	code, payload := env.do(t, http.MethodPost, base,
		`{"expectedConfigRevision":1,"lockDeathTimeoutSeconds":600,"lockRetryIntervalSeconds":10}`)
	if code != http.StatusOK {
		t.Fatalf("锁死配置应 200：%d %v", code, payload)
	}
}

func TestW13GRemoveRouteAndEditBasicDetail(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-rm", adminID, "w13g-rm", "active")
	admin := AccessScope{ViewerID: adminID, IsAdmin: true}

	// FindEditBasicDetail：空 ID / 缺失行。
	if out, err := env.store.FindEditBasicDetail(context.Background(), "  ", admin); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	if out, err := env.store.FindEditBasicDetail(context.Background(), "acc-w13g-none", admin); err != nil || out != nil {
		t.Fatalf("缺失行：%v %v", out, err)
	}
	basic, err := env.store.FindEditBasicDetail(context.Background(), "acc-w13g-rm", admin)
	if err != nil || basic == nil || basic.ID != "acc-w13g-rm" {
		t.Fatalf("编辑详情：%v %v", basic, err)
	}

	// DELETE 路由：先缺失 404 再成功。
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/acc-w13g-none", ""); code != http.StatusNotFound {
		t.Fatal("删除缺失账户应 404")
	}
	code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/acc-w13g-rm", "")
	if code != http.StatusNoContent {
		t.Fatalf("删除应 204：%d %v", code, payload)
	}
	var deletedAt any
	if err := env.db.QueryRow(`SELECT deleted_at FROM accounts WHERE id = 'acc-w13g-rm'`).Scan(&deletedAt); err != nil {
		t.Fatal(err)
	}
	if deletedAt == nil {
		t.Fatal("应软删除")
	}
	_ = time.Now
}
