package gatewayquota

// T93-1（C3-H1）+ T93-4（C3-H8）请求级在途额度预留：
//   - API Key 在途成本预占从 preflight 后移到「选定账户、拿到 vendor 与上游
//     映射模型之后、真正发上游请求之前」，用真实请求体规模估算（preflight
//     阶段 provider 恒空、模型未映射，估算必然目录 miss → 恒 no-op）；
//   - 授权（group_authorization / account_authorization）与团队授权
//     （*_team）scope 复用同一在途状态表与同一释放生命周期（30 分钟泄漏兜底
//     + Complete 后延迟释放），不新造第二套；
//   - 一次请求的全部 scope（API Key + 授权/团队）在同一临界区内原子预留：
//     任一 scope 投影超限即整体拒绝且不留下部分预留；
//   - 无额度的 scope 不参与预留（HasEnabledRequestQuotaLimit 门）。

import (
	"context"
	"strings"
	"time"
)

// authorizationInflightScopePrefix 是授权/团队 scope 在进程内状态表的键前缀
// （与 API Key 直键隔离，避免与 api key id 冲突）。
const authorizationInflightScopePrefix = "authz:"

// AuthorizationInflightScopeKey 由授权额度检查的 cacheKey（已含 systemAccount、
// scopeType、scopeID、窗口与 limits_json 事实）构造在途状态键：同一窗口内
// 同一 scope 的并发请求共享累加，跨窗口自然分键。
func AuthorizationInflightScopeKey(cacheKey string) string {
	return authorizationInflightScopePrefix + cacheKey
}

func isAuthorizationInflightScopeKey(stateKey string) bool {
	return strings.HasPrefix(stateKey, authorizationInflightScopePrefix)
}

// BuildInflightScopes 返回 (groupAuthorizationID, accountAuthorizationID)
// 组合下所有启用额度 scope 的在途预留描述（T93-4）：与
// CheckAuthorizationQuotaBatchByIDs 同一 scope 构造、同一已结算成本读面
// （LoadCostsBatch，窗口口径一致），只是把「是否超限」换成「limits + 当前
// 成本」交给在途预留累加。无启用额度的 scope 不返回。
func (s *AuthorizationQuotaService) BuildInflightScopes(ctx context.Context, groupAuthorizationID, accountAuthorizationID string, now time.Time) ([]InflightScope, error) {
	if groupAuthorizationID == "" && accountAuthorizationID == "" {
		return nil, nil
	}
	if now.IsZero() {
		now = s.now()
	}
	scopes := uniqueAuthorizationQuotaScopes(authorizationQuotaScopeList(groupAuthorizationID, []AccountRef{{
		AccountID:              accountAuthorizationID,
		AccountAuthorizationID: accountAuthorizationID,
	}}))
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return nil, err
	}
	costChecksByScope, err := s.loadCostChecksByScope(ctx, scopes, now, location)
	if err != nil {
		return nil, err
	}
	checks := uniqueAuthorizationQuotaCostChecks(costChecksByScope)
	if len(checks) == 0 {
		return nil, nil
	}
	inputs := make([]CostInput, 0, len(checks))
	for _, check := range checks {
		inputs = append(inputs, check.costInput)
	}
	costsByKey, err := s.stats.LoadCostsBatch(ctx, inputs, location)
	if err != nil {
		return nil, err
	}
	output := make([]InflightScope, 0, len(checks))
	for _, check := range checks {
		costs := EmptyRequestQuotaCosts()
		if loaded, ok := costsByKey[CostKey(check.costInput, location)]; ok {
			costs = loaded
		}
		output = append(output, InflightScope{
			ScopeKey:     AuthorizationInflightScopeKey(check.cacheKey),
			Limits:       check.limits,
			CurrentCosts: costs,
		})
	}
	return output, nil
}

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
	// GroupAuthorizationID / AccountAuthorizationID 为空表示该 scope 不在本次
	// 请求上（无分组授权 / 账户非实例授权）。
	GroupAuthorizationID   string
	AccountAuthorizationID string
	// Now 是额度窗口时间基准；零值用服务时钟。
	Now time.Time
	// ReleaseDelayMs 覆盖默认延迟释放（测试用）。
	ReleaseDelayMs *int
}

// RequestInflightReserveOutcome 是请求级在途预留结果。
type RequestInflightReserveOutcome struct {
	Allowed             bool
	EstimatedCostUsd    float64
	HasEstimatedCostUsd bool
	// DeniedKind 在 Allowed=false 时区分被拒 scope：api_key / authorization。
	DeniedKind  string
	Reservation *InflightReservation
}

// 预留被拒的 scope 类别。
const (
	InflightDeniedAPIKey        = "api_key"
	InflightDeniedAuthorization = "authorization"
)

// RequestInflightReserver 组合 API Key 与授权/团队 scope 的请求级在途预留。
type RequestInflightReserver struct {
	inflight *InflightQuotaService
	apiKeys  *APIKeyQuotaService
	authz    *AuthorizationQuotaService
}

// NewRequestInflightReserver 装配请求级在途预留器；inflight 必填，apiKeys /
// authz 缺席时对应 scope 不预留（组合测试与关闭额度的部署）。
func NewRequestInflightReserver(inflight *InflightQuotaService, apiKeys *APIKeyQuotaService, authz *AuthorizationQuotaService) *RequestInflightReserver {
	return &RequestInflightReserver{inflight: inflight, apiKeys: apiKeys, authz: authz}
}

// Estimate 暴露目录成本估算（无估算器或目录 miss → ok=false）。
func (s *InflightQuotaService) Estimate(ctx context.Context, input EstimateRequestInput) (float64, bool) {
	return EstimateGatewayRequestCostUsd(ctx, s.estimator, input)
}

// ReserveRequest 计算一次请求的真实成本估算，并对全部启用额度的 scope
// （API Key + 授权/团队）做原子在途预留。无估算、无额度或无 scope 时直接
// 放行（不预留）。
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
	scopes, denied, err := r.collectScopes(ctx, input, estimatedCostUsd)
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
		outcome.DeniedKind = InflightDeniedAuthorization
		if decision.DeniedScopeKey != "" && !isAuthorizationInflightScopeKey(decision.DeniedScopeKey) {
			outcome.DeniedKind = InflightDeniedAPIKey
		}
		return outcome, nil
	}
	outcome.Reservation = decision.Reservation
	return outcome, nil
}

// collectScopes 汇总本次请求涉及的额度 scope。返回 denied 非 nil 表示 API Key
// 成本快照读取失败的保护性拒绝（与既有 ReserveGatewayCost 语义一致：读不到
// 已结算成本时拒绝而不是放行）。
func (r *RequestInflightReserver) collectScopes(ctx context.Context, input RequestInflightReserveInput, estimatedCostUsd float64) ([]InflightScope, *RequestInflightReserveOutcome, error) {
	scopes := make([]InflightScope, 0, 4)
	if input.APIKey.ID != "" {
		limits, err := ParseRequestQuotaLimitsJSON(input.APIKey.QuotaLimitsJSON)
		if err != nil {
			return nil, nil, err
		}
		if HasEnabledRequestQuotaLimit(limits) {
			scope, denied, err := r.apiKeyScope(ctx, input.APIKey, limits, estimatedCostUsd)
			if err != nil || denied != nil {
				return nil, denied, err
			}
			scopes = append(scopes, scope)
		}
	}
	if r.authz != nil {
		authzScopes, err := r.authz.BuildInflightScopes(ctx, input.GroupAuthorizationID, input.AccountAuthorizationID, input.Now)
		if err != nil {
			return nil, nil, err
		}
		scopes = append(scopes, authzScopes...)
	}
	return scopes, nil, nil
}

func (r *RequestInflightReserver) apiKeyScope(ctx context.Context, apiKey APIKeyRow, limits RequestQuotaLimits, estimatedCostUsd float64) (InflightScope, *RequestInflightReserveOutcome, error) {
	costs := EmptyRequestQuotaCosts()
	found := false
	var err error
	if r.apiKeys != nil {
		costs, found, err = r.apiKeys.ReadAPIKeyQuotaCostsSnapshotAsync(ctx, apiKey)
		if err != nil {
			return InflightScope{}, nil, err
		}
	}
	if !found {
		if r.inflight.dbService == nil {
			r.inflight.log("gateway_api_key_inflight_quota_exact_cost_failed", map[string]any{
				"apiKeyId": apiKey.ID,
				"error":    "db service client is not configured",
			}, "API Key 在途额度缺少成本快照且精确成本读取失败，按保护策略拒绝请求")
			return InflightScope{}, &RequestInflightReserveOutcome{
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
			return InflightScope{}, &RequestInflightReserveOutcome{
				Allowed:             false,
				EstimatedCostUsd:    estimatedCostUsd,
				HasEstimatedCostUsd: true,
				DeniedKind:          InflightDeniedAPIKey,
			}, nil
		}
	}
	return InflightScope{
		ScopeKey:     apiKey.ID,
		Limits:       limits,
		CurrentCosts: costs,
	}, nil, nil
}
