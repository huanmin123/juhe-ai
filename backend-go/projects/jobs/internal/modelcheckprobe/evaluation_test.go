package modelcheckprobe

import "testing"

func TestEvaluationMatchesNodeBasicProtocolScoring(t *testing.T) {
	passed := EvaluateBasicResponses(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: " OK-MODEL-CHECK "}}, "gpt-5.6-sol", "target", false)
	if passed.Status != "passed" || passed.Score != 10 || passed.MaxScore != 10 || passed.Evidence["outputMatches"] != true {
		t.Fatalf("passed=%#v", passed)
	}
	mismatch := EvaluateBasicResponses(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "other-model", OutputText: expectedBasicOutput}}, "gpt-5.6-sol", "target", false)
	if mismatch.Status != "failed" || mismatch.Score != 1 || mismatch.Evidence["modelMismatch"] != true || mismatch.Evidence["message"] != "上游返回模型 other-model，与请求模型 gpt-5.6-sol 不一致" {
		t.Fatalf("mismatch=%#v", mismatch)
	}
	mappingApplied := true
	mapped := EvaluateBasicResponses(ProbeResult{HTTPStatusCode: 200, Success: true, RequestModel: "mapped-model", ModelMappingApplied: &mappingApplied, Response: ParsedResponse{Model: "mapped-model", OutputText: expectedBasicOutput}}, "gpt-5.6-sol", "target", false)
	if mapped.Status != "passed" || mapped.Score != 10 {
		t.Fatalf("mapped=%#v", mapped)
	}
	wrongOutput := EvaluateBasicResponses(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "gateway error"}}, "gpt-5.6-sol", "target", false)
	if wrongOutput.Status != "warning" || wrongOutput.Score != 3 || wrongOutput.MaxScore != 10 || wrongOutput.Evidence["outputMatches"] != false {
		t.Fatalf("wrong output=%#v", wrongOutput)
	}
}

func TestEvaluationMatchesNodeStreamStructuredToolAndUsageScoring(t *testing.T) {
	stream := EvaluateStream(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "wrong"}}, "gpt-5.6-sol", "target")
	if stream.Status != "warning" || stream.Score != 11 || stream.MaxScore != 15 {
		t.Fatalf("stream=%#v", stream)
	}
	structured := EvaluateStructuredOutput(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: `prefix {"status":"ok","value":7,"credential":"raw-api-key"}`}}, "gpt-5.6-sol", "target")
	if structured.Status != "passed" || structured.Score != 15 {
		t.Fatalf("structured=%#v", structured)
	}
	if output := structured.Evidence["outputJson"].(map[string]any); len(output) != 2 || output["status"] != "ok" || output["value"] != float64(7) {
		t.Fatalf("structured evidence=%#v", structured.Evidence)
	}
	structuredWrongValue := EvaluateStructuredOutput(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: `{"status":"ok","value":8}`}}, "gpt-5.6-sol", "target")
	if structuredWrongValue.Status != "warning" || structuredWrongValue.Score != 11 {
		t.Fatalf("structured wrong=%#v", structuredWrongValue)
	}
	tool := EvaluateToolCalling(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}}}, "gpt-5.6-sol", "target")
	if tool.Status != "passed" || tool.Score != 15 || tool.Evidence["called"] != true {
		t.Fatalf("tool=%#v", tool)
	}
	toolWrongArguments := EvaluateToolCalling(ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"wrong","count":1}`}}}}}, "gpt-5.6-sol", "target")
	if toolWrongArguments.Status != "warning" || toolWrongArguments.Score != 11 {
		t.Fatalf("tool wrong=%#v", toolWrongArguments)
	}
	usage := EvaluateUsageShape([]ProbeResult{{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Usage: map[string]any{"total_tokens": float64(3), "credential": "raw-api-key"}}}}, "target")
	if usage.Status != "passed" || usage.Score != 10 || usage.MaxScore != 10 {
		t.Fatalf("usage=%#v", usage)
	}
	if fields := usage.Evidence["usage"].(map[string]any); len(fields) != 1 || fields["total_tokens"] != float64(3) {
		t.Fatalf("usage evidence=%#v", usage.Evidence)
	}
	insufficient := EvaluateUsageShape([]ProbeResult{{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Usage: map[string]any{}}}}, "target")
	if insufficient.Status != "skipped" || insufficient.Score != 0 || insufficient.MaxScore != 0 || insufficient.Evidence["evidenceInsufficient"] != true {
		t.Fatalf("insufficient=%#v", insufficient)
	}
}

func TestEvaluationMatchesNodeRequestFailureSemantics(t *testing.T) {
	item := EvaluateBasicResponses(ProbeResult{HTTPStatusCode: 503, Success: false, Response: ParsedResponse{ErrorMessage: "upstream unavailable"}}, "gpt-5.6-sol", "target", false)
	if item.Status != "skipped" || item.Score != 0 || item.MaxScore != 0 || item.ErrorCode != "http_503" || item.ErrorMessage != "upstream unavailable" || item.Evidence["requestFailure"] != true || item.Evidence["excludedFromScoring"] != true {
		t.Fatalf("item=%#v", item)
	}
	usage := EvaluateUsageShape([]ProbeResult{{HTTPStatusCode: 0, Success: false, Response: ParsedResponse{ErrorMessage: "模型检测探针超时"}}}, "target")
	if usage.Status != "skipped" || usage.Score != 0 || usage.MaxScore != 0 || usage.Evidence["requestFailure"] != true {
		t.Fatalf("usage=%#v", usage)
	}
}
