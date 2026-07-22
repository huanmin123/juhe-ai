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
	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type announcementManagementServiceStub struct {
	page, pageSize int
	listResult     announcements.Page
	listErr        error
	findID         string
	findResult     announcements.Announcement
	findFound      bool
	findErr        error
}

func (s *announcementManagementServiceStub) ListManagement(_ context.Context, page int, pageSize int) (announcements.Page, error) {
	s.page, s.pageSize = page, pageSize
	return s.listResult, s.listErr
}

func (s *announcementManagementServiceStub) FindManagement(_ context.Context, id string) (announcements.Announcement, bool, error) {
	s.findID = id
	return s.findResult, s.findFound, s.findErr
}

func announcementManagementRequest(method, path string, role string, id string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(request.Context(), managementAuthContextKey, managementauth.Context{SystemAccountID: "actor-1", Role: role})
	request = request.WithContext(ctx)
	if id != "" {
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", id)
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	}
	return request
}

func TestAnnouncementManagementHandlerListsForAdminWithProgressivePage(t *testing.T) {
	service := &announcementManagementServiceStub{listResult: announcements.Page{
		Items: []port.Announcement{{ID: "a1", Content: "摘要"}}, Page: 2, PageSize: 10, PageUpperBound: 21, HasMore: true,
	}}
	recorder := httptest.NewRecorder()
	newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements?page=2&pageSize=10", "admin", ""))
	if recorder.Code != http.StatusOK || service.page != 2 || service.pageSize != 10 {
		t.Fatalf("status=%d page=%d pageSize=%d", recorder.Code, service.page, service.pageSize)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache=%q pragma=%q", recorder.Header().Get("Cache-Control"), recorder.Header().Get("Pragma"))
	}
	if !strings.Contains(recorder.Body.String(), `"total":21`) || !strings.Contains(recorder.Body.String(), `"hasMore":true`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	var response struct {
		Data struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if _, ok := response.Data.Items[0]["content"]; ok {
		t.Fatalf("management list leaked content: %s", recorder.Body.String())
	}
	if preview := response.Data.Items[0]["contentPreview"]; string(preview) != `"摘要"` {
		t.Fatalf("contentPreview=%s body=%s", preview, recorder.Body.String())
	}
}

func TestAnnouncementManagementHandlerAcceptsExtremeFiniteNodePage(t *testing.T) {
	service := &announcementManagementServiceStub{}
	recorder := httptest.NewRecorder()
	newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements?page=1e20", "admin", ""))
	if recorder.Code != http.StatusOK || service.page != int(^uint(0)>>1) {
		t.Fatalf("status=%d page=%d, want saturated max int", recorder.Code, service.page)
	}
}

func TestAnnouncementManagementHandlerAcceptsLargeNodeRadixPage(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "0xFFFFFFFFFFFFFFFFFFFFFFFF", want: int(^uint(0) >> 1)},
		{value: "0b11111111111111111111111111111111", want: 4294967295},
		{value: "0o777777777777777777777", want: int(^uint(0) >> 1)},
	} {
		service := &announcementManagementServiceStub{}
		recorder := httptest.NewRecorder()
		newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements?page="+test.value, "admin", ""))
		if recorder.Code != http.StatusOK || service.page != test.want {
			t.Fatalf("page %q status=%d parsed=%d, want %d", test.value, recorder.Code, service.page, test.want)
		}
	}
}

func TestAnnouncementManagementHandlerRejectsNonAdminAndInvalidQuery(t *testing.T) {
	service := &announcementManagementServiceStub{}
	for _, test := range []struct {
		role string
		path string
	}{
		{role: "user", path: "/announcements"},
		{role: "", path: "/announcements"},
		{role: "admin", path: "/announcements?page=0"},
		{role: "admin", path: "/announcements?pageSize=101"},
		{role: "admin", path: "/announcements?pageSize=1&pageSize=2"},
	} {
		recorder := httptest.NewRecorder()
		newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, test.path, test.role, ""))
		want := http.StatusForbidden
		if test.role == "admin" {
			want = http.StatusBadRequest
		}
		if recorder.Code != want {
			t.Fatalf("role=%q path=%s status=%d, want %d", test.role, test.path, recorder.Code, want)
		}
	}
}

func TestAnnouncementManagementHandlerReturnsDetailAndNotFound(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service := &announcementManagementServiceStub{findResult: announcements.Announcement{
		ID: "a1", Title: "标题", Content: strings.Repeat("全文", 200), CreatedAt: now, UpdatedAt: now,
	}, findFound: true}
	recorder := httptest.NewRecorder()
	newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements/a1", "super_admin", "a1"))
	if recorder.Code != http.StatusOK || service.findID != "a1" || !strings.Contains(recorder.Body.String(), `"content"`) {
		t.Fatalf("status=%d id=%q body=%s", recorder.Code, service.findID, recorder.Body.String())
	}

	service.findFound = false
	recorder = httptest.NewRecorder()
	newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements/missing", "admin", "missing"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d, want 404", recorder.Code)
	}
}

func TestAnnouncementManagementHandlerMapsServiceErrors(t *testing.T) {
	service := &announcementManagementServiceStub{listErr: errors.New("database down")}
	recorder := httptest.NewRecorder()
	newAnnouncementManagementHandler(service).ServeHTTP(recorder, announcementManagementRequest(http.MethodGet, "/announcements", "admin", ""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list error status=%d, want 500", recorder.Code)
	}
}

func TestRouterRegistersAnnouncementManagementReadsWithoutShadowingPublicRoute(t *testing.T) {
	managementHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	publicHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router := NewRouter(RouterOptions{
		Config:                                  config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:             func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware:        func(next http.Handler) http.Handler { return next },
		ManagementAnnouncementsHandler:          managementHandler,
		ManagementAnnouncementPublicListHandler: publicHandler,
	})
	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/__aisys__/api/announcements", want: http.StatusAccepted},
		{path: "/__aisys__/api/announcements/a1", want: http.StatusAccepted},
		{path: "/__aisys__/api/announcements/public", want: http.StatusNoContent},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.want {
			t.Fatalf("%s status=%d, want %d", test.path, recorder.Code, test.want)
		}
	}

	disabledRouter := NewRouter(RouterOptions{
		ManagementAPIAuthMiddleware:      func(next http.Handler) http.Handler { return next },
		ManagementAPIAuthTouchMiddleware: func(next http.Handler) http.Handler { return next },
		ManagementAnnouncementsHandler:   managementHandler,
	})
	for _, path := range []string{"/__aisys__/api/announcements", "/__aisys__/api/announcements/a1"} {
		recorder := httptest.NewRecorder()
		disabledRouter.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("disabled %s status=%d, want 404", path, recorder.Code)
		}
	}
}
