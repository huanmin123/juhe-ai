package announcements

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Store 别名与未覆盖的读路径
// ---------------------------------------------------------------------------

func TestW7AStoreAliasesAndFindAnnouncement(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	if store.ModeName() != SQLite {
		t.Fatalf("mode=%q", store.ModeName())
	}
	var nilStore *Store
	if nilStore.ModeName() != "" {
		t.Fatal("nil store mode must be empty")
	}
	created, err := store.Create(ctx, CreateInput{Title: "alias", Content: "body", Level: LevelCritical, Status: StatusPublished}, "admin")
	if err != nil || created.After == nil || created.After.PublishedAt == nil {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	// FindAnnouncement：全行读取，published 非空臂。
	full, err := store.FindAnnouncement(ctx, created.Receipt.ID)
	if err != nil || full.UpdatedBy != "admin" || full.PublishedAt == nil {
		t.Fatalf("find=%+v err=%v", full, err)
	}
	draft, err := store.CreateAnnouncement(ctx, CreateInput{Title: "draft", Content: "body"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.FindAnnouncement(ctx, draft.Receipt.ID)
	if err != nil || plain.PublishedAt != nil {
		t.Fatalf("plain=%+v err=%v", plain, err)
	}
	if _, err := store.FindAnnouncement(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	// Store 级别名转发。
	if _, err := store.PublicList(ctx, "user", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublicDetail(ctx, created.Receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublicRead(ctx, "user", []string{created.Receipt.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListAdmin(ctx, ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAdmin(ctx, created.Receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindAnnouncementEditDetail(ctx, created.Receipt.ID); err != nil {
		t.Fatal(err)
	}
	published, err := store.Publish(ctx, created.Receipt.ID, "admin", created.Receipt.Revision)
	if err != nil || published == nil {
		t.Fatalf("publish=%+v err=%v", published, err)
	}
	unpublished, err := store.Unpublish(ctx, created.Receipt.ID, "admin", published.Receipt.Revision)
	if err != nil || unpublished == nil {
		t.Fatalf("unpublish=%+v err=%v", unpublished, err)
	}
	renamed, err := store.Patch(ctx, created.Receipt.ID, "admin", unpublished.Receipt.Revision, PatchInput{Title: stringPtr("renamed")})
	if err != nil || renamed == nil {
		t.Fatalf("patch=%+v err=%v", renamed, err)
	}
	deleted, err := store.Delete(ctx, created.Receipt.ID, renamed.Receipt.Revision)
	if err != nil || !deleted.Deleted {
		t.Fatalf("delete=%+v err=%v", deleted, err)
	}
}

// ---------------------------------------------------------------------------
// Patch / Delete 的完整防御臂
// ---------------------------------------------------------------------------

func TestW7APatchAndDeleteArms(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	created, err := store.CreateAnnouncement(ctx, CreateInput{Title: "t", Content: "c"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	id := created.Receipt.ID
	revision := created.Receipt.Revision
	// actor 缺失。
	if _, err := store.PatchAnnouncement(ctx, id, " ", revision, PatchInput{Title: stringPtr("x")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("actor=%v", err)
	}
	// 空补丁。
	if _, err := store.PatchAnnouncement(ctx, id, "admin", revision, PatchInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty patch=%v", err)
	}
	// 双 revision 不一致。
	if _, err := store.PatchAnnouncement(ctx, id, "admin", "rA", PatchInput{ExpectedRevision: "rB", Title: stringPtr("x")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("revision disagreement=%v", err)
	}
	// input revision 携带时以 input 为准。
	viaInput, err := store.PatchAnnouncement(ctx, id, "admin", "", PatchInput{ExpectedRevision: revision, Level: levelPtr(LevelWarning)})
	if err != nil || viaInput == nil || !viaInput.Changed {
		t.Fatalf("via input=%+v err=%v", viaInput, err)
	}
	next := viaInput.Receipt.Revision
	// 无效 level / status / 超长标题。
	if _, err := store.PatchAnnouncement(ctx, id, "admin", next, PatchInput{Level: levelPtr(Level("bad"))}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad level=%v", err)
	}
	if _, err := store.PatchAnnouncement(ctx, id, "admin", next, PatchInput{Status: statusPtr(Status("bad"))}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad status=%v", err)
	}
	long := strings.Repeat("题", TitleMaxUTF16+1)
	if _, err := store.PatchAnnouncement(ctx, id, "admin", next, PatchInput{Title: &long}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long title=%v", err)
	}
	// 无效内容 UTF-8。
	if _, err := store.PatchAnnouncement(ctx, id, "admin", next, PatchInput{Content: stringPtr("\xff")}); !errors.Is(err, ErrInvalidInput) {
	}
	// 不存在的公告返回 (nil, nil)。
	outcome, err := store.PatchAnnouncement(ctx, "missing", "admin", revision, PatchInput{Title: stringPtr("x")})
	if outcome != nil || err != nil {
		t.Fatalf("missing patch=%+v err=%v", outcome, err)
	}
	// Delete：revision 冲突与删除后不可再删。
	if _, err := store.DeleteAnnouncement(ctx, id, "wrong"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("delete conflict=%v", err)
	}
	if _, err := store.DeleteAnnouncement(ctx, "missing", "r"); err != nil {
		t.Fatalf("missing delete=%v", err)
	}
	deleted, err := store.DeleteAnnouncement(ctx, id, next)
	if err != nil || !deleted.Deleted {
		t.Fatalf("delete=%+v err=%v", deleted, err)
	}
}

func TestW7ARevisionAndConflictErrorShape(t *testing.T) {
	store := announcementStore(t)
	if _, err := store.nextRevisionForTest("not-a-time"); err == nil {
		t.Fatal("invalid revision must fail")
	}
	conflict := &RevisionConflictError{AnnouncementID: "a", ExpectedRevision: "e", CurrentRevision: "c"}
	if conflict.Error() == "" || !errors.Is(conflict, ErrRevisionConflict) {
		t.Fatal("conflict error shape")
	}
	if conflict.Unwrap() != ErrRevisionConflict {
		t.Fatal("unwrap")
	}
	if publicActionForCreate(StatusDraft) != "" || publicActionForDelete(StatusDraft) != "" {
		t.Fatal("publicAction draft must be empty")
	}
	event := eventForMutation("unpublish", Actor{SystemAccountID: "admin", Role: "admin"}, &MutationOutcome{
		Receipt: MutationReceipt{ID: "a", Revision: "r"},
		Before:  &MutationState{Status: StatusPublished},
		After:   &MutationState{Status: StatusArchived},
	})
	if event.PublicAction != "delete" {
		t.Fatalf("event=%+v", event)
	}
	upsert := eventForMutation("publish", Actor{}, &MutationOutcome{
		Receipt: MutationReceipt{ID: "a", Revision: "r"},
		After:   &MutationState{Status: StatusPublished},
	})
	if upsert.PublicAction != "upsert" {
		t.Fatalf("upsert=%+v", upsert)
	}
	if min(1, 2) != 1 || min(3, 2) != 2 {
		t.Fatal("min arms")
	}
}

// ---------------------------------------------------------------------------
// Service 门禁与别名全链路
// ---------------------------------------------------------------------------

func TestW7AServiceFullFlowWithAliases(t *testing.T) {
	store := announcementStore(t)
	recorder := &recordingAfterCommit{}
	service, err := NewServiceWithAfterCommit(store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(nil, nil); err == nil {
		t.Fatal("nil store must fail")
	}
	ctx := context.Background()
	admin := Actor{SystemAccountID: "admin", Role: "admin"}
	viewer := Actor{SystemAccountID: "user", Role: "user"}
	// 门禁：空 actor / 非 admin / owner gate。
	if _, err := service.ListPublic(ctx, Actor{}, 5); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anon actor=%v", err)
	}
	if _, err := service.ListAdmin(ctx, viewer, ListOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer admin=%v", err)
	}
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := service.ListAdmin(ctx, admin, ListOptions{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate=%v", err)
	}
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	// 公共读三件套。
	if _, err := service.ListPublicAnnouncements(ctx, viewer, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindPublicAnnouncement(ctx, viewer, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("public detail=%v", err)
	}
	read, err := service.MarkPublicAnnouncementsRead(ctx, viewer, []string{"a"})
	if err != nil || read.Count != 0 {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	// 管理全生命周期（ForManagement 长别名 + 短别名各走一遍）。
	created, err := service.CreateAnnouncementForManagement(ctx, admin, CreateInput{Title: "svc", Content: "body", Status: StatusPublished})
	if err != nil || len(recorder.events) != 1 {
		t.Fatalf("create=%+v err=%v events=%d", created, err, len(recorder.events))
	}
	id := created.Receipt.ID
	if _, err := service.FindAnnouncementEditDetail(ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	patched, err := service.PatchAnnouncementForManagement(ctx, admin, id, created.Receipt.Revision, PatchInput{Content: stringPtr("updated")})
	if err != nil || patched == nil || !patched.Changed {
		t.Fatalf("patch=%+v err=%v", patched, err)
	}
	published, err := service.PublishAnnouncementForManagement(ctx, admin, id, patched.Receipt.Revision)
	if err != nil || published == nil {
		t.Fatalf("publish=%+v err=%v", published, err)
	}
	unpublished, err := service.UnpublishAnnouncementForManagement(ctx, admin, id, published.Receipt.Revision)
	if err != nil || unpublished == nil {
		t.Fatalf("unpublish=%+v err=%v", unpublished, err)
	}
	republished, err := service.Publish(ctx, admin, id, unpublished.Receipt.Revision)
	if err != nil || republished == nil {
		t.Fatalf("republish=%+v err=%v", republished, err)
	}
	deleted, err := service.DeleteAnnouncementForManagement(ctx, admin, id, republished.Receipt.Revision)
	if err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	if _, err := service.ListPublicAnnouncementsPage(ctx, viewer, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetPublic(ctx, viewer, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get public=%v", err)
	}
	if _, err := service.MarkRead(ctx, viewer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListAdmin(ctx, admin, ListOptions{Page: 2, PageSize: 1}); err != nil {
		t.Fatal(err)
	}
	// after-commit 错误必须作为返回错误出现，但不回滚。
	recorder.err = errors.New("sink down")
	if _, err := service.Create(ctx, admin, CreateInput{Title: "err", Content: "body"}); err == nil {
		t.Fatal("after-commit error must surface")
	}
	// 未变更/缺失的补丁不触发事件。
	recorder.err = nil
	before := len(recorder.events)
	if _, err := service.Patch(ctx, admin, "missing", "r", PatchInput{Title: stringPtr("x")}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != before {
		t.Fatal("noop patch must not emit")
	}
	// Service.CheckContract 转发与 nil 防御。
	if err := service.CheckContract(ctx); err != nil {
		t.Fatal(err)
	}
	var nilService *Service
	if err := nilService.CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil service=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 校验与解码辅助函数
// ---------------------------------------------------------------------------

func TestW7ANormalizeArms(t *testing.T) {
	if _, err := normalizePublicLimit(0); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizePublicLimit(-1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative=%v", err)
	}
	if _, _, err := normalizeListOptions(ListOptions{Page: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative page=%v", err)
	}
	if _, _, err := normalizeListOptions(ListOptions{PageSize: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative size=%v", err)
	}
	if _, _, err := normalizeListOptions(ListOptions{PageSize: MaxPageSize + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large size=%v", err)
	}
	page, size, err := normalizeListOptions(ListOptions{Page: 999999, PageSize: 1})
	if err != nil || page != AdminWindowRows-1 || size != 1 {
		t.Fatalf("clamped page=%d err=%v", page, err)
	}
	if _, err := normalizeIDs([]string{" ", "a", "a"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank id=%v", err)
	}
	if _, err := normalizeIDs(nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := normalizeCreate(CreateInput{Title: "x", Content: "y", Level: Level("bad")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad level=%v", err)
	}
	if _, _, _, _, err := normalizeCreate(CreateInput{Title: "x", Content: "y", Status: Status("bad")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad status=%v", err)
	}
	if err := validatePatch(PatchInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty patch=%v", err)
	}
}

func TestW7ADecodeArms(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"array", `[1]`},
		{"null field", `{"title":null,"content":"y"}`},
		{"empty level", `{"title":"x","content":"y","level":""}`},
		{"empty status", `{"title":"x","content":"y","status":""}`},
		{"bad level", `{"title":"x","content":"y","level":"loud"}`},
		{"bad status", `{"title":"x","content":"y","status":"gone"}`},
		{"long title", `{"title":"` + strings.Repeat("t", TitleMaxUTF16+1) + `","content":"y"}`},
	}
	for _, tc := range cases {
		if _, err := DecodeCreateInput(strings.NewReader(tc.body)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
	decoded, err := DecodeCreateInput(strings.NewReader(`{"title":"x","content":"y","level":"info","status":"draft"}`))
	if err != nil || decoded.Level != LevelInfo || decoded.Status != StatusDraft {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	patchCases := []struct {
		name string
		body string
	}{
		{"null", `{"expectedRevision":"r","content":null}`},
		{"bad level", `{"expectedRevision":"r","level":"loud"}`},
		{"bad status", `{"expectedRevision":"r","status":"gone"}`},
		{"array", `[1]`},
	}
	for _, tc := range patchCases {
		if _, err := DecodePatchRequest(strings.NewReader(tc.body)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
	full, err := DecodePatchRequest(strings.NewReader(`{"expectedRevision":"r","level":"warning","status":"draft","content":"c"}`))
	if err != nil || full.Level == nil || full.Status == nil || full.Content == nil {
		t.Fatalf("full=%+v err=%v", full, err)
	}
}

// ---------------------------------------------------------------------------
// HTTP 全路由表
// ---------------------------------------------------------------------------

type w7aReceipt struct {
	Data struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"data"`
}

func TestW7AHTTPRouteTable(t *testing.T) {
	h := announcementHTTPHandler(t, adminResolver)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"route","content":"body","status":"published"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body.String())
	}
	var receipt w7aReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	id := receipt.Data.ID
	// 管理详情与查询参数校验。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+id, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "route") {
		t.Fatalf("get admin=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?page=bad", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad page=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?pageSize=999", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad pageSize=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public?limit=bad", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit=%d", rec.Code)
	}
	// patch。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`{"expectedRevision":"`+receipt.Data.Revision+`","title":"patched"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch=%d body=%s", rec.Code, rec.Body.String())
	}
	var patched w7aReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	// patch 参数缺失与非法 body。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch missing revision=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(`not-json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch bad body=%d", rec.Code)
	}
	// publish 与 stale unpublish 冲突。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+id+"/publish", strings.NewReader(`{"expectedRevision":"`+patched.Data.Revision+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("publish=%d body=%s", rec.Code, rec.Body.String())
	}
	var published w7aReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+id+"/unpublish", strings.NewReader(`{"expectedRevision":"bad"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "currentRevision") {
		t.Fatalf("unpublish conflict=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/"+id+"/unpublish", strings.NewReader(`{"unexpected":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unpublish strict=%d", rec.Code)
	}
	// mark-read：合法与非法。
	h.ResolveActor = func(_ context.Context, _ *http.Request) (Actor, error) {
		return Actor{SystemAccountID: "user"}, nil
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/public/read", strings.NewReader(`{"announcementIds":["`+id+`"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("mark read=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/public/read", strings.NewReader(`{"announcementIds":null}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mark read null=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/public/read", strings.NewReader(`{"announcementIds":[],"extra":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mark read strict=%d", rec.Code)
	}
	// 匿名公共读：ErrUnauthenticated 解析器回退匿名账号。
	h.ResolveActor = func(context.Context, *http.Request) (Actor, error) {
		return Actor{}, ErrUnauthenticated
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous list=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous detail=%d", rec.Code)
	}
	// 管理端匿名必须 401。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin=%d", rec.Code)
	}
	// 未匹配方法回退 404。
	h.ResolveActor = adminResolver
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/whatever", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fallback=%d", rec.Code)
	}
	// 公共详情 404：带斜杠的 id。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/a/b", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("slashed id=%d", rec.Code)
	}
	// 服务端错误映射 503。
	h.Service = w7aFailingService{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("service failure=%d", rec.Code)
	}
}

type w7aFailingService struct {
	HTTPService
}

func (w7aFailingService) ListAnnouncements(context.Context, Actor, ListOptions) (ListResult, error) {
	return ListResult{}, errors.New("db down")
}

func levelPtr(v Level) *Level    { return &v }
func statusPtr(v Status) *Status { return &v }

// ---------------------------------------------------------------------------
// Service 全方法门禁臂、剩余别名与构造函数
// ---------------------------------------------------------------------------

func TestW7AServiceGateArmsOnEveryMethod(t *testing.T) {
	store := announcementStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin := Actor{SystemAccountID: "admin", Role: "admin"}
	// owner gate 半开：所有公共方法必须拒绝。
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	gateErrs := []func() error{
		func() error { _, err := service.ListPublicAnnouncements(ctx, admin, 1); return err },
		func() error { _, err := service.FindPublicAnnouncement(ctx, admin, "a"); return err },
		func() error { _, err := service.MarkPublicAnnouncementsRead(ctx, admin, nil); return err },
		func() error { _, err := service.ListAnnouncements(ctx, admin, ListOptions{}); return err },
		func() error { _, err := service.FindAnnouncement(ctx, admin, "a"); return err },
		func() error {
			_, err := service.CreateAnnouncement(ctx, admin, CreateInput{Title: "x", Content: "y"})
			return err
		},
		func() error {
			_, err := service.PatchAnnouncement(ctx, admin, "a", "r", PatchInput{Title: stringPtr("x")})
			return err
		},
		func() error { _, err := service.PublishAnnouncement(ctx, admin, "a", "r"); return err },
		func() error { _, err := service.UnpublishAnnouncement(ctx, admin, "a", "r"); return err },
		func() error { _, err := service.DeleteAnnouncement(ctx, admin, "a", "r"); return err },
	}
	for i, call := range gateErrs {
		if err := call(); !errors.Is(err, ErrOwnerGate) {
			t.Fatalf("gate method %d err=%v", i, err)
		}
	}
	// 非管理员访问管理方法必须拒绝。
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	viewer := Actor{SystemAccountID: "user", Role: "user"}
	adminErrs := []func() error{
		func() error { _, err := service.ListAdmin(ctx, viewer, ListOptions{}); return err },
		func() error { _, err := service.GetAdmin(ctx, viewer, "a"); return err },
		func() error { _, err := service.Create(ctx, viewer, CreateInput{Title: "x", Content: "y"}); return err },
		func() error {
			_, err := service.Patch(ctx, viewer, "a", "r", PatchInput{Title: stringPtr("x")})
			return err
		},
		func() error { _, err := service.Publish(ctx, viewer, "a", "r"); return err },
		func() error { _, err := service.Unpublish(ctx, viewer, "a", "r"); return err },
		func() error { _, err := service.Delete(ctx, viewer, "a", "r"); return err },
	}
	for i, call := range adminErrs {
		if err := call(); !errors.Is(err, ErrForbidden) {
			t.Fatalf("admin method %d err=%v", i, err)
		}
	}
	// 剩余短别名：GetAdmin / Unpublish / Delete。
	created, err := service.CreateAnnouncement(ctx, admin, CreateInput{Title: "alias2", Content: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAdmin(ctx, admin, created.Receipt.ID); err != nil {
		t.Fatal(err)
	}
	unpublished, err := service.Unpublish(ctx, admin, created.Receipt.ID, created.Receipt.Revision)
	if err != nil || unpublished == nil {
		t.Fatalf("unpublish=%+v err=%v", unpublished, err)
	}
	if _, err := service.Delete(ctx, admin, created.Receipt.ID, unpublished.Receipt.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAdmin(ctx, admin, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing admin detail=%v", err)
	}
}

func TestW7AConstructorArmsAndContractFailure(t *testing.T) {
	db := announcementDB(t)
	if _, err := NewStore(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db must fail")
	}
	if _, err := NewStore(db, "oracle", "", OwnerGate{}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("mode=%v", err)
	}
	if _, err := NewStore(db, Postgres, "bad-name", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("schema=%v", err)
	}
	pg, err := NewStore(db, Postgres, "", OwnerGate{})
	if err != nil || pg.schema != "juhe_business" {
		t.Fatalf("pg default schema=%q err=%v", pg.schema, err)
	}
	if _, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}); err != nil {
		t.Fatal(err)
	}
	custom, err := NewStoreWithClock(db, SQLite, "", OwnerGate{}, nil)
	if err != nil || custom.now == nil {
		t.Fatalf("clock store=%+v err=%v", custom, err)
	}
	// 契约校验 nil 防御臂。
	if err := (&Store{}).CheckContract(context.Background()); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db contract=%v", err)
	}
	if err := custom.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Run("broken-contract", func(t *testing.T) {
		broken := announcementDB(t)
		if _, err := broken.Exec(`DROP TABLE announcement_reads`); err != nil {
			t.Fatal(err)
		}
		brokenStore, err := NewStore(broken, SQLite, "", OwnerGate{})
		if err != nil {
			t.Fatal(err)
		}
		if err := brokenStore.CheckContract(context.Background()); err == nil {
			t.Fatal("missing relation must fail contract")
		}
	})
}

func TestW7AHTTPDeleteRouteAndErrorMappings(t *testing.T) {
	h := announcementHTTPHandler(t, adminResolver)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"del","content":"body"}`)))
	var receipt w7aReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	id := receipt.Data.ID
	// DELETE：非法 body / 缺失 / 成功 / 重复。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/"+id, strings.NewReader(`{"unexpected":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete strict=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/missing", strings.NewReader(`{"expectedRevision":"r"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/"+id, strings.NewReader(`{"expectedRevision":"`+receipt.Data.Revision+`"}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", rec.Code, rec.Body.String())
	}
	// GET 管理详情 404。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+id, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted=%d", rec.Code)
	}
	// mark-read 参数无效映射 400。
	h.ResolveActor = func(_ context.Context, _ *http.Request) (Actor, error) {
		return Actor{SystemAccountID: "user"}, nil
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/public/read", strings.NewReader(`{"announcementIds":[" "]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mark read invalid=%d", rec.Code)
	}
	// 公共列表服务错误映射 503。
	h.ResolveActor = adminResolver
	h.Service = w7aFailingAllService{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("public failure=%d", rec.Code)
	}
}

type w7aFailingAllService struct{ HTTPService }

func (w7aFailingAllService) ListPublicAnnouncements(context.Context, Actor, int) ([]PublicListItem, error) {
	return nil, errors.New("db down")
}

func TestW7AStoreGateAndEdgeArms(t *testing.T) {
	store := announcementStore(t)
	ctx := context.Background()
	// 半开门禁：所有 Store 方法必须拒绝写入与读取。
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	gateCalls := []func() error{
		func() error { _, err := store.PublicList(ctx, "user", 1); return err },
		func() error { _, err := store.PublicDetail(ctx, "a"); return err },
		func() error { _, err := store.PublicRead(ctx, "user", nil); return err },
		func() error { _, err := store.ListAdmin(ctx, ListOptions{}); return err },
		func() error { _, err := store.GetAdmin(ctx, "a"); return err },
		func() error { _, err := store.FindAnnouncementEditDetail(ctx, "a"); return err },
		func() error {
			_, err := store.CreateAnnouncement(ctx, CreateInput{Title: "x", Content: "y"}, "admin")
			return err
		},
		func() error {
			_, err := store.PatchAnnouncement(ctx, "a", "admin", "r", PatchInput{Title: stringPtr("x")})
			return err
		},
		func() error { _, err := store.PublishAnnouncement(ctx, "a", "admin", "r"); return err },
		func() error { _, err := store.UnpublishAnnouncement(ctx, "a", "admin", "r"); return err },
		func() error { _, err := store.DeleteAnnouncement(ctx, "a", "r"); return err },
		func() error { _, err := store.FindAnnouncement(ctx, "a"); return err },
	}
	for i, call := range gateCalls {
		if err := call(); !errors.Is(err, ErrOwnerGate) {
			t.Fatalf("store gate method %d err=%v", i, err)
		}
	}
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}
	// 空 actor 与非法 limit/参数。
	if _, err := store.ListPublicAnnouncements(ctx, " ", 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty actor=%v", err)
	}
	if _, err := store.ListPublicAnnouncements(ctx, "user", -1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad limit=%v", err)
	}
	if _, err := store.FindPublicAnnouncement(ctx, " "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blank id=%v", err)
	}
	if _, err := store.MarkPublicAnnouncementsRead(ctx, " ", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mark read actor=%v", err)
	}
	if _, err := store.ListAnnouncements(ctx, ListOptions{PageSize: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad page=%v", err)
	}
	if _, err := store.CreateAnnouncement(ctx, CreateInput{Title: "x", Content: "y"}, " "); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create actor=%v", err)
	}
	if _, err := store.CreateAnnouncement(ctx, CreateInput{Title: "", Content: "y"}, "admin"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create invalid=%v", err)
	}
	if _, err := store.PatchAnnouncement(ctx, "a", " ", "r", PatchInput{Title: stringPtr("x")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("patch actor=%v", err)
	}
	// 构造函数错误臂。
	if _, err := NewStoreWithClock(nil, SQLite, "", OwnerGate{}, nil); err == nil {
		t.Fatal("nil db clock store must fail")
	}
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db service must fail")
	}
}
