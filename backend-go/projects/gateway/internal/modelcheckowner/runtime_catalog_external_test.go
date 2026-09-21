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

// TestBusinessResolveRunsChatShapeAccountCatalogExternalModel 走真实
// BusinessTargetSource.Resolve + Runtime.Run 全链，证明 chat_json 请求形态的
// openai 兼容账户可以检测自己的目录外支持模型：Target 协议链跟随账户自身
// endpoint mode（openai_chat），探针项协议一致性子集齐全，全部请求落在
// /v1/chat/completions；目录内模型在同一账户上仍拒绝，且错误文本给出可操作
// 提示。
func TestBusinessResolveRunsChatShapeAccountCatalogExternalModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business-chat-external.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range append(businessSourceContractDDL(), runtimeTestDDL()...) {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
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
	if _, err := seed.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'` + server.URL + `/v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`INSERT INTO groups VALUES ('group-1','sys-1',1)`); err != nil {
		t.Fatal(err)
	}
	credential := testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["chat_json"]}`)
	if _, err := seed.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted,name) VALUES ('acct-chat','sys-1','openai','profile_openai_openai_v1','openai','api_key',1,1,'active',1,'chat_json',?,'Chat Shape')`, credential); err != nil {
		t.Fatal(err)
	}
	// 目录内模型拒绝用例的对照账户：mode 同为 chat_json，但支持模型只登记
	// 目录内 gpt 系。
	if _, err := seed.Exec(`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted,name) VALUES ('acct-chat-gpt','sys-1','openai','profile_openai_openai_v1','openai','api_key',1,1,'active',1,'chat_json',?,'Chat Shape GPT')`, credential); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-chat','sys-1','group-1',1),('acct-chat-gpt','sys-1','group-1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`INSERT INTO account_supported_models VALUES ('acct-chat','deepseek-v4.1-flash'),('acct-chat-gpt','gpt-5.6-sol')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := NewBusinessTargetSource(store.db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	target, err := source.Resolve(ctx, RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-chat", Model: "deepseek-v4.1-flash"})
	if err != nil {
		t.Fatalf("chat-shape catalog-external resolve failed: %v", err)
	}
	if target.Protocol != modelcheckprofile.ProtocolOpenAIChat || target.UpstreamProtocol != modelcheckprofile.ProtocolOpenAIChat {
		t.Fatalf("catalog-external target must follow the account chat protocol: protocol=%s upstream=%s", target.Protocol, target.UpstreamProtocol)
	}
	if target.EndpointMode != modelcheckprofile.EndpointModeChatJSON || target.UpstreamEndpointMode != modelcheckprofile.EndpointModeChatJSON {
		t.Fatalf("catalog-external target must keep the account endpoint mode: mode=%s upstreamMode=%s", target.EndpointMode, target.UpstreamEndpointMode)
	}
	if target.UpstreamModel != "deepseek-v4.1-flash" || target.Headers.Get("Authorization") != "Bearer key" {
		t.Fatalf("target model/headers=%+v headers=%v", target.UpstreamModel, target.Headers)
	}
	_, catalogErr := source.Resolve(ctx, RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-chat-gpt", Model: "gpt-5.6-sol"})
	if catalogErr == nil {
		t.Fatal("catalog model on a chat-shape account must stay rejected")
	}
	for _, fragment := range []string{"请将检查请求形态切换为 responses_json", "或选择账户支持模型作为检测目标"} {
		if !strings.Contains(catalogErr.Error(), fragment) {
			t.Fatalf("catalog rejection must carry the actionable hint %q: %v", fragment, catalogErr)
		}
	}
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time { return now }, Resolve: source.Resolver()}
	request, err := source.BuildRequest(ctx, "sys-1", RunCommand{TargetType: "account", TargetID: "acct-chat", Model: "deepseek-v4.1-flash", Profile: "quick"})
	if err != nil {
		t.Fatalf("freeze chat-shape request: %v", err)
	}
	result, err := runtime.Run(ctx, request)
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("chat-shape catalog-external run result=%+v err=%v", result, err)
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
	if snapshot["protocol"] != string(modelcheckprofile.ProtocolOpenAIChat) || snapshot["upstreamProtocol"] != string(modelcheckprofile.ProtocolOpenAIChat) {
		t.Fatalf("run snapshot protocol=%v upstreamProtocol=%v", snapshot["protocol"], snapshot["upstreamProtocol"])
	}
	if snapshot["upstreamEndpointMode"] != modelcheckprofile.EndpointModeChatJSON {
		t.Fatalf("run snapshot upstreamEndpointMode=%v", snapshot["upstreamEndpointMode"])
	}
	if snapshot["detectionScope"] != "protocol_consistency" {
		t.Fatalf("run snapshot detectionScope=%v", snapshot["detectionScope"])
	}
	if level, _ := report["level"].(string); level == "unavailable" {
		t.Fatalf("chat-shape catalog-external run must form a quality level, report=%s", resultSummary)
	}
	items := readRunItemKinds(t, store, result.RunID)
	for _, kind := range []string{"protocol_basic", "structured_output", "tool_calling", "usage_shape"} {
		if _, ok := items[kind]; !ok {
			t.Fatalf("protocol-consistency item %s missing: %v", kind, items)
		}
	}
	if kind, ok := items["cross_model"]; !ok || kind != "skipped" {
		t.Fatalf("catalog-external cross_model must be skipped: %v", items)
	}
}

// TestRuntimeFullProfileSkipsTokenIntegrityForCatalogExternalModel 证明
// openai_responses 协议账户上的目录外模型在 full 档不会跑 GPT tokenizer
// 差分基线：token_integrity 以 model_catalog_scope_not_applicable 跳过、
// excludedFromScoring，且以 Score=0/MaxScore=0 落库（不进评分分母），品牌
// tokenizer 基线无法再压低目录外模型分数并触发处罚。
func TestRuntimeFullProfileSkipsTokenIntegrityForCatalogExternalModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-full-external.db")
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
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected probe path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		model, _ := request["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"` + model + `","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "status"):
			_, _ = w.Write([]byte(`{"model":"` + model + `","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"` + model + `","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
		}
	}))
	defer server.Close()
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time { return now }, Resolve: func(_ context.Context, request RunRequest) (Target, error) {
		return Target{
			Endpoint: server.URL, TargetName: "OpenAI Compatible", TargetOwnerSystemAccountID: request.SystemAccountID, GroupID: "group-1",
			ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key",
			Protocol: modelcheckprofile.ProtocolOpenAIResponses, UpstreamProtocol: modelcheckprofile.ProtocolOpenAIResponses,
			EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, UpstreamEndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
			SupportedEndpointModes: []string{modelcheckprofile.EndpointModeResponsesJSON},
			SupportedModels:        []string{"deepseek-v4.1-flash"},
			UpstreamModel:          request.Model, Prompt: "Reply with exactly: OK-MODEL-CHECK", DispatchRevision: 3,
		}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "deepseek-v4.1-flash", Profile: "full", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", Threshold: 70})
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("full catalog-external run result=%+v err=%v", result, err)
	}
	var score, maxScore int
	var status string
	if err := store.db.QueryRow(`SELECT status,score,max_score FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status, &score, &maxScore); err != nil || status != string(RunCompleted) {
		t.Fatalf("run row status=%s err=%v", status, err)
	}
	var tokenStatus string
	var tokenScore, tokenMaxScore int
	if err := store.db.QueryRow(`SELECT status,score,max_score FROM model_check_items WHERE run_id=? AND item_type='token_integrity'`, result.RunID).Scan(&tokenStatus, &tokenScore, &tokenMaxScore); err != nil {
		t.Fatalf("token_integrity item must exist: %v", err)
	}
	if tokenStatus != "skipped" || tokenScore != 0 || tokenMaxScore != 0 {
		t.Fatalf("token_integrity must be an excluded skip, got status=%s score=%d maxScore=%d", tokenStatus, tokenScore, tokenMaxScore)
	}
	var resultSummary string
	if err := store.db.QueryRow(`SELECT result_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&resultSummary); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Evaluations []struct {
			Kind     string         `json:"kind"`
			Status   string         `json:"status"`
			Evidence map[string]any `json:"evidence"`
		} `json:"evaluations"`
	}
	if err := json.Unmarshal([]byte(resultSummary), &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, evaluation := range report.Evaluations {
		if evaluation.Kind != "token_integrity" {
			continue
		}
		found = true
		if evaluation.Evidence["excludedFromScoring"] != true || evaluation.Evidence["reason"] != "model_catalog_scope_not_applicable" {
			t.Fatalf("token_integrity evidence=%v", evaluation.Evidence)
		}
	}
	if !found {
		t.Fatalf("token_integrity evaluation missing in report: %s", resultSummary)
	}
	if !strings.Contains(resultSummary, `"detectionScope":"protocol_consistency"`) {
		t.Fatalf("full catalog-external report must carry detectionScope: %s", resultSummary)
	}
}
