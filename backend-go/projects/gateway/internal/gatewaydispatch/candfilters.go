package gatewaydispatch

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Candidate account filters, migrated from dispatch/account-capability-filter.ts
// and dispatch/model-filter.ts. The mapping resolution is the migrated
// gatewayopenai.ResolveAccountModelMapping (protocol model-mapping.ts); the
// filter bookkeeping mirrors dispatch/model-filter.ts exactly.

// AccountSkip 是候选过滤器带出的单账户跳过明细（W1b 调度可解释性）：
// 被跳账户 ID + 稳定机器原因（常量字符串，JSON 形状即审计/日志形状）。
type AccountSkip struct {
	AccountID string `json:"id"`
	Reason    string `json:"reason"`
}

// 候选过滤跳过原因的稳定常量。模型过滤三个 continue 分支一一对应
// （dispatch/model-filter.ts 语义）；能力过滤自 BUG-0325 起带出逐账户真实
// driver reason（model_unsupported / endpoint_mode_unsupported 等），
// capability_mismatch 降级为 driver 单账户 reason 判空时的回退常量
// （client-compatibility 门在复数 reason 端口里以空兼容类判定，可能判空）。
const (
	// CapabilitySkipReasonMismatch：driver 单账户 reason 判空时的能力拒绝
	// 回退标记（BUG-0325 前为唯一折叠值，保留以兼容既有消费点）。
	CapabilitySkipReasonMismatch = "capability_mismatch"
	// ModelSkipReasonUnsupportedConstraint：账户未配置支持模型约束
	// （SupportedModels 为空，invalidModelConstraint 分支）。
	ModelSkipReasonUnsupportedConstraint = "unsupported_model_constraint"
	// ModelSkipReasonUnsupportedMapping：模型映射已解析，但映射后的上游模型
	// 不在该账户支持模型内（isMappingAllowedBySupportedModels 为 false）。
	ModelSkipReasonUnsupportedMapping = "unsupported_model_mapping"
	// ModelSkipReasonModelNotMatched：无映射且请求模型不在账户支持模型内
	//（直接匹配失败分支）。
	ModelSkipReasonModelNotMatched = "model_not_matched"
)

// dispatchCandidateSkipMetadataCap 限制随审计标签出站的逐账户明细条数：
// 大分组不得爆炸审计记录；截断由 skippedTruncated 布尔键表达。
const dispatchCandidateSkipMetadataCap = 20

// AccountSkipsAuditMetadata 把过滤器跳过明细投影为审计 metadata 值
// （[{id, reason}...]，最多 dispatchCandidateSkipMetadataCap 条），并返回
// 是否发生截断。
func AccountSkipsAuditMetadata(skips []AccountSkip) ([]map[string]any, bool) {
	if len(skips) == 0 {
		return nil, false
	}
	capped := skips
	truncated := false
	if len(skips) > dispatchCandidateSkipMetadataCap {
		capped = skips[:dispatchCandidateSkipMetadataCap]
		truncated = true
	}
	out := make([]map[string]any, 0, len(capped))
	for _, skip := range capped {
		out = append(out, map[string]any{"id": skip.AccountID, "reason": skip.Reason})
	}
	return out, truncated
}

// CapabilityFilterResult mirrors GatewayAccountCapabilityFilterResult.
type CapabilityFilterResult struct {
	Accounts     []AccountCandidate
	SkippedCount int
	Reason       string
	// Skipped 是逐账户跳过明细（与 SkippedCount 同一事实的展开；W1b 追加
	// 字段，零值（nil）不改变既有调用方与 JSON/metadata 形状）。
	Skipped []AccountSkip
}

// FilterGatewayAccountsByRequestCapability mirrors
// filterGatewayAccountsByRequestCapability.
func FilterGatewayAccountsByRequestCapability(
	req *gatewaypreauth.GatewayRequest,
	accounts []AccountCandidate,
	driver ProviderDriver,
	requestClientCompatibility string,
	requestModelOverride string,
) CapabilityFilterResult {
	capabilityReq := req
	if trimString(requestModelOverride) != "" {
		capabilityReq = gatewayRequestWithModelOverride(req, requestModelOverride)
	}
	filtered := make([]AccountCandidate, 0, len(accounts))
	var skipped []AccountSkip
	for _, account := range accounts {
		if driver.AccountSupportsGatewayRequest(capabilityReq, account, requestClientCompatibility) {
			filtered = append(filtered, account)
			continue
		}
		// BUG-0325：带出逐账户真实 driver reason，混编能力域分组全拒时聚合
		// 处可分辨模型拒绝与路径/协议拒绝（此前折叠为 capability_mismatch，
		// model_unsupported 特判文案永远轮不到）。
		skipped = append(skipped, AccountSkip{AccountID: account.ID, Reason: capabilitySkipReasonForAccount(driver, capabilityReq, account)})
	}
	skippedCount := len(accounts) - len(filtered)
	result := CapabilityFilterResult{Accounts: filtered, SkippedCount: skippedCount, Skipped: skipped}
	if len(accounts) > 0 && len(filtered) == 0 {
		result.Reason = driver.GatewayRequestCapabilityMismatchReason(capabilityReq, accounts)
	}
	return result
}

// capabilitySkipReasonForAccount 解析单账户能力拒绝的真实 driver reason
// （BUG-0325）：ProviderDriver 端口只暴露复数版
// GatewayRequestCapabilityMismatchReason，单账户形态经单元素切片复用
// （chain driver 的复数版对单元素即逐账户判定），接口签名与 cmd 实现不动。
// client-compatibility 门在复数形态里以空兼容类判定，账户可能判空；空时
// 回退 CapabilitySkipReasonMismatch，保证审计明细 reason 恒非空。
func capabilitySkipReasonForAccount(driver ProviderDriver, req *gatewaypreauth.GatewayRequest, account AccountCandidate) string {
	if reason := trimString(driver.GatewayRequestCapabilityMismatchReason(req, []AccountCandidate{account})); reason != "" {
		return reason
	}
	return CapabilitySkipReasonMismatch
}

// gatewayRequestWithModelOverride mirrors gatewayRequestWithModelOverride: a
// shallow view of the request whose body model is replaced.
func gatewayRequestWithModelOverride(req *gatewaypreauth.GatewayRequest, model string) *gatewaypreauth.GatewayRequest {
	targetModel := trimString(model)
	if targetModel == "" {
		return req
	}
	clone := *req
	if req.HTTP != nil && req.HTTP.Context() != nil {
		httpClone := req.HTTP.Clone(req.HTTP.Context())
		clone.HTTP = httpClone
	}
	if req.Body != nil {
		bodyClone := *req.Body
		if source, ok := gatewaybodyJSONObject(req); ok {
			updated := make(map[string]any, len(source)+1)
			for key, value := range source {
				updated[key] = value
			}
			updated["model"] = targetModel
			bodyClone.Body = updated
		} else {
			bodyClone.Body = map[string]any{"model": targetModel}
		}
		if bodyClone.State != nil {
			stateClone := *bodyClone.State
			stateModel := targetModel
			stateClone.Model = &stateModel
			bodyClone.State = &stateClone
		}
		clone.Body = &bodyClone
	}
	return &clone
}

func gatewaybodyJSONObject(req *gatewaypreauth.GatewayRequest) (map[string]any, bool) {
	if req == nil || req.Body == nil {
		return nil, false
	}
	if object, ok := req.Body.Body.(map[string]any); ok {
		return object, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Model filter (dispatch/model-filter.ts)
// ---------------------------------------------------------------------------

// Model priority ranks mirror gatewayAccountModelPriorityRank. 方案B
// （2026-10-24）：rank 1 仅留给跨协议转换映射（两族不同）；同族改名映射与
// 直接命中同权记 rank 0（见 FilterGatewayAccountsByRequestedModel 映射臂）。
const (
	ModelPriorityRankDirect      = 0
	ModelPriorityRankMapping     = 1
	ModelPriorityRankUnsupported = 2
)

// ModelFilterResult mirrors GatewayModelAccountFilterResult.
type ModelFilterResult struct {
	Accounts                    []AccountCandidate
	SkippedCount                int
	LimitedAccountCount         int
	InvalidModelConstraintCount int
	DirectMatchedCount          int
	MappingMatchedCount         int
	RequestedModel              string
	SourceEndpointFamily        string
	ModelPriority               *gatewayrouting.GatewayAccountModelPriority
	Reason                      string
	// Skipped 是逐账户跳过明细，reason 取 ModelSkipReason* 稳定常量（W1b
	// 追加字段，零值（nil）不改变既有调用方与 JSON/metadata 形状）。
	Skipped []AccountSkip
}

// FilterGatewayAccountsByRequestedModel mirrors
// filterGatewayAccountsByRequestedModel.
func FilterGatewayAccountsByRequestedModel(
	accounts []AccountCandidate,
	requestedModel string,
	sourceEndpointFamily string,
) ModelFilterResult {
	model := trimString(requestedModel)
	var skippedCount, limitedAccountCount, invalidModelConstraintCount int
	var directMatchedCount, mappingMatchedCount int
	var skipped []AccountSkip
	rankByAccountID := make(map[string]int, len(accounts))
	// 方案B（2026-10-24）：单遍过滤、命中即按输入序保留——同族改名映射与直连
	// 同秩（rank 0）后，输出序必须继承上游装配序（比较器已按 tier 排好），不得
	// 按「直连桶在前、映射桶在后」分类重拼：桶拼接会摧毁同秩内的上游位置，把
	// 秩 0 映射账户压到全部直连账户之后（T62 生产案例：tokenrhythm 超级优先被
	// 桶序垫底）。下游各保序层（latency/proxyhealth/clientip/热质量展平）以本
	// 数组顺序为基准层序。
	filtered := make([]AccountCandidate, 0, len(accounts))

	for _, account := range accounts {
		supportedModels := account.SupportedModels
		if len(supportedModels) == 0 {
			invalidModelConstraintCount++
			skippedCount++
			rankByAccountID[account.ID] = ModelPriorityRankUnsupported
			skipped = append(skipped, AccountSkip{AccountID: account.ID, Reason: ModelSkipReasonUnsupportedConstraint})
			continue
		}
		limitedAccountCount++
		mapping := resolveAccountModelMapping(account, model, sourceEndpointFamily)
		if mapping != nil {
			if isMappingAllowedBySupportedModels(mapping.UpstreamModel, supportedModels) {
				mappingMatchedCount++
				// 恒等映射已被 IsAdmissibleModelMapping 拒绝，故此处同族必为改名映射。
				if mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily {
					rankByAccountID[account.ID] = ModelPriorityRankDirect
				} else {
					rankByAccountID[account.ID] = ModelPriorityRankMapping
				}
				filtered = append(filtered, account)
			} else {
				skippedCount++
				rankByAccountID[account.ID] = ModelPriorityRankUnsupported
				skipped = append(skipped, AccountSkip{AccountID: account.ID, Reason: ModelSkipReasonUnsupportedMapping})
			}
			continue
		}
		if resolveGatewayAccountModelMatch(model, supportedModels) {
			directMatchedCount++
			rankByAccountID[account.ID] = ModelPriorityRankDirect
			filtered = append(filtered, account)
			continue
		}
		skippedCount++
		rankByAccountID[account.ID] = ModelPriorityRankUnsupported
		skipped = append(skipped, AccountSkip{AccountID: account.ID, Reason: ModelSkipReasonModelNotMatched})
	}

	requestedModelOut := ""
	if model != "" {
		requestedModelOut = model
	}
	result := ModelFilterResult{
		Accounts:                    filtered,
		SkippedCount:                skippedCount,
		Skipped:                     skipped,
		LimitedAccountCount:         limitedAccountCount,
		InvalidModelConstraintCount: invalidModelConstraintCount,
		DirectMatchedCount:          directMatchedCount,
		MappingMatchedCount:         mappingMatchedCount,
		RequestedModel:              requestedModelOut,
		SourceEndpointFamily:        sourceEndpointFamily,
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{
			RequestedModel:       requestedModelOut,
			SourceEndpointFamily: sourceEndpointFamily,
			RankByAccountID:      rankByAccountID,
		},
	}
	if skippedCount > 0 && len(filtered) == 0 {
		if model != "" {
			result.Reason = "unsupported_model"
		} else {
			result.Reason = "missing_model"
		}
	}
	return result
}

// BypassGatewayModelFilter mirrors bypassGatewayModelFilter.
func BypassGatewayModelFilter(accounts []AccountCandidate, sourceEndpointFamily string) ModelFilterResult {
	rankByAccountID := make(map[string]int, len(accounts))
	limitedAccountCount := 0
	for _, account := range accounts {
		rankByAccountID[account.ID] = ModelPriorityRankDirect
		if len(account.SupportedModels) > 0 {
			limitedAccountCount++
		}
	}
	return ModelFilterResult{
		Accounts:             accounts,
		LimitedAccountCount:  limitedAccountCount,
		DirectMatchedCount:   len(accounts),
		SourceEndpointFamily: sourceEndpointFamily,
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{
			SourceEndpointFamily: sourceEndpointFamily,
			RankByAccountID:      rankByAccountID,
		},
	}
}

// GatewayModelFilterFailureMessage mirrors gatewayModelFilterFailureMessage.
func GatewayModelFilterFailureMessage(result ModelFilterResult) string {
	if result.Reason == "missing_model" {
		return "请求缺少 model，当前分组内账户均需要按支持模型匹配，无法调度"
	}
	requested := result.RequestedModel
	if requested == "" {
		requested = "未知模型"
	}
	return "当前分组无账户支持请求模型：" + requested
}

// GatewayAccountModelPriorityFor mirrors gatewayAccountModelPriority.
func GatewayAccountModelPriorityFor(accountID string, priority *gatewayrouting.GatewayAccountModelPriority) int {
	if priority == nil {
		return ModelPriorityRankDirect
	}
	if rank, ok := priority.RankByAccountID[accountID]; ok {
		return rank
	}
	return ModelPriorityRankUnsupported
}

// isMappingAllowedBySupportedModels 判定映射产出的上游模型是否在账户声明
// supportedModels 内：严格相等之外，volcengine TTS 占位名↔现役名等价组
// （modelalias.go）同样放行；等价组之外的匹配行为不变。
func isMappingAllowedBySupportedModels(upstreamModel string, supportedModels []string) bool {
	if len(supportedModels) == 0 {
		return false
	}
	for _, model := range supportedModels {
		if gatewayDispatchModelNamesMatch(upstreamModel, model) {
			return true
		}
	}
	return false
}

// resolveGatewayAccountModelMatch 判定请求模型是否被账户声明 supportedModels
// 直接持有：严格相等之外，volcengine TTS 占位名↔现役名等价组（modelalias.go）
// 同样命中；等价组之外的匹配行为不变。
func resolveGatewayAccountModelMatch(requestedModel string, supportedModels []string) bool {
	if requestedModel == "" {
		return false
	}
	for _, model := range supportedModels {
		if gatewayDispatchModelNamesMatch(requestedModel, model) {
			return true
		}
	}
	return false
}

// resolveAccountModelMapping adapts gatewayopenai.ResolveAccountModelMapping
// (the migrated protocol resolver) to the runtime secret.
func resolveAccountModelMapping(account AccountCandidate, requestedModel, sourceEndpointFamily string) *gatewayproto.ResolvedModelMapping {
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(account.ModelMappings))
	for _, mapping := range account.ModelMappings {
		enabled := mapping.Enabled
		runtimeSource := ""
		if mapping.RuntimeSource != nil {
			runtimeSource = *mapping.RuntimeSource
		}
		runtimeRouteRuleID := ""
		if mapping.RuntimeRouteRuleID != nil {
			runtimeRouteRuleID = *mapping.RuntimeRouteRuleID
		}
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          runtimeSource,
			RuntimeRouteRuleID:     runtimeRouteRuleID,
		})
	}
	runtimeAccount := &gatewayopenai.RuntimeAccount{
		ModelMappings: mappings,
	}
	return gatewayopenai.ResolveAccountModelMapping(runtimeAccount, requestedModel, sourceEndpointFamily)
}

// ---------------------------------------------------------------------------
// Endpoint family (protocols/openai-v1/model-mapping.ts gatewayRequestEndpointFamily)
// ---------------------------------------------------------------------------

// GatewayRequestEndpointFamily mirrors gatewayRequestEndpointFamily(req):
// the OpenAI family wins, then anthropic messages, then gemini. M4b 媒体映射
// （媒体设计 §9 hybrid 行）：chat/responses/messages 之后的媒体创建形态归
// 媒体映射族（POST /v1/videos → video_generation、POST /v1/audio/speech →
// tts），让候选过滤/模型感知加载与构造侧 requestMappingSourceFamilyOf 用
// 同一词表解析媒体映射（此前空族使映射账户在模型过滤恒 miss）。
func GatewayRequestEndpointFamily(req *gatewaypreauth.GatewayRequest) string {
	if family := openAIRequestEndpointFamily(req); family != "" {
		return family
	}
	if family := anthropicMessagesRequestEndpointFamily(req); family != "" {
		return family
	}
	if family := mediaRequestEndpointFamily(req); family != "" {
		return family
	}
	return geminiRequestEndpointFamilyOf(req)
}

// mediaRequestEndpointFamily maps the media creation request shapes onto the
// M4b media mapping families. 任务面 GET/DELETE 不归族（账户亲和直连，不经
// 映射解析）；STT 转写形态本批不设映射族，维持空。
func mediaRequestEndpointFamily(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.MethodUpper() != "POST" {
		return ""
	}
	path := strings.ToLower(trimString(splitPath(req.PathAndQuery())))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = stripV1Prefix(path)
	switch path {
	case "/videos":
		return gatewayopenai.FamilyVideoGeneration
	case "/audio/speech":
		return gatewayopenai.FamilyTts
	}
	return ""
}

func openAIRequestEndpointFamily(req *gatewaypreauth.GatewayRequest) string {
	endpoint := splitPath(req.PathAndQuery())
	return openAIEndpointFamilyFromPath(endpoint)
}

func openAIEndpointFamilyFromPath(value string) string {
	path := strings.ToLower(trimString(value))
	if path == "" {
		return ""
	}
	if strings.Contains(path, "/chat/completions") {
		return gatewayrouting.EndpointFamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return gatewayrouting.EndpointFamilyResponses
	}
	return ""
}

func anthropicMessagesRequestEndpointFamily(req *gatewaypreauth.GatewayRequest) string {
	if req.MethodUpper() != "POST" {
		return ""
	}
	endpoint := splitPath(req.PathAndQuery())
	normalizedPath := endpoint
	if !strings.HasPrefix(normalizedPath, "/") {
		normalizedPath = "/" + normalizedPath
	}
	normalizedPath = stripV1Prefix(normalizedPath)
	if normalizedPath == "/messages" {
		return gatewayrouting.EndpointFamilyMessages
	}
	return ""
}

func geminiRequestEndpointFamilyOf(req *gatewaypreauth.GatewayRequest) string {
	if req.MethodUpper() != "POST" {
		return ""
	}
	endpoint := splitPath(req.PathAndQuery())
	family := gatewaygemini.EndpointFamilyFromPath(endpoint)
	familyText := string(family)
	if familyText == "" || familyText == "models" {
		return ""
	}
	return familyText
}

func splitPath(value string) string {
	parts := strings.SplitN(value, "?", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return value
}
