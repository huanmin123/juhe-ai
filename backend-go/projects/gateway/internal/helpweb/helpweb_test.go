package helpweb

// 帮助中心静态面契约测试：会话门（405 / 302 登录重定向 / 503 基础设施
// 失败）、角色跳转（/help → /help/ → /help/admin|user/）、admin 目录权限
// 重定向与静态文件服务（Node server.ts requireHelpSession +
// express.static，含 express.static 的 Cache-Control 语义）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type helpFixture struct {
	deps   *Deps
	root   string
	server *authsys.AuthContext
}

func newHelpFixture(t *testing.T) *helpFixture {
	t.Helper()
	root := t.TempDir()
	helpDir := filepath.Join(root, "help", "user")
	adminDir := filepath.Join(root, "help", "admin")
	if err := os.MkdirAll(helpDir, 0o755); err != nil {
		t.Fatalf("mkdir user: %v", err)
	}
	if err := os.MkdirAll(adminDir, 0o755); err != nil {
		t.Fatalf("mkdir admin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(helpDir, "guide.html"), []byte("<html>user guide</html>"), 0o644); err != nil {
		t.Fatalf("write guide: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adminDir, "ops.html"), []byte("<html>admin ops</html>"), 0o644); err != nil {
		t.Fatalf("write ops: %v", err)
	}
	// 会话由 DevAutoLogin 解析（Node 侧为 /auth/me loopback 校验的等价注
	// 入点；解析失败即视为未登录）。
	fixture := &helpFixture{root: root}
	fixture.deps = &Deps{
		DistPath: root,
		DevAutoLogin: func(r *http.Request) *authsys.AuthContext {
			return fixture.server
		},
	}
	return fixture
}

// tokenFixture 构造走 authenticate 钩子（token 路径）的 fixture。
func tokenFixture(t *testing.T) (*helpFixture, *[]error) {
	t.Helper()
	fixture := newHelpFixture(t)
	errs := &[]error{}
	fixture.deps.authenticate = func(ctx context.Context, token string) (helpActor, error) {
		queue := *errs
		if len(queue) == 0 {
			return helpActor{}, errors.New("no scripted auth result")
		}
		err := queue[0]
		*errs = queue[1:]
		if err != nil {
			return helpActor{}, err
		}
		return helpActor{SystemAccountID: "sa-1", Username: "admin", DisplayName: "管理员", Role: "admin"}, nil
	}
	return fixture, errs
}

func helpTokenRequest(target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer juhe_tmp_"+strings.Repeat("a", 43))
	return request
}

func TestHelpMethodContract(t *testing.T) {
	fixture := newHelpFixture(t)
	// requireHelpSession 对非 GET/HEAD 直接 405，且无需会话。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/help/user/guide.html", nil)
	fixture.deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post not 405: %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"message":"帮助文档只支持读取"}` {
		t.Fatalf("405 body wrong: %q", got)
	}
}

// 挂载面：非 GET/HEAD 请求经方法无关的 subtree 注册进入 requireHelpSession，
// 405 JSON 原样可达（kernel 显式豁免）。
func TestHelpMountedMethodContract(t *testing.T) {
	fixture := newHelpFixture(t)
	stub := newKernelStub()
	fixture.deps.Mount(stub)
	handler := stub.handlerFor(http.MethodPost, "/__aisys__/help/user/guide.html")
	if handler == nil {
		t.Fatalf("subtree handler not registered")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/help/user/guide.html", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mounted post not 405: %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"message":"帮助文档只支持读取"}` {
		t.Fatalf("mounted 405 body wrong: %q", got)
	}
	// 裸前缀同样方法无关。
	recorder = httptest.NewRecorder()
	handler = stub.handlerFor(http.MethodDelete, "/__aisys__/help")
	if handler == nil {
		t.Fatalf("bare prefix handler not registered")
	}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/__aisys__/help", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("bare prefix delete not 405: %d", recorder.Code)
	}
}

// token 认证基础设施失败 → 503 JSON；401/403 形态的拒绝 → 302 登录重定向
//（Node readHelpCurrentUser 401/403 → undefined user → redirect）。
func TestHelpSessionFailureSemantics(t *testing.T) {
	fixture, errs := tokenFixture(t)

	*errs = append(*errs, errors.New("db connection refused"))
	recorder := httptest.NewRecorder()
	fixture.deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, helpTokenRequest("/__aisys__/help/user/"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("infrastructure failure not 503: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != `{"message":"登录态校验暂不可用，请稍后重试"}` {
		t.Fatalf("503 body wrong: %q", got)
	}

	// 会话过期（401 形态）→ 302 登录重定向。
	*errs = append(*errs, modelcheckauth.ErrSessionExpired)
	recorder = httptest.NewRecorder()
	fixture.deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, helpTokenRequest("/__aisys__/help/user/"))
	if recorder.Code != http.StatusFound {
		t.Fatalf("expired session not 302: %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); !strings.HasPrefix(location, "/__aisys__/login?redirect=") {
		t.Fatalf("login redirect wrong: %q", location)
	}

	// 无效 token（401 形态）→ 302。
	*errs = append(*errs, modelcheckauth.ErrInvalidToken)
	recorder = httptest.NewRecorder()
	fixture.deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, helpTokenRequest("/__aisys__/help/user/"))
	if recorder.Code != http.StatusFound {
		t.Fatalf("invalid token not 302: %d", recorder.Code)
	}
}

func TestHelpRedirectsPerRole(t *testing.T) {
	fixture := newHelpFixture(t)
	// 走真实 Mount 装配：/help 精确面与 /help/ 子树面。
	stub := newKernelStub()
	fixture.deps.Mount(stub)
	if len(stub.handlers) == 0 {
		t.Fatalf("mount registered nothing")
	}
	// 会话由 fixture.server 提供（DevAutoLogin 注入点）。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/help", nil)
	fixture.server = &authsys.AuthContext{SystemAccountID: "sa-admin", Role: "admin"}
	stub.handlerFor(http.MethodGet, "/__aisys__/help").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/__aisys__/help/" {
		t.Fatalf("bare prefix redirect wrong: %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	// /help/ 按角色跳转。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/help/", nil)
	stub.handlerFor(http.MethodGet, "/__aisys__/help/").ServeHTTP(recorder, request)
	// 普通用户访问 admin 目录 → 重定向 /user/。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/help/admin/ops.html", nil)
	fixture.server = &authsys.AuthContext{SystemAccountID: "sa-user", Role: "user"}
	stub.handlerFor(http.MethodGet, "/__aisys__/help/").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/__aisys__/help/user/" {
		t.Fatalf("admin gate redirect wrong: %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestHelpStaticServesAndFallsBack(t *testing.T) {
	fixture := newHelpFixture(t)
	// 命中静态文件。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/guide.html", nil)
	fixture.deps.serve(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("static not 200: %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "<html>user guide</html>" {
		t.Fatalf("static body wrong: %q", got)
	}
	// 未命中 → SPA index 回退（no-cache）。
	if err := os.WriteFile(filepath.Join(fixture.root, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/missing", nil)
	fixture.deps.serve(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("spa fallback not 200: %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "<html>spa</html>" {
		t.Fatalf("spa body wrong: %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control wrong: %q", got)
	}
}

// express.static 缓存头语义：仅 index.html/brand-icon.svg/build-info.json
// 携带 no-cache；help 子树内的其他文件（包括 help 自身的 assets 目录）不设
// Cache-Control；目录解析 <dir>/index.html。
func TestHelpStaticCacheHeaders(t *testing.T) {
	fixture := newHelpFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.root, "help", "user", "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "help", "user", "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "help", "user", "index.html"), []byte("<html>user index</html>"), 0o644); err != nil {
		t.Fatalf("write user index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "help", "brand-icon.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatalf("write brand icon: %v", err)
	}

	// 普通静态文件：无 Cache-Control 头（Node setHeaders 不命中任何分支）。
	recorder := httptest.NewRecorder()
	fixture.deps.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/guide.html", nil))
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("plain static file should carry no Cache-Control: %q", got)
	}
	// help 子树 assets：immutable 分支只匹配 dist/assets，不匹配
	// dist/help/**，因此同样不设头。
	recorder = httptest.NewRecorder()
	fixture.deps.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/assets/app.js", nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "" {
		t.Fatalf("help asset wrong: %d %q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	// basename 命中：brand-icon.svg → no-cache。
	recorder = httptest.NewRecorder()
	fixture.deps.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/brand-icon.svg", nil))
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("brand icon cache-control wrong: %q", got)
	}
	// 目录解析：/help/user/ → help/user/index.html（no-cache）。
	recorder = httptest.NewRecorder()
	fixture.deps.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<html>user index</html>" {
		t.Fatalf("directory index wrong: %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("directory index cache-control wrong: %q", got)
	}
	// 无 index 的目录 → SPA 回退。
	if err := os.MkdirAll(filepath.Join(fixture.root, "help", "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write spa index: %v", err)
	}
	recorder = httptest.NewRecorder()
	fixture.deps.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/empty/", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "<html>spa</html>" {
		t.Fatalf("indexless directory fallback wrong: %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("fallback cache-control wrong: %q", got)
	}
}

func TestHelpDisabledWithoutDist(t *testing.T) {
	stub := newKernelStub()
	deps := &Deps{DistPath: ""}
	deps.Mount(stub)
	if len(stub.handlers) != 0 {
		t.Fatalf("empty dist should mount nothing: %d", len(stub.handlers))
	}
}
