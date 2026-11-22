package playground

// POST /embeddings —— 同步向量生成（设计 §3.9，v1.1）：固定派发
// POST /v1/embeddings（网关公开规范面，qwen 经派发链改写
// /compatible-mode/v1/embeddings），请求体 {model, input(归一数组),
// dimensions?, encoding_format?}；响应归一 camelCase {model, usage?, items:
// [{index, dimensions, vector}]}——完整向量回传（相似度矩阵由前端本地计算，
// §4.3），输入限制（≤10 条 × 8KB）兜住响应体积，响应读取上限 8MB 防带外。
// 无产物落盘（§3.8 暂存范围仍是「无法持续获取」的媒体产物），不检查
// artifactsReady。请求构造与响应解析是 images.go 生图链的同构精简版。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// 向量限额（设计 §3.9：数组 ≤10 条、每条 trim 后非空 ≤8192 字节；8MB 为响应
// 读取上限，防带外）。
const (
	embeddingsMaxInputs    = 10
	embeddingsTextMaxBytes = 8 * 1024
	embeddingsRespMaxBytes = 8 * 1024 * 1024
)

// embeddingsRequest 是 POST /embeddings 请求体（设计 §3.9；input 为字符串或
// 字符串数组——单字符串视为一条，RawMessage 承接双形态）。
type embeddingsRequest struct {
	APIKeyID       string          `json:"apiKeyId"`
	ModelID        string          `json:"modelId"`
	Input          json.RawMessage `json:"input"`
	Dimensions     *int            `json:"dimensions"`
	EncodingFormat string          `json:"encodingFormat"`
}

// playgroundUsage 是演练场 JSON 端点的 usage 归一（向量/重排共用，camelCase；
// usage 缺省允许——上游无 usage 字段时响应省略 usage，设计 §3.3 usage 事件
// 同口径）。
type playgroundUsage struct {
	PromptTokens int64 `json:"promptTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

// embeddingItem 是单条向量结果（dimensions = len(vector)）。
type embeddingItem struct {
	Index      int       `json:"index"`
	Dimensions int       `json:"dimensions"`
	Vector     []float64 `json:"vector"`
}

// embeddingsResponse 是 POST /embeddings 响应（设计 §3.9 归一形状）。
type embeddingsResponse struct {
	Model string           `json:"model"`
	Usage *playgroundUsage `json:"usage,omitempty"`
	Items []embeddingItem  `json:"items"`
}

// embeddingsHandler 服务 POST /embeddings（同步等待；候选校验与 images 链同
// 构：400 model_not_available / 404 account_not_found）。
func (rt *playgroundRoutes) embeddingsHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	// 10 条 × 8KB 输入的 JSON 转义余量（传输层上限，语义限额在逐条校验）。
	body, err := readJSONBody(r, 1024*1024)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req embeddingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest(fieldShapeMessage(err)))
		return
	}
	options, candidateErr := rt.resolveKeyModels(r.Context(), scope.ViewerID, strings.TrimSpace(req.APIKeyID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindEmbedding) == nil {
		writeHTTPError(w, modelNotAvailable("模型不在所选 Key 的演练场向量候选内"))
		return
	}
	payload, runErr := rt.runEmbeddings(r.Context(), scope, &req)
	if runErr != nil {
		writeHTTPError(w, runErr)
		return
	}
	writeOKPayload(w, payload)
}

// runEmbeddings 执行向量派发与响应归一（测试断言请求构造与归一行为经 Mock
// DispatchPort 回放）。
func (rt *playgroundRoutes) runEmbeddings(ctx context.Context, scope Scope, req *embeddingsRequest) (*embeddingsResponse, *HTTPError) {
	model := strings.TrimSpace(req.ModelID)
	inputs, httpErr := normalizeEmbeddingInputs(req.Input)
	if httpErr != nil {
		return nil, httpErr
	}
	payloadValues := map[string]any{"model": model, "input": inputs}
	if req.Dimensions != nil {
		if *req.Dimensions <= 0 {
			return nil, invalidRequest("dimensions 必须是正整数")
		}
		payloadValues["dimensions"] = *req.Dimensions
	}
	if format := strings.TrimSpace(req.EncodingFormat); format != "" {
		payloadValues["encoding_format"] = format
	}
	payload, _ := json.Marshal(payloadValues)
	response, err := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		APIKeyID: strings.TrimSpace(req.APIKeyID), OwnerID: scope.ViewerID, RemoteAddr: scope.RemoteAddr,
		Path: "/v1/embeddings", Method: http.MethodPost,
		Headers: map[string]string{
			"accept":       "application/json",
			"content-type": "application/json",
		},
		Body: payload,
	})
	if err != nil {
		return nil, dispatchError("向量派发失败", err)
	}
	if response == nil {
		return nil, dispatchFailed("向量派发失败")
	}
	// 有界读取（8MB+1，超限按派发失败收敛——完整向量回传体积已被输入限额兜
	// 住，超出即上游异常）；非 2xx 读错误体走错误面。
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, embeddingsRespMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("向量响应读取失败")
	}
	if len(respBody) > embeddingsRespMaxBytes {
		return nil, dispatchFailed("向量响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("向量", response.Status, respBody))
	}
	normalized, parseErr := normalizeEmbeddingsPayload(model, respBody)
	if parseErr != nil {
		return nil, dispatchFailed("向量响应归一失败: " + parseErr.Error())
	}
	return normalized, nil
}

// normalizeEmbeddingInputs 归一 input 双形态（单字符串视为一条，设计 §3.9）；
// 数组上限 10 条，逐条 trim 非空且 ≤8192 字节。null 是形态错误（null 直解
// string 会静默落空串），按字段级消息拒绝而非落入空条目路径。
func normalizeEmbeddingInputs(raw json.RawMessage) ([]string, *HTTPError) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, invalidRequest("input 必须是字符串或字符串数组")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return normalizeEmbeddingTexts([]string{single})
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, invalidRequest("input 必须是字符串或字符串数组")
	}
	if len(many) == 0 {
		return nil, invalidRequest("input 不能为空")
	}
	if len(many) > embeddingsMaxInputs {
		return nil, invalidRequest("input 超过 10 条上限")
	}
	return normalizeEmbeddingTexts(many)
}

// normalizeEmbeddingTexts 逐条 trim + 非空 + 字节上限校验（恒归一为数组出站，
// /v1/embeddings 的 input 本就接受数组形态）。
func normalizeEmbeddingTexts(values []string) ([]string, *HTTPError) {
	texts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, invalidRequest("input 条目不能为空")
		}
		if len(text) > embeddingsTextMaxBytes {
			return nil, invalidRequest("input 条目超过 8192 字节上限")
		}
		texts = append(texts, text)
	}
	return texts, nil
}

// normalizeEmbeddingsPayload 把上游 OpenAI 形态 {data:[{index,embedding}],usage:
// {prompt_tokens,total_tokens}} 归一为演练场响应（设计 §3.9）；embedding 仅按
// []float64 解——encodingFormat=base64 时上游回字符串形态，此处不转码，归一
// 失败保持原始语义（不加兼容层）。
func normalizeEmbeddingsPayload(model string, body []byte) (*embeddingsResponse, error) {
	var payload struct {
		Data []struct {
			Index     int             `json:"index"`
			Embedding json.RawMessage `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int64 `json:"prompt_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("响应不是合法 JSON")
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("响应缺少 data 数组")
	}
	items := make([]embeddingItem, 0, len(payload.Data))
	for _, entry := range payload.Data {
		// JSON null 直解 []float64 会静默落 nil 零值，与「非数字按归一失败收敛，
		// 不静默改零值」矛盾：null 字面量显式判归一失败（502 同路径）。
		if bytes.Equal(bytes.TrimSpace(entry.Embedding), []byte("null")) {
			return nil, errors.New("embedding 仅支持 float 数组形态")
		}
		var vector []float64
		if err := json.Unmarshal(entry.Embedding, &vector); err != nil {
			return nil, errors.New("embedding 仅支持 float 数组形态")
		}
		items = append(items, embeddingItem{
			Index:      entry.Index,
			Dimensions: len(vector),
			Vector:     vector,
		})
	}
	response := &embeddingsResponse{Model: model, Items: items}
	if payload.Usage != nil {
		response.Usage = &playgroundUsage{
			PromptTokens: payload.Usage.PromptTokens,
			TotalTokens:  payload.Usage.TotalTokens,
		}
	}
	return response, nil
}
