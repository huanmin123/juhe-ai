package gatewayopenai

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 模型映射的上游路径改写契约：7 pair 协议等价矩阵（2026-11-07 跨协议映射退役）
// 下不再有任何需要改写路径的映射组合。同族改名沿用客户端路径；Gemini 原生
// stream 归一的路径/mode 处理在 chain_driver 的 gemini 原生分支。旧的跨族
// 改写（responses→chat_completions 的 /chat/completions、anthropic messages
// 目标的 /messages、gemini→chat 桥 query 清洗）已随退役删除。

func resolvedMapping(source, upstream string) *gatewayproto.ResolvedModelMapping {
	return &gatewayproto.ResolvedModelMapping{
		SourceModel:            "m",
		SourceEndpointFamily:   source,
		UpstreamModel:          "m-up",
		UpstreamEndpointFamily: upstream,
	}
}

func TestModelMappedUpstreamPathCrossFamilyNoRewrite(t *testing.T) {
	// 全部跨族组合（含已退役的 responses→chat_completions bridge）不再改写
	// 上游路径，也不报告改写发生——即使有退役行泄漏也不合成桥路径。
	cases := []struct {
		name     string
		source   string
		upstream string
		original string
	}{
		{"responses to chat (retired bridge)", FamilyResponses, FamilyChatCompletions, "/v1/responses?x=1"},
		{"responses to chat no query (retired)", FamilyResponses, FamilyChatCompletions, "/v1/responses"},
		{"chat to anthropic messages", FamilyChatCompletions, FamilyAnthropicMessages, "/v1/chat/completions?beta=1"},
		{"responses to anthropic messages", FamilyResponses, FamilyAnthropicMessages, "/v1/responses"},
		{"chat to stored anthropic vocabulary", FamilyChatCompletions, "messages", "/chat/completions?a=b"},
		{"chat to gemini generate content", FamilyChatCompletions, FamilyGeminiGenerateContent, "/v1/chat/completions?alt=sse&key=k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ModelMappedUpstreamPathAndQuery(tc.original, resolvedMapping(tc.source, tc.upstream))
			if ok || got != tc.original {
				t.Fatalf("retired cross-family pair rewrote: got %q ok=%v want %q ok=false", got, ok, tc.original)
			}
		})
	}
}

func TestModelMappedUpstreamPathNonBridgeUnchanged(t *testing.T) {
	// 同协议映射不重写。
	got, ok := ModelMappedUpstreamPathAndQuery("/chat/completions", resolvedMapping(FamilyChatCompletions, FamilyChatCompletions))
	if ok || got != "/chat/completions" {
		t.Fatalf("same-family mapping rewrote: %q ok=%v", got, ok)
	}
	// gemini stream→generate 归一（pair 5）同样不在此重写（路径/mode 归
	// chain_driver gemini 原生分支）。
	got, ok = ModelMappedUpstreamPathAndQuery("/v1beta/models/m:streamGenerateContent?alt=sse", resolvedMapping(FamilyGeminiStreamGenerate, FamilyGeminiGenerateContent))
	if ok || got != "/v1beta/models/m:streamGenerateContent?alt=sse" {
		t.Fatalf("gemini stream normalize should not rewrite here: %q ok=%v", got, ok)
	}
}
