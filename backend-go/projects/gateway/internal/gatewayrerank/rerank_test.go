package gatewayrerank

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return root
}

func TestParseRequest(t *testing.T) {
	request, err := ParseRequest([]byte(`{
		"model":"gte-rerank-v2",
		"query":"什么是向量检索",
		"documents":["文档一",{"text":"文档二","meta":{"id":2}}],
		"top_n":2,
		"return_documents":true,
		"max_chunk_per_doc":128,
		"overlap_tokens":16
	}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if request.Model != "gte-rerank-v2" || request.Query != "什么是向量检索" {
		t.Fatalf("model/query = %q/%q", request.Model, request.Query)
	}
	if len(request.Documents) != 2 || request.Documents[0].Text != "文档一" || !request.Documents[1].Object || request.Documents[1].Text != "文档二" {
		t.Fatalf("documents = %#v", request.Documents)
	}
	if request.TopN == nil || *request.TopN != 2 || !request.ReturnDocuments {
		t.Fatalf("top_n/return_documents = %v/%v", request.TopN, request.ReturnDocuments)
	}
	if request.MaxChunkPerDoc == nil || *request.MaxChunkPerDoc != 128 || request.OverlapTokens == nil || *request.OverlapTokens != 16 {
		t.Fatalf("jina fields = %v/%v", request.MaxChunkPerDoc, request.OverlapTokens)
	}
}

func TestParseRequestRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty", ``, "请求体不能为空"},
		{"not object", `[]`, "必须是 JSON 对象"},
		{"stream", `{"model":"m","query":"q","documents":["d"],"stream":true}`, "不支持流式"},
		{"stream string", `{"model":"m","query":"q","documents":["d"],"stream":"true"}`, "不支持流式"},
		{"stream number", `{"model":"m","query":"q","documents":["d"],"stream":1}`, "不支持流式"},
		{"missing model", `{"query":"q","documents":["d"]}`, "缺少 model"},
		{"missing query", `{"model":"m","documents":["d"]}`, "缺少 query"},
		{"missing documents", `{"model":"m","query":"q"}`, "documents 必须是数组"},
		{"empty documents", `{"model":"m","query":"q","documents":[]}`, "documents 不能为空"},
		{"empty document", `{"model":"m","query":"q","documents":["  "]}`, "不能为空字符串"},
		{"object without text", `{"model":"m","query":"q","documents":[{"id":1}]}`, "对象缺少 text"},
		{"number document", `{"model":"m","query":"q","documents":[1]}`, "必须是字符串或对象"},
		{"top_n zero", `{"model":"m","query":"q","documents":["d"],"top_n":0}`, "top_n 必须是大于 0 的整数"},
		{"top_n fractional", `{"model":"m","query":"q","documents":["d"],"top_n":1.5}`, "top_n 必须是大于 0 的整数"},
		{"return_documents string", `{"model":"m","query":"q","documents":["d"],"return_documents":"true"}`, "return_documents 必须是布尔值"},
		{"max_chunk negative", `{"model":"m","query":"q","documents":["d"],"max_chunk_per_doc":-1}`, "max_chunk_per_doc 必须是非负整数"},
		{"overlap fractional", `{"model":"m","query":"q","documents":["d"],"overlap_tokens":1.2}`, "overlap_tokens 必须是非负整数"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseRequest([]byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseRequest error = %v, want contains %q", err, test.want)
			}
		})
	}
}

func TestBuildDashScopeRequest(t *testing.T) {
	request, err := ParseRequest([]byte(`{
		"model":"public-model",
		"query":"q",
		"documents":["d1",{"text":"d2"}],
		"top_n":1,
		"return_documents":true,
		"max_chunk_per_doc":32,
		"overlap_tokens":8
	}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := BuildDashScopeRequest(request, "gte-rerank-v2")
	if err != nil {
		t.Fatalf("BuildDashScopeRequest: %v", err)
	}
	root := mustObject(t, body)
	if root["model"] != "gte-rerank-v2" {
		t.Fatalf("model = %v", root["model"])
	}
	input := root["input"].(map[string]any)
	if input["query"] != "q" {
		t.Fatalf("query = %v", input["query"])
	}
	documents := input["documents"].([]any)
	if len(documents) != 2 || documents[0] != "d1" || documents[1] != "d2" {
		t.Fatalf("documents = %#v", documents)
	}
	parameters := root["parameters"].(map[string]any)
	if parameters["top_n"] != float64(1) || parameters["return_documents"] != true {
		t.Fatalf("parameters = %#v", parameters)
	}
	if _, has := parameters["max_chunk_per_doc"]; has {
		t.Fatalf("DashScope parameters must not carry Jina-only keys: %#v", parameters)
	}
	if _, has := parameters["overlap_tokens"]; has {
		t.Fatalf("DashScope parameters must not carry Jina-only keys: %#v", parameters)
	}
}

func TestNormalizeResponseDashScope(t *testing.T) {
	upstream := []byte(`{
		"request_id":"req-1",
		"output":{"results":[
			{"index":0,"relevance_score":0.99,"document":{"text":"d1"}},
			{"index":1,"relevance_score":0.42,"document":{"text":"d2"}},
			{"index":2,"relevance_score":0.11,"document":{"text":"d3"}}
		]},
		"usage":{"total_tokens":120}
	}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d1","d2","d3"],"top_n":2}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	root := mustObject(t, body)
	results := root["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results length = %d, want top_n 2", len(results))
	}
	first := results[0].(map[string]any)
	if first["index"] != float64(0) || first["relevance_score"] != 0.99 {
		t.Fatalf("first result = %#v", first)
	}
	if _, has := first["document"]; has {
		t.Fatalf("return_documents=false must strip document: %#v", first)
	}
	usage := root["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(120) || usage["total_tokens"] != float64(120) {
		t.Fatalf("usage = %#v", usage)
	}
	if _, has := root["request_id"]; has {
		t.Fatalf("normalized response must not invent upstream metadata: %#v", root)
	}
}

func TestNormalizeResponseReturnDocumentsKeepsDocument(t *testing.T) {
	upstream := []byte(`{"results":[{"index":0,"relevance_score":0.8,"document":{"text":"d1"}}],"usage":{"prompt_tokens":10,"total_tokens":10}}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d1"],"return_documents":true}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	document := mustObject(t, body)["results"].([]any)[0].(map[string]any)["document"].(map[string]any)
	if document["text"] != "d1" {
		t.Fatalf("document = %#v", document)
	}
}

func TestNormalizeResponseSynthesizesRequestedDocument(t *testing.T) {
	// 上游忽略 return_documents 不回显文档时，网关按请求语义回填：字符串
	// 文档 → {text}，对象文档保留原始字段。
	upstream := []byte(`{"results":[{"index":1,"relevance_score":0.7},{"index":0,"relevance_score":0.2}],"usage":{"prompt_tokens":10,"total_tokens":10}}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d0",{"text":"d1","meta":{"id":2}}],"return_documents":true}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	results := mustObject(t, body)["results"].([]any)
	first := results[0].(map[string]any)
	firstDocument := first["document"].(map[string]any)
	if firstDocument["text"] != "d1" || firstDocument["meta"].(map[string]any)["id"] != float64(2) {
		t.Fatalf("object document = %#v", firstDocument)
	}
	second := results[1].(map[string]any)
	secondDocument := second["document"].(map[string]any)
	if secondDocument["text"] != "d0" {
		t.Fatalf("string document = %#v", secondDocument)
	}
}

func TestNormalizeResponseBackfillsNullAndEmptyDocument(t *testing.T) {
	// 上游在 return_documents=true 下回 null/空串/空对象时，按请求原文回填。
	upstream := []byte(`{"results":[` +
		`{"index":0,"relevance_score":0.9,"document":null},` +
		`{"index":1,"relevance_score":0.8,"document":""},` +
		`{"index":2,"relevance_score":0.7,"document":{}}` +
		`],"usage":{"prompt_tokens":10,"total_tokens":10}}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d0",{"text":"d1","meta":{"id":1}},"d2"],"return_documents":true}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	results := mustObject(t, body)["results"].([]any)
	if document := results[0].(map[string]any)["document"].(map[string]any); document["text"] != "d0" {
		t.Fatalf("null document backfill = %#v", document)
	}
	if document := results[1].(map[string]any)["document"].(map[string]any); document["text"] != "d1" || document["meta"].(map[string]any)["id"] != float64(1) {
		t.Fatalf("empty string document backfill = %#v", document)
	}
	if document := results[2].(map[string]any)["document"].(map[string]any); document["text"] != "d2" {
		t.Fatalf("empty object document backfill = %#v", document)
	}
}

func TestNormalizeResponseBackfillsEmptyTextAndStringDocuments(t *testing.T) {
	upstream := []byte(`{"results":[` +
		`{"index":0,"relevance_score":0.9,"document":{"text":""}},` +
		`{"index":1,"relevance_score":0.8,"document":"d1"}` +
		`],"usage":{"prompt_tokens":10,"total_tokens":10}}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d0","d1"],"return_documents":true}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	results := mustObject(t, body)["results"].([]any)
	if document := results[0].(map[string]any)["document"].(map[string]any); document["text"] != "d0" {
		t.Fatalf("empty text document backfill = %#v", document)
	}
	if document := results[1].(map[string]any)["document"].(map[string]any); document["text"] != "d1" {
		t.Fatalf("string document normalization = %#v", document)
	}
}

func TestRewriteModelPreservesLargeDocumentInteger(t *testing.T) {
	raw := []byte(`{"model":"m","query":"q","documents":[{"text":"d","id":12345678901234567890}]}`)
	body, err := RewriteModel(raw, "upstream")
	if err != nil {
		t.Fatalf("RewriteModel: %v", err)
	}
	if !strings.Contains(string(body), `"id":12345678901234567890`) {
		t.Fatalf("large integer lost: %s", body)
	}
}

func TestNormalizeResponsePreservesLargeDocumentInteger(t *testing.T) {
	upstream := []byte(`{"results":[{"index":0,"relevance_score":0.9,"document":{"text":"d","id":12345678901234567890}}]}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":[{"text":"d","id":12345678901234567890}],"return_documents":true}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	if !strings.Contains(string(body), `"id":12345678901234567890`) {
		t.Fatalf("large integer lost: %s", body)
	}
}

func TestNormalizeResponseTopNSelectsHighestScores(t *testing.T) {
	// 上游忽略 top_n 且未按分数排序时，先按 relevance_score 降序再裁剪。
	upstream := []byte(`{"results":[{"index":0,"relevance_score":0.2},{"index":1,"relevance_score":0.9},{"index":2,"relevance_score":0.5}]}`)
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d0","d1","d2"],"top_n":2}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	body, err := NormalizeResponse(upstream, request)
	if err != nil {
		t.Fatalf("NormalizeResponse: %v", err)
	}
	results := mustObject(t, body)["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results length = %d", len(results))
	}
	if results[0].(map[string]any)["index"] != float64(1) || results[1].(map[string]any)["index"] != float64(2) {
		t.Fatalf("top_n must keep highest scores: %#v", results)
	}
}

func TestNormalizeResponseGenericUsageShapes(t *testing.T) {
	tests := []struct {
		name      string
		upstream  string
		prompt    float64
		total     float64
		wantUsage bool
	}{
		{"jina prompt", `{"results":[{"index":0,"relevance_score":1}],"usage":{"prompt_tokens":7,"total_tokens":7}}`, 7, 7, true},
		{"total only", `{"results":[{"index":0,"relevance_score":1}],"usage":{"total_tokens":9}}`, 9, 9, true},
		{"prompt zero total", `{"results":[{"index":0,"relevance_score":1}],"usage":{"prompt_tokens":0,"total_tokens":12}}`, 12, 12, true},
		{"input zero total", `{"results":[{"index":0,"relevance_score":1}],"usage":{"input_tokens":0,"total_tokens":13}}`, 13, 13, true},
		{"prompt zero input wins", `{"results":[{"index":0,"relevance_score":1}],"usage":{"prompt_tokens":0,"input_tokens":5,"total_tokens":5}}`, 5, 5, true},
		{"total zero uses prompt", `{"results":[{"index":0,"relevance_score":1}],"usage":{"prompt_tokens":6,"total_tokens":0}}`, 6, 6, true},
		{"all zero keeps zero", `{"results":[{"index":0,"relevance_score":1}],"usage":{"prompt_tokens":0,"total_tokens":0}}`, 0, 0, true},
		{"input tokens", `{"results":[{"index":0,"relevance_score":1}],"usage":{"input_tokens":5}}`, 5, 5, true},
		{"cohere meta", `{"results":[{"index":0,"relevance_score":1}],"meta":{"billed_units":{"input_tokens":4}}}`, 4, 4, true},
		{"no usage", `{"results":[{"index":0,"relevance_score":1}]}`, 0, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d"]}`))
			if err != nil {
				t.Fatalf("ParseRequest: %v", err)
			}
			body, err := NormalizeResponse([]byte(test.upstream), request)
			if err != nil {
				t.Fatalf("NormalizeResponse: %v", err)
			}
			usage, hasUsage := mustObject(t, body)["usage"].(map[string]any)
			if hasUsage != test.wantUsage {
				t.Fatalf("usage present = %v, want %v (body=%s)", hasUsage, test.wantUsage, body)
			}
			if !test.wantUsage {
				return
			}
			if usage["prompt_tokens"] != test.prompt || usage["total_tokens"] != test.total {
				t.Fatalf("usage = %#v, want prompt=%v total=%v", usage, test.prompt, test.total)
			}
		})
	}
}

func TestNormalizeResponseRejectsInvalid(t *testing.T) {
	request, err := ParseRequest([]byte(`{"model":"m","query":"q","documents":["d"]}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	for _, body := range []string{
		`not-json`,
		`{"output":{}}`,
		`{"results":{}}`,
		`{"results":[1]}`,
		`{"results":[{"index":"0","relevance_score":1}]}`,
		`{"results":[{"index":0,"relevance_score":"1"}]}`,
	} {
		if _, err := NormalizeResponse([]byte(body), request); err == nil {
			t.Fatalf("NormalizeResponse(%s) must fail", body)
		}
	}
}

func TestRewriteModel(t *testing.T) {
	body, err := RewriteModel([]byte(`{"model":"public","query":"q","documents":["d"],"top_n":1}`), "upstream")
	if err != nil {
		t.Fatalf("RewriteModel: %v", err)
	}
	root := mustObject(t, body)
	if root["model"] != "upstream" || root["query"] != "q" {
		t.Fatalf("rewritten body = %#v", root)
	}
}

func TestIsRequestPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/rerank":         true,
		"/rerank":            true,
		"/v1/rerank?trace=1": true,
		"/v1/reranks":        false,
		"/v1/embeddings":     false,
		"v1/rerank":          true,
	} {
		if got := IsRequestPath(path); got != want {
			t.Fatalf("IsRequestPath(%q) = %v, want %v", path, got, want)
		}
	}
}
