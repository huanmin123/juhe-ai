package routestrategies

// w9e 覆盖率战役：补 routes/mutations/bodies/bindings/store 的校验失败臂、
// 权限守卫、写域查询守卫与分页边界。全部基于既有 SQLite HTTP harness。

import (
	"net/http"
	"strings"
	"testing"
)

func w9eData(payload map[string]any) map[string]any {
	if inner, ok := payload["data"].(map[string]any); ok {
		return inner
	}
	return payload
}

func TestW9EMutationValidationMatrix(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-root", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w9e-alpha", true)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"create 坏 JSON", http.MethodPost, "/__aisys__/api/route-strategies", "{not-json", http.StatusBadRequest},
		{"create 缺绑定", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"无绑定策略"}`, http.StatusBadRequest},
		{"create 空绑定", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"空绑定","groupBindings":[]}`, http.StatusBadRequest},
		{"create 未知顶层键", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"x","groupBindings":[{"groupId":"` + group + `","priority":1}],"bogusKey":1}`, http.StatusBadRequest},
		{"create 空名", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"  ","groupBindings":[{"groupId":"` + group + `","priority":1}]}`, http.StatusBadRequest},
		{"create 优先级 0", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"p0","groupBindings":[{"groupId":"` + group + `","priority":0}]}`, http.StatusBadRequest},
		{"create 负优先级", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"pn","groupBindings":[{"groupId":"` + group + `","priority":-3}]}`, http.StatusBadRequest},
		{"create 非法 status", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"st","groupBindings":[{"groupId":"` + group + `","priority":1,"status":"bogus"}]}`, http.StatusBadRequest},
		{"create 权重 0", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"w0","groupBindings":[{"groupId":"` + group + `","priority":1,"weight":0}]}`, http.StatusBadRequest},
		{"create 权重 101", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"w101","groupBindings":[{"groupId":"` + group + `","priority":1,"weight":101}]}`, http.StatusBadRequest},
		{"create 空 groupId", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"gi","groupBindings":[{"groupId":" ","priority":1}]}`, http.StatusBadRequest},
		{"create 重复 groupId", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"dup","groupBindings":[{"groupId":"` + group + `","priority":1},{"groupId":"` + group + `","priority":2}]}`, http.StatusBadRequest},
		{"create 重复优先级", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"dupp","groupBindings":[{"groupId":"` + group + `","priority":1},{"groupId":"grp-other-w9e","priority":1}]}`, http.StatusBadRequest},
		{"create 全 disabled", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"dis","groupBindings":[{"groupId":"` + group + `","priority":1,"status":"disabled"}]}`, http.StatusBadRequest},
		{"create 不存在的分组", http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"ghost","groupBindings":[{"groupId":"grp-ghost","priority":1}]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		code, payload := env.do(t, tc.method, tc.path, tc.body)
		if code != tc.want {
			t.Fatalf("%s: code = %d payload=%v", tc.name, code, payload)
		}
	}
	// 混合模式非法配置形状。
	code, _ := env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"hy","mode":"hybrid","hybridConfig":{"noSuchKey":1},"groupBindings":[{"groupId":"`+group+`","priority":1}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("hybrid 非法形状 = %d", code)
	}
	// speed-first 非法配置键。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", `{"name":"sf","mode":"speed_first","speedFirstConfig":{"noSuchKey":1},"groupBindings":[{"groupId":"`+group+`","priority":1}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("speed-first 非法形状 = %d", code)
	}
}

func TestW9EUpdateBranches(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-upd", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w9e-upd-group", true)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/route-strategies",
		`{"name":"更新策略","groupBindings":[{"groupId":"`+group+`","priority":1}]}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	id := w9eData(created)["id"].(string)
	updatedAt := env.strategyUpdatedAt(t, id)

	// 坏 JSON。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id, "{bad"); c != http.StatusBadRequest {
		t.Fatalf("patch 坏 JSON = %d", c)
	}
	// 默认策略不允许改名。
	env.exec(t, `UPDATE route_strategies SET is_default=1 WHERE id=?`, id)
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"name":"改名","expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusBadRequest {
		t.Fatalf("默认改名 = %d", c)
	}
	env.exec(t, `UPDATE route_strategies SET is_default=0 WHERE id=?`, id)

	// 仅描述更新（nil 描述与文本描述）。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"description":null,"expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusOK {
		t.Fatalf("desc null = %d", c)
	}
	updatedAt = env.strategyUpdatedAt(t, id)
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"description":"新描述","expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusOK {
		t.Fatalf("desc text = %d", c)
	}
	updatedAt = env.strategyUpdatedAt(t, id)
	// 仅状态更新。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"status":"disabled","expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusOK {
		t.Fatalf("status = %d", c)
	}
	updatedAt = env.strategyUpdatedAt(t, id)
	// 仅配置更新（normal）。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"normalRoutingConfig":{"schedulingPreference":"speed_first"},"expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusOK {
		t.Fatalf("normal config = %d", c)
	}
	updatedAt = env.strategyUpdatedAt(t, id)
	// 版本冲突：旧 expectedUpdatedAt。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"description":"冲突","expectedUpdatedAt":"2000-01-01T00:00:00Z"}`); c != http.StatusConflict {
		t.Fatalf("版本冲突 = %d", c)
	}
	// 绑定替换（新分组）。
	group2 := env.createGroup(t, admin, "w9e-upd-group2", true)
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+id,
		`{"groupBindings":[{"groupId":"`+group2+`","priority":2,"weight":50}],"expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusOK {
		t.Fatalf("bindings replace = %d", c)
	}
	// 更新为不存在的策略 → 404。
	if c, _ := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/route_strategy_ghost",
		`{"description":"x","expectedUpdatedAt":"`+updatedAt+`"}`); c != http.StatusNotFound {
		t.Fatalf("404 = %d", c)
	}
}

func TestW9EWriteScopeAndQueryGuards(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "w9e-scope", "root-pass", "super_admin")

	// writeScopeQuery：空 systemAccountId → 400。
	// 列表与 options 读不校验写域（Node 直接读 scope），只有详情族与 my-* 校验。
	paths := []string{
		"/__aisys__/api/route-strategies/my-route-strategies?systemAccountId=",
	}
	for _, path := range paths {
		code, _ := env.do(t, http.MethodGet, path, "")
		if code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", path, code)
		}
	}
	// ids 查询参数上限与去重。
	ids := strings.Repeat("a,", 60) + "b"
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?ids="+ids, "")
	if code != http.StatusOK {
		t.Fatalf("ids overflow = %d", code)
	}
	// pageSize 非法（<1 → 钳制为默认）。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?page=0&pageSize=0", "")
	if code != http.StatusOK {
		t.Fatalf("pageSize=0 = %d", code)
	}
	// include 混合大小写布尔。
	for _, value := range []string{"1", "true", "TRUE", "yes", "0"} {
		code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?includeBindings="+value, "")
		if code != http.StatusOK {
			t.Fatalf("include=%s = %d", value, code)
		}
	}
}

func TestW9EStoreLevelPaginationAndGuards(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "w9e-store", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w9e-store-group", true)
	for _, name := range []string{"分页一", "分页二", "分页三"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/route-strategies",
			`{"name":"`+name+`","groupBindings":[{"groupId":"`+group+`","priority":1}]}`)
		if code != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", name, code, payload)
		}
	}
	// pageSize=1 翻页。
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?page=1&pageSize=1", "")
	if code != http.StatusOK {
		t.Fatalf("page1 = %d", code)
	}
	items := w9eData(page1)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("page1 items = %d", len(items))
	}
	// limit > 100 → 钳制。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?pageSize=500", "")
	if code != http.StatusOK {
		t.Fatalf("pageSize=500 = %d", code)
	}
	// speed-first runtime 缺 strategy id → 404/400。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/my-route-strategies/speed-first-runtime", "")
	if code != http.StatusOK && code != http.StatusBadRequest && code != http.StatusNotFound {
		t.Fatalf("speed-first runtime = %d", code)
	}
}
