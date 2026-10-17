package gatewaydispatch

// BUG-0325：混编能力域分组请求无效模型（无任何账户声明）时，能力过滤曾把
// 逐账户 driver reason 折叠为 capability_mismatch，全拒聚合又取候选顺序
// 第一个账户的 endpoint_mode_unsupported，model_unsupported 特判（404
// model_not_found 语义）永远轮不到 → 客户端收到 503「当前分组无账户支持
// 请求路径或客户端协议」。本文件钉住修复后的三面契约：
//  1. 全部账户因模型被拒 → 404 + model_not_found + message 含请求模型名；
//  2. 存在路径/协议拒绝（audio 打 chat-only 组合等）→ 维持 503 原文案；
//  3. 部分拒绝（组内存在兼容账户）→ 正常派发，Skipped 只作明细。
//
// 全部纯函数/内存 fake（Mock 优先），不连数据库、不走真实上游。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// bug0325ReasonDriver 按账户 ID 表给出逐账户能力拒绝 reason，bool 判定与
// reason 同源（支持 ⟺ reason 空，复数版返回首个非空 reason）——镜像
// chain_driver.go gatewayRequestCapabilityMismatchReasonFor 的端口契约：
// 过滤器经复数 reason 端口的单账户形态取逐账户细分（BUG-0325）。
type bug0325ReasonDriver struct {
	*fakeDriver
	reasonByAccountID map[string]string
}

func (d *bug0325ReasonDriver) AccountSupportsGatewayRequest(req *gatewaypreauth.GatewayRequest, account AccountCandidate, requestClientCompatibility string) bool {
	return d.reasonByAccountID[account.ID] == ""
}

func (d *bug0325ReasonDriver) GatewayRequestCapabilityMismatchReason(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) string {
	for _, account := range accounts {
		if reason := d.reasonByAccountID[account.ID]; reason != "" {
			return reason
		}
	}
	return ""
}

// bug0325Request 构造指定路径的 JSON 网关请求（newTestRequest 固定
// /v1/chat/completions；媒体复现需要 /v1/audio/speech）。
func bug0325Request(t *testing.T, path, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, path, nil)
	raw.Header.Set("Content-Type", "application/json")
	request := gatewaypreauth.NewGatewayRequest(raw)
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    mustJSONObject(t, body),
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
		},
	}
	return request
}

// bug0325CandidateArgs 组装候选过滤入参（capturingCoordinator 记录
// CompleteFailure / RequestFallback 终态）。
func bug0325CandidateArgs(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, coordinator *capturingCoordinator) CandidateFilterArgs {
	return CandidateFilterArgs{
		Req:                  req,
		AuditCapture:         AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}},
		UsageContext:         testUsageContext(),
		StartedAt:            1_000,
		RawCandidateAccounts: accounts,
		SystemAccountID:      "system-1",
		APIKeyID:             "apikey-1",
		GroupID:              "group-1",
		RouteCoordinator:     coordinator,
	}
}

// 过滤器出口：AccountSkip.Reason 带出逐账户真实 driver reason；driver 单账户
// reason 判空时回退 capability_mismatch 稳定常量（client-compatibility 门
// 形态），审计明细 reason 恒非空。
func TestBug0325CapabilityFilterCarriesPerAccountDriverReason(t *testing.T) {
	req := newTestRequest(t, `{"model":"not-a-real-model","stream":true}`)
	driver := &bug0325ReasonDriver{fakeDriver: &fakeDriver{}, reasonByAccountID: map[string]string{
		"a-model":     "model_unsupported",
		"b-endpoint":  "endpoint_mode_unsupported",
		"c-supported": "",
	}}
	result := FilterGatewayAccountsByRequestCapability(req, testAccounts("a-model", "b-endpoint", "c-supported"), driver, "", "")
	if result.SkippedCount != 2 || len(result.Accounts) != 1 || result.Accounts[0].ID != "c-supported" {
		t.Fatalf("skipped/remaining = %d/%#v", result.SkippedCount, accountIDs(result.Accounts))
	}
	want := []AccountSkip{
		{AccountID: "a-model", Reason: "model_unsupported"},
		{AccountID: "b-endpoint", Reason: "endpoint_mode_unsupported"},
	}
	if !reflect.DeepEqual(result.Skipped, want) {
		t.Fatalf("skipped = %#v, want %#v", result.Skipped, want)
	}
	silent := &w13g3SilentMismatchDriver{fakeDriver: &fakeDriver{mismatchAll: true}}
	silentResult := FilterGatewayAccountsByRequestCapability(req, testAccounts("a-model"), silent, "", "")
	if silentResult.SkippedCount != 1 || silentResult.Skipped[0].Reason != CapabilitySkipReasonMismatch {
		t.Fatalf("empty driver reason must fall back to %s: %#v", CapabilitySkipReasonMismatch, silentResult.Skipped)
	}
}

// 验收 1：混编能力域分组账户 A（声明 gpt-x）+ 账户 B（声明 other-model），
// 请求 not-a-real-model → 404 + model_not_found + message 含模型名（不再
// 503 路径协议文案）。
func TestBug0325AllModelUnsupportedExhaustionReturns404ModelNotFound(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Driver = &bug0325ReasonDriver{fakeDriver: &fakeDriver{}, reasonByAccountID: map[string]string{
		"chat-a":  "model_unsupported",
		"other-b": "model_unsupported",
	}}
	req := newTestRequest(t, `{"model":"not-a-real-model","stream":true}`)
	accounts := []AccountCandidate{
		{ID: "chat-a", Name: "chat-a", Type: "api_key", SupportedModels: []string{"gpt-x"}},
		{ID: "other-b", Name: "other-b", Type: "api_key", SupportedModels: []string{"other-model"}},
	}
	coordinator := &capturingCoordinator{}
	args := bug0325CandidateArgs(req, accounts, coordinator)
	result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil {
		t.Fatal("completeFailure missing")
	}
	failure := coordinator.failure
	if failure.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", failure.StatusCode)
	}
	if failure.ErrorCode != "model_not_found" {
		t.Fatalf("error code = %q, want model_not_found", failure.ErrorCode)
	}
	if failure.ErrorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", failure.ErrorType)
	}
	if failure.Message != "当前分组无账户支持请求模型：not-a-real-model" {
		t.Fatalf("message = %q", failure.Message)
	}
	if failure.ErrorPhase != "dispatch" {
		t.Fatalf("error phase = %q", failure.ErrorPhase)
	}
}

// 验收 2：混编组存在端点模式/协议拒绝（audio 请求打 chat-only 组合）→
// 维持 503 + 路径/协议原文案（聚合 reason 取第一个账户的
// endpoint_mode_unsupported，与既有行为一致，不回归）。
func TestBug0325MixedRejectionKeeps503PathProtocolContract(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Driver = &bug0325ReasonDriver{fakeDriver: &fakeDriver{}, reasonByAccountID: map[string]string{
		"chat-a":  "endpoint_mode_unsupported",
		"other-b": "model_unsupported",
	}}
	// 修订判据下的混编 503 语义：chat-a 声明命中请求模型（模型过滤有幸存
	// 者）但端点不符——路径/协议语义是主因，维持 503；若模型也全组无人
	// 声明（如请求 not-a-real-model），则 404 model_not_found 优先（见
	// TestBug0325MixedRejectionModelUndeclaredReturns404）。
	req := bug0325Request(t, "/v1/audio/speech", `{"model":"not-a-real-model","input":"hi"}`)
	accounts := []AccountCandidate{
		{ID: "chat-a", Name: "chat-a", Type: "api_key", SupportedModels: []string{"not-a-real-model"}},
		{ID: "other-b", Name: "other-b", Type: "api_key", SupportedModels: []string{"other-model"}},
	}
	coordinator := &capturingCoordinator{}
	args := bug0325CandidateArgs(req, accounts, coordinator)
	result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil {
		t.Fatal("completeFailure missing")
	}
	failure := coordinator.failure
	if failure.StatusCode != 503 {
		t.Fatalf("status = %d, want 503 (path/protocol semantics unchanged)", failure.StatusCode)
	}
	if failure.ErrorCode != "endpoint_mode_unsupported" {
		t.Fatalf("error code = %q, want endpoint_mode_unsupported", failure.ErrorCode)
	}
	if failure.ErrorType != "service_unavailable" {
		t.Fatalf("error type = %q", failure.ErrorType)
	}
	if failure.Message != "当前分组无账户支持请求路径或客户端协议" {
		t.Fatalf("message = %q", failure.Message)
	}
}

// 修订判据补例：chat+video 混编分组请求全组无人声明的模型——视频账户以
// endpoint_mode_unsupported 被拒不再掩盖模型无效主因，404 model_not_found
// 优先（真实复现：volctest 混编分组 /v1/chat/completions model=gpt-never）。
func TestBug0325MixedRejectionModelUndeclaredReturns404(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Driver = &bug0325ReasonDriver{fakeDriver: &fakeDriver{}, reasonByAccountID: map[string]string{
		"chat-a":  "model_unsupported",
		"video-b": "endpoint_mode_unsupported",
	}}
	req := bug0325Request(t, "/v1/chat/completions", `{"model":"gpt-never","messages":[]}`)
	accounts := []AccountCandidate{
		{ID: "chat-a", Name: "chat-a", Type: "api_key", SupportedModels: []string{"doubao-seed-2-0-mini-260428"}},
		{ID: "video-b", Name: "video-b", Type: "api_key", SupportedModels: []string{"doubao-seedance-2-0-mini-260615"}},
	}
	coordinator := &capturingCoordinator{}
	args := bug0325CandidateArgs(req, accounts, coordinator)
	result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil {
		t.Fatal("completeFailure missing")
	}
	failure := coordinator.failure
	if failure.StatusCode != 404 || failure.ErrorCode != "model_not_found" {
		t.Fatalf("status = %d code = %q, want 404 model_not_found", failure.StatusCode, failure.ErrorCode)
	}
	if failure.Message != "当前分组无账户支持请求模型：gpt-never" {
		t.Fatalf("message = %q", failure.Message)
	}
}

// 验收 3：部分拒绝——组内一个账户兼容 → 正常派发（Skipped 带 per-account
// reason 不影响放行），兼容账户进入候选窗口且模型过滤通过。
func TestBug0325PartialCapabilityRejectionStillDispatches(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Driver = &bug0325ReasonDriver{fakeDriver: &fakeDriver{}, reasonByAccountID: map[string]string{
		"chat-a":   "endpoint_mode_unsupported",
		"chat-okt": "",
	}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	accounts := testAccounts("chat-a", "chat-okt")
	coordinator := &capturingCoordinator{}
	args := bug0325CandidateArgs(req, accounts, coordinator)
	result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s (reason %q)", result.Outcome, result.Reason)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "chat-okt" {
		t.Fatalf("accounts = %#v, want only chat-okt", accountIDs(result.Accounts))
	}
	if coordinator.failure != nil || coordinator.fallbackCalled {
		t.Fatalf("partial rejection must not terminate: failure=%#v fallback=%v", coordinator.failure, coordinator.fallbackCalled)
	}
	// PreFilterSkipped 明细带出被拒账户的真实 reason（决策可解释性），不阻断。
	found := false
	for _, skip := range result.PreFilterSkipped {
		if skip.AccountID == "chat-a" {
			found = true
			if skip.Reason != "endpoint_mode_unsupported" {
				t.Fatalf("preFilterSkip reason = %q, want endpoint_mode_unsupported", skip.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("preFilterSkipped missing chat-a: %#v", result.PreFilterSkipped)
	}
}
