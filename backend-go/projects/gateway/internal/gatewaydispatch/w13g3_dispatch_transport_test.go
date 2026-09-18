package gatewaydispatch

// w13g3 第四轮：key-model 前台准入 busy 轮换、驱动器准备/URL 错误、并发
// 重新获取、预留并发槽、锁状态观察、代理失败键跨周期清理、高并发排队收尾、
// RequestUpstream 传输错误路径。全部 fake 驱动，无真实 PG/Redis。
//
// 追加不可达登记：
//   - dispatchsingle.go:420-422 attemptStopRotation 分支：
//     runUpstreamAttemptLoop 只返回 attemptStopNone/attemptStopAccount
//     （M-2 注释：轮换循环以 error 或 Selected 退出），该分支不可达。
//   - dispatchsingle.go:424-425 retryAccountApiKey 收尾 break：M-2（BUG-0174）
//     注释明确此处 retryAccountApiKey 恒为 false，条件恒假。
//   - dispatchsingle.go:431-433 accountScopedResult 收尾返回：result 非空时
//     attemptLoopSelected 已在 411-413 提前返回，此处 result 恒为 nil。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// ---------------------------------------------------------------------------
// 可配置驱动器
// ---------------------------------------------------------------------------

type w13g3Driver struct {
	*fakeDriver
	prepareErr     error
	prepareErrOnN  int
	prepareCalls   int
	urlsPlan       [][]string // 按调用次序消费，末位重复
	urlsCalls      int
}

func (d *w13g3Driver) PrepareGatewayUpstreamAccount(ctx context.Context, account AccountCandidate) (AccountCandidate, error) {
	d.prepareCalls++
	if d.prepareErrOnN > 0 && d.prepareCalls >= d.prepareErrOnN {
		return AccountCandidate{}, errW13g3
	}
	if d.prepareErr != nil {
		return AccountCandidate{}, d.prepareErr
	}
	return d.fakeDriver.PrepareGatewayUpstreamAccount(ctx, account)
}

func (d *w13g3Driver) BuildGatewayUpstreamURLsForAccount(ctx context.Context, account AccountCandidate, req *gatewaypreauth.GatewayRequest) ([]string, error) {
	if len(d.urlsPlan) > 0 {
		index := d.urlsCalls
		if index >= len(d.urlsPlan) {
			index = len(d.urlsPlan) - 1
		}
		d.urlsCalls++
		return d.urlsPlan[index], nil
	}
	return d.fakeDriver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
}

// w13g3Admission 返回恒定 busy/blocked 的 key-model 准入 fake。
type w13g3Admission struct {
	status gatewayaccounteffects.AttemptPreparationStatus
}

func (a *w13g3Admission) Prepare(ctx context.Context, store gatewayaccounteffects.KeyModelRuntimeStore, input gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput) (gatewayaccounteffects.GatewayKeyModelAttemptPreparation, error) {
	return gatewayaccounteffects.GatewayKeyModelAttemptPreparation{Status: a.status}, nil
}

// w13g3PoolAccount 构造带 Key 池的 openai 账户（选中指纹生效）。
func w13g3PoolAccount(id string) AccountCandidate {
	revision := int64(1)
	return AccountCandidate{
		ID:               id,
		Name:             "账号 " + id,
		Type:             "api_key",
		Status:           "active",
		ConcurrencyLimit: 4,
		Priority:         1,
		ProviderCode:     "openai",
		ProtocolCode:     "openai",
		ProtocolVersion:  "v1",
		DispatchRevision: &revision,
		Credentials: map[string]any{
			"api_key":  "key-" + id,
			"api_keys": []any{"pool-key-1", "pool-key-2"},
		},
		SupportedModels: []string{"gpt-test"},
	}
}

func TestW13g3KeyModelBusyRotatesKeys(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	driver := &w13g3Driver{fakeDriver: h.driver}
	h.engine.Driver = driver
	h.engine.KeyModel = &w13g3Admission{status: gatewayaccounteffects.AttemptPreparationBusy}
	h.engine.Config.KeyModelForegroundQueuePollMs = 10
	h.engine.Config.KeyModelForegroundQueueWaitMs = 60 // 收紧前台等待窗，控制轮换次数
	accounts := []AccountCandidate{w13g3PoolAccount("a-1")}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, accounts)
	// 两把 Key 都被 busy 拒绝 → Key 池耗尽 → 跳过账户。
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error after busy rotation, got %v", err)
	}
	// busy 等待窗内保留 Key 反复轮换（M-3），窗口过期后剔除 → 多轮准备。
	if driver.prepareCalls < 6 {
		t.Fatalf("prepare calls = %d", driver.prepareCalls)
	}
	if attemptErr.LastAttempt == nil || !strings.Contains(attemptErr.LastAttempt.UpstreamURL, "api_key_pool_unavailable") {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

func TestW13g3KeyModelBusyWaitAborts(t *testing.T) {
	h := newW13g3Harness(t)
	h.driver.urlByAccount = map[string][]string{"a-1": {"https://upstream.example/v1/chat/completions"}}
	h.engine.KeyModel = &w13g3Admission{status: gatewayaccounteffects.AttemptPreparationBusy}
	h.engine.Config.KeyModelForegroundQueuePollMs = 10_000 // 等待窗内被信号中止
	accounts := []AccountCandidate{w13g3PoolAccount("a-1")}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, accounts)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	args.Signal = ctx
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("expected abort during key-model busy wait, got %v", err)
	}
}

func TestW13g3KeyModelBlockedExcludesKeyAndSkips(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	h.engine.KeyModel = &w13g3Admission{status: gatewayaccounteffects.AttemptPreparationBlocked}
	accounts := []AccountCandidate{w13g3PoolAccount("a-1")}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, accounts)
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error, got %v", err)
	}
}

func TestW13g3RotationPreparationAndUrlErrors(t *testing.T) {
	t.Run("prepare error on rotation surfaces", func(t *testing.T) {
		h := newW13g3Harness(t)
		driver := &w13g3Driver{fakeDriver: h.driver, prepareErrOnN: 2}
		h.engine.Driver = driver
		h.engine.KeyModel = &w13g3Admission{status: gatewayaccounteffects.AttemptPreparationBusy}
		h.engine.Config.KeyModelForegroundQueuePollMs = 5
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, []AccountCandidate{w13g3PoolAccount("a-1")})
		if _, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected prepare error, got %v", err)
		}
	})
	t.Run("empty url plan skips account", func(t *testing.T) {
		h := newW13g3Harness(t)
		driver := &w13g3Driver{fakeDriver: h.driver}
		h.engine.Driver = driver
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		// 空 URL 计划 → 跳过账户。
		driver.urlsPlan = [][]string{{}}
		_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("expected attempt error, got %v", err)
		}
	})
}

func TestW13g3RotationReacquireNotAcquiredSkips(t *testing.T) {
	h := newW13g3Harness(t)
	h.engine.Config.AccountConcurrencyRetryBudgetMs = 0
	concurrency := &w13g3AcquireConcurrency{failFromNth: 2}
	h.engine.Concurrency = concurrency
	h.engine.KeyModel = &w13g3Admission{status: gatewayaccounteffects.AttemptPreparationBusy}
	h.engine.Config.KeyModelForegroundQueuePollMs = 5
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, []AccountCandidate{w13g3PoolAccount("a-1")})
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error after reacquire failure, got %v", err)
	}
}

type w13g3AcquireConcurrency struct {
	fakeConcurrencyStore
	failFromNth int // >=1 时第 N 次起拒绝获取
	calls       int
	noHooks     bool // 返回的槽不带 Release/MarkFirstOutput
}

func (c *w13g3AcquireConcurrency) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	c.calls++
	if c.failFromNth > 0 && c.calls >= c.failFromNth {
		return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane, Release: func() {}}, nil
	}
	if c.noHooks {
		return ConcurrencySlot{Acquired: true, Current: 1, Limit: concurrencyLimit, Lane: options.Lane}, nil
	}
	return c.fakeConcurrencyStore.TryAcquireAsync(ctx, accountID, concurrencyLimit, options)
}

func TestW13g3KeySelectionErrorSurfaces(t *testing.T) {
	h := newW13g3Harness(t)
	h.engine.KeyRotation = &w13g3Counter{err: errW13g3}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, []AccountCandidate{w13g3PoolAccount("a-1")})
	if _, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args); !errors.Is(err, errW13g3) {
		t.Fatalf("expected key selection error, got %v", err)
	}
}

func TestW13g3ReservedConcurrencySlotTaken(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"reserved"}`))
	}))
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	args.PreAcquiredConcurrency = &SpeedFirstCutoverReservationHandle{
		TakeForAccount: func(account AccountCandidate) (ConcurrencySlot, bool) {
			return ConcurrencySlot{Acquired: true, Limit: account.ConcurrencyLimit, Release: func() {}}, true
		},
	}
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

func TestW13g3ConcurrencyAcquireErrorSurfaces(t *testing.T) {
	h := newW13g3Harness(t)
	h.engine.Concurrency = &w13g3AcquireErrorStore{}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	if _, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args); !errors.Is(err, errW13g3) {
		t.Fatalf("expected acquire error, got %v", err)
	}
}

type w13g3AcquireErrorStore struct{ fakeConcurrencyStore }

func (c *w13g3AcquireErrorStore) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{}, errW13g3
}

func TestW13g3PerAccountSuppressionFilterError(t *testing.T) {
	h := newW13g3Harness(t)
	h.suppression.perAccountPlan = []w13g3Phase{{err: errW13g3}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	if _, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args); !errors.Is(err, errW13g3) {
		t.Fatalf("expected suppression filter error, got %v", err)
	}
}

func TestW13g3LoopTopAbortAfterSuppressionSleep(t *testing.T) {
	h := newW13g3Harness(t)
	h.suppression.perAccountPlan = []w13g3Phase{{sleep: 200 * time.Millisecond}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	args.Signal = ctx
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var aborted *UpstreamRequestAbortedError
	if !errorsAs(err, &aborted) {
		t.Fatalf("expected abort at attempt loop top, got %v", err)
	}
}

func TestW13g3EscapedDispatchTierObserved(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"tier"}`))
	}))
	defer server.Close()
	h.driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
		"a-2": {server.URL + "/v1/chat/completions"},
	}
	// a-1 高优先级持续 500（关掉同账户重试窗口），a-2 低优先级成功。
	failServer := httptestW13g3StatusServer(t, http.StatusInternalServerError)
	defer failServer.Close()
	h.driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {server.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, testAccounts("a-1", "a-2"))
	args.Settings.TemporaryUnschedulableRetryAttempts = 0
	result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

func TestW13g3AccountLockObservationWiredFromState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stateErr error
	}{
		{"state view wires observation", nil},
		{"state error surfaces", errW13g3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newW13g3Harness(t)
			h.locks.acquireSeq = []LockLeaseAcquire{{Allowed: true, LeaseID: "lease-w13g3-obs"}}
			h.locks.stateErr = tc.stateErr
			if tc.stateErr == nil {
				h.locks.state = &AccountLockStateView{Generation: 7, IncidentID: "inc-7"}
			}
			args := w13g3TransientServer(t, h)
			args.RequestCoordination.AccountLockRetryLease = &AccountLockRetryLease{AccountID: "a-1", LeaseID: "lease-w13g3-obs"}
			_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
			if tc.stateErr != nil {
				if !errors.Is(err, errW13g3) {
					t.Fatalf("expected lock state error, got %v", err)
				}
				return
			}
			// 观察挂载后重试成功是合法结果。
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
		})
	}
}

func TestW13g3FailedProxyKeysClearedAfterRecoverableWait(t *testing.T) {
	h := newW13g3Harness(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"proxy-ok"}`))
	}))
	defer server.Close()
	h.engine.Config.AccountConcurrencyRetryBudgetMs = 0
	h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
	// a-1 绑定不可用代理：账户级放行 → 代理失败跳过并记账；周期后过滤标记
	// 可恢复 → 延迟后重试 → 失败代理键被清空。
	proxyID := "proxy-1"
	proxyMsg := "proxy down"
	account := testAccounts("a-1")[0]
	account.ProxyProfileID = &proxyID
	account.ProxyProfileUnavailable = ptrBool(true)
	account.ProxyProfileErrorMessage = &proxyMsg
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := w13g3Args(t, req, []AccountCandidate{account})
	args.WaitForRecoverableFailures = true
	h.suppression.perAccountPlan = []w13g3Phase{{}}
	h.suppression.postCyclePlan = []w13g3Phase{{ids: []string{"a-1"}}, {}}
	_, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected attempt error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 高并发排队收尾
// ---------------------------------------------------------------------------

type w13g3ConcurrencyHC2 struct {
	fakeConcurrencyStore
	loadErr      error
	loadErrAfter int
	loadCalls    int
}

func (c *w13g3ConcurrencyHC2) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	c.loadCalls++
	if c.loadErr != nil && (c.loadErrAfter == 0 || c.loadCalls > c.loadErrAfter) {
		return nil, c.loadErr
	}
	return map[string]int{}, nil
}

type w13g3AffinityBusy struct {
	fakeAffinity
	busyFromNth int
	busyErrFrom int
	busyCalls   int
}

func (a *w13g3AffinityBusy) AreHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []AccountCandidate, options HighConcurrencyBusyOptions) (bool, error) {
	a.busyCalls++
	if a.busyErrFrom > 0 && a.busyCalls >= a.busyErrFrom {
		return false, errW13g3
	}
	if a.busyFromNth > 0 && a.busyCalls >= a.busyFromNth {
		return true, nil
	}
	return false, nil
}

func w13g3HCPrepInput(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, coordinator *w13g3Coordinator) gatewaypreauth.DispatchPreparationInput {
	t.Helper()
	input := w13g3DispatchPrepInput(t, req, accounts, coordinator)
	hc := "high_concurrency"
	input.GroupAccess.GroupType = &hc
	input.GatewayRequestWallBudget = w13g3WallBudget(t, 60_000)
	return input
}

func TestW13g3PrepareHighConcurrencyTail(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	t.Run("snapshot refresh error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{loadErr: errW13g3}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected refresh error, got %v", err)
		}
	})

	t.Run("busy refresh error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{loadErr: errW13g3, loadErrAfter: 1}
		engine.Affinity = &w13g3AffinityBusy{busyFromNth: 1}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected busy refresh error, got %v", err)
		}
	})

	t.Run("second busy check error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		engine.Affinity = &w13g3AffinityBusy{busyErrFrom: 2}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected second busy error, got %v", err)
		}
	})

	t.Run("third busy check error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		engine.Affinity = &w13g3AffinityBusy{busyErrFrom: 3}
		engine.ClientIPConcurrency = &w13g3ClientIPConcurrency{decision: ClientIPConcurrencyDecision{Enabled: true, Acquired: true, Release: func() {}}}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected third busy error, got %v", err)
		}
	})

	t.Run("busy fallback error after queue", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		engine.Affinity = &w13g3AffinityBusy{busyFromNth: 4}
		engine.ClientIPConcurrency = &w13g3ClientIPConcurrency{decision: ClientIPConcurrencyDecision{Enabled: true, Acquired: true, Release: func() {}}}
		engine.HighConcurrencyQueue = &w13g3Queue{result: QueueWaitResult{Ready: true}}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{fallbackErr: errW13g3})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected busy fallback error, got %v", err)
		}
	})

	t.Run("busy fallback attempted after queue", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		engine.Affinity = &w13g3AffinityBusy{busyFromNth: 4}
		engine.ClientIPConcurrency = &w13g3ClientIPConcurrency{decision: ClientIPConcurrencyDecision{Enabled: true, Acquired: true, Release: func() {}}}
		engine.HighConcurrencyQueue = &w13g3Queue{result: QueueWaitResult{Ready: true}}
		input := w13g3HCPrepInput(t, req, testAccounts("a-1", "a-2"),
			&w13g3Coordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}})
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "high_concurrency_group_busy" {
			t.Fatalf("result = %#v", result)
		}
	})
}

func TestW13g3PrepareCapacityBusyFallback(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	t.Run("capacity busy fallback attempted", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyAlwaysBusy{}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"),
			&w13g3Coordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}})
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "group_capacity_busy" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("capacity busy fallback error", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyAlwaysBusy{}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{fallbackErr: errW13g3})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
	})

	t.Run("capacity ordering error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		// busy 检查成功（第一次 LoadCurrent），排序（第二次）失败。
		engine.Concurrency = &w13g3ConcurrencyHC2{loadErr: errW13g3, loadErrAfter: 1}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected capacity order error, got %v", err)
		}
	})

	t.Run("hot quality order error on normal group", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		engine.HotQuality = &w13g3HotQuality{err: errW13g3}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected hot quality error, got %v", err)
		}
	})

	t.Run("claim reorder error settles exploration", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Concurrency = &w13g3ConcurrencyHC2{}
		settled := ""
		engine.HotQuality = &w13g3HotQualityWithSettle{settle: func(ctx context.Context, outcome string) error {
			settled = outcome
			return nil
		}}
		affinity := &w13g3Affinity{claimID: "a-2", orderErrOn: 2}
		engine.Affinity = affinity
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected claim reorder error, got %v", err)
		}
		if settled != "not_dispatched" {
			t.Fatalf("settle outcome = %q", settled)
		}
	})
}

type w13g3HotQualityWithSettle struct {
	w13g3HotQuality
	settle func(ctx context.Context, outcome string) error
}

func (q *w13g3HotQualityWithSettle) OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	order, err := q.w13g3HotQuality.OrderAsync(ctx, input)
	if err != nil {
		return order, err
	}
	order.ExplorationReservation = &HotQualityReservation{}
	order.SettleExplorationAfterDispatch = q.settle
	return order, nil
}

// ---------------------------------------------------------------------------
// RequestUpstream 传输错误路径
// ---------------------------------------------------------------------------

type w13g3Governor struct{ err error }

func (g *w13g3Governor) Acquire(ctx context.Context) (func(), error) {
	if g.err != nil {
		return nil, g.err
	}
	return func() {}, nil
}

type w13g3Policy struct{ err error }

func (p *w13g3Policy) PrepareSafeUpstreamRequestURL(ctx context.Context, rawURL string) (*url.URL, error) {
	if p.err != nil {
		return nil, p.err
	}
	return url.Parse(rawURL)
}

func TestW13g3RequestUpstreamErrorPaths(t *testing.T) {
	t.Run("governor error surfaces", func(t *testing.T) {
		_, err := RequestUpstream(context.Background(), "https://upstream.example/v1", UpstreamRequestOptions{Method: http.MethodPost}, TransportDeps{Governor: &w13g3Governor{err: errW13g3}})
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected governor error, got %v", err)
		}
	})
	t.Run("policy error surfaces", func(t *testing.T) {
		_, err := RequestUpstream(context.Background(), "https://upstream.example/v1", UpstreamRequestOptions{Method: http.MethodPost}, TransportDeps{
			Governor: &w13g3Governor{}, URLPolicy: &w13g3Policy{err: &UnsafeUpstreamURLError{Message: "unsafe"}},
		})
		var unsafeErr *UnsafeUpstreamURLError
		if !errorsAs(err, &unsafeErr) {
			t.Fatalf("expected unsafe url error, got %v", err)
		}
	})
	t.Run("pre-canceled signal aborts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := RequestUpstream(ctx, "https://upstream.example/v1", UpstreamRequestOptions{Method: http.MethodGet, Signal: ctx}, TransportDeps{})
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort, got %v", err)
		}
	})
	t.Run("invalid proxy surfaces client error", func(t *testing.T) {
		_, err := RequestUpstream(context.Background(), "https://upstream.example/v1", UpstreamRequestOptions{
			Method: http.MethodGet, ProxyURL: "://bad-proxy",
		}, TransportDeps{})
		if err == nil {
			t.Fatal("expected proxy client error")
		}
	})
	t.Run("request timeout fires", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("late"))
		}))
		defer server.Close()
		timeout := int64(30)
		_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
			Method: http.MethodGet, RequestTimeoutMs: &timeout,
		}, TransportDeps{})
		var timeoutErr *UpstreamRequestTimeoutError
		if !errorsAs(err, &timeoutErr) {
			t.Fatalf("expected request timeout, got %v", err)
		}
	})
	t.Run("unsupported encoding fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "bogus")
			_, _ = w.Write([]byte("data"))
		}))
		defer server.Close()
		_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{Method: http.MethodGet}, TransportDeps{})
		var encodingErr *UnsupportedUpstreamResponseEncodingError
		if !errorsAs(err, &encodingErr) {
			t.Fatalf("expected encoding error, got %v", err)
		}
	})
}
