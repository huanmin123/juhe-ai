package helpweb

// 帮助中心静态面契约测试：会话门（405 / 302 登录重定向）、角色跳转
// （/help → /help/ → /help/admin|user/）、admin 目录权限重定向与静态文件
// 服务（Node server.ts requireHelpSession + express.static）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
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

func TestHelpDisabledWithoutDist(t *testing.T) {
	stub := newKernelStub()
	deps := &Deps{DistPath: ""}
	deps.Mount(stub)
	if len(stub.handlers) != 0 {
		t.Fatalf("empty dist should mount nothing: %d", len(stub.handlers))
	}
}
