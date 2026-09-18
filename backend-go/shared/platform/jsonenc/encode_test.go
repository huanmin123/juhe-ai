package jsonenc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEncodeJSON(t *testing.T) {
	got := EncodeJSON(map[string]int{"a": 1})
	if got != `{"a":1}` {
		t.Fatalf("encode=%s", got)
	}
}

func TestEncodeJSONUnserializableReturnsEmpty(t *testing.T) {
	if got := EncodeJSON(map[string]any{"f": func() {}}); got != "" {
		t.Fatalf("不可序列化载荷应返回空串（忽略错误语义）: %s", got)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusOK, map[string]string{"ok": "yes"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%s", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != "yes" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestWriteJSONUnserializableReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusOK, map[string]any{"f": func() {}}); err == nil {
		t.Fatal("不可序列化载荷必须返回错误")
	}
}
