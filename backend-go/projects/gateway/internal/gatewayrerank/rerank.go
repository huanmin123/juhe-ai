// Package gatewayrerank 承载公开重排协议（POST /v1/rerank）的请求解析、
// DashScope 适配与响应归一。
//
// 对外协议按《对外协议面对齐 new-api 设计》§4.4 固定为 Cohere/Jina 形态：
//
//	请求 {model, query, documents[]（string | {text}）, top_n?, return_documents?,
//	      max_chunk_per_doc?, overlap_tokens?}
//	响应 {results:[{index, relevance_score, document?}], usage:{prompt_tokens,total_tokens}}
//
// 上游 Jina/Cohere 直出形态直接透传（方舟 rerank 未收录）；百炼 DashScope
// /api/v1/services/rerank/text-rerank/text-rerank 的
// {model,input:{query,documents},parameters:{top_n,return_documents}} 报文由本包
// 构造，响应由本包归一（usage 只有 total_tokens，没有 prompt_tokens）。
// 本包是纯函数层，不依赖 gateway 内部其它包，便于单测与链级复用。
package gatewayrerank

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// Request 是规范化后的公开重排请求。
type Request struct {
	Model           string
	Query           string
	Documents       []Document
	TopN            *int
	ReturnDocuments bool
	MaxChunkPerDoc  *int
	OverlapTokens   *int
	// Raw 保留客户端原始 JSON 对象（通用上游透传时只改写 model）。
	Raw map[string]any
}

// Document 是 documents[] 的单条声明；Object 表示客户端传入对象形态。
type Document struct {
	Text   string
	Object bool
	Raw    any
}

// decodeJSONObject 以 UseNumber 解码对象，保留 document 元数据中大整数的
// 原始字面量（float64 会丢失 >2^53 的精度）。同时拒绝 JSON 之后的尾随内容，
// 与 json.Unmarshal 的严格性保持一致。
func decodeJSONObject(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return root, true
}

// ParseRequest 解析并校验公开重排请求。错误消息可直接作为 400 文案。
func ParseRequest(raw []byte) (Request, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Request{}, errors.New("重排请求体不能为空")
	}
	root, ok := decodeJSONObject(raw)
	if !ok {
		return Request{}, errors.New("重排请求体必须是 JSON 对象")
	}
	request := Request{Raw: root}
	if value, present := root["stream"]; present {
		// 只接受显式 false；true 与非布尔值（"true"/1）都按非法流式声明拒绝，
		// 避免 qwen 丢弃字段而 generic 透传把非法值送给 Jina/Cohere。
		if stream, ok := value.(bool); !ok || stream {
			return Request{}, errors.New("重排请求不支持流式")
		}
	}
	model, _ := root["model"].(string)
	request.Model = strings.TrimSpace(model)
	if request.Model == "" {
		return Request{}, errors.New("重排请求缺少 model")
	}
	query, _ := root["query"].(string)
	request.Query = strings.TrimSpace(query)
	if request.Query == "" {
		return Request{}, errors.New("重排请求缺少 query")
	}
	rawDocuments, ok := root["documents"].([]any)
	if !ok {
		return Request{}, errors.New("重排请求 documents 必须是数组")
	}
	if len(rawDocuments) == 0 {
		return Request{}, errors.New("重排请求 documents 不能为空")
	}
	documents := make([]Document, 0, len(rawDocuments))
	for index, item := range rawDocuments {
		switch typed := item.(type) {
		case string:
			text := strings.TrimSpace(typed)
			if text == "" {
				return Request{}, fmt.Errorf("重排请求 documents[%d] 不能为空字符串", index)
			}
			documents = append(documents, Document{Text: typed, Raw: typed})
		case map[string]any:
			text, _ := typed["text"].(string)
			if strings.TrimSpace(text) == "" {
				return Request{}, fmt.Errorf("重排请求 documents[%d] 对象缺少 text", index)
			}
			documents = append(documents, Document{Text: text, Object: true, Raw: typed})
		default:
			return Request{}, fmt.Errorf("重排请求 documents[%d] 必须是字符串或对象", index)
		}
	}
	request.Documents = documents
	if value, present := root["top_n"]; present {
		parsed, err := positiveInteger(value)
		if err != nil {
			return Request{}, errors.New("重排请求 top_n 必须是大于 0 的整数")
		}
		request.TopN = &parsed
	}
	if value, present := root["return_documents"]; present {
		parsed, ok := value.(bool)
		if !ok {
			return Request{}, errors.New("重排请求 return_documents 必须是布尔值")
		}
		request.ReturnDocuments = parsed
	}
	if value, present := root["max_chunk_per_doc"]; present {
		parsed, err := nonNegativeInteger(value)
		if err != nil {
			return Request{}, errors.New("重排请求 max_chunk_per_doc 必须是非负整数")
		}
		request.MaxChunkPerDoc = &parsed
	}
	if value, present := root["overlap_tokens"]; present {
		parsed, err := nonNegativeInteger(value)
		if err != nil {
			return Request{}, errors.New("重排请求 overlap_tokens 必须是非负整数")
		}
		request.OverlapTokens = &parsed
	}
	return request, nil
}

// RewriteModel 在通用透传报文中写入最终上游模型（未命中模型映射时与请求
// 模型相同，不改变客户端其它字段）。
func RewriteModel(raw []byte, upstreamModel string) ([]byte, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		return raw, nil
	}
	root, ok := decodeJSONObject(raw)
	if !ok {
		return nil, errors.New("重排请求体必须是 JSON 对象")
	}
	root["model"] = model
	return json.Marshal(root)
}

// BuildDashScopeRequest 构造百炼 text-rerank 请求体：
// {model,input:{query,documents[]},parameters:{top_n?,return_documents?}}。
// DashScope 的 documents 只接受字符串数组；对象文档按 text 字段展开。
// max_chunk_per_doc / overlap_tokens 是 Jina 词，不进入 DashScope 报文。
func BuildDashScopeRequest(request Request, upstreamModel string) ([]byte, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		model = request.Model
	}
	documents := make([]string, 0, len(request.Documents))
	for index, document := range request.Documents {
		if strings.TrimSpace(document.Text) == "" {
			return nil, fmt.Errorf("百炼重排 documents[%d] 缺少 text", index)
		}
		documents = append(documents, document.Text)
	}
	parameters := map[string]any{}
	if request.TopN != nil {
		parameters["top_n"] = *request.TopN
	}
	if request.ReturnDocuments {
		parameters["return_documents"] = true
	}
	payload := map[string]any{
		"model": model,
		"input": map[string]any{
			"query":     request.Query,
			"documents": documents,
		},
	}
	if len(parameters) > 0 {
		payload["parameters"] = parameters
	}
	return json.Marshal(payload)
}

// NormalizeResponse 把上游重排响应归一为 Cohere/Jina 形态：
// {results:[{index,relevance_score,document?}],usage:{prompt_tokens,total_tokens}}。
// 兼容百炼 {output:{results},usage:{total_tokens}}、Jina/方舟直出 results 以及
// Cohere {meta:{billed_units:{input_tokens}}} 的 usage 形态。top_n 与
// return_documents 在网关侧再兜底一次（上游可能忽略或默认回显文档）。
func NormalizeResponse(raw []byte, request Request) ([]byte, error) {
	root, ok := decodeJSONObject(raw)
	if !ok {
		return nil, errors.New("上游重排响应不是 JSON 对象")
	}
	rawResults, hasResults := root["results"].([]any)
	if !hasResults {
		if output, isObject := root["output"].(map[string]any); isObject {
			rawResults, hasResults = output["results"].([]any)
		}
		if !hasResults {
			return nil, errors.New("上游重排 JSON 响应结构无效：缺少 results")
		}
	}
	results := make([]any, 0, len(rawResults))
	for position, item := range rawResults {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("上游重排 results[%d] 必须是对象", position)
		}
		normalized := map[string]any{}
		index := position
		if value, ok := object["index"]; ok {
			parsed, err := nonNegativeInteger(value)
			if err != nil {
				return nil, fmt.Errorf("上游重排 results[%d].index 必须是整数", position)
			}
			index = parsed
		}
		normalized["index"] = index
		score := any(float64(0))
		if value, ok := object["relevance_score"]; ok {
			if _, ok := numberFloat(value); !ok {
				return nil, fmt.Errorf("上游重排 results[%d].relevance_score 必须是数字", position)
			}
			score = value
		}
		normalized["relevance_score"] = score
		if request.ReturnDocuments {
			// 上游回显 null/空串/空对象/仅空 text 对象等同未回显；统一按请求
			// 原文回填，避免客户端拿到违反 return_documents 语义的占位值。
			if document, hasDocument := object["document"]; hasDocument && documentPresent(document) {
				// Cohere/Jina 规范回显是 {text} 对象；非标准上游给字符串时
				// 归一为同形态，保持与请求字符串文档的回填语义一致。
				if text, isString := document.(string); isString {
					normalized["document"] = map[string]any{"text": text}
				} else {
					normalized["document"] = document
				}
			} else if document, ok := requestDocumentAt(request, index); ok {
				// index 是原 documents[] 下标，position 兜底用于未回传 index
				// 的上游。
				normalized["document"] = document
			} else if document, ok := requestDocumentAt(request, position); ok {
				normalized["document"] = document
			}
		}
		results = append(results, normalized)
	}
	if request.TopN != nil && len(results) > *request.TopN {
		// 上游可能忽略 top_n；按契约「返回相关性最高的前 N 条」先稳定按分数
		// 降序，再裁剪。上游已排序时该排序是 no-op。
		sort.SliceStable(results, func(left, right int) bool {
			return relevanceScoreOf(results[left]) > relevanceScoreOf(results[right])
		})
		results = results[:*request.TopN]
	}
	response := map[string]any{"results": results}
	if usage := normalizeUsage(root); usage != nil {
		response["usage"] = usage
	}
	return json.Marshal(response)
}

// requestDocumentAt 取请求 documents[] 的规范化回显形态：字符串文档按
// Cohere/Jina 约定包成 {text}，对象文档原样返回。
func requestDocumentAt(request Request, index int) (any, bool) {
	if index < 0 || index >= len(request.Documents) {
		return nil, false
	}
	document := request.Documents[index]
	if document.Object {
		return document.Raw, true
	}
	return map[string]any{"text": document.Text}, true
}

// documentPresent 判定上游 document 是否是可回显的有效值：nil、空字符串、
// 空对象、仅含空 text 的对象与其他类型都视为未回显，交由请求原文回填。
func documentPresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		if len(typed) == 0 {
			return false
		}
		if len(typed) == 1 {
			if text, ok := typed["text"].(string); ok {
				return strings.TrimSpace(text) != ""
			}
		}
		return true
	default:
		return false
	}
}

// relevanceScoreOf 读取归一后结果的分数（归一阶段已确认是数字）。
func relevanceScoreOf(result any) float64 {
	object, _ := result.(map[string]any)
	score, _ := numberFloat(object["relevance_score"])
	return score
}

// normalizeUsage 依次尝试 usage.prompt_tokens / usage.input_tokens /
// usage.total_tokens / meta.billed_units.input_tokens；只输出
// prompt_tokens 与 total_tokens 两个规范键。零占位（输入键显式为 0）不得
// 遮蔽后续口径的真实规模；全部口径都缺失时才返回 nil，全部显式为 0 时保留
// 零事实。
func normalizeUsage(root map[string]any) map[string]any {
	promptTokens, hasPrompt := positiveToken(root, "usage", "prompt_tokens")
	if !hasPrompt {
		promptTokens, hasPrompt = positiveToken(root, "usage", "input_tokens")
	}
	if !hasPrompt {
		promptTokens, hasPrompt = positiveToken(root, "usage", "total_tokens")
	}
	if !hasPrompt {
		promptTokens, hasPrompt = positiveBilledInputTokens(root)
	}
	if !hasPrompt {
		// 所有口径都是显式 0：按 prompt/input/total/billed 顺序保留零事实，
		// 与「完全缺失」区分（后者交给既有文本 token 自算）。
		if value, ok := tokenValue(root, "usage", "prompt_tokens"); ok {
			promptTokens, hasPrompt = value, true
		} else if value, ok := tokenValue(root, "usage", "input_tokens"); ok {
			promptTokens, hasPrompt = value, true
		} else if value, ok := tokenValue(root, "usage", "total_tokens"); ok {
			promptTokens, hasPrompt = value, true
		} else if value, ok := billedInputTokens(root); ok {
			promptTokens, hasPrompt = value, true
		}
	}
	if !hasPrompt {
		return nil
	}
	// total 显式 0 但输入有正值时，total 至少等于输入（重排无输出 token）。
	totalTokens, hasTotal := positiveToken(root, "usage", "total_tokens")
	if !hasTotal {
		totalTokens = promptTokens
	}
	return map[string]any{
		"prompt_tokens": promptTokens,
		"total_tokens":  totalTokens,
	}
}

// positiveToken 取显式非零 token；0 占位返回 false 以便继续尝试下一口径。
func positiveToken(root map[string]any, objectKey, key string) (int, bool) {
	value, ok := tokenValue(root, objectKey, key)
	if !ok || value <= 0 {
		return 0, false
	}
	return value, true
}

func positiveBilledInputTokens(root map[string]any) (int, bool) {
	value, ok := billedInputTokens(root)
	if !ok || value <= 0 {
		return 0, false
	}
	return value, true
}

func tokenValue(root map[string]any, objectKey, key string) (int, bool) {
	object, ok := root[objectKey].(map[string]any)
	if !ok {
		return 0, false
	}
	value, ok := object[key]
	if !ok {
		return 0, false
	}
	parsed, err := nonNegativeInteger(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func billedInputTokens(root map[string]any) (int, bool) {
	meta, ok := root["meta"].(map[string]any)
	if !ok {
		return 0, false
	}
	billed, ok := meta["billed_units"].(map[string]any)
	if !ok {
		return 0, false
	}
	value, ok := billed["input_tokens"]
	if !ok {
		return 0, false
	}
	parsed, err := nonNegativeInteger(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func positiveInteger(value any) (int, error) {
	parsed, err := nonNegativeInteger(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("not a positive integer")
	}
	return parsed, nil
}

func nonNegativeInteger(value any) (int, error) {
	number, ok := numberFloat(value)
	if !ok || number < 0 || number != math.Trunc(number) {
		return 0, errors.New("not a non-negative integer")
	}
	maxInt := int(^uint(0) >> 1)
	if number > float64(maxInt) {
		return 0, errors.New("not a non-negative integer")
	}
	return int(number), nil
}

// numberFloat 解析 UseNumber 解码产生的 json.Number 与既有 float64/int 形态；
// 非有限值按非法数字处理。
func numberFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

// IsRequestPath 判定路径是否为本协议的创建面（POST /v1/rerank 或 /rerank）。
func IsRequestPath(pathAndQuery string) bool {
	path := pathAndQuery
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path == "/v1/rerank" || path == "/rerank"
}
