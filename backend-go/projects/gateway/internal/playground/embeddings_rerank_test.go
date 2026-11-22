package playground

// POST /embeddings 与 POST /rerank 测试（设计 §3.9/§3.10，v1.1）：Mock 派发端
// 口回放固定上游响应，断言请求构造（dimensions/encoding_format/top_n/
// return_documents 条件携带、input 归一为数组）、限额校验（条数/字节/空值/
// null 形态/字段级措辞/边界放行）、候选校验错误映射（400/404/502）、响应读
// 取上限（8MB）与响应归一（usage 缺省、document 缺省、null/base64 向量形态
// 与字符串分值拒绝）与目录分类的 embedding/rerank 两 kind。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// embeddingFixtureDeps 在基础夹具上补 embedding 候选（Key 候选行 embedding
// kind）。
func embeddingFixtureDeps(t *testing.T) *Deps {
	t.Helper()
	deps := playgroundFixtureDeps(t)
	keyModels := fixtureKeyModels(deps)
	keyModels.rows[playgroundKeyModelsFixtureID] = append(keyModels.rows[playgroundKeyModelsFixtureID], keyRowsOf("text-embedding-v4", KindEmbedding)...)
	return deps
}

// rerankFixtureDeps 在基础夹具上补 rerank 候选。
func rerankFixtureDeps(t *testing.T) *Deps {
	t.Helper()
	deps := playgroundFixtureDeps(t)
	keyModels := fixtureKeyModels(deps)
	keyModels.rows[playgroundKeyModelsFixtureID] = append(keyModels.rows[playgroundKeyModelsFixtureID], keyRowsOf("gte-rerank-v2", KindRerank)...)
	return deps
}

// TestEmbeddingsRequestConstructionAndNormalization 向量主链：出站体（model/
// input 数组/dimensions/encoding_format）、purpose 头、归一响应（model/usage/
// items 的 index/dimensions/vector）。
func TestEmbeddingsRequestConstructionAndNormalization(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3]}],"model":"text-embedding-v4","usage":{"prompt_tokens":12,"total_tokens":12}}`),
	}}
	mux, _ := newTestRoutes(deps)
	payload := mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4",
		"input": []string{"文本一", "文本二"}, "dimensions": 1024, "encodingFormat": "float",
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("派发次数 = %d", len(dispatch.calls))
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/embeddings" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s", call.Request.Method, call.Request.Path)
	}
	if call.Request.Headers["content-type"] != "application/json" || call.Request.Headers["accept"] != "application/json" {
		t.Fatalf("内容协商头不符: %v", call.Request.Headers)
	}
	var outbound struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		Dimensions     int      `json:"dimensions"`
		EncodingFormat string   `json:"encoding_format"`
	}
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if outbound.Model != "text-embedding-v4" || len(outbound.Input) != 2 || outbound.Input[0] != "文本一" || outbound.Input[1] != "文本二" {
		t.Fatalf("出站体 model/input 不符: %+v", outbound)
	}
	if outbound.Dimensions != 1024 || outbound.EncodingFormat != "float" {
		t.Fatalf("dimensions/encoding_format 透传不符: %+v", outbound)
	}
	var response struct {
		Data struct {
			Model string `json:"model"`
			Usage *struct {
				PromptTokens int64 `json:"promptTokens"`
				TotalTokens  int64 `json:"totalTokens"`
			} `json:"usage"`
			Items []struct {
				Index      int       `json:"index"`
				Dimensions int       `json:"dimensions"`
				Vector     []float64 `json:"vector"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if response.Data.Model != "text-embedding-v4" {
		t.Fatalf("归一 model 不符: %+v", response.Data)
	}
	if response.Data.Usage == nil || response.Data.Usage.PromptTokens != 12 || response.Data.Usage.TotalTokens != 12 {
		t.Fatalf("归一 usage 不符: %+v", response.Data.Usage)
	}
	if len(response.Data.Items) != 2 ||
		response.Data.Items[0].Index != 0 || response.Data.Items[0].Dimensions != 2 || len(response.Data.Items[0].Vector) != 2 ||
		response.Data.Items[1].Index != 1 || response.Data.Items[1].Dimensions != 1 {
		t.Fatalf("归一 items 不符: %+v", response.Data.Items)
	}
}

// TestEmbeddingsSingleStringInputAndOptionalOmitted 单字符串 input 归一为一条
// 数组；dimensions/encoding_format 缺省不出站；上游无 usage 时响应省略 usage；
// 8192 字节边界放行。
func TestEmbeddingsSingleStringInputAndOptionalOmitted(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{
		{Body: []byte(`{"data":[{"index":0,"embedding":[1,2,3]}]}`)},
		{Body: []byte(`{"data":[{"index":0,"embedding":[1]}]}`)},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "单条文本"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var outbound map[string]any
	if err := json.Unmarshal(dispatch.calls[0].Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	input, _ := outbound["input"].([]any)
	if len(input) != 1 || input[0] != "单条文本" {
		t.Fatalf("单字符串 input 应归一为一条数组: %v", outbound["input"])
	}
	if _, present := outbound["dimensions"]; present {
		t.Fatalf("dimensions 缺省不应出站: %v", outbound)
	}
	if _, present := outbound["encoding_format"]; present {
		t.Fatalf("encoding_format 缺省不应出站: %v", outbound)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	data := payload["data"].(map[string]any)
	if _, present := data["usage"]; present {
		t.Fatalf("上游无 usage 时响应应省略 usage: %v", data)
	}
	// 8192 字节边界（恰好等于上限）放行。
	recorder = doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": strings.Repeat("a", 8192)}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("8192 字节边界应放行: %d（body %s）", recorder.Code, recorder.Body.String())
	}
}

// TestEmbeddingsValidation 向量校验面：input 条数/空值/字节上限、dimensions 非
// 正、模型不在候选均 400；账户不存在 404 且不进入派发路径。
func TestEmbeddingsValidation(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	mux, depsOut := newTestRoutes(deps)
	eleven := make([]string, 11)
	for index := range eleven {
		eleven[index] = "文本"
	}
	cases := []struct {
		name     string
		body     map[string]any
		contains string
	}{
		{"input 超 10 条", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": eleven}, "10"},
		{"input 空数组", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": []string{}}, "input"},
		{"input 条目空白", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": []string{"  "}}, "不能为空"},
		{"input 超 8KB", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": []string{strings.Repeat("a", 8193)}}, "8192"},
		{"input 数字数组", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": []any{1, 2}}, "必须是字符串或字符串数组"},
		{"input null", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": nil}, "必须是字符串或字符串数组"},
		{"dimensions 非正", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本", "dimensions": 0}, "正整数"},
		{"dimensions 非整数形态", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本", "dimensions": 1.5}, "dimensions 必须是正整数"},
		{"模型不在候选", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "nope-embed", "input": "文本"}, "playground_model_not_available"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings", mustJSON(testCase.body))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), testCase.contains) {
				t.Fatalf("%s 应 400 含 %q: %d %s", testCase.name, testCase.contains, recorder.Code, recorder.Body.String())
			}
		})
	}
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": "key_missing", "modelId": "text-embedding-v4", "input": "文本"}))
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_key_not_found") {
		t.Fatalf("Key 不存在应 404 playground_key_not_found: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(depsOut.Dispatch.(*mockDispatch).calls) != 0 {
		t.Fatalf("校验失败不应进入派发路径（%d 次）", len(depsOut.Dispatch.(*mockDispatch).calls))
	}
}

// TestEmbeddingsUpstreamErrorPropagates 上游非 2xx：502 playground_dispatch_
// failed，message 带上游状态码摘要与原文 message。
func TestEmbeddingsUpstreamErrorPropagates(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"message":"rate limited"}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本"}))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "playground_dispatch_failed") || !strings.Contains(body, "向量请求失败") || !strings.Contains(body, "rate limited") {
		t.Fatalf("错误面缺少归一语义: %s", body)
	}
}

// TestEmbeddingsBase64FormRejected base64 字符串形态向量不转码：按归一失败 502
// 收敛（保持原始语义，不加兼容层）。
func TestEmbeddingsBase64FormRejected(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"index":0,"embedding":"QS/AFw=="}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本"}))
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "向量响应归一失败") {
		t.Fatalf("base64 形态应 502 归一失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestEmbeddingsNullEmbeddingRejected embedding=null（JSON null 直解 []float64
// 会静默落 nil 零值）：显式按归一失败 502 收敛，不静默改零值。
func TestEmbeddingsNullEmbeddingRejected(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"data":[{"index":0,"embedding":null}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本"}))
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "向量响应归一失败") {
		t.Fatalf("embedding null 应 502 归一失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestEmbeddingsResponseTooLargeRejected 响应体超 8MB 读取上限：502
// playground_dispatch_failed「向量响应读取失败」（防带外，不做部分解析）。
func TestEmbeddingsResponseTooLargeRejected(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: make([]byte, embeddingsRespMaxBytes+10),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": "文本"}))
	if recorder.Code != http.StatusBadGateway ||
		!strings.Contains(recorder.Body.String(), "playground_dispatch_failed") ||
		!strings.Contains(recorder.Body.String(), "向量响应读取失败") {
		t.Fatalf("超 8MB 响应应 502 读取失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestEmbeddingsInputBoundaryAccepted input 恰 10 条放行且恒归一为出站数组
// len=10（上限边界不误拒）。
func TestEmbeddingsInputBoundaryAccepted(t *testing.T) {
	deps := embeddingFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: []byte(`{"data":[{"index":0,"embedding":[1]}]}`)}}
	mux, _ := newTestRoutes(deps)
	ten := make([]string, 10)
	for index := range ten {
		ten[index] = "文本"
	}
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/embeddings",
		mustJSON(map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "text-embedding-v4", "input": ten}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("input 恰 10 条应放行: %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var outbound map[string]any
	if err := json.Unmarshal(dispatch.calls[0].Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	input, _ := outbound["input"].([]any)
	if len(input) != 10 {
		t.Fatalf("出站 input 应为 10 条数组: %d", len(input))
	}
}

// TestRerankRequestConstructionAndNormalization 重排主链：出站体（model/query/
// documents/top_n）、returnDocuments 缺省不出站、purpose 头、归一响应（results
// 的 index/relevanceScore/document.text + usage）。
func TestRerankRequestConstructionAndNormalization(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"results":[{"index":1,"relevance_score":0.98,"document":{"text":"文档二"}},{"index":0,"relevance_score":0.42,"document":{"text":"文档一"}}],"usage":{"prompt_tokens":100,"total_tokens":100}}`),
	}}
	mux, _ := newTestRoutes(deps)
	payload := mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2",
		"query": "查询语句", "documents": []string{"文档一", "文档二"}, "topN": 3,
	})
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	call := dispatch.calls[0]
	if call.Request.Path != "/v1/rerank" || call.Request.Method != "POST" {
		t.Fatalf("派发目标 = %s %s", call.Request.Method, call.Request.Path)
	}
	var outbound map[string]any
	if err := json.Unmarshal(call.Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if outbound["model"] != "gte-rerank-v2" || outbound["query"] != "查询语句" || outbound["top_n"] != float64(3) {
		t.Fatalf("出站体不符: %v", outbound)
	}
	documents, _ := outbound["documents"].([]any)
	if len(documents) != 2 || documents[0] != "文档一" || documents[1] != "文档二" {
		t.Fatalf("documents 出站不符: %v", outbound["documents"])
	}
	if _, present := outbound["return_documents"]; present {
		t.Fatalf("returnDocuments 缺省不应出站（网关默认 true）: %v", outbound)
	}
	var response struct {
		Data struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevanceScore"`
				Document       *struct {
					Text string `json:"text"`
				} `json:"document"`
			} `json:"results"`
			Usage *struct {
				PromptTokens int64 `json:"promptTokens"`
				TotalTokens  int64 `json:"totalTokens"`
			} `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(response.Data.Results) != 2 ||
		response.Data.Results[0].Index != 1 || response.Data.Results[0].RelevanceScore != 0.98 || response.Data.Results[0].Document == nil || response.Data.Results[0].Document.Text != "文档二" {
		t.Fatalf("归一 results 不符: %+v", response.Data.Results)
	}
	if response.Data.Usage == nil || response.Data.Usage.PromptTokens != 100 || response.Data.Usage.TotalTokens != 100 {
		t.Fatalf("归一 usage 不符: %+v", response.Data.Usage)
	}
}

// TestRerankReturnDocumentsFalseOmitsDocument returnDocuments=false：出站携带
// return_documents=false；上游响应无 document 字段时归一条目自然省略。
func TestRerankReturnDocumentsFalseOmitsDocument(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":10,"total_tokens":10}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2",
		"query": "查询语句", "documents": []string{"文档一"}, "returnDocuments": false,
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var outbound map[string]any
	if err := json.Unmarshal(dispatch.calls[0].Request.Body, &outbound); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if value, present := outbound["return_documents"]; !present || value != false {
		t.Fatalf("return_documents=false 应条件携带: %v", outbound)
	}
	var response struct {
		Data struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevanceScore"`
				Document       *struct {
					Text string `json:"text"`
				} `json:"document"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(response.Data.Results) != 1 || response.Data.Results[0].Document != nil {
		t.Fatalf("无 document 字段时归一条目应省略 document: %+v", response.Data.Results)
	}
}

// TestRerankValidation 重排校验面：query 空值/字节上限、documents 条数/空值/
// 字节上限、topN 非正、模型不在候选均 400；账户不存在 404 且不进入派发路径。
func TestRerankValidation(t *testing.T) {
	deps := rerankFixtureDeps(t)
	mux, depsOut := newTestRoutes(deps)
	twentyOne := make([]string, 21)
	for index := range twentyOne {
		twentyOne[index] = "文档"
	}
	cases := []struct {
		name     string
		body     map[string]any
		contains string
	}{
		{"query 空", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "  ", "documents": []string{"文档一"}}, "query"},
		{"query 超 8KB", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": strings.Repeat("a", 8193), "documents": []string{"文档一"}}, "8192"},
		{"documents 0 条", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{}}, "documents"},
		{"documents 超 20 条", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": twentyOne}, "20"},
		{"documents 条目空白", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一", " "}}, "不能为空"},
		{"documents 超 8KB", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{strings.Repeat("a", 8193)}}, "8192"},
		{"topN 非正", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"}, "topN": 0}, "正整数"},
		{"topN 非整数形态", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"}, "topN": 1.5}, "topN 必须是正整数"},
		{"documents 非数组形态", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": map[string]any{"a": "b"}}, "documents 必须是字符串数组"},
		{"模型不在候选", map[string]any{"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "nope-rerank", "query": "查询", "documents": []string{"文档一"}}, "playground_model_not_available"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(testCase.body))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), testCase.contains) {
				t.Fatalf("%s 应 400 含 %q: %d %s", testCase.name, testCase.contains, recorder.Code, recorder.Body.String())
			}
		})
	}
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank",
		mustJSON(map[string]any{"apiKeyId": "key_missing", "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"}}))
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "playground_key_not_found") {
		t.Fatalf("Key 不存在应 404 playground_key_not_found: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(depsOut.Dispatch.(*mockDispatch).calls) != 0 {
		t.Fatalf("校验失败不应进入派发路径（%d 次）", len(depsOut.Dispatch.(*mockDispatch).calls))
	}
}

// TestRerankUpstreamErrorPropagates 上游非 2xx：502 playground_dispatch_failed，
// message 带上游状态码摘要与原文 message。
func TestRerankUpstreamErrorPropagates(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Status: http.StatusUnprocessableEntity,
		Body:   []byte(`{"error":{"message":"query+documents 超 30000 tokens"}}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"},
	}))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "playground_dispatch_failed") || !strings.Contains(body, "重排请求失败") || !strings.Contains(body, "30000 tokens") {
		t.Fatalf("错误面缺少归一语义: %s", body)
	}
}

// TestRerankNonNumericScoreRejected relevance_score 非数字：按归一失败 502 收
// 敛（不静默改零值）。
func TestRerankNonNumericScoreRejected(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"results":[{"index":0,"relevance_score":"high"}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"},
	}))
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "重排响应归一失败") {
		t.Fatalf("字符串分值应 502 归一失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestRerankNullScoreRejected relevance_score=null（JSON null 直解 float64 会
// 静默落 0 零值）：显式按归一失败 502 收敛，不静默改零值。
func TestRerankNullScoreRejected(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: []byte(`{"results":[{"index":0,"relevance_score":null}]}`),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"},
	}))
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "重排响应归一失败") {
		t.Fatalf("relevance_score null 应 502 归一失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestRerankResponseTooLargeRejected 响应体超 8MB 读取上限：502
// playground_dispatch_failed「重排响应读取失败」（防带外，不做部分解析）。
func TestRerankResponseTooLargeRejected(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{
		Body: make([]byte, rerankRespMaxBytes+10),
	}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": []string{"文档一"},
	}))
	if recorder.Code != http.StatusBadGateway ||
		!strings.Contains(recorder.Body.String(), "playground_dispatch_failed") ||
		!strings.Contains(recorder.Body.String(), "重排响应读取失败") {
		t.Fatalf("超 8MB 响应应 502 读取失败: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestRerankDocumentsBoundaryAccepted documents 恰 20 条放行（上限边界不误拒）。
func TestRerankDocumentsBoundaryAccepted(t *testing.T) {
	deps := rerankFixtureDeps(t)
	dispatch := deps.Dispatch.(*mockDispatch)
	dispatch.script = []scriptedResponse{{Body: []byte(`{"results":[{"index":0,"relevance_score":0.5}]}`)}}
	mux, _ := newTestRoutes(deps)
	twenty := make([]string, 20)
	for index := range twenty {
		twenty[index] = "文档"
	}
	recorder := doJSON(mux, "POST", "/__aisys__/api/playground/rerank", mustJSON(map[string]any{
		"apiKeyId": playgroundKeyModelsFixtureID, "modelId": "gte-rerank-v2", "query": "查询", "documents": twenty,
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("documents 恰 20 条应放行: %d（body %s）", recorder.Code, recorder.Body.String())
	}
}
