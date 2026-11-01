package main

// T93-1（C3-H1）+ T93-4（C3-H8）链面接线：把 gatewayquota 的请求级在途预留器
// 适配成 dispatch 引擎端口。预占点在引擎内「选定账户 + 上游映射模型 + 最终
// 上游请求体定稿之后、发上游请求之前」；API Key 与授权/团队 scope 一次原子
// 预留，释放走既有 inflight 生命周期。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
)

// chainRequestInflightQuota adapts gatewayquota.RequestInflightReserver.
type chainRequestInflightQuota struct {
	reserver *gatewayquota.RequestInflightReserver
}

// newChainRequestInflightQuota 装配端口；inflight 缺席时返回 nil（引擎保持
// 未装配语义：只做 preflight 已结算额度拒绝）。
func newChainRequestInflightQuota(inflight *gatewayquota.InflightQuotaService, apiKeys *gatewayquota.APIKeyQuotaService, authz *gatewayquota.AuthorizationQuotaService) gatewaydispatch.InflightQuotaPort {
	if inflight == nil {
		return nil
	}
	return &chainRequestInflightQuota{reserver: gatewayquota.NewRequestInflightReserver(inflight, apiKeys, authz)}
}

// ReserveUpstreamRequest implements gatewaydispatch.InflightQuotaPort.
func (c *chainRequestInflightQuota) ReserveUpstreamRequest(ctx context.Context, input gatewaydispatch.InflightQuotaReserveInput) (gatewaydispatch.InflightQuotaReserveOutcome, error) {
	outcome, err := c.reserver.ReserveRequest(ctx, gatewayquota.RequestInflightReserveInput{
		APIKey: gatewayquota.APIKeyRow{
			ID:              input.APIKeyID,
			SystemAccountID: input.SystemAccountID,
			QuotaLimitsJSON: input.APIKeyQuotaLimitsJSON,
		},
		ProviderCode:           input.ProviderCode,
		SystemAccountID:        input.SystemAccountID,
		Model:                  input.Model,
		ServiceTier:            input.ServiceTier,
		RawBodyBytes:           input.BodyBytes,
		HasMaxOutputTokens:     input.HasMaxOutputTokens,
		MaxOutputTokens:        input.MaxOutputTokens,
		GroupAuthorizationID:   input.GroupAuthorizationID,
		AccountAuthorizationID: input.AccountAuthorizationID,
	})
	if err != nil {
		return gatewaydispatch.InflightQuotaReserveOutcome{}, err
	}
	projected := gatewaydispatch.InflightQuotaReserveOutcome{
		Allowed:     outcome.Allowed,
		DeniedKind:  outcome.DeniedKind,
		EstimateUSD: outcome.EstimatedCostUsd,
		HasEstimate: outcome.HasEstimatedCostUsd,
	}
	if outcome.Reservation != nil {
		reservation := outcome.Reservation
		projected.OnComplete = func() { reservation.Complete() }
	}
	return projected, nil
}
