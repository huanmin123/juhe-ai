package chat

// 新建会话绑定下拉（GET /conversation-bind-options）的路由级覆盖：登录态
// 200 信封精确形状、查询端口未接线的 500 臂、未登录 401 臂，以及数据范围
// （ChatBindScope）两臂——普通用户传 viewer scope 且拿到用户集合，
// admin/super_admin 拿到全量集合。Mock 风格与 chat_bind_modes_test.go 一致。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// mockGroupOptionsLookup 返回确定性的启用分组最小摘要：按 IsAdmin 臂返回不
// 同集合（admin=管理集合，普通用户=用户集合），seen 非 nil 时记录收到的
// scope。
type mockGroupOptionsLookup struct {
	seen *bindScopeRecorder
}

func (m mockGroupOptionsLookup) ListChatGroupOptions(_ context.Context, scope ChatBindScope) ([]ChatBindOption, error) {
	if m.seen != nil {
		m.seen.record(scope, "")
	}
	if scope.IsAdmin {
		return []ChatBindOption{{ID: "g-admin", Name: "管理分组"}}, nil
	}
	return []ChatBindOption{{ID: "g1", Name: "分组一"}, {ID: "g2", Name: "分组二"}}, nil
}

// mockAccountOptionsLookup 返回确定性的可绑定账户最小摘要：按 IsAdmin 臂返
// 回不同集合，seen 非 nil 时记录收到的 scope。
type mockAccountOptionsLookup struct {
	seen *bindScopeRecorder
}

func (m mockAccountOptionsLookup) ListChatAccountOptions(_ context.Context, scope ChatBindScope) ([]ChatBindOption, error) {
	if m.seen != nil {
		m.seen.record(scope, "")
	}
	if scope.IsAdmin {
		return []ChatBindOption{{ID: "a-admin", Name: "管理账户"}}, nil
	}
	return []ChatBindOption{{ID: "a1", Name: "账户一"}}, nil
}

func TestConversationBindOptionsRoutes(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupOptionsLookup = mockGroupOptionsLookup{}
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	response := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("bind options = %d %s", response.status, response.rawString())
	}
	// 最小投影信封的精确形状：groups/accounts 恒为数组，元素只含 id/name。
	want := `{"data":{"groups":[{"id":"g1","name":"分组一"},{"id":"g2","name":"分组二"}],"accounts":[{"id":"a1","name":"账户一"}]}}`
	if response.rawString() != want {
		t.Fatalf("bind options body = %s, want %s", response.rawString(), want)
	}
}

// TestConversationBindOptionsScopeArms 覆盖下拉端点的数据范围两臂：普通用户
// 把 viewer scope（IsAdmin=false）传给查询端口并拿到用户集合；admin /
// super_admin 把 IsAdmin=true scope 传给查询端口并拿到全量集合。
func TestConversationBindOptionsScopeArms(t *testing.T) {
	t.Run("普通用户传 viewer scope 且拿到用户集合", func(t *testing.T) {
		env := newGenerationEnv(t)
		groupSeen := &bindScopeRecorder{}
		accountSeen := &bindScopeRecorder{}
		env.deps.GroupOptionsLookup = mockGroupOptionsLookup{seen: groupSeen}
		env.deps.AccountOptionsLookup = mockAccountOptionsLookup{seen: accountSeen}
		response := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", routeTestOwner, "")
		if response.status != http.StatusOK {
			t.Fatalf("user bind options = %d %s", response.status, response.rawString())
		}
		userScope := ChatBindScope{ViewerID: routeTestOwner, IsAdmin: false}
		for name, seen := range map[string]*bindScopeRecorder{"groups": groupSeen, "accounts": accountSeen} {
			scopes, _ := seen.snapshot()
			if len(scopes) != 1 || scopes[0] != userScope {
				t.Fatalf("%s 查询收到的 scope = %v, want [%v]", name, scopes, userScope)
			}
		}
		want := `{"data":{"groups":[{"id":"g1","name":"分组一"},{"id":"g2","name":"分组二"}],"accounts":[{"id":"a1","name":"账户一"}]}}`
		if response.rawString() != want {
			t.Fatalf("user bind options body = %s, want %s", response.rawString(), want)
		}
	})

	t.Run("admin 与 super_admin 拿到全量集合", func(t *testing.T) {
		for _, role := range []string{"admin", "super_admin"} {
			env := newBindOptionsEnvWithRole(t, role)
			env.deps.GroupOptionsLookup = mockGroupOptionsLookup{}
			env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
			response := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", routeTestOwner, "")
			if response.status != http.StatusOK {
				t.Fatalf("%s bind options = %d %s", role, response.status, response.rawString())
			}
			want := `{"data":{"groups":[{"id":"g-admin","name":"管理分组"}],"accounts":[{"id":"a-admin","name":"管理账户"}]}}`
			if response.rawString() != want {
				t.Fatalf("%s bind options body = %s, want %s", role, response.rawString(), want)
			}
		}
	})
}

// newBindOptionsEnvWithRole 构建以指定角色登录的绑定下拉测试环境：生产
// RequireSession 在 Register 时捕获闭包、newGenerationEnv 的同名中间件恒为
// user 角色，无法事后换角色，故本地重装同一形状的登录中间件。
func newBindOptionsEnvWithRole(t *testing.T, role string) *generationEnv {
	t.Helper()
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	executor := &mockExecutor{}
	hub := NewGenerationHub(func() string { return fixture.nowISO })
	deps := &Deps{
		Store: fixture.store, MaxTurnsPerConversation: 100, Now: clock,
		Generations: hub, Hub: hub, Executor: executor,
	}
	deps.RequireSession = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner := r.Header.Get("X-Test-Owner")
			if owner == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "请先登录"})
				return
			}
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{
				SystemAccountID: owner, Username: owner, DisplayName: owner, Role: role,
			})))
		})
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Register(k, "/__aisys__/api/my-chat")
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &generationEnv{
		routeEnv:  &routeEnv{t: t, server: server, fixture: fixture},
		executor:  executor,
		chatKeys:  &mockChatKeys{},
		objectDir: t.TempDir(),
		hub:       hub,
		deps:      deps,
	}
}

func TestConversationBindOptionsUnwiredArms(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"
	cases := []struct {
		name           string
		groupsLookup   ChatGroupOptionsLookup
		accountsLookup ChatAccountOptionsLookup
	}{
		{name: "两个查询端口均未接线"},
		{name: "分组查询端口未接线", accountsLookup: mockAccountOptionsLookup{}},
		{name: "账户查询端口未接线", groupsLookup: mockGroupOptionsLookup{}},
	}
	for _, item := range cases {
		env := newGenerationEnv(t)
		env.deps.GroupOptionsLookup = item.groupsLookup
		env.deps.AccountOptionsLookup = item.accountsLookup
		response := env.do("GET", prefix+"/conversation-bind-options", routeTestOwner, "")
		if response.status != http.StatusInternalServerError || response.code() != "internal_generation_failed" {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
		// DomainError 明细沿 writeChatRouteError 的既有 500 语义附带在"详情"中。
		if !strings.Contains(response.message(), "绑定选项列表暂不可用，请稍后重试") {
			t.Fatalf("%s message = %q, want 含 %q", item.name, response.message(), "绑定选项列表暂不可用，请稍后重试")
		}
	}
}

func TestConversationBindOptionsUnauthenticated(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupOptionsLookup = mockGroupOptionsLookup{}
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	unauthorized := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", "", "")
	if unauthorized.status != http.StatusUnauthorized || unauthorized.message() != "请先登录" {
		t.Fatalf("未登录 = %d %s", unauthorized.status, unauthorized.rawString())
	}
}
