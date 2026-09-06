package main

// accounts RuntimeResetEffects.DispatchAccountHealthCheck 接线断言：生产装配把
// reset/激活面的健康检查派发接到 chain_request_failure_health.go 的 HMAC 桥
// （POST {JobsInternalURL}/__aiinternal__/v1/account-health-check/dispatch，
// 消费端 jobs internal/internalapi/healthdispatch.go；Node
// dispatchAccountHealthCheck，internal-api service）。本文件用 httptest 假
// jobs 端点断言：wire 契约（version/accountId/reason + 签名头）、受理与拒绝
// 两条路径（fire-and-forget，派发拒绝不中断 reset），以及空目标桥的 inert
// 降级契约。

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

const (
	resetDispatchTestAccountID = "acc-reset-dispatch"
	resetDispatchTestSecret    = "reset-dispatch-secret"
)

// newResetDispatchComposition 提供派发装配所需的最小 composition
// （accountkeystates 构造只 fail-fast 空句柄/空密钥，派发路径不触库）。
func newResetDispatchComposition(t *testing.T) *composition {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &composition{db: db, Bus: inval.New(time.Now)}
}

// newResetDispatchBridge 走生产同款构造：newAccountsRuntimeResetBridge +
// newChainJobsHealthDispatchBridge（HMAC 桥指向 httptest 目标）。
func newResetDispatchBridge(t *testing.T, composed *composition, server *httptest.Server) accounts.RuntimeResetEffects {
	t.Helper()
	client := server.Client()
	client.Timeout = 5 * time.Second
	resetEffects, err := newAccountsRuntimeResetBridge(composed, nil, &chainRuntimeServices{}, resetDispatchTestSecret,
		newChainJobsHealthDispatchBridge(server.URL, resetDispatchTestSecret, client))
	if err != nil {
		t.Fatalf("assemble runtime reset bridge: %v", err)
	}
	return resetEffects
}

func TestAccountsRuntimeResetBridgeDispatchesSignedHealthCheck(t *testing.T) {
	composed := newResetDispatchComposition(t)
	dispatched := make(chan chainHealthDispatchRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != chainHealthDispatchPath {
			t.Errorf("unexpected bridge request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			t.Errorf("read bridge body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("X-Juhe-Ai-Signature"); got != signChainHealthDispatch(resetDispatchTestSecret, rawBody) {
			t.Errorf("signature mismatch: %s", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload chainHealthDispatchRequest
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			t.Errorf("decode bridge payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Version != 1 || payload.AccountID != resetDispatchTestAccountID || payload.Reason == "" {
			t.Errorf("wire payload mismatch: %+v", payload)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"outcome":"queued","decisionCode":"queued"}`))
		dispatched <- payload
	}))
	defer server.Close()

	resetEffects := newResetDispatchBridge(t, composed, server)
	// 管理面接线同款：write.go 激活 / runtime_reset.go reset / routes.go PATCH
	// 都经 SetRuntimeResetEffects 后的该端口派发。
	accountStore, err := accounts.NewStore(composed.db, false, resetDispatchTestSecret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	accountStore.SetRuntimeResetEffects(resetEffects)

	resetEffects.DispatchAccountHealthCheck(resetDispatchTestAccountID, "activation")
	select {
	case payload := <-dispatched:
		if payload.Reason != "activation" || payload.TraceID != "" || payload.SourceFence != nil {
			t.Fatalf("unexpected dispatched payload: %+v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health-check dispatch never reached the jobs internalapi stub")
	}
}

func TestAccountsRuntimeResetBridgeDispatchRejectedKeepsReset(t *testing.T) {
	composed := newResetDispatchComposition(t)
	attempts := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		attempts <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	resetEffects := newResetDispatchBridge(t, composed, server)
	// 非 202 → 派发拒绝：fire-and-forget，端口不 panic、不向调用方传错。
	resetEffects.DispatchAccountHealthCheck(resetDispatchTestAccountID, "manual_reset")
	select {
	case <-attempts:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge never attempted the dispatch")
	}

	// 空目标桥（chain 关闭/手工装配未接 jobs）：inert 契约——立即返回、
	// 不触网络、reset 继续。
	inert, err := newAccountsRuntimeResetBridge(composed, nil, &chainRuntimeServices{}, resetDispatchTestSecret,
		newChainJobsHealthDispatchBridge("", "", nil))
	if err != nil {
		t.Fatalf("assemble inert reset bridge: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		inert.DispatchAccountHealthCheck(resetDispatchTestAccountID, "inert")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("inert bridge blocked the reset")
	}
}

func TestAccountsRuntimeResetBridgeDispatchIsFireAndForget(t *testing.T) {
	composed := newResetDispatchComposition(t)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		close(started)
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	defer close(release)

	resetEffects := newResetDispatchBridge(t, composed, server)
	returned := make(chan struct{})
	go func() {
		resetEffects.DispatchAccountHealthCheck(resetDispatchTestAccountID, "configuration")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("health-check dispatch waited for the jobs response")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("health-check dispatch never reached the jobs stub")
	}
}
