// 委托面（delegated）merge 模式注册回归：strategyModes 突变白名单与
// routeStrategyModeQuery 列表过滤白名单放行 merge（设计 B5），store 校验
// （≥2 启用绑定）穿透委托面。
package delegated

import (
	"net/http"
	"testing"
)

// TestWMMergeDelegatedStrategyEnum：委托面创建 merge 策略与 mode 过滤。
func TestWMMergeDelegatedStrategyEnum(t *testing.T) {
	f := newFixture(t, "juhe:route_strategies.read", "juhe:route_strategies.write")
	f.env.seedGroup("grp-m1", f.accountID, "m1", "openai", "personal", true)
	f.env.seedGroup("grp-m2", f.accountID, "m2", "openai", "personal", true)

	// merge 单绑定 → 400（store 合并路由校验穿透委托面）。
	r := f.env.do(http.MethodPost, Prefix+"/route-strategies",
		`{"name":"m-single","mode":"merge","groupBindings":[{"groupId":"grp-m1"}]}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("merge 单绑定 = %d %v", r.status, r.body["message"])
	}

	// merge 双绑定 → 201。
	r = f.env.do(http.MethodPost, Prefix+"/route-strategies",
		`{"name":"m-pair","mode":"merge","groupBindings":[{"groupId":"grp-m1"},{"groupId":"grp-m2","priority":2}]}`, f.token)
	if r.status != http.StatusCreated {
		t.Fatalf("merge create = %d (%s)", r.status, r.raw)
	}
	created := r.body["data"].(map[string]any)
	if created["mode"] != "merge" {
		t.Fatalf("merge 模式投影: %v", created)
	}
	strategyID, _ := created["id"].(string)

	// 列表 mode=merge 过滤命中（routeStrategyModeQuery 白名单）。
	r = f.env.do(http.MethodGet, Prefix+"/route-strategies?mode=merge", "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("list mode=merge = %d (%s)", r.status, r.raw)
	}
	items := r.body["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != strategyID {
		t.Fatalf("mode=merge 过滤: %v", items)
	}

	// PATCH 裸切 merge：单绑定 normal 策略 → 400。
	f.env.seedGroup("grp-n1", f.accountID, "n1", "openai", "personal", true)
	r = f.env.do(http.MethodPost, Prefix+"/route-strategies",
		`{"name":"n-single","groupBindings":[{"groupId":"grp-n1"}]}`, f.token)
	if r.status != http.StatusCreated {
		t.Fatalf("normal create = %d (%s)", r.status, r.raw)
	}
	normalID := r.body["data"].(map[string]any)["id"].(string)
	r = f.env.do(http.MethodGet, Prefix+"/route-strategies/"+normalID, "", f.token)
	if r.status != http.StatusOK {
		t.Fatalf("get normal strategy = %d (%s)", r.status, r.raw)
	}
	updatedAt := r.body["data"].(map[string]any)["updatedAt"].(string)
	r = f.env.do(http.MethodPatch, Prefix+"/route-strategies/"+normalID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"merge"}`, f.token)
	if r.status != http.StatusBadRequest || r.body["message"] != "合并路由至少需要两个启用分组" {
		t.Fatalf("patch 裸切 merge = %d %v", r.status, r.body["message"])
	}
}
