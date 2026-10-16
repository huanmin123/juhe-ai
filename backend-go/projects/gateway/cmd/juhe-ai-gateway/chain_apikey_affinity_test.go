package main

// 账户内 API Key 会话亲和（账户内APIKey会话亲和设计 §9）：cmd 侧两端口实现
// 的 Key 黏性读写语义 + 最终协议成功结算对写回句柄的消费点。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// chainApiKeyAffinityTestFingerprint 是恰好 64 位小写 hex 的合法指纹样例。
const chainApiKeyAffinityTestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestLocalSessionAffinityApiKeyFingerprintSemantics(t *testing.T) {
	local := newLocalSessionAffinity()
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{GroupID: "grp-1"}

	// 无记录写回跳过（不创建），黏性读为空。
	local.RememberApiKeyFingerprintAsync(ctx, "sess", "acc-a", chainApiKeyAffinityTestFingerprint)
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != "" {
		t.Fatalf("Key 写回不得创建记录，黏性读 = %q", got)
	}

	// claim 后：同 owner 写回生效；非法指纹被拒绝。
	if _, ok := local.ClaimAsync(ctx, "sess", "acc-a", scope); !ok {
		t.Fatal("claim 失败")
	}
	local.RememberApiKeyFingerprintAsync(ctx, "sess", "acc-a", chainApiKeyAffinityTestFingerprint)
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("同 owner 写回后黏性读 = %q", got)
	}
	local.RememberApiKeyFingerprintAsync(ctx, "sess", "acc-a", "short")
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("非法指纹应被拒绝，黏性读 = %q", got)
	}

	// 异 owner 写回跳过，不影响原 owner。
	local.RememberApiKeyFingerprintAsync(ctx, "sess", "acc-b", chainApiKeyAffinityTestFingerprint)
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-b"); got != "" {
		t.Fatalf("异 owner 写回应被跳过，黏性读 = %q", got)
	}
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("异 owner 写回不得影响原 owner，黏性读 = %q", got)
	}

	// 同 owner claim（early return）保留指纹。
	if owner, ok := local.ClaimAsync(ctx, "sess", "acc-a", scope); !ok || owner != "acc-a" {
		t.Fatalf("同 owner claim = (%q, %v)", owner, ok)
	}
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("同 owner claim 应保留 Key 指纹，黏性读 = %q", got)
	}

	// 账户级 RememberAsync 同 owner 保留指纹。
	local.RememberAsync(ctx, "sess", "acc-a", scope)
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("同 owner RememberAsync 应保留 Key 指纹，黏性读 = %q", got)
	}

	// 账户级 RememberAsync 换 owner 清除旧指纹。
	local.RememberAsync(ctx, "sess", "acc-b", scope)
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-b"); got != "" {
		t.Fatalf("owner 替换不得携带旧 Key 指纹，黏性读 = %q", got)
	}

	// ForgetAsync 整条删除（含指纹）。
	if _, ok := local.ClaimAsync(ctx, "sess2", "acc-c", scope); !ok {
		t.Fatal("claim sess2 失败")
	}
	local.RememberApiKeyFingerprintAsync(ctx, "sess2", "acc-c", chainApiKeyAffinityTestFingerprint)
	if err := local.ForgetAsync(ctx, "sess2", "acc-c"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := local.StickyApiKeyFingerprintAsync(ctx, "sess2", "acc-c"); got != "" {
		t.Fatalf("forget 后黏性读应为空，实际 %q", got)
	}
}

// TestChainSessionAffinityDispatchPortApiKeyMethods：Redis 端口适配层把两个
// 新方法委托到 AffinityService（memory driver 下走服务进程内路径）。
func TestChainSessionAffinityDispatchPortApiKeyMethods(t *testing.T) {
	service, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{
		CacheDriver:    gatewaysession.CacheDriverMemory,
		Secret:         "chain-affinity-test-secret",
		RedisNamespace: "chain-affinity-tests",
	})
	if err != nil {
		t.Fatalf("NewAffinityService: %v", err)
	}
	port := &chainSessionAffinityDispatchPort{service: service, busy: newLocalSessionAffinity()}
	ctx := context.Background()
	scope := gatewaydispatch.AffinityScope{SystemAccountID: "sys-1", GroupID: "grp-1"}

	if owner, ok := port.ClaimAsync(ctx, "sess", "acc-a", scope); !ok || owner != "acc-a" {
		t.Fatalf("claim = (%q, %v)", owner, ok)
	}
	port.RememberApiKeyFingerprintAsync(ctx, "sess", "acc-a", chainApiKeyAffinityTestFingerprint)
	if got := port.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != chainApiKeyAffinityTestFingerprint {
		t.Fatalf("端口适配层黏性读 = %q", got)
	}
	if err := port.ForgetAsync(ctx, "sess", "acc-a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := port.StickyApiKeyFingerprintAsync(ctx, "sess", "acc-a"); got != "" {
		t.Fatalf("forget 后黏性读应为空，实际 %q", got)
	}
}

// TestConfirmProtocolSuccessSideEffectsInvokesApiKeyFingerprintWriteBack：
// 写回句柄只在 ProtocolValidatedSuccess=true 的最终成功结算消费一次。
func TestConfirmProtocolSuccessSideEffectsInvokesApiKeyFingerprintWriteBack(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	calls := 0
	result := gatewaydispatch.UpstreamDispatchResult{
		Account:                         gatewaydispatch.AccountCandidate{ID: "acc_w1v"},
		ConfirmSessionApiKeyFingerprint: func() { calls++ },
	}

	// 协议未验证成功：不消费写回句柄。
	loop.confirmProtocolSuccessSideEffects(context.Background(), result, gatewayresponse.UpstreamResponseHandlingResult{})
	if calls != 0 {
		t.Fatalf("协议未成功不得消费写回句柄，调用次数 = %d", calls)
	}

	// 协议验证成功：消费一次。
	loop.confirmProtocolSuccessSideEffects(context.Background(), result, gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true})
	if calls != 1 {
		t.Fatalf("协议成功应恰好消费一次写回句柄，调用次数 = %d", calls)
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("结算不应渲染响应：%+v", sink.inputs)
	}
}

// 编译面守护：w1fAffinityPort（最小 fake）必须持续满足端口接口，
// failureDispatchAffinity 同理——端口演进时此处先失败。
var (
	_ gatewaydispatch.SessionAffinityPort = (*w1fAffinityPort)(nil)
	_ gatewaydispatch.SessionAffinityPort = (*failureDispatchAffinity)(nil)
)
