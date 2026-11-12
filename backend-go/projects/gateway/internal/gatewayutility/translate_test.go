package gatewayutility

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

func TestRerankUpstreamBodyTranslatesExternalShape(t *testing.T) {
	encoded, err := RerankUpstreamBody([]byte(`{"model":"qwen3.7-text-rerank","query":"什么是向量","documents":["a","b"],"top_n":2,"return_documents":true}`))
	if err != nil {
		t.Fatalf("RerankUpstreamBody: %v", err)
	}
	value := decode(t, encoded)
	if value["model"] != "qwen3.7-text-rerank" {
		t.Fatalf("model = %v", value["model"])
	}
	input, _ := value["input"].(map[string]any)
	if input["query"] != "什么是向量" {
		t.Fatalf("input.query = %v", input["query"])
	}
	documents, _ := input["documents"].([]any)
	if len(documents) != 2 || documents[0] != "a" {
		t.Fatalf("input.documents = %v", input["documents"])
	}
	parameters, _ := value["parameters"].(map[string]any)
	if parameters["top_n"] != float64(2) || parameters["return_documents"] != true {
		t.Fatalf("parameters = %v", parameters)
	}
}

func TestRerankUpstreamBodyRejectsMissingFields(t *testing.T) {
	if _, err := RerankUpstreamBody([]byte(`{"model":"m","documents":["a"]}`)); err == nil {
		t.Fatal("缺少 query 必须拒绝")
	}
	if _, err := RerankUpstreamBody([]byte(`{"model":"m","query":"q"}`)); err == nil {
		t.Fatal("缺少 documents 必须拒绝")
	}
	if _, err := RerankUpstreamBody([]byte(`[]`)); err == nil {
		t.Fatal("非对象必须拒绝")
	}
}

func TestRerankDownstreamBodyNormalizesDashScope(t *testing.T) {
	encoded, err := RerankDownstreamBody([]byte(`{"output":{"results":[{"index":0,"relevance_score":0.9,"document":{"text":"a"}},{"index":1,"relevance_score":0.1}]},"usage":{"total_tokens":7}}`), "qwen3.7-text-rerank")
	if err != nil {
		t.Fatalf("RerankDownstreamBody: %v", err)
	}
	value := decode(t, encoded)
	if value["model"] != "qwen3.7-text-rerank" {
		t.Fatalf("model = %v", value["model"])
	}
	results, _ := value["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v", value["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["index"] != float64(0) || first["relevance_score"] != 0.9 {
		t.Fatalf("first = %v", first)
	}
	if _, ok := first["document"]; !ok {
		t.Fatalf("document 应保留：%v", first)
	}
	second, _ := results[1].(map[string]any)
	if _, ok := second["document"]; ok {
		t.Fatalf("无 document 时不得补空：%v", second)
	}
	usage, _ := value["usage"].(map[string]any)
	if usage["total_tokens"] != float64(7) {
		t.Fatalf("usage = %v", value["usage"])
	}
	if usage["input_tokens"] != float64(7) {
		t.Fatalf("total_tokens 应补为 input_tokens：%v", usage)
	}
}

func TestRerankDownstreamBodyRejectsInvalidPayload(t *testing.T) {
	if _, err := RerankDownstreamBody([]byte(`not-json`), ""); err == nil {
		t.Fatal("非 JSON 必须拒绝")
	}
	if _, err := RerankDownstreamBody([]byte(`{"output":{"results":[]}}`), ""); err == nil {
		t.Fatal("空 results 必须拒绝")
	}
	if _, err := RerankDownstreamBody([]byte(`{"output":{"results":[1,"x"]}}`), ""); err == nil {
		t.Fatal("无有效结果对象必须拒绝")
	}
}

func TestRewriteModelKeepsOtherFields(t *testing.T) {
	encoded, err := RewriteModel([]byte(`{"model":"alias","input":"hello"}`), "real-model")
	if err != nil {
		t.Fatalf("RewriteModel: %v", err)
	}
	value := decode(t, encoded)
	if value["model"] != "real-model" || value["input"] != "hello" {
		t.Fatalf("value = %v", value)
	}
	if _, err := RewriteModel([]byte(`[]`), "m"); err == nil {
		t.Fatal("非对象必须拒绝")
	}
}

// TestRerankDownstreamBodyRealQwenUsageShape 锁定 2026-11-12 真实上游
// qwen3.7-text-rerank 的响应形态：usage 同时给 prompt_tokens/total_tokens，
// document 为对象；下游保持原 usage（不再补 input_tokens），ExtractUsage 可
// 直接按 prompt_tokens 计费。
func TestRerankDownstreamBodyRealQwenUsageShape(t *testing.T) {
	body := []byte(`{"output":{"results":[{"index":0,"relevance_score":0.9324,"document":{"text":"向量检索"}},{"index":2,"relevance_score":0.3262}]},"usage":{"prompt_tokens":72,"total_tokens":72}}`)
	translated, err := RerankDownstreamBody(body, "qwen3.7-text-rerank")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(translated, &value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	results, _ := value["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %#v", value["results"])
	}
	usage, _ := value["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(72) || usage["total_tokens"] != float64(72) {
		t.Fatalf("usage = %#v", usage)
	}
	if _, hasInput := usage["input_tokens"]; hasInput {
		t.Fatalf("prompt_tokens 已存在时不应补 input_tokens: %#v", usage)
	}
}
