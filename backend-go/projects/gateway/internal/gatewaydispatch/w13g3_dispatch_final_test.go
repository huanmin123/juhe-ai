package gatewaydispatch

// w13g3 第六轮（dispatch 收尾）：候选过滤空原因回退、固定指纹排除、Key 池
// 瞬态代选择、失败处理器未知动作落点、PendingApiKeyFailure 换 Key、带期限
// 的竞态中止、空正文管道。全部 fake 驱动。
//
// 追加不可达/不可稳定触发登记：
//   - attemptoutcomes.go:87（half-open 成功结算的 complete 分支）：
//     automaticAccountStateMutationAllowed 要求 probe 流量，而 probe 流量
//     绕过本地抑制（bypass），永不持有 half-open 租约，两条件互斥。
//   - body.go:511-515 / 731-736 的 settledAt > deadline 分支：读取先于
//     precommit 定时器完成时 settledAt <= deadline 恒真；反之 precommit
//     竞态先触发，不会进入 readDone 分支，需纳秒级竞态才可能触发。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW13g3CandidateFilterDefaultReasonAndErrors(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	t.Run("empty capability reason falls back to default", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Driver = &w13g3SilentMismatchDriver{fakeDriver: &fakeDriver{mismatchAll: true}}
		args := w13g3CandidateArgs(req, testAccounts("a-1"), &w13g3Coordinator{
			nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true},
		})
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "request_capability_mismatch" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("unsupported model fallback error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Driver = &fakeDriver{}
		noMatch := []AccountCandidate{{ID: "a-1", SupportedModels: []string{"other"}}}
		args := w13g3CandidateArgs(req, noMatch, &w13g3Coordinator{fallbackErr: errW13g3})
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
		args.RouteCoordinator = &w13g3Coordinator{completeErr: errW13g3}
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})
}

// w13g3SilentMismatchDriver 报告不匹配但不给原因（触发默认原因回退）。
type w13g3SilentMismatchDriver struct {
	*fakeDriver
}

func (d *w13g3SilentMismatchDriver) GatewayRequestCapabilityMismatchReason(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate) string {
	return ""
}

func TestW13g3FixedFingerprintExcludedSkipsAccount(t *testing.T) {
	h := newW13g3Harness(t)
	secret := ""
	account := w13g3PoolAccount("a-1")
	// 预置固定指纹（与池内第一把一致），并在请求级排除它 → 池暂不可用。
	fixed := apiKeyFingerprint("pool-key-1", secret)
	account.SelectedAPIKeyFingerprint = &fixed
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, []AccountCandidate{account})
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error, got %v", err)
	}
}

func TestW13g3TransientGenerationSelection(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen"}`))
	}))
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	generation := "gen-1"
	recovery := "recovery-1"
	account := w13g3PoolAccount("a-1")
	account.APIKeyRuntimeStates = []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
		{Fingerprint: apiKeyFingerprint("", "pool-key-1"), Generation: &generation, RecoveryStartedAt: &recovery},
		{Fingerprint: apiKeyFingerprint("", "pool-key-2"), Generation: &generation, RecoveryStartedAt: &recovery},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, []AccountCandidate{account})
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.SelectedAPIKeyTransientGeneration == nil || *result.Account.SelectedAPIKeyTransientGeneration != "gen-1" {
		t.Fatalf("transient generation = %v", result.Account.SelectedAPIKeyTransientGeneration)
	}
}

func TestW13g3PendingApiKeyFailureRotatesKey(t *testing.T) {
	h := newW13g3Harness(t)
	failServer := httptestW13g3StatusServer(t, http.StatusInternalServerError)
	defer failServer.Close()
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rotated"}`))
	}))
	defer okServer.Close()
	h.driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	// a-1 失败响应携带待定 Key 失败并请求换 Key → 记入 pending 列表。
	h.dispatcher.failedResult = FailedUpstreamResponseResult{
		Action:                  FailedResponseActionSkipAccount,
		TryNextApiKeyForRequest: true,
		PendingApiKeyFailure:    &PendingAccountApiKeyFailure{Account: w13g3PoolAccount("a-1"), Status: "temporary_unavailable"},
	}
	accounts := []AccountCandidate{w13g3PoolAccount("a-1"), w13g3PoolAccount("a-2")}
	accounts[0].SupportedModels = []string{"gpt-test"}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, accounts)
	args.Settings.TemporaryUnschedulableRetryAttempts = 0
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
	// 结算待定失败不返回错误。
	if err := result.ConfirmSameAccountApiKeyFailures(); err != nil {
		t.Fatalf("confirm pending failures: %v", err)
	}
}

func TestW13g3UnknownHandlerActionEndsSkip(t *testing.T) {
	h := newAttemptErrorHarness(t)
	h.engine.FailureDispatcher = &w13g3ClearActionDispatcher{inner: h.engine.FailureDispatcher.(*fakeFailureDispatcher)}
	proven := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, err := h.errorContext(testAccounts("a-1")[0], proven, nil)
	if kind != errorKindHandled || err != nil || stop.kind != errorStopSkipAccount {
		t.Fatalf("expected plain skip stop, got kind=%v stop=%v err=%v", kind, stop, err)
	}
}

// w13g3ClearActionDispatcher 抹掉动作标记（未知动作分支）。
type w13g3ClearActionDispatcher struct {
	inner *fakeFailureDispatcher
}

func (d *w13g3ClearActionDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	return d.inner.HandleFailedUpstreamResponse(ctx, input)
}

func (d *w13g3ClearActionDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	result, err := d.inner.HandleUpstreamRequestError(ctx, input)
	result.Action = ""
	return result, err
}

func (d *w13g3ClearActionDispatcher) IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool {
	return d.inner.IsOpaqueUpstreamFailoverAllowed(req)
}

func TestW13g3PipeRaceAbortAndCaptureBytes(t *testing.T) {
	t.Run("race abort with deadlines configured", func(t *testing.T) {
		hard := int64(60_000)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := PipeNonStreamUpstreamResponse(ctx, &w13g3SlowReader{delay: 300 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: gatewayupstream.NowMs(), FirstByteTimeoutMs: &hard, Signal: ctx})
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected race abort, got %v", err)
		}
	})
	t.Run("soft and precommit race picks precommit", func(t *testing.T) {
		started := gatewayupstream.NowMs()
		soft := int64(200)
		precommit := gatewayupstream.NowMs() + 20
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: 300 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: started, FirstByteDeadlineMs: &soft, ResponsePrecommitDeadlineAtMs: &precommit})
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("expected precommit override, got %v", err)
		}
	})
	t.Run("capture bytes override", func(t *testing.T) {
		captureBytes := int64(4)
		result, err := PipeNonStreamUpstreamResponse(context.Background(),
			strings.NewReader(`{"id":"capture-bytes"}`), &w13g3Writer{}, NonStreamPipeInput{
				StartedAt: gatewayupstream.NowMs(), CaptureBytes: &captureBytes,
			})
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if !result.CaptureTruncated {
			t.Fatalf("captured = %q truncated = %v", result.CapturedBody, result.CaptureTruncated)
		}
		if result.CapturedBodyText == nil || len(*result.CapturedBodyText) != 4 {
			t.Fatalf("captured text = %v", result.CapturedBodyText)
		}
	})
	t.Run("empty body completes without prepare", func(t *testing.T) {
		prepared := false
		completedBytes := -1
		result, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{data: nil},
			&w13g3Writer{}, NonStreamPipeInput{
				StartedAt:         gatewayupstream.NowMs(),
				PrepareDownstream: func() { prepared = true },
				OnBodyCompleted:   func(n int) { completedBytes = n },
			})
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if !prepared || completedBytes != 0 {
			t.Fatalf("prepared=%v completedBytes=%d", prepared, completedBytes)
		}
		_ = result
	})
	t.Run("nil signal falls back to background", func(t *testing.T) {
		outcome, downstreamWriting, err := pipeNonStreamUpstreamResponseCommon(nil,
			strings.NewReader(`{"id":"nil-signal"}`), &w13g3Writer{}, NonStreamPipeInput{StartedAt: gatewayupstream.NowMs()}, false, nil)
		if err != nil || downstreamWriting || !outcome.Result.CaptureTruncated == false {
			t.Fatalf("outcome=%#v writing=%v err=%v", outcome.Result, downstreamWriting, err)
		}
	})
}

func TestW13g3CanAttemptFallbackSingleBinding(t *testing.T) {
	single := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{GroupID: "group-1", Status: "active", GroupEnabled: 1},
	}}
	if CanAttemptApiKeyGroupFallback(single, "group-1", nil) {
		t.Fatal("single binding must not allow fallback")
	}
}
