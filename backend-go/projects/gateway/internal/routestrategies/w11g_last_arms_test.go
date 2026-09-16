package routestrategies

// w11g 覆盖补充（第九批）：my 面 speed-first 路由、操作日志绑定摘要与
// 前缀上界边界。

import (
	"context"
	"net/http"
	"testing"
)

func TestW11GMySurfaceSpeedFirstAndMutations(t *testing.T) {
	env := newTestEnvFull(t, &wlRuntimeFacade{available: true})
	admin := env.login(t, "w11g-my", "root-pass", "super_admin")
	group := env.createGroup(t, admin, "w11g-my-group", true)
	viewer := AccessScope{ViewerID: admin}
	store := w9eStore(t, env)
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("w11g 我的"), Mode: ptrString(ModeNormal), HasBindings: true,
		Bindings: []BindingInput{{GroupID: group, Priority: intPtr(1), Status: "active"}},
	}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// my 面 speed-first：空白 scope 400、正常 200、ghost 404。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/"+created.ID+"/speed-first-runtime?systemAccountId=", "")
	if code != http.StatusBadRequest {
		t.Fatalf("my runtime blank scope = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/"+created.ID+"/speed-first-runtime", "")
	if code != http.StatusOK {
		t.Fatalf("my runtime = %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-route-strategies/rs-w11g-ghost/speed-first-runtime", "")
	if code != http.StatusNotFound {
		t.Fatalf("my runtime 404 = %d", code)
	}
	// my 面变更与删除路由。
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/my-route-strategies/"+created.ID,
		`{"name":"w11g 我的改名","expectedUpdatedAt":"`+strategyUpdatedAt(env, t, created.ID)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("my patch = %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-route-strategies/"+created.ID, "")
	if code != http.StatusNoContent {
		t.Fatalf("my delete = %d", code)
	}
	// 操作日志绑定摘要：全字段 provided 与默认。
	summary := summarizeBindings([]BindingInput{{
		GroupID: "g1", Priority: intPtr(3), Weight: intPtr(7), Status: "disabled",
		priorityProvided: true, weightProvided: true, statusProvided: true,
	}})
	if summary == "" {
		t.Fatal("summary must render")
	}
	if got := summarizeBindings(nil); got != "[]" {
		t.Fatalf("empty summary = %q", got)
	}
	// 前缀上界：空白输入返回下一码位。
	if got := textPrefixUpperBound(" "); got != "!" {
		t.Fatalf("space upper bound = %q", got)
	}
}
