package main

// M5b realtime ephemeral token 签发端点（Realtime 设计 §2/§4，本批基建面）：
// POST /v1/realtime/client_secrets——API Key 认证沿 preauth（链入口
// PreResolveGatewayRuntime 已完成 Bearer 解析、IP 黑名单与用户请求限流
// 消费），body {"model":"gpt-realtime"}，响应 OpenAI 形态
// {"value":"<token>","expires_at":<unix 秒>}。
//
// GET /v1/realtime WS 升级面（?token= 认证消费）属 M5b2 WS 桥接 handler，
// 本批不实现——协议门只放行 client_secrets 精确路径（见
// gatewayopenai/endpoint.go）。
//
// 审计沿媒体任务面短路先例（chain_v1.go serveMediaJobTaskPlane）：绑定
// API Key 身份 + 状态码 Finalize 的最小收尾，不记请求/响应 body（token
// 值不进审计）。

import (
	"context"
	"errors"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
)

// chainIsRealtimeClientSecretsPath 报告请求路径是否 realtime ephemeral token
// 签发端点（/v1/realtime/client_secrets，v1 前缀可剥；方法无关——非 POST
// 形态在 handler 内按 404 收敛，不落进派发链）。
func chainIsRealtimeClientSecretsPath(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	path := gatewaypreauth.RequestPathWithoutQuery(req)
	return chainStripGatewayVersionPrefix(path) == "/realtime/client_secrets"
}

// serveRealtimeClientSecrets 处理签发请求（chain_v1 入口短路分支：preauth
// 认证完成、body 管道解析之后，派发预检之前）。签发是网关本地动作（不发起
// 上游账户尝试），按 2026-10-16 审计收录范围收窄不再落审计；链入口 defer 的
// CancelAuditCapture 对未 Finalize 的捕获兜底。
func (c *gatewayChain) serveRealtimeClientSecrets(ctx context.Context, req *gatewaypreauth.GatewayRequest, res *gatewaypreauth.TrackingWriter) {
	if req.MethodUpper() != http.MethodPost {
		// 签发端点唯一形态是 POST；其余方法维持链面 404 JSON 契约。
		writeChainNotFound(res)
		return
	}
	if c.realtimeTokens == nil {
		c.renderRealtimeSecretsError(res, req, http.StatusServiceUnavailable, "realtime token 服务未装配（需要 redis 运行态驱动）", "service_unavailable")
		return
	}
	if req.Runtime == nil || req.Runtime.APIKey == nil {
		c.renderRealtimeSecretsError(res, req, http.StatusUnauthorized, "缺少网关 API Key 身份", "invalid_api_key")
		return
	}
	model, hasModel := gatewaypreauth.RequestModel(req)
	if !hasModel || model == "" {
		c.renderRealtimeSecretsError(res, req, http.StatusBadRequest, "请求体必须携带非空 model 字段（realtime 会话模型）", "invalid_request_body")
		return
	}
	// M5b2 补齐（Realtime 设计 §2/§4）：签发前校验 model 在 realtime 目录
	//（active + realtime 协议）——token 只对 realtime 会话模型有意义，早拒
	// 免得签出不可连接的 token。目录消费沿 runtimecache 聚合读先例（链
	// chain_realtime.go resolveRealtimeCatalogModel 同源）。
	if c.resolveRealtimeCatalogModel(ctx, req.Runtime.APIKey.SystemAccountID, model) == nil {
		c.renderRealtimeSecretsError(res, req, http.StatusBadRequest,
			"model 不在 realtime 目录（需为协议 realtime 的 active 目录模型）", "model_not_in_realtime_catalog")
		return
	}
	issued, err := c.realtimeTokens.Issue(ctx, req.Runtime.APIKey.ID, model)
	if err != nil {
		if errors.Is(err, realtimetoken.ErrInvalidInput) {
			c.renderRealtimeSecretsError(res, req, http.StatusBadRequest, "请求体必须携带非空 model 字段（realtime 会话模型）", "invalid_request_body")
			return
		}
		// 其余（存储不可用/随机源失败）按 503 显式失败，不静默降级。
		c.renderRealtimeSecretsError(res, req, http.StatusServiceUnavailable, "realtime token 签发失败", "service_unavailable")
		return
	}
	// OpenAI client_secrets 响应形态：{"value","expires_at"}（expires_at 为
	// Unix 秒）。
	c.writeRealtimeSecretsJSON(res, http.StatusOK, map[string]any{
		"value":      issued.Value,
		"expires_at": issued.ExpiresAt,
	})
}

// renderRealtimeSecretsError 渲染 openai 形态错误包（沿媒体任务面错误形态）。
func (c *gatewayChain) renderRealtimeSecretsError(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest, status int, message, code string) {
	body := map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}}
	c.writeRealtimeSecretsJSON(res, status, body)
}

// writeRealtimeSecretsJSON 写 JSON 响应（沿任务面 kernel.WriteJSON 形态）。
func (c *gatewayChain) writeRealtimeSecretsJSON(res *gatewaypreauth.TrackingWriter, status int, body map[string]any) {
	kernel.WriteJSON(res, status, body)
}
