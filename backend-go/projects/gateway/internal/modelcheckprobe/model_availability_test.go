package modelcheckprobe

import "testing"

func TestIsModelUnavailableRecognizesStructured200Error(t *testing.T) {
	result := Result{HTTPStatus: 200, ErrorMessage: "The model 'gpt-5.6-sol' does not exist", JSON: map[string]any{
		"error": map[string]any{"type": "invalid_request_error", "param": "model", "code": "model_not_found", "message": "The model 'gpt-5.6-sol' does not exist"},
	}}
	if !IsModelUnavailable(result, "gpt-5.6-sol") {
		t.Fatal("expected structured model_not_found to be recognized")
	}
}

func TestIsModelUnavailableDoesNotClassifyGenericEndpoint404(t *testing.T) {
	result := Result{HTTPStatus: 404, ErrorMessage: "404 page not found"}
	if IsModelUnavailable(result, "gpt-5.6-sol") {
		t.Fatal("generic endpoint 404 must not be treated as model unavailability")
	}
}

func TestEvaluateBasicSkipsUnavailableModel(t *testing.T) {
	item := EvaluateBasic(Result{HTTPStatus: 400, ErrorMessage: "model not found", JSON: map[string]any{
		"error": map[string]any{"type": "invalid_request_error", "message": "model not found"},
	}}, "gpt-5.6-sol")
	if item.Status != "skipped" || item.Evidence["modelUnavailable"] != true {
		t.Fatalf("unexpected evaluation: %#v", item)
	}
}

func TestModelUnavailableHTTP200RemainsQualityEvidence(t *testing.T) {
	result := Result{HTTPStatus: 200, ExpectedModel: "gpt-5.6-sol", ErrorMessage: "The model 'gpt-5.6-sol' does not exist", JSON: map[string]any{
		"error": map[string]any{"code": "model_not_found", "message": "The model 'gpt-5.6-sol' does not exist"},
	}}
	if isTerminalProbeFailure(result) {
		t.Fatal("HTTP 200 model-scoped error is quality evidence, not a terminal transport failure")
	}
}
