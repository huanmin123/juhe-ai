package announcements

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// recordingSink captures the operation-log entries Mount emits so tests can
// assert visibility/detail/changes against announcements.routes.ts.
type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}


var mustChangeFalse = false
func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) snapshot() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry(nil), s.entries...)
}

func (s *recordingSink) byAction(action string) []authsys.OperationLogEntry {
	var matched []authsys.OperationLogEntry
	for _, entry := range s.snapshot() {
		if entry.Action == action {
			matched = append(matched, entry)
		}
	}
	return matched
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	sink   *recordingSink
	db     *sql.DB
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
	// Sequential ids keep same-second creates distinct (the default id
	// generator is second-granular and the pagination tests create many
	// announcements in one burst).
	var sequence atomic.Int64
	store, err := NewStore(db, false, nil, func(prefix string) string {
		return fmt.Sprintf("%s_%06d", prefix, sequence.Add(1))
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	sink := &recordingSink{}
	deps.MountAuth(k, "lax", false)
	Mount(k, deps, store, sink)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, sink: sink, db: db, jar: map[string]string{}}
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
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
}

func (e *testEnv) createAnnouncement(t *testing.T, title, content, status string) (string, string) {
	t.Helper()
	body := `{"title":"` + title + `","content":"` + content + `"`
	if status != "" {
		body += `,"status":"` + status + `"`
	}
	body += `}`
	code, created := e.do(t, http.MethodPost, "/__aisys__/api/announcements", body)
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", title, code, created)
	}
	data := created["data"].(map[string]any)
	return data["id"].(string), data["revision"].(string)
}

func (e *testEnv) publishAnnouncement(t *testing.T, id, revision string) string {
	t.Helper()
	code, published := e.do(t, http.MethodPost, "/__aisys__/api/announcements/"+id+"/publish",
		`{"expectedRevision":"`+revision+`"}`)
	if code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, published)
	}
	return published["data"].(map[string]any)["revision"].(string)
}

func dataObject(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected object payload, got %#v", payload)
	}
	return data
}

func dataArray(t *testing.T, payload map[string]any) []any {
	t.Helper()
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("expected array payload, got %#v", payload)
	}
	return data
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
	newRevision := env.publishAnnouncement(t, id, revision)
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

	// Read tracking (my-announcements alternate path).
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
	republished := env.publishAnnouncement(t, id, rev2)

	// No-op patch (identical title) returns the current revision. A patch
	// without any change field is rejected like the Node refine.
	code, emptyPatch := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+republished+`"}`)
	if code != http.StatusBadRequest || emptyPatch["message"] != "公告参数无效" {
		t.Fatalf("empty patch must 400: %d %v", code, emptyPatch)
	}
	code, noOp := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+republished+`","title":"维护公告 v2"}`)
	if code != 200 || noOp["data"].(map[string]any)["revision"] != republished {
		t.Fatalf("no-op patch: %d %v", code, noOp)
	}

	code, page := env.do(t, http.MethodGet, "/__aisys__/api/announcements?page=1&pageSize=20", "")
	pageData := page["data"].(map[string]any)
	if code != 200 || len(pageData["items"].([]any)) != 1 {
		t.Fatalf("admin list: %d %v", code, page)
	}
	if pageData["page"] != float64(1) || pageData["pageSize"] != float64(20) {
		t.Fatalf("admin list normalized page values: %#v", pageData)
	}

	deleteCode, _ := env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+republished+`"}`)
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

	// createAnnouncementSchema validates the level enum at the route, so an
	// invalid level is 400 公告参数无效 (not the store-level 409).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements",
		`{"title":"标题","content":"内容","level":"bogus"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("bad level: %d %v", code, payload)
	}
}

// TestPublicRoutesContract covers the Node-contract public surface: normal
// users reach the three /announcements/public* routes (the ServeMux literal
// beats the admin-gated /announcements/{id}), and only published
// announcements appear with the public projections.
func TestPublicRoutesContract(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "boss", "boss-pass", "super_admin")
	id, revision := env.createAnnouncement(t, "公开契约", "公开正文内容", "")

	// Draft: public list/detail hide it.
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/announcements/public", "")
	if code != 200 || len(dataArray(t, list)) != 0 {
		t.Fatalf("draft hidden from public list: %d %v", code, list)
	}
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/announcements/public/"+id, "")
	if code != http.StatusNotFound || detail["message"] != "公告不存在" {
		t.Fatalf("draft public detail: %d %v", code, detail)
	}

	env.publishAnnouncement(t, id, revision)

	// Normal user: public routes pass, admin gate stays on /announcements*.
	env.login(t, "member", "member-pass", "user")
	code, list = env.do(t, http.MethodGet, "/__aisys__/api/announcements/public", "")
	if code != 200 {
		t.Fatalf("user public list: %d %v", code, list)
	}
	items := dataArray(t, list)
	if len(items) != 1 {
		t.Fatalf("public list items: %#v", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != id || item["title"] != "公开契约" || item["level"] != "info" {
		t.Fatalf("public list projection: %#v", item)
	}
	if _, hasReadAt := item["readAt"]; hasReadAt {
		t.Fatalf("unread item must omit readAt: %#v", item)
	}

	// Route precedence: /announcements/public/{id} never falls into the
	// admin {id} handler.
	code, detail = env.do(t, http.MethodGet, "/__aisys__/api/announcements/public/"+id, "")
	detailData := dataObject(t, detail)
	if code != 200 || detailData["content"] != "公开正文内容" || detailData["publishedAt"] == nil {
		t.Fatalf("user public detail: %d %v", code, detail)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+id, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin detail must stay gated for users: %d", code)
	}

	// Read through the Node-contract route; a replay is a no-op.
	code, read := env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read",
		`{"announcementIds":["`+id+`"]}`)
	if code != 200 || dataObject(t, read)["count"] != float64(1) {
		t.Fatalf("public read: %d %v", code, read)
	}
	code, replay := env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read",
		`{"announcementIds":["`+id+`"]}`)
	if code != 200 || dataObject(t, replay)["count"] != float64(0) {
		t.Fatalf("public read replay: %d %v", code, replay)
	}
	code, list = env.do(t, http.MethodGet, "/__aisys__/api/announcements/public", "")
	item = dataArray(t, list)[0].(map[string]any)
	if item["readAt"] == nil {
		t.Fatalf("readAt must appear after read: %#v", item)
	}
}

// TestPublicListLimitValidation walks the publicListQuerySchema matrix:
// 0/negative/non-numeric/fractional/empty/>30/repeated keys → 400, a valid
// range passes, a missing limit defaults, and scientific notation coerces
// like JavaScript Number() (1e1 → 10 items, not the 30 fallback).
func TestPublicListLimitValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "boss", "boss-pass", "super_admin")
	for i := 0; i < 12; i++ {
		id, revision := env.createAnnouncement(t, fmt.Sprintf("限宽%02d", i), "内容", "")
		env.publishAnnouncement(t, id, revision)
	}

	cases := []struct {
		query    string
		wantCode int
		wantLen  int
	}{
		{query: "", wantCode: 200, wantLen: 12}, // omitted limit → default 30
		{query: "?limit=1", wantCode: 200, wantLen: 1},
		{query: "?limit=12", wantCode: 200, wantLen: 12},
		{query: "?limit=30", wantCode: 200, wantLen: 12},
		{query: "?limit=0", wantCode: 400},
		{query: "?limit=-1", wantCode: 400},
		{query: "?limit=31", wantCode: 400},
		{query: "?limit=abc", wantCode: 400},
		{query: "?limit=1.5", wantCode: 400},
		{query: "?limit=", wantCode: 400},
		{query: "?limit=1e1", wantCode: 200, wantLen: 10}, // Number("1e1") = 10
		{query: "?limit=1&limit=2", wantCode: 400},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/announcements/public"+testCase.query, "")
		if code != testCase.wantCode {
			t.Fatalf("limit %q: status %d, want %d (%v)", testCase.query, code, testCase.wantCode, payload)
		}
		if testCase.wantCode == 200 && len(dataArray(t, payload)) != testCase.wantLen {
			t.Fatalf("limit %q: %d items, want %d", testCase.query, len(dataArray(t, payload)), testCase.wantLen)
		}
		if testCase.wantCode == 400 && payload["message"] != "公告查询参数无效" {
			t.Fatalf("limit %q: message %v", testCase.query, payload["message"])
		}
	}
}

// TestPublicReadValidation walks the readAnnouncementsSchema matrix: only
// announcementIds (string array ≤30, trim-nonempty elements) passes;
// missing/null/non-array/empty-element/unknown-field bodies render 400.
func TestPublicReadValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "boss", "boss-pass", "super_admin")
	id, revision := env.createAnnouncement(t, "已读校验", "内容", "")
	env.publishAnnouncement(t, id, revision)
	replayID, replayRevision := env.createAnnouncement(t, "已读校验二", "内容二", "")
	env.publishAnnouncement(t, replayID, replayRevision)

	thirtyOneIDs := `{"announcementIds":["` + strings.Join(peekIDs(31), `","`) + `"]}`
	thirtyIDs := `{"announcementIds":["` + strings.Join(peekIDs(30), `","`) + `"]}`
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantLen  int
	}{
		{name: "empty array is valid", body: `{"announcementIds":[]}`, wantCode: 200},
		{name: "trims and marks real id", body: `{"announcementIds":["  ` + id + `  "]}`, wantCode: 200, wantLen: 1},
		{name: "30 unknown ids ok", body: thirtyIDs, wantCode: 200},
		{name: "31 ids rejected", body: thirtyOneIDs, wantCode: 400},
		{name: "missing key", body: `{}`, wantCode: 400},
		{name: "null array", body: `{"announcementIds":null}`, wantCode: 400},
		{name: "non-array", body: `{"announcementIds":"x"}`, wantCode: 400},
		{name: "empty element", body: `{"announcementIds":[""]}`, wantCode: 400},
		{name: "blank element", body: `{"announcementIds":[" "]}`, wantCode: 400},
		{name: "non-string element", body: `{"announcementIds":[1]}`, wantCode: 400},
		{name: "unknown field", body: `{"announcementIds":[],"extra":true}`, wantCode: 400},
		{name: "non-object body", body: `[]`, wantCode: 400},
		{name: "no body", body: "", wantCode: 400},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read", testCase.body)
		if code != testCase.wantCode {
			t.Fatalf("%s: status %d, want %d (%v)", testCase.name, code, testCase.wantCode, payload)
		}
		if testCase.wantCode == 400 && payload["message"] != "公告已读参数无效" {
			t.Fatalf("%s: message %v", testCase.name, payload["message"])
		}
		if testCase.wantLen > 0 && dataObject(t, payload)["count"] != float64(testCase.wantLen) {
			t.Fatalf("%s: count %v, want %d", testCase.name, dataObject(t, payload)["count"], testCase.wantLen)
		}
	}

	// The validation matrix must not have written anything: reading the
	// second announcement still counts 1.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read",
		`{"announcementIds":["`+replayID+`"]}`)
	if code != 200 || dataObject(t, payload)["count"] != float64(1) {
		t.Fatalf("read after validation matrix: %d %v", code, payload)
	}
}

func peekIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("ann_missing_%02d", i)
	}
	return ids
}

// TestAdminListProjectionAndPaging covers the management list projection
// (contentPreview/contentTruncated/updatedByName/revision) and the
// normalized pagination contract (default pageSize 50, windowed page,
// pagedTotalUpperBound total/hasMore).
func TestAdminListProjectionAndPaging(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "boss", "boss-pass", "super_admin")

	shortID, _ := env.createAnnouncement(t, "短内容", "短正文", "")
	longContent := strings.Repeat("测", 300)
	longID, _ := env.createAnnouncement(t, "长内容", longContent, "")
	_, _ = env.createAnnouncement(t, "草稿", "草稿正文", "")

	code, page := env.do(t, http.MethodGet, "/__aisys__/api/announcements", "")
	if code != 200 {
		t.Fatalf("admin list: %d %v", code, page)
	}
	pageData := dataObject(t, page)
	if pageData["page"] != float64(1) || pageData["pageSize"] != float64(50) {
		t.Fatalf("default pagination must be page=1 pageSize=50: %#v", pageData)
	}
	if pageData["hasMore"] != false || pageData["total"] != float64(3) {
		t.Fatalf("single page metadata: %#v", pageData)
	}
	items := pageData["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items: %#v", items)
	}
	byID := map[string]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		byID[item["id"].(string)] = item
		if _, hasCreatedAt := item["createdAt"]; hasCreatedAt {
			t.Fatalf("list items must not carry createdAt (Node projection): %#v", item)
		}
		if _, hasEditVersion := item["editVersion"]; hasEditVersion {
			t.Fatalf("list items must not carry editVersion (Node projection): %#v", item)
		}
	}

	short := byID[shortID]
	if short["contentPreview"] != "短正文" || short["contentTruncated"] != false {
		t.Fatalf("short preview: %#v", short)
	}
	if short["updatedByName"] != "boss_name" || short["status"] != "draft" {
		t.Fatalf("short actor/status: %#v", short)
	}
	if _, hasPublishedAt := short["publishedAt"]; hasPublishedAt {
		t.Fatalf("draft must omit publishedAt: %#v", short)
	}
	long := byID[longID]
	preview := long["contentPreview"].(string)
	if utf16Length(preview) != 240+3 || !strings.HasSuffix(preview, "...") {
		t.Fatalf("long preview shape: %d %q", utf16Length(preview), preview)
	}
	if long["contentTruncated"] != true {
		t.Fatalf("long truncated flag: %#v", long)
	}

	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+shortID, "")
	if code != 200 || dataObject(t, detail)["revision"] != short["revision"] {
		t.Fatalf("list revision must equal edit detail revision: %d %v vs %#v", code, detail, short["revision"])
	}

	// pageSize=2 → hasMore + upper-bound total; page 2 carries the rest.
	code, page = env.do(t, http.MethodGet, "/__aisys__/api/announcements?pageSize=2", "")
	pageData = dataObject(t, page)
	if pageData["hasMore"] != true || pageData["total"] != float64(3) || pageData["pageSize"] != float64(2) {
		t.Fatalf("first page metadata: %#v", pageData)
	}
	if len(pageData["items"].([]any)) != 2 {
		t.Fatalf("first page items: %#v", pageData)
	}
	code, page = env.do(t, http.MethodGet, "/__aisys__/api/announcements?page=2&pageSize=2", "")
	pageData = dataObject(t, page)
	if pageData["hasMore"] != false || pageData["total"] != float64(3) {
		t.Fatalf("second page metadata: %#v", pageData)
	}
	if len(pageData["items"].([]any)) != 1 {
		t.Fatalf("second page items: %#v", pageData)
	}

	// Pages clamp into the 1001-row window like normalizeListPage.
	code, page = env.do(t, http.MethodGet, "/__aisys__/api/announcements?page=999&pageSize=50", "")
	pageData = dataObject(t, page)
	if code != 200 || pageData["page"] != float64(20) {
		t.Fatalf("window clamp: %d %#v", code, pageData)
	}

	// Validation matrix (adminListQuerySchema).
	for _, query := range []string{"?page=0", "?page=-1", "?page=abc", "?page=1.5", "?page=", "?pageSize=0", "?pageSize=101", "?pageSize=1.5", "?pageSize=", "?page=1&page=2"} {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/announcements"+query, "")
		if code != http.StatusBadRequest || payload["message"] != "公告查询参数无效" {
			t.Fatalf("query %q: %d %v", query, code, payload)
		}
	}
}

// TestCreateTimeColumns pins the fixed INSERT parameter list: booleans must
// never land in published_at/created_at, and drafts keep published_at NULL
// (announcement-management-write.repository.ts).
func TestCreateTimeColumns(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	draftID, draftRevision := env.createAnnouncement(t, "时间列草稿", "草稿", "")
	publishedID, publishedRevision := env.createAnnouncement(t, "时间列发布", "正文", "published")

	assertColumns := func(id, revision string, wantPublished bool) {
		t.Helper()
		var publishedAt sql.NullString
		var createdAt, updatedAt string
		if err := env.db.QueryRow(`SELECT published_at, created_at, updated_at FROM announcements WHERE id = ?`, id).
			Scan(&publishedAt, &createdAt, &updatedAt); err != nil {
			t.Fatal(err)
		}
		if wantPublished {
			if !publishedAt.Valid || publishedAt.String != revision {
				t.Fatalf("published create published_at = %#v, want revision %s", publishedAt, revision)
			}
		} else if publishedAt.Valid {
			t.Fatalf("draft published_at must be NULL, got %#v", publishedAt)
		}
		if createdAt != revision || updatedAt != revision {
			t.Fatalf("time columns = created_at %q updated_at %q, want revision %s", createdAt, updatedAt, revision)
		}
	}
	assertColumns(draftID, draftRevision, false)
	assertColumns(publishedID, publishedRevision, true)
}

// TestPatchNoOpSemantics mirrors patchAnnouncementForManagementAsync: an
// identical content/title/level/status patch is a no-op (no revision bump,
// no updated_at write), while any real change bumps the revision.
func TestPatchNoOpSemantics(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	id, revision := env.createAnnouncement(t, "幂等标题", "幂等正文", "")

	sameContent, payload := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+revision+`","content":"幂等正文"}`)
	if sameContent != 200 || dataObject(t, payload)["revision"] != revision {
		t.Fatalf("same-content patch must be a no-op: %d %v", sameContent, payload)
	}

	// The content change with identical whitespace-only difference trims to
	// the same normalized text and stays a no-op.
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+revision+`","content":"  幂等正文  "}`)
	if code != 200 || dataObject(t, payload)["revision"] != revision {
		t.Fatalf("trimmed same-content patch must be a no-op: %d %v", code, payload)
	}

	var updatedAt string
	if err := env.db.QueryRow(`SELECT updated_at FROM announcements WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != revision {
		t.Fatalf("no-op patch wrote updated_at %q, want %q", updatedAt, revision)
	}

	code, changed := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":"`+revision+`","content":"真正的新正文"}`)
	if code != 200 || dataObject(t, changed)["revision"] == revision {
		t.Fatalf("real change must bump revision: %d %v", code, changed)
	}
}

// TestStrictMutationValidation walks the create/patch/version strict zod
// schemas: unknown fields, non-string values, empty/oversized trimmed text
// and empty patches all render 400 with the route-specific message.
func TestStrictMutationValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	id, revision := env.createAnnouncement(t, "校验基线", "基线正文", "")

	longTitle := strings.Repeat("标", 121)
	okTitle := strings.Repeat("标", 120)
	longContent := strings.Repeat("容", 5001)
	okContent := strings.Repeat("容", 5000)

	// Each case carries a distinct content: the create route runs behind the
	// mutation guard, so identical fingerprints would collide with the
	// dedupe replay response instead of the schema rejection.
	createCases := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"title":"t","content":"c1","extra":1}`},
		{name: "non-string title", body: `{"title":123,"content":"c2"}`},
		{name: "null title", body: `{"title":null,"content":"c3"}`},
		{name: "empty title", body: `{"title":"  ","content":"c4"}`},
		{name: "overlong title", body: `{"title":"` + longTitle + `","content":"c5"}`},
		{name: "overlong content", body: `{"title":"t","content":"` + longContent + `"}`},
		{name: "null level", body: `{"title":"t","content":"c6","level":null}`},
		{name: "space-padded level", body: `{"title":"t","content":"c7","level":" info"}`},
		{name: "bad status", body: `{"title":"t","content":"c8","status":"gone"}`},
	}
	for _, testCase := range createCases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements", testCase.body)
		if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
			t.Fatalf("create %s: %d %v", testCase.name, code, payload)
		}
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements",
		`{"title":"`+okTitle+`","content":"`+okContent+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("boundary-length create must pass: %d %v", code, payload)
	}

	patchCases := []struct {
		name string
		body string
	}{
		{name: "empty patch", body: `{}`},
		{name: "revision only", body: `{"expectedRevision":"` + revision + `"}`},
		{name: "unknown field", body: `{"expectedRevision":"` + revision + `","title":"x","why":"no"}`},
		{name: "null title", body: `{"expectedRevision":"` + revision + `","title":null}`},
		{name: "empty title", body: `{"expectedRevision":"` + revision + `","title":" "}`},
		{name: "overlong title", body: `{"expectedRevision":"` + revision + `","title":"` + longTitle + `"}`},
		{name: "null content", body: `{"expectedRevision":"` + revision + `","content":null}`},
		{name: "missing revision", body: `{"title":"新标题"}`},
		{name: "blank revision", body: `{"expectedRevision":"  ","title":"新标题"}`},
		{name: "non-string revision", body: `{"expectedRevision":123,"title":"新标题"}`},
		{name: "bad level", body: `{"expectedRevision":"` + revision + `","level":"loud"}`},
		{name: "bad status", body: `{"expectedRevision":"` + revision + `","status":"gone"}`},
	}
	for _, testCase := range patchCases {
		code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id, testCase.body)
		if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
			t.Fatalf("patch %s: %d %v", testCase.name, code, payload)
		}
	}

	// expectedRevision trims like z.string().trim().min(1): a padded current
	// revision compares equal and succeeds; a padded wrong revision still
	// conflicts.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":" `+revision+` ","title":"新标题"}`)
	if code != http.StatusOK {
		t.Fatalf("padded current revision must compare equal after trim: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id,
		`{"expectedRevision":" `+revision+` ","title":"另一标题"}`)
	if code != http.StatusConflict {
		t.Fatalf("padded stale revision must conflict: %d %v", code, payload)
	}

	versionCases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantText string
	}{
		{name: "publish empty", method: http.MethodPost, path: "/__aisys__/api/announcements/" + id + "/publish", body: `{}`, wantText: "公告版本参数无效"},
		{name: "publish unknown field", method: http.MethodPost, path: "/__aisys__/api/announcements/" + id + "/publish", body: `{"expectedRevision":"x","extra":1}`, wantText: "公告版本参数无效"},
		{name: "publish non-string revision", method: http.MethodPost, path: "/__aisys__/api/announcements/" + id + "/publish", body: `{"expectedRevision":9}`, wantText: "公告版本参数无效"},
		{name: "publish blank revision", method: http.MethodPost, path: "/__aisys__/api/announcements/" + id + "/publish", body: `{"expectedRevision":" "}`, wantText: "公告版本参数无效"},
		{name: "unpublish unknown field", method: http.MethodPost, path: "/__aisys__/api/announcements/" + id + "/unpublish", body: `{"expectedRevision":"x","why":1}`, wantText: "公告版本参数无效"},
		{name: "delete empty", method: http.MethodDelete, path: "/__aisys__/api/announcements/" + id, body: `{}`, wantText: "公告版本参数无效"},
		{name: "delete unknown field", method: http.MethodDelete, path: "/__aisys__/api/announcements/" + id, body: `{"expectedRevision":"x","why":1}`, wantText: "公告版本参数无效"},
	}
	for _, testCase := range versionCases {
		code, payload = env.do(t, testCase.method, testCase.path, testCase.body)
		if code != http.StatusBadRequest || payload["message"] != testCase.wantText {
			t.Fatalf("%s: %d %v", testCase.name, code, payload)
		}
	}
}

// TestOperationLogContract pins the Node operation-log semantics:
// create/update/publish/unpublish/delete visibility, summaries, resource
// names, diff-style changes, and log-only-when-changed.
func TestOperationLogContract(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Create draft → admin_only/full.
	draftID, draftRevision := env.createAnnouncement(t, "日志草稿", "草稿正文", "")
	creates := env.sink.byAction("create")
	if len(creates) != 1 {
		t.Fatalf("create entries: %d", len(creates))
	}
	draftCreate := creates[0]
	if draftCreate.VisibilityScope != "admin_only" || draftCreate.DetailLevel != "full" {
		t.Fatalf("draft create visibility: %#v", draftCreate)
	}
	if draftCreate.Summary != "创建公告：日志草稿" || draftCreate.ResourceName != "日志草稿" {
		t.Fatalf("draft create summary/name: %#v", draftCreate)
	}
	if len(draftCreate.Changes) != 3 {
		t.Fatalf("create changes: %#v", draftCreate.Changes)
	}

	// Update with a real content change → log carries only the content diff.
	code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+draftID,
		`{"expectedRevision":"`+draftRevision+`","content":"新草稿正文"}`)
	if code != 200 {
		t.Fatalf("update: %d", code)
	}
	updates := env.sink.byAction("update")
	if len(updates) != 1 {
		t.Fatalf("update entries: %d", len(updates))
	}
	update := updates[0]
	if update.Summary != "更新公告：日志草稿" || update.ResourceName != "日志草稿" {
		t.Fatalf("update summary/name: %#v", update)
	}
	if update.VisibilityScope != "admin_only" || update.DetailLevel != "full" {
		t.Fatalf("draft update visibility: %#v", update)
	}
	if len(update.Changes) != 1 || update.Changes[0].Field != "content" ||
		update.Changes[0].Label != "内容" || update.Changes[0].Before != "草稿正文" ||
		update.Changes[0].After != "新草稿正文" {
		t.Fatalf("update changes: %#v", update.Changes)
	}

	// No-op update must not log (Node outcome.changed gate).
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+draftID, "")
	if code != 200 {
		t.Fatalf("detail: %d", code)
	}
	changedRevision := dataObject(t, detail)["revision"].(string)
	entryCount := len(env.sink.snapshot())
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+draftID,
		`{"expectedRevision":"`+changedRevision+`","content":"新草稿正文"}`)
	if code != 200 {
		t.Fatalf("no-op update: %d", code)
	}
	if len(env.sink.snapshot()) != entryCount {
		t.Fatalf("no-op update logged an entry: %#v", env.sink.snapshot()[entryCount:])
	}

	// Publish → all_users/summary with status + publishedAt changes.
	code, detail = env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+draftID, "")
	if code != 200 {
		t.Fatalf("detail before publish: %d", code)
	}
	publishedRevision := env.publishAnnouncement(t, draftID, dataObject(t, detail)["revision"].(string))
	publishes := env.sink.byAction("publish")
	if len(publishes) != 1 {
		t.Fatalf("publish entries: %d", len(publishes))
	}
	publish := publishes[0]
	if publish.VisibilityScope != "all_users" || publish.DetailLevel != "summary" {
		t.Fatalf("publish visibility: %#v", publish)
	}
	if publish.Summary != "发布公告：日志草稿" {
		t.Fatalf("publish summary: %#v", publish)
	}
	if len(publish.Changes) != 2 {
		t.Fatalf("publish changes: %#v", publish.Changes)
	}
	statusChange := publish.Changes[0]
	if statusChange.Field != "status" || statusChange.Before != "draft" || statusChange.After != "published" {
		t.Fatalf("publish status change: %#v", statusChange)
	}
	publishedAtChange := publish.Changes[1]
	if publishedAtChange.Field != "publishedAt" || publishedAtChange.Label != "发布时间" || publishedAtChange.After != publishedRevision {
		t.Fatalf("publish publishedAt change: %#v", publishedAtChange)
	}

	// Republish of an already-published announcement is a no-op → no log.
	env.publishAnnouncement(t, draftID, publishedRevision)
	if len(env.sink.byAction("publish")) != 1 {
		t.Fatal("republish must not log")
	}

	// Unpublish → all_users/summary with only the status change.
	unpublishRevision := func() string {
		code, detail := env.do(t, http.MethodGet, "/__aisys__/api/announcements/"+draftID, "")
		if code != 200 {
			t.Fatalf("detail: %d", code)
		}
		revision := dataObject(t, detail)["revision"].(string)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/announcements/"+draftID+"/unpublish",
			`{"expectedRevision":"`+revision+`"}`)
		if code != 200 {
			t.Fatalf("unpublish: %d %v", code, payload)
		}
		return payload["data"].(map[string]any)["revision"].(string)
	}()
	unpublishes := env.sink.byAction("unpublish")
	if len(unpublishes) != 1 {
		t.Fatalf("unpublish entries: %d", len(unpublishes))
	}
	unpublish := unpublishes[0]
	if unpublish.Summary != "下线公告：日志草稿" || unpublish.VisibilityScope != "all_users" {
		t.Fatalf("unpublish log: %#v", unpublish)
	}
	if len(unpublish.Changes) != 1 || unpublish.Changes[0].Field != "status" {
		t.Fatalf("unpublish changes: %#v", unpublish.Changes)
	}

	// A published create logs all_users/summary.
	publishedDirectID, publishedDirectRevision := env.createAnnouncement(t, "日志直发", "正文", "published")
	creates = env.sink.byAction("create")
	directCreate := creates[len(creates)-1]
	if directCreate.VisibilityScope != "all_users" || directCreate.DetailLevel != "summary" {
		t.Fatalf("published create visibility: %#v", directCreate)
	}
	if directCreate.Changes[1].After != "info" || directCreate.Changes[2].After != "published" {
		t.Fatalf("published create change defaults: %#v", directCreate.Changes)
	}

	// Delete keys visibility off the before status: deleting a published
	// announcement logs all_users/summary with the boolean deleted change.
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+publishedDirectID,
		`{"expectedRevision":"`+publishedDirectRevision+`"}`)
	if code != http.StatusNoContent {
		t.Fatalf("delete published: %d", code)
	}
	deletes := env.sink.byAction("delete")
	if len(deletes) != 1 {
		t.Fatalf("delete entries: %d", len(deletes))
	}
	publishedDelete := deletes[0]
	if publishedDelete.VisibilityScope != "all_users" || publishedDelete.DetailLevel != "summary" {
		t.Fatalf("published delete visibility: %#v", publishedDelete)
	}
	if publishedDelete.Summary != "删除公告：日志直发" || publishedDelete.ResourceName != "日志直发" {
		t.Fatalf("published delete summary: %#v", publishedDelete)
	}
	if len(publishedDelete.Changes) != 1 || publishedDelete.Changes[0].Field != "deleted" ||
		publishedDelete.Changes[0].Label != "删除状态" {
		t.Fatalf("delete change: %#v", publishedDelete.Changes)
	}
	if value := publishedDelete.Changes[0].AfterValue; value != true {
		t.Fatalf("delete change after value: %#v", value)
	}

	// Deleting an announcement that is no longer published (archived by the
	// unpublish above) logs admin_only/full.
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+draftID,
		`{"expectedRevision":"`+unpublishRevision+`"}`)
	if code != http.StatusNoContent {
		t.Fatalf("delete archived: %d", code)
	}
	deletes = env.sink.byAction("delete")
	if len(deletes) != 2 {
		t.Fatalf("delete entries: %d", len(deletes))
	}
	archivedDelete := deletes[1]
	if archivedDelete.VisibilityScope != "admin_only" || archivedDelete.DetailLevel != "full" {
		t.Fatalf("archived delete visibility: %#v", archivedDelete)
	}
}

// TestMyAnnouncementsCompatibility pins the Go-introduced alternate paths:
// they stay registered and usable even though the Node-contract public
// routes now cover the same surface.
func TestMyAnnouncementsCompatibility(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	id, revision := env.createAnnouncement(t, "兼容路径", "兼容正文", "")
	env.publishAnnouncement(t, id, revision)

	env.login(t, "plain", "plain-pass", "user")
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/my-announcements", "")
	if code != 200 || len(dataArray(t, list)) != 1 {
		t.Fatalf("my-announcements list: %d %v", code, list)
	}
	code, read := env.do(t, http.MethodPost, "/__aisys__/api/my-announcements/read",
		`{"announcementIds":["`+id+`"]}`)
	if code != 200 || dataObject(t, read)["count"] != float64(1) {
		t.Fatalf("my-announcements read: %d %v", code, read)
	}
}
