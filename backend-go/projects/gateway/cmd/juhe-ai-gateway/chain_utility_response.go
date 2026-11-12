package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayutility"
)

// transformUtilityUpstreamResponse 在成功响应上处理工具端点的厂商差异：
// 当前只有 qwen 重排需要把 DashScope {output:{results}} 归一为外部
// OpenAI/Cohere 兼容 {results:[{index,relevance_score,document?}]}。
func transformUtilityUpstreamResponse(input gatewaydispatch.UpstreamResponseTransformInput) (*gatewaydispatch.GatewayUpstreamResponse, bool, error) {
	if input.Req == nil || input.Response == nil || !input.Response.OK() || input.Response.Body == nil {
		return nil, false, nil
	}
	if chainUtilityPathKind(input.Req.PathAndQuery()) != "rerank" ||
		chainNormalizeProviderToken(input.Account.VendorCode) != "qwen" {
		return nil, false, nil
	}
	body, err := io.ReadAll(input.Response.Body)
	if err != nil {
		return nil, true, err
	}
	_ = input.Response.Body.Close()
	model := ""
	if value, ok := gatewaypreauth.RequestModel(input.Req); ok {
		model = strings.TrimSpace(value)
	}
	translated, err := gatewayutility.RerankDownstreamBody(body, model)
	if err != nil {
		return nil, true, &gatewaypreauth.GatewayRequestValidationError{
			Message:    err.Error(),
			Code:       "upstream_protocol_invalid",
			StatusCode: http.StatusBadGateway,
			Type:       "api_error",
		}
	}
	headers := input.Response.Header.Clone()
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Del("Content-Length")
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(
		input.Response.Status(), headers, io.NopCloser(bytes.NewReader(translated))), true, nil
}
