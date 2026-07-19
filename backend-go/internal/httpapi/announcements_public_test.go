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

	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type announcementPublicServiceStub struct {
	items      []announcements.Announcement
	listInput  announcements.PublicListInput
	readInput  announcements.PublicReadInput
	readResult announcements.PublicReadResult
	listErr    error
	readErr    error
}

func (s *announcementPublicServiceStub) ListPublic(_ context.Context, input announcements.PublicListInput) ([]announcements.Announcement, error) {
	s.listInput = input
	return s.items, s.listErr
}

func (s *announcementPublicServiceStub) MarkPublicRead(_ context.Context, input announcements.PublicReadInput) (announcements.PublicReadResult, error) {
	s.readInput = input
	return s.readResult, s.readErr
}

func announcementRequest(method string, path string, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "user-1"})
	return request.WithContext(ctx)
}

func TestAnnouncementPublicListHandlerPreservesDataAndNoStore(t *testing.T) {
	service := &announcementPublicServiceStub{items: []announcements.Announcement{{ID: "a1"}}}
	recorder := httptest.NewRecorder()
	newAnnouncementPublicListHandler(service).ServeHTTP(recorder, announcementRequest(http.MethodGet, "/__aisys__/api/announcements/public?limit=7", ""))

	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("status=%d cache=%q pragma=%q", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Header().Get("Pragma"))
	}
	if service.listInput.SystemAccountID != "user-1" || service.listInput.Limit != 7 {
		t.Fatalf("list input = %+v", service.listInput)
	}
	var response DataResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	if !strings.Contains(string(data), `"id":"a1"`) || strings.Contains(string(data), `"ID"`) || strings.Contains(string(data), `"createdBy"`) {
		t.Fatalf("response data = %s", data)
	}
}

func TestAnnouncementPublicListHandlerRejectsInvalidLimitAndServiceErrors(t *testing.T) {
	for _, path := range []string{"/announcements/public?limit=", "/announcements/public?limit=0", "/announcements/public?limit=31", "/announcements/public?limit=1.5", "/announcements/public?limit=1&limit=2"} {
		recorder := httptest.NewRecorder()
		newAnnouncementPublicListHandler(&announcementPublicServiceStub{}).ServeHTTP(recorder, announcementRequest(http.MethodGet, path, ""))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", path, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	newAnnouncementPublicListHandler(&announcementPublicServiceStub{listErr: errors.New("database down")}).ServeHTTP(recorder, announcementRequest(http.MethodGet, "/announcements/public", ""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("service error status=%d, want 500", recorder.Code)
	}
}

func TestAnnouncementPublicListHandlerMatchesNodeNumericCoercion(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "1e1", want: 10},
		{value: "0x10", want: 16},
		{value: "+20", want: 20},
	} {
		service := &announcementPublicServiceStub{}
		recorder := httptest.NewRecorder()
		newAnnouncementPublicListHandler(service).ServeHTTP(recorder, announcementRequest(http.MethodGet, "/announcements/public?limit="+test.value, ""))
		if recorder.Code != http.StatusOK || service.listInput.Limit != test.want {
			t.Fatalf("limit %q status=%d parsed=%d, want %d", test.value, recorder.Code, service.listInput.Limit, test.want)
		}
	}
}

func TestAnnouncementPublicReadHandlerValidatesStrictBodyAndReturnsResult(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service := &announcementPublicServiceStub{readResult: announcements.PublicReadResult{ReadAt: now, Count: 2}}
	recorder := httptest.NewRecorder()
	newAnnouncementPublicReadHandler(service).ServeHTTP(recorder, announcementRequest(http.MethodPost, "/announcements/public/read", `{"announcementIds":[" a1 ","a2"]}`))
	if recorder.Code != http.StatusOK || service.readInput.SystemAccountID != "user-1" || len(service.readInput.AnnouncementIDs) != 2 {
		t.Fatalf("status=%d input=%+v", recorder.Code, service.readInput)
	}
	if !strings.Contains(recorder.Body.String(), `"readAt":"2026-07-19T12:00:00Z"`) || !strings.Contains(recorder.Body.String(), `"count":2`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	emptyRecorder := httptest.NewRecorder()
	newAnnouncementPublicReadHandler(service).ServeHTTP(emptyRecorder, announcementRequest(http.MethodPost, "/announcements/public/read", `{"announcementIds":[]}`))
	if emptyRecorder.Code != http.StatusOK || service.readInput.AnnouncementIDs == nil || len(service.readInput.AnnouncementIDs) != 0 {
		t.Fatalf("empty read status=%d input=%+v", emptyRecorder.Code, service.readInput)
	}

	for _, body := range []string{`{}`, `{"announcementIds":null}`, `{"announcementIds":[" "]}`, `{"announcementIds":["a"],"extra":1}`, `{"announcementIds":["a"]}{}`} {
		recorder := httptest.NewRecorder()
		newAnnouncementPublicReadHandler(service).ServeHTTP(recorder, announcementRequest(http.MethodPost, "/announcements/public/read", body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status=%d, want 400", body, recorder.Code)
		}
	}
}

func TestAnnouncementPublicHandlersRequireManagementAuthContext(t *testing.T) {
	recorder := httptest.NewRecorder()
	newAnnouncementPublicListHandler(&announcementPublicServiceStub{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/announcements/public", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", recorder.Code)
	}
}

func TestRouterRegistersAnnouncementPublicHandlersOnlyWhenManagementAPIEnabled(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	base := RouterOptions{
		ManagementAPIAuthMiddleware:             func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware:        func(next http.Handler) http.Handler { return next },
		ManagementAnnouncementPublicListHandler: handler,
		ManagementAnnouncementPublicReadHandler: handler,
	}

	enabled := base
	enabled.Config.ManagementAPIEnabled = true
	router := NewRouter(enabled)
	for _, path := range []string{"/__aisys__/api/announcements/public", "/__aisys__/api/announcements/public/read"} {
		recorder := httptest.NewRecorder()
		method := http.MethodGet
		if strings.HasSuffix(path, "/read") {
			method = http.MethodPost
		}
		router.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(`{"announcementIds":["a1"]}`)))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("enabled %s status=%d, want 204", path, recorder.Code)
		}
	}

	disabled := base
	disabled.Config.ManagementAPIEnabled = false
	router = NewRouter(disabled)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/announcements/public", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d, want 404", recorder.Code)
	}
}
