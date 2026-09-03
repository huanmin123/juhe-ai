package announcements

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:announcements-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS announcements (id TEXT PRIMARY KEY, title TEXT NOT NULL, content TEXT NOT NULL, level TEXT NOT NULL, status TEXT NOT NULL, created_by TEXT NOT NULL, updated_by TEXT NOT NULL, published_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS announcement_reads (announcement_id TEXT NOT NULL, system_account_id TEXT NOT NULL, read_at TEXT NOT NULL, PRIMARY KEY (announcement_id, system_account_id), FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE)`,
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
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	store, err := NewStore(db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	Mount(k, deps, store, nil)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *testEnv) login(t *testing.T, username, password, role string) {
	t.Helper()
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestAnnouncementFullLifecycle(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/announcements",
		`{"title":"维护公告","content":"系统将于今晚维护","level":"warning"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	data := created["data"].(map[string]any)
	id := data["id"].(string)
	revision := data["revision"].(string)

	// Draft must not be public.
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/my-announcements", "")
	if code != 200 || len(list["data"].([]any)) != 0 {
		t.Fatalf("draft must not be public: %d %v", code, list)
	}

	// Publish with the creation revision.
	code, published := env.do(t, http.MethodPost, "/__aisys__/api/announcements/"+id+"/publish",
		`{"expectedRevision":"`+revision+`"}`)
	if code != 200 {
		t.Fatalf("publish: %d %v", code, published)
	}
	newRevision := published["data"].(map[string]any)["revision"].(string)
	if newRevision == revision {
		t.Fatal("publish must bump revision")
	}

	code, list = env.do(t, http.MethodGet, "/__aisys__/api/my-announcements", "")
	items := list["data"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["title"] != "维护公告" {
		t.Fatalf("public list after publish: %d %v", code, list)
	}

	// Stale-revision patch → 409 + currentRevision.
	staleCode, stalePayload := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+revision+`","title":"过期标题"}`)
	if staleCode != http.StatusConflict || stalePayload["currentRevision"] != newRevision {
		t.Fatalf("stale patch: %d %v", staleCode, stalePayload)
	}

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+newRevision+`","title":"维护公告 v2","content":"时间改为明晚"}`)
	if code != 200 {
		t.Fatalf("fresh patch: %d %v", code, patched)
	}
	latestRevision := patched["data"].(map[string]any)["revision"].(string)

	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+id, "")
	detailData := detail["data"].(map[string]any)
	if code != 200 || detailData["title"] != "维护公告 v2" || detailData["revision"] != latestRevision {
		t.Fatalf("edit detail: %d %v", code, detail)
	}

	// Read tracking.
	code, readResult := env.do(t, http.MethodPost, "/__aisys__/api/my-announcements/read",
		`{"announcementIds":["`+id+`"]}`)
	if code != 200 || readResult["data"].(map[string]any)["count"] != float64(1) {
		t.Fatalf("mark read: %d %v", code, readResult)
	}

	// Draft transition then republish clears read state (Node semantics).
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+latestRevision+`","status":"draft"}`)
	if code != 200 {
		t.Fatal("draft transition failed")
	}
	code, detail2 := env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+id, "")
	rev2 := detail2["data"].(map[string]any)["revision"].(string)
	code, republished := env.do(t, http.MethodPost, "/__aisys__/api/announcements/"+id+"/publish",
		`{"expectedRevision":"`+rev2+`"}`)
	if code != 200 {
		t.Fatalf("republish: %d %v", code, republished)
	}

	// No-op patch returns the current revision.
	code, noOp := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+republished["data"].(map[string]any)["revision"].(string)+`"}`)
	if code != 200 || noOp["data"].(map[string]any)["revision"] != republished["data"].(map[string]any)["revision"] {
		t.Fatalf("no-op patch: %d %v", code, noOp)
	}

	code, page := env.do(t, http.MethodGet, "/__aisys__/api/announcements?page=1&pageSize=20", "")
	pageData := page["data"].(map[string]any)
	if code != 200 || len(pageData["items"].([]any)) != 1 {
		t.Fatalf("admin list: %d %v", code, page)
	}

	deleteCode, _ := env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+republished["data"].(map[string]any)["revision"].(string)+`"}`)
	if deleteCode != http.StatusNoContent {
		t.Fatalf("delete: %d", deleteCode)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+id, "")
	if code != 404 {
		t.Fatalf("after delete: %d", code)
	}
}

func TestAnnouncementAuthorization(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "plain", "plain-pass", "user")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/announcements", "")
	if code != http.StatusForbidden || payload["message"] != "需要管理员权限" {
		t.Fatalf("user list: %d %v", code, payload)
	}

	// Create guard: user role cannot create either.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements",
		`{"title":"x","content":"y"}`)
	if code != http.StatusForbidden {
		t.Fatalf("user create: %d %v", code, payload)
	}
}

func TestAnnouncementCreateValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"title":"only-title"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("missing content: %d %v", code, payload)
	}

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements",
		`{"title":"标题","content":"内容","level":"bogus"}`)
	if code != http.StatusConflict || payload["message"] != "公告级别无效" {
		t.Fatalf("bad level: %d %v", code, payload)
	}
}
