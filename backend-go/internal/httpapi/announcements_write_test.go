package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type announcementWriteServiceFake struct {
	listResult      announcements.Page
	listErr         error
	createResult    port.Announcement
	createErr       error
	updateResult    port.Announcement
	updateErr       error
	publishResult   port.Announcement
	publishErr      error
	unpublishResult port.Announcement
	unpublishErr    error
	deleteErr       error
	deleteResult    port.Announcement
	findResult      announcements.Announcement
	findFound       bool
	findErr         error
	createCalls     int
	updateCalls     int
	publishCalls    int
	unpublishCalls  int
	deleteCalls     int
}

func (f *announcementWriteServiceFake) ListManagement(_ context.Context, _ int, _ int) (announcements.Page, error) {
	return f.listResult, f.listErr
}

func (f *announcementWriteServiceFake) Create(_ context.Context, _ announcements.CreateInput) (port.Announcement, error) {
	f.createCalls++
	return f.createResult, f.createErr
}
func (f *announcementWriteServiceFake) Update(_ context.Context, _ announcements.UpdateInput) (port.Announcement, error) {
	f.updateCalls++
	return f.updateResult, f.updateErr
}
func (f *announcementWriteServiceFake) Publish(_ context.Context, _ announcements.ActionInput) (port.Announcement, error) {
	f.publishCalls++
	return f.publishResult, f.publishErr
}
func (f *announcementWriteServiceFake) Unpublish(_ context.Context, _ announcements.ActionInput) (port.Announcement, error) {
	f.unpublishCalls++
	return f.unpublishResult, f.unpublishErr
}
func (f *announcementWriteServiceFake) Delete(_ context.Context, _ announcements.ActionInput) (port.Announcement, error) {
	f.deleteCalls++
	return f.deleteResult, f.deleteErr
}
func (f *announcementWriteServiceFake) FindManagement(_ context.Context, _ string) (announcements.Announcement, bool, error) {
	return f.findResult, f.findFound, f.findErr
}

type announcementPageDataFake struct {
	err   error
	calls []announcementPageDataCall
}

type announcementPageDataCall struct {
	id        string
	operation string
	fieldMask []string
}

func (f *announcementPageDataFake) PublishAnnouncementPublicChange(_ context.Context, id, operation string, fieldMask []string) error {
	f.calls = append(f.calls, announcementPageDataCall{id: id, operation: operation, fieldMask: append([]string(nil), fieldMask...)})
	return f.err
}

func announcementWriteRequest(method, path, body, role string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		request = request.WithContext(context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{
			SystemAccountID: "actor-1", Username: "admin", DisplayName: "管理员", Role: role,
		}))
	}
	if id := chi.URLParam(request, "id"); id != "" {
		return request
	}
	for _, segment := range []string{"publish", "unpublish"} {
		if strings.HasSuffix(path, "/"+segment) {
			id := strings.TrimSuffix(strings.TrimSuffix(path, "/"+segment), "/")
			id = id[strings.LastIndex(id, "/")+1:]
			return announcementWriteRequestWithID(request, id)
		}
	}
	if method == http.MethodPatch || method == http.MethodDelete {
		id := path[strings.LastIndex(path, "/")+1:]
		return announcementWriteRequestWithID(request, id)
	}
	return request
}

func announcementWriteRequestWithID(request *http.Request, id string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", id)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func newAnnouncementWriteHandler(fake *announcementWriteServiceFake, queue *operationLogQueueStub, pageData *announcementPageDataFake) http.Handler {
	return newAnnouncementManagementWriteHandler(fake, announcementManagementWriteOptions{
		operationLogs: newManagementOperationLogOptions(ManagementOperationLogOptions{
			Client:   queue,
			Now:      func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) },
			NewLogID: func() string { return "oplog-announcement" },
		}),
		pageData: pageData,
		reader:   fake,
	})
}

func announcementPublished(id string) port.Announcement {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return port.Announcement{ID: id, Title: "标题", Content: "内容", Level: "info", Status: "published", PublishedAt: &now, CreatedAt: now, UpdatedAt: now}
}

func TestAnnouncementManagementWriteCreateStrictJSONAndStatus(t *testing.T) {
	valid := `{"title":"标题","content":"内容","level":"info","status":"draft"}`
	for name, body := range map[string]string{
		"unknown":        `{"title":"标题","content":"内容","extra":true}`,
		"null":           `null`,
		"null level":     `{"title":"标题","content":"内容","level":null}`,
		"null status":    `{"title":"标题","content":"内容","status":null}`,
		"empty level":    `{"title":"标题","content":"内容","level":""}`,
		"empty status":   `{"title":"标题","content":"内容","status":""}`,
		"empty":          "",
		"extra document": valid + ` {"another":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &announcementWriteServiceFake{createResult: port.Announcement{ID: "ann-1", Title: "标题"}}
			recorder := httptest.NewRecorder()
			newAnnouncementWriteHandler(fake, &operationLogQueueStub{}, &announcementPageDataFake{}).ServeHTTP(recorder, announcementWriteRequest(http.MethodPost, "/announcements", body, "admin"))
			if recorder.Code != http.StatusBadRequest || fake.createCalls != 0 {
				t.Fatalf("status=%d createCalls=%d body=%s", recorder.Code, fake.createCalls, recorder.Body.String())
			}
		})
	}

	fake := &announcementWriteServiceFake{createResult: port.Announcement{ID: "ann-1", Title: "标题", Status: "draft"}}
	recorder := httptest.NewRecorder()
	newAnnouncementWriteHandler(fake, &operationLogQueueStub{}, &announcementPageDataFake{}).ServeHTTP(recorder, announcementWriteRequest(http.MethodPost, "/announcements", valid, "admin"))
	if recorder.Code != http.StatusCreated || fake.createCalls != 1 {
		t.Fatalf("status=%d createCalls=%d body=%s", recorder.Code, fake.createCalls, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	newAnnouncementWriteHandler(fake, &operationLogQueueStub{}, &announcementPageDataFake{}).ServeHTTP(recorder, announcementWriteRequest(http.MethodPost, "/announcements", `{"title":"标题","content":"内容","status":null,"status":"draft"}`, "admin"))
	if recorder.Code != http.StatusCreated || fake.createCalls != 2 {
		t.Fatalf("duplicate last value status=%d createCalls=%d body=%s", recorder.Code, fake.createCalls, recorder.Body.String())
	}
}

func TestAnnouncementManagementWriteCreateRejectsBodyOverNodeLimit(t *testing.T) {
	fake := &announcementWriteServiceFake{}
	recorder := httptest.NewRecorder()
	body := `{"title":"标题","content":"` + strings.Repeat("x", announcementJSONMaxBodyBytes) + `"}`
	newAnnouncementWriteHandler(fake, &operationLogQueueStub{}, &announcementPageDataFake{}).ServeHTTP(
		recorder,
		announcementWriteRequest(http.MethodPost, "/announcements", body, "admin"),
	)
	if recorder.Code != http.StatusRequestEntityTooLarge || fake.createCalls != 0 {
		t.Fatalf("status=%d createCalls=%d body=%s", recorder.Code, fake.createCalls, recorder.Body.String())
	}
}

func TestAnnouncementManagementWriteUpdateActionsAndNotFound(t *testing.T) {
	fake := &announcementWriteServiceFake{
		findResult: announcementPublished("ann-1"), findFound: true,
		updateResult: announcementPublished("ann-1"), publishResult: announcementPublished("ann-1"),
		unpublishResult: port.Announcement{ID: "ann-1", Title: "标题", Status: "archived"},
	}
	queue := &operationLogQueueStub{}
	pageData := &announcementPageDataFake{}
	handler := newAnnouncementWriteHandler(fake, queue, pageData)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, announcementWriteRequest(http.MethodPatch, "/announcements/ann-1", `{"title":"新标题"}`, "admin"))
	if recorder.Code != http.StatusOK || fake.updateCalls != 1 {
		t.Fatalf("update status=%d calls=%d", recorder.Code, fake.updateCalls)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, announcementWriteRequest(http.MethodPatch, "/announcements/ann-1", `{}`, "admin"))
	if recorder.Code != http.StatusOK || fake.updateCalls != 2 {
		t.Fatalf("empty update status=%d calls=%d", recorder.Code, fake.updateCalls)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, announcementWriteRequest(http.MethodPatch, "/announcements/ann-1", `{"status":null}`, "admin"))
	if recorder.Code != http.StatusBadRequest || fake.updateCalls != 2 {
		t.Fatalf("null update status=%d calls=%d", recorder.Code, fake.updateCalls)
	}

	fake.findFound = false
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, announcementWriteRequest(http.MethodPatch, "/announcements/ann-1", `{"title":"新标题"}`, "admin"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing update status=%d, want 404", recorder.Code)
	}
	fake.findFound = true

	for _, test := range []struct {
		method, path string
		want         int
		calls        func() int
	}{
		{http.MethodPost, "/announcements/ann-1/publish", http.StatusOK, func() int { return fake.publishCalls }},
		{http.MethodPost, "/announcements/ann-1/unpublish", http.StatusOK, func() int { return fake.unpublishCalls }},
		{http.MethodDelete, "/announcements/ann-1", http.StatusNoContent, func() int { return fake.deleteCalls }},
	} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, announcementWriteRequest(test.method, test.path, "", "admin"))
		if recorder.Code != test.want || test.calls() != 1 {
			t.Fatalf("%s %s status=%d calls=%d", test.method, test.path, recorder.Code, test.calls())
		}
	}
}

func TestAnnouncementManagementWriteAuthAndServiceErrorMapping(t *testing.T) {
	for _, test := range []struct {
		name  string
		role  string
		want  int
		err   error
		found bool
	}{
		{name: "unauthenticated", role: "", want: http.StatusUnauthorized},
		{name: "ordinary user", role: "user", want: http.StatusForbidden},
		{name: "invalid", role: "admin", want: http.StatusBadRequest, err: announcements.ErrAnnouncementInputInvalid},
		{name: "not found", role: "admin", want: http.StatusNotFound, err: announcements.ErrAnnouncementNotFound},
		{name: "internal", role: "admin", want: http.StatusInternalServerError, err: errors.New("database down")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &announcementWriteServiceFake{createErr: test.err, createResult: port.Announcement{ID: "ann-1"}}
			if test.err == announcements.ErrAnnouncementNotFound {
				fake.findFound = false
			}
			recorder := httptest.NewRecorder()
			newAnnouncementWriteHandler(fake, &operationLogQueueStub{}, &announcementPageDataFake{}).ServeHTTP(recorder, announcementWriteRequest(http.MethodPost, "/announcements", `{"title":"标题","content":"内容"}`, test.role))
			if recorder.Code != test.want {
				t.Fatalf("status=%d, want %d; body=%s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestAnnouncementManagementWriteOperationLogAndPageData(t *testing.T) {
	before := announcementPublished("ann-1")
	fake := &announcementWriteServiceFake{
		createResult:    before,
		updateResult:    before,
		publishResult:   before,
		unpublishResult: port.Announcement{ID: "ann-1", Title: "标题", Status: "archived"},
		deleteResult:    before,
		findResult:      before, findFound: true,
	}
	queue := &operationLogQueueStub{}
	pageData := &announcementPageDataFake{}
	handler := newAnnouncementWriteHandler(fake, queue, pageData)
	requests := []struct {
		action, method, path, body string
		wantStatus                 int
		wantOperation              string
		wantMask                   []string
	}{
		{"create", http.MethodPost, "/announcements", `{"title":"标题","content":"内容","status":"published"}`, http.StatusCreated, "upsert", []string{"title", "content", "level", "status", "publishedAt"}},
		{"update", http.MethodPatch, "/announcements/ann-1", `{"title":"新标题"}`, http.StatusOK, "upsert", []string{"title", "content", "level", "status", "publishedAt"}},
		{"publish", http.MethodPost, "/announcements/ann-1/publish", "", http.StatusOK, "upsert", []string{"status", "publishedAt"}},
		{"unpublish", http.MethodPost, "/announcements/ann-1/unpublish", "", http.StatusOK, "delete", []string{"status"}},
		{"delete", http.MethodDelete, "/announcements/ann-1", "", http.StatusNoContent, "delete", nil},
	}
	for _, test := range requests {
		queue.payload = nil
		pageData.calls = nil
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, announcementWriteRequest(test.method, test.path, test.body, "admin"))
		if recorder.Code != test.wantStatus || len(pageData.calls) != 1 {
			t.Fatalf("action=%s status=%d pageCalls=%d body=%s", test.action, recorder.Code, len(pageData.calls), recorder.Body.String())
		}
		call := pageData.calls[0]
		if call.id != "ann-1" || call.operation != test.wantOperation || strings.Join(call.fieldMask, ",") != strings.Join(test.wantMask, ",") {
			t.Fatalf("action=%s pageData=%+v", test.action, call)
		}
		logInput, err := operationlogjob.DecodeWriteTaskPayload(queue.payload)
		if err != nil {
			t.Fatalf("action=%s decode operation log: %v", test.action, err)
		}
		if logInput.Action != test.action || logInput.OperationKey != "announcements."+test.action || logInput.ResourceType != "announcement" || logInput.ResourceID != "ann-1" {
			t.Fatalf("action=%s log=%+v", test.action, logInput)
		}
		if logInput.StatusCode == nil || *logInput.StatusCode != test.wantStatus {
			t.Fatalf("action=%s statusCode=%v", test.action, logInput.StatusCode)
		}
		if test.action == "create" && len(logInput.Changes) != 3 {
			t.Fatalf("create changes=%+v", logInput.Changes)
		}
	}
}

func TestAnnouncementManagementWriteSideEffectFailuresDoNotChangeSuccess(t *testing.T) {
	fake := &announcementWriteServiceFake{createResult: port.Announcement{ID: "ann-1", Title: "标题", Status: "published"}}
	queue := &operationLogQueueStub{err: errors.New("redis down")}
	pageData := &announcementPageDataFake{err: errors.New("page data down")}
	recorder := httptest.NewRecorder()
	newAnnouncementWriteHandler(fake, queue, pageData).ServeHTTP(recorder, announcementWriteRequest(http.MethodPost, "/announcements", `{"title":"标题","content":"内容","status":"published"}`, "admin"))
	if recorder.Code != http.StatusCreated || fake.createCalls != 1 || queue.calls != 1 || len(pageData.calls) != 1 {
		t.Fatalf("status=%d createCalls=%d queueCalls=%d pageCalls=%d", recorder.Code, fake.createCalls, queue.calls, len(pageData.calls))
	}
}

func TestAnnouncementManagementWriteRoutesUseTouchAuthAndRespectFeatureGate(t *testing.T) {
	var readCalls, writeCalls, touchCalls int
	managementHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	readAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { readCalls++; w.WriteHeader(http.StatusTeapot) })
	}
	writeAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeCalls++
			touchCalls++
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "actor-1", Role: "admin",
			})))
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      readAuth,
		ManagementAPIAuthTouchMiddleware: writeAuth,
		ManagementAnnouncementsHandler:   managementHandler,
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(`{"title":"标题","content":"正文"}`)))
	if recorder.Code != http.StatusAccepted || readCalls != 0 || writeCalls != 1 || touchCalls != 1 {
		t.Fatalf("status=%d read=%d write=%d touch=%d", recorder.Code, readCalls, writeCalls, touchCalls)
	}

	disabled := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: false},
		ManagementAPIAuthMiddleware:      readAuth,
		ManagementAPIAuthTouchMiddleware: writeAuth,
		ManagementAnnouncementsHandler:   managementHandler,
	})
	recorder = httptest.NewRecorder()
	disabled.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d, want 404", recorder.Code)
	}
}

func TestAnnouncementCreateRouteRejectsDuplicateSubmission(t *testing.T) {
	createCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		createCalls++
		w.WriteHeader(http.StatusCreated)
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "actor-1", Role: "admin",
			})))
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      auth,
		ManagementAPIAuthTouchMiddleware: auth,
		ManagementAnnouncementsHandler:   handler,
	})
	body := `{"title":"  标题  ","content":" 正文 ","level":"warning","status":"draft"}`
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(body)))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(
		`{"status":"draft","content":"正文","title":"标题","level":"warning"}`,
	)))
	if first.Code != http.StatusCreated || second.Code != http.StatusConflict || createCalls != 1 {
		t.Fatalf("first=%d second=%d createCalls=%d secondBody=%s", first.Code, second.Code, createCalls, second.Body.String())
	}
}

func TestAnnouncementCreateMutationGuardMatchesNodeTrimWhitespace(t *testing.T) {
	createCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		createCalls++
		w.WriteHeader(http.StatusCreated)
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "actor-1", Role: "admin",
			})))
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      auth,
		ManagementAPIAuthTouchMiddleware: auth,
		ManagementAnnouncementsHandler:   handler,
	})

	request := func(body string) int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(body)))
		return recorder.Code
	}
	if first, duplicate := request(`{"title":"\uFEFF标题\uFEFF","content":"\uFEFF正文\uFEFF","level":"\uFEFFinfo\uFEFF","status":"\uFEFFdraft\uFEFF"}`), request(`{"title":"标题","content":"正文","level":"info","status":"draft"}`); first != http.StatusCreated || duplicate != http.StatusConflict {
		t.Fatalf("ECMAScript trim statuses=%d,%d", first, duplicate)
	}
	if kept, plain := request(`{"title":"边界\u0085","content":"正文","level":"info","status":"draft"}`), request(`{"title":"边界","content":"正文","level":"info","status":"draft"}`); kept != http.StatusCreated || plain != http.StatusCreated {
		t.Fatalf("non-ECMAScript whitespace statuses=%d,%d", kept, plain)
	}
	if createCalls != 3 {
		t.Fatalf("createCalls=%d, want 3", createCalls)
	}
}

func TestAnnouncementCreateMutationGuardRejectsBodyOverNodeLimit(t *testing.T) {
	createCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		createCalls++
		w.WriteHeader(http.StatusCreated)
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), managementAuthContextKey, managementauth.Context{
				SystemAccountID: "actor-1", Role: "admin",
			})))
		})
	}
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      auth,
		ManagementAPIAuthTouchMiddleware: auth,
		ManagementAnnouncementsHandler:   handler,
	})
	body := `{"title":"标题","content":"` + strings.Repeat("x", announcementJSONMaxBodyBytes) + `"}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(body)))
	if recorder.Code != http.StatusRequestEntityTooLarge || createCalls != 0 {
		t.Fatalf("status=%d createCalls=%d", recorder.Code, createCalls)
	}
}

func TestAnnouncementWriteRequestHelperKeepsJSONContract(t *testing.T) {
	request := announcementWriteRequest(http.MethodPatch, "/announcements/ann-1", `{"title":"x"}`, "admin")
	if chi.URLParam(request, "id") != "ann-1" {
		t.Fatal("request helper did not attach route id")
	}
	var body map[string]string
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["title"] != "x" {
		t.Fatalf("body=%v err=%v", body, err)
	}
}
