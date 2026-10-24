package gatewaydispatch

// volcengine TTS 占位名↔现役名派发匹配等价（契约《媒体上游协议契约与Mock上游
// 规格》§9.2、docs/functions/火山方舟账号接入.md「TTS 目录行演进」节
// 「派发匹配等价」）：账户 supportedModels 声明 doubao-tts / seed-audio-1.0
// 任一名字时，两种名字的请求都派发命中；等价组之外的模型匹配行为零变化；
// modelMappings 映射表键解析保持精确（不享受等价）。全部纯函数/内存构造，
// 不连数据库。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 直接匹配环节：声明任一名字 + 请求任一名字 → directMatched；等价组之外
// （含大小写变体）保持既有严格匹配语义。
func TestVolcengineTTSModelAliasDispatchDirectMatch(t *testing.T) {
	cases := []struct {
		name           string
		supportedModel string
		requestModel   string
		wantMatched    bool
	}{
		{"占位声明+现役请求", "doubao-tts", "seed-audio-1.0", true},
		{"现役声明+占位请求", "seed-audio-1.0", "doubao-tts", true},
		{"占位声明+占位请求不回归", "doubao-tts", "doubao-tts", true},
		{"现役声明+现役请求不回归", "seed-audio-1.0", "seed-audio-1.0", true},
		{"组内请求侧大小写变体按EqualFold命中", "doubao-tts", "Seed-Audio-1.0", true},
		{"组内声明侧大小写变体按EqualFold命中", "Seed-Audio-1.0", "doubao-tts", true},
		{"等价组外模型仍跳过", "doubao-tts", "gpt-4o-mini", false},
		{"等价组外大小写变体零变化", "gpt-4o-mini", "GPT-4O-MINI", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accounts := []AccountCandidate{
				{ID: "a", SupportedModels: []string{tc.supportedModel}},
			}
			result := FilterGatewayAccountsByRequestedModel(accounts, tc.requestModel, gatewayrouting.EndpointFamilyChatCompletions)
			if tc.wantMatched {
				if result.DirectMatchedCount != 1 || len(result.Accounts) != 1 {
					t.Fatalf("direct = %d, accounts = %d, want direct matched", result.DirectMatchedCount, len(result.Accounts))
				}
				if len(result.Skipped) != 0 || result.SkippedCount != 0 {
					t.Fatalf("skipped = %#v, want empty", result.Skipped)
				}
				return
			}
			if len(result.Accounts) != 0 || result.DirectMatchedCount != 0 || result.MappingMatchedCount != 0 {
				t.Fatalf("accounts = %d, direct = %d, mapping = %d, want all zero", len(result.Accounts), result.DirectMatchedCount, result.MappingMatchedCount)
			}
			if result.SkippedCount != 1 || len(result.Skipped) != 1 || result.Skipped[0].Reason != ModelSkipReasonModelNotMatched {
				t.Fatalf("skipped = %#v, want single %s", result.Skipped, ModelSkipReasonModelNotMatched)
			}
		})
	}
}

// 映射放行环节同样享受等价：映射产出的 upstream 模型与声明模型命中等价组即
// 放行（mappingMatched）；映射表键解析保持精确——请求模型大小写变体不命中
// 映射键，落到直连也不匹配 → 跳过。
func TestVolcengineTTSModelAliasDispatchMappingPath(t *testing.T) {
	aliasMappingAccounts := []AccountCandidate{
		{ID: "mapping-alias", SupportedModels: []string{"seed-audio-1.0"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "doubao-tts",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}},
		{ID: "mapping-alias-reverse", SupportedModels: []string{"doubao-tts"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "seed-audio-1.0",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}},
	}
	result := FilterGatewayAccountsByRequestedModel(aliasMappingAccounts, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.MappingMatchedCount != 2 || len(result.Accounts) != 2 {
		t.Fatalf("mapping = %d, accounts = %d, want 2/2", result.MappingMatchedCount, len(result.Accounts))
	}

	exactKeyAccount := []AccountCandidate{
		{ID: "mapping-key-exact", SupportedModels: []string{"seed-audio-1.0"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "doubao-tts",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}},
	}
	exactKeyResult := FilterGatewayAccountsByRequestedModel(exactKeyAccount, "GPT-TEST", gatewayrouting.EndpointFamilyChatCompletions)
	if len(exactKeyResult.Accounts) != 0 || exactKeyResult.MappingMatchedCount != 0 || exactKeyResult.DirectMatchedCount != 0 {
		t.Fatalf("exact-key accounts = %d, mapping = %d, direct = %d, want all zero",
			len(exactKeyResult.Accounts), exactKeyResult.MappingMatchedCount, exactKeyResult.DirectMatchedCount)
	}
	if exactKeyResult.SkippedCount != 1 || len(exactKeyResult.Skipped) != 1 || exactKeyResult.Skipped[0].Reason != ModelSkipReasonModelNotMatched {
		t.Fatalf("exact-key skipped = %#v, want single %s", exactKeyResult.Skipped, ModelSkipReasonModelNotMatched)
	}
}

// 等价组判定不改变 Rank 语义：等价命中沿用 direct / mapping 既有 rank，
// 未命中仍 unsupported。方案B（2026-10-24）：同族改名映射（chat→chat）与
// direct 同权记 rank 0，rank 1 仅留给跨协议转换映射。
func TestVolcengineTTSModelAliasDispatchRankUnchanged(t *testing.T) {
	directResult := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{{ID: "direct-alias", SupportedModels: []string{"doubao-tts"}}},
		"seed-audio-1.0", gatewayrouting.EndpointFamilyChatCompletions)
	if rank := directResult.ModelPriority.RankByAccountID["direct-alias"]; rank != ModelPriorityRankDirect {
		t.Fatalf("direct-alias rank = %d, want %d", rank, ModelPriorityRankDirect)
	}

	mappingResult := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{{ID: "mapping-alias", SupportedModels: []string{"doubao-tts"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "seed-audio-1.0",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}}},
		"gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if rank := mappingResult.ModelPriority.RankByAccountID["mapping-alias"]; rank != ModelPriorityRankDirect {
		t.Fatalf("mapping-alias rank = %d, want %d (same-family rename ties with direct, plan B)", rank, ModelPriorityRankDirect)
	}

	unmatchedResult := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{{ID: "unmatched", SupportedModels: []string{"other-model"}}},
		"seed-audio-1.0", gatewayrouting.EndpointFamilyChatCompletions)
	if rank := unmatchedResult.ModelPriority.RankByAccountID["unmatched"]; rank != ModelPriorityRankUnsupported {
		t.Fatalf("unmatched rank = %d, want %d", rank, ModelPriorityRankUnsupported)
	}
}
