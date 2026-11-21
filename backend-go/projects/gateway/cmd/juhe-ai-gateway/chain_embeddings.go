package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// qwenEmbeddingsUpstreamPath 是百炼 OpenAI 兼容模式向量端点（官方
// POST /compatible-mode/v1/embeddings）。qwen base_url 语义是 origin，服务
// 路径自带 /compatible-mode/v1 全路径（与 chat 的 chainQwenChatPathPrefix 同源）。
const qwenEmbeddingsUpstreamPath = "/compatible-mode/v1/embeddings"

// chainNormalizedGatewayRequestPath 把入站 path+query 归一为剥掉可选 /v1
// 前缀的小写路径（根形态 /embeddings、/rerank 与 /v1 形态同权）。
func chainNormalizedGatewayRequestPath(pathAndQuery string) string {
	path := strings.ToLower(strings.TrimSpace(pathAndQuery))
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path == "/v1" {
		path = "/"
	}
	if strings.HasPrefix(path, "/v1/") {
		path = path[len("/v1"):]
	}
	return path
}

// chainIsEmbeddingsRequestPath 判定规范向量创建面（精确 /v1/embeddings）。
// /v1/embeddings/multimodal 是方舟私有子路径，不进入本协议适配（维持既有
// 透传语义），因此不能复用字符串前缀匹配。
func chainIsEmbeddingsRequestPath(pathAndQuery string) bool {
	return chainNormalizedGatewayRequestPath(pathAndQuery) == "/embeddings"
}

// chainProtocolRequestMethodPathAllowed 是协议门之后的精确方法/子路径守卫：
// 向量/重排只承接 POST 规范面（/embeddings、/embeddings/multimodal、/rerank），
// 非 POST 与其他 /embeddings/* 子路径按协议门既有 404 契约拒绝，避免落入
// 通用派发链产生无意义的上游请求。其余协议路径不受影响。
func chainProtocolRequestMethodPathAllowed(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	path := chainNormalizedGatewayRequestPath(req.PathAndQuery())
	switch {
	case path == "/rerank":
		return req.MethodUpper() == http.MethodPost
	case path == "/embeddings", path == "/embeddings/multimodal":
		return req.MethodUpper() == http.MethodPost
	case strings.HasPrefix(path, "/embeddings/"):
		return false
	}
	return true
}

// chainQwenEmbeddingsUpstreamURL 构造 qwen 向量上游 URL：base 为 origin 时
// 直拼 /compatible-mode/v1/embeddings；存量/自定义 base 若已带
// /compatible-mode/v1 或 /api/v1 服务根，先剥掉再拼，避免双根。
func chainQwenEmbeddingsUpstreamURL(baseURL string) (string, error) {
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
	parsed.Path = basePath + qwenEmbeddingsUpstreamPath
	return parsed.String(), nil
}

// chainCanonicalProtocolPathWithQuery 把已通过协议门的向量/重排请求归一为
// 小写规范路径并保留 query。协议门对路径大小写不敏感，但上游路径可能大小写
// 敏感；不能把客户端的 /v1/Embeddings、/V1/Embeddings 原样带给上游。
func chainCanonicalProtocolPathWithQuery(req *gatewaypreauth.GatewayRequest, canonicalPath string) string {
	if req == nil {
		return canonicalPath
	}
	_, query := gatewayopenai.SplitPathAndQuery(req.PathAndQuery())
	return canonicalPath + query
}

// chainEmbeddingsUpstreamURL 返回向量请求的上游 URL：qwen 走 DashScope
// 兼容模式专用路径，其余厂商走 openai 直拼（base_url 版本根 + /embeddings）。
func chainEmbeddingsUpstreamURL(account gatewaydispatch.AccountCandidate, req *gatewaypreauth.GatewayRequest) (string, error) {
	if chainNormalizeProviderToken(account.VendorCode) == "qwen" {
		return chainQwenEmbeddingsUpstreamURL(account.BaseURL)
	}
	return gatewayopenai.BuildUpstreamURL(
		account.BaseURL,
		chainCanonicalProtocolPathWithQuery(req, "/embeddings"),
	)
}

// chainEmbeddingsRequestParts 构造向量上游报文：OpenAI 形态直接透传，
// 只注入协议鉴权头与 JSON Content-Type；不进入 chat 映射/兼容性改写链。
func (d *chainProviderDriver) chainEmbeddingsRequestParts(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) (gatewaydispatch.PreparedRequestParts, bool, error) {
	if req == nil || req.MethodUpper() != http.MethodPost || !chainIsEmbeddingsRequestPath(req.PathAndQuery()) {
		return gatewaydispatch.PreparedRequestParts{}, false, nil
	}
	body := clientUpstreamBody(req)
	if len(body) == 0 {
		return gatewaydispatch.PreparedRequestParts{}, true, gatewaypreauth.NewGatewayRequestValidationError("向量请求体不能为空")
	}
	headers := upstreamHeadersOf(req, account)
	headers.Set("Content-Type", "application/json")
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, true, nil
}
