package announcements

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var w9dStoreSeq int64

// w9dStore 构造独立命名的内存库 store，避免共享库名重复建表。
func w9dStore(t *testing.T) *Store {
	t.Helper()
	name := fmt.Sprintf("w9d-ann-http-%d", atomic.AddInt64(&w9dStoreSeq, 1))
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, display_name TEXT)`,
		`CREATE TABLE announcements (id TEXT PRIMARY KEY,title TEXT NOT NULL,content TEXT NOT NULL,level TEXT NOT NULL,status TEXT NOT NULL,created_by TEXT NOT NULL,updated_by TEXT,published_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE announcement_reads (announcement_id TEXT NOT NULL,system_account_id TEXT NOT NULL,read_at TEXT NOT NULL,PRIMARY KEY (announcement_id,system_account_id))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	return store
}

// ---------------------------------------------------------------------------
// w9d：HTTP 适配层错误臂。基于真实 store + httptest 请求逐臂触达。
// ---------------------------------------------------------------------------

func w9dHandler(t *testing.T, resolver ActorResolver) *HTTPHandler {
	t.Helper()
	service, err := NewService(w9dStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &HTTPHandler{Service: service, ResolveActor: resolver}
}

func w9dDo(h *HTTPHandler, method, target, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func w9dBrokenResolver(_ context.Context, _ *http.Request) (Actor, error) {
	return Actor{}, errors.New("resolver down")
}

func w9dUserResolver(_ context.Context, _ *http.Request) (Actor, error) {
	return Actor{SystemAccountID: "user", Role: "user"}, nil
}

func TestW9DHTTPAuthArms(t *testing.T) {
	// publicActor 非 Unauthenticated 错误 → getPublic writeError。
	h := w9dHandler(t, w9dBrokenResolver)
	if rec := w9dDo(h, http.MethodGet, "/public/a1", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("getPublic broken resolver status=%d", rec.Code)
	}
	// 非 admin 访问管理端点。
	uh := w9dHandler(t, w9dUserResolver)
	if rec := w9dDo(uh, http.MethodGet, "/a1", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("getAdmin user status=%d", rec.Code)
	}
	if rec := w9dDo(uh, http.MethodPost, "/", `{"title":"t","content":"c"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("create user status=%d", rec.Code)
	}
	if rec := w9dDo(uh, http.MethodPatch, "/a1", `{"expectedRevision":"r","title":"t"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("patch user status=%d", rec.Code)
	}
	if rec := w9dDo(uh, http.MethodPost, "/a1/publish", `{"expectedRevision":"r"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("publish user status=%d", rec.Code)
	}
	if rec := w9dDo(uh, http.MethodDelete, "/a1", `{"expectedRevision":"r"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("delete user status=%d", rec.Code)
	}
}

func TestW9DHTTPMutationServiceErrors(t *testing.T) {
	// Create：owner gate 半开 → Service err（独立 store 切换门禁）。
	blockedService, err := NewService(w9dStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	blockedService.store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	blocked := &HTTPHandler{Service: blockedService, ResolveActor: adminResolver}
	if rec := w9dDo(blocked, http.MethodPost, "/", `{"title":"t","content":"c"}`); rec.Code != http.StatusForbidden && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("blocked create status=%d body=%s", rec.Code, rec.Body.String())
	}
	h := w9dHandler(t, adminResolver)
	// 先创建一条公告用于冲突臂。
	rec := w9dDo(h, http.MethodPost, "/", `{"title":"hello","content":"body"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	receipt := envelope.Data
	// Patch：revision 冲突 → writeError；缺失 → outcome==nil → 404。
	if rec := w9dDo(h, http.MethodPatch, "/missing", `{"expectedRevision":"r","title":"t"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing status=%d body=%s", rec.Code, rec.Body.String())
	}
	conflict := fmt.Sprintf(`{"expectedRevision":"stale","title":"t2"}`)
	if rec := w9dDo(h, http.MethodPatch, "/"+receipt.ID, conflict); rec.Code != http.StatusConflict {
		t.Fatalf("patch conflict status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Publish：缺失 → outcome==nil → 404。
	if rec := w9dDo(h, http.MethodPost, "/missing/publish", `{"expectedRevision":"r"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("publish missing status=%d", rec.Code)
	}
	// Delete：revision 冲突 → writeError；缺失 → 幂等 200。
	if rec := w9dDo(h, http.MethodDelete, "/"+receipt.ID, `{"expectedRevision":"stale"}`); rec.Code != http.StatusConflict {
		t.Fatalf("delete conflict status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := w9dDo(h, http.MethodDelete, "/missing", `{"expectedRevision":"r"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status=%d", rec.Code)
	}
}

func TestW9DHTTPRevisionDecodeAndAnonymization(t *testing.T) {
	h := w9dHandler(t, adminResolver)
	// decodeExpectedRevision：decodeObject 失败 / 空 revision。
	if rec := w9dDo(h, http.MethodPost, "/a1/publish", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("publish broken json status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := w9dDo(h, http.MethodPost, "/a1/publish", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("publish empty revision status=%d", rec.Code)
	}
	// publicActor：空 SystemAccountID → 匿名化。
	blank := w9dHandler(t, func(_ context.Context, _ *http.Request) (Actor, error) {
		return Actor{SystemAccountID: "   ", Role: "admin"}, nil
	})
	if rec := w9dDo(blank, http.MethodGet, "/public", ""); rec.Code != http.StatusOK {
		t.Fatalf("public blank actor status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 合法 limit 参数。
	if rec := w9dDo(h, http.MethodGet, "/public?limit=5", ""); rec.Code != http.StatusOK {
		t.Fatalf("public limit status=%d", rec.Code)
	}
}

func TestW9DHTTPRequireActorAndDecodeJSON(t *testing.T) {
	svc, err := NewService(w9dStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// ResolveActor 为 nil → requireActor 直接 ErrOwnerGate。
	bare := &HTTPHandler{Service: svc}
	if _, err := bare.requireActor(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil resolver actor err=%v", err)
	}
	h := w9dHandler(t, adminResolver)
	rec := httptest.NewRecorder()
	var dst struct {
		Title string `json:"title"`
	}
	// 合法单值 JSON。
	if err := h.decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"a"}`)), &dst); err != nil || dst.Title != "a" {
		t.Fatalf("decodeJSON ok err=%v title=%q", err, dst.Title)
	}
	// 非法 JSON。
	if err := h.decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`)), &dst); err == nil {
		t.Fatal("broken json must fail")
	}
	// 多 JSON 值。
	if err := h.decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"a"} {"title":"b"}`)), &dst); err == nil {
		t.Fatal("multiple values must fail")
	}
	// 尾部垃圾。
	if err := h.decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"a"} garbage`)), &dst); err == nil {
		t.Fatal("trailing garbage must fail")
	}
}
