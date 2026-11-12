package main

import (
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayutility"
)

// B7 工具端点（向量/重排/安全）：无状态 dispatch，不产生会话亲和。
//
// 上游语义：
//   - gpt：/v1/embeddings、/v1/moderations OpenAI 原样透传；
//   - qwen：向量走 OpenAI 兼容 /compatible-mode/v1/embeddings；重排把外部
//     OpenAI/Cohere 兼容 /v1/rerank 请求翻译为 DashScope text-rerank 报文；
//   - gemini 向量需 Gemini embedContent 桥，本批不开放（目录不注册）。
const chainQwenRerankPath = "/api/v1/services/rerank/text-rerank/text-rerank"

func chainUtilityPathKind(pathAndQuery string) string {
	path, _ := gatewayopenai.SplitPathAndQuery(pathAndQuery)
	normalized := strings.ToLower(chainStripGatewayVersionPrefix(path))
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	switch {
	case normalized == "/embeddings" || strings.HasPrefix(normalized, "/embeddings/"):
		return "embeddings"
	case normalized == "/rerank" || strings.HasPrefix(normalized, "/rerank/"):
		return "rerank"
	case normalized == "/moderations" || strings.HasPrefix(normalized, "/moderations/"):
		return "moderation"
	}
	return ""
}

// buildUtilityRequestParts 在通用对话/媒体分支之前构造工具端点出站请求。
// 支持 gpt 原样透传、qwen 向量兼容面、qwen 重排翻译。
func (d *chainProviderDriver) buildUtilityRequestParts(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate, kind string) (gatewaydispatch.PreparedRequestParts, error) {
	body := clientUpstreamBody(req)
	if mapping := d.resolveAccountModelMapping(account, req, ""); mapping != nil {
		rewritten, err := gatewayutility.RewriteModel(body, mapping.UpstreamModel)
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
		}
		body = rewritten
	}
	vendor := chainNormalizeProviderToken(account.VendorCode)
	switch kind {
	case "rerank":
		if vendor != "qwen" {
			return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("账户 %s 不支持当前重排请求", account.ID)
		}
		translated, err := gatewayutility.RerankUpstreamBody(body)
		if err != nil {
			return gatewaydispatch.PreparedRequestParts{}, gatewaypreauth.NewGatewayRequestValidationError(err.Error())
		}
		body = translated
	case "embeddings":
		if vendor != "gpt" && vendor != "qwen" {
			return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("账户 %s 不支持当前向量请求", account.ID)
		}
	case "moderation":
		if vendor != "gpt" {
			return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("账户 %s 不支持当前安全请求", account.ID)
		}
	default:
		return gatewaydispatch.PreparedRequestParts{}, fmt.Errorf("未知工具端点类型：%s", kind)
	}
	headers := upstreamHeadersOf(req, account)
	headers.Set("Content-Type", "application/json")
	return gatewaydispatch.PreparedRequestParts{Headers: headers, Body: body}, nil
}
