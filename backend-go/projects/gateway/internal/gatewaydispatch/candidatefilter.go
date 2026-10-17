package gatewaydispatch

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// filterOpenAIGatewayRequestCandidateAccounts, migrated from
// dispatch/candidate-filter.ts.

// CandidateFilterOutput mirrors RequestCandidateFilterResult.
type CandidateFilterOutput struct {
	// Outcome is 'accounts' | 'fallback' | 'completed'.
	Outcome       string
	Accounts      []AccountCandidate
	ModelPriority *gatewayrouting.GatewayAccountModelPriority
	// PreFilterSkipped 是能力 + 模型两层过滤的逐账户跳过明细（最终窗口
	// 之前的过滤层；W1b 续带出，见最终出口组装）。
	PreFilterSkipped []AccountSkip
	// Fallback variant
	Reason  string
	Context any
}

// CandidateFilterArgs mirrors the Node input object.
type CandidateFilterArgs struct {
	Req                                 *gatewaypreauth.GatewayRequest
	AuditCapture                        AuditCapture
	UsageContext                        gatewaypreauth.GatewayFailureUsageContext
	StartedAt                           int64
	RawCandidateAccounts                []AccountCandidate
	ClientStrategy                      gatewaypreauth.ClientStrategyContext
	SystemAccountID                     string
	APIKeyID                            string
	GroupID                             string
	ClientIP                            string
	Endpoint                            string
	BypassModelFilter                   bool
	RequestModelOverride                string
	RouteCoordinator                    gatewayrouting.GatewayRouteCoordinatorOwner
	RecoverUnavailableCandidateAccounts func(ctx context.Context) ([]AccountCandidate, error)
	LoadModelAwareCandidateAccounts     func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error)
}

// requestCapabilityMismatchMessage mirrors requestCapabilityMismatchMessage.
// model_unsupported 特判（BUG-0261）：driver 能力判定链末段按账户支持模型
// 列表拒绝时（chain_driver.go gatewayRequestCapabilityMismatchReasonFor），
// 泛化协议文案会把模型问题误报成"请求路径或客户端协议"，客户端与排障均
// 无法定位；对齐模型过滤层文案（candfilters.go GatewayModelFilterFailureMessage）
// 带出请求模型。
func requestCapabilityMismatchMessage(reason string, requestedModel string) string {
	if reason == "anthropic_native_group_openai_compatible_request" {
		return "当前 API Key 绑定的是 Anthropic 原生分组，不兼容 Codex / OpenAI 请求路径；请改用 Anthropic /v1/messages 客户端，或绑定支持 OpenAI Responses / Chat Completions 的分组"
	}
	if reason == "model_unsupported" && trimString(requestedModel) != "" {
		return "当前分组无账户支持请求模型：" + trimString(requestedModel)
	}
	return "当前分组无账户支持请求路径或客户端协议"
}

// capabilityModelNotDeclaredFailure 判定能力过滤全拒是否为「全组无人声明
// 支持请求模型」（BUG-0325 修订判据）：对能力过滤前的候选全集重跑模型
// 过滤（FilterGatewayAccountsByRequestedModel，与派发后段同一匹配口径——
// 含 volcengine TTS 等价组），过滤后无任何幸存账户 → 模型无效是请求级
// 参数错误，返回 404 model_not_found 本地终态（invalid_request_error，
// message 带请求模型名，与 GatewayModelFilterFailureMessage 文案对齐——
// OpenAI 对未知模型即 404）。该判据不依赖逐账户 reason 的词面（混编组中
// 视频等异端点账户会以 endpoint_mode_unsupported 被拒，词面全等判定会
// 漏判 404——真实复现：chat+video 混编分组请求无效模型）；只要存在模型
// 声明命中但被端点/协议拒绝的账户（模型过滤有幸存者），路径/协议语义仍
// 是主因，维持 503 现状。requestedModel 为空时不成立（model_not_found 无
// 从谈起），调用方回退既有通用文案形态。
func capabilityModelNotDeclaredFailure(rawCandidateAccounts []AccountCandidate, requestedModel, sourceEndpointFamily string) (gatewayrouting.GatewayRouteFinalFailure, bool) {
	model := trimString(requestedModel)
	if model == "" || len(rawCandidateAccounts) == 0 {
		return gatewayrouting.GatewayRouteFinalFailure{}, false
	}
	if modelFilter := FilterGatewayAccountsByRequestedModel(rawCandidateAccounts, model, sourceEndpointFamily); len(modelFilter.Accounts) > 0 {
		return gatewayrouting.GatewayRouteFinalFailure{}, false
	}
	return gatewayrouting.GatewayRouteFinalFailure{
		StatusCode: 404,
		Message:    "当前分组无账户支持请求模型：" + model,
		ErrorType:  "invalid_request_error",
		ErrorCode:  "model_not_found",
		ErrorPhase: "dispatch",
	}, true
}

// shouldReloadModelAwareCandidates mirrors shouldReloadModelAwareCandidates.
func shouldReloadModelAwareCandidates(requestedModel string, filter ModelFilterResult, loader func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error)) bool {
	return requestedModel != "" && loader != nil &&
		filter.DirectMatchedCount == 0 && filter.MappingMatchedCount == 0
}

// FilterOpenAIGatewayRequestCandidateAccounts mirrors
// filterOpenAIGatewayRequestCandidateAccounts.
func (p *CandidatePipeline) FilterOpenAIGatewayRequestCandidateAccounts(ctx context.Context, input CandidateFilterArgs) (CandidateFilterOutput, error) {
	requestedModel := trimString(input.RequestModelOverride)
	if requestedModel == "" {
		if model, ok := gatewaypreauth.RequestModel(input.Req); ok {
			requestedModel = model
		}
	}
	sourceEndpointFamily := GatewayRequestEndpointFamily(input.Req)
	rawCandidateAccounts := input.RawCandidateAccounts
	if len(rawCandidateAccounts) == 0 && requestedModel != "" && input.LoadModelAwareCandidateAccounts != nil {
		loaded, err := input.LoadModelAwareCandidateAccounts(ctx, requestedModel, sourceEndpointFamily)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if loaded != nil {
			rawCandidateAccounts = loaded
		}
	}
	if len(rawCandidateAccounts) == 0 {
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, "no_candidate_accounts")
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: "no_candidate_accounts", Context: fallback.Context}, nil
		}
		if input.RecoverUnavailableCandidateAccounts != nil {
			recovered, err := input.RecoverUnavailableCandidateAccounts(ctx)
			if err != nil {
				return CandidateFilterOutput{}, err
			}
			if recovered != nil {
				rawCandidateAccounts = recovered
			}
		}
	}

	capabilityFilter := FilterGatewayAccountsByRequestCapability(
		input.Req, rawCandidateAccounts, p.engine.Driver,
		input.ClientStrategy.RequestClientCompatibility, "",
	)
	if capabilityFilter.SkippedCount > 0 {
		// W1b：逐账户跳过明细带出（加法键；截断保护见
		// AccountSkipsAuditMetadata）。
		skippedDetail, skippedTruncated := AccountSkipsAuditMetadata(capabilityFilter.Skipped)
		input.AuditCapture.AddGatewayMetadata("account_request_capability_filter", map[string]any{
			"skippedCount":               capabilityFilter.SkippedCount,
			"remainingCount":             len(capabilityFilter.Accounts),
			"reason":                     capabilityFilter.Reason,
			"requestClientCompatibility": input.ClientStrategy.RequestClientCompatibility,
			"skipped":                    skippedDetail,
			"skippedTruncated":           skippedTruncated,
		})
	}
	if len(rawCandidateAccounts) > 0 && len(capabilityFilter.Accounts) == 0 {
		reason := capabilityFilter.Reason
		if reason == "" {
			reason = "request_capability_mismatch"
		}
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: reason, Context: fallback.Context}, nil
		}
		// BUG-0325：全拒聚合逐账户真实 driver reason。全组无人声明支持请求
		// 模型时按未知模型语义本地终态 404 model_not_found（混编组中异端点
		// 账户的 endpoint_mode_unsupported 不掩盖模型无效主因）；存在模型
		// 声明命中但被端点/协议拒绝的账户时维持 503 现状，路径/协议语义不受
		// 影响。
		if failure, ok := capabilityModelNotDeclaredFailure(rawCandidateAccounts, requestedModel, sourceEndpointFamily); ok {
			if err := input.RouteCoordinator.CompleteFailure(ctx, failure); err != nil {
				return CandidateFilterOutput{}, err
			}
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeCompleted}, nil
		}
		message := requestCapabilityMismatchMessage(reason, requestedModel)
		if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: 503,
			Message:    message,
			ErrorType:  "service_unavailable",
			ErrorCode:  reason,
			ErrorPhase: "dispatch",
		}); err != nil {
			return CandidateFilterOutput{}, err
		}
		return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeCompleted}, nil
	}

	var modelFilter ModelFilterResult
	if input.BypassModelFilter {
		modelFilter = BypassGatewayModelFilter(capabilityFilter.Accounts, sourceEndpointFamily)
	} else {
		modelFilter = FilterGatewayAccountsByRequestedModel(capabilityFilter.Accounts, requestedModel, sourceEndpointFamily)
	}
	if shouldReloadModelAwareCandidates(requestedModel, modelFilter, input.LoadModelAwareCandidateAccounts) {
		modelAwareRawAccounts, err := input.LoadModelAwareCandidateAccounts(ctx, requestedModel, sourceEndpointFamily)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if len(modelAwareRawAccounts) > 0 {
			modelAwareCapabilityFilter := FilterGatewayAccountsByRequestCapability(
				input.Req, modelAwareRawAccounts, p.engine.Driver,
				input.ClientStrategy.RequestClientCompatibility, "",
			)
			modelAwareModelFilter := FilterGatewayAccountsByRequestedModel(modelAwareCapabilityFilter.Accounts, requestedModel, sourceEndpointFamily)
			if modelAwareModelFilter.DirectMatchedCount > 0 ||
				modelAwareModelFilter.MappingMatchedCount > 0 ||
				(len(modelFilter.Accounts) == 0 && len(modelAwareModelFilter.Accounts) > 0) {
				capabilityFilter = modelAwareCapabilityFilter
				modelFilter = modelAwareModelFilter
				requestedModelAttribute := any(modelAwareModelFilter.RequestedModel)
				if requestedModelAttribute == "" {
					requestedModelAttribute = nil
				}
				familyAttribute := any(modelAwareModelFilter.SourceEndpointFamily)
				if familyAttribute == "" {
					familyAttribute = nil
				}
				reasonAttribute := any(modelAwareModelFilter.Reason)
				if reasonAttribute == "" {
					reasonAttribute = nil
				}
				input.AuditCapture.AddGatewayMetadata("account_model_candidate_window", map[string]any{
					"requestedModel":              requestedModelAttribute,
					"sourceEndpointFamily":        familyAttribute,
					"directMatchedCount":          modelAwareModelFilter.DirectMatchedCount,
					"mappingMatchedCount":         modelAwareModelFilter.MappingMatchedCount,
					"invalidModelConstraintCount": modelAwareModelFilter.InvalidModelConstraintCount,
					"remainingCount":              len(modelAwareModelFilter.Accounts),
				})
			}
		}
	}
	// SwitchTarget（切号冻结目标）：切号触发的候选窗口（组回退 preflight、
	// 模型感知 reload 重载结果）在产出前按冻结目标后置过滤；初始 preflight
	// 尚无账户完成构造（无冻结目标），此处惰性不参与（设计文档 §4）。
	if gate := SwitchTargetGateFromContext(ctx); gate != nil && gate.Frozen() {
		modelFilter.Accounts = gate.FilterAccounts(modelFilter.Accounts)
		if gate.Unresolved() && gate.MarkUnresolvedDiagnosed() {
			input.AuditCapture.AddGatewayMetadata("switch_target_unresolved", map[string]any{
				"reason":          "frozen_switch_target_unresolvable",
				"sourceAccountId": SwitchTargetGateSourceOf(gate),
				"stage":           "candidate_filter",
			})
		}
	}
	if modelFilter.SkippedCount > 0 || modelFilter.MappingMatchedCount > 0 {
		requestedModelAttribute := any(modelFilter.RequestedModel)
		if requestedModelAttribute == "" {
			requestedModelAttribute = nil
		}
		familyAttribute := any(modelFilter.SourceEndpointFamily)
		if familyAttribute == "" {
			familyAttribute = nil
		}
		reasonAttribute := any(modelFilter.Reason)
		if reasonAttribute == "" {
			reasonAttribute = nil
		}
		modelFilterMetadata := map[string]any{
			"requestedModel":              requestedModelAttribute,
			"sourceEndpointFamily":        familyAttribute,
			"skippedCount":                modelFilter.SkippedCount,
			"limitedAccountCount":         modelFilter.LimitedAccountCount,
			"invalidModelConstraintCount": modelFilter.InvalidModelConstraintCount,
			"directMatchedCount":          modelFilter.DirectMatchedCount,
			"mappingMatchedCount":         modelFilter.MappingMatchedCount,
			"remainingCount":              len(modelFilter.Accounts),
			"reason":                      reasonAttribute,
		}
		// W1b：逐账户跳过明细带出（reason 为 ModelSkipReason* 三分支稳定
		// 常量；加法键，截断保护见 AccountSkipsAuditMetadata）。
		if len(modelFilter.Skipped) > 0 {
			skippedDetail, skippedTruncated := AccountSkipsAuditMetadata(modelFilter.Skipped)
			modelFilterMetadata["skipped"] = skippedDetail
			modelFilterMetadata["skippedTruncated"] = skippedTruncated
		}
		input.AuditCapture.AddGatewayMetadata("account_model_filter", modelFilterMetadata)
	}
	if len(capabilityFilter.Accounts) > 0 && len(modelFilter.Accounts) == 0 {
		reason := modelFilter.Reason
		if reason == "" {
			reason = "unsupported_model"
		}
		fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
		if err != nil {
			return CandidateFilterOutput{}, err
		}
		if fallback.Attempted {
			return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeFallback, Reason: reason, Context: fallback.Context}, nil
		}
		message := GatewayModelFilterFailureMessage(modelFilter)
		if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			// BUG-0325 顺路对齐：模型过滤层耗尽本就是模型语义（reason
			// unsupported_model/missing_model + GatewayModelFilterFailureMessage
			// 模型文案），状态码与错误码同步对齐 OpenAI 未知模型契约
			// （404 model_not_found），不再以 503 service_unavailable 暗示可重试。
			StatusCode: 404,
			Message:    message,
			ErrorType:  "invalid_request_error",
			ErrorCode:  "model_not_found",
			ErrorPhase: "dispatch",
		}); err != nil {
			return CandidateFilterOutput{}, err
		}
		return CandidateFilterOutput{Outcome: gatewaypreauth.CandidateOutcomeCompleted}, nil
	}

	return CandidateFilterOutput{
		Outcome:       gatewaypreauth.CandidateOutcomeAccounts,
		Accounts:      modelFilter.Accounts,
		ModelPriority: modelFilter.ModelPriority,
		// W1b 续：能力 + 模型两层过滤的逐账户跳过明细随出口带出（reload
		// 分支覆盖 capabilityFilter/modelFilter 后此处取最终值），经端口透
		// 传给决策摘要；只读投影，不改变过滤行为。
		PreFilterSkipped: append(append([]AccountSkip{}, capabilityFilter.Skipped...), modelFilter.Skipped...),
	}, nil
}
