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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// w12a_executor_test.go 覆盖执行链的取消传播、comparison recheck 错误臂、
// OnItem 对照项回调与终态重放冲突。

func w12aExecutorServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
}

func w12aExecutorStore(t *testing.T) *modelcheckdurable.Store {
	t.Helper()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "w12a-executor.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func w12aRetry() modelcheckprobe.RetryOptions {
	return modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}
}

func w12aTarget(serverURL string) ResolvedTarget {
	return ResolvedTarget{ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses", ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol", Prompt: "hello", MaxOutputTokens: 32}
}

func TestW12aClaimFailsWhenContextCanceledDuringResolution(t *testing.T) {
	server := w12aExecutorServer()
	defer server.Close()
	store := w12aExecutorStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(context.Background(), validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		cancel()
		return w12aTarget(server.URL), nil
	}, w12aRetry())
	if err == nil {
		t.Fatalf("解析中取消上下文应使 Claim 失败")
	}
}

func TestW12aComparisonRecheckErrorArm(t *testing.T) {
	server := w12aExecutorServer()
	defer server.Close()
	store := w12aExecutorStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("w12a comparison recheck boom")
	calls := 0
	_, err = ExecuteInput(context.Background(), store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		calls++
		if request.Account.ID == "comparison-account" && calls == 4 {
			return ResolvedTarget{}, boom
		}
		return w12aTarget(server.URL), nil
	}, w12aRetry())
	if err == nil || !strings.Contains(err.Error(), "w12a comparison recheck boom") {
		t.Fatalf("comparison recheck 错误臂应失败: %v", err)
	}
}

func TestW12aTrustedComparisonOnItemReceivesComparisonItem(t *testing.T) {
	server := w12aExecutorServer()
	defer server.Close()
	store := w12aExecutorStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	payload, err := ExecuteInputWithOptions(context.Background(), store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		return w12aTarget(server.URL), nil
	}, w12aRetry(), ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		observed++
	}})
	if err != nil {
		t.Fatal(err)
	}
	if observed != len(payload.Items) {
		t.Fatalf("OnItem 应覆盖全部条目: %d vs %d", observed, len(payload.Items))
	}
}

func TestW12aTerminalReplayWithDifferentNowConflicts(t *testing.T) {
	server := w12aExecutorServer()
	defer server.Close()
	store := w12aExecutorStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(context.Background(), validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		return w12aTarget(server.URL), nil
	}
	if _, err := ExecuteInput(context.Background(), store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, resolver, w12aRetry()); err != nil {
		t.Fatal(err)
	}
	// 相同 outcomeID 不同提交时间 → payload 摘要变化 → 终态冲突。
	if _, err := ExecuteInput(context.Background(), store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now.Add(time.Second), resolver, w12aRetry()); err == nil {
		t.Fatalf("不同提交时间的终态重放应冲突")
	}
}
