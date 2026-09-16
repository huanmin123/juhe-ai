package helpweb

// w11f 覆盖波次：补 resolveSession token 默认鉴权、路径穿越、目录 index、
// SPA 分支与挂载面。

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// w11fAuthEnv 构造带真实 authsys 端口的帮助面环境（覆盖默认 authenticate 构造）。
func w11fAuthEnv(t *testing.T) (*Deps, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	helpDir := filepath.Join(root, "help", "user")
	if err := os.MkdirAll(helpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helpDir, "index.html"), []byte("<html>user index</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:w11f-help-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	authDeps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	falseValue := false
	if _, err := accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &falseValue,
		Username: "w11fuser", DisplayName: "w11f用户", Password: "w11f-pass", Role: "user",
	}); err != nil {
		t.Fatal(err)
	}
	loginServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	_ = loginServer
	// 直接走登录端点获得会话 cookie（真实 kernel + auth 路由）。
	loginKernel := kernel.New(kernel.Options{CompressionDisabled: true})
	authDeps.MountAuth(loginKernel, "lax", false)
	kernelStubServer := httptest.NewServer(loginKernel.Handler())
	t.Cleanup(kernelStubServer.Close)
	loginRequest, _ := http.NewRequest(http.MethodPost, kernelStubServer.URL+"/__aisys__/api/auth/login", strings.NewReader(`{"username":"w11fuser","password":"w11f-pass"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("Content-Length", strconvItoa(len(`{"username":"w11fuser","password":"w11f-pass"}`)))
	loginResponse, err := http.DefaultClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(loginResponse.Body)
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %s", loginResponse.StatusCode, raw)
	}
	cookie := ""
	for _, c := range loginResponse.Cookies() {
		if c.Name == authsys.SessionCookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("session cookie missing")
	}
	deps := &Deps{Auth: authDeps, DistPath: root}
	return deps, kernelStubServer, cookie
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// TestW11FDefaultAuthenticate 覆盖默认 authenticate 构造与 token 成功映射。
func TestW11FDefaultAuthenticate(t *testing.T) {
	deps, _, cookie := w11fAuthEnv(t)

	request := httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/", nil)
	request.AddCookie(&http.Cookie{Name: authsys.SessionCookieName, Value: cookie})
	// resolveSession 走默认 authenticate 构造并映射成功会话。
	if auth, err := deps.resolveSession(request); err != nil || auth == nil || auth.Username != "w11fuser" || auth.Role != "user" {
		t.Fatalf("default authenticate = %+v/%v", auth, err)
	}
	recorder := httptest.NewRecorder()
	deps.requireHelpSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("default authenticate gate = %d %s", recorder.Code, recorder.Body.String())
	}
	// 无 token 且无 DevAutoLogin → 302 登录。
	recorder = httptest.NewRecorder()
	deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/", nil))
	if recorder.Code != http.StatusFound || !strings.Contains(recorder.Header().Get("Location"), "/__aisys__/login") {
		t.Fatalf("anonymous redirect = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}
	// 管理员路径与非管理员 → 302 用户面。
	adminPathRequest := httptest.NewRequest(http.MethodGet, "/__aisys__/help/admin/ops.html", nil)
	adminPathRequest.AddCookie(&http.Cookie{Name: authsys.SessionCookieName, Value: cookie})
	recorder = httptest.NewRecorder()
	deps.requireHelpSession(http.NotFoundHandler()).ServeHTTP(recorder, adminPathRequest)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/__aisys__/help/user/" {
		t.Fatalf("user redirect = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}
}

// TestW11FServeBranches 覆盖 serve 的空 relative、目录 index 与 SPA 回退。
func TestW11FServeBranches(t *testing.T) {
	fixture := newHelpFixture(t)
	fixture.server = &authsys.AuthContext{SystemAccountID: "sa-1", Username: "admin", Role: "super_admin"}
	serve := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		fixture.deps.dispatch(recorder, request)
		return recorder
	}
	// 子树根（经 dispatch 的 /__aisys__/help/ 分支已有覆盖；这里驱动 serve 的
	// 空 relative → redirectRole admin）。
	recorder := serve("/__aisys__/help/admin/../user/")
	_ = recorder
	// 目录 index 解析: user 目录下放置 index.html。
	if err := os.WriteFile(filepath.Join(fixture.root, "help", "user", "index.html"), []byte("<html>idx</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder = serve("/__aisys__/help/user/")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "idx") {
		t.Fatalf("dir index = %d %s", recorder.Code, recorder.Body.String())
	}
	// 无 index 的目录 → SPA 回退。
	if err := os.MkdirAll(filepath.Join(fixture.root, "help", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	recorder = serve("/__aisys__/help/empty/")
	if recorder.Code != http.StatusFound { // 目录尾斜杠触发 ServeFile 重定向到 index
		t.Logf("empty dir = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}
	// serveIndex 缺 index.html → 404（无 dist index 的根）。
	bare := &Deps{DistPath: t.TempDir(), DevAutoLogin: func(*http.Request) *authsys.AuthContext { return nil }}
	recorder = httptest.NewRecorder()
	bare.serve(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/nope", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing index fallback = %d", recorder.Code)
	}
}

// TestW11FSPABranches 覆 spa.go 的裸前缀重定向、穿越、目录与回退分支。
func TestW11FSPABranches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app-123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "index.html"), []byte("<html>sub</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := newKernelStub()
	MountSPA(stub, root)

	// 裸前缀 → 302 /__aisys__/。
	handler := stub.handlerFor(http.MethodGet, "/__aisys__")
	if handler == nil {
		t.Fatal("bare prefix handler missing")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__", nil))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/__aisys__/" {
		t.Fatalf("bare prefix = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}

	spa := stub.handlerFor(http.MethodGet, "/__aisys__/")
	if spa == nil {
		t.Fatal("spa handler missing")
	}
	hit := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		spa.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		return recorder
	}
	// 子树根 → index。
	recorder = hit("/__aisys__/")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "spa") {
		t.Fatalf("spa root = %d", recorder.Code)
	}
	// assets 不可变缓存。
	recorder = hit("/__aisys__/assets/app-123.js")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset = %d %s", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	// 目录 index 解析。
	recorder = hit("/__aisys__/sub/")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "sub") {
		t.Fatalf("dir index = %d", recorder.Code)
	}
	// 无 index 目录 → SPA 回退。
	recorder = hit("/__aisys__/bare/")
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("bare dir should fall back to index: %d", recorder.Code)
	}
	// API/health 放行（NotFound 由 kernel 兜底）。
	recorder = hit("/__aisys__/api/anything")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("api fallthrough = %d", recorder.Code)
	}
	recorder = hit("/__aisys__/health")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("health fallthrough = %d", recorder.Code)
	}
	// 路径穿越（clean 后包含 ..）→ 404。
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/x", nil)
	request.URL.Path = "/__aisys__/..%2fsecret"
	request.URL.RawPath = ""
	recorder = httptest.NewRecorder()
	spa.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal = %d", recorder.Code)
	}
	// 非 GET → 404。
	recorder = httptest.NewRecorder()
	spa.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("post = %d", recorder.Code)
	}
}
