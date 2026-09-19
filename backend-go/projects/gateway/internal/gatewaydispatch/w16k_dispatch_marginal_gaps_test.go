package gatewaydispatch

// 目的：阶段 A 拆分后根包 94.9% 距 95.0% 门槛差 2.35 条语句。本文件只覆盖
// 拆分后遗留的纯函数便宜缺口（bridge 转发谓词、客户端 IP 并发审计/文案、
// 请求体替换 helper、no-op release），不触达需要真实依赖的链路。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestW16KIsOpenAIProtocolProfileSecretBridge(t *testing.T) {
	if !isOpenAIProtocolProfileSecret(gatewayopenai.ProtocolCode, gatewayopenai.ProtocolVersion) {
		t.Fatalf("canonical openai profile should match")
	}
	// 谓词对 token 归一化（大小写/空白）不敏感。
	if !isOpenAIProtocolProfileSecret(" OpenAI ", " V1 ") {
		t.Fatalf("normalized openai profile should match")
	}
	if isOpenAIProtocolProfileSecret("gpt", "v1") {
		t.Fatalf("non-openai profile should not match")
	}
}

func TestW16KClientIPConcurrencyCopyArms(t *testing.T) {
	disabled := clientIpConcurrencyAuditMetadata(ClientIPConcurrencyDecision{Enabled: false})
	if disabled["enabled"] != false {
		t.Fatalf("disabled metadata = %+v", disabled)
	}
	acquired := clientIpConcurrencyAuditMetadata(ClientIPConcurrencyDecision{
		Enabled:                true,
		Acquired:               true,
		Current:                1,
		Limit:                  4,
		WaitedMs:               12,
		Queued:                 true,
		QueueSizeBeforeAcquire: 3,
	})
	if acquired["acquired"] != true || acquired["limit"] != 4 || acquired["queueSizeBeforeAcquire"] != 3 {
		t.Fatalf("acquired metadata = %+v", acquired)
	}
	rejected := clientIpConcurrencyAuditMetadata(ClientIPConcurrencyDecision{
		Enabled:   true,
		Acquired:  false,
		Reason:    "queue_full",
		Current:   4,
		Limit:     4,
		WaitedMs:  20,
		QueueSize: 5,
	})
	if rejected["acquired"] != false || rejected["reason"] != "queue_full" || rejected["queueSize"] != 5 {
		t.Fatalf("rejected metadata = %+v", rejected)
	}

	if msg := clientIpConcurrencyFailureMessage(ClientIPConcurrencyDecision{Enabled: true, Reason: "timeout"}); msg != "当前 IP 并发排队等待超时，请稍后重试" {
		t.Fatalf("timeout message = %q", msg)
	}
	if msg := clientIpConcurrencyFailureMessage(ClientIPConcurrencyDecision{Enabled: true, Reason: "queue_full"}); msg != "当前 IP 并发已达到分组限制，请稍后重试" {
		t.Fatalf("default message = %q", msg)
	}
}

func TestW16KGatewayReplaceJSONBodyArms(t *testing.T) {
	// nil request / nil body 早退不 panic。
	gatewayReplaceJSONBody(nil, nil)
	orphan := &gatewaypreauth.GatewayRequest{}
	gatewayReplaceJSONBody(orphan, map[string]any{"model": "ignored"})

	req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{Body: map[string]any{"model": "old"}}}
	gatewayReplaceJSONBody(req, map[string]any{"model": "new"})
	if body, ok := req.Body.Body.(map[string]any); !ok || body["model"] != "new" {
		t.Fatalf("replaced body = %+v", req.Body.Body)
	}
}

func TestW16KNoopRelease(t *testing.T) {
	noopRelease()
}
