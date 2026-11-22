package playground

// POST /rerank —— 同步重排（设计 §3.10，v1.1）：固定派发 POST /v1/rerank（网
// 关公开面已归一 Cohere/Jina 形态），请求体 {model, query, documents, top_n?,
// return_documents?}；响应归一 camelCase {results:[{index, relevanceScore,
// document?}], usage?}——results 沿用网关归一顺序（按分数降序），index 是请
// 求 documents[] 下标，document 为归一回显 {text}（演练场恒收字符串文档，对
// 象文档是 /v1 公开面能力，试玩页不透出；returnDocuments=false 时网关响应无
// document 字段，归一条目自然省略）。无产物落盘，同 §3.9。qwen 上游单次
// query+documents 合计 30000 tokens 上限，超限上游错误原样透出。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// 重排限额（设计 §3.10：query trim 非空 ≤8192 字节；documents 1~20 条、每条
// trim 后非空 ≤8192 字节；8MB 为响应读取上限，防带外）。
const (
	rerankMaxDocuments = 20
	rerankTextMaxBytes = 8 * 1024
	rerankRespMaxBytes = 8 * 1024 * 1024
)

// rerankRequest 是 POST /rerank 请求体（设计 §3.10；returnDocuments 缺省按网
// 关默认 true——回显文档便于前端对照）。
type rerankRequest struct {
	APIKeyID        string   `json:"apiKeyId"`
	ModelID         string   `json:"modelId"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"topN"`
	ReturnDocuments *bool    `json:"returnDocuments"`
}

// rerankResult 是单条重排结果（document 透传为对象 {text}，缺省省略）。
type rerankResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevanceScore"`
	Document       *rerankDocument `json:"document,omitempty"`
}

// rerankDocument 是重排回显文档（上游归一恒 {text} 形态）。
type rerankDocument struct {
	Text string `json:"text"`
}

// rerankResponse 是 POST /rerank 响应（设计 §3.10 归一形状）。
type rerankResponse struct {
	Results []rerankResult   `json:"results"`
	Usage   *playgroundUsage `json:"usage,omitempty"`
}

// rerankHandler 服务 POST /rerank（同步等待；候选校验与 embeddings 链同构）。
func (rt *playgroundRoutes) rerankHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	// query + 20 条 × 8KB 文档的 JSON 转义余量（传输层上限，语义限额在逐条
	// 校验）。
	body, err := readJSONBody(r, 2*1024*1024)
	if err != nil {
		writeHTTPError(w, invalidRequest("请求体读取失败或超过上限"))
		return
	}
	var req rerankRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, invalidRequest(fieldShapeMessage(err)))
		return
	}
	options, candidateErr := rt.resolveKeyModels(r.Context(), scope.ViewerID, strings.TrimSpace(req.APIKeyID))
	if candidateErr != nil {
		writeHTTPError(w, candidateErr)
		return
	}
	if modelOptionOf(options, strings.TrimSpace(req.ModelID), KindRerank) == nil {
		writeHTTPError(w, modelNotAvailable("模型不在所选 Key 的演练场重排候选内"))
		return
	}
	payload, runErr := rt.runRerank(r.Context(), scope, &req)
	if runErr != nil {
		writeHTTPError(w, runErr)
		return
	}
	writeOKPayload(w, payload)
}

// runRerank 执行重排派发与响应归一（测试断言请求构造与归一行为经 Mock
// DispatchPort 回放）。
func (rt *playgroundRoutes) runRerank(ctx context.Context, scope Scope, req *rerankRequest) (*rerankResponse, *HTTPError) {
	model := strings.TrimSpace(req.ModelID)
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, invalidRequest("query 不能为空")
	}
	if len(query) > rerankTextMaxBytes {
		return nil, invalidRequest("query 超过 8192 字节上限")
	}
	if len(req.Documents) == 0 {
		return nil, invalidRequest("documents 不能为空")
	}
	if len(req.Documents) > rerankMaxDocuments {
		return nil, invalidRequest("documents 超过 20 条上限")
	}
	documents := make([]string, 0, len(req.Documents))
	for _, document := range req.Documents {
		text := strings.TrimSpace(document)
		if text == "" {
			return nil, invalidRequest("documents 条目不能为空")
		}
		if len(text) > rerankTextMaxBytes {
			return nil, invalidRequest("documents 条目超过 8192 字节上限")
		}
		documents = append(documents, text)
	}
	payloadValues := map[string]any{"model": model, "query": query, "documents": documents}
	if req.TopN != nil {
		if *req.TopN <= 0 {
			return nil, invalidRequest("topN 必须是正整数")
		}
		payloadValues["top_n"] = *req.TopN
	}
	// returnDocuments 条件携带：显式给出才透传（缺省交由网关默认 true）。
	if req.ReturnDocuments != nil {
		payloadValues["return_documents"] = *req.ReturnDocuments
	}
	payload, _ := json.Marshal(payloadValues)
	response, err := rt.deps.Dispatch.Dispatch(ctx, DispatchRequest{
		APIKeyID: strings.TrimSpace(req.APIKeyID), OwnerID: scope.ViewerID, RemoteAddr: scope.RemoteAddr,
		Path: "/v1/rerank", Method: http.MethodPost,
		Headers: map[string]string{
			"accept":       "application/json",
			"content-type": "application/json",
		},
		Body: payload,
	})
	if err != nil {
		return nil, dispatchError("重排派发失败", err)
	}
	if response == nil {
		return nil, dispatchFailed("重排派发失败")
	}
	// 有界读取（8MB+1，超限按派发失败收敛——输入限额已兜住响应体积，超出即
	// 上游异常）；非 2xx 读错误体走错误面。
	respBody, readErr := io.ReadAll(io.LimitReader(response.Body, rerankRespMaxBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, dispatchFailed("重排响应读取失败")
	}
	if len(respBody) > rerankRespMaxBytes {
		return nil, dispatchFailed("重排响应读取失败")
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, dispatchFailed(upstreamDispatchMessage("重排", response.Status, respBody))
	}
	normalized, parseErr := normalizeRerankPayload(respBody)
	if parseErr != nil {
		return nil, dispatchFailed("重排响应归一失败: " + parseErr.Error())
	}
	return normalized, nil
}

// normalizeRerankPayload 把网关归一形态 {results:[{index,relevance_score,
// document?}],usage:{prompt_tokens,total_tokens}} 归一为演练场响应（设计
// §3.10）；relevance_score 必须是数字（非数字按归一失败收敛，不静默改零值），
// document 为归一回显 {text} 原样透传。
func normalizeRerankPayload(body []byte) (*rerankResponse, error) {
	var payload struct {
		Results []struct {
			Index          int             `json:"index"`
			RelevanceScore json.RawMessage `json:"relevance_score"`
			Document       *rerankDocument `json:"document"`
		} `json:"results"`
		Usage *struct {
			PromptTokens int64 `json:"prompt_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("响应不是合法 JSON")
	}
	if len(payload.Results) == 0 {
		return nil, errors.New("响应缺少 results 数组")
	}
	results := make([]rerankResult, 0, len(payload.Results))
	for _, entry := range payload.Results {
		// JSON null 直解 float64 会静默落 0 零值，与「非数字按归一失败收敛，不
		// 静默改零值」矛盾：null 字面量显式判归一失败（502 同路径）。
		if bytes.Equal(bytes.TrimSpace(entry.RelevanceScore), []byte("null")) {
			return nil, errors.New("relevance_score 必须是数字")
		}
		var score float64
		if err := json.Unmarshal(entry.RelevanceScore, &score); err != nil {
			return nil, errors.New("relevance_score 必须是数字")
		}
		results = append(results, rerankResult{
			Index:          entry.Index,
			RelevanceScore: score,
			Document:       entry.Document,
		})
	}
	response := &rerankResponse{Results: results}
	if payload.Usage != nil {
		response.Usage = &playgroundUsage{
			PromptTokens: payload.Usage.PromptTokens,
			TotalTokens:  payload.Usage.TotalTokens,
		}
	}
	return response, nil
}
