package gatewayrouting

import (
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

// Gateway account model priority ranks (dispatch/model-filter.ts
// gatewayAccountModelPriorityRank).
const (
	gatewayAccountModelPriorityRankDirect      = 0
	gatewayAccountModelPriorityRankMapping     = 1
	gatewayAccountModelPriorityRankUnsupported = 2
)

// Model filter reasons (dispatch/model-filter.ts).
const (
	ModelFilterReasonMissingModel     = "missing_model"
	ModelFilterReasonUnsupportedModel = "unsupported_model"
)

// GatewayAccountModelPriority mirrors GatewayAccountModelPriority: the
// per-account model rank map produced alongside the filtered accounts.
type GatewayAccountModelPriority struct {
	RequestedModel        string
	SourceEndpointFamily  string
	RankByAccountID       map[string]int
}

// GatewayModelAccountFilterResult mirrors GatewayModelAccountFilterResult.
type GatewayModelAccountFilterResult struct {
	Accounts                   []UpstreamAccount
	SkippedCount               int
	LimitedAccountCount        int
	InvalidModelConstraintCount int
	DirectMatchedCount         int
	MappingMatchedCount        int
	RequestedModel             string
	SourceEndpointFamily       string
	ModelPriority              GatewayAccountModelPriority
	Reason                     string
}

// FilterAccountsByRequestedModel mirrors filterGatewayAccountsByRequestedModel
// (dispatch/model-filter.ts): direct supported-model matches first, then
// account model-mapping matches whose upstream model is itself supported.
// The mapping resolution rules come from
// gatewayopenai.ResolveAccountModelMapping (Node
// resolveOpenAIAccountModelMapping) so both layers share one source of truth.
func FilterAccountsByRequestedModel(accounts []UpstreamAccount, requestedModel string, sourceEndpointFamily string) GatewayModelAccountFilterResult {
	model := trimModel(requestedModel)
	var skippedCount, limitedAccountCount, invalidModelConstraintCount int
	var directMatchedCount, mappingMatchedCount int
	directMatchedAccounts := make([]UpstreamAccount, 0, len(accounts))
	mappingMatchedAccounts := make([]UpstreamAccount, 0, len(accounts))
	rankByAccountID := make(map[string]int)

	for _, account := range accounts {
		supportedModels := account.SupportedModels
		if len(supportedModels) == 0 {
			invalidModelConstraintCount++
			skippedCount++
			rankByAccountID[account.ID] = gatewayAccountModelPriorityRankUnsupported
			continue
		}
		limitedAccountCount++
		mapping := gatewayopenai.ResolveAccountModelMapping(account.runtimeAccount(), model, sourceEndpointFamily)
		if mapping != nil {
			if isMappingAllowedBySupportedModels(mapping.UpstreamModel, supportedModels) {
				mappingMatchedCount++
				rankByAccountID[account.ID] = gatewayAccountModelPriorityRankMapping
				mappingMatchedAccounts = append(mappingMatchedAccounts, account)
			} else {
				skippedCount++
				rankByAccountID[account.ID] = gatewayAccountModelPriorityRankUnsupported
			}
			continue
		}
		if resolveGatewayAccountModelMatch(model, supportedModels) {
			directMatchedCount++
			rankByAccountID[account.ID] = gatewayAccountModelPriorityRankDirect
			directMatchedAccounts = append(directMatchedAccounts, account)
			continue
		}
		skippedCount++
		rankByAccountID[account.ID] = gatewayAccountModelPriorityRankUnsupported
	}

	filtered := make([]UpstreamAccount, 0, len(directMatchedAccounts)+len(mappingMatchedAccounts))
	filtered = append(filtered, directMatchedAccounts...)
	filtered = append(filtered, mappingMatchedAccounts...)

	reason := ""
	if skippedCount > 0 && len(filtered) == 0 {
		if model != "" {
			reason = ModelFilterReasonUnsupportedModel
		} else {
			reason = ModelFilterReasonMissingModel
		}
	}

	return GatewayModelAccountFilterResult{
		Accounts:                    filtered,
		SkippedCount:                skippedCount,
		LimitedAccountCount:         limitedAccountCount,
		InvalidModelConstraintCount: invalidModelConstraintCount,
		DirectMatchedCount:          directMatchedCount,
		MappingMatchedCount:         mappingMatchedCount,
		RequestedModel:              model,
		SourceEndpointFamily:        sourceEndpointFamily,
		ModelPriority: GatewayAccountModelPriority{
			RequestedModel:       model,
			SourceEndpointFamily: sourceEndpointFamily,
			RankByAccountID:      rankByAccountID,
		},
		Reason: reason,
	}
}

// runtimeAccount projects the account onto gatewayopenai.RuntimeAccount for
// mapping resolution.
func (a *UpstreamAccount) runtimeAccount() *gatewayopenai.RuntimeAccount {
	if a == nil {
		return nil
	}
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(a.ModelMappings))
	for _, item := range a.ModelMappings {
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            item.SourceModel,
			SourceEndpointFamily:   item.SourceEndpointFamily,
			UpstreamModel:          item.UpstreamModel,
			UpstreamEndpointFamily: item.UpstreamEndpointFamily,
			Enabled:                item.Enabled,
			RuntimeSource:          item.RuntimeSource,
			RuntimeRouteRuleID:     item.RuntimeRouteRuleID,
		})
	}
	return &gatewayopenai.RuntimeAccount{
		ModelMappings:             mappings,
		ProviderCode:              a.ProviderCode,
		ProviderProtocolProfileID: a.ProviderProtocolProfileID,
		ProtocolCode:              a.ProtocolCode,
		ProtocolVersion:           a.ProtocolVersion,
	}
}

// isMappingAllowedBySupportedModels mirrors isMappingAllowedBySupportedModels.
func isMappingAllowedBySupportedModels(upstreamModel string, supportedModels []string) bool {
	if len(supportedModels) == 0 {
		return false
	}
	for _, model := range supportedModels {
		if model == upstreamModel {
			return true
		}
	}
	return false
}

// resolveGatewayAccountModelMatch mirrors resolveGatewayAccountModelMatch:
// only strict direct membership counts.
func resolveGatewayAccountModelMatch(requestedModel string, supportedModels []string) bool {
	if requestedModel == "" {
		return false
	}
	for _, model := range supportedModels {
		if model == requestedModel {
			return true
		}
	}
	return false
}

// GatewayModelFilterFailureMessage mirrors gatewayModelFilterFailureMessage
// (Chinese text preserved byte-for-byte).
func GatewayModelFilterFailureMessage(result GatewayModelAccountFilterResult) string {
	if result.Reason == ModelFilterReasonMissingModel {
		return "请求缺少 model，当前分组内账户均需要按支持模型匹配，无法调度"
	}
	requested := result.RequestedModel
	if requested == "" {
		requested = "未知模型"
	}
	return fmt.Sprintf("当前分组无账户支持请求模型：%s", requested)
}

// GatewayAccountModelPriorityFor mirrors gatewayAccountModelPriority:
// unranked accounts default to the unsupported rank.
func GatewayAccountModelPriorityFor(account UpstreamAccount, priority *GatewayAccountModelPriority) int {
	if priority == nil {
		return gatewayAccountModelPriorityRankDirect
	}
	if rank, ok := priority.RankByAccountID[account.ID]; ok {
		return rank
	}
	return gatewayAccountModelPriorityRankUnsupported
}

// CompareGatewayAccountModelPriority mirrors compareGatewayAccountModelPriority.
func CompareGatewayAccountModelPriority(left, right UpstreamAccount, priority *GatewayAccountModelPriority) int {
	return GatewayAccountModelPriorityFor(left, priority) - GatewayAccountModelPriorityFor(right, priority)
}

// trimModel mirrors `requestedModel?.trim()` at the filter boundary.
func trimModel(value string) string {
	return trimSpace(value)
}
