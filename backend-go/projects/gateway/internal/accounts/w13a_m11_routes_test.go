package accounts

// w13a m11_routes.go 未覆盖臂补齐：M11 路由族错误与校验分支（授权实例 403、
// 余额明细禁止、test-draft 参数校验与失败快照、force-activate/traffic/
// return/authorized-dispatch/group 的空白作用域、坏 JSON、非法参数）。
// 成功主路径由 w2/w10a 既有测试覆盖，这里专攻错误臂。

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// w13aFakeBalanceRefresher 可编程的余额刷新端口。
type w13aFakeBalanceRefresher struct {
	refreshErr    error
	testDraftSnap map[string]any
	testDraftErr  error
}

func (r *w13aFakeBalanceRefresher) RefreshManual(context.Context, BalanceRefreshCandidate) (BalanceManualRefreshOutcome, error) {
	return BalanceManualRefreshOutcome{}, r.refreshErr
}

func (r *w13aFakeBalanceRefresher) TestDraft(context.Context, BalanceDraftProbeInput) (map[string]any, error) {
	if r.testDraftErr != nil {
		return nil, r.testDraftErr
	}
	return r.testDraftSnap, nil
}

func TestW13AM11ReadRoutesForbiddenArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-m11a", adminID, "w13a-m11a", "active")
	env.seedAccount(t, "acc-w13a-m11i", adminID, "w13a-m11i", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-m11a',
		authorization_instance_authorization_id = 'authz-w13a-1' WHERE id = 'acc-w13a-m11i'`)
	// 实例行只有在授权行 active 时才出现在列表面（列表过滤
	// authorizations.status IN ('active','paused','expired')）。
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id,
		resource_owner_system_account_id, grantee_system_account_id, scope, status, created_by,
		created_at, updated_at) VALUES ('authz-w13a-1', 'account', 'acc-w13a-m11a', ?, ?, 'use', 'active', ?, ?, ?)`,
		adminID, adminID, adminID, "2026-09-17T00:00:00.000Z", "2026-09-17T00:00:00.000Z")
	// gemini/google_oauth 账户（重授权上下文只读取该供应商/类型组合）。
	env.seedAccount(t, "acc-w13a-m11g", adminID, "w13a-m11g", "active")
	env.exec(t, `UPDATE accounts SET provider_code = 'gemini', type = 'google_oauth' WHERE id = 'acc-w13a-m11g'`)
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-m11a',
		authorization_instance_authorization_id = 'authz-w13a-2' WHERE id = 'acc-w13a-m11g'`)

	// 授权实例不能克隆来源账户上下文 → 403（123-125）。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-m11g/oauth-reauthorization-context", ""); code != http.StatusForbidden {
		t.Fatalf("实例重授权上下文应 403：%d %v", code, payload)
	}
	// 授权实例不能查看 API Key 运行明细 → 403（154-157）。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-m11i/api-key-runtime", ""); code != http.StatusForbidden {
		t.Fatalf("实例 Key 运行明细应 403：%d %v", code, payload)
	}
	// 实例不能查看余额明细 → 403（181-184 errBalanceDetailsForbidden）。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-m11i/balance/details", ""); code != http.StatusForbidden {
		t.Fatalf("实例余额明细应 403：%d %v", code, payload)
	}
	// 实例余额刷新 → 403（228-232）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-m11i/balance/refresh", ""); code != http.StatusForbidden {
		t.Fatalf("实例余额刷新应 403：%d %v", code, payload)
	}
	// 非 pending_test 账户人工恢复 → 409（449-452）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-m11a/force-activate",
		`{"acknowledgedAccountAvailable":true}`); code != http.StatusConflict {
		t.Fatalf("非待检查人工恢复应 409：%d %v", code, payload)
	}
	// force-activate：空白作用域（423-426）；换账户避免去重守护按同指纹缓存。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-m11d/force-activate?systemAccountId=",
		`{"acknowledgedAccountAvailable":true}`); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 force-activate 应 400：%d %v", code, payload)
	}
}

func TestW13AForceActivateAuthorizedInstanceForbidden(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "w13a-owner", "owner-pass", "user")
	memberID := env.login(t, "w13a-member", "member-pass", "user")
	env.seedAccount(t, "acc-w13a-fsrc", ownerID, "w13a-fsrc", "active")
	env.seedTeamMember(t, "team-w13a-force", ownerID, memberID)
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-w13a-fsrc",
		GranteeType: "team", GranteeID: "team-w13a-force",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-w13a-fsrc'`, memberID)
	env.seedAuthorizationInstance(t, "acc-w13a-finst", memberID, runtimeID, "acc-w13a-fsrc")

	// 成员（grantee）对授权实例人工恢复 → 400 授权账户不能人工恢复（445-448）。
	env.login(t, "w13a-member", "member-pass", "user")
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w13a-finst/force-activate",
		`{"acknowledgedAccountAvailable":true}`); code != http.StatusBadRequest {
		t.Fatalf("实例人工恢复应 400：%d %v", code, payload)
	}
	// 缺少确认字段 → 400（427-430，校验守护）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w13a-finst/force-activate",
		`{"acknowledgedAccountAvailable":false}`); code != http.StatusBadRequest {
		t.Fatalf("缺确认 force-activate 应 400：%d %v", code, payload)
	}
}

func TestW13AM11BalanceTestDraftArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	path := "/__aisys__/api/accounts/balance/test-draft"

	// 坏 JSON（286-288）。
	if code, _ := env.doRaw(t, http.MethodPost, path, "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON test-draft 应 400：%d", code)
	}
	// 未知键。
	if code, payload := env.do(t, http.MethodPost, path, `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("未知键 test-draft 应 400：%d %v", code, payload)
	}
	// 缺 account（298-301）。
	if code, payload := env.do(t, http.MethodPost, path,
		`{"balanceQueryConfig":{"adapter":"builtin"}}`); code != http.StatusBadRequest {
		t.Fatalf("缺 account 应 400：%d %v", code, payload)
	}
	// 缺 balanceQueryConfig（303-306）。
	if code, payload := env.do(t, http.MethodPost, path,
		`{"account":{"groupId":"grp","providerCode":"gpt","type":"api_key"}}`); code != http.StatusBadRequest {
		t.Fatalf("缺 config 应 400：%d %v", code, payload)
	}
	// 非法 config（308-311）。
	if code, payload := env.do(t, http.MethodPost, path,
		`{"account":{"groupId":"grp","providerCode":"gpt","type":"api_key"},"balanceQueryConfig":{"bogus":1}}`); code != http.StatusBadRequest {
		t.Fatalf("非法 config 应 400：%d %v", code, payload)
	}
	// 草稿准备失败：分组不存在 → 400 pipeline。
	if code, payload := env.do(t, http.MethodPost, path,
		`{"account":{"groupId":"grp-w13a-none","providerCode":"gpt","type":"api_key","providerProtocolProfileId":"prof-gpt",
		"credentials":{"api_key":"sk-w13a"}},"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"newapi"}}`); code != http.StatusBadRequest {
		t.Fatalf("草稿分组无效应 400：%d %v", code, payload)
	}
	// 完整草稿 + 装配端口抛错 → 失败快照 200（346-353）。
	draftBody := func(groupID string) string {
		return `{"account":{"groupId":"` + groupID + `","providerCode":"gpt","type":"api_key","providerProtocolProfileId":"prof-gpt",
		"credentials":{"api_key":"sk-w13a","base_url":"https://api.openai.com/v1"}},"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"newapi"}}`
	}
	env.store.SetManualBalanceRefresher(&w13aFakeBalanceRefresher{testDraftErr: errors.New("w13a-探针失败")})
	if code, payload := env.do(t, http.MethodPost, path, draftBody("grp-default-"+adminID)); code != http.StatusOK {
		t.Fatalf("探针错误应回失败快照 200：%d %v", code, payload)
	} else if data, ok := payload["data"].(map[string]any); !ok || data["status"] != "failed" {
		t.Fatalf("失败快照应带 failed 状态：%v", payload)
	}
	// 端口成功 → 快照透传。
	env.store.SetManualBalanceRefresher(&w13aFakeBalanceRefresher{testDraftSnap: map[string]any{"status": "ok"}})
	if code, payload := env.do(t, http.MethodPost, path, draftBody("grp-default-"+adminID)); code != http.StatusOK {
		t.Fatalf("探针成功应 200：%d %v", code, payload)
	} else if data, _ := payload["data"].(map[string]any); data == nil || data["status"] != "ok" {
		t.Fatalf("成功快照应透传：%v", payload)
	}
}

func TestW13AM11WriteRoutesValidationArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-m11b", adminID, "w13a-m11b", "active")
	base := "/__aisys__/api/accounts/acc-w13a-m11b"

	// traffic-migration：空白作用域（506-509）、坏 JSON（516-518）。
	if code, payload := env.do(t, http.MethodPost, base+"/traffic-migration?systemAccountId=", `{}`); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 traffic 应 400：%d %v", code, payload)
	}
	if code, _ := env.doRaw(t, http.MethodPost, base+"/traffic-migration", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON traffic 应 400：%d", code)
	}
	// return-authorization：非授权实例 → 404 缺失（errReturnAuthorizationMissing 臂
	// 629-631）；守护去重按 actor+指纹缓存失败结果，故与空白作用域用例分离账户。
	if code, payload := env.do(t, http.MethodPost, base+"/return-authorization", ""); code != http.StatusNotFound {
		t.Fatalf("非实例归还应 404：%d %v", code, payload)
	}
	// 归还：另一账户走空白作用域（617-620）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-m11c/return-authorization?systemAccountId=", ""); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 return 应 400：%d %v", code, payload)
	}
	// authorized-dispatch：空白作用域（673-676）、坏 JSON（683-685）。
	if code, payload := env.do(t, http.MethodPatch, base+"/authorized-dispatch?systemAccountId=", `{}`); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 dispatch 应 400：%d %v", code, payload)
	}
	if code, _ := env.doRaw(t, http.MethodPatch, base+"/authorized-dispatch", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON dispatch 应 400：%d", code)
	}
	// authorized-dispatch：非法参数（未知键 / 版本缺失）。
	if code, payload := env.do(t, http.MethodPatch, base+"/authorized-dispatch", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("未知键 dispatch 应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPatch, base+"/authorized-dispatch", `{"expectedConfigRevision":0}`); code != http.StatusBadRequest {
		t.Fatalf("版本缺失 dispatch 应 400：%d %v", code, payload)
	}
	// group：空白作用域（775-778）、坏 JSON（785-787）、未知键、空 groupId、非法版本。
	if code, payload := env.do(t, http.MethodPost, base+"/group?systemAccountId=", `{}`); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 group 应 400：%d %v", code, payload)
	}
	if code, _ := env.doRaw(t, http.MethodPost, base+"/group", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON group 应 400：%d", code)
	}
	if code, payload := env.do(t, http.MethodPost, base+"/group", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("未知键 group 应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPost, base+"/group", `{"groupId":"   ","expectedConfigRevision":1}`); code != http.StatusBadRequest {
		t.Fatalf("空 groupId 应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPost, base+"/group", `{"groupId":"grp","expectedConfigRevision":0}`); code != http.StatusBadRequest {
		t.Fatalf("非法版本 group 应 400：%d %v", code, payload)
	}
	// group：目标分组不存在 → Patch ValidationError → 400 pipeline（818-819）。
	if code, payload := env.do(t, http.MethodPost, base+"/group", `{"groupId":"grp-w13a-none","expectedConfigRevision":1}`); code != http.StatusBadRequest {
		t.Fatalf("缺失分组绑定应 400：%d %v", code, payload)
	}
	// group：合法绑定默认分组 → 200 + changedFields 含 groupId。
	if code, payload := env.do(t, http.MethodPost, base+"/group",
		`{"groupId":"grp-default-`+adminID+`","expectedConfigRevision":1}`); code != http.StatusOK {
		t.Fatalf("合法绑定应 200：%d %v", code, payload)
	}
	// model-catalog/refresh：坏 JSON（367-369）与未知键。
	if code, _ := env.doRaw(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON catalog 应 400：%d", code)
	}
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("未知键 catalog 应 400：%d %v", code, payload)
	}
}

func TestW13AAuthorizedDispatchChangeLabel(t *testing.T) {
	for field, want := range map[string]string{
		"status":               "实例状态",
		"schedulable":          "参与调度",
		"priority":             "分组内优先级",
		"superPriorityEnabled": "分组内超级优先",
		"fallbackEnabled":      "分组内降级备用",
		"failureState":         "恢复实例异常状态",
		"bogus":                "bogus",
	} {
		if got := authorizedDispatchChangeLabel(field); got != want {
			t.Fatalf("field %s：got %q want %q", field, got, want)
		}
	}
}
