package main

// realtime 候选面显式域前门断言（AI账户上游接口能力设计 §4，五域退役修订）：
// 请求域=audio（realtime_session ∈ audio lane），账户能力域 ≠ audio 不进候选
// ——chat 域账户即使声明 realtime_session 也被域门拒绝；audio 域账户带
// realtime 语义（显式声明或空声明无约束）进候选，账户声明 opt-in 语义保留。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestRealtimeSessionCandidatesExplicitDomainGate(t *testing.T) {
	chain := &gatewayChain{}
	runtime := &gatewayruntimecache.GatewayRuntime{
		Accounts: []gatewayruntimecache.OpenAIAccountSecret{
			{ID: "acc-chat", CapabilityDomain: "chat",
				SupportedEndpointModes: []string{"chat_json"}, APIKey: "sk-chat"},
			// 修复前该账户凭 realtime_session 声明进候选（opt-in mode 过滤拦
			// 不住跨域账户），是域前门要堵的洞。
			{ID: "acc-chat-realtime", CapabilityDomain: "chat",
				SupportedEndpointModes: []string{"chat_json", "realtime_session"}, APIKey: "sk-chat-rt"},
			{ID: "acc-audio-declared", CapabilityDomain: "audio",
				SupportedEndpointModes: []string{"realtime_session"}, APIKey: "sk-audio"},
			// 五域退役后 audio 账户不声明 modes：空声明=无约束 opt-in 语义，
			// 域门放行后由下方 mode 过滤保持通过。
			{ID: "acc-audio-implicit", CapabilityDomain: "audio", APIKey: "sk-audio-implicit"},
			// 五域其他域账户同样不进 realtime 候选。
			{ID: "acc-image", CapabilityDomain: "image",
				SupportedEndpointModes: []string{"images_json"}, APIKey: "sk-image"},
		},
	}
	out := chain.realtimeSessionCandidates(context.Background(), runtime, "gpt-realtime")
	ids := make([]string, 0, len(out))
	for _, account := range out {
		ids = append(ids, account.ID)
	}
	if len(ids) != 2 || ids[0] != "acc-audio-declared" || ids[1] != "acc-audio-implicit" {
		t.Fatalf("域前门应只放行 audio 域账户（chat 声明 realtime / image 域均拒绝）：%v", ids)
	}
}
