package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementruntimeloggrep"
)

func TestManagementRuntimeLogGrepHandlerRequiresAdminAndNoStore(t *testing.T) {
	service := &managementRuntimeLogGrepServiceStub{}
	handler := newManagementRuntimeLogGrepHandler(service)

	for _, role := range []string{"user", ""} {
		req := withManagementRuntimeLogAuth(httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs/grep", nil), role)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %q status=%d body=%s", role, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("role %q missing no-store", role)
		}
	}
	if service.grepCalled || service.runtimeCalled {
		t.Fatal("ordinary users must not reach grep service")
	}
}

func TestManagementRuntimeLogGrepHandlerParsesQueryAndUsesBoundedContext(t *testing.T) {
	service := &managementRuntimeLogGrepServiceStub{grepResult: managementruntimeloggrep.Result{
		Available: true,
		Keywords:  []string{"needle", "second"},
		Items:     []managementruntimeloggrep.Item{},
		Limit:     12,
	}}
	handler := newManagementRuntimeLogGrepHandler(service)
	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=needle,second&keyword=third&limit=12.8&startAt=2026-07-22T12:00:00Z&endAt=2026-07-22T10:00:00Z", nil)
	req = withManagementRuntimeLogAuth(req, "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	if !service.grepCalled || !service.deadlineSet {
		t.Fatalf("grepCalled=%v deadline=%v", service.grepCalled, service.deadlineSet)
	}
	if got := strings.Join(service.input.Keywords, "|"); got != "needle,second|third" || service.input.Limit != 12 {
		t.Fatalf("input=%+v", service.input)
	}
	if !service.input.StartAt.Before(service.input.EndAt) {
		t.Fatalf("range=%s - %s", service.input.StartAt, service.input.EndAt)
	}
	var body struct {
		Data managementruntimeloggrep.Result `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Data.Available || body.Data.Limit != 12 {
		t.Fatalf("data=%+v", body.Data)
	}
}

func TestManagementRuntimeLogGrepOptionsUsesStaticRoute(t *testing.T) {
	service := &managementRuntimeLogGrepServiceStub{runtime: managementruntimeloggrep.Runtime{
		DefaultStartAt:        "2026-07-19T12:00:00.000Z",
		DefaultEndAt:          "2026-07-22T12:00:00.000Z",
		DefaultRangeDays:      3,
		MaxRangeDays:          7,
		FileRetentionDays:     30,
		MaxConcurrentSearches: 1,
	}}
	router := NewRouter(RouterOptions{
		Config:                          config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                          slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		ManagementRuntimeLogGrepHandler: newManagementRuntimeLogGrepHandler(service),
		ManagementAPIAuthMiddleware: NewManagementAPIAuthMiddleware(&managementAPIAuthenticatorStub{
			context: managementauth.Context{SystemAccountID: "sys_admin", Role: "admin", SessionID: "sess_admin"},
		}),
	})

	for _, path := range []string{"/__aisys__/api/runtime-logs/grep-options", "/__aisys__/api/runtime-logs/grep-options/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", "juhe_ai_session=session-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d cache=%q body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if !service.runtimeCalled || service.grepCalled {
		t.Fatalf("runtime=%v grep=%v", service.runtimeCalled, service.grepCalled)
	}

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs/grep?keywords=needle", nil)
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !service.grepCalled {
		t.Fatalf("grep route status=%d called=%v body=%s", rec.Code, service.grepCalled, rec.Body.String())
	}
}

func TestManagementRuntimeLogGrepOptionsReturnsRedactedDependencyError(t *testing.T) {
	service := &managementRuntimeLogGrepServiceStub{runtimeErr: errors.New("secret directory D:/private/logs")}
	handler := newManagementRuntimeLogGrepHandler(service)
	req := withManagementRuntimeLogAuth(httptest.NewRequest(http.MethodGet, "/__aisys__/api/runtime-logs/grep-options", nil), "admin")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "服务器内部错误") || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type managementRuntimeLogGrepServiceStub struct {
	grepCalled    bool
	runtimeCalled bool
	deadlineSet   bool
	input         managementruntimeloggrep.Input
	grepResult    managementruntimeloggrep.Result
	runtime       managementruntimeloggrep.Runtime
	runtimeErr    error
}

func (s *managementRuntimeLogGrepServiceStub) Grep(ctx context.Context, input managementruntimeloggrep.Input) managementruntimeloggrep.Result {
	s.grepCalled = true
	s.input = input
	_, s.deadlineSet = ctx.Deadline()
	return s.grepResult
}

func (s *managementRuntimeLogGrepServiceStub) Runtime(ctx context.Context) (managementruntimeloggrep.Runtime, error) {
	s.runtimeCalled = true
	_, s.deadlineSet = ctx.Deadline()
	return s.runtime, s.runtimeErr
}
