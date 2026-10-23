package gatewaydispatch

// 调度慢失败直切与墙钟收尾修复设计 §7 的 B1 记账用例：上游归因失败在结算点
// 记入来源避让 PendingFailures（同账户去重覆盖），纯 downstream_closed 不记。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
)

// avoidanceRememberCapture 把引擎回调接到真实 gatewayclientip 记账。
type avoidanceRememberCapture struct {
	avoidance *gatewayclientip.Avoidance
	tracker   *gatewayclientip.AvoidanceTracker
}

func newAvoidanceRememberCapture(t *testing.T, scope gatewayclientip.AvoidanceScopeInput) *avoidanceRememberCapture {
	t.Helper()
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{})
	if err != nil {
		t.Fatalf("NewAvoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	capture := &avoidanceRememberCapture{
		avoidance: avoidance,
		tracker:   avoidance.CreateAvoidanceTracker(scope),
	}
	return capture
}

// rememberFunc 是引擎回调形态的记账闭包（与 chain 层注入同构）。
func (c *avoidanceRememberCapture) rememberFunc(accountID string, failure gatewayclientip.AccountFailure) {
	c.avoidance.RememberPendingFailure(c.tracker, accountID, failure.AccountName, failure)
}

// TestDispatchRememberUpstreamResponseFailuresWithDedupe：B1 响应臂。a-1 两次
// 瞬态 500（初始 + 同账户重试）在结算点各记一次账，tracker 同账户去重覆盖
// 不追加；现场字段（phase / status / endpoint）来自失败结算。
func TestDispatchRememberUpstreamResponseFailuresWithDedupe(t *testing.T) {
	failServer := slowFailServer(t, 0, http.StatusInternalServerError)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	capture := newAvoidanceRememberCapture(t, gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: "system-1", GroupID: "group-1", APIKeyID: "apikey-1", ClientIP: "203.0.113.9",
	})
	args.RememberUpstreamAttemptFailure = capture.rememberFunc
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if len(capture.tracker.PendingFailures) != 1 {
		t.Fatalf("同账户失败应去重覆盖, pending = %+v", capture.tracker.PendingFailures)
	}
	pending := capture.tracker.PendingFailures[0]
	if pending.AccountID != "a-1" || pending.AccountName != "账号 a-1" {
		t.Fatalf("pending 身份不符: %+v", pending)
	}
	if pending.ErrorPhase != "upstream_response" || pending.StatusCode == nil || *pending.StatusCode != 500 {
		t.Fatalf("pending 现场不符: %+v", pending)
	}
	if pending.Endpoint != failServer.URL+"/v1/chat/completions" {
		t.Fatalf("endpoint = %q", pending.Endpoint)
	}
}

// TestDispatchRememberUpstreamTransportFailure：B1 传输臂。快速断开的
// started transport failure 记为 upstream_request、无状态码。
func TestDispatchRememberUpstreamTransportFailure(t *testing.T) {
	dropServer := slowFailServer(t, 0, 0)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {dropServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	capture := newAvoidanceRememberCapture(t, gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: "system-1", GroupID: "group-1", APIKeyID: "apikey-1", ClientIP: "203.0.113.9",
	})
	args.RememberUpstreamAttemptFailure = capture.rememberFunc
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	if len(capture.tracker.PendingFailures) != 1 {
		t.Fatalf("同账户失败应去重覆盖, pending = %+v", capture.tracker.PendingFailures)
	}
	pending := capture.tracker.PendingFailures[0]
	if pending.ErrorPhase != "upstream_request" {
		t.Fatalf("phase = %q", pending.ErrorPhase)
	}
	if pending.StatusCode != nil {
		t.Fatalf("传输失败不得携带状态码: %v", pending.StatusCode)
	}
	if pending.Endpoint != dropServer.URL+"/v1/chat/completions" {
		t.Fatalf("endpoint = %q", pending.Endpoint)
	}
}

// TestDispatchDownstreamClosedFailureNotRemembered：B1 边界。客户端中途断开
// （downstream_closed）无上游失败，不得记账。
func TestDispatchDownstreamClosedFailureNotRemembered(t *testing.T) {
	hangServer := slowFailServer(t, 5_000, http.StatusBadGateway)
	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{5, 5, 5, 5}
	driver.urlByAccount = map[string][]string{
		"a-1": {hangServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := dispatchArgs(t, req, testAccounts("a-1"))
	args.Settings = fastDispatchSettings()
	args.Settings.SameAccountRetryFastFailMaxMs = 0
	signal, cancel := context.WithCancel(context.Background())
	args.Signal = signal
	remembered := 0
	args.RememberUpstreamAttemptFailure = func(accountID string, failure gatewayclientip.AccountFailure) {
		remembered++
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil {
		t.Fatal("客户端中止后派发应以错误返回")
	}
	if remembered != 0 {
		t.Fatalf("downstream_closed 不得记账, remembered = %d", remembered)
	}
}
