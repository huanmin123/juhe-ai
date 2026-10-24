package gatewaydispatch

// W62 方案B（2026-10-24 用户裁决）：同族改名映射（source 与 upstream endpoint
// family 相同）与直连同权 rank 0；跨协议转换映射（两族不同）保留 rank 1。
// 权威口径：docs/functions/AI账户模型映射选择规则.md §2.4。全部纯函数/内存
// 构造，不连数据库。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestFilterGatewayAccountsByRequestedModelFamilyArms 覆盖同一请求模型下
// 同族改名 / 跨协议 / 直连三个 rank 判定臂（SQL 候选窗口的同构语义由
// cmd/juhe-ai-gateway 的 w62_model_rank_family_test.go 以真实 seed 验证）。
func TestFilterGatewayAccountsByRequestedModelFamilyArms(t *testing.T) {
	sameFamilyRename := AccountCandidate{
		ID:              "acc_same_family_rename",
		SupportedModels: []string{"up-alias"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "up-alias",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}
	crossProtocol := AccountCandidate{
		ID:              "acc_cross_protocol",
		SupportedModels: []string{"up-cross"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "up-cross",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyMessages,
			Enabled:                true,
		}},
	}
	direct := AccountCandidate{ID: "acc_direct", SupportedModels: []string{"client-model"}}

	result := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{sameFamilyRename, crossProtocol, direct},
		"client-model", gatewayrouting.EndpointFamilyChatCompletions)

	if got := len(result.Accounts); got != 3 {
		t.Fatalf("accounts = %d, want 3 (same-family rename + cross protocol + direct)", got)
	}
	ranks := result.ModelPriority.RankByAccountID
	if ranks["acc_same_family_rename"] != ModelPriorityRankDirect {
		t.Fatalf("same-family rename rank = %d, want %d (ties with direct)", ranks["acc_same_family_rename"], ModelPriorityRankDirect)
	}
	if ranks["acc_cross_protocol"] != ModelPriorityRankMapping {
		t.Fatalf("cross-protocol rank = %d, want %d", ranks["acc_cross_protocol"], ModelPriorityRankMapping)
	}
	if ranks["acc_direct"] != ModelPriorityRankDirect {
		t.Fatalf("direct rank = %d, want %d", ranks["acc_direct"], ModelPriorityRankDirect)
	}
	if result.MappingMatchedCount != 2 || result.DirectMatchedCount != 1 {
		t.Fatalf("counts = direct:%d mapping:%d, want direct:1 mapping:2",
			result.DirectMatchedCount, result.MappingMatchedCount)
	}
}

// TestFilterGatewayAccountsByRequestedModelSameFamilyIdentityFallsToDirect：
// 同名同族恒等映射行仍被 IsAdmissibleModelMapping 拒绝，账户按直接匹配路径
// 处理（rank 0，DirectMatchedCount 计数），不进入映射臂。
func TestFilterGatewayAccountsByRequestedModelSameFamilyIdentityFallsToDirect(t *testing.T) {
	identityMapping := AccountCandidate{
		ID:              "acc_identity_mapping",
		SupportedModels: []string{"client-model"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "client-model",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}
	result := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{identityMapping},
		"client-model", gatewayrouting.EndpointFamilyChatCompletions)
	if got := result.ModelPriority.RankByAccountID["acc_identity_mapping"]; got != ModelPriorityRankDirect {
		t.Fatalf("identity-mapping account rank = %d, want %d (direct path)", got, ModelPriorityRankDirect)
	}
	if result.DirectMatchedCount != 1 || result.MappingMatchedCount != 0 {
		t.Fatalf("counts = direct:%d mapping:%d, want direct:1 mapping:0",
			result.DirectMatchedCount, result.MappingMatchedCount)
	}
}
