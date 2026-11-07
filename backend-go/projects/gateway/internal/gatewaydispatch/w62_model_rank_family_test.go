package gatewaydispatch

// W62 方案B（2026-10-24 用户裁决）：同族改名映射（source 与 upstream endpoint
// family 相同）与直连同权 rank 0。2026-11-07 跨协议映射退役后不再有 rank 1
// 桥臂——跨协议映射在运行时解析即被拒绝（拒绝面见 entry_mappings 写侧矩阵
// 与 gatewayopenai 运行时判定）。权威口径：docs/functions/AI账户模型映射选择
// 规则.md §2.4。全部纯函数/内存构造，不连数据库。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestFilterGatewayAccountsByRequestedModelFamilyArms 覆盖同一请求模型下
// 同族改名 / 跨协议映射拒绝 / 直连三个 rank 判定臂（SQL 候选窗口的同构语义
// 由 cmd/juhe-ai-gateway 的 w62_model_rank_family_test.go 以真实 seed 验证）。
// 2026-11-07 跨协议退役后不再有 rank 1 桥臂：responses -> chat 映射在运行时
// 解析即被拒绝，持有该映射的账户不进入候选。
func TestFilterGatewayAccountsByRequestedModelFamilyArms(t *testing.T) {
	sameFamilyRename := AccountCandidate{
		ID:              "acc_same_family_rename",
		SupportedModels: []string{"up-alias"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "up-alias",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyResponses,
			Enabled:                true,
		}},
	}
	crossProtocol := AccountCandidate{
		ID:              "acc_responses_chat_bridge",
		SupportedModels: []string{"up-cross"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "up-cross",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}
	direct := AccountCandidate{ID: "acc_direct", SupportedModels: []string{"client-model"}}

	result := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{sameFamilyRename, crossProtocol, direct},
		"client-model", gatewayrouting.EndpointFamilyResponses)

	gotIDs := make([]string, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		gotIDs = append(gotIDs, account.ID)
	}
	if len(gotIDs) != 2 || gotIDs[0] != "acc_same_family_rename" || gotIDs[1] != "acc_direct" {
		t.Fatalf("accounts = %v, want [acc_same_family_rename acc_direct]（跨协议映射账户被拒绝，同族改名与直连同秩保留）", gotIDs)
	}
	ranks := result.ModelPriority.RankByAccountID
	if ranks["acc_same_family_rename"] != ModelPriorityRankDirect {
		t.Fatalf("same-family rename rank = %d, want %d (ties with direct)", ranks["acc_same_family_rename"], ModelPriorityRankDirect)
	}
	if ranks["acc_direct"] != ModelPriorityRankDirect {
		t.Fatalf("direct rank = %d, want %d", ranks["acc_direct"], ModelPriorityRankDirect)
	}
	if result.MappingMatchedCount != 1 || result.DirectMatchedCount != 1 {
		t.Fatalf("counts = direct:%d mapping:%d, want direct:1 mapping:1",
			result.DirectMatchedCount, result.MappingMatchedCount)
	}
}

// TestW62ModelFilterKeepsRankOrderNotBucketOrder 输出序按秩稳定排序而非
// 「直连桶+映射桶」分类拼接（T62 生产案例：装配序在前的同秩映射账户不得被
// 桶序压到全部直连账户之后——下游保序层以本数组顺序为基准层序）。
func TestW62ModelFilterKeepsRankOrderNotBucketOrder(t *testing.T) {
	sameFamilyRename := AccountCandidate{
		ID:              "acc_rename_first",
		SupportedModels: []string{"up-alias"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "up-alias",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyResponses,
			Enabled:                true,
		}},
	}
	directA := AccountCandidate{ID: "acc_direct_a", SupportedModels: []string{"client-model"}}
	directB := AccountCandidate{ID: "acc_direct_b", SupportedModels: []string{"client-model"}}
	bridge := AccountCandidate{
		ID:              "acc_cross_last",
		SupportedModels: []string{"up-cross"},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "up-cross",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}

	// 输入序 = 上游装配序：同秩映射账户在最前（比较器已按超级优先置顶）。
	// 2026-11-07 跨协议退役：跨协议映射账户在解析即被拒绝，不进入输出序；
	// 输出序继续锁定「同秩继承上游装配序」不按桶拼接。
	result := FilterGatewayAccountsByRequestedModel(
		[]AccountCandidate{sameFamilyRename, directA, directB, bridge},
		"client-model", gatewayrouting.EndpointFamilyResponses)

	got := make([]string, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		got = append(got, account.ID)
	}
	t.Logf("ranks = %v", result.ModelPriority.RankByAccountID)
	want := []string{"acc_rename_first", "acc_direct_a", "acc_direct_b"}
	if len(got) != len(want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("filtered order = %v, want %v (rank-0 mapping keeps assembly order)", got, want)
		}
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
