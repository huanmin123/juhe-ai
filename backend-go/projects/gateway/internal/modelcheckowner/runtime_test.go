package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestHasTerminalEvidenceIgnoresTokenIntegrityFailures(t *testing.T) {
	if hasTerminalEvidence([]map[string]any{{
		"kind":     "target.token_integrity",
		"evidence": map[string]any{"terminalFailure": true},
	}}) {
		t.Fatal("token integrity terminal failure must not suppress the quality decision")
	}
	if !hasTerminalEvidence([]map[string]any{{
		"kind":     "target.protocol_basic",
		"evidence": map[string]any{"terminalFailure": true},
	}}) {
		t.Fatal("transport probe terminal failure must suppress the quality decision")
	}
}

// BUG-0259 / §1.2：非 basic 核心协议探针的终态请求失败同样必须抑制质量
// 判定，与 basic 终态失败走同一 hasTerminalEvidence 门。
func TestHasTerminalEvidenceCoversNonBasicCoreProbes(t *testing.T) {
	for _, kind := range []string{"tool_calling", "structured_output", "protocol_stream", "responses_stream"} {
		if !hasTerminalEvidence([]map[string]any{{
			"kind":     kind,
			"evidence": map[string]any{"requestFailure": true, "terminalFailure": true},
		}}) {
			t.Fatalf("%s 终态失败必须抑制质量判定", kind)
		}
	}
	// 无 terminalFailure 的请求失败证据（重试中非终态）不得抑制；题库环节的
	// 真实终态失败会带 terminalFailure 并走任意族抑制（§5.7 整跑失败不扣分），
	// 不因其缺席本门而漏抑制。
	if hasTerminalEvidence([]map[string]any{{
		"kind":     "custom_quiz",
		"evidence": map[string]any{"requestFailure": true, "excludedFromScoring": true},
	}}) {
		t.Fatal("request failure without terminal evidence must not suppress the quality decision")
	}
}

// BUG-0259 / §1.2：核心协议探针（tool）第三次仍非 HTTP 200 时整轮判
// unavailable，且必须抑制质量判定（not_triggered、不处罚、不写健康事实），
// 不得按部分证据正常计分为达标。
func TestRuntimeSuppressesQualityDecisionOnCoreProbeTerminalFailure(t *testing.T) {
	server := w14mNewProbeServer(t, func(body string) (int, string) {
		if strings.Contains(body, "record_model_check") {
			return http.StatusServiceUnavailable, `{"error":"tool probe unavailable"}`
		}
		if strings.Contains(body, `"status"`) {
			return http.StatusOK, w14mOKResponse("gpt-5.6-sol", `{"status":"ok","value":7}`)
		}
		return http.StatusOK, w14mOKResponse("gpt-5.6-sol", "OK-MODEL-CHECK")
	})
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	store.HealthStatHour = mustHealthStatHourFunc(t, "UTC")
	enforcement := &countingEnforcement{}
	runtime := &Runtime{Store: store, OwnerID: "gateway-b0259", Now: w14mFrozenNow, Resolve: w14mResolveOK(server.URL), Projector: &QualityProjector{Store: store, Enforcement: enforcement}}
	request := w14mQuickRequest()
	request.Threshold = 70
	request.ProviderCode = "openai"
	request.TriggerKind = "scheduled"
	result, err := runtime.Run(context.Background(), request)
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var level string
	if err := store.db.QueryRow(`SELECT level FROM model_check_runs WHERE id=?`, result.RunID).Scan(&level); err != nil || level != "unavailable" {
		t.Fatalf("level=%q err=%v，核心探针终态失败必须判整轮不可用", level, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["modelCheckUnverified"] != true {
		t.Fatalf("data=%v，want modelCheckUnverified=true", result.Data)
	}
	if reason, _ := data["qualityDecisionSuppressedReason"].(string); reason != "未形成质量判定证据" {
		t.Fatalf("qualityDecisionSuppressedReason=%v", data["qualityDecisionSuppressedReason"])
	}
	var decision string
	if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(decision), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["result"] != "not_triggered" || fields["triggered"] != false || fields["modelCheckUnverified"] != true || fields["qualityDecisionSuppressedReason"] != "未形成质量判定证据" {
		t.Fatalf("decision=%+v", fields)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM account_quality_health_hourly`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("健康事实行数=%d err=%v，抑制链路不得写质量健康失败统计", count, err)
	}
	if enforcement.calls != 0 {
		t.Fatalf("处罚调用次数=%d，终态失败不得处罚", enforcement.calls)
	}
}

func TestRuntimeExecutesAndPersistsBasicProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
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
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "VECTOR"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"VECTOR","usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "status"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
		}
	}))
	defer server.Close()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time { return now }, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: server.URL, TargetName: "Runtime Account", TargetOwnerSystemAccountID: "sys", GroupID: "group-runtime", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", DispatchRevision: 3}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", ManualEnforcementEnabled: true, OwnPhysicalAccount: true})
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("runtime result data=%T, want map", result.Data)
	}
	if formed, ok := data["evidenceFormed"].(bool); !ok || formed {
		t.Fatalf("quick profile evidenceFormed=%v, want explicit false", data["evidenceFormed"])
	}
	if trusted, ok := data["trustFormed"].(bool); !ok || trusted {
		t.Fatalf("quick profile trustFormed=%v, want explicit false", data["trustFormed"])
	}
	var status, targetName, targetOwner, groupID, traceID string
	if err := store.db.QueryRow(`SELECT status,target_name,target_owner_system_account_id,group_id,trace_id FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status, &targetName, &targetOwner, &groupID, &traceID); err != nil || status != string(RunCompleted) || targetName != "Runtime Account" || targetOwner != "sys" || groupID != "group-runtime" || traceID == "" {
		t.Fatalf("run projection status=%s targetName=%q targetOwner=%q groupID=%q traceID=%q err=%v", status, targetName, targetOwner, groupID, traceID, err)
	}
	var requestSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&requestSummary); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(requestSummary), &snapshot); err != nil || snapshot["targetName"] != "Runtime Account" || snapshot["targetOwnerSystemAccountId"] != "sys" || snapshot["groupId"] != "group-runtime" || snapshot["traceId"] != traceID || snapshot["configRevision"] != "cfg-1" || snapshot["policyRevision"] != "pol-1" || snapshot["manualEnforcementEnabled"] != true || snapshot["ownPhysicalAccount"] != true {
		t.Fatalf("request snapshot=%s err=%v", requestSummary, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id=?`, result.RunID).Scan(&count); err != nil || count != 5 {
		t.Fatalf("item count=%d err=%v", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("quick observation count=%d err=%v, quick checks must be diagnostic-only", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_trust_observation_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("quick trust receipt count=%d err=%v", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=? AND aggregation_completed_at IS NOT NULL`, result.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("quick consumed observation count=%d err=%v", count, err)
	}
	var observationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6-sol'`).Scan(&observationCount); err != nil || observationCount != 0 {
		t.Fatalf("quick trust latest rows=%d err=%v, quick checks must not update latest", observationCount, err)
	}
	var evidenceSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&evidenceSummary); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(evidenceSummary, server.URL) {
		t.Fatalf("durable request summary leaked endpoint: %s", evidenceSummary)
	}
}

func TestRuntimeKeepsHTTP200QualityFailureCompleted(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"WRONG-CONTENT","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-quality",
		Now:     func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", DispatchRevision: 1}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("HTTP 200 quality failure must complete durably: result=%+v err=%v", result, err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("durable status=%q err=%v, want completed", status, err)
	}
	var itemCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id=?`, result.RunID).Scan(&itemCount); err != nil || itemCount == 0 {
		t.Fatalf("quality result item count=%d err=%v, want at least one", itemCount, err)
	}
	var level string
	if err := store.db.QueryRow(`SELECT level FROM model_check_runs WHERE id=?`, result.RunID).Scan(&level); err != nil || level == "likely" || level == "high_confidence" {
		t.Fatalf("negative HTTP 200 evidence level=%q err=%v, want non-positive quality level", level, err)
	}
}

// quality_decision_json v2：finalize 写入判定基础字段，健康投影完成后由
// MergeRunQualityDecision 增补处罚结果字段；RunResult.Data 暴露 profile 与
// hardFailure 供恢复门槛使用。
func TestRuntimeWritesQualityDecisionV2Fields(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	store.HealthStatHour = mustHealthStatHourFunc(t, "UTC")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"WRONG-CONTENT","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	enforcement := &countingEnforcement{}
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-decision",
		Now:     func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", DispatchRevision: 1}, nil
		},
		Projector: &QualityProjector{Store: store, Enforcement: enforcement},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ProviderCode: "openai", Threshold: 80, TriggerKind: "scheduled", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("runtime result data=%T, want map", result.Data)
	}
	if data["profile"] != "quick" {
		t.Fatalf("result data profile=%v", data["profile"])
	}
	if hardFailure, ok := data["hardFailure"].(bool); !ok || hardFailure {
		t.Fatalf("result data hardFailure=%v", data["hardFailure"])
	}
	var decision string
	if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(decision), &fields); err != nil {
		t.Fatal(err)
	}
	// finalize 基础字段。
	if fields["triggerKind"] != "scheduled" || fields["triggered"] != true || fields["hardFailure"] != false || fields["configuredAction"] != "quality_isolate" || fields["decidedAt"] == "" {
		t.Fatalf("decision base fields=%+v", fields)
	}
	if _, ok := fields["reasonCodes"].([]any); !ok {
		t.Fatalf("decision reasonCodes=%v", fields["reasonCodes"])
	}
	if score, ok := fields["score"].(float64); !ok || score < 0 || score > 100 {
		t.Fatalf("decision score=%v", fields["score"])
	}
	if threshold, ok := fields["threshold"].(float64); !ok || threshold != 80 {
		t.Fatalf("decision threshold=%v", fields["threshold"])
	}
	// 投影增补字段：低分触发处罚并同步健康事实。
	if fields["result"] != "applied" || fields["healthSyncResult"] != "applied" || fields["enforcementId"] != "enf-counting" || fields["generation"] != float64(1) || fields["beforeStatus"] != "active" || fields["afterStatus"] != "quality_isolated" || fields["recoveryDueAt"] == "" || fields["healthStatHour"] == "" {
		t.Fatalf("decision merged fields=%+v", fields)
	}
	if enforcement.calls != 1 {
		t.Fatalf("enforcement calls=%d", enforcement.calls)
	}
}

func TestRuntimeRejectsIncompleteTargetContract(t *testing.T) {
	store := &Store{}
	runtime := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: "https://example.invalid", Prompt: "OK", DispatchRevision: 3}, nil
	}}
	base := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick"}
	for name, request := range map[string]RunRequest{
		"missing target type": func() RunRequest { value := base; value.TargetType = "group"; return value }(),
		"missing profile":     func() RunRequest { value := base; value.Profile = ""; return value }(),
		"invalid profile":     func() RunRequest { value := base; value.Profile = "fast"; return value }(),
		"missing actor":       func() RunRequest { value := base; value.ActorSystemAccountID = ""; return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.Run(context.Background(), request); err == nil {
				t.Fatal("invalid runtime request must be rejected")
			}
		})
	}
}

func TestRuntimeRejectsStaleDispatchRevision(t *testing.T) {
	runtime := &Runtime{Store: &Store{}, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: "https://example.invalid", Prompt: "OK", DispatchRevision: 7}, nil
	}}
	request := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", DispatchRevision: 6}
	if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "dispatch revision") {
		t.Fatalf("stale dispatch revision must be rejected, err=%v", err)
	}
}

func TestRuntimeRejectsStaleSourceRevision(t *testing.T) {
	runtime := &Runtime{Store: &Store{}, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: "https://example.invalid", Prompt: "OK", DispatchRevision: 7, SourceConfigRevision: "source-7", SourceDispatchRevision: 9}, nil
	}}
	base := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick"}
	t.Run("config", func(t *testing.T) {
		request := base
		request.SourceConfigRevision = "source-6"
		if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "source account config revision") {
			t.Fatalf("stale source config revision must be rejected, err=%v", err)
		}
	})
	t.Run("dispatch", func(t *testing.T) {
		request := base
		request.SourceDispatchRevision = 8
		if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "source account dispatch revision") {
			t.Fatalf("stale source dispatch revision must be rejected, err=%v", err)
		}
	})
}

func TestRuntimeRejectsStaleTrustedComparisonRevision(t *testing.T) {
	runtime := &Runtime{
		Store: &Store{},
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: "https://target.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 3, ConfigRevision: "cfg-1", SourceConfigRevision: "src-1", SourceDispatchRevision: 4}, nil
		},
		ResolveComparison: func(_ context.Context, request RunRequest) (Target, error) {
			if request.TargetID != "comparison" || request.ConfigRevision != "cfg-2" || request.DispatchRevision != 8 || request.SourceConfigRevision != "src-2" || request.SourceDispatchRevision != 9 {
				return Target{}, fmt.Errorf("comparison resolver received incomplete frozen revisions: %+v", request)
			}
			return Target{Endpoint: "https://comparison.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 8, ConfigRevision: "cfg-2", SourceConfigRevision: "src-2", SourceDispatchRevision: 9}, nil
		},
	}
	base := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "target", Model: "gpt-5.6", Profile: "full", TrustedComparison: true, TrustedComparisonAccountID: "comparison", TrustedComparisonSystemAccountID: "comparison-sys", ConfigRevision: "cfg-1", TrustedComparisonConfigRevision: "cfg-2", TrustedComparisonDispatchRevision: 8, TrustedComparisonSourceConfigRevision: "src-2", TrustedComparisonSourceDispatchRevision: 9}
	for name, mutate := range map[string]func(*RunRequest){
		"system account":  func(request *RunRequest) { request.TrustedComparisonSystemAccountID = "" },
		"dispatch":        func(request *RunRequest) { request.TrustedComparisonDispatchRevision = 7 },
		"source config":   func(request *RunRequest) { request.TrustedComparisonSourceConfigRevision = "src-1" },
		"source dispatch": func(request *RunRequest) { request.TrustedComparisonSourceDispatchRevision = 8 },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := runtime.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "trusted comparison") {
				t.Fatalf("stale trusted comparison revision must be rejected, err=%v", err)
			}
		})
	}
}

func TestRuntimeRejectsIncompatibleTrustedComparisonProfile(t *testing.T) {
	baseTarget := Target{Endpoint: "https://target.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 3, ConfigRevision: "cfg-1", SourceConfigRevision: "src-1", SourceDispatchRevision: 4}
	baseRequest := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "target", Model: "gpt-5.6", Profile: "full", TrustedComparison: true, TrustedComparisonAccountID: "comparison", TrustedComparisonSystemAccountID: "comparison-sys", ConfigRevision: "cfg-1", TrustedComparisonConfigRevision: "cfg-2", TrustedComparisonDispatchRevision: 8, TrustedComparisonSourceConfigRevision: "src-2", TrustedComparisonSourceDispatchRevision: 9}
	for name, comparison := range map[string]Target{
		"provider":        {Endpoint: "https://comparison.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "gemini", ProviderProtocolProfileID: "profile_gemini_openai_chat_v1beta", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 8, ConfigRevision: "cfg-2", SourceConfigRevision: "src-2", SourceDispatchRevision: 9},
		"protocol":        {Endpoint: "https://comparison.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Protocol: modelcheckprofile.ProtocolOpenAIChat, DispatchRevision: 8, ConfigRevision: "cfg-2", SourceConfigRevision: "src-2", SourceDispatchRevision: 9},
		"profile":         {Endpoint: "https://comparison.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_other_v1", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 8, ConfigRevision: "cfg-2", SourceConfigRevision: "src-2", SourceDispatchRevision: 9},
		"missing profile": {Endpoint: "https://comparison.example", Prompt: "OK", UpstreamModel: "gpt-5.6", ProviderCode: "openai", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 8, ConfigRevision: "cfg-2", SourceConfigRevision: "src-2", SourceDispatchRevision: 9},
	} {
		t.Run(name, func(t *testing.T) {
			runtime := &Runtime{Store: &Store{}, Resolve: func(context.Context, RunRequest) (Target, error) { return baseTarget, nil }, ResolveComparison: func(context.Context, RunRequest) (Target, error) { return comparison, nil }}
			if _, err := runtime.Run(context.Background(), baseRequest); err == nil || !strings.Contains(err.Error(), "trusted comparison") {
				t.Fatalf("incompatible trusted comparison must be rejected, err=%v", err)
			}
		})
	}
}

func TestRuntimeManualEnforcementRequiresEnabledPhysicalAccount(t *testing.T) {
	for name, input := range map[string]struct {
		trigger string
		request RunRequest
		want    bool
	}{
		"manual enabled physical":               {request: RunRequest{ManualEnforcementEnabled: true, OwnPhysicalAccount: true}, want: true},
		"manual disabled physical":              {request: RunRequest{ManualEnforcementEnabled: false, OwnPhysicalAccount: true}, want: false},
		"manual enabled authorization instance": {request: RunRequest{ManualEnforcementEnabled: true, OwnPhysicalAccount: false}, want: false},
		"scheduled remains automatic":           {trigger: "scheduled", request: RunRequest{}, want: true},
		"recovery remains automatic":            {trigger: "quality_recovery", request: RunRequest{}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runtimeEnforcementAllowed(input.trigger, input.request); got != input.want {
				t.Fatalf("enforcement allowed=%v want=%v", got, input.want)
			}
		})
	}
}

func TestRuntimeUsesAndFreezesResolvedUpstreamModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-mapped-model.db")
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
	var mu sync.Mutex
	models := make([]string, 0, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read probe request: %v", err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode probe request: %v", err)
		}
		mu.Lock()
		models = append(models, request.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "VECTOR"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output_text":"VECTOR","usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "status"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
		}
	}))
	defer server.Close()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	runtime := &Runtime{Store: store, OwnerID: "gateway-1", Now: func() time.Time { return now }, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra", DispatchRevision: 3}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	if len(models) != 3 {
		mu.Unlock()
		t.Fatalf("terminal quick suite probe calls=%d models=%v", len(models), models)
	}
	resolvedCount := 0
	for _, model := range models {
		switch model {
		case "gpt-5.6-terra":
			resolvedCount++
		default:
			mu.Unlock()
			t.Fatalf("probe used unexpected model %q", model)
		}
	}
	if resolvedCount != 3 {
		mu.Unlock()
		t.Fatalf("probe model distribution resolved=%d models=%v", resolvedCount, models)
	}
	mu.Unlock()
	var requestSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&requestSummary); err != nil {
		t.Fatal(err)
	}
	var frozen map[string]any
	if err := json.Unmarshal([]byte(requestSummary), &frozen); err != nil || frozen["model"] != "gpt-5.6-sol" || frozen["upstreamModel"] != "gpt-5.6-terra" || frozen["protocol"] != string(modelcheckprofile.ProtocolOpenAIResponses) || frozen["endpointFingerprint"] != endpointFingerprint(server.URL) || strings.Contains(requestSummary, server.URL) {
		t.Fatalf("frozen request=%s err=%v", requestSummary, err)
	}
	var observationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&observationCount); err != nil || observationCount != 0 {
		t.Fatalf("quick mapping observation count=%d err=%v", observationCount, err)
	}
}

func TestRuntimeExecutesAndFreezesTrustedComparison(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-trusted-comparison.db")
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
	var mu sync.Mutex
	requests := map[string]int{}
	newServer := func(model string) *httptest.Server {
		return httptest.NewServer(&runtimeModelServer{t: t, expectedModel: model, mu: &mu, requests: requests})
	}
	targetServer := newServer("gpt-5.6-sol")
	defer targetServer.Close()
	comparisonServer := newServer("gpt-5.6-terra")
	defer comparisonServer.Close()
	if targetServer.URL == comparisonServer.URL {
		t.Fatal("trusted comparison test servers must be distinct")
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	runtime := &Runtime{
		Store:       store,
		OwnerID:     "gateway-1",
		Tokenizer:   runtimeTestTokenizer{},
		ModelLimits: runtimeTestModelLimits{},
		Now:         func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: targetServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "openai-responses", ConfigRevision: "cfg-1", DispatchRevision: 3, SourceConfigRevision: "src-1", SourceDispatchRevision: 4}, nil
		},
		ResolveComparison: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: comparisonServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra", ProviderCode: "openai", ProviderProtocolProfileID: "openai-responses", ConfigRevision: "cfg-2", DispatchRevision: 4, SourceConfigRevision: "src-2", SourceDispatchRevision: 5}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TrustedComparison: true, TrustedComparisonAccountID: "comparison-acct", TrustedComparisonSystemAccountID: "comparison-sys", TrustedComparisonConfigRevision: "cfg-2", TrustedComparisonDispatchRevision: 4, TrustedComparisonSourceConfigRevision: "src-2", TrustedComparisonSourceDispatchRevision: 5})
	if err != nil || result.RunID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	targetRequests, comparisonRequests := requests["gpt-5.6-sol"], requests["gpt-5.6-terra"]
	mu.Unlock()
	if targetRequests == 0 || comparisonRequests == 0 {
		t.Fatalf("trusted comparison probe calls target=%d comparison=%d", targetRequests, comparisonRequests)
	}
	var requestSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&requestSummary); err != nil {
		t.Fatal(err)
	}
	var frozen map[string]any
	if err := json.Unmarshal([]byte(requestSummary), &frozen); err != nil {
		t.Fatal(err)
	}
	comparison, ok := frozen["trustedComparison"].(map[string]any)
	if !ok || comparison["accountId"] != "comparison-acct" || comparison["systemAccountId"] != "comparison-sys" || comparison["configRevision"] != "cfg-2" || comparison["dispatchRevision"] != float64(4) || comparison["sourceConfigRevision"] != "src-2" || comparison["sourceDispatchRevision"] != float64(5) || comparison["upstreamModel"] != "gpt-5.6-terra" || comparison["endpointFingerprint"] != endpointFingerprint(comparisonServer.URL) {
		t.Fatalf("frozen trusted comparison=%#v", frozen["trustedComparison"])
	}
	var trustedComparisonEnabled, trustedComparisonAvailable int
	if err := store.db.QueryRow(`SELECT trusted_comparison_enabled,trusted_comparison_available FROM model_check_runs WHERE id=?`, result.RunID).Scan(&trustedComparisonEnabled, &trustedComparisonAvailable); err != nil || trustedComparisonEnabled != 1 || trustedComparisonAvailable != 1 {
		t.Fatalf("trusted comparison durable state enabled=%d available=%d err=%v", trustedComparisonEnabled, trustedComparisonAvailable, err)
	}
	var observations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&observations); err != nil || observations < 10 {
		t.Fatalf("observations=%d err=%v", observations, err)
	}
	var trusted int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=? AND probe_family LIKE 'trusted_comparison.%'`, result.RunID).Scan(&trusted); err != nil || trusted != 0 {
		t.Fatalf("trusted comparison observations=%d err=%v", trusted, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_account_trust_results WHERE system_account_id='comparison-sys' AND account_id='comparison-acct' AND requested_model='gpt-5.6-sol'`).Scan(&trusted); err != nil || trusted != 0 {
		t.Fatalf("trusted comparison projection=%d err=%v", trusted, err)
	}
	var resultSummary string
	if err := store.db.QueryRow(`SELECT result_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&resultSummary); err != nil {
		t.Fatal(err)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resultSummary), &summary); err != nil {
		t.Fatal(err)
	}
	var report TrustReport
	if err := json.Unmarshal(summary["trustReport"], &report); err != nil {
		t.Fatalf("full run trustReport missing or invalid: %v summary=%s", err, resultSummary)
	}
	if report.ProtocolStatus == "" || report.IdentityStatus == "" || report.EvidenceCoverage < 0 || report.EvidenceCoverage > 100 {
		t.Fatalf("full run trustReport=%+v", report)
	}
	var latestProtocol, latestIdentity string
	var latestCoverage int
	if err := store.db.QueryRow(`SELECT protocol_status,identity_status,evidence_coverage FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6-sol'`).Scan(&latestProtocol, &latestIdentity, &latestCoverage); err != nil {
		t.Fatal(err)
	}
	if latestProtocol != report.ProtocolStatus || latestIdentity != report.IdentityStatus || latestCoverage != report.EvidenceCoverage {
		t.Fatalf("full run trust/latest drift report=%+v latest=(protocol=%q identity=%q coverage=%d)", report, latestProtocol, latestIdentity, latestCoverage)
	}
}

// TestRuntimeFullRunFormsUniversalEvidenceBothWays 验证通用套件 v5（退役
// juice/astra/identity/自配 cross_model 后）的 full 档证据形成：
//   - 非信任路径：6 族通用证据表中 distribution 以 trusted_comparison_not_attached
//     中性跳过，其余族形成完整回执 → evidenceFormed/trustFormed 均为真；
//   - 信任路径：distribution_similarity/comparison 项映射回通用族 → 同样形成；
//   - 形成证据的质量失败（长上下文失败驱动 suspicious）不再被健康投影门阻断，
//     健康事实与处罚结果正常落地（health.go 投影门随族表收敛）。
func TestRuntimeFullRunFormsUniversalEvidenceBothWays(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	statHour, err := NewHealthStatHourFunc("UTC")
	if err != nil {
		t.Fatal(err)
	}
	store.HealthStatHour = statHour
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		model := request.Model
		if model == "" {
			model = "gpt-5.6-sol"
		}
		w.Header().Set("Content-Type", "application/json")
		output := "OK-MODEL-CHECK"
		usage := `{"input_tokens":10,"total_tokens":11}`
		switch {
		case strings.Contains(text, "Controlled token integrity probe"):
			output = "OK"
			local := strings.Count(text, " x") + 1
			usage = fmt.Sprintf(`{"input_tokens":%d,"total_tokens":%d}`, local, local+1)
		case strings.Contains(text, "record_model_check"):
			_, _ = w.Write([]byte(`{"model":"` + model + `","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`))
			return
		case strings.Contains(text, "status"):
			output = `{"status":"ok","value":7}`
		}
		_, _ = w.Write([]byte(`{"model":"` + model + `","output_text":"` + output + `","usage":` + usage + `}`))
	}))
	defer server.Close()
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	enforcement := &countingEnforcement{}
	runtime := &Runtime{
		Store:       store,
		OwnerID:     "gateway-universal",
		Tokenizer:   runtimeTestTokenizer{},
		ModelLimits: runtimeTestModelLimits{},
		Projector:   &QualityProjector{Store: store, Enforcement: enforcement},
		Now:         func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key", ConfigRevision: "cfg-1", DispatchRevision: 3, SourceConfigRevision: "src-1", SourceDispatchRevision: 4}, nil
		},
		ResolveComparison: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", CredentialType: "api_key", ConfigRevision: "cfg-2", DispatchRevision: 4, SourceConfigRevision: "src-2", SourceDispatchRevision: 5}, nil
		},
	}
	assertFormedQualityFailure := func(t *testing.T, request RunRequest) {
		t.Helper()
		result, err := runtime.Run(context.Background(), request)
		if err != nil || result.Status != string(RunCompleted) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		data, ok := result.Data.(map[string]any)
		if !ok {
			t.Fatalf("result data=%T", result.Data)
		}
		if formed, ok := data["evidenceFormed"].(bool); !ok || !formed {
			t.Fatalf("full run evidenceFormed=%v missing=%v", data["evidenceFormed"], data["missingFamilies"])
		}
		if trusted, ok := data["trustFormed"].(bool); !ok || !trusted {
			t.Fatalf("full run trustFormed=%v", data["trustFormed"])
		}
		if missing, ok := data["missingFamilies"].([]string); ok && len(missing) != 0 {
			t.Fatalf("full run missingFamilies=%v", missing)
		}
		if level := data["level"]; level != "suspicious" {
			t.Fatalf("long-context failure must stay score-driven suspicious: %v", level)
		}
	}
	// 非信任 full：通用 6 族形成 + 质量失败健康投影。
	assertFormedQualityFailure(t, RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ProviderCode: "openai", Threshold: 70, TriggerKind: "scheduled", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	// 信任 full：distribution_similarity/comparison 映射回通用族后同样形成。
	assertFormedQualityFailure(t, RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ProviderCode: "openai", Threshold: 70, TriggerKind: "scheduled", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TrustedComparison: true, TrustedComparisonAccountID: "acct-2", TrustedComparisonSystemAccountID: "sys", TrustedComparisonConfigRevision: "cfg-2", TrustedComparisonDispatchRevision: 4, TrustedComparisonSourceConfigRevision: "src-2", TrustedComparisonSourceDispatchRevision: 5})
	if enforcement.calls != 2 {
		t.Fatalf("formed quality failures must reach the enforcement adapter: calls=%d", enforcement.calls)
	}
	var facts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct'`).Scan(&facts); err != nil || facts != 1 {
		t.Fatalf("health facts=%d err=%v", facts, err)
	}
	var latest int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct' AND requested_model='gpt-5.6-sol'`).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("full trust latest rows=%d err=%v", latest, err)
	}
}

func TestRuntimeAllowsQuickTrustedComparison(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	var mu sync.Mutex
	requests := map[string]int{}
	targetServer := httptest.NewServer(&runtimeModelServer{t: t, expectedModel: "gpt-5.6-sol", mu: &mu, requests: requests})
	defer targetServer.Close()
	comparisonServer := httptest.NewServer(&runtimeModelServer{t: t, expectedModel: "gpt-5.6-terra", mu: &mu, requests: requests})
	defer comparisonServer.Close()
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-quick-trusted",
		Now:     func() time.Time { return time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: targetServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", ConfigRevision: "cfg-1", DispatchRevision: 3, SourceConfigRevision: "src-1", SourceDispatchRevision: 4}, nil
		},
		ResolveComparison: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: comparisonServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", ConfigRevision: "cfg-2", DispatchRevision: 4, SourceConfigRevision: "src-2", SourceDispatchRevision: 5}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TrustedComparison: true, TrustedComparisonAccountID: "comparison-acct", TrustedComparisonSystemAccountID: "comparison-sys", TrustedComparisonConfigRevision: "cfg-2", TrustedComparisonDispatchRevision: 4, TrustedComparisonSourceConfigRevision: "src-2", TrustedComparisonSourceDispatchRevision: 5})
	if err != nil || result.RunID == "" {
		t.Fatalf("quick trusted comparison must run: result=%+v err=%v", result, err)
	}
	mu.Lock()
	targetRequests, comparisonRequests := requests["gpt-5.6-sol"], requests["gpt-5.6-terra"]
	mu.Unlock()
	if targetRequests == 0 || comparisonRequests == 0 {
		t.Fatalf("quick trusted comparison must probe both accounts: target=%d comparison=%d", targetRequests, comparisonRequests)
	}
	var trustedComparisonEnabled, trustedComparisonAvailable int
	if err := store.db.QueryRow(`SELECT trusted_comparison_enabled,trusted_comparison_available FROM model_check_runs WHERE id=?`, result.RunID).Scan(&trustedComparisonEnabled, &trustedComparisonAvailable); err != nil || trustedComparisonEnabled != 1 || trustedComparisonAvailable != 1 {
		t.Fatalf("quick trusted comparison durable state enabled=%d available=%d err=%v", trustedComparisonEnabled, trustedComparisonAvailable, err)
	}
}

func TestRuntimeRejectsTargetChangeAfterClaim(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	var resolveCalls int
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-toctou",
		Resolve: func(context.Context, RunRequest) (Target, error) {
			resolveCalls++
			endpoint := "https://example.invalid/first"
			if resolveCalls > 1 {
				endpoint = "https://example.invalid/rotated"
			}
			return Target{Endpoint: endpoint, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", ConfigRevision: "cfg-1", DispatchRevision: 1, SourceConfigRevision: "src-1", SourceDispatchRevision: 1}, nil
		},
	}
	_, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err == nil || !strings.Contains(err.Error(), "changed after claim") {
		t.Fatalf("target changes after claim must be rejected, calls=%d err=%v", resolveCalls, err)
	}
	if resolveCalls != 2 {
		t.Fatalf("target must be resolved before and after claim, calls=%d", resolveCalls)
	}
}

func TestUndeclaredResponseModelMismatchOnlyUsesNonEmptyTargetEvidence(t *testing.T) {
	if hasUndeclaredResponseModelMismatch([]map[string]any{
		{"kind": "protocol_basic", "evidence": map[string]any{"modelMismatch": false, "responseModel": ""}},
		{"kind": "trusted_comparison.protocol_basic", "evidence": map[string]any{"modelMismatch": true, "responseModel": "other-model"}},
	}) {
		t.Fatal("missing target model and comparison mismatch must not trigger target hard gate")
	}
	if !hasUndeclaredResponseModelMismatch([]map[string]any{
		{"kind": "protocol_basic", "evidence": map[string]any{"success": true, "modelMismatch": true, "responseModel": "other-model"}},
	}) {
		t.Fatal("non-empty target response mismatch must trigger target hard gate")
	}
	if hasUndeclaredResponseModelMismatch([]map[string]any{
		{"kind": "protocol_basic", "evidence": map[string]any{"success": false, "modelMismatch": true, "responseModel": "other-model"}},
	}) {
		t.Fatal("failed request response model must not trigger target hard gate")
	}
	if hasResponseModelEvidence([]map[string]any{
		{"kind": "protocol_basic", "evidence": map[string]any{"success": false, "responseModel": "gpt-5.6"}},
	}) {
		t.Fatal("failed request response model must not count as model evidence")
	}
}

func TestEvaluationObservationStatusIsPartialForSkippedAndUnknown(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{name: "passed", input: "passed", want: "complete"},
		{name: "failed", input: "failed", want: "complete"},
		{name: "warning", input: "warning", want: "complete"},
		{name: "skipped", input: "skipped", want: "partial"},
		{name: "unknown", input: "", want: "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := evaluationObservationStatus(test.input); got != test.want {
				t.Fatalf("status=%q want %q", got, test.want)
			}
		})
	}
}

func TestAppendEvaluationObservationsPersistsFamilyRowsWithoutEvidencePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "family-observations.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,schedule_id,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,trace_id,created_at,updated_at) VALUES ('run-family','sys','actor','openai','account','acct','acct','gpt-5.6','full','manual',NULL,'running','unavailable',0,100,'','{}','{}','{}','{}','j3b-v1','2026-08-28T00:00:00Z',NULL,'2026-08-28T00:00:00Z','2026-08-28T00:00:00Z')`); err != nil {
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
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	evaluations := []modelcheckprobe.Evaluation{
		{Kind: "token_integrity", Status: "skipped", Evidence: map[string]any{"secret": "must-not-persist"}},
		{Kind: "behavior_probe", Status: "mystery", Evidence: map[string]any{"raw": "must-not-persist"}},
		{Kind: "stability", Status: "passed", Evidence: map[string]any{"response": "must-not-persist"}},
	}
	if err := appendEvaluationObservations(context.Background(), store, "run-family", "sys", "acct", "openai", "requested", "mapped", "mapped", "passed", "unknown", 2, evaluations, now); err != nil {
		t.Fatal(err)
	}
	if err := appendEvaluationObservations(context.Background(), store, "run-family", "sys", "acct", "openai", "requested", "mapped", "mapped", "passed", "unknown", 2, evaluations, now); err != nil {
		t.Fatalf("exact family observation replay must be idempotent: %v", err)
	}
	rows, err := store.db.Query(`SELECT id,probe_family,observation_status,evidence_coverage,created_at FROM model_check_observations WHERE run_id=? ORDER BY id`, "run-family")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make([][]string, 0, 3)
	for rows.Next() {
		var id, family, status, created string
		var coverage int
		if err := rows.Scan(&id, &family, &status, &coverage, &created); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(id, "must-not-persist") || strings.Contains(family, "must-not-persist") || coverage != 2 || created != now.Format(time.RFC3339Nano) {
			t.Fatalf("unexpected persisted row id=%q family=%q status=%q coverage=%d created=%q", id, family, status, coverage, created)
		}
		got = append(got, []string{family, status})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0][0] != "token_integrity" || got[0][1] != "partial" || got[1][0] != "behavior_probe" || got[1][1] != "partial" || got[2][0] != "stability" || got[2][1] != "complete" {
		t.Fatalf("family observations=%v", got)
	}
}

func TestRuntimeFailureCommitsOutcomeBeforeProjection(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-failure",
		Now:     func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: "https://target.invalid", Prompt: "hello", Protocol: modelcheckprofile.Protocol("unsupported"), DispatchRevision: 1}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err == nil || result.Status != string(RunFailed) {
		t.Fatalf("result=%+v err=%v, want durable failed result", result, err)
	}
	assertRuntimeTerminalDurability(t, store, result.RunID, RunFailed, now)
}

func TestRuntimeCancelCommitsCanceledOutcomeWithIndependentFinalizeContext(t *testing.T) {
	store := newRuntimeTestStore(t)
	defer store.Close()
	started := make(chan struct{})
	var once sync.Once
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	runtime := &Runtime{
		Store:   store,
		OwnerID: "gateway-cancel",
		Now:     func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: "https://target.invalid", Prompt: "hello", Protocol: modelcheckprofile.ProtocolOpenAIResponses, DispatchRevision: 1, Client: &http.Client{Transport: cancelRoundTripper{started: started, once: &once}}}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	result, err := runtime.Run(ctx, RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err == nil || !errors.Is(err, context.Canceled) || result.Status != string(RunCanceled) {
		t.Fatalf("result=%+v err=%v, want canceled durable result", result, err)
	}
	assertRuntimeTerminalDurability(t, store, result.RunID, RunCanceled, now)
}

type cancelRoundTripper struct {
	started chan struct{}
	once    *sync.Once
}

func (t cancelRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func newRuntimeTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-failure.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range runtimeTestDDL() {
		if _, err := seed.Exec(ddl); err != nil {
			_ = seed.Close()
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
	return store
}

func assertRuntimeTerminalDurability(t *testing.T, store *Store, runID string, status RunStatus, now time.Time) {
	t.Helper()
	var gotStatus, finished string
	if err := store.db.QueryRow(`SELECT status,finished_at FROM model_check_runs WHERE id=?`, runID).Scan(&gotStatus, &finished); err != nil {
		t.Fatal(err)
	}
	if gotStatus != string(status) || finished != now.Format(time.RFC3339Nano) {
		t.Fatalf("run status=%q finished=%q, want %q at %s", gotStatus, finished, status, now.Format(time.RFC3339Nano))
	}
	var outcomes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_outcomes WHERE input_id IN (SELECT input_id FROM model_check_inputs WHERE target_id='acct')`).Scan(&outcomes); err != nil {
		t.Fatal(err)
	}
	if outcomes != 1 {
		t.Fatalf("durable outcome count=%d, want 1", outcomes)
	}
}

type runtimeModelServer struct {
	t             *testing.T
	expectedModel string
	mu            *sync.Mutex
	requests      map[string]int
}

func (s *runtimeModelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("read request: %v", err)
		return
	}
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		s.t.Errorf("decode request model=%q err=%v", s.expectedModel, err)
	}
	s.mu.Lock()
	s.requests[s.expectedModel]++
	s.requests[s.expectedModel+"|"+request.Model]++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	response := `{"model":"` + s.expectedModel + `","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":10,"total_tokens":2}}`
	switch {
	case strings.Contains(string(body), "record_model_check"):
		response = `{"model":"` + s.expectedModel + `","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`
	case strings.Contains(string(body), "status"):
		response = `{"model":"` + s.expectedModel + `","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`
	}
	_, _ = w.Write([]byte(response))
}

type runtimeTestTokenizer struct{}

func (runtimeTestTokenizer) Version() string { return "runtime-test-tokenizer-v1" }
func (runtimeTestTokenizer) Count(value string) (int, error) {
	count := 1
	for index := 0; index+1 < len(value); index++ {
		if value[index] == ' ' && value[index+1] == 'x' {
			count++
		}
	}
	return count, nil
}

type runtimeTestModelLimits struct{}

func (runtimeTestModelLimits) Version() string { return "runtime-test-limits-v1" }
func (runtimeTestModelLimits) MaxInputTokens(string, string, modelcheckprofile.Protocol) (int, error) {
	return 12000, nil
}

func runtimeTestDDL() []string {
	return []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE TABLE model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, claim_until TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL)`,
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL, trigger_kind TEXT NOT NULL, schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, request_summary_json TEXT NOT NULL, result_summary_json TEXT NOT NULL, policy_snapshot_json TEXT NOT NULL, quality_decision_json TEXT NOT NULL, probe_set_version TEXT NOT NULL, started_at TEXT NOT NULL, trace_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, level TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, message TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, error_code TEXT, error_message TEXT, quality_health_sync_status TEXT)`,
		`CREATE TABLE model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, probe_family TEXT NOT NULL, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL, created_at TEXT NOT NULL, aggregation_completed_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL, score INTEGER NOT NULL, threshold INTEGER NOT NULL, level TEXT NOT NULL, error_code TEXT, error_message TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,stat_hour))`,
		`CREATE TABLE model_account_trust_results (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', mapping_status TEXT NOT NULL DEFAULT 'unknown', usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence', evidence_status TEXT NOT NULL DEFAULT 'insufficient', evidence_coverage INTEGER NOT NULL DEFAULT 0, observation_count INTEGER NOT NULL DEFAULT 0, reason_codes_json TEXT NOT NULL DEFAULT '[]', last_observed_id TEXT, last_observed_at TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE model_trust_latest_dirty_accounts (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, dirty_reason TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE model_trust_observation_receipts (observation_id TEXT PRIMARY KEY, observation_created_at TEXT NOT NULL, processed_at TEXT NOT NULL)`,
		`CREATE TABLE model_trust_aggregation_state (scope_key TEXT PRIMARY KEY, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, updated_at TEXT NOT NULL)`,
	}
}

var _ = json.Valid
var _ = strings.TrimSpace
