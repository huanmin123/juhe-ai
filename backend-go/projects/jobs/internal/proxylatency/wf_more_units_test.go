package proxylatency

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本文件补齐剩余错误传播分支：管理面配置装载、执行器 claim 释放失败
// （fault-injection 接缝）、runner 释放钩子的 join 语义与投影事务失败传播。

func TestWFLoadManualAdminConfig(t *testing.T) {
	if _, err := LoadManualAdminConfig(nil); err != nil {
		t.Fatalf("nil getenv 回退 os.Getenv: %v", err)
	}
	cfg, err := LoadManualAdminConfig(wfEnv(nil))
	if err != nil || cfg.Enabled {
		t.Fatalf("未启用 cfg=%+v err=%v", cfg, err)
	}
	base := map[string]string{
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:18432",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   "postgres://m:x@127.0.0.1:5432/b",
	}
	cases := []struct {
		name    string
		patch   map[string]string
		wantErr string
	}{
		{name: "缺监听地址", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": ""}, wantErr: "host:port"},
		{name: "监听端口越界", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:70000"}, wantErr: "端口必须"},
		{name: "缺业务 URL", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL": ""}, wantErr: "POSTGRES_URL"},
		{name: "连接数非数字", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_OPEN_CONNS": "x"}, wantErr: "正整数"},
		{name: "deadline 非法", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE": "nope"}, wantErr: "duration"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for key, value := range base {
				env[key] = value
			}
			for key, value := range tt.patch {
				env[key] = value
			}
			_, err := LoadManualAdminConfig(wfEnv(env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v 必须包含 %q", err, tt.wantErr)
			}
		})
	}
	cfg, err = LoadManualAdminConfig(wfEnv(base))
	if err != nil || !cfg.Enabled || cfg.ListenAddress != "127.0.0.1:18432" || cfg.PostgresURL == "" || cfg.MaxOpenConns <= 0 || cfg.RequestDeadline <= 0 {
		t.Fatalf("合法配置=%+v err=%v", cfg, err)
	}
}

func TestWFExecuteIssuedInputClaimReleaseFailure(t *testing.T) {
	store := wfOpenJobsStore(t)
	ctx := context.Background()
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-relfail", time.Hour)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-relfail", time.Hour)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	issued, err := store.IssueInput(ctx, wfCycleDraft("p-relfail"))
	if err != nil {
		t.Fatalf("IssueInput 失败: %v", err)
	}
	admitted, claimToken, _, err := store.AdmitExecution(ctx, owner, proxy, issued)
	if err != nil || claimToken == "" {
		t.Fatalf("admission claim=%q err=%v", claimToken, err)
	}
	// 释放预占 claim，执行器内部会重新 admission。
	if err := store.ReleaseExecutionClaim(ctx, issued.RequestID, claimToken); err != nil {
		t.Fatalf("释放预占 claim 失败: %v", err)
	}
	// fault-injection 接缝：claim 释放失败必须使整次执行失败并清空 outcome。
	store.releaseExecutionClaim = func(context.Context, string, string) error {
		return errors.New("release seam boom")
	}
	options := ExecutorOptions{CredentialSecret: "wf-secret", Timeout: 5 * time.Second}
	outcome, committed, runErr := ExecuteIssuedInput(ctx, store, owner, proxy, admitted, options)
	if runErr == nil || !strings.Contains(runErr.Error(), "release seam boom") {
		t.Fatalf("释放失败 err=%v", runErr)
	}
	if committed || outcome.OutcomeID != "" {
		t.Fatalf("释放失败必须清空 outcome=%+v committed=%v", outcome, committed)
	}
	// AppendOutcome 已随提交删除 claim，且 outcome 已提交：
	// 重复 admission 直接返回 committed 重放（claimToken 为空）。
	replayed, claimToken2, committedReplay, err := store.AdmitExecution(ctx, owner, proxy, admitted)
	if err != nil || claimToken2 != "" || committedReplay == nil || committedReplay.RequestID != replayed.RequestID {
		t.Fatalf("重复 admission claim=%q replay=%+v err=%v", claimToken2, committedReplay, err)
	}
}

func TestWFRunnerReleaseHookFailures(t *testing.T) {
	realStore := wfOpenJobsStore(t)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf", OwnerLease: time.Hour}, realStore, &wfFakeReader{}, nil)
	// 释放钩子缺失时回退 Store：租约行不存在 → 已丢失。
	if err := runner.releaseOwnerLeaseBounded(context.Background(), leaseForTest()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("缺 owner 释放钩子 err=%v", err)
	}
	if err := runner.releaseProxyLeaseBounded(context.Background(), ProxyLease{ProxyID: "p", OwnerID: "o", FenceToken: 1}); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("缺 proxy 释放钩子 err=%v", err)
	}
	// joinReleaseFailure：已有错误 + 释放错误 → 合并。
	joined := joinReleaseFailure(errors.New("primary"), "manual owner lease", errors.New("release"))
	if joined == nil || !strings.Contains(joined.Error(), "primary") || !strings.Contains(joined.Error(), "release") {
		t.Fatalf("join=%v", joined)
	}
	if joinReleaseFailure(nil, "x", nil) != nil {
		t.Fatal("无释放错误必须返回原错误")
	}
	// SetResultProjector nil 接收者安全。
	var nilRunner *Runner
	nilRunner.SetResultProjector(&ResultProjector{})
	// projectOutcomeResult：projector 为 nil 时静默成功（不可达回退防护）。
	if _, err := runner.projectOutcomeResult(context.Background(), Outcome{}); err != nil {
		t.Fatalf("nil projector err=%v", err)
	}
	// 未运行过的 runner 不 ready；nil 接收者安全。
	if _, ready := runner.Snapshot(); ready {
		t.Fatal("未运行的 runner 不得 ready")
	}
	var nilSnapshot *Runner
	if _, ok := nilSnapshot.Snapshot(); ok {
		t.Fatal("nil runner 不得返回状态")
	}
	// health：未就绪时 503。
	handler := runner.HealthHandler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未就绪 status=%d", recorder.Code)
	}
}

func TestWFDecryptBase64SubCases(t *testing.T) {
	secret := "wf-secret"
	envelope := wfSealPassword(t, secret, "pw")
	parts := strings.Split(envelope, ":")
	// iv 非 base64。
	badIV := "v1:" + "!!!" + ":" + parts[2] + ":" + parts[3]
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: badIV}); err == nil {
		t.Fatal("非法 iv base64 必须拒绝")
	}
	// tag 非 base64。
	badTag := "v1:" + parts[1] + ":" + "###" + ":" + parts[3]
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: badTag}); err == nil {
		t.Fatal("非法 tag base64 必须拒绝")
	}
}

func TestWFProjectOutcomeResultPropagatesError(t *testing.T) {
	store := wfOpenJobsStore(t)
	business := wfOpenBusinessDB(t)
	projector := wfNewProjector(t, store, business)
	runner := NewRunner(RuntimeConfig{InstanceID: "wf"}, store, &wfFakeReader{}, nil)
	runner.SetResultProjector(projector)
	// projector 存在但 outcome 不存在 → 错误透传。
	if _, err := runner.projectOutcomeResult(context.Background(), Outcome{OutcomeID: "missing"}); err == nil {
		t.Fatal("projector 错误必须透传")
	}
}
