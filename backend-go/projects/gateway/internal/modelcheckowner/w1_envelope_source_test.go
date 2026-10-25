package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

func TestW1QuestionRunnerPreservesEnvelopeSource(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		profile        string
		credentialType string
	}{
		{name: "glm profile", provider: "glm", profile: "profile_glm_general_openai_v1", credentialType: "api_key"},
		{name: "google oauth credential", provider: "gemini", profile: "profile_gemini_openai_chat_v1beta", credentialType: "google_oauth"},
	}
	input := modelcheckquestionbank.TestQuestionInput{Model: "public-model"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := Target{
				ProviderCode:              test.provider,
				ProviderProtocolProfileID: test.profile,
				CredentialType:            test.credentialType,
				UpstreamModel:             "upstream-model",
				Protocol:                  modelcheckprofile.ProtocolOpenAIChat,
			}
			suite := (&questionTestRunner{}).suiteFor(target, input)
			if suite.ProviderProtocolProfileID != test.profile || suite.CredentialType != test.credentialType {
				t.Fatalf("suite envelope source profile=%q credential=%q; want %q/%q", suite.ProviderProtocolProfileID, suite.CredentialType, test.profile, test.credentialType)
			}
		})
	}
}

type w1OwnerEnvelopeRequest struct {
	url           string
	accept        string
	authorization string
}

type w1OwnerEnvelopeTransport struct {
	mu           sync.Mutex
	model        string
	responseText string
	requests     []w1OwnerEnvelopeRequest
}

func (transport *w1OwnerEnvelopeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, err := io.Copy(io.Discard, request.Body); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, w1OwnerEnvelopeRequest{
		url: request.URL.String(), accept: request.Header.Get("Accept"), authorization: request.Header.Get("Authorization"),
	})
	transport.mu.Unlock()

	responseText := transport.responseText
	if responseText == "" {
		responseText = "OK-MODEL-CHECK"
	}
	body := `{"model":"` + transport.model + `","choices":[{"message":{"content":"` + responseText + `"}}],"usage":{"total_tokens":2}}`
	contentType := "application/json"
	if request.Header.Get("Accept") == "text/event-stream" {
		body = "data: {\"model\":\"" + transport.model + "\",\"choices\":[{\"delta\":{\"content\":\"" + responseText + "\"}}]}\n\ndata: [DONE]\n\n"
		contentType = "text/event-stream"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func (transport *w1OwnerEnvelopeTransport) captured() []w1OwnerEnvelopeRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]w1OwnerEnvelopeRequest(nil), transport.requests...)
}

func TestW1QuestionRunnerUsesOAuthProfileAtTransport(t *testing.T) {
	transport := &w1OwnerEnvelopeTransport{model: "gemini-3.5-flash"}
	target := Target{
		Endpoint:                  "https://generativelanguage.googleapis.com/v1beta/openai",
		ProviderCode:              "gemini",
		ProviderProtocolProfileID: "profile_gemini_openai_chat_v1beta",
		CredentialType:            "google_oauth",
		UpstreamModel:             "gemini-3.5-flash",
		Protocol:                  modelcheckprofile.ProtocolOpenAIChat,
		EndpointMode:              modelcheckprofile.EndpointModeChatSSE,
		Headers:                   http.Header{"Authorization": []string{"Bearer synthetic-test-token"}},
		Client:                    &http.Client{Transport: transport},
	}
	resolver := &fakeQuestionTestResolver{target: target}
	_, err := NewQuestionTestRunner(resolver.Resolve, nil).TestQuestion(context.Background(), modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys", AccountID: "acct", Model: "gemini-3.5-flash", QuestionText: "question", ReferenceAnswer: "answer",
	})
	if err != nil {
		t.Fatalf("question diagnostic: %v", err)
	}
	requests := transport.captured()
	if len(requests) != 2 {
		t.Fatalf("question diagnostic requests=%+v; want two isolated hops", requests)
	}
	for _, request := range requests {
		if request.url != "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" || request.accept != "text/event-stream" || request.authorization != "Bearer synthetic-test-token" {
			t.Fatalf("question diagnostic envelope=%+v", request)
		}
	}
}

func TestW1QuestionRunnerReturnsSuiteModeError(t *testing.T) {
	target := Target{Endpoint: "https://target.invalid", UpstreamModel: "model", Protocol: modelcheckprofile.ProtocolOpenAIChat, EndpointMode: "responses_json"}
	resolver := &fakeQuestionTestResolver{target: target}
	_, err := NewQuestionTestRunner(resolver.Resolve, nil).TestQuestion(context.Background(), modelcheckquestionbank.TestQuestionInput{
		SystemAccountID: "sys", AccountID: "acct", Model: "model", QuestionText: "question", ReferenceAnswer: "answer",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match protocol") {
		t.Fatalf("invalid suite mode error=%v", err)
	}
}

func TestW1RuntimeResolvedEnvelopeReachesTargetAndComparison(t *testing.T) {
	tests := []struct {
		name              string
		provider          string
		profile           string
		credentialType    string
		model             string
		baseURL           string
		comparisonBaseURL string
		mode              string
		wantURL           string
		wantComparisonURL string
		wantAccept        string
	}{
		{
			name:     "GLM official version root",
			provider: "glm", profile: "profile_glm_general_openai_v1", credentialType: "api_key",
			model: "glm-5.2", baseURL: "https://open.bigmodel.cn/api/paas/v4", comparisonBaseURL: "https://comparison.invalid/api/paas/v4",
			mode:    modelcheckprofile.EndpointModeChatJSON,
			wantURL: "https://open.bigmodel.cn/api/paas/v4/chat/completions", wantComparisonURL: "https://comparison.invalid/api/paas/v4/chat/completions", wantAccept: "application/json",
		},
		{
			name:     "Gemini OpenAI OAuth SSE",
			provider: "gemini", profile: "profile_gemini_openai_chat_v1beta", credentialType: "google_oauth",
			model: "gemini-3.5-flash", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai", comparisonBaseURL: "https://comparison.invalid/v1beta/openai",
			mode:    modelcheckprofile.EndpointModeChatSSE,
			wantURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", wantComparisonURL: "https://comparison.invalid/v1beta/openai/chat/completions", wantAccept: "text/event-stream",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetTransport := &w1OwnerEnvelopeTransport{model: test.model}
			comparisonTransport := &w1OwnerEnvelopeTransport{model: test.model}
			target := w1RuntimeEnvelopeTarget(test, test.baseURL, 1, "target")
			target.Client = &http.Client{Transport: targetTransport}
			comparison := w1RuntimeEnvelopeTarget(test, test.comparisonBaseURL, 2, "comparison")
			comparison.Client = &http.Client{Transport: comparisonTransport}

			store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
			runtime := &Runtime{
				Store:             store,
				OwnerID:           "w1-envelope-source",
				Now:               func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
				Resolve:           func(context.Context, RunRequest) (Target, error) { return target, nil },
				ResolveComparison: func(context.Context, RunRequest) (Target, error) { return comparison, nil },
			}
			request := RunRequest{
				SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "target-account",
				Model: test.model, Profile: "quick", ConfigRevision: target.ConfigRevision, SourceConfigRevision: target.SourceConfigRevision,
				DispatchRevision: target.DispatchRevision, SourceDispatchRevision: target.SourceDispatchRevision, PolicyRevision: "policy-1",
				TrustedComparison: true, TrustedComparisonAccountID: "comparison-account", TrustedComparisonSystemAccountID: "sys",
				TrustedComparisonConfigRevision: comparison.ConfigRevision, TrustedComparisonDispatchRevision: comparison.DispatchRevision,
				TrustedComparisonSourceConfigRevision: comparison.SourceConfigRevision, TrustedComparisonSourceDispatchRevision: comparison.SourceDispatchRevision,
			}
			var startedRunID string
			request.OnStarted = func(runID string) { startedRunID = runID }
			result, err := runtime.Run(context.Background(), request)
			if err != nil || result.Status != string(RunCompleted) {
				t.Fatalf("runtime result=%+v err=%v", result, err)
			}
			if startedRunID == "" || startedRunID != result.RunID {
				t.Fatalf("OnStarted runID=%q result runID=%q", startedRunID, result.RunID)
			}
			for _, hop := range []struct {
				name      string
				transport *w1OwnerEnvelopeTransport
				wantURL   string
			}{{"target", targetTransport, test.wantURL}, {"trusted comparison", comparisonTransport, test.wantComparisonURL}} {
				requests := hop.transport.captured()
				if len(requests) == 0 {
					t.Fatalf("%s emitted no captured requests", hop.name)
				}
				found := false
				for _, captured := range requests {
					if captured.url == hop.wantURL && captured.accept == test.wantAccept {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s requests=%+v; want URL=%q Accept=%q", hop.name, requests, hop.wantURL, test.wantAccept)
				}
			}
		})
	}
}

func w1RuntimeEnvelopeTarget(test struct {
	name              string
	provider          string
	profile           string
	credentialType    string
	model             string
	baseURL           string
	comparisonBaseURL string
	mode              string
	wantURL           string
	wantComparisonURL string
	wantAccept        string
}, endpoint string, dispatchRevision int64, revisionPrefix string) Target {
	return Target{
		Endpoint: endpoint, ProviderCode: test.provider, ProviderProtocolProfileID: test.profile, CredentialType: test.credentialType,
		ConfigRevision: revisionPrefix + "-config", SourceConfigRevision: revisionPrefix + "-source-config",
		DispatchRevision: dispatchRevision, SourceDispatchRevision: dispatchRevision,
		UpstreamModel: test.model, Protocol: modelcheckprofile.ProtocolOpenAIChat, EndpointMode: test.mode,
		Prompt: "Reply with exactly: OK-MODEL-CHECK", Headers: http.Header{"Authorization": []string{"Bearer synthetic-test-token"}},
	}
}

func TestW1RuntimeRejectsIncompleteRequestContracts(t *testing.T) {
	validTarget := Target{Endpoint: "https://target.invalid", Prompt: "hello", DispatchRevision: 1}
	resolve := func(context.Context, RunRequest) (Target, error) { return validTarget, nil }
	runtime := &Runtime{Store: &Store{}, Resolve: resolve}
	valid := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "model", Profile: "quick"}

	if _, err := (*Runtime)(nil).Run(context.Background(), valid); err == nil || err.Error() != "J3b Gateway runtime is not initialized" {
		t.Fatalf("nil runtime error=%v", err)
	}
	if _, err := (&Runtime{Resolve: resolve}).Run(context.Background(), valid); err == nil || err.Error() != "J3b Gateway runtime is not initialized" {
		t.Fatalf("missing store error=%v", err)
	}
	if _, err := (&Runtime{Store: &Store{}}).Run(context.Background(), valid); err == nil || err.Error() != "J3b Gateway runtime is not initialized" {
		t.Fatalf("missing resolver error=%v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*RunRequest)
		want string
	}{
		{name: "scope", edit: func(request *RunRequest) { request.SystemAccountID = "" }, want: "J3b runtime scope is incomplete"},
		{name: "actor scope", edit: func(request *RunRequest) { request.ActorSystemAccountID = "" }, want: "J3b runtime scope is incomplete"},
		{name: "target type", edit: func(request *RunRequest) { request.TargetType = "group" }, want: "J3b runtime request is incomplete"},
		{name: "target id", edit: func(request *RunRequest) { request.TargetID = "" }, want: "J3b runtime request is incomplete"},
		{name: "model", edit: func(request *RunRequest) { request.Model = "" }, want: "J3b runtime request is incomplete"},
		{name: "profile", edit: func(request *RunRequest) { request.Profile = "unknown" }, want: "J3b runtime profile is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.edit(&request)
			if _, err := runtime.Run(context.Background(), request); err == nil || err.Error() != test.want {
				t.Fatalf("error=%v; want %q", err, test.want)
			}
		})
	}

	for _, test := range []struct {
		name   string
		target Target
		edit   func(*RunRequest)
		want   string
	}{
		{name: "missing endpoint", target: Target{Prompt: "hello", DispatchRevision: 1}, want: "resolved J3b target is incomplete"},
		{name: "missing prompt", target: Target{Endpoint: "https://target.invalid", DispatchRevision: 1}, want: "resolved J3b target is incomplete"},
		{name: "invalid dispatch revision", target: Target{Endpoint: "https://target.invalid", Prompt: "hello"}, want: "resolved J3b target dispatch revision is invalid"},
		{name: "stale dispatch revision", target: validTarget, edit: func(request *RunRequest) { request.DispatchRevision = 2 }, want: "resolved J3b target dispatch revision is stale"},
		{name: "incomplete comparison", target: validTarget, edit: func(request *RunRequest) { request.TrustedComparison = true }, want: "resolved J3b trusted comparison contract is incomplete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := test.target
			caseRuntime := &Runtime{Store: &Store{}, Resolve: func(context.Context, RunRequest) (Target, error) { return current, nil }}
			request := valid
			if test.edit != nil {
				test.edit(&request)
			}
			if _, err := caseRuntime.Run(context.Background(), request); err == nil || err.Error() != test.want {
				t.Fatalf("error=%v; want %q", err, test.want)
			}
		})
	}

	t.Run("trusted comparison admission", func(t *testing.T) {
		target := Target{
			Endpoint: "https://target.invalid", Prompt: "hello", ProviderCode: "openai",
			ProviderProtocolProfileID: "profile_openai_openai_v1", Protocol: modelcheckprofile.ProtocolOpenAIResponses,
			ConfigRevision: "target-config", SourceConfigRevision: "target-source-config", DispatchRevision: 1, SourceDispatchRevision: 1,
			UpstreamModel: "gpt-5.6-sol",
		}
		validComparison := target
		validComparison.Endpoint = "https://comparison.invalid"
		validComparison.ConfigRevision = "comparison-config"
		validComparison.SourceConfigRevision = "comparison-source-config"
		validComparison.DispatchRevision = 2
		validComparison.SourceDispatchRevision = 2
		validRequest := RunRequest{
			SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct",
			Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: target.ConfigRevision, PolicyRevision: "policy",
			TrustedComparison: true, TrustedComparisonAccountID: "comparison-acct", TrustedComparisonSystemAccountID: "sys",
			TrustedComparisonConfigRevision: validComparison.ConfigRevision, TrustedComparisonDispatchRevision: validComparison.DispatchRevision,
			TrustedComparisonSourceConfigRevision: validComparison.SourceConfigRevision, TrustedComparisonSourceDispatchRevision: validComparison.SourceDispatchRevision,
		}
		for _, test := range []struct {
			name   string
			mutate func(*Target, *RunRequest)
			want   string
		}{
			{name: "missing endpoint", mutate: func(comparison *Target, _ *RunRequest) { comparison.Endpoint = "" }, want: "resolved J3b trusted comparison target is incomplete"},
			{name: "missing prompt", mutate: func(comparison *Target, _ *RunRequest) { comparison.Prompt = "" }, want: "resolved J3b trusted comparison target is incomplete"},
			{name: "missing upstream model", mutate: func(comparison *Target, _ *RunRequest) { comparison.UpstreamModel = "" }, want: "resolved J3b trusted comparison target is incomplete"},
			{name: "invalid dispatch revision", mutate: func(comparison *Target, _ *RunRequest) { comparison.DispatchRevision = 0 }, want: "resolved J3b trusted comparison target is incomplete"},
			{name: "incompatible provider", mutate: func(comparison *Target, _ *RunRequest) { comparison.ProviderCode = "gemini" }, want: "resolved J3b trusted comparison provider is incompatible"},
			{name: "incompatible protocol", mutate: func(comparison *Target, _ *RunRequest) { comparison.Protocol = modelcheckprofile.ProtocolOpenAIChat }, want: "resolved J3b trusted comparison protocol is incompatible"},
			{name: "incompatible profile", mutate: func(comparison *Target, _ *RunRequest) { comparison.ProviderProtocolProfileID = "profile_other" }, want: "resolved J3b trusted comparison provider protocol profile is incompatible"},
			{name: "stale config revision", mutate: func(comparison *Target, _ *RunRequest) { comparison.ConfigRevision = "stale" }, want: "resolved J3b trusted comparison config revision is stale"},
			{name: "stale dispatch revision", mutate: func(comparison *Target, _ *RunRequest) { comparison.DispatchRevision++ }, want: "resolved J3b trusted comparison dispatch revision is stale"},
			{name: "stale source config revision", mutate: func(comparison *Target, _ *RunRequest) { comparison.SourceConfigRevision = "stale" }, want: "resolved J3b trusted comparison source account config revision is stale"},
			{name: "stale source dispatch revision", mutate: func(comparison *Target, _ *RunRequest) { comparison.SourceDispatchRevision++ }, want: "resolved J3b trusted comparison source account dispatch revision is stale"},
		} {
			t.Run(test.name, func(t *testing.T) {
				comparison := validComparison
				request := validRequest
				test.mutate(&comparison, &request)
				caseRuntime := &Runtime{
					Store:             &Store{},
					Resolve:           func(context.Context, RunRequest) (Target, error) { return target, nil },
					ResolveComparison: func(context.Context, RunRequest) (Target, error) { return comparison, nil },
				}
				if _, err := caseRuntime.Run(context.Background(), request); err == nil || err.Error() != test.want {
					t.Fatalf("error=%v; want %q", err, test.want)
				}
			})
		}
	})
}

type w1FailingQuestionBankReader struct {
	cancel     context.CancelFunc
	beforeFail func() error
	err        error
}

func (reader w1FailingQuestionBankReader) ListByIDs(context.Context, []string, string) ([]modelcheckquestionbank.Question, error) {
	if reader.cancel != nil {
		reader.cancel()
	}
	if reader.beforeFail != nil {
		if err := reader.beforeFail(); err != nil {
			return nil, err
		}
	}
	return nil, reader.err
}

func TestW1RuntimeQuestionBankFailureFinalizesRunState(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancelRun  bool
		wantStatus string
		wantError  error
	}{
		{name: "failed", wantStatus: string(RunFailed), wantError: errors.New("question bank unavailable")},
		{name: "canceled", cancelRun: true, wantStatus: string(RunCanceled), wantError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := w1FailingQuestionBankReader{err: test.wantError}
			if test.cancelRun {
				reader.cancel = cancel
			}
			store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
			runtime := &Runtime{
				Store: store, OwnerID: "w1-envelope-failure",
				Now: func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
				Resolve: func(context.Context, RunRequest) (Target, error) {
					return Target{Endpoint: "https://unused.invalid", Prompt: "hello", DispatchRevision: 1}, nil
				},
				QuestionBank: reader,
			}
			request := RunRequest{
				SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct",
				Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "policy-1",
				CustomQuestionIds: []string{"question-1"},
			}
			result, err := runtime.Run(ctx, request)
			if err == nil || !errors.Is(err, test.wantError) {
				t.Fatalf("runtime error=%v; want error matching %v", err, test.wantError)
			}
			if result.Status != test.wantStatus || result.RunID == "" {
				t.Fatalf("runtime result=%+v; want status=%q and durable run id", result, test.wantStatus)
			}
			var status, errorCode, errorMessage string
			if err := store.db.QueryRow(`SELECT status,error_code,error_message FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status, &errorCode, &errorMessage); err != nil {
				t.Fatal(err)
			}
			if status != test.wantStatus || errorCode != "model_check_execution_failed" {
				t.Fatalf("persisted run status=%q errorCode=%q; want status=%q execution failure", status, errorCode, test.wantStatus)
			}
			if test.cancelRun && errorMessage != "J3b model check canceled without quality evidence" {
				t.Fatalf("canceled run message=%q", errorMessage)
			}
			var itemType, itemStatus string
			if err := store.db.QueryRow(`SELECT item_type,status FROM model_check_items WHERE run_id=?`, result.RunID).Scan(&itemType, &itemStatus); err != nil {
				t.Fatal(err)
			}
			if itemType != "execution" || itemStatus != string(ItemFailed) {
				t.Fatalf("failure item type/status=%q/%q", itemType, itemStatus)
			}
		})
	}
}

func TestW1RuntimeCancellationBeforeOutcomeFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &w1OwnerEnvelopeTransport{model: "gpt-5.6-sol"}
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	itemCompleted := false
	runtime := &Runtime{
		Store:   store,
		OwnerID: "w1-envelope-cancel",
		Now:     func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{
				Endpoint: "https://unused.invalid", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1",
				CredentialType: "api_key", UpstreamModel: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIChat,
				EndpointMode: modelcheckprofile.EndpointModeChatJSON, Prompt: "Reply with exactly: OK-MODEL-CHECK", DispatchRevision: 1,
				Client: &http.Client{Transport: transport}, Headers: http.Header{"Authorization": []string{"Bearer synthetic-test-token"}},
			}, nil
		},
		OnEvent: func(event ProgressEvent) {
			if event.Kind == "item_completed" && !itemCompleted {
				itemCompleted = true
				cancel()
			}
		},
	}
	result, err := runtime.Run(ctx, RunRequest{
		SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct",
		Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "policy-1",
	})
	if !errors.Is(err, context.Canceled) || result.Status != string(RunCanceled) || !itemCompleted {
		t.Fatalf("result=%+v err=%v itemCompleted=%v", result, err, itemCompleted)
	}
	if len(transport.captured()) == 0 {
		t.Fatal("runtime canceled before producing any upstream request")
	}
	var status, errorMessage string
	if err := store.db.QueryRow(`SELECT status,error_message FROM model_check_runs WHERE id=?`, result.RunID).Scan(&status, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != string(RunCanceled) || errorMessage != "J3b model check canceled without quality evidence" {
		t.Fatalf("persisted cancellation status/message=%q/%q", status, errorMessage)
	}
}

func TestW1RuntimeFailureFinalizationStorageErrors(t *testing.T) {
	for _, test := range []struct {
		name          string
		dropTable     string
		wantError     string
		wantCommitted int
	}{
		{name: "commit outcome", dropTable: "model_check_outcomes", wantError: "model_check_outcomes"},
		{name: "project outcome", dropTable: "model_check_items", wantError: "model_check_items", wantCommitted: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
			reader := w1FailingQuestionBankReader{
				err: errors.New("question bank unavailable"),
				beforeFail: func() error {
					_, err := store.db.Exec(`DROP TABLE ` + test.dropTable)
					return err
				},
			}
			runtime := &Runtime{
				Store: store, OwnerID: "w1-envelope-finalize-failure",
				Now: func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
				Resolve: func(context.Context, RunRequest) (Target, error) {
					return Target{Endpoint: "https://unused.invalid", Prompt: "hello", DispatchRevision: 1}, nil
				},
				QuestionBank: reader,
			}
			result, err := runtime.Run(context.Background(), RunRequest{
				SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct",
				Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "policy-1",
				CustomQuestionIds: []string{"question-1"},
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runtime error=%v; want error containing %q", err, test.wantError)
			}
			if result.RunID != "" || result.Status != "" {
				t.Fatalf("failed finalization must not report a finalized run: %+v", result)
			}
			var status string
			if err := store.db.QueryRow(`SELECT status FROM model_check_runs`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != string(RunRunning) {
				t.Fatalf("failed finalization run state=%q; want running", status)
			}
			if test.wantCommitted > 0 {
				var committed int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_outcomes WHERE committed=1`).Scan(&committed); err != nil {
					t.Fatal(err)
				}
				if committed != test.wantCommitted {
					t.Fatalf("committed outcomes=%d; want %d", committed, test.wantCommitted)
				}
			}
		})
	}
}

func TestW1RuntimeHealthProjectionFailureRemainsRetryable(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	transport := &w1OwnerEnvelopeTransport{model: "gpt-5.6-sol", responseText: "WRONG-CONTENT"}
	var events []ProgressEvent
	store.HealthStatHour = mustHealthStatHourFunc(t, "UTC")
	runtime := &Runtime{
		Store: store, OwnerID: "w1-envelope-health-projection",
		Projector: &QualityProjector{Store: store},
		Now:       func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{
				Endpoint: "https://unused.invalid", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1",
				CredentialType: "api_key", UpstreamModel: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIChat,
				EndpointMode: modelcheckprofile.EndpointModeChatJSON, Prompt: "Reply with exactly: OK-MODEL-CHECK", DispatchRevision: 1,
				Client: &http.Client{Transport: transport}, Headers: http.Header{"Authorization": []string{"Bearer synthetic-test-token"}},
			}, nil
		},
		OnEvent: func(event ProgressEvent) {
			events = append(events, event)
			if event.Kind == "run_started" {
				if _, err := store.db.Exec(`DROP TABLE account_quality_health_hourly`); err != nil {
					t.Errorf("drop health fact table: %v", err)
				}
			}
		},
	}
	result, err := runtime.Run(context.Background(), RunRequest{
		SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct",
		Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "policy-1", ProviderCode: "openai",
		Threshold: 70,
	})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("health projection failure changed durable run result: result=%+v err=%v", result, err)
	}
	if len(transport.captured()) == 0 {
		t.Fatal("runtime did not reach mocked upstream before health projection")
	}
	var syncStatus string
	if err := store.db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id=?`, result.RunID).Scan(&syncStatus); err != nil || syncStatus != "failed" {
		t.Fatalf("health sync status=%q err=%v; want retryable failed", syncStatus, err)
	}
	foundFailureEvent := false
	for _, event := range events {
		if event.Kind != "health_sync_failed" {
			continue
		}
		data, _ := event.Data.(map[string]any)
		message, _ := data["message"].(string)
		if strings.Contains(message, "account_quality_health_hourly") {
			foundFailureEvent = true
		}
	}
	if !foundFailureEvent {
		t.Fatalf("health_sync_failed must retain the original projection error, events=%+v", events)
	}
}

func TestW1OwnerLoadConfigGetterBoundaries(t *testing.T) {
	get := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	// 2026-10-24 存储形态收口：恒 postgres，JUHE_AI_J3B_STORE / sqlite 路径 /
	// JUHE_AI_SQLITE_READ_POOL 读取随生产删除；边界只剩 owner / 容量 / 保留
	// 时长 / 连接串回退与 Redis namespace 必填。
	base := map[string]string{"JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b"}
	if cfg, err := LoadConfig(get(base)); err != nil || cfg.StoreMode != "postgres" {
		t.Fatalf("default config=%+v err=%v", cfg, err)
	}
	for _, tc := range []struct {
		name string
		key  string
		val  string
	}{
		{"bad owner", "JUHE_AI_J3B_OWNER", "jobs"},
		{"bad capacity", "JUHE_AI_J3B_CIRCUIT_RUNTIME_CAPACITY", "0"},
		{"bad retention", "JUHE_AI_J3B_CIRCUIT_RUNTIME_RETENTION", "0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			values[tc.key] = tc.val
			if _, err := LoadConfig(get(values)); err == nil {
				t.Fatalf("LoadConfig(%s=%q) unexpectedly succeeded", tc.key, tc.val)
			}
		})
	}
	if _, err := LoadConfig(get(map[string]string{})); err == nil || !strings.Contains(err.Error(), "JUHE_AI_J3B_POSTGRES_URL") {
		t.Fatalf("missing postgres URL err=%v", err)
	}
	redis := map[string]string{"JUHE_AI_J3B_CIRCUIT_REDIS_URL": "redis://state", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b"}
	if _, err := LoadConfig(get(redis)); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("missing redis namespace err=%v", err)
	}
}
func TestW1OwnerStoreMergeAndRunScoreFailureContracts(t *testing.T) {
	ctx := context.Background()
	if err := (*Store)(nil).MergeRunQualityDecision(ctx, "run", map[string]any{"result": "x"}); err == nil {
		t.Fatal("nil store merge must fail")
	}
	store, fp := w14mRuntimeDB(t)
	fp.disarm()
	wbSeedFailedHealthRun(t, store, "run-merge-edges", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{"trustReason":"kept"}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
	if err := store.MergeRunQualityDecision(ctx, "", map[string]any{"result": "x"}); err == nil {
		t.Fatal("blank merge identity must fail")
	}
	fp.failBeginsFrom = 1
	if err := store.MergeRunQualityDecision(ctx, "run-merge-edges", map[string]any{"result": "x"}); err == nil || !strings.Contains(err.Error(), "begin J3b quality decision merge") {
		t.Fatalf("begin merge error=%v", err)
	}
	fp.disarm()
	fp.arm("SELECT quality_decision_json")
	if err := store.MergeRunQualityDecision(ctx, "run-merge-edges", map[string]any{"result": "x"}); err == nil || !strings.Contains(err.Error(), "read J3b quality decision for merge") {
		t.Fatalf("read merge error=%v", err)
	}
	fp.disarm()
	if _, err := store.db.Exec(`UPDATE model_check_runs SET quality_decision_json='{' WHERE id='run-merge-edges'`); err != nil {
		t.Fatal(err)
	}
	if err := store.MergeRunQualityDecision(ctx, "run-merge-edges", map[string]any{"result": "x"}); err == nil || !strings.Contains(err.Error(), "parse J3b quality decision for merge") {
		t.Fatalf("invalid JSON merge error=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_runs SET quality_decision_json='{"trustReason":"kept"}' WHERE id='run-merge-edges'`); err != nil {
		t.Fatal(err)
	}
	if err := store.MergeRunQualityDecision(ctx, "run-merge-edges", map[string]any{"bad": make(chan int)}); err == nil || !strings.Contains(err.Error(), "encode J3b quality decision merge") {
		t.Fatalf("marshal merge error=%v", err)
	}
	fp.arm("UPDATE model_check_runs SET quality_decision_json")
	if err := store.MergeRunQualityDecision(ctx, "run-merge-edges", map[string]any{"result": "x"}); err == nil || !strings.Contains(err.Error(), "merge J3b quality decision") {
		t.Fatalf("update merge error=%v", err)
	}
	fp.disarm()
	var merged string
	if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id='run-merge-edges'`).Scan(&merged); err != nil || !strings.Contains(merged, `"trustReason":"kept"`) {
		t.Fatalf("failed merge changed bytes=%q err=%v", merged, err)
	}
	if _, err := store.RunScoresByIDs(ctx, []string{"", "  "}); err != nil {
		t.Fatalf("blank score ids err=%v", err)
	}
	wbSeedFailedHealthRun(t, store, "run-score-a", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
	wbSeedFailedHealthRun(t, store, "run-score-b", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
	scores, err := store.RunScoresByIDs(ctx, []string{"run-score-a", "run-score-a", "", "run-score-b", "missing"})
	if err != nil || len(scores) != 2 || scores["run-score-a"] != 20 || scores["run-score-b"] != 20 {
		t.Fatalf("deduplicated scores=%v err=%v", scores, err)
	}
	fp.disarm()
	fp.armNextErr("SELECT id,score FROM model_check_runs")
	if _, err := store.RunScoresByIDs(ctx, []string{"run-score-a"}); err == nil || !strings.Contains(err.Error(), "iterate J3b run scores") {
		t.Fatalf("score rows error=%v", err)
	}
	fp.disarm()
	if _, err := (*Store)(nil).RunScoresByIDs(ctx, []string{"run"}); err == nil {
		t.Fatal("nil store score lookup must fail")
	}
	fp.arm("SELECT id,score FROM model_check_runs")
	if _, err := store.RunScoresByIDs(ctx, []string{"run-a"}); err == nil || !strings.Contains(err.Error(), "read J3b run scores") {
		t.Fatalf("score query error=%v", err)
	}
	fp.disarm()
	fp.armScan("SELECT id,score FROM model_check_runs")
	if _, err := store.RunScoresByIDs(ctx, []string{"run-a"}); err == nil || !strings.Contains(err.Error(), "scan J3b run score") {
		t.Fatalf("score scan error=%v", err)
	}
	fp.disarm()
	if err := store.MergeRunQualityDecision(ctx, "missing", map[string]any{"result": "ignored"}); err != nil {
		t.Fatalf("missing merge should be idempotent: %v", err)
	}
}

func TestW1OwnerQuestionScheduleConfigBoundaries(t *testing.T) {
	manager, db, verifier := qbQualityFixture(t)
	ctx := context.Background()
	ids := []string{"", "q1", "q1"}
	if _, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, CustomQuestionIds: &ids}); err == nil {
		t.Fatal("blank question id must be rejected")
	}
	ids = []string{"q1", " q1 ", "q1"}
	policy, err := manager.PatchPolicy(ctx, "sys", QualityPolicyPatch{ExpectedRevision: 0, CustomQuestionIds: &ids})
	if err != nil || len(policy.CustomQuestionIds) != 1 || policy.CustomQuestionIds[0] != "q1" {
		t.Fatalf("normalized policy=%+v err=%v", policy, err)
	}
	verifier.approved["q2"] = true
	schedule, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10, CustomQuestionIds: []string{"q1"}})
	if err != nil {
		t.Fatal(err)
	}
	model := "gpt-5.6-terra"
	patched, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: schedule.Revision, Model: &model, CustomQuestionIds: qbPtrIds([]string{"q2"})})
	if err != nil || patched.Revision != schedule.Revision+1 || patched.Model != model || len(patched.CustomQuestionIds) != 1 || patched.CustomQuestionIds[0] != "q2" {
		t.Fatalf("patched schedule=%+v err=%v", patched, err)
	}
	verifier.approved["missing"] = false
	bad := []string{"missing"}
	if _, err := manager.PatchSchedule(ctx, "sys", schedule.ID, QualitySchedulePatch{ExpectedRevision: patched.Revision, CustomQuestionIds: &bad}); err == nil {
		t.Fatal("unapproved schedule question must fail")
	}
	var rev int
	if err := db.QueryRow(`SELECT revision FROM model_quality_schedules WHERE id=?`, schedule.ID).Scan(&rev); err != nil || rev != patched.Revision {
		t.Fatalf("failed patch changed revision=%d err=%v", rev, err)
	}
}

type w1OwnerScoreReader struct {
	scores map[string]int
	err    error
}

func (reader w1OwnerScoreReader) RunScoresByIDs(context.Context, []string) (map[string]int, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.scores, nil
}

func TestW1OwnerListSchedulesPagingAndScoreReaderErrors(t *testing.T) {
	manager, db, _ := qbQualityFixture(t)
	ctx := context.Background()
	for _, account := range []string{"acct", "acct-2"} {
		if account == "acct-2" {
			w11eExec(t, db, `INSERT INTO accounts (id,system_account_id,name,vendor_code,connection_driver_code,health_check_connection_id,status) VALUES ('acct-2','sys','Second account','gpt','gpt','conn-acct-2','active')`)
			w11eExec(t, db, `INSERT INTO account_connections (id,account_id,connection_preset_code,base_url,enabled) VALUES ('conn-acct-2','acct-2','gpt_openai_v1','https://quality.invalid/v1',1)`)
		}
		if _, err := manager.CreateSchedule(ctx, "sys", QualityScheduleInput{AccountID: account, Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE model_quality_schedules SET last_run_id='run-1' WHERE account_id='acct'`); err != nil {
		t.Fatal(err)
	}
	manager.SetRunScoreReader(w1OwnerScoreReader{scores: map[string]int{"run-1": 88}})
	page, err := manager.ListSchedules(ctx, "sys", 1, 1)
	if err != nil || page.Total != 2 || !page.HasMore || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	all, err := manager.ListSchedules(ctx, "sys", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundScore := false
	for _, item := range all.Items {
		if item.LastRunID != nil && *item.LastRunID == "run-1" {
			foundScore = item.LastRunScore != nil && *item.LastRunScore == 88
		}
	}
	if !foundScore {
		t.Fatalf("run-1 score was not attached to its schedule: %+v", all.Items)
	}
	manager.SetRunScoreReader(w1OwnerScoreReader{err: errors.New("score reader unavailable")})
	if _, err := manager.ListSchedules(ctx, "sys", 1, 10); err == nil || !strings.Contains(err.Error(), "score reader unavailable") {
		t.Fatalf("score reader error=%v", err)
	}
}

func TestW1OwnerHealthProjectorFailureAndIdempotencyContracts(t *testing.T) {
	ctx := context.Background()
	t.Run("nil projector", func(t *testing.T) {
		if err := (*QualityProjector)(nil).Project(ctx, "run", EvidenceAggregate{}, HealthFact{}); err == nil {
			t.Fatal("nil projector must fail")
		}
	})
	t.Run("invalid enforcement action", func(t *testing.T) {
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		wbSeedFailedHealthRun(t, store, "run-health-invalid", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
		fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", StatHour: "2026-09-05T10", RunID: "run-health-invalid", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "failure", PenaltyAction: "invalid-action", EnforcementAllowed: true}
		projector := &QualityProjector{Store: store, Enforcement: EnforcementApplierFunc(func(context.Context, QualityEnforcement) (EnforcementOutcome, error) {
			t.Fatal("invalid action must be rejected before enforcement")
			return EnforcementOutcome{}, nil
		})}
		if err := projector.Project(ctx, fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "enforcement action is invalid") {
			t.Fatalf("invalid action err=%v", err)
		}
	})
	t.Run("already effective", func(t *testing.T) {
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		wbSeedFailedHealthRun(t, store, "run-health-effective", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
		fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", StatHour: "2026-09-05T10", RunID: "run-health-effective", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "failure", PenaltyAction: "quality_isolate", EnforcementAllowed: true}
		projector := &QualityProjector{Store: store, Enforcement: EnforcementApplierFunc(func(context.Context, QualityEnforcement) (EnforcementOutcome, error) {
			return EnforcementOutcome{AlreadyEffective: true, EnforcementID: "enf-existing", Generation: 2, BeforeStatus: "quality_isolated", AfterStatus: "quality_isolated"}, nil
		})}
		if err := projector.Project(ctx, fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
			t.Fatalf("already effective projection err=%v", err)
		}
		var decision string
		if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id=?`, fact.RunID).Scan(&decision); err != nil || !strings.Contains(decision, `"result":"already_effective"`) {
			t.Fatalf("already effective decision=%q err=%v", decision, err)
		}
	})
	t.Run("decision update failure", func(t *testing.T) {
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		wbSeedFailedHealthRun(t, store, "run-health-update", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
		fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", StatHour: "2026-09-05T10", RunID: "run-health-update", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "failure"}
		fp.arm("UPDATE model_check_runs SET quality_decision_json")
		if err := (&QualityProjector{Store: store}).Project(ctx, fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "merge J3b quality decision outcome") {
			t.Fatalf("decision update error=%v", err)
		}
		fp.disarm()
	})
	t.Run("canceled health write keeps pending retry", func(t *testing.T) {
		store, fp := w14mRuntimeDB(t)
		fp.disarm()
		wbSeedFailedHealthRun(t, store, "run-health-canceled", map[string]any{"account": "acct", "profile": "quick", "policy": `{}`, "decision": `{}`, "request": `{}`, "finished": "2026-09-05T09:29:00Z"})
		if _, err := store.db.Exec(`DROP TABLE account_quality_health_hourly`); err != nil {
			t.Fatal(err)
		}
		fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", StatHour: "2026-09-05T10", RunID: "run-health-canceled", ProviderCode: "openai", Model: "gpt-5.6-sol", Profile: "quick", ObservedAt: time.Date(2026, 9, 5, 10, 30, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "failure"}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if err := (&QualityProjector{Store: store}).Project(cancelled, fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil {
			t.Fatal("missing health table must fail")
		}
		var decision string
		if err := store.db.QueryRow(`SELECT quality_decision_json FROM model_check_runs WHERE id=?`, fact.RunID).Scan(&decision); err != nil || !strings.Contains(decision, `"healthSyncResult":"pending_retry"`) {
			t.Fatalf("pending retry decision=%q err=%v", decision, err)
		}
	})
}

func w1OwnerBusinessQualityFixture(t *testing.T) (*BusinessQualityManager, *sql.DB, *w14mFailpoint) {
	t.Helper()
	scheduleDDL := `CREATE TABLE model_quality_schedules (id TEXT PRIMARY KEY,revision INTEGER,system_account_id TEXT,account_id TEXT,model TEXT,interval_minutes INTEGER,profile TEXT,penalty_threshold INTEGER,penalty_action TEXT,recovery_interval_minutes INTEGER,timeout_penalty_enabled INTEGER NOT NULL DEFAULT 0,enabled INTEGER,next_run_at TEXT,lease_owner TEXT,lease_until TEXT,last_run_id TEXT,last_run_at TEXT,last_run_status TEXT,created_at TEXT,updated_at TEXT,custom_question_ids TEXT, UNIQUE(system_account_id,account_id))`
	enforcementDDL := `CREATE TABLE account_quality_enforcements (account_id TEXT PRIMARY KEY,system_account_id TEXT,enforcement_id TEXT,generation INTEGER,state TEXT,action TEXT,recovery_model TEXT,account_config_revision INTEGER,policy_revision INTEGER,config_source_id TEXT,profile TEXT,penalty_threshold INTEGER,recovery_interval_minutes INTEGER,recovery_due_at TEXT,recovery_lease_owner TEXT,recovery_lease_until TEXT,updated_at TEXT,config_source TEXT)`
	db, fp := w14mFailDB(t, append(businessSourceContractDDL(), scheduleDDL, enforcementDDL))
	w14mSeedPlainAccount(t, db, "acct")
	manager, err := NewBusinessQualityManager(db, false)
	if err != nil {
		t.Fatal(err)
	}
	return manager, db, fp
}

func TestW1OwnerBusinessQualityFailureContracts(t *testing.T) {
	ctx := context.Background()
	t.Run("policy transaction and schedule SQL failures", func(t *testing.T) {
		manager, db, fp := w1OwnerBusinessQualityFixture(t)
		fp.disarm()
		fp.arm("FROM model_quality_policies")
		if _, err := manager.PatchPolicy(ctx, "sys-1", QualityPolicyPatch{ExpectedRevision: 4, PenaltyThreshold: qbIntPtr(80)}); err == nil {
			t.Fatal("policy read failure must propagate")
		}
		fp.disarm()
		fp.arm("UPDATE model_quality_policies SET")
		if _, err := manager.PatchPolicy(ctx, "sys-1", QualityPolicyPatch{ExpectedRevision: 4, PenaltyThreshold: qbIntPtr(80)}); err == nil {
			t.Fatal("policy update failure must propagate")
		}
		fp.disarm()
		fp.arm("INSERT INTO model_quality_schedules")
		if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
			t.Fatal("schedule insert failure must propagate")
		}
		fp.disarm()
		fp.arm("FROM accounts")
		if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err == nil {
			t.Fatal("schedule account query failure must propagate")
		}
		_ = db
	})
	t.Run("list and patch schedule rows failures", func(t *testing.T) {
		manager, db, fp := w1OwnerBusinessQualityFixture(t)
		fp.disarm()
		if _, err := manager.CreateSchedule(ctx, "sys-1", QualityScheduleInput{AccountID: "acct", Model: "gpt-5.6-sol", IntervalMinutes: 60, Profile: "quick", PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}); err != nil {
			t.Fatal(err)
		}
		fp.arm("FROM model_quality_schedules mqs JOIN")
		if _, err := manager.ListSchedules(ctx, "sys-1", 1, 20); err == nil {
			t.Fatal("schedule list query failure must propagate")
		}
		fp.disarm()
		if _, err := manager.ListSchedules(ctx, "sys-1", 1, 20); err != nil {
			t.Fatal(err)
		}
		fp.arm("UPDATE model_quality_schedules SET")
		if _, err := manager.PatchSchedule(ctx, "sys-1", "missing", QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: qbIntPtr(90)}); err == nil {
			t.Fatal("missing schedule must fail before update")
		}
		fp.disarm()
		var id string
		if err := db.QueryRow(`SELECT id FROM model_quality_schedules LIMIT 1`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		fp.arm("UPDATE model_quality_schedules SET")
		if _, err := manager.PatchSchedule(ctx, "sys-1", id, QualitySchedulePatch{ExpectedRevision: 1, IntervalMinutes: qbIntPtr(90)}); err == nil {
			t.Fatal("schedule update failure must propagate")
		}
		fp.disarm()
		fp.armScan("FROM model_quality_schedules mqs JOIN")
		if _, err := manager.ListSchedules(ctx, "sys-1", 1, 20); err == nil {
			t.Fatal("schedule scan failure must propagate")
		}
	})
}
