// 公开面（/__aipublic__）merge 退役回归：strategyModeOptions /
// strategyMutationModes / 写入口 payload 一律不再接受 merge；历史 merge 行
// 仅由 store 直建/迁移基线承载，公开写入口统一 400 unsupported_route_mode。
package aipublic

import (
	"net/http"
	"testing"
)

// TestWMMergePublicStrategyLifecycle：真实路径 merge 新建 / 切换全部拒绝。
func TestWMMergePublicStrategyLifecycle(t *testing.T) {
	env := newWCLifeEnv(t)
	groupA := wcAddGroup(t, env, "pusher", "合并组A")
	groupB := wcAddGroup(t, env, "pusher", "合并组B")

	// merge 新建（单绑定与双绑定）→ 400 unsupported_route_mode，不落库。
	for _, payload := range []string{
		`{"targetUsername":"pusher","name":"合并单绑","mode":"merge","groupBindings":[{"groupId":"` + groupA + `"}]}`,
		`{"targetUsername":"pusher","name":"合并策略","mode":"merge","groupBindings":[{"groupId":"` + groupA + `"},{"groupId":"` + groupB + `","priority":2}]}`,
	} {
		status, body, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add", payload, wcLifeToken)
		if status != http.StatusBadRequest || body["code"] != "unsupported_route_mode" || body["message"] != "不支持的路由模式" {
			t.Fatalf("merge add must be unsupported: %d %v", status, body)
		}
	}

	// list mode=merge 按未知模式处理（公开面 enum 校验 → 400）。
	status, body, _ := env.doAuth(http.MethodGet, Prefix+"/route-strategy/list?targetUsername=pusher&mode=merge", "", wcLifeToken)
	if status != http.StatusBadRequest {
		t.Fatalf("list mode=merge must be rejected as unknown: %d %v", status, body)
	}

	// update 切到 merge：normal 单绑定策略 → 400 unsupported_route_mode。
	status, body, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"pusher","name":"普通策略","groupBindings":[{"groupId":"`+groupA+`"}]}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("normal add: %d %v", status, body)
	}
	normalID := body["data"].(map[string]any)["routeStrategy"].(map[string]any)["id"].(string)
	status, body, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"targetUsername":"pusher","routeStrategyId":"`+normalID+`","mode":"merge"}`, wcLifeToken)
	if status != http.StatusBadRequest || body["code"] != "unsupported_route_mode" || body["message"] != "不支持的路由模式" {
		t.Fatalf("update 裸切 merge: %d %v", status, body)
	}
}

// TestWMMergeMockProjection：test token 会话下 merge 写入口同样拒绝，
// mock 路径不再投影 merge。
func TestWMMergeMockProjection(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"u1","name":"合并策略","mode":"merge","groupBindings":[{"groupId":"g1"},{"groupId":"g2"}]}`, token)
	if status != http.StatusBadRequest || payload["code"] != "unsupported_route_mode" {
		t.Fatalf("mock merge add must be unsupported: %d %v", status, payload)
	}
}
