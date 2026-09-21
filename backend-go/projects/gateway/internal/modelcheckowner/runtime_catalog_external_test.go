package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// TestRuntimeRunsCatalogExternalAccountSupportedModel 证明账户支持模型中的
// 目录外目标可以真实发起并产出成功报告：探针只覆盖协议一致性子集（品牌
// 专属隐藏探针与基线自动跳过），报告与请求快照携带 detectionScope 标注；
// 目录内模型的持久化形态不含该键，保持既有字节行为。
func TestRuntimeRunsCatalogExternalAccountSupportedModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-catalog-external.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected probe path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		model, _ := request["model"].(string)
		if model == "" {
			t.Fatalf("probe payload must carry the target model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"` + model + `","choices":[{"message":{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}}]}}],"usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "status"):
			_, _ = w.Write([]byte(`{"model":"` + model + `","choices":[{"message":{"role":"assistant","content":"{\"status\":\"ok\",\"value\":7}"}}],"usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"` + model + `","choices":[{"message":{"role":"assistant","content":"OK-MODEL-CHECK"}}],"usage":{"total_tokens":2}}`))
		}
	}))
	defer server.Close()
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	newRuntime := func() *Runtime {
		return &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time { return now }, Resolve: func(_ context.Context, request RunRequest) (Target, error) {
			return Target{
				Endpoint: server.URL, TargetName: "OpenAI Compatible", TargetOwnerSystemAccountID: request.SystemAccountID, GroupID: "group-1",
				ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key",
				Protocol: modelcheckprofile.ProtocolOpenAIChat, UpstreamProtocol: modelcheckprofile.ProtocolOpenAIChat,
				EndpointMode: modelcheckprofile.EndpointModeChatJSON, UpstreamEndpointMode: modelcheckprofile.EndpointModeChatJSON,
				SupportedEndpointModes: []string{modelcheckprofile.EndpointModeChatJSON},
				SupportedModels:        []string{"deepseek-v4.1-flash", "grok-4.5", "grok-4.6", "gpt-5.6-sol"},
				UpstreamModel:          request.Model, Prompt: "Reply with exactly: OK-MODEL-CHECK", DispatchRevision: 3,
			}, nil
		}}
	}
	external := newRuntime()
	result, err := external.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "deepseek-v4.1-flash", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("catalog-external run result=%+v err=%v", result, err)
	}
	var requestSummary, resultSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json,result_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&requestSummary, &resultSummary); err != nil {
		t.Fatal(err)
	}
	var snapshot, report map[string]any
	if err := json.Unmarshal([]byte(requestSummary), &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(resultSummary), &report); err != nil {
		t.Fatal(err)
	}
	if snapshot["detectionScope"] != "protocol_consistency" || report["detectionScope"] != "protocol_consistency" {
		t.Fatalf("catalog-external run must be labelled protocol_consistency: snapshot=%v report=%v", snapshot["detectionScope"], report["detectionScope"])
	}
	if snapshot["model"] != "deepseek-v4.1-flash" || snapshot["upstreamModel"] != "deepseek-v4.1-flash" {
		t.Fatalf("snapshot models=%v/%v", snapshot["model"], snapshot["upstreamModel"])
	}
	if level, _ := report["level"].(string); level == "unavailable" {
		t.Fatalf("catalog-external run must form a quality level, report=%s", resultSummary)
	}
	items := readRunItemKinds(t, store, result.RunID)
	for _, kind := range []string{"protocol_basic", "structured_output", "tool_calling", "usage_shape"} {
		if _, ok := items[kind]; !ok {
			t.Fatalf("protocol-consistency item %s missing: %v", kind, items)
		}
	}
	if kind, ok := items["cross_model"]; !ok || kind != "skipped" {
		t.Fatalf("catalog-external cross_model must be skipped without a paired model: %v", items)
	}
	catalog := newRuntime()
	catalogResult, err := catalog.Run(context.Background(), RunRequest{SystemAccountID: "sys-2", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct-2", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-2", PolicyRevision: "pol-2"})
	if err != nil || catalogResult.Status != string(RunCompleted) || catalogResult.RunID == "" {
		t.Fatalf("catalog run result=%+v err=%v", catalogResult, err)
	}
	var catalogSummary, catalogResultSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json,result_summary_json FROM model_check_runs WHERE id=?`, catalogResult.RunID).Scan(&catalogSummary, &catalogResultSummary); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalogSummary, "detectionScope") || strings.Contains(catalogResultSummary, "detectionScope") {
		t.Fatalf("catalog run must keep its durable byte shape without detectionScope: request=%s result=%s", catalogSummary, catalogResultSummary)
	}
}

func readRunItemKinds(t *testing.T, store *Store, runID string) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT item_type,status FROM model_check_items WHERE run_id=?`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := make(map[string]string)
	for rows.Next() {
		var kind, status string
		if err := rows.Scan(&kind, &status); err != nil {
			t.Fatal(err)
		}
		kinds[kind] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return kinds
}
