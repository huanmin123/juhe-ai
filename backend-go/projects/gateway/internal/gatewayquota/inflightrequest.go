package gatewayquota

// T93-1（C3-H1）+ T93-4（C3-H8）请求级在途额度预留：API Key 在途成本预占
// 从 preflight 后移到「选定账户、拿到 vendor 与上游映射模型之后、真正发上游
// 请求之前」，用真实请求体规模估算（preflight 阶段 provider 恒空、模型未
// 映射，估算必然目录 miss → 恒 no-op）；一次请求在临界区内原子预留，无额度
// 时不参与预留（HasEnabledRequestQuotaLimit 门）。授权/团队 scope 的在途
// 预留臂已随授权额度退役（revision 31）移除。

import (
	"context"
)

// RequestInflightReserveInput 是请求级在途预留输入：由链面在选定账户、拿到
// vendor 与上游映射模型后按真实请求事实构造。
type RequestInflightReserveInput struct {
	APIKey          APIKeyRow
	ProviderCode    string
	SystemAccountID string
	Model           string
	// ServiceTier 为空按 "default" 处理。
	ServiceTier string
	// RawBodyBytes 是真正发往上游的请求体字节数。
	RawBodyBytes int64
	// HasMaxOutputTokens / MaxOutputTokens 来自最终上游请求体。
	HasMaxOutputTokens bool
	MaxOutputTokens    int64
	// ReleaseDelayMs 覆盖默认延迟释放（测试用）。
	ReleaseDelayMs *int
}

// RequestInflightReserveOutcome 是请求级在途预留结果。
type RequestInflightReserveOutcome struct {
	Allowed             bool
	EstimatedCostUsd    float64
	HasEstimatedCostUsd bool
	// DeniedKind 在 Allowed=false 时区分被拒 scope：当前仅 api_key。
	DeniedKind  string
	Reservation *InflightReservation
}

// 预留被拒的 scope 类别。
const (
	InflightDeniedAPIKey = "api_key"
)

// RequestInflightReserver 组合 API Key scope 的请求级在途预留。
type RequestInflightReserver struct {
	inflight *InflightQuotaService
	apiKeys  *APIKeyQuotaService
}

// NewRequestInflightReserver 装配请求级在途预留器；inflight 必填，apiKeys
// 缺席时无成本快照读面（组合测试与关闭额度的部署）。
func NewRequestInflightReserver(inflight *InflightQuotaService, apiKeys *APIKeyQuotaService) *RequestInflightReserver {
	return &RequestInflightReserver{inflight: inflight, apiKeys: apiKeys}
}

// Estimate 暴露目录成本估算（无估算器或目录 miss → ok=false）。
func (s *InflightQuotaService) Estimate(ctx context.Context, input EstimateRequestInput) (float64, bool) {
	return EstimateGatewayRequestCostUsd(ctx, s.estimator, input)
}

// ReserveRequest 计算一次请求的真实成本估算，并对启用额度的 API Key scope
// 做原子在途预留。无估算、无额度时直接放行（不预留）。
func (r *RequestInflightReserver) ReserveRequest(ctx context.Context, input RequestInflightReserveInput) (RequestInflightReserveOutcome, error) {
	if r == nil || r.inflight == nil {
		return RequestInflightReserveOutcome{Allowed: true}, nil
	}
	estimatedCostUsd, ok := r.inflight.Estimate(ctx, EstimateRequestInput{
		ProviderCode:       input.ProviderCode,
		SystemAccountID:    input.SystemAccountID,
		Model:              input.Model,
		ServiceTier:        input.ServiceTier,
		RawBodyBytes:       input.RawBodyBytes,
		HasMaxOutputTokens: input.HasMaxOutputTokens,
		MaxOutputTokens:    input.MaxOutputTokens,
	})
	if !ok || estimatedCostUsd <= 0 {
		return RequestInflightReserveOutcome{Allowed: true}, nil
	}
	scopes, denied, err := r.apiKeyScope(ctx, input.APIKey, estimatedCostUsd)
	if err != nil {
		return RequestInflightReserveOutcome{}, err
	}
	if denied != nil {
		return *denied, nil
	}
	if len(scopes) == 0 {
		return RequestInflightReserveOutcome{Allowed: true, EstimatedCostUsd: estimatedCostUsd, HasEstimatedCostUsd: true}, nil
	}
	decision, err := r.inflight.ReserveScopesContext(ctx, ScopeReserveInput{
		Scopes:           scopes,
		EstimatedCostUsd: estimatedCostUsd,
		ReleaseDelayMs:   input.ReleaseDelayMs,
	})
	if err != nil {
		return RequestInflightReserveOutcome{}, err
	}
	outcome := RequestInflightReserveOutcome{
		Allowed:             decision.Allowed,
		EstimatedCostUsd:    decision.EstimatedCostUsd,
		HasEstimatedCostUsd: decision.HasEstimatedCostUsd,
	}
	if !decision.Allowed {
		outcome.DeniedKind = InflightDeniedAPIKey
		return outcome, nil
	}
	outcome.Reservation = decision.Reservation
	return outcome, nil
}

func (r *RequestInflightReserver) apiKeyScope(ctx context.Context, apiKey APIKeyRow, estimatedCostUsd float64) ([]InflightScope, *RequestInflightReserveOutcome, error) {
	if apiKey.ID == "" {
		return nil, nil, nil
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return nil, nil, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return nil, nil, nil
	}
	costs := EmptyRequestQuotaCosts()
	found := false
	if r.apiKeys != nil {
		costs, found, err = r.apiKeys.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, apiKey)
		if err != nil {
			return nil, nil, err
		}
	}
	if !found {
		if r.inflight.dbService == nil {
			r.inflight.log("gateway_api_key_inflight_quota_exact_cost_failed", map[string]any{
				"apiKeyId": apiKey.ID,
				"error":    "db service client is not configured",
			}, "API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求")
			return nil, &RequestInflightReserveOutcome{
				Allowed:             false,
				EstimatedCostUsd:    estimatedCostUsd,
				HasEstimatedCostUsd: true,
				DeniedKind:          InflightDeniedAPIKey,
			}, nil
		}
		costs, err = r.inflight.dbService.ReadAPIKeyQuotaCosts(ctx, apiKey)
		if err != nil {
			r.inflight.log("gateway_api_key_inflight_quota_exact_cost_failed", map[string]any{
				"apiKeyId": apiKey.ID,
				"error":    err.Error(),
			}, "API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求")
			return nil, &RequestInflightReserveOutcome{
				Allowed:             false,
				EstimatedCostUsd:    estimatedCostUsd,
				HasEstimatedCostUsd: true,
				DeniedKind:          InflightDeniedAPIKey,
			}, nil
		}
	}
	return []InflightScope{{
		ScopeKey:     apiKey.ID,
		Limits:       limits,
		CurrentCosts: costs,
	}}, nil, nil
}
