package modelcheckprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestRunSuiteKeepsOrderedItemsAfterOneHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" && r.Header.Get("Accept") == "text/event-stream" {
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"STREAM-OK","usage":{"input_tokens":1}}`))
			return
		}
		if r.URL.Path == "/v1/responses" {
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
			return
		}
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16}, SuiteOptions{IncludeStream: true, IncludeStructured: true, IncludeTool: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || len(items) != 5 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if items[0].ItemKey != "target.responses_basic" || items[1].ItemKey != "target.responses_stream" || items[2].ItemKey != "target.structured_output" || items[3].ItemKey != "target.tool_calling" || items[4].ItemKey != "target.usage_shape" {
		t.Fatalf("item order=%#v", items)
	}
}

func TestRunSuiteEmitsEveryItemInReturnedOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	var observed []string
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16}, SuiteOptions{
		IncludeStructured: true,
		IncludeTool:       true,
		OnItem: func(item EvaluationItem) {
			observed = append(observed, item.ItemKey)
		},
	}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || len(items) != len(observed) {
		t.Fatalf("items=%#v observed=%#v err=%v", items, observed, err)
	}
	for index, item := range items {
		if observed[index] != item.ItemKey {
			t.Fatalf("index=%d observed=%q item=%q", index, observed[index], item.ItemKey)
		}
	}
}

func TestRunSuiteStopsAfterTerminalBasicFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16}, SuiteOptions{IncludeStream: true, IncludeStructured: true, IncludeTool: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 1 || calls != 1 {
		t.Fatalf("quick terminal failure items=%#v err=%v calls=%d", items, err, calls)
	}
	items, err = RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16}, SuiteOptions{IncludeStream: true, IncludeStructured: true, IncludeTool: true, IncludeUsageOnBasicFailure: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 2 || calls != 2 || items[1].ItemKey != "target.usage_shape" {
		t.Fatalf("full terminal failure items=%#v err=%v calls=%d", items, err, calls)
	}
}

func TestRunSuiteFullProfileAppendsBehaviorAfterCoreSuite(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16}, SuiteOptions{IncludeStructured: true, IncludeTool: true, IncludeBehavior: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 5 || calls != 11 {
		t.Fatalf("full suite items=%d calls=%d err=%v", len(items), calls, err)
	}
	if items[len(items)-1].ItemKey != "target.behavior_probe" {
		t.Fatalf("full suite tail=%#v", items[len(items)-1])
	}
}

func TestRunSuiteStopsAfterTerminalBehaviorProbe(t *testing.T) {
	calls := 0
	longOrStabilitySeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		body, _ := json.Marshal(payload)
		text := string(body)
		if strings.Contains(text, "NEEDLE-") || strings.Contains(text, "VECTOR") {
			longOrStabilitySeen = true
		}
		if strings.Contains(text, "QUARTZ") {
			http.Error(w, "terminal behavior failure", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16, ModelLimit: 8000, CountTokens: func(value string) int { return len([]rune(value)) }}, SuiteOptions{IncludeStructured: true, IncludeTool: true, IncludeBehavior: true, IncludeLongContext: true, IncludeStability: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 5 || calls != 4 || longOrStabilitySeen {
		t.Fatalf("terminal behavior items=%#v err=%v calls=%d postBehavior=%v", items, err, calls, longOrStabilitySeen)
	}
	if items[len(items)-1].ItemKey != "target.behavior_probe" {
		t.Fatalf("tail=%#v", items[len(items)-1])
	}
}

func TestRunSuiteStopsAfterTerminalLongContextProbe(t *testing.T) {
	calls := 0
	stabilitySeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		body, _ := json.Marshal(payload)
		text := string(body)
		if strings.Contains(text, "VECTOR") {
			stabilitySeen = true
		}
		if strings.Contains(text, "NEEDLE-") {
			http.Error(w, "terminal long context failure", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	items, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16, ModelLimit: 8000, CountTokens: func(value string) int { return len([]rune(value)) }}, SuiteOptions{IncludeStructured: true, IncludeTool: true, IncludeBehavior: true, IncludeLongContext: true, IncludeStability: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(items) != 6 || calls != 12 || stabilitySeen {
		t.Fatalf("terminal long context items=%#v err=%v calls=%d stability=%v", items, err, calls, stabilitySeen)
	}
	if items[len(items)-1].ItemKey != "target.long_context" {
		t.Fatalf("tail=%#v", items[len(items)-1])
	}
}

func TestRunSuitePassesStreamModeToStructuredAndTool(t *testing.T) {
	var streams []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if _, ok := payload["text"]; ok {
			if value, ok := payload["stream"].(bool); ok {
				streams = append(streams, value)
			}
		}
		if _, ok := payload["tools"]; ok {
			if value, ok := payload["stream"].(bool); ok {
				streams = append(streams, value)
			}
		}
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()
	_, err := RunSuite(context.Background(), BasicProbeInput{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", MaxOutputTokens: 16, Stream: true}, SuiteOptions{IncludeStructured: true, IncludeTool: true}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second}})
	if err != nil || len(streams) != 2 || !streams[0] || !streams[1] {
		t.Fatalf("structured/tool streams=%v err=%v", streams, err)
	}
}
