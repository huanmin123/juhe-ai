package chat

// GET /my-chat/accounts（AI 问答会话账户唯一绑定设计 §5.2，替代已退场的
// GET /conversation-bind-options）的路由级覆盖：登录态 200 信封精确形状、查询
// 端口未接线的 500 臂、未登录 401 臂，以及数据范围（ChatBindScope）两臂——
// 普通用户传 viewer scope 且拿到用户集合，admin/super_admin 拿到全量集合；
// 旧 bind-options 端点退场为 404。Mock 风格与 chat_bind_modes_test.go 一致。

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

// mockAccountOptionsLookup 返回确定性的可派发账户摘要（id/name/providerCode/
// status）：按 IsAdmin 臂返回不同集合，seen 非 nil 时记录收到的 scope。
type mockAccountOptionsLookup struct {
	seen *bindScopeRecorder
}

func (m mockAccountOptionsLookup) ListChatAccountOptions(_ context.Context, scope ChatBindScope) ([]ChatAccountOption, error) {
	if m.seen != nil {
		m.seen.record(scope, "")
	}
	if scope.IsAdmin {
		return []ChatAccountOption{{ID: "a-admin", Name: "管理账户", ProviderCode: "openai", Status: "active"}}, nil
	}
	return []ChatAccountOption{{ID: "a1", Name: "账户一", ProviderCode: "openai", Status: "active"}}, nil
}

func TestMyChatAccountsRoute(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	response := env.do("GET", "/__aisys__/api/my-chat/accounts", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("accounts = %d %s", response.status, response.rawString())
	}
	// 最小投影信封的精确形状：accounts 恒为数组，元素只含
	// id/name/providerCode/status。
	want := `{"data":[{"id":"a1","name":"账户一","providerCode":"openai","status":"active"}]}`
	if response.rawString() != want {
		t.Fatalf("accounts body = %s, want %s", response.rawString(), want)
	}
}

// TestMyChatAccountsScopeArms 覆盖账户列表端点的数据范围两臂：普通用户把
// viewer scope（IsAdmin=false）传给查询端口并拿到用户集合；admin/super_admin
// 把 IsAdmin=true scope 传给查询端口并拿到全量集合。
func TestMyChatAccountsScopeArms(t *testing.T) {
	t.Run("普通用户传 viewer scope 且拿到用户集合", func(t *testing.T) {
		env := newGenerationEnv(t)
		accountSeen := &bindScopeRecorder{}
		env.deps.AccountOptionsLookup = mockAccountOptionsLookup{seen: accountSeen}
		response := env.do("GET", "/__aisys__/api/my-chat/accounts", routeTestOwner, "")
		if response.status != http.StatusOK {
			t.Fatalf("user accounts = %d %s", response.status, response.rawString())
		}
		userScope := ChatBindScope{ViewerID: routeTestOwner, IsAdmin: false}
		scopes, _ := accountSeen.snapshot()
		if len(scopes) != 1 || scopes[0] != userScope {
			t.Fatalf("查询收到的 scope = %v, want [%v]", scopes, userScope)
		}
		want := `{"data":[{"id":"a1","name":"账户一","providerCode":"openai","status":"active"}]}`
		if response.rawString() != want {
			t.Fatalf("user accounts body = %s, want %s", response.rawString(), want)
		}
	})

	t.Run("admin 与 super_admin 拿到全量集合", func(t *testing.T) {
		for _, role := range []string{"admin", "super_admin"} {
			env := newBindOptionsEnvWithRole(t, role)
			env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
			response := env.do("GET", "/__aisys__/api/my-chat/accounts", routeTestOwner, "")
			if response.status != http.StatusOK {
				t.Fatalf("%s accounts = %d %s", role, response.status, response.rawString())
			}
			want := `{"data":[{"id":"a-admin","name":"管理账户","providerCode":"openai","status":"active"}]}`
			if response.rawString() != want {
				t.Fatalf("%s accounts body = %s, want %s", role, response.rawString(), want)
			}
		}
	})
}

// newBindOptionsEnvWithRole 构建以指定角色登录的账户列表测试环境：生产
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

func TestMyChatAccountsUnwiredArm(t *testing.T) {
	env := newGenerationEnv(t)
	response := env.do("GET", "/__aisys__/api/my-chat/accounts", routeTestOwner, "")
	if response.status != http.StatusInternalServerError || response.code() != "internal_generation_failed" {
		t.Fatalf("unwired accounts = %d %s", response.status, response.rawString())
	}
	// DomainError 明细沿 writeChatRouteError 的既有 500 语义附带在"详情"中。
	if !strings.Contains(response.message(), "账户列表暂不可用，请稍后重试") {
		t.Fatalf("message = %q, want 含 %q", response.message(), "账户列表暂不可用，请稍后重试")
	}
}

func TestMyChatAccountsUnauthenticated(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	unauthorized := env.do("GET", "/__aisys__/api/my-chat/accounts", "", "")
	if unauthorized.status != http.StatusUnauthorized || unauthorized.message() != "请先登录" {
		t.Fatalf("未登录 = %d %s", unauthorized.status, unauthorized.rawString())
	}
}

// TestMyChatAccountsNoStoreHeaders：账户列表内容随数据范围实时变化，成功
// 响应必须携带 no-store（与 accounts 包 options 口径一致）。
func TestMyChatAccountsNoStoreHeaders(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	request, err := http.NewRequest(http.MethodGet, env.server.URL+"/__aisys__/api/my-chat/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test-Owner", routeTestOwner)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("accounts = %d", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
	if got := response.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want %q", got, "no-cache")
	}
}

// TestConversationBindOptionsRetired：旧绑定下拉端点随三种绑定模式退场（设计
// §9.3），不再注册。
func TestConversationBindOptionsRetired(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	response := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", routeTestOwner, "")
	if response.status != http.StatusNotFound {
		t.Fatalf("retired bind options = %d %s", response.status, response.rawString())
	}
}
