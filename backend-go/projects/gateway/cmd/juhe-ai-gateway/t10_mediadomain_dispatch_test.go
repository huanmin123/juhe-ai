package main

// T10 对话与非对话媒体调度分离落地：失败派发器与派发循环的调度域切断回归
// （对话与非对话媒体调度分离设计 §4.1/§4.2/§5）。Mock 优先、结果稳定可回放：
//
//	媒体创建失败不派发请求失败健康检查（失败响应分支 + 传输失败分支）。
//	媒体创建失败无规则命中不轮换兄弟 Key（显式 retry_next 决策仍授权）。
//	媒体创建域传输失败不记录来源级失败避让。
//	媒体创建域不等待账户恢复（可恢复等待恒关）。
//	媒体创建域的 streamServerRetry verdict 在消费面终止（不换号、不写排除集）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// t10MediaLanes 是 StatelessMediaDispatch 域的 lane 词表。
var t10MediaLanes = []string{"image", "audio", "video"}

// t10NewHealthOutboxFixture 建表后的 outbox 夹具：零行断言需要表已存在。
func t10NewHealthOutboxFixture(t *testing.T) *healthOutboxFixture {
	t.Helper()
	fixture := newHealthOutboxFixture(t, 65_000)
	if err := fixture.writer.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure outbox schema: %v", err)
	}
	return fixture
}

// t10FailedResponseInput 构造带 lane 与多 Key 账户的 gateway 失败响应输入。
func t10FailedResponseInput(t *testing.T, response *gatewaydispatch.GatewayUpstreamResponse, sink *failureDispatchAuditSink, lane string) gatewaydispatch.FailedUpstreamResponseInput {
	t.Helper()
	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.RequestLane = lane
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))
	input.Account = gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "账户一", Type: "api_key"}
	fingerprint := "fp_key_1"
	input.Account.SelectedAPIKeyFingerprint = &fingerprint
	input.Account.APIKeys = []string{"fp_key_1", "fp_key_2"}
	return input
}

// TestT10MediaDomainFailedResponseNoAutomaticRotationNoHealthDispatch：
// 媒体创建失败（无规则命中）不派发请求失败健康检查、不轮换兄弟 Key；text
// 对照组保持既有行为（派发一次 + 自动轮换授权）。
func TestT10MediaDomainFailedResponseNoAutomaticRotationNoHealthDispatch(t *testing.T) {
	for _, lane := range t10MediaLanes {
		t.Run(lane, func(t *testing.T) {
			fixture := t10NewHealthOutboxFixture(t)
			sink := &failureDispatchAuditSink{}
			dispatcher := &chainFailureDispatcher{healthDispatch: fixture.dispatcher()}
			result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
				t10FailedResponseInput(t, failureDispatchUpstreamResponse(t, http.StatusInternalServerError,
					"application/json", `{"error":{"message":"boom"}}`), sink, lane))
			if err != nil {
				t.Fatalf("handle failed response: %v", err)
			}
			if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
				t.Fatalf("action = %s, want skip_account（统一候选边界不变）", result.Action)
			}
			if result.KeyScopedFailure {
				t.Fatal("无规则命中的媒体失败不得轮换兄弟 Key")
			}
			if rows := fixture.count(t); rows != 0 {
				t.Fatalf("媒体创建失败不得派发请求失败健康检查, rows = %d", rows)
			}
		})
	}

	// text 对照组：健康检查派发一次 + 自动兄弟 Key 轮换授权（既有行为）。
	fixture := t10NewHealthOutboxFixture(t)
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{healthDispatch: fixture.dispatcher()}
	textResult, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		t10FailedResponseInput(t, failureDispatchUpstreamResponse(t, http.StatusInternalServerError,
			"application/json", `{"error":{"message":"boom"}}`), sink, "text"))
	if err != nil {
		t.Fatalf("text 对照组: %v", err)
	}
	if !textResult.KeyScopedFailure {
		t.Fatal("text 对照组必须保持自动兄弟 Key 轮换授权")
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("text 对照组必须派发一次健康检查, rows = %d", rows)
	}
}

// TestT10MediaDomainExplicitRetryNextStillRotates：媒体创建失败命中显式
// retry_next 规则时，兄弟 Key 轮换授权保留（设计 §4.1：显式错误策略决策仍
// 生效），健康检查派发仍被媒体域切断。
func TestT10MediaDomainExplicitRetryNextStillRotates(t *testing.T) {
	fixture := t10NewHealthOutboxFixture(t)
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{
		policy:         newFixedErrorPolicyService(nil),
		healthDispatch: fixture.dispatcher(),
	}
	input := t10FailedResponseInput(t, failureDispatchUpstreamResponse(t, http.StatusInternalServerError,
		"application/json", `{"error":{"message":"上游系统过载，请稍后重试"}}`), sink, "video")
	input.Account.Credentials = map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "过载关键字", "priority": float64(1), "action": "retry_next",
			"keywords": []any{"系统过载"},
		}},
	}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed response: %v", err)
	}
	if !result.KeyScopedFailure {
		t.Fatal("显式 retry_next 决策必须保留媒体域兄弟 Key 轮换授权")
	}
	if rows := fixture.count(t); rows != 0 {
		t.Fatalf("媒体域显式决策同样不派发健康检查, rows = %d", rows)
	}
}

// TestT10MediaDomainTransportFailureNoHealthDispatchNoSourceAvoidance：
// 媒体创建域传输失败不派发健康检查、不记录来源级失败避让；text 对照组照常。
func TestT10MediaDomainTransportFailureNoHealthDispatchNoSourceAvoidance(t *testing.T) {
	for _, lane := range t10MediaLanes {
		t.Run(lane, func(t *testing.T) {
			fixture := t10NewHealthOutboxFixture(t)
			sink := &failureDispatchAuditSink{}
			dispatcher, _, adapter := newAvoidanceDispatcherForTest()
			dispatcher.healthDispatch = fixture.dispatcher()
			input := avoidanceRecordRequestInput(t, sink, "gateway")
			input.RequestLane = lane

			for _, attempt := range []string{"attempt_1", "attempt_2"} {
				input.AuditAttemptID = attempt
				if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
					t.Fatalf("transport failure: %v", err)
				}
			}
			if rows := fixture.count(t); rows != 0 {
				t.Fatalf("媒体创建传输失败不得派发健康检查, rows = %d", rows)
			}
			// 来源级避让保持关闭：两次失败后排序不得激活、无失败计数。
			order, err := adapter.OrderAsync(context.Background(),
				[]gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}},
				gatewaypreauth.ClientStrategyContext{}, nil)
			if err != nil {
				t.Fatalf("avoidance order: %v", err)
			}
			if order.Applied || order.FailureCount != 0 || len(order.AvoidedAccountIDs) != 0 {
				t.Fatalf("媒体创建传输失败不得记录来源避让: %+v", order)
			}
		})
	}

	// text 对照组：健康检查派发一次。
	fixture := t10NewHealthOutboxFixture(t)
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{healthDispatch: fixture.dispatcher()}
	input := avoidanceRecordRequestInput(t, sink, "gateway")
	input.RequestLane = "text"
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("text 对照组: %v", err)
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("text 对照组必须派发一次健康检查, rows = %d", rows)
	}
}

// TestT10MediaDomainLoopSkipsRecoverableWait：媒体创建域不等待账户恢复
// （可恢复等待恒关）；text 单绑定域保持既有等待语义。
func TestT10MediaDomainLoopSkipsRecoverableWait(t *testing.T) {
	for _, lane := range t10MediaLanes {
		t.Run(lane, func(t *testing.T) {
			sink := &recordingFailureSink{}
			l := newV1TestLoop(t, sink)
			l.current.RequestLane = gatewayproto.RequestLane(lane)
			// 单绑定形态在 text 域返回 true（无备用可切 → 引擎内等待）；媒体域恒 false。
			if l.waitForRecoverableFailures(l.current) {
				t.Fatalf("lane %q 媒体创建域不得进入可恢复等待", lane)
			}
			// 强制等待置位同样被域门压住。
			l.forceRecoverableFailureWait = true
			if l.waitForRecoverableFailures(l.current) {
				t.Fatalf("lane %q 强制等待标记不得穿透媒体域门", lane)
			}
		})
	}

	sink := &recordingFailureSink{}
	l := newV1TestLoop(t, sink)
	l.current.RequestLane = gatewayproto.RequestLane("text")
	if !l.waitForRecoverableFailures(l.current) {
		t.Fatal("text 对照组单绑定必须保持可恢复等待（既有行为）")
	}
}

// TestT10MediaDomainLoopStopsStreamServerRetry：媒体创建域的响应层换号
// verdict 在消费面终止——不写排除集、不级联候选，按耗尽契约渲染终态；text
// 对照组照常排除当前账户继续循环。
func TestT10MediaDomainLoopStopsStreamServerRetry(t *testing.T) {
	for _, lane := range t10MediaLanes {
		t.Run(lane, func(t *testing.T) {
			ctx := context.Background()
			sink := &recordingFailureSink{}
			l := newV1TestLoop(t, sink)
			l.current.RequestLane = gatewayproto.RequestLane(lane)
			l.current.Accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-c"}, {ID: "acc-d"}}

			handling := bug0298SwitchHandling()
			dispatched := gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc-a"}, AuditAttemptID: "audit-1"}
			if settled := l.settleResponseStreamServerRetry(ctx, dispatched, handling); !settled {
				t.Fatalf("lane %q 媒体创建的换号 verdict 必须即停", lane)
			}
			if _, excluded := l.streamRetryExcludedAccounts["acc-a"]; excluded {
				t.Fatalf("lane %q 不得写换号排除集: %v", lane, l.streamRetryExcludedAccounts)
			}
			if len(sink.inputs) == 0 {
				t.Fatalf("lane %q 必须下发终态响应", lane)
			}
		})
	}

	// text 对照组：照常排除当前账户并继续候选循环。
	ctx := context.Background()
	sink := &recordingFailureSink{}
	l := newV1TestLoop(t, sink)
	l.current.RequestLane = gatewayproto.RequestLane("text")
	l.current.Accounts = []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-c"}, {ID: "acc-d"}}
	if settled := l.settleResponseStreamServerRetry(ctx,
		gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc-a"}, AuditAttemptID: "audit-1"},
		bug0298SwitchHandling()); settled {
		t.Fatal("text 对照组首次换号必须继续候选循环")
	}
	if _, excluded := l.streamRetryExcludedAccounts["acc-a"]; !excluded {
		t.Fatalf("text 对照组必须排除当前账户: %v", l.streamRetryExcludedAccounts)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("text 对照组不得提前终态: %v", sink.inputs)
	}
}
