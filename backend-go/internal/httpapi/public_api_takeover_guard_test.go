package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestRouterDoesNotRegisterW1bPublicAPIBeforeTakeover(t *testing.T) {
	router := NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["error"]; got != "接口不存在" {
		t.Fatalf("error = %v, want current generic not-found before W1b takeover", got)
	}
}

func TestRouterRegistersW1bPublicAPIOnlyWhenExplicitlyEnabled(t *testing.T) {
	var paths []string
	publicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	router := NewRouter(RouterOptions{
		Config:           config.Config{Host: "127.0.0.1", Port: 3000, PublicAPIEnabled: true},
		Logger:           slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
		PublicAPIHandler: publicHandler,
	})

	for _, path := range []string{"/__aipublic__/group/list", "/__aipublic__/unknown"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, rec.Code)
		}
	}
	if len(paths) != 2 || paths[0] != "/__aipublic__/group/list" || paths[1] != "/__aipublic__/unknown" {
		t.Fatalf("public handler paths = %v, want full unstripped public paths", paths)
	}

	req := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-public status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["error"]; got != "接口不存在" {
		t.Fatalf("non-public error = %v, want root generic not-found", got)
	}
}

func TestRouterRequiresPublicAPIHandlerWhenEnabled(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewRouter() did not panic without public API handler")
		}
	}()

	_ = NewRouter(RouterOptions{
		Config: config.Config{Host: "127.0.0.1", Port: 3000, PublicAPIEnabled: true},
		Logger: slog.New(slog.NewTextHandler(testWriter{t: t}, nil)),
	})
}
