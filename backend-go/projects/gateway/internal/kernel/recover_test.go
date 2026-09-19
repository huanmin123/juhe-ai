package kernel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 2026-09-18 加固：此前 handler panic 走 net/http 连接级兜底——客户端收到空回
// 复断连，无 JSON envelope、无 traceId。recoverMiddleware 把未提交 panic 转
// 成内核 500 {"message":"服务器内部错误"}，已提交（流式中途）panic 只记日志。

func TestRecoverMiddlewareUncommittedPanicWritesErrorEnvelope(t *testing.T) {
	k := New(Options{CompressionDisabled: true})
	k.Register("GET /panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "服务器内部错误") {
		t.Fatalf("body=%s", body)
	}
	if resp.Header.Get("X-Trace-Id") == "" {
		t.Fatal("X-Trace-Id must survive the recovery envelope")
	}
}

func TestRecoverMiddlewareCommittedPanicKeepsCommittedResponse(t *testing.T) {
	k := New(Options{CompressionDisabled: true})
	k.Register("GET /late-panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom after commit")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/late-panic")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("committed status=%d, want 200 (envelope must not overwrite)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "partial") || strings.Contains(string(body), "服务器内部错误") {
		t.Fatalf("committed body=%s", body)
	}
}

func TestRecoverMiddlewareNormalRequestsUnaffected(t *testing.T) {
	k := New(Options{CompressionDisabled: true})
	k.Register("GET /ok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteOK(w, map[string]string{"state": "ok"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"state":"ok"`) {
		t.Fatalf("normal request changed: status=%d body=%s", resp.StatusCode, body)
	}
}
