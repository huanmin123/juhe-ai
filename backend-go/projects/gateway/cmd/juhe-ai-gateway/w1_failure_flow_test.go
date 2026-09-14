package main

// w1（单元层）：失败派发器直调收割——诊断/非网关流量直返、网关失败响应的
// 候选切换主流程、配额系统规则命中、传输错误结算。决策服务用零值实例
// （无规则配置），状态写侧走显式降级实现。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func w1UpstreamFailure(status int, body string) *gatewaydispatch.GatewayUpstreamResponse {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(status, header, io.NopCloser(strings.NewReader(body)))
}

func TestW1HandleFailedUpstreamResponseArms(t *testing.T) {
	dispatcher := &chainFailureDispatcher{}
	ctx := context.Background()
	account := gatewaydispatch.AccountCandidate{ID: "acc_1", Type: "api_key"}
	// 诊断流量：直返上游响应，不进候选切换。
	diagnosticInput := gatewaydispatch.FailedUpstreamResponseInput{
		Account: account, UpstreamURL: "https://u/v1/chat/completions",
		Response: w1UpstreamFailure(http.StatusTooManyRequests, `{"error":{"message":"限流"}}`),
	}
	diagnosticInput.UsageContext.TrafficSource = "manual_account_test"
	result, err := dispatcher.HandleFailedUpstreamResponse(ctx, diagnosticInput)
	if err != nil || result.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("diagnostic = %+v, %v", result, err)
	}
	// 非网关流量：直返 + 会话亲和遗忘（affinity 缺席安全）。
	nonGateway := diagnosticInput
	nonGateway.UsageContext.TrafficSource = "client_proxy"
	nonGateway.SessionAffinityKey = "sess_1"
	result, err = dispatcher.HandleFailedUpstreamResponse(ctx, nonGateway)
	if err != nil || result.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("non-gateway = %+v, %v", result, err)
	}
	// 网关流量 500（无规则命中）：主流程走完 → skip_account 切换候选。
	gatewayInput := gatewaydispatch.FailedUpstreamResponseInput{
		Account: account, UpstreamURL: "https://u/v1/chat/completions",
		Response:         w1UpstreamFailure(http.StatusInternalServerError, `{"error":{"message":"内部错误"}}`),
		AttemptStartedAt: 1,
	}
	gatewayInput.UsageContext.TrafficSource = gatewayTrafficSource
	result, err = dispatcher.HandleFailedUpstreamResponse(ctx, gatewayInput)
	if err != nil {
		t.Fatalf("gateway 500 = %v", err)
	}
	if result.Action == gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("网关失败不得直返 = %+v", result)
	}
	// 402 配额体：系统额度规则命中 → 显式策略失败类型。
	quotaInput := gatewaydispatch.FailedUpstreamResponseInput{
		Account: account, UpstreamURL: "https://u/v1/chat/completions",
		Response: w1UpstreamFailure(http.StatusPaymentRequired,
			`{"error":{"message":"insufficient quota remaining","code":"insufficient_quota"}}`),
	}
	quotaInput.UsageContext.TrafficSource = gatewayTrafficSource
	result, err = dispatcher.HandleFailedUpstreamResponse(ctx, quotaInput)
	if err != nil {
		t.Fatalf("gateway 402 = %v", err)
	}
	_ = result
	// nil 响应（已耗尽读取）：不 panic，走完主流程。
	nilResponse := gatewayInput
	nilResponse.Response = nil
	if _, err := dispatcher.HandleFailedUpstreamResponse(ctx, nilResponse); err != nil {
		t.Fatalf("nil response = %v", err)
	}
}

func TestW1HandleUpstreamRequestErrorArms(t *testing.T) {
	dispatcher := &chainFailureDispatcher{}
	ctx := context.Background()
	input := gatewaydispatch.UpstreamRequestErrorInput{
		Account:     gatewaydispatch.AccountCandidate{ID: "acc_1", Type: "api_key"},
		UpstreamURL: "https://u/v1/chat/completions", Error: context.DeadlineExceeded,
		AuditAttemptIndex: 2,
	}
	input.UsageContext.TrafficSource = gatewayTrafficSource
	result, err := dispatcher.HandleUpstreamRequestError(ctx, input)
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	if result.Action == gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("传输错误应切候选 = %+v", result)
	}
	_ = gatewaypreauth.SystemClock{}
}

// TestW1HandleFailedUpstreamResponsePolicyArms 接线真实效果桥，驱动策略
// 命中的深臂：系统配额规则（402）、账户限流规则（429）、规则读取错误。
func TestW1HandleFailedUpstreamResponsePolicyArms(t *testing.T) {
	fixture := newChainFixture(t)
	// 补齐 markCooldown 引用的生产列。
	for _, column := range []string{
		"last_error_trace_id TEXT", "cooldown_retest_failure_count INTEGER",
		"cooldown_retest_observation_started_at TEXT", "cooldown_retest_last_at TEXT",
		"cooldown_retest_last_status_code INTEGER", "cooldown_retest_generation TEXT",
		"updated_at TEXT",
	} {
		if _, err := fixture.db.Exec("ALTER TABLE accounts ADD COLUMN " + column); err != nil {
			t.Fatalf("add column %s = %v", column, err)
		}
	}
	bridge := w1EffectsBridge(t, fixture)
	dispatcher := &chainFailureDispatcher{effects: bridge}
	gatewayInput := func(account gatewaydispatch.AccountCandidate, status int, body string) gatewaydispatch.FailedUpstreamResponseInput {
		input := gatewaydispatch.FailedUpstreamResponseInput{
			Account: account, UpstreamURL: "https://u/v1/chat/completions",
			Response:                    w1UpstreamFailure(status, body),
			AccountStateMutationEnabled: true, AutomaticAccountStateMutationEnabled: true,
		}
		input.UsageContext.TrafficSource = gatewayTrafficSource
		return input
	}
	// 系统配额规则命中（402 + 配额标记）→ 显式策略 + Key 级配额记录 + 冷却写。
	quotaAccount := gatewaydispatch.AccountCandidate{ID: fixture.accountID, Type: "api_key"}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayInput(quotaAccount, http.StatusPaymentRequired,
			`{"error":{"message":"insufficient quota remaining","code":"insufficient_quota"}}`))
	if err != nil {
		t.Fatalf("quota policy = %v", err)
	}
	if result.Action == gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("策略命中必须切换候选 = %+v", result)
	}
	// 账户限流规则命中（429 + duration 策略）。
	ruleAccount := gatewaydispatch.AccountCandidate{
		ID: fixture.accountID, Type: "api_key",
		Credentials: map[string]any{
			"error_handling_rules": []any{map[string]any{
				"enabled": true, "name": "限流", "priority": 1.0, "action": "rate_limited",
				"status_codes": []any{429.0}, "reset_strategy": "duration", "duration_hours": 1.0,
			}},
		},
	}
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayInput(ruleAccount, http.StatusTooManyRequests, `{"error":{"message":"限流"}}`)); err != nil {
		t.Fatalf("account rate limit = %v", err)
	}
	// 规则读取错误：非法规则载荷 → 决策错误上抛。
	brokenAccount := gatewaydispatch.AccountCandidate{
		ID:          fixture.accountID,
		Credentials: map[string]any{"error_handling_rules": "not-a-list"},
	}
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayInput(brokenAccount, http.StatusInternalServerError, `{}`)); err == nil {
		t.Fatal("规则载荷非法必须报错")
	}
	// 停用规则（关键字命中）。
	disableAccount := gatewaydispatch.AccountCandidate{
		ID: fixture.accountID, Type: "api_key",
		Credentials: map[string]any{
			"error_handling_rules": []any{map[string]any{
				"enabled": true, "name": "停用", "priority": 1.0, "action": "error_disabled",
				"keywords": []any{"永久不可用"},
			}},
		},
	}
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayInput(disableAccount, http.StatusInternalServerError, `{"error":{"message":"账户永久不可用"}}`)); err != nil {
		t.Fatalf("disable rule = %v", err)
	}
}
