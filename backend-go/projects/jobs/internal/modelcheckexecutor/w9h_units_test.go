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

func w9hNewStore(t *testing.T) *modelcheckdurable.Store {
	t.Helper()
	store, err := modelcheckdurable.OpenSQLite(filepath.Join(t.TempDir(), "w9h-executor.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func w9hResolvingResolver(target ResolvedTarget, calls *int) TargetResolver {
	return func(_ context.Context, _ ResolutionRequest) (ResolvedTarget, error) {
		if calls != nil {
			*calls++
		}
		return target, nil
	}
}

func w9hTarget(serverURL string) ResolvedTarget {
	return ResolvedTarget{
		ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses",
		ProtocolProfileRevision: "profile-revision-1", Endpoint: serverURL,
		Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol",
		Prompt: "hello", MaxOutputTokens: 32,
	}
}

func w9hRetry() modelcheckprobe.RetryOptions {
	return modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}
}

func TestW9HExecuteInputGuardArms(t *testing.T) {
	store := w9hNewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	resolver := w9hResolvingResolver(w9hTarget("http://127.0.0.1:1"), nil)

	if _, err := ExecuteInputWithOptions(ctx, nil, "input", "owner", "claim", "outcome", now, resolver, w9hRetry(), ExecuteOptions{}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil store err=%v", err)
	}
	if _, err := ExecuteInputWithOptions(ctx, store, "input", "owner", "claim", "outcome", now, nil, w9hRetry(), ExecuteOptions{}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil resolver err=%v", err)
	}
	// LoadInput 缺失。
	if _, err := ExecuteInput(ctx, store, "w9h-absent", "owner", "claim", "outcome", now, resolver, w9hRetry()); err == nil {
		t.Fatal("missing input must fail")
	}
	// 发行一个输入后：resolver 错误传播。
	issued, err := store.Issue(ctx, validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("resolver down")
	if _, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now,
		func(context.Context, ResolutionRequest) (ResolvedTarget, error) { return ResolvedTarget{}, boom }, w9hRetry()); !errors.Is(err, boom) {
		t.Fatalf("resolver err=%v", err)
	}
	// 快照漂移：resolver 返回不同 revision → stale。
	stale := w9hTarget("http://127.0.0.1:1")
	stale.ConfigRevision = "config-revision-9"
	if _, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now,
		w9hResolvingResolver(stale, nil), w9hRetry()); err == nil || !strings.Contains(err.Error(), "target revision or profile is stale") {
		t.Fatalf("stale err=%v", err)
	}
	// 声明后再校验失败 → 释放声明并返回原始错误。
	sequence := 0
	flaky := func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		sequence++
		if sequence == 1 {
			return w9hTarget("http://127.0.0.1:1"), nil
		}
		return ResolvedTarget{}, errors.New("second look failed")
	}
	if _, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-2", "claim-2", "outcome-2", now, flaky, w9hRetry()); err == nil || !strings.Contains(err.Error(), "second look failed") {
		t.Fatalf("recheck err=%v", err)
	}
}

func TestW9HExecuteInputTrustedComparisonArms(t *testing.T) {
	store := w9hNewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()

	draft := validDraft(now)
	comparison := draft.Target
	comparison.ID = "comparison-account"
	draft.Comparison = &comparison
	draft.TrustedComparison = true
	issued, err := store.Issue(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(ctx context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		if request.Account.ID == "comparison-account" {
			return ResolvedTarget{
				ConfigRevision: "config-revision-1", ProtocolProfileID: "profile-openai-responses",
				ProtocolProfileRevision: "profile-revision-1", Endpoint: server.URL,
				Protocol: modelcheckprofile.ProtocolOpenAIResponses, Model: "gpt-5.6-sol",
				Prompt: "hello", MaxOutputTokens: 32,
			}, nil
		}
		return w9hTarget(server.URL), nil
	}
	payload, err := ExecuteInput(ctx, store, issued.Input.InputID, "jobs-1", "claim-1", "outcome-1", now, resolver, w9hRetry())
	if err != nil {
		t.Fatalf("trusted comparison run err=%v", err)
	}
	_ = payload

	// 对照快照漂移：对照 revision 不一致 → stale。
	issued2, err := store.Issue(ctx, func() modelcheckinput.Draft {
		draft2 := validDraft(now.Add(time.Second))
		draft2.InputID = "executor-input-2"
		comparison2 := draft2.Target
		comparison2.ID = "comparison-account"
		draft2.Comparison = &comparison2
		draft2.TrustedComparison = true
		return draft2
	}())
	if err != nil {
		t.Fatal(err)
	}
	// 用显式闭包捕获 request。
	shifted := func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		if request.Account.ID == "comparison-account" {
			stale := w9hTarget(server.URL)
			stale.ConfigRevision = "config-revision-9"
			return stale, nil
		}
		return w9hTarget(server.URL), nil
	}
	if _, err := ExecuteInput(ctx, store, issued2.Input.InputID, "jobs-2", "claim-2", "outcome-2", now, shifted, w9hRetry()); err == nil || !strings.Contains(err.Error(), "trusted comparison revision or profile is stale") {
		t.Fatalf("comparison drift err=%v", err)
	}
}

func TestW9HTerminalSuiteArms(t *testing.T) {
	if terminalSuite(nil) {
		t.Fatal("empty suite is not terminal")
	}
	if terminalSuite([]modelcheckprobe.EvaluationItem{{Evidence: map[string]any{"requestFailure": false}}}) {
		t.Fatal("non-failing evidence is not terminal")
	}
	if !terminalSuite([]modelcheckprobe.EvaluationItem{{Evidence: map[string]any{"requestFailure": true}}}) {
		t.Fatal("request failure is terminal")
	}
	// 无 Evidence 的条目被跳过。
	if terminalSuite([]modelcheckprobe.EvaluationItem{{}, {Evidence: nil}}) {
		t.Fatal("nil evidence entries are skipped")
	}
}
