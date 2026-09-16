package modelcheckexecutor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// ExecuteInput 中 trusted-comparison 链路的错误臂：初始 comparison 失败/漂移、
// recheck 失败/漂移、comparison recheck 漂移。

type w10eStep int

const (
	w10eInitialComparisonError w10eStep = iota
	w10eInitialComparisonStale
	w10eTargetRecheckError
	w10eTargetRecheckStale
	w10eComparisonRecheckStale
)

func w10eTrustedIssue(t *testing.T, store *modelcheckdurable.Store, now time.Time) modelcheckinput.IssuedInput {
	t.Helper()
	draft := validDraft(now)
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return issued.Input
}

func w10eResolver(serverURL string, step w10eStep, calls *int, boom error) TargetResolver {
	return func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		*calls++
		accountID := request.Account.ID
		if accountID == "comparison-account" {
			if step == w10eInitialComparisonError && *calls == 2 {
				return ResolvedTarget{}, boom
			}
			if step == w10eInitialComparisonStale && *calls == 2 {
				return ResolvedTarget{ConfigRevision: "comparison-drifted", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
			}
			if step == w10eComparisonRecheckStale && *calls == 4 {
				return ResolvedTarget{ConfigRevision: "comparison-recheck-drifted", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
			}
		} else if accountID == "target-account" {
			if step == w10eTargetRecheckError && *calls == 3 {
				return ResolvedTarget{}, boom
			}
			if step == w10eTargetRecheckStale && *calls == 3 {
				return ResolvedTarget{ConfigRevision: "target-recheck-drifted", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
			}
		}
		return ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}, nil
	}
}

func TestW10ETrustedComparisonErrorArms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK","usage":{"input_tokens":1}}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	retry := modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}
	boom := errors.New("w10e resolver boom")

	run := func(t *testing.T, step w10eStep, wantSubstr string) {
		store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "w10e-executor-trusted.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		issued := w10eTrustedIssue(t, store, now)
		calls := 0
		_, err = ExecuteInput(context.Background(), store, issued.InputID, "jobs-1", "claim-1", "outcome-1", now,
			w10eResolver(server.URL, step, &calls, boom), retry)
		if err == nil || !strings.Contains(err.Error(), wantSubstr) {
			t.Fatalf("step=%v err=%v，期望包含 %q", step, err, wantSubstr)
		}
	}

	run(t, w10eInitialComparisonError, "boom")
	run(t, w10eInitialComparisonStale, "trusted comparison revision or profile is stale")
	run(t, w10eTargetRecheckError, "boom")
	run(t, w10eTargetRecheckStale, "target revision or profile is stale")
	run(t, w10eComparisonRecheckStale, "trusted comparison revision or profile is stale")
}
