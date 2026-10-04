package mediajobsadmin

// 管理面媒体任务列表 handler 测试（沿 apikeys/statreads 管理面测试先例：
// 内存 SQLite + businessauth/authsys 会话面 + kernel 装配）：管理员可见、
// 非管理员 403、过滤/分页参数、行渲染契约（promptSummary 截断、costUsd、
// error 摘要）与零行快速返回。
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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var mustChangeFalse bool

type testEnv struct {
	deps   *authsys.Deps
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	db     *sql.DB
	repo   *gatewaymedia.MediaJobsRepo
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:mediajobsadmin-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE media_jobs (
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, api_key_id TEXT NOT NULL, account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT, upstream_job_id TEXT NOT NULL,
			status TEXT NOT NULL, request_snapshot_json TEXT NOT NULL DEFAULT '{}', artifact_json TEXT NOT NULL DEFAULT '{}',
			error_json TEXT NOT NULL DEFAULT '{}', usage_json TEXT NOT NULL DEFAULT '{}', cost_usd REAL NOT NULL DEFAULT 0,
			params_applied_json TEXT NOT NULL DEFAULT '[]', params_ignored_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("建表: %v: %v", statement, err)
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
	repo := gatewaymedia.NewMediaJobsRepo(db, false, time.Now)
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Repo: repo, Auth: deps}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, server: server, jar: map[string]string{}, db: db, repo: repo}
}

func (e *testEnv) do(t *testing.T, method, path string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
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

func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	id := ""
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err == nil {
		id = existing.ID
	}
	if id == "" {
		created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
		})
		if err != nil {
			t.Fatal(err)
		}
		id = created.ID
	}
	request, err := http.NewRequest(http.MethodPost, e.server.URL+"/__aisys__/api/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
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
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d %s", response.StatusCode, raw)
	}
	return id
}

// seedJob 经仓储插入一行（管理面读路径与 /v1 任务面同一仓储）。
func (e *testEnv) seedJob(t *testing.T, record gatewaymedia.MediaJobRecord) {
	t.Helper()
	if err := e.repo.Insert(context.Background(), record); err != nil {
		t.Fatalf("insert media job: %v", err)
	}
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope data 缺失: %#v", payload)
	}
	return data
}

func TestMediaJobsListRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	// 未登录：401。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs")
	if code != http.StatusUnauthorized {
		t.Fatalf("匿名 status = %d, want 401", code)
	}
	// 普通用户：403（requireAdmin）。
	env.login(t, "plainuser", "pass-123456", "user")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs")
	if code != http.StatusForbidden {
		t.Fatalf("普通用户 status = %d %v, want 403", code, payload)
	}
}

func TestMediaJobsListRowsAndFilters(t *testing.T) {
	env := newTestEnv(t)
	longPrompt := strings.Repeat("长", 200)
	seconds := 4.0
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_admin_completed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_1", AccountID: "acc_1",
		ProviderCode: "openai", UpstreamJobID: "up_1", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: longPrompt, Seconds: &seconds, Size: "1280x720"},
	})
	secondsOut := 4.0
	if err := env.repo.UpdateTerminal(context.Background(), "video_admin_completed", gatewaymedia.JobStatusCompleted,
		nil, gatewaymedia.MediaJobArtifact{}, gatewaymedia.MediaJobUsage{OutputVideoSeconds: &secondsOut}, 0.4); err != nil {
		t.Fatalf("update terminal: %v", err)
	}
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_admin_failed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_2", AccountID: "acc_2",
		ProviderCode: "openai", UpstreamJobID: "up_2", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "失败任务"},
	})
	if err := env.repo.UpdateTerminal(context.Background(), "video_admin_failed", gatewaymedia.JobStatusFailed,
		&gatewaymedia.MediaJobError{Code: "upstream_5xx", Message: "上游失败"}, gatewaymedia.MediaJobArtifact{},
		gatewaymedia.MediaJobUsage{}, 0); err != nil {
		t.Fatalf("update terminal failed row: %v", err)
	}

	env.login(t, "adminuser", "pass-123456", "admin")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs")
	if code != http.StatusOK {
		t.Fatalf("list status = %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["total"] != float64(2) {
		t.Fatalf("total = %v, want 2", data["total"])
	}
	rows, _ := data["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// 倒序：completed（后更新）在前不保证——按 id 找行断言形状。
	byID := map[string]map[string]any{}
	for _, row := range rows {
		object, _ := row.(map[string]any)
		byID[object["id"].(string)] = object
	}
	completed := byID["video_admin_completed"]
	if completed == nil {
		t.Fatalf("completed 行缺失: %#v", rows)
	}
	if completed["status"] != "completed" || completed["kind"] != "video" || completed["providerCode"] != "openai" {
		t.Fatalf("completed 行形状错误: %#v", completed)
	}
	if completed["providerJobId"] != "up_1" || completed["apiKeyId"] != "key_1" || completed["accountId"] != "acc_1" {
		t.Fatalf("completed 行归属错误: %#v", completed)
	}
	if completed["model"] != "sora-2" || completed["seconds"] != float64(4) || completed["size"] != "1280x720" {
		t.Fatalf("completed 行参数错误: %#v", completed)
	}
	if completed["costUsd"] != 0.4 {
		t.Fatalf("costUsd = %v, want 0.4", completed["costUsd"])
	}
	// promptSummary 截断 120 rune + 省略号。
	summary, _ := completed["promptSummary"].(string)
	if want := strings.Repeat("长", 120) + "…"; summary != want {
		t.Fatalf("promptSummary 长度 = %d, want %d（截断）", len([]rune(summary)), len([]rune(want)))
	}
	failed := byID["video_admin_failed"]
	if failed == nil || failed["costUsd"] != float64(0) {
		t.Fatalf("failed 行错误: %#v", failed)
	}
	failureError, _ := failed["error"].(map[string]any)
	if failureError == nil || failureError["code"] != "upstream_5xx" {
		t.Fatalf("failed 行 error 缺失: %#v", failed)
	}

	// 过滤：status=failed 只剩失败行。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?status=failed")
	if code != http.StatusOK {
		t.Fatalf("filter status = %d", code)
	}
	data = dataMap(t, payload)
	if data["total"] != float64(1) {
		t.Fatalf("status filter total = %v, want 1", data["total"])
	}
	// 过滤：api_key_id=key_2。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?api_key_id=key_2")
	if code != http.StatusOK {
		t.Fatalf("filter api_key_id = %d", code)
	}
	data = dataMap(t, payload)
	if data["total"] != float64(1) {
		t.Fatalf("api_key_id filter total = %v, want 1", data["total"])
	}
	// 过滤：account_id=acc_none 零行快速返回。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?account_id=acc_none")
	if code != http.StatusOK {
		t.Fatalf("filter account = %d", code)
	}
	data = dataMap(t, payload)
	if data["total"] != float64(0) {
		t.Fatalf("empty filter total = %v, want 0", data["total"])
	}
	// 分页：limit=1 → rows 1 行、total 仍 2。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?limit=1")
	if code != http.StatusOK {
		t.Fatalf("limit = %d", code)
	}
	data = dataMap(t, payload)
	rows, _ = data["rows"].([]any)
	if data["total"] != float64(2) || len(rows) != 1 {
		t.Fatalf("limit 分页错误: total=%v rows=%d", data["total"], len(rows))
	}
	// 非法 limit 回落默认 20（不 400）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?limit=abc")
	if code != http.StatusOK {
		t.Fatalf("invalid limit status = %d", code)
	}
	// 越界 limit 回落默认。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?limit=999")
	if code != http.StatusOK {
		t.Fatalf("oversize limit status = %d", code)
	}
	// 无写端点：POST 404/405（kernel 未登记）。
	postRequest, _ := http.NewRequest(http.MethodPost, env.server.URL+"/__aisys__/api/media-jobs", strings.NewReader("{}"))
	postRequest.Header.Set("Content-Type", "application/json")
	env.mu.Lock()
	for name, value := range env.jar {
		postRequest.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	env.mu.Unlock()
	postResponse, err := http.DefaultClient.Do(postRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusNotFound && postResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 404/405（只读）", postResponse.StatusCode)
	}
}
