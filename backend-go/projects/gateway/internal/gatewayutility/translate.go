// Package gatewayutility 承载 B7 工具端点（向量/重排/安全）的纯报文翻译：
// 不依赖 HTTP、存储或账户状态，便于在任意平台单测。链上层只负责按账户厂商
// 选择透传/翻译分支与注入鉴权头。
package gatewayutility

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RewriteModel 把请求体 JSON 对象的 model 字段改写为目标上游模型名。
func RewriteModel(body []byte, model string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("工具端点请求体必须是 JSON 对象")
	}
	object["model"] = model
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// RerankUpstreamBody 把外部 OpenAI/Cohere 兼容
// {model,query,documents,top_n,return_documents} 翻译为 DashScope
// {model,input:{query,documents},parameters:{...}}。documents 接受字符串数组或
// {text} 对象数组，原样透传。
func RerankUpstreamBody(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("重排请求体必须是 JSON 对象")
	}
	query, ok := request["query"]
	if !ok || strings.TrimSpace(fmt.Sprint(query)) == "" {
		return nil, fmt.Errorf("重排请求缺少 query")
	}
	documents, ok := request["documents"]
	if !ok {
		return nil, fmt.Errorf("重排请求缺少 documents")
	}
	input := map[string]any{"query": query, "documents": documents}
	parameters := map[string]any{}
	for _, key := range []string{"top_n", "return_documents"} {
		if value, exists := request[key]; exists {
			parameters[key] = value
		}
	}
	upstream := map[string]any{"model": request["model"], "input": input}
	if len(parameters) > 0 {
		upstream["parameters"] = parameters
	}
	encoded, err := json.Marshal(upstream)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// RerankDownstreamBody 把 DashScope {output:{results:[...]}} 归一为外部
// {results:[{index,relevance_score,document?}], model?, usage?}。
func RerankDownstreamBody(body []byte, model string) ([]byte, error) {
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("qwen 重排响应不是 JSON 对象")
	}
	output, _ := upstream["output"].(map[string]any)
	rawResults, _ := output["results"].([]any)
	if len(rawResults) == 0 {
		return nil, fmt.Errorf("qwen 重排响应缺少 output.results")
	}
	results := make([]map[string]any, 0, len(rawResults))
	for _, item := range rawResults {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result := map[string]any{
			"index":           entry["index"],
			"relevance_score": entry["relevance_score"],
		}
		if document, exists := entry["document"]; exists {
			result["document"] = document
		}
		results = append(results, result)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("qwen 重排响应没有有效结果")
	}
	downstream := map[string]any{"results": results}
	if model != "" {
		downstream["model"] = model
	}
	if usage, ok := upstream["usage"]; ok {
		downstream["usage"] = normalizeRerankUsage(usage)
	}
	return json.Marshal(downstream)
}

// normalizeRerankUsage 把 DashScope 只给 total_tokens 的 usage 补成可计费形态：
// 网关计费按输入 token 单价结算重排，缺少 input_tokens/prompt_tokens 会让
// usage 被当作全零丢弃。原字段保留，仅补充 input_tokens。
func normalizeRerankUsage(value any) any {
	usage, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if _, hasInput := usage["input_tokens"]; hasInput {
		return usage
	}
	if _, hasPrompt := usage["prompt_tokens"]; hasPrompt {
		return usage
	}
	total, ok := usage["total_tokens"]
	if !ok {
		return usage
	}
	usage["input_tokens"] = total
	return usage
}
