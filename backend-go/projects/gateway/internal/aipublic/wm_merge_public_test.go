// 公开面（/__aipublic__）merge 模式注册回归：strategyModeOptions /
// strategyMutationModes 白名单放行 merge（设计 B5），真实路径创建 merge 策略
// 经 routestrategies store 校验（≥2 启用绑定）渲染 cost_first 默认；mock 投影
// 经 ModeSupportsSchedulingPreference 自动为 merge 渲染 cost_first 默认。
package aipublic

import (
	"net/http"
	"testing"
)

// TestWMMergePublicStrategyLifecycle：真实路径 merge 创建 / 校验 / 列表过滤。
func TestWMMergePublicStrategyLifecycle(t *testing.T) {
	env := newWCLifeEnv(t)
	groupA := wcAddGroup(t, env, "pusher", "合并组A")
	groupB := wcAddGroup(t, env, "pusher", "合并组B")

	// merge 单绑定 → 400（store 的合并路由校验穿透公开面）。
	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"pusher","name":"合并单绑","mode":"merge","groupBindings":[{"groupId":"`+groupA+`"}]}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("merge 单绑定: %d %v", status, payload)
	}

	// merge 双绑定 → 201 + mode 投影 + cost_first 默认渲染。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"pusher","name":"合并策略","mode":"merge","groupBindings":[{"groupId":"`+groupA+`"},{"groupId":"`+groupB+`","priority":2}]}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("merge add: %d %v", status, payload)
	}
	strategy := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)
	if strategy["mode"] != "merge" {
		t.Fatalf("merge 模式投影: %v", strategy)
	}
	config, ok := strategy["normalRoutingConfig"].(map[string]any)
	if !ok || config["schedulingPreference"] != "cost_first" {
		t.Fatalf("merge 必须渲染 cost_first 默认: %v", strategy["normalRoutingConfig"])
	}
	strategyID := strategy["id"].(string)
	bindings := strategy["groupBindings"].([]any)
	if len(bindings) != 2 {
		t.Fatalf("merge 绑定投影: %v", bindings)
	}

	// list mode=merge 过滤命中。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/route-strategy/list?targetUsername=pusher&mode=merge", "", wcLifeToken)
	if status != http.StatusOK {
		t.Fatalf("list mode=merge: %d %v", status, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != strategyID {
		t.Fatalf("mode=merge 过滤: %v", items)
	}

	// update 切到 merge：normal 单绑定策略 → 400。
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"pusher","name":"普通策略","groupBindings":[{"groupId":"`+groupA+`"}]}`, wcLifeToken)
	if status != http.StatusCreated {
		t.Fatalf("normal add: %d %v", status, payload)
	}
	normalID := payload["data"].(map[string]any)["routeStrategy"].(map[string]any)["id"].(string)
	status, payload, _ = env.doAuth(http.MethodPost, Prefix+"/route-strategy/update",
		`{"targetUsername":"pusher","routeStrategyId":"`+normalID+`","mode":"merge"}`, wcLifeToken)
	if status != http.StatusBadRequest || payload["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("update 裸切 merge: %d %v", status, payload)
	}
}

// TestWMMergeMockProjection：test token 会话下 mock add 为 merge 渲染
// cost_first 默认（mockStrategySummary 谓词分支）；mock list 的 mode=merge
// 入参投影回显。
func TestWMMergeMockProjection(t *testing.T) {
	env := newWCMockEnv(t)
	token := "juis_token_wcwcwcwcwcwcwcwc"

	status, payload, _ := env.doAuth(http.MethodPost, Prefix+"/route-strategy/add",
		`{"targetUsername":"u1","name":"合并策略","mode":"merge","groupBindings":[{"groupId":"g1"},{"groupId":"g2"}]}`, token)
	data := mustMockEnvelope(t, status, payload, http.StatusOK)
	strategy := data["routeStrategy"].(map[string]any)
	if strategy["mode"] != "merge" {
		t.Fatalf("mock merge 模式投影: %v", strategy)
	}
	config, ok := strategy["normalRoutingConfig"].(map[string]any)
	if !ok || config["schedulingPreference"] != "cost_first" {
		t.Fatalf("mock merge 必须渲染 cost_first 默认: %v", strategy["normalRoutingConfig"])
	}

	// mock list：mode=merge 入参投影回显（不再回落 normal）。
	status, payload, _ = env.doAuth(http.MethodGet, Prefix+"/route-strategy/list?targetUsername=u1&mode=merge", "", token)
	data = mustMockEnvelope(t, status, payload, http.StatusOK)
	item := data["items"].([]any)[0].(map[string]any)
	if item["mode"] != "merge" {
		t.Fatalf("mock list mode=merge 投影: %v", item)
	}
}
