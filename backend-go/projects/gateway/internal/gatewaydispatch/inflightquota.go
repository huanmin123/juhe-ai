package gatewaydispatch

// T93-1（C3-H1）+ T93-4（C3-H8）在途额度预占端口：预占点从 preflight 后移到
// 「选定账户、拿到 vendor（account.VendorCode）与上游映射模型之后、真正发
// 上游请求之前」，按最终上游请求体规模估算真实成本；一次请求只预留一次
// （req.InflightQuotaReserved 幂等标记），完成/失败/流中断/panic 由既有
// inflight 生命周期释放（请求 ctx 结束触发 Complete，30 分钟泄漏兜底兜住
// 未触发 Complete 的路径）。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// 在途额度拒绝的 scope 类别（与 gatewayquota 的同名常量同值；本包不 import
// gatewayquota，端口用纯字符串跨包传递，避免调度内核依赖额度实现）。
const (
	InflightDeniedAPIKey        = "api_key"
	InflightDeniedAuthorization = "authorization"
)

// InflightQuotaReserveInput 是一次上游尝试前的在途预留输入。
type InflightQuotaReserveInput struct {
	SystemAccountID        string
	APIKeyID               string
	APIKeyQuotaLimitsJSON  string
	GroupID                string
	GroupAuthorizationID   string
	AccountID              string
	AccountAuthorizationID string
	ProviderCode           string
	// Model 是上游映射后的最终模型名（计价目录查找键）。
	Model string
	// ServiceTier 是本次上游请求的实际档位（空按 default）。
	ServiceTier string
	// BodyBytes 是最终上游请求体字节数。
	BodyBytes int64
	// HasMaxOutputTokens / MaxOutputTokens 来自最终上游请求体。
	HasMaxOutputTokens bool
	MaxOutputTokens    int64
}

// InflightQuotaReserveOutcome 是预留结果。Reservation 非 nil 时由引擎在请求
// 结束时调用 OnComplete（完成/失败/中断/panic 出口一致）。
type InflightQuotaReserveOutcome struct {
	Allowed     bool
	DeniedKind  string
	OnComplete  func()
	EstimateUSD float64
	HasEstimate bool
}

// InflightQuotaPort 由组合根以 gatewayquota.RequestInflightReserver 适配。
type InflightQuotaPort interface {
	ReserveUpstreamRequest(ctx context.Context, input InflightQuotaReserveInput) (InflightQuotaReserveOutcome, error)
}

// GatewayInflightQuotaExceededError 是请求级在途额度拒绝：整请求终态
// （不切换账户重试），由响应层按 DeniedKind 渲染 429 文案。
type GatewayInflightQuotaExceededError struct {
	DeniedKind  string
	EstimateUSD float64
	HasEstimate bool
}

func (e *GatewayInflightQuotaExceededError) Error() string {
	if e == nil {
		return "gateway inflight quota exceeded"
	}
	if e.DeniedKind == "" {
		return "gateway inflight quota exceeded"
	}
	return "gateway inflight quota exceeded: " + e.DeniedKind
}

// GatewayQuotaRejection 让压缩摘要派发（gatewaycodex）把额度拒绝渲染为 429
// 而不是 502「上游摘要请求失败」（T93-1/T93-4 文案契约与 preflight 一致）。
func (e *GatewayInflightQuotaExceededError) GatewayQuotaRejection() (status int, errorType string, code string, message string) {
	if e != nil && e.DeniedKind == InflightDeniedAuthorization {
		return 429, "rate_limit_exceeded", "rate_limit_exceeded", gatewaypreauth.AuthorizationQuotaExceededMessage
	}
	return 429, "rate_limit_exceeded", "api_key_inflight_quota_exceeded", gatewaypreauth.APIKeyQuotaExceededMessage
}

// requestAPIKeyQuotaLimitsJSON 取请求 API Key 的额度配置（preflight 与派发同源
// 于 req.Runtime.APIKey）：空串表示未配置额度。
func requestAPIKeyQuotaLimitsJSON(req *gatewaypreauth.GatewayRequest) string {
	if req == nil || req.Runtime == nil || req.Runtime.APIKey == nil {
		return ""
	}
	return derefStringPtr(req.Runtime.APIKey.QuotaLimitsJSON)
}

// reserveUpstreamInflightQuota 在最终上游请求体确定后执行一次在途预留。
// 返回 *GatewayInflightQuotaExceededError 表示额度拒绝；其余 error 是基础设施
// 错误（原样上抛）。
func (e *Engine) reserveUpstreamInflightQuota(ctx context.Context, req *gatewaypreauth.GatewayRequest, usageContext gatewaypreauth.GatewayFailureUsageContext, account AccountCandidate, body []byte, effectiveServiceTier string) error {
	if e.InflightQuota == nil || req == nil || req.InflightQuotaReserved {
		return nil
	}
	model, ok := gatewaypreauth.RequestModel(req)
	if !ok || model == "" {
		return nil
	}
	upstreamModel := model
	if mapping := resolveAccountModelMapping(account, model, GatewayRequestEndpointFamily(req)); mapping != nil && mapping.UpstreamModel != "" {
		upstreamModel = mapping.UpstreamModel
	}
	input := InflightQuotaReserveInput{
		SystemAccountID:        firstNonEmptyString(account.AccountOwnerSystemAccountID, usageContext.SystemAccountID),
		APIKeyID:               usageContext.APIKeyID,
		APIKeyQuotaLimitsJSON:  requestAPIKeyQuotaLimitsJSON(req),
		GroupID:                usageContext.GroupID,
		GroupAuthorizationID:   usageContext.GroupAuthorizationID,
		AccountID:              account.ID,
		AccountAuthorizationID: derefStringPtr(account.AccountAuthorizationID),
		ProviderCode:           account.VendorCode,
		Model:                  upstreamModel,
		ServiceTier:            effectiveServiceTier,
		BodyBytes:              int64(len(body)),
	}
	if metadata := PreparedUpstreamBodyMetadata(req, body); metadata != nil && metadata.MaxOutputTokens != nil {
		input.HasMaxOutputTokens = true
		input.MaxOutputTokens = int64(*metadata.MaxOutputTokens)
	}
	outcome, err := e.InflightQuota.ReserveUpstreamRequest(ctx, input)
	if err != nil {
		return err
	}
	if !outcome.Allowed {
		return &GatewayInflightQuotaExceededError{
			DeniedKind:  outcome.DeniedKind,
			EstimateUSD: outcome.EstimateUSD,
			HasEstimate: outcome.HasEstimate,
		}
	}
	if outcome.OnComplete == nil {
		return nil
	}
	req.InflightQuotaReserved = true
	releaseCtx := ctx
	if releaseCtx == nil && req.HTTP != nil {
		releaseCtx = req.HTTP.Context()
	}
	if releaseCtx == nil {
		// 无生命周期载体：交给 inflight 的 30 分钟泄漏兜底释放。
		return nil
	}
	onComplete := outcome.OnComplete
	// 请求退出（完成/失败/取消/panic 收尾）时释放在途预留；自启 goroutine
	// 走 safego 屏障，防止单次 panic 打进程（与 preflight 同约定）。
	safego.Go("gatewaydispatch.inflight_quota_release", func() {
		<-releaseCtx.Done()
		onComplete()
	})
	return nil
}
