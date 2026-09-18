package modelcheckexecutor

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	_ "modernc.org/sqlite"
)

// w14i_release_and_suite_arms_test.go 覆盖执行链剩余错误臂：
//   - release 内 ReleaseClaim 失败的合并错误臂（recheck 阶段删除 claim 行）；
//   - runSuite 错误臂（非正重试超时 / 延迟函数注入失败）；
//   - full 剖面扩展探针的错误臂与终态臂（stability 条目后切换探针故障）；
//   - 受信比对套件的错误臂。
//
// w14i 波次不可达清单（均已核对）：
//   - ExecuteInputWithOptions 的 len(items)==0 守卫：RunSuite 的 basic 条目
//     无条件追加，items 恒 >=1，守卫不可达。
//   - commitOutcome 的 len(items)==0 守卫：138 调用点扩展终态至少含 1 条，
//     159 调用点已被前置守卫保护，不可达。
//   - commitOutcome 的 json.Marshal 错误臂：OutcomePayload 仅含字符串/整数/
//     时间与探针包内受控 map（无 chan/func/NaN），序列化恒成功。

type w14iPoisonSwitch struct{ armed bool }

func w14iExecutorStorePath(t *testing.T) (string, *modelcheckdurable.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w14i-executor.sqlite3")
	store, err := modelcheckdurable.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return path, store
}

func w14iRetryWithDelay(poison *w14iPoisonSwitch, boom error) modelcheckprobe.RetryOptions {
	return modelcheckprobe.RetryOptions{
		AttemptTimeouts: []time.Duration{time.Second, time.Second},
		Delay: func(context.Context) error {
			if poison.armed {
				return boom
			}
			return nil
		},
	}
}

func w14iOKBody(w http.ResponseWriter, poison *w14iPoisonSwitch, status int) {
	if poison.armed {
		w.WriteHeader(status)
		return
	}
	_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
}

func TestW14iReleaseJoinsReleaseClaimFailure(t *testing.T) {
	// 契约：recheck 失败进入 release 时，若 claim 行已被删除（并发接管语义），
	// 返回的错误必须同时包含原始失败与 release 失败。
	path, store := w14iExecutorStorePath(t)
	server := w12aExecutorServer()
	defer server.Close()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(context.Background(), validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = ExecuteInput(context.Background(), store, issued.Input.InputID, "w14i-jobs", "w14i-claim", "w14i-outcome", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		calls++
		if calls == 2 {
			// 第二次调用是 recheck：先删除 claim 行再报错，迫使
			// ReleaseClaim 命中 stale fence。
			db, openErr := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
			if openErr != nil {
				t.Fatalf("打开辅助连接失败: %v", openErr)
			}
			defer db.Close()
			if _, execErr := db.Exec(`DELETE FROM model_check_execution_claims`); execErr != nil {
				t.Fatalf("删除 claim 行失败: %v", execErr)
			}
			return ResolvedTarget{}, errors.New("w14i recheck boom")
		}
		return w12aTarget(server.URL), nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "w14i recheck boom") || !strings.Contains(err.Error(), "release model check claim") {
		t.Fatalf("应返回合并错误: %v", err)
	}
}

func TestW14iRunSuiteErrorReleasesClaim(t *testing.T) {
	// 契约：重试超时配置非法时 runSuite 立即失败，执行器释放 claim 并上抛。
	_, store := w14iExecutorStorePath(t)
	server := w12aExecutorServer()
	defer server.Close()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(context.Background(), validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteInput(context.Background(), store, issued.Input.InputID, "w14i-jobs", "w14i-claim", "w14i-outcome", now, func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		return w12aTarget(server.URL), nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{0}})
	if err == nil || !strings.Contains(err.Error(), "retry timeout must be positive") {
		t.Fatalf("非正超时应使套件失败: %v", err)
	}
}

func TestW14iFullProfileExtensionErrorReleasesClaim(t *testing.T) {
	// 契约：full 剖面下扩展探针（identity）失败时必须释放 claim 并上抛原始
	// 错误。stability 条目是核心套件最后一个条目，其 OnItem 回调后进入扩展。
	poison := &w14iPoisonSwitch{}
	boom := errors.New("w14i extension delay boom")
	_, store := w14iExecutorStorePath(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	draft.Profile = "full"
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w14iOKBody(w, poison, http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err = ExecuteInputWithOptions(context.Background(), store, issued.Input.InputID, "w14i-jobs", "w14i-claim", "w14i-outcome", now, func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		return w12aTarget(srv.URL), nil
	}, w14iRetryWithDelay(poison, boom), ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		if item.ItemKey == "target.stability" {
			poison.armed = true
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "w14i extension delay boom") {
		t.Fatalf("扩展探针错误臂应上抛: %v", err)
	}
}

func TestW14iFullProfileExtensionTerminalCommitsOutcome(t *testing.T) {
	// 契约：扩展探针出现终态传输失败时，执行器直接提交已收集条目并成功返回。
	poison := &w14iPoisonSwitch{}
	_, store := w14iExecutorStorePath(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	draft.Profile = "full"
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer srv.Close()
	target := w12aTarget(srv.URL)
	target.Client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if poison.armed {
			return nil, errors.New("w14i forced transport failure")
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	payload, err := ExecuteInputWithOptions(context.Background(), store, issued.Input.InputID, "w14i-jobs", "w14i-claim", "w14i-outcome", now, func(context.Context, ResolutionRequest) (ResolvedTarget, error) {
		return target, nil
	}, modelcheckprobe.RetryOptions{AttemptTimeouts: []time.Duration{time.Second}, Delay: func(context.Context) error { return nil }}, ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		if item.ItemKey == "target.stability" {
			poison.armed = true
		}
	}})
	if err != nil {
		t.Fatalf("扩展终态应直接提交: %v", err)
	}
	found := false
	for _, item := range payload.Items {
		if item.ItemKey == "target.identity_observation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("终态提交应包含 identity 条目: %#v", payload.Items)
	}
}

func TestW14iComparisonSuiteErrorReleasesClaim(t *testing.T) {
	// 契约：受信比对套件探针失败时必须释放 claim 并上抛原始错误。
	poison := &w14iPoisonSwitch{}
	boom := errors.New("w14i comparison delay boom")
	_, store := w14iExecutorStorePath(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	draft := validDraft(now)
	comparison := draft.Target
	comparison.ID = "w14i-comparison-account"
	draft.TrustedComparison = true
	draft.Comparison = &comparison
	issued, err := store.Issue(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w14iOKBody(w, poison, http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err = ExecuteInputWithOptions(context.Background(), store, issued.Input.InputID, "w14i-jobs", "w14i-claim", "w14i-outcome", now, func(_ context.Context, request ResolutionRequest) (ResolvedTarget, error) {
		resolved := w12aTarget(srv.URL)
		resolved.ConfigRevision = request.Account.ConfigRevision
		return resolved, nil
	}, w14iRetryWithDelay(poison, boom), ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		if strings.HasPrefix(item.ItemKey, "trusted_comparison.") {
			poison.armed = true
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "w14i comparison delay boom") {
		t.Fatalf("比对套件错误臂应上抛: %v", err)
	}
}
