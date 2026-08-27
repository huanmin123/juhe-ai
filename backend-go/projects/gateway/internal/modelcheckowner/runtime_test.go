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
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

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
		return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello"}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) || result.RunID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("status=%s err=%v", status, err)
	}
	var requestSummary string
	if err := store.db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&requestSummary); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(requestSummary), &snapshot); err != nil || snapshot["configRevision"] != "cfg-1" || snapshot["policyRevision"] != "pol-1" {
		t.Fatalf("request snapshot=%s err=%v", requestSummary, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id=?`, result.RunID).Scan(&count); err != nil || count != 5 {
		t.Fatalf("item count=%d err=%v", count, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("observation count=%d err=%v", count, err)
	}
}

func TestRuntimeRejectsIncompleteTargetContract(t *testing.T) {
	store := &Store{}
	runtime := &Runtime{Store: store, Resolve: func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: "https://example.invalid", Prompt: "OK"}, nil
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
		return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra"}, nil
	}}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1"})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	if len(models) != 6 {
		mu.Unlock()
		t.Fatalf("probe calls=%d models=%v", len(models), models)
	}
	for _, model := range models {
		if model != "gpt-5.6-terra" {
			mu.Unlock()
			t.Fatalf("probe used model %q, want resolved upstream model", model)
		}
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
	var requested, mapped, mappingStatus string
	if err := store.db.QueryRow(`SELECT requested_model,mapped_upstream_model,mapping_status FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&requested, &mapped, &mappingStatus); err != nil {
		t.Fatal(err)
	}
	if requested != "gpt-5.6-sol" || mapped != "gpt-5.6-terra" || mappingStatus != "mapped" {
		t.Fatalf("observation requested=%q mapped=%q mappingStatus=%q", requested, mapped, mappingStatus)
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
		Store:   store,
		OwnerID: "gateway-1",
		Now:     func() time.Time { return now },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: targetServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", ProviderCode: "openai"}, nil
		},
		ResolveComparison: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: comparisonServer.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-terra", ProviderCode: "openai"}, nil
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TrustedComparison: true, TrustedComparisonAccountID: "comparison-acct", TrustedComparisonConfigRevision: "cfg-2"})
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
	if !ok || comparison["accountId"] != "comparison-acct" || comparison["configRevision"] != "cfg-2" || comparison["upstreamModel"] != "gpt-5.6-terra" || comparison["endpointFingerprint"] != endpointFingerprint(comparisonServer.URL) {
		t.Fatalf("frozen trusted comparison=%#v", frozen["trustedComparison"])
	}
	var observations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE run_id=?`, result.RunID).Scan(&observations); err != nil || observations != 2 {
		t.Fatalf("observations=%d err=%v", observations, err)
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
	response := `{"model":"` + s.expectedModel + `","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`
	switch {
	case strings.Contains(string(body), "record_model_check"):
		response = `{"model":"` + s.expectedModel + `","output":[{"type":"function_call","name":"record_model_check","arguments":"{\"code\":\"ok\",\"count\":1}"}],"usage":{"total_tokens":2}}`
	case strings.Contains(string(body), "status"):
		response = `{"model":"` + s.expectedModel + `","output_text":"{\"status\":\"ok\",\"value\":7}","usage":{"total_tokens":2}}`
	}
	_, _ = w.Write([]byte(response))
}

func runtimeTestDDL() []string {
	return []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE TABLE model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, claim_until TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL)`,
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, account_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL, trigger_kind TEXT NOT NULL, schedule_id TEXT, status TEXT NOT NULL, request_summary_json TEXT NOT NULL, result_summary_json TEXT NOT NULL, policy_snapshot_json TEXT NOT NULL, quality_decision_json TEXT NOT NULL, probe_set_version TEXT NOT NULL, started_at TEXT NOT NULL, trace_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, level TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, message TEXT NOT NULL, finished_at TEXT, quality_health_sync_status TEXT)`,
		`CREATE TABLE model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, probe_family TEXT NOT NULL, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL, score INTEGER NOT NULL, threshold INTEGER NOT NULL, level TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,stat_hour))`,
	}
}

var _ = json.Valid
var _ = strings.TrimSpace
