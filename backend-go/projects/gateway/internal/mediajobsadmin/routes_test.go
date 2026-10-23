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
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, vendor_code TEXT)`,
		`CREATE TABLE media_jobs (
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, api_key_id TEXT NOT NULL, account_id TEXT NOT NULL,
			provider_protocol_profile_id TEXT, upstream_job_id TEXT NOT NULL,
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
	// vendor 水合源：accounts 行（B4① 拆除 media_jobs.provider_code 后读面
	// 经 LEFT JOIN accounts 取 vendor_code）。
	for _, seed := range []struct{ id, vendor string }{
		{"acc_1", "gpt"}, {"acc_2", "gpt"},
	} {
		if _, err := env.db.Exec("INSERT INTO accounts (id, vendor_code) VALUES (?, ?)", seed.id, seed.vendor); err != nil {
			t.Fatalf("seed accounts: %v", err)
		}
	}
	longPrompt := strings.Repeat("长", 200)
	seconds := 4.0
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_admin_completed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_1", AccountID: "acc_1",
		UpstreamJobID: "up_1", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: longPrompt, Seconds: &seconds, Size: "1280x720"},
	})
	secondsOut := 4.0
	if _, err := env.repo.UpdateTerminal(context.Background(), "video_admin_completed", gatewaymedia.JobStatusCompleted,
		nil, gatewaymedia.MediaJobArtifact{}, gatewaymedia.MediaJobUsage{OutputVideoSeconds: &secondsOut}, 0.4); err != nil {
		t.Fatalf("update terminal: %v", err)
	}
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_admin_failed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_2", AccountID: "acc_2",
		UpstreamJobID: "up_2", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "失败任务"},
	})
	if _, err := env.repo.UpdateTerminal(context.Background(), "video_admin_failed", gatewaymedia.JobStatusFailed,
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
	if completed["status"] != "completed" || completed["kind"] != "video" || completed["providerCode"] != "gpt" {
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

func TestMediaJobsDetailRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	// 未登录：401。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/video_detail")
	if code != http.StatusUnauthorized {
		t.Fatalf("匿名 status = %d, want 401", code)
	}
	// 普通用户：403（requireAdmin，详情与列表同门禁）。
	env.login(t, "plainuser", "pass-123456", "user")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/video_detail")
	if code != http.StatusForbidden {
		t.Fatalf("普通用户 status = %d %v, want 403", code, payload)
	}
}

func TestMediaJobsDetailReturnsFullRecord(t *testing.T) {
	env := newTestEnv(t)
	seconds := 8.0
	expires := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_detail_completed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_detail", AccountID: "acc_detail",
		UpstreamJobID: "up_detail", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "sora-2", Prompt: "全量提示词", Seconds: &seconds, Size: "1280x720", N: 1,
			ProviderOptionsApplied: []string{"resolution"},
		},
		ParamsApplied: []string{"seconds", "size"}, ParamsIgnored: []string{"seed"},
	})
	artifact := gatewaymedia.MediaJobArtifact{ContentURL: "https://upstream.example/content.mp4", ExpiresAt: &expires}
	usageSeconds := 8.0
	if _, err := env.repo.UpdateTerminal(context.Background(), "video_detail_completed", gatewaymedia.JobStatusCompleted,
		nil, artifact, gatewaymedia.MediaJobUsage{OutputVideoSeconds: &usageSeconds, Raw: map[string]any{"native_seconds": 8}}, 0.8); err != nil {
		t.Fatalf("update terminal: %v", err)
	}
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_detail_failed", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_detail", AccountID: "acc_detail",
		UpstreamJobID: "up_detail_failed", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "失败任务"},
	})
	if _, err := env.repo.UpdateTerminal(context.Background(), "video_detail_failed", gatewaymedia.JobStatusFailed,
		&gatewaymedia.MediaJobError{Code: "upstream_5xx", Message: "上游失败"}, gatewaymedia.MediaJobArtifact{},
		gatewaymedia.MediaJobUsage{}, 0); err != nil {
		t.Fatalf("update terminal failed row: %v", err)
	}
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "audiojob_detail_transcription", Kind: gatewaymedia.JobKindAudioTranscription, APIKeyID: "key_detail", AccountID: "acc_detail",
		UpstreamJobID: "up_detail_transcription", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "paraformer-v2", Language: "zh"},
	})

	env.login(t, "adminuser", "pass-123456", "admin")
	// 未命中：404 媒体任务不存在。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/video_missing")
	if code != http.StatusNotFound {
		t.Fatalf("missing status = %d %v, want 404", code, payload)
	}
	// completed 行：全量细节（prompt 全文、快照参数、产物定位、用量、参数回显、成本）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/video_detail_completed")
	if code != http.StatusOK {
		t.Fatalf("detail status = %d %v", code, payload)
	}
	detail := dataMap(t, payload)
	if detail["prompt"] != "全量提示词" || detail["model"] != "sora-2" || detail["n"] != float64(1) || detail["size"] != "1280x720" {
		t.Fatalf("detail 请求参数错误: %#v", detail)
	}
	if detail["providerJobId"] != "up_detail" || detail["apiKeyId"] != "key_detail" || detail["accountId"] != "acc_detail" || detail["costUsd"] != 0.8 {
		t.Fatalf("detail 归属/成本错误: %#v", detail)
	}
	options, _ := detail["providerOptionsApplied"].([]any)
	if len(options) != 1 || options[0] != "resolution" {
		t.Fatalf("providerOptionsApplied 错误: %#v", detail["providerOptionsApplied"])
	}
	artifactPayload, _ := detail["artifact"].(map[string]any)
	if artifactPayload == nil || artifactPayload["contentUrl"] != "https://upstream.example/content.mp4" {
		t.Fatalf("artifact 定位缺失: %#v", detail["artifact"])
	}
	if artifactPayload["expiresAt"] != "2026-10-17T12:00:00.000Z" {
		t.Fatalf("artifact expiresAt = %v", artifactPayload["expiresAt"])
	}
	usagePayload, _ := detail["usage"].(map[string]any)
	if usagePayload == nil || usagePayload["outputVideoSeconds"] != float64(8) {
		t.Fatalf("usage 缺失: %#v", detail["usage"])
	}
	rawUsage, _ := usagePayload["raw"].(map[string]any)
	if rawUsage == nil || rawUsage["native_seconds"] != float64(8) {
		t.Fatalf("usage raw 缺失: %#v", usagePayload)
	}
	applied, _ := detail["paramsApplied"].([]any)
	ignored, _ := detail["paramsIgnored"].([]any)
	if len(applied) != 2 || len(ignored) != 1 || ignored[0] != "seed" {
		t.Fatalf("参数回显错误: applied=%v ignored=%v", detail["paramsApplied"], detail["paramsIgnored"])
	}
	if _, hasError := detail["error"]; hasError {
		t.Fatalf("completed 行不应携带 error: %#v", detail)
	}
	// failed 行：错误面在场、无产物定位与用量。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/video_detail_failed")
	if code != http.StatusOK {
		t.Fatalf("failed detail status = %d", code)
	}
	detail = dataMap(t, payload)
	failureError, _ := detail["error"].(map[string]any)
	if failureError == nil || failureError["code"] != "upstream_5xx" || failureError["message"] != "上游失败" {
		t.Fatalf("failed 行 error 错误: %#v", detail["error"])
	}
	if _, hasArtifact := detail["artifact"]; hasArtifact {
		t.Fatalf("failed 行不应携带 artifact: %#v", detail)
	}
	if _, hasUsage := detail["usage"]; hasUsage {
		t.Fatalf("failed 行不应携带 usage: %#v", detail)
	}
	// 转写行：language 回显、空 prompt 与缺省参数省略。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs/audiojob_detail_transcription")
	if code != http.StatusOK {
		t.Fatalf("transcription detail status = %d", code)
	}
	detail = dataMap(t, payload)
	if detail["language"] != "zh" || detail["model"] != "paraformer-v2" || detail["kind"] != "audio_transcription" {
		t.Fatalf("transcription 行形状错误: %#v", detail)
	}
	if _, hasPrompt := detail["prompt"]; hasPrompt {
		t.Fatalf("空 prompt 应省略: %#v", detail)
	}
	if _, hasN := detail["n"]; hasN {
		t.Fatalf("缺省 n 应省略: %#v", detail)
	}
	if _, hasSeconds := detail["seconds"]; hasSeconds {
		t.Fatalf("缺省 seconds 应省略: %#v", detail)
	}
}

func TestMyMediaJobsScopesToCaller(t *testing.T) {
	env := newTestEnv(t)
	// 匿名：401（先于任何登录，jar 为空才成立）。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs")
	if code != http.StatusUnauthorized {
		t.Fatalf("匿名 status = %d, want 401", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs/video_owner_a")
	if code != http.StatusUnauthorized {
		t.Fatalf("匿名详情 status = %d, want 401", code)
	}
	env.login(t, "adminuser", "pass-123456", "admin")
	userAID := env.login(t, "plainusera", "pass-123456", "user")
	userBID := env.login(t, "plainuserb", "pass-123456", "user")
	for _, key := range []struct{ id, owner string }{
		{id: "key_a1", owner: userAID},
		{id: "key_b1", owner: userBID},
	} {
		if _, err := env.db.Exec(`INSERT INTO api_keys (id, system_account_id) VALUES (?, ?)`, key.id, key.owner); err != nil {
			t.Fatalf("seed api key: %v", err)
		}
	}
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_owner_a", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_a1", AccountID: "acc_a",
		UpstreamJobID: "up_a", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "用户 A 任务一"},
	})
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_owner_a2", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_a1", AccountID: "acc_a",
		UpstreamJobID: "up_a2", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "用户 A 任务二"},
	})
	env.seedJob(t, gatewaymedia.MediaJobRecord{
		ID: "video_owner_b", Kind: gatewaymedia.JobKindVideo, APIKeyID: "key_b1", AccountID: "acc_b",
		UpstreamJobID: "up_b", Status: gatewaymedia.JobStatusQueued,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "sora-2", Prompt: "用户 B 任务"},
	})

	// 用户 A：列表只见自己 2 行，行不带 providerJobId。
	env.login(t, "plainusera", "pass-123456", "user")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs")
	if code != http.StatusOK {
		t.Fatalf("userA list status = %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["total"] != float64(2) {
		t.Fatalf("userA total = %v, want 2", data["total"])
	}
	rows, _ := data["rows"].([]any)
	for _, row := range rows {
		object, _ := row.(map[string]any)
		if object["apiKeyId"] != "key_a1" {
			t.Fatalf("userA 行跨属主: %#v", object)
		}
		if _, has := object["providerJobId"]; has {
			t.Fatalf("userA 行不应携带 providerJobId: %#v", object)
		}
	}
	// 用户 A：自己的详情可见且省略 providerJobId；他人详情 404；他人 Key 过滤被归属收敛为 0。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs/video_owner_a")
	if code != http.StatusOK {
		t.Fatalf("userA detail status = %d %v", code, payload)
	}
	detail := dataMap(t, payload)
	if _, has := detail["providerJobId"]; has {
		t.Fatalf("userA 详情不应携带 providerJobId: %#v", detail)
	}
	if detail["accountId"] != "acc_a" {
		t.Fatalf("userA 详情缺命中账户: %#v", detail)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs/video_owner_b")
	if code != http.StatusNotFound {
		t.Fatalf("跨属主详情 status = %d, want 404", code)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs?api_key_id=key_b1")
	if code != http.StatusOK || dataMap(t, payload)["total"] != float64(0) {
		t.Fatalf("跨属主 api_key_id 过滤未收敛: %d %v", code, payload)
	}

	// 管理员在 my 面同样只看自己（未持有任何 Key → 0 行，不因角色放宽）。
	env.login(t, "adminuser", "pass-123456", "admin")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-media-jobs")
	if code != http.StatusOK || dataMap(t, payload)["total"] != float64(0) {
		t.Fatalf("admin my 面 status = %d %v, want 200/total 0", code, payload)
	}

	// 管理面列表：无筛选全量 3 行；按属主筛选收敛。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs")
	if code != http.StatusOK || dataMap(t, payload)["total"] != float64(3) {
		t.Fatalf("admin 全量 status = %d %v, want 200/total 3", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?system_account_id="+userBID)
	if code != http.StatusOK {
		t.Fatalf("admin owner filter status = %d", code)
	}
	data = dataMap(t, payload)
	rows, _ = data["rows"].([]any)
	if data["total"] != float64(1) || len(rows) != 1 || rows[0].(map[string]any)["id"] != "video_owner_b" {
		t.Fatalf("admin owner filter 结果错误: total=%v rows=%v", data["total"], rows)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/media-jobs?system_account_id="+userAID)
	if code != http.StatusOK || dataMap(t, payload)["total"] != float64(2) {
		t.Fatalf("admin owner filter userA status = %d %v", code, payload)
	}
}
