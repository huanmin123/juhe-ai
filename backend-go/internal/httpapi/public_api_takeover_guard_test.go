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
