package modelcheckprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestRunTrustedComparisonUsesBothResolvedModels(t *testing.T) {
	newServer := func(model string) *httptest.Server {
		expectedModel := model
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatalf("model=%q err=%v", request.Model, err)
			}
			output := "OK-MODEL-CHECK"
			switch {
			case strings.Contains(string(body), "json_reasoning") || strings.Contains(string(body), "SIGMA"):
				output = `{"result":83,"tag":"SIGMA"}`
			case strings.Contains(string(body), "code_judgement") || strings.Contains(string(body), "const xs"):
				output = "ALPHA 4-7"
			case strings.Contains(string(body), "refusal_boundary") || strings.Contains(string(body), "绕过他人"):
				output = "DELTA 不能提供"
			case strings.Contains(string(body), "sequence_transform") || strings.Contains(string(body), "从小到大"):
				output = "THETA 4|7|9"
			case strings.Contains(string(body), "table_extract") || strings.Contains(string(body), "北区"):
				output = "IOTA 17 23"
			case strings.Contains(string(body), "style_compact") || strings.Contains(string(body), "向量数据库"):
				output = "向量数据库召回率衡量结果相关内容被找回的比例"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"` + expectedModel + `","output_text":` + mustJSONText(t, output) + `,"usage":{"total_tokens":2}}`))
		}))
	}
	target := newServer("gpt-5.6-sol")
	defer target.Close()
	comparison := newServer("gpt-5.6-terra")
	defer comparison.Close()
	items, err := RunSuite(context.Background(), Suite{Endpoint: target.URL, Model: "gpt-5.6-sol", Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Comparison: &Suite{Endpoint: comparison.URL, Model: "gpt-5.6-terra", Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses}}, time.Second)
	var distribution, cross Evaluation
	for _, item := range items {
		switch item.Kind {
		case "distribution":
			distribution = item
		case "cross_model":
			cross = item
		}
	}
	if err != nil || distribution.Status != "passed" || cross.Status != "passed" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestRunSelfCrossModelUsesPairedModelOnSameEndpoint(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		requested = append(requested, request.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"` + request.Model + `","output_text":"CROSS-MODEL-OK","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	first := Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "OK-MODEL-CHECK"}
	item, err := RunSelfCrossModel(context.Background(), Suite{Endpoint: server.URL, Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses}, first, time.Second)
	if err != nil || item.Status != "passed" || len(requested) != 1 || requested[0] != "gpt-5.6-terra" {
		t.Fatalf("item=%+v requested=%v err=%v", item, requested, err)
	}
}

func mustJSONText(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
