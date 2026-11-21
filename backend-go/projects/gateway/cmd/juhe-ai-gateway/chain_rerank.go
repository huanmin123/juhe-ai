package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrerank"
)

// maxRerankResponseBytes 与 gatewayresponse 非流式协议校验窗口（1MiB）对齐：
// 归一化阶段先用有界读取，避免异常上游/超大 documents 回显把完整响应无界
// 读入内存后又被后续校验拒绝。
const maxRerankResponseBytes = 1 << 20

// qwenRerankUpstreamPath 是百炼 text-rerank 服务路径（官方
// POST /api/v1/services/rerank/text-rerank/text-rerank，设计 §4.4 置信度已按
// 官方文档回填）。qwen base_url 语义是 origin（同 qwen chat / api/v1 媒体
// opPath 的双根特例），服务路径自带 /api/v1 全路径。
const qwenRerankUpstreamPath = "/api/v1/services/rerank/text-rerank/text-rerank"

// chainQwenRerankUpstreamURL 构造 qwen 重排上游 URL：base 为 origin 时直拼
// 服务全路径；存量/自定义 base 若带 /compatible-mode/v1 或 /api/v1 服务根，
// 先剥掉再拼，避免出现 .../compatible-mode/v1/api/v1/... 的双根。
func chainQwenRerankUpstreamURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", gatewaypreauth.NewGatewayRequestValidationError("qwen 账户 base_url 无效: " + baseURL)
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/compatible-mode/v1", "/api/v1"} {
		if strings.HasSuffix(basePath, suffix) {
			basePath = strings.TrimSuffix(basePath, suffix)
			break
		}
	}
	parsed.Path = basePath + qwenRerankUpstreamPath
	return parsed.String(), nil
}

// chainRerankUpstreamURL 返回重排请求的上游 URL：qwen 走 DashScope 专用
// 服务路径，其余厂商走 openai 直拼（base_url 即版本根 + 规范小写 /rerank）。
func chainRerankUpstreamURL(account gatewaydispatch.AccountCandidate, req *gatewaypreauth.GatewayRequest) (string, error) {
	if chainNormalizeProviderToken(account.VendorCode) == "qwen" {
		return chainQwenRerankUpstreamURL(account.BaseURL)
	}
	return gatewayopenai.BuildUpstreamURL(
		account.BaseURL,
		chainCanonicalProtocolPathWithQuery(req, "/rerank"),
	)
}

// chainRerankRequestParts 构造重排上游报文：请求先经 gatewayrerank 统一校验
// （400 本地终态），qwen 账户转换成 DashScope {model,input,parameters} 报文，
// 其余厂商保留客户端 Cohere/Jina 报文只做 model 原值透传。重排/向量不在
// 账户模型映射矩阵内（映射仅覆盖 7 个对话/媒体协议等价 pair），请求 model
// 即上游 model，不做映射改写。
func (d *chainProviderDriver) chainRerankRequestParts(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (gatewaydispatch.PreparedRequestParts, bool, error) {
	if req == nil || req.MethodUpper() != http.MethodPost || !gatewayrerank.IsRequestPath(req.PathAndQuery()) {
		return gatewaydispatch.PreparedRequestParts{}, false, nil
	}
	raw := clientUpstreamBody(req)
	parsed, err := gatewayrerank.ParseRequest(raw)
	if err != nil {
		return gatewaydispatch.PreparedRequestParts{}, true, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	upstreamModel := parsed.Model
	var body []byte
	if chainNormalizeProviderToken(account.VendorCode) == "qwen" {
		body, err = gatewayrerank.BuildDashScopeRequest(parsed, upstreamModel)
	} else {
		body, err = gatewayrerank.RewriteModel(raw, upstreamModel)
	}
	if err != nil {
		return gatewaydispatch.PreparedRequestParts{}, true, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
	}
	headers := upstreamHeadersOf(req, account)
	headers.Set("Content-Type", "application/json")
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, true, nil
}

// chainRerankNormalizeResponse 在链路响应变换阶段把上游重排响应归一为
// {results,usage}。qwen 的 DashScope 响应由 {output:{results},usage:{total_tokens}}
// 转成规范形态；Jina/Cohere 直出形态也统一补齐 usage.prompt_tokens 与
// return_documents/top_n 语义。非重排路径不进本函数。
func chainRerankNormalizeResponse(
	req *gatewaypreauth.GatewayRequest,
	response *gatewaydispatch.GatewayUpstreamResponse,
) (*gatewaydispatch.GatewayUpstreamResponse, error) {
	if req == nil || response == nil || !response.OK() || response.Body == nil {
		return response, nil
	}
	request, err := gatewayrerank.ParseRequest(clientUpstreamBody(req))
	if err != nil {
		// 请求在派发前已校验；此处失败说明请求体缓存缺失，按上游响应原样
		// 透传由协议校验层裁决，避免把可诊断的上游响应替换成本地错误。
		return response, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRerankResponseBytes+1))
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	if len(raw) > maxRerankResponseBytes {
		return nil, fmt.Errorf("重排响应超过 %d 字节协议窗口", maxRerankResponseBytes)
	}
	normalized, err := gatewayrerank.NormalizeResponse(raw, request)
	if err != nil {
		return nil, err
	}
	headers := response.Header.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json; charset=utf-8")
	headers.Del("Content-Length")
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(
		response.Status(),
		headers,
		io.NopCloser(strings.NewReader(string(normalized))),
	), nil
}
