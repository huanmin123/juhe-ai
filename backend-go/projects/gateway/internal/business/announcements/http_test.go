package announcements

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func announcementHTTPHandler(t *testing.T, resolver ActorResolver) *HTTPHandler {
	t.Helper()
	store := announcementStore(t)
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &HTTPHandler{Service: service, ResolveActor: resolver}
}

func adminResolver(_ context.Context, _ *http.Request) (Actor, error) {
	return Actor{SystemAccountID: "admin", Role: "admin"}, nil
}

func TestHTTPHandlerPublicAndAdminLifecycle(t *testing.T) {
	h := announcementHTTPHandler(t, adminResolver)
	create := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"hello","content":"body","status":"published"}`))
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusCreated || !strings.Contains(createRec.Body.String(), `"id"`) {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}

	public := httptest.NewRecorder()
	h.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/public", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "hello") {
		t.Fatalf("public status=%d body=%s", public.Code, public.Body.String())
	}
	admin := httptest.NewRecorder()
	h.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/", nil))
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), "hello") {
		t.Fatalf("admin status=%d body=%s", admin.Code, admin.Body.String())
	}
}

func TestHTTPHandlerAuthAndErrorMapping(t *testing.T) {
	h := announcementHTTPHandler(t, func(context.Context, *http.Request) (Actor, error) {
		return Actor{}, nil
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/public/read", strings.NewReader(`{"announcementIds":[]}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth read status=%d body=%s", rec.Code, rec.Body.String())
	}

	h.ResolveActor = func(context.Context, *http.Request) (Actor, error) {
		return Actor{SystemAccountID: "user", Role: "user"}, nil
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden status=%d body=%s", rec.Code, rec.Body.String())
	}

	h.ResolveActor = adminResolver
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/not-found", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHTTPHandlerRejectsUnknownTrailingAndOversizedJSON(t *testing.T) {
	h := announcementHTTPHandler(t, adminResolver)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"x","content":"y","extra":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"x","content":"y"} {}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing value status=%d body=%s", rec.Code, rec.Body.String())
	}
	h.MaxBody = 8
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"x","content":"y"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHTTPHandlerUnavailableDependencyMaps503(t *testing.T) {
	h := &HTTPHandler{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service status=%d body=%s", rec.Code, rec.Body.String())
	}
	h = announcementHTTPHandler(t, func(context.Context, *http.Request) (Actor, error) {
		return Actor{}, errors.New("auth backend down")
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("resolver failure status=%d body=%s", rec.Code, rec.Body.String())
	}
}
