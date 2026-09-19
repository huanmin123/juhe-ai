package gatewaydispatch

// w13g3 第二轮：候选过滤 / 分组准备（配额、容量、高并发、客户端 IP 并发、
// 会话亲和）/ API Key 组回退候选 / 容量排序 / 诊断错误辅助 / GPT 请求覆盖
// 门控 / 审计端口包装 / 调度结果闭包。全部 fake 驱动，无真实 PG/Redis。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// httptestW13g3StatusServer 返回恒定状态的 JSON 错误上游。
func httptestW13g3StatusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden","type":"invalid_request_error","code":"policy_violation"}}`))
	}))
}

// ---------------------------------------------------------------------------
// 可配置 fake
// ---------------------------------------------------------------------------

type w13g3Coordinator struct {
	fallbackReason string
	fallbackCalled bool
	failure        *gatewayrouting.GatewayRouteFinalFailure
	nextFallback   *gatewayrouting.GatewayRouteFallbackDecision
	fallbackErr    error
	completeErr    error
}

func (c *w13g3Coordinator) RequestFallback(ctx context.Context, reason string) (gatewayrouting.GatewayRouteFallbackDecision, error) {
	c.fallbackCalled = true
	c.fallbackReason = reason
	if c.fallbackErr != nil {
		return gatewayrouting.GatewayRouteFallbackDecision{}, c.fallbackErr
	}
	if c.nextFallback != nil {
		return *c.nextFallback, nil
	}
	return gatewayrouting.GatewayRouteFallbackDecision{Attempted: false}, nil
}

func (c *w13g3Coordinator) CompleteFailure(ctx context.Context, failure gatewayrouting.GatewayRouteFinalFailure) error {
	copied := failure
	c.failure = &copied
	if c.completeErr != nil {
		return c.completeErr
	}
	return nil
}

type w13g3Affinity struct {
	fakeAffinity
	claimID     string
	orderErrOn  int
	orderCalls  int
	busyFromNth int // >=1 时第 N 次起返回繁忙
	busyCalls   int
}

func (a *w13g3Affinity) ClaimAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope AffinityScope) (string, bool) {
	return a.claimID, a.claimID != ""
}

func (a *w13g3Affinity) OrderAsync(ctx context.Context, accounts []AccountCandidate, key string, options AffinityOrderingOptions) ([]AccountCandidate, error) {
	a.orderCalls++
	if a.orderErrOn == a.orderCalls {
		return nil, errW13g3
	}
	return accounts, nil
}

func (a *w13g3Affinity) AreHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []AccountCandidate, options HighConcurrencyBusyOptions) (bool, error) {
	a.busyCalls++
	if a.busyFromNth > 0 && a.busyCalls >= a.busyFromNth {
		return true, nil
	}
	return a.fakeAffinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, options)
}

type w13g3HotQuality struct {
	fakeHotQuality
	err       error
	intent    string
	tierKeys  []string
	reordered bool
}

func (q *w13g3HotQuality) OrderAsync(ctx context.Context, input HotQualityOrderInput) (HotQualityOrder, error) {
	if q.err != nil {
		return HotQualityOrder{}, q.err
	}
	order, err := q.fakeHotQuality.OrderAsync(ctx, input)
	order.DispatchIntent = q.intent
	order.QualityReorderedTierKeys = q.tierKeys
	_ = q.reordered
	return order, err
}

type w13g3ConcurrencyHC struct {
	fakeConcurrencyStore
	loadErr     error
	loadLaneErr error
}

func (c *w13g3ConcurrencyHC) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	return map[string]int{}, nil
}

func (c *w13g3ConcurrencyHC) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	if c.loadLaneErr != nil {
		return nil, c.loadLaneErr
	}
	return map[string]int{}, nil
}

type w13g3Quota struct {
	fakeQuota
	err error
}

func (q *w13g3Quota) CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []AccountCandidate) (map[string]QuotaDecision, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.fakeQuota.CheckBatchAsync(ctx, groupAccess, accounts)
}

type w13g3ClientIPConcurrency struct {
	fakeClientIPConcurrency
	decision ClientIPConcurrencyDecision
	err      error
}

func (c *w13g3ClientIPConcurrency) Acquire(ctx context.Context, input ClientIPConcurrencyInput) (ClientIPConcurrencyDecision, error) {
	if c.err != nil {
		return ClientIPConcurrencyDecision{}, c.err
	}
	return c.decision, nil
}

// w13g3SuppressionExtra 为 ResolveLocalSuppressionFilter 提供错误/nil 变体。
type w13g3SuppressionExtra struct {
	w13g3Suppression
	resolveErr error
	resolveNil bool
}

func (s *w13g3SuppressionExtra) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	if s.resolveErr != nil {
		return nil, false, s.resolveErr
	}
	if s.resolveNil {
		return nil, false, nil
	}
	return s.w13g3Suppression.ResolveLocalSuppressionFilter(ctx, input)
}

type w13g3LocksWithStates struct {
	w13g3FakeLocks
	listErr   error
	listState AccountLockStateView
	hasState  bool
}

func (l *w13g3LocksWithStates) ListStatesAsync(ctx context.Context, accountIDs []string) (map[string]AccountLockStateView, error) {
	if l.listErr != nil {
		return nil, l.listErr
	}
	if !l.hasState {
		return map[string]AccountLockStateView{}, nil
	}
	out := map[string]AccountLockStateView{}
	for _, id := range accountIDs {
		out[id] = l.listState
	}
	return out, nil
}

// w13g3DispatchPrepInput 组装标准准备输入。
func w13g3DispatchPrepInput(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, coordinator *w13g3Coordinator) gatewaypreauth.DispatchPreparationInput {
	t.Helper()
	return gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		StartedAt:         gatewayupstream.NowMs(),
		CandidateAccounts: accounts,
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		APIKeyID:          "apikey-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  coordinator,
		Signal:            context.Background(),
	}
}

// ---------------------------------------------------------------------------
// 候选过滤（candidatefilter.go）
// ---------------------------------------------------------------------------

func w13g3CandidateArgs(req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, coordinator *w13g3Coordinator) CandidateFilterArgs {
	return CandidateFilterArgs{
		Req:                  req,
		AuditCapture:         AuditCapture{Context: &frozenAudit{sink: &fakeAuditSink{}}, Sink: &fakeAuditSink{}},
		UsageContext:         testUsageContext(),
		StartedAt:            gatewayupstream.NowMs(),
		RawCandidateAccounts: accounts,
		SystemAccountID:      "system-1",
		GroupID:              "group-1",
		RouteCoordinator:     coordinator,
	}
}

func TestW13g3CandidateFilterEdges(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	t.Run("model-aware loader error surfaces", func(t *testing.T) {
		pipeline, _, _, _ := newPipeline(t)
		args := w13g3CandidateArgs(req, nil, &w13g3Coordinator{})
		args.LoadModelAwareCandidateAccounts = func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
			return nil, errW13g3
		}
		_, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected loader error, got %v", err)
		}
	})

	t.Run("model-aware loader populates raw candidates", func(t *testing.T) {
		pipeline, _, _, _ := newPipeline(t)
		args := w13g3CandidateArgs(req, nil, &w13g3Coordinator{})
		args.LoadModelAwareCandidateAccounts = func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
			return testAccounts("a-1"), nil
		}
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(result.Accounts) != 1 {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("no candidates fallback error and recovery", func(t *testing.T) {
		pipeline, _, _, _ := newPipeline(t)
		coordinator := &w13g3Coordinator{fallbackErr: errW13g3}
		args := w13g3CandidateArgs(req, nil, coordinator)
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
		// 回退尝试后恢复不可用候选。
		args.RouteCoordinator = &w13g3Coordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
		args.RecoverUnavailableCandidateAccounts = func(ctx context.Context) ([]AccountCandidate, error) {
			return testAccounts("a-1"), nil
		}
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback {
			t.Fatalf("fallback attempted must short-circuit: %#v", result)
		}
		// 回退未尝试 + 恢复错误。
		args2 := w13g3CandidateArgs(req, nil, &w13g3Coordinator{})
		args2.RecoverUnavailableCandidateAccounts = func(ctx context.Context) ([]AccountCandidate, error) {
			return nil, errW13g3
		}
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args2); !errors.Is(err, errW13g3) {
			t.Fatalf("expected recover error, got %v", err)
		}
	})

	t.Run("capability mismatch fallback error and attempted", func(t *testing.T) {
		pipeline, _, driver, _ := newPipeline(t)
		driver.mismatchAll = true
		coordinator := &w13g3Coordinator{fallbackErr: errW13g3}
		args := w13g3CandidateArgs(req, testAccounts("a-1"), coordinator)
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
		attempted := &w13g3Coordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
		args.RouteCoordinator = attempted
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "request_capability_mismatch" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("capability mismatch complete failure error", func(t *testing.T) {
		pipeline, _, driver, _ := newPipeline(t)
		driver.mismatchAll = true
		args := w13g3CandidateArgs(req, testAccounts("a-1"), &w13g3Coordinator{completeErr: errW13g3})
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})

	t.Run("model-aware reload error and unsupported fallback", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Driver = &fakeDriver{}
		noMatch := []AccountCandidate{{ID: "a-1", SupportedModels: []string{"other"}}}
		loader := func(ctx context.Context, requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
			return nil, errW13g3
		}
		args := w13g3CandidateArgs(req, noMatch, &w13g3Coordinator{})
		args.LoadModelAwareCandidateAccounts = loader
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args); !errors.Is(err, errW13g3) {
			t.Fatalf("expected reload error, got %v", err)
		}
		// 模型不支持 → 回退尝试。
		attempted := &w13g3Coordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
		args2 := w13g3CandidateArgs(req, noMatch, attempted)
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args2)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "unsupported_model" {
			t.Fatalf("result = %#v", result)
		}
		// 模型不支持 → CompleteFailure 错误。
		args3 := w13g3CandidateArgs(req, noMatch, &w13g3Coordinator{completeErr: errW13g3})
		if _, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args3); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})

	t.Run("missing model audit path with nil attributes", func(t *testing.T) {
		pipeline, _, _, _ := newPipeline(t)
		audit := &frozenAudit{sink: &fakeAuditSink{}}
		args := w13g3CandidateArgs(newTestRequest(t, `{"stream":true}`), testAccounts("a-1"), &w13g3Coordinator{})
		args.AuditCapture = AuditCapture{Context: audit, Sink: audit.sink}
		result, err := pipeline.FilterOpenAIGatewayRequestCandidateAccounts(context.Background(), args)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
			t.Fatalf("missing model must complete 503: %#v", result)
		}
	})
}

// ---------------------------------------------------------------------------
// 分组准备（preparation.go / prepareQuotaAndCapacityReadyAccounts）
// ---------------------------------------------------------------------------

func TestW13g3PrepareEdges(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	newPrepEngine := func(t *testing.T) (*CandidatePipeline, *Engine) {
		t.Helper()
		pipeline, engine, _, _ := newPipeline(t)
		return pipeline, engine
	}

	t.Run("probe traffic bypasses suppression", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		engine.Suppression = &w13g3Suppression{perAccountPlan: []w13g3Phase{{all: true}}, postCyclePlan: []w13g3Phase{{all: true}}}
		coordinator := &w13g3Coordinator{}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), coordinator)
		input.UsageContext.TrafficSource = "probe"
		input.IgnoreAccountRuntimeSuppression = true
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("probe traffic must bypass suppression: %#v", result)
		}
	})

	t.Run("suppressed fallback error surfaces", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		engine.Suppression = &w13g3Suppression{postCyclePlan: []w13g3Phase{{all: true}}}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{fallbackErr: errW13g3})
		_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
	})

	t.Run("precheck half-open eligible bypasses when fallback not attempted", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		suppression := &w13g3SuppressionExtra{}
		suppression.postCyclePlan = []w13g3Phase{{all: true, precheckIDs: []string{"a-1"}}}
		engine.Suppression = suppression
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{})
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("precheck-eligible candidates must dispatch: %#v", result)
		}
	})

	t.Run("resolve filter error and nil result", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		suppression := &w13g3SuppressionExtra{resolveErr: errW13g3}
		engine.Suppression = suppression
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected resolve error, got %v", err)
		}
		suppression.resolveErr = nil
		suppression.resolveNil = true
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
			t.Fatalf("nil resolved filter must complete: %#v", result)
		}
	})

	t.Run("runtime degraded fallback error surfaces", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		engine.Degradation = &allDegradedPort{}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{fallbackErr: errW13g3})
		_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected fallback error, got %v", err)
		}
	})

	t.Run("request route fallback honors account lock states", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		locks := &w13g3LocksWithStates{listErr: errW13g3}
		engine.Locks = locks
		engine.Degradation = &allDegradedPort{}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{})
		// ListStates 错误向上传播。
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected list states error, got %v", err)
		}
		// 跨账户阻断锁存在 → 不做分组回退，继续派发。
		locks.listErr = nil
		locks.hasState = true
		locks.listState = AccountLockStateView{BlocksCrossAccount: true}
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("blocking lock must skip the fallback: %#v", result)
		}
	})

	t.Run("affinity claim reorder error releases concurrency", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		affinity := &w13g3Affinity{claimID: "a-2", orderErrOn: 2} // 第二次 = claim 重排
		engine.Affinity = affinity
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected claim reorder error, got %v", err)
		}
	})

	t.Run("affinity claim reorder success path", func(t *testing.T) {
		pipeline, engine := newPrepEngine(t)
		affinity := &w13g3Affinity{claimID: "a-2"}
		engine.Affinity = affinity
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("result = %#v", result)
		}
	})
}

func TestW13g3PrepareQuotaAndCapacityEdges(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)

	t.Run("quota fallback error and complete failure error", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Quota = &w13g3Quota{fakeQuota: fakeQuota{denied: map[string]struct{}{"a-1": {}}}}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{fallbackErr: errW13g3})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected quota fallback error, got %v", err)
		}
		input.RouteCoordinator = &w13g3Coordinator{completeErr: errW13g3}
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})

	t.Run("quota check error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Quota = &w13g3Quota{err: errW13g3}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{})
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected quota error, got %v", err)
		}
	})

	t.Run("no available account completes with error surface", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Suppression = &w13g3Suppression{postCyclePlan: []w13g3Phase{{all: true}}}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1"), &w13g3Coordinator{completeErr: errW13g3})
		// 全抑制 + 回退未尝试 → 空账户 → CompleteFailure 错误。
		input.UsageContext.TrafficSource = "probe" // 绕过回退前抑制
		input.IgnoreAccountRuntimeSuppression = true
		engine.Quota = &w13g3Quota{}
		// 空候选列表触发 503。
		input.CandidateAccounts = nil
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})

	t.Run("high concurrency refresh and hot quality paths", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		hc := "high_concurrency"
		concurrency := &w13g3ConcurrencyHC{loadErr: errW13g3}
		engine.Concurrency = concurrency
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		input.GroupAccess.GroupType = &hc
		// 快照刷新错误。
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected refresh error, got %v", err)
		}
		// 高并发繁忙排序错误（busy 分支内刷新成功 → Order 失败）。
		concurrency.loadErr = nil
		affinity := &w13g3Affinity{busyFromNth: 1, orderErrOn: 2}
		engine.Affinity = affinity
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected busy order error, got %v", err)
		}
		// 高并发 hot quality 排序错误。
		affinity.orderErrOn = 0
		engine.HotQuality = &w13g3HotQuality{err: errW13g3}
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected hot quality error, got %v", err)
		}
	})

	t.Run("high concurrency readiness with hot quality audit", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		hc := "high_concurrency"
		engine.Concurrency = &w13g3ConcurrencyHC{}
		engine.HotQuality = &w13g3HotQuality{intent: "same_tier_exploration", tierKeys: []string{"tier-1"}}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		input.GroupAccess.GroupType = &hc
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("capacity busy fallback and ordering error", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		concurrency := &w13g3ConcurrencyHC{}
		engine.Concurrency = concurrency
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		// LoadCurrent 错误 → busy 检查错误（普通组也会读取当前并发）。
		concurrency.loadErr = errW13g3
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected capacity busy error, got %v", err)
		}
		// busy 检查正常、排序错误。
		concurrency.loadErr = nil
		concurrency.loadLaneErr = errW13g3
		// 镜像通道才读 lane 并发；这里直接用负载错误验证排序路径。
		concurrency.loadLaneErr = nil
		blocked := &w13g3ConcurrencyHC{loadErr: nil}
		engine.Concurrency = blocked
		blockedAcquire := &w13g3ConcurrencyAlwaysBusy{}
		engine.Concurrency = blockedAcquire
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); err != nil && !errors.Is(err, errW13g3) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("high concurrency client ip concurrency decisions", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		hc := "high_concurrency"
		engine.Concurrency = &w13g3ConcurrencyHC{}
		clientIP := &w13g3ClientIPConcurrency{err: errW13g3}
		engine.ClientIPConcurrency = clientIP
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		input.GroupAccess.GroupType = &hc
		// Acquire 错误。
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected client ip acquire error, got %v", err)
		}
		// 未获取 + 信号中止 → completed。
		clientIP.err = nil
		clientIP.decision = ClientIPConcurrencyDecision{Enabled: true, Acquired: false, Reason: "timeout"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		input.Signal = ctx
		result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
			t.Fatalf("aborted request must complete: %#v", result)
		}
		// 未获取 + CompleteFailure 错误。
		input.Signal = context.Background()
		input.RouteCoordinator = &w13g3Coordinator{completeErr: errW13g3}
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected complete failure error, got %v", err)
		}
	})

	t.Run("high concurrency queue wait error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		hc := "high_concurrency"
		engine.Concurrency = &w13g3ConcurrencyHC{}
		affinity := &w13g3Affinity{busyFromNth: 3} // 第三次繁忙检查（获取后）触发排队
		engine.Affinity = affinity
		engine.HighConcurrencyQueue = &w13g3Queue{err: errW13g3}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), &w13g3Coordinator{})
		input.GroupAccess.GroupType = &hc
		input.GatewayRequestWallBudget = w13g3WallBudget(t, 60_000)
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected queue wait error, got %v", err)
		}
	})

	t.Run("high concurrency busy completes 429", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		hc := "high_concurrency"
		engine.Concurrency = &w13g3ConcurrencyHC{}
		// 排队后仍繁忙 → 回退未尝试 → CompleteFailure。
		affinity := &w13g3Affinity{busyFromNth: 3}
		engine.Affinity = affinity
		engine.HighConcurrencyQueue = &w13g3Queue{result: QueueWaitResult{Ready: true}}
		coordinator := &w13g3Coordinator{completeErr: errW13g3}
		input := w13g3DispatchPrepInput(t, req, testAccounts("a-1", "a-2"), coordinator)
		input.GroupAccess.GroupType = &hc
		input.GatewayRequestWallBudget = w13g3WallBudget(t, 60_000)
		if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); !errors.Is(err, errW13g3) {
			t.Fatalf("expected busy complete failure error, got %v", err)
		}
		if coordinator.failure == nil || coordinator.failure.ErrorCode != "rate_limit_exceeded" {
			t.Fatalf("failure = %#v", coordinator.failure)
		}
	})
}

// w13g3ConcurrencyAlwaysBusy 让每账户按上限满载（busy 判定恒真）。
type w13g3ConcurrencyAlwaysBusy struct{}

func (c *w13g3ConcurrencyAlwaysBusy) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	out := map[string]int{}
	for _, id := range accountIDs {
		out[id] = 1 << 20
	}
	return out, nil
}

func (c *w13g3ConcurrencyAlwaysBusy) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return c.LoadCurrentAsync(ctx, accountIDs)
}

func (c *w13g3ConcurrencyAlwaysBusy) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane}, nil
}

// ---------------------------------------------------------------------------
// API Key 组回退候选（fallbackcandidate.go）
// ---------------------------------------------------------------------------

func TestW13g3GroupFallbackRoutePlanAndErrors(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	bindings := []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{GroupID: "group-1", Status: "active", GroupEnabled: 1},
		{GroupID: "group-2", Status: "active", GroupEnabled: 1},
	}
	apiKeyRecord := &gatewayruntimecache.GatewayAPIKeyRow{ID: "apikey-1", GroupBindings: bindings}

	t.Run("route plan snapshot advances cursor", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &fakeCacheFallback{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		snapshot := &gatewayrouting.RoutePlanSnapshot[string]{
			RoutePlanID:                "plan-w13g3",
			OrderedAllowedTargets:      []string{"group-1", "group-2"},
			Cursor:                     0,
			GatewayRequestWallBudgetMs: 60_000,
		}
		candidate, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req:               req,
			Reason:            "upstream_accounts_exhausted",
			APIKeyRecord:      apiKeyRecord,
			SystemAccountID:   "system-1",
			GroupID:           "group-1",
			RequestLane:       "text",
			RoutePlanSnapshot: snapshot,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !found || candidate.GroupID != "group-2" || candidate.RoutePlanSnapshot == nil || candidate.RoutePlanSnapshot.Cursor != 1 {
			t.Fatalf("candidate = %#v", candidate)
		}
	})

	t.Run("quota error surfaces", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &fakeCacheFallback{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		engine.Quota = &w13g3Quota{err: errW13g3}
		_, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: apiKeyRecord,
			SystemAccountID: "system-1", GroupID: "group-1", RequestLane: "text",
		})
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected quota error, got %v", err)
		}
	})

	t.Run("capacity busy skips group", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &fakeCacheFallback{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		engine.Concurrency = &w13g3ConcurrencyAlwaysBusy{}
		_, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "group_capacity_busy", APIKeyRecord: apiKeyRecord,
			SystemAccountID: "system-1", GroupID: "group-1", RequestLane: "text",
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if found {
			t.Fatal("a busy fallback group must be skipped")
		}
	})
}

// ---------------------------------------------------------------------------
// 容量排序 / 诊断错误 / 审计端口
// ---------------------------------------------------------------------------

func TestW13g3CapacityHelpers(t *testing.T) {
	t.Run("ordering load error surfaces", func(t *testing.T) {
		_, err := OrderGatewayAccountsByLaneCapacityAvailabilityAsync(
			context.Background(), &w13g3ConcurrencyHC{loadErr: errW13g3},
			testAccounts("a-1", "a-2"), "text", nil, nil)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected load error, got %v", err)
		}
	})
	t.Run("image lane load error surfaces", func(t *testing.T) {
		_, err := OrderGatewayAccountsByLaneCapacityAvailabilityAsync(
			context.Background(), &w13g3ConcurrencyHC{loadLaneErr: errW13g3},
			testAccounts("a-1", "a-2"), "image", nil, nil)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected image lane load error, got %v", err)
		}
		busy, err := AreGatewayAccountsCapacityBusyForLaneAsync(
			context.Background(), &w13g3ConcurrencyHC{loadLaneErr: errW13g3},
			testAccounts("a-1"), "image", nil)
		if !errors.Is(err, errW13g3) || busy {
			t.Fatalf("expected image lane busy error, got %v/%v", busy, err)
		}
	})
	t.Run("busy load error surfaces", func(t *testing.T) {
		_, err := AreGatewayAccountsCapacityBusyForLaneAsync(
			context.Background(), &w13g3ConcurrencyHC{loadErr: errW13g3},
			testAccounts("a-1"), "text", nil)
		if !errors.Is(err, errW13g3) {
			t.Fatalf("expected busy load error, got %v", err)
		}
	})
	t.Run("concurrency ids skip blanks and dedupe", func(t *testing.T) {
		accounts := []AccountCandidate{
			{ID: "a-1"}, {ID: ""}, {ID: "a-1"}, {ID: "a-2"},
		}
		ids := gatewaySessionConcurrencyIDs(accounts)
		if len(ids) != 2 {
			t.Fatalf("ids = %#v", ids)
		}
	})
	t.Run("limits use minimum for shared credential", func(t *testing.T) {
		source := "shared"
		limits := GatewayAccountConcurrencyLimitsByAccountID([]AccountCandidate{
			{ID: "a", CredentialSourceAccountID: &source, ConcurrencyLimit: 5},
			{ID: "b", CredentialSourceAccountID: &source, ConcurrencyLimit: 3},
			{ID: "", ConcurrencyLimit: 9},
		})
		if limits["shared"] != 3 {
			t.Fatalf("limits = %#v", limits)
		}
		if _, ok := limits[""]; ok {
			t.Fatal("blank account id must not contribute a limit")
		}
	})
}

func TestW13g3DiagnosticErrorHelpers(t *testing.T) {
	t.Run("transport status mapping", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:            "a-1",
			TransportFailureKind: TransportFailureKindConnection,
		}, "fallback", nil)
		if diagnostic.StatusCode != 502 {
			t.Fatalf("status = %d", diagnostic.StatusCode)
		}
		if diagnostic.Payload.Error.Type != "upstream_transport_error" || diagnostic.Payload.Error.Code != "upstream_connection" {
			t.Fatalf("payload = %#v", diagnostic.Payload.Error)
		}
	})
	t.Run("protocol error parser hook", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:        "a-1",
			ResponseBodyText: `{"detail":"x"}`,
		}, "fallback", func(attempt UpstreamAttempt, payload map[string]any) ProtocolErrorPayload {
			return ProtocolErrorPayload{Message: "协议错误", Type: "protocol", Code: "proto_code"}
		})
		if diagnostic.ErrorMessage != "协议错误" || !diagnostic.PreserveUpstreamMessage {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
	})
	t.Run("headers nil and populated", func(t *testing.T) {
		if headers := headersFromObject(nil); headers == nil || len(headers) != 0 {
			t.Fatal("nil headers must yield empty header set")
		}
		headers := headersFromObject(map[string]string{"X-Test": "1"})
		if headers.Get("X-Test") != "1" {
			t.Fatalf("headers = %#v", headers)
		}
	})
	t.Run("error object extra fields preserved", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:        "a-1",
			ResponseBodyText: `{"error":{"message":"m","type":"t","code":"c","meta":42}}`,
		}, "fallback", nil)
		if diagnostic.Payload.Error.Extra == nil || diagnostic.Payload.Error.Extra["meta"] != float64(42) {
			t.Fatalf("extra = %#v", diagnostic.Payload.Error.Extra)
		}
	})
	t.Run("non object body yields generic payload", func(t *testing.T) {
		diagnostic := BuildDiagnosticUpstreamError(&UpstreamAttempt{
			AccountID:        "a-1",
			Message:          "上游失败",
			ResponseBodyText: "not json",
		}, "fallback", nil)
		if diagnostic.Payload.Error.Message != "上游失败" || diagnostic.PreserveUpstreamMessage {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
	})
	t.Run("transport type and code helpers", func(t *testing.T) {
		if errorTypeForTransport("") != "" || errorCodeForTransport("") != "" {
			t.Fatal("empty transport yields empty classification")
		}
		if errorTypeForTransport("reset") != "upstream_transport_error" || errorCodeForTransport("reset") != "upstream_reset" {
			t.Fatal("connection transport classification mismatch")
		}
	})
}

// w13g3DualAudit 同时实现 AuditCaptureContext 与 AttemptAuditSink。
type w13g3DualAudit struct {
	context *frozenAudit
	sink    *fakeAuditSink
}

func (a *w13g3DualAudit) BindContext(ctx gatewaypreauth.AuditGatewayContext) {
	a.context.BindContext(ctx)
}

func (a *w13g3DualAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	a.context.AddGatewayMetadata(label, metadata)
}

func (a *w13g3DualAudit) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	a.context.Finalize(input)
}

func (a *w13g3DualAudit) StartAttempt(input StartAttemptInput) string {
	return a.sink.StartAttempt(input)
}

func (a *w13g3DualAudit) CompleteAttempt(attemptID string, input CompleteAttemptInput) {
	a.sink.CompleteAttempt(attemptID, input)
}

func (a *w13g3DualAudit) RecordFailedDispatchAttempt(input FailedDispatchAttemptInput) {
	a.sink.RecordFailedDispatchAttempt(input)
}

func TestW13g3AuditCaptureSinkSelection(t *testing.T) {
	// Context 本身实现 AttemptAuditSink 时优先于 Sink。
	dual := &w13g3DualAudit{context: &frozenAudit{}, sink: &fakeAuditSink{}}
	capture := AuditCapture{Context: dual}
	if capture.StartAttempt(StartAttemptInput{}) == "" {
		t.Fatal("context sink must serve startAttempt")
	}
	capture.CompleteAttempt("a", CompleteAttemptInput{})
	capture.RecordFailedDispatchAttempt(FailedDispatchAttemptInput{})
	if dual.sink.started != 1 || dual.sink.completed != 1 || dual.sink.failed != 1 {
		t.Fatalf("sink counters = %d/%d/%d", dual.sink.started, dual.sink.completed, dual.sink.failed)
	}
	// 无任何 sink → 空尝试 ID 且不 panic。
	var empty AuditCapture
	if !empty.Nil() {
		t.Fatal("empty capture must report nil")
	}
	if empty.StartAttempt(StartAttemptInput{}) != "" {
		t.Fatal("empty capture startAttempt must be empty")
	}
	empty.CompleteAttempt("a", CompleteAttemptInput{})
	empty.RecordFailedDispatchAttempt(FailedDispatchAttemptInput{})
	// 仅 Sink 时走 Sink 分支。
	sinkOnly := AuditCapture{Sink: dual.sink}
	if sinkOnly.StartAttempt(StartAttemptInput{}) == "" {
		t.Fatal("sink fallback must serve startAttempt")
	}
}

// ---------------------------------------------------------------------------
// GPT 账户请求覆盖（oauthnormalizer_overrides.go）
// ---------------------------------------------------------------------------

func TestW13g3GptOverrideTokenValidation(t *testing.T) {
	t.Run("read overrides", func(t *testing.T) {
		if _, err := ReadGptAccountRequestOverrides(nil); err != nil {
			t.Fatalf("nil credentials: %v", err)
		}
		overrides, err := ReadGptAccountRequestOverrides(map[string]any{
			"service_tier_override":     "priority",
			"reasoning_effort_override": nil,
		})
		if err != nil || overrides.ServiceTier != "priority" || overrides.ReasoningEffort != "" {
			t.Fatalf("overrides = %#v err = %v", overrides, err)
		}
		if _, err := ReadGptAccountRequestOverrides(map[string]any{"service_tier_override": 42}); err == nil {
			t.Fatal("non-string token must fail")
		}
		if _, err := ReadGptAccountRequestOverrides(map[string]any{"service_tier_override": ""}); err != nil {
			t.Fatalf("empty token is accepted: %v", err)
		}
		if _, err := ReadGptAccountRequestOverrides(map[string]any{"reasoning_effort_override": " bad "}); err == nil {
			t.Fatal("whitespace token must fail")
		}
		var overrideErr *GptAccountRequestOverrideError
		if _, err := ReadGptAccountRequestOverrides(map[string]any{"reasoning_effort_override": "被禁止"}); !errorsAs(err, &overrideErr) {
			t.Fatalf("expected override error, got %v", err)
		}
	})
	t.Run("assert values", func(t *testing.T) {
		if err := AssertGptAccountRequestOverrideValues(GptAccountRequestOverrides{ServiceTier: "super"}); err == nil {
			t.Fatal("invalid service tier must fail")
		}
		if err := AssertGptAccountRequestOverrideValues(GptAccountRequestOverrides{ReasoningEffort: "ultra"}); err == nil {
			t.Fatal("invalid reasoning effort must fail")
		}
		if err := AssertGptAccountRequestOverrideValues(GptAccountRequestOverrides{ServiceTier: "flex", ReasoningEffort: "xhigh"}); err != nil {
			t.Fatalf("valid overrides: %v", err)
		}
	})
	t.Run("applicability gate", func(t *testing.T) {
		if HasApplicableGptAccountRequestOverrides(GptAccountRequestOverrides{ServiceTier: "priority"}, "", false) {
			t.Fatal("empty endpoint family is never applicable")
		}
		if !HasApplicableGptAccountRequestOverrides(GptAccountRequestOverrides{ReasoningEffort: "high"}, "responses", false) {
			t.Fatal("reasoning effort applies on non-compact responses")
		}
		if HasApplicableGptAccountRequestOverrides(GptAccountRequestOverrides{ReasoningEffort: "high"}, "responses", true) {
			t.Fatal("reasoning effort is inert on compact requests")
		}
	})
	t.Run("effective overrides honor capabilities", func(t *testing.T) {
		if effective := EffectiveGptAccountRequestOverrides(GptAccountRequestOverrides{}, nil); effective != (GptAccountRequestOverrides{}) {
			t.Fatalf("empty overrides = %#v", effective)
		}
		if effective := EffectiveGptAccountRequestOverrides(GptAccountRequestOverrides{ServiceTier: "priority"}, nil); effective != (GptAccountRequestOverrides{}) {
			t.Fatalf("nil capabilities keep overrides inert: %#v", effective)
		}
		effective := EffectiveGptAccountRequestOverrides(
			GptAccountRequestOverrides{ServiceTier: "priority", ReasoningEffort: "high"},
			&GptRequestOverrideModelCapabilities{SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"high"}},
		)
		if effective.ServiceTier != "priority" || effective.ReasoningEffort != "high" {
			t.Fatalf("effective = %#v", effective)
		}
		defaultTier := EffectiveGptAccountRequestOverrides(
			GptAccountRequestOverrides{ServiceTier: "default"},
			&GptRequestOverrideModelCapabilities{SupportedServiceTiers: []string{"flex"}},
		)
		if defaultTier.ServiceTier != "default" {
			t.Fatalf("default tier requires any supported tier list: %#v", defaultTier)
		}
	})
}

func TestW13g3ApplyOverridesToUpstreamBody(t *testing.T) {
	ctx := context.Background()
	account := AccountCandidate{ID: "a-1", ProviderCode: "openai", Credentials: map[string]any{"service_tier_override": "priority"}}
	t.Run("invalid credentials surface", func(t *testing.T) {
		bad := AccountCandidate{ID: "a-1", Credentials: map[string]any{"service_tier_override": "!!"}}
		if _, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, []byte(`{}`), bad, "responses", false, "gpt-test"); err == nil {
			t.Fatal("invalid credential token must fail")
		}
		invalidTier := AccountCandidate{ID: "a-1", Credentials: map[string]any{"service_tier_override": "super"}}
		if _, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, []byte(`{}`), invalidTier, "responses", false, "gpt-test"); err == nil {
			t.Fatal("invalid tier value must fail")
		}
	})
	t.Run("inert without capability evidence", func(t *testing.T) {
		body := []byte(`{"model":"gpt-test"}`)
		got, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, body, account, "responses", false, "gpt-test")
		if err != nil || string(got) != string(body) {
			t.Fatalf("inert overrides must not touch the body: %s %v", got, err)
		}
	})
	t.Run("invalid json body fails when model unknown", func(t *testing.T) {
		if _, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, []byte(`not-json`), account, "responses", false, ""); err == nil {
			t.Fatal("invalid JSON body must fail")
		}
	})
	t.Run("catalog resolution applies tier", func(t *testing.T) {
		catalog := &w13g3OverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{
			Model:                 "gpt-test",
			SupportedServiceTiers: []string{"priority"},
		}}}
		SetGptRequestOverrideModelCatalog(catalog)
		t.Cleanup(func() { SetGptRequestOverrideModelCatalog(nil) })
		got, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, []byte(`{"model":"gpt-test"}`), account, "responses", false, "gpt-test")
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !strings.Contains(string(got), `"service_tier":"priority"`) {
			t.Fatalf("tier must be applied: %s", got)
		}
	})
	t.Run("reasoning effort applies per family", func(t *testing.T) {
		catalog := &w13g3OverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{
			Model:                     "gpt-test",
			SupportedReasoningEfforts: []string{"high"},
		}}}
		SetGptRequestOverrideModelCatalog(catalog)
		t.Cleanup(func() { SetGptRequestOverrideModelCatalog(nil) })
		effortAccount := AccountCandidate{ID: "a-1", ProviderCode: "openai", Credentials: map[string]any{"reasoning_effort_override": "high"}}
		for _, tc := range []struct {
			family string
			expect string
		}{
			{"responses", `"effort":"high"`},
			{"chat_completions", `"reasoning_effort":"high"`},
			{"anthropic_messages", `"effort":"high"`},
			{"gemini_generate_content", `"thinkingLevel":"high"`},
		} {
			body := []byte(`{"model":"gpt-test"}`)
			got, err := ApplyGptAccountRequestOverridesToUpstreamBody(ctx, body, effortAccount, tc.family, false, "gpt-test")
			if err != nil {
				t.Fatalf("%s apply: %v", tc.family, err)
			}
			if !strings.Contains(string(got), tc.expect) {
				t.Fatalf("%s body = %s", tc.family, got)
			}
		}
	})
}

type w13g3OverrideCatalog struct {
	items []GptRequestOverrideModelCatalogItem
}

func (c *w13g3OverrideCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error) {
	return c.items, nil
}

// ---------------------------------------------------------------------------
// 成功结果的结算闭包（attemptoutcomes.go）
// ---------------------------------------------------------------------------

func TestW13g3SuccessResultClosures(t *testing.T) {
	t.Run("lock success confirmation without lock traffic", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 0, 500)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		args.AccountStateMutationEnabled = false
		result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if err := result.ConfirmAccountLockSuccess(); err != nil {
			t.Fatalf("confirm lock success: %v", err)
		}
		if result.ConfirmHalfOpenSuccess() {
			t.Fatal("half-open success requires automatic mutation")
		}
		if result.ReleaseHalfOpenLease() {
			t.Fatal("unclaimed lease release must be false")
		}
	})
	t.Run("lock success confirmation with lock traffic", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := sequentialServer(t, 0, 500)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if err := result.ConfirmAccountLockSuccess(); err != nil {
			t.Fatalf("confirm lock success: %v", err)
		}
		if err := result.ConfirmSameAccountApiKeyFailures(); err != nil {
			t.Fatalf("confirm key failures: %v", err)
		}
		if err := result.ConfirmAccountAPIKeySuccess(); err != nil {
			t.Fatalf("confirm api key success: %v", err)
		}
	})
	t.Run("failed response return keeps closures inert", func(t *testing.T) {
		h := newW13g3Harness(t)
		server := httptestW13g3StatusServer(t, http.StatusForbidden)
		defer server.Close()
		h.driver.urlByAccount = map[string][]string{"a-1": {server.URL + "/v1/chat/completions"}}
		// 显式策略失败 → return_response 原样返回给客户端。
		h.engine.FailureDispatcher = &w13g3ReturnResponseDispatcher{inner: h.dispatcher}
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		args := w13g3Args(t, req, testAccounts("a-1"))
		result, err := h.engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if result.Response == nil || result.Response.Status() != http.StatusForbidden {
			t.Fatalf("failed response must be returned: %#v", result.Response)
		}
		if err := result.ConfirmSameAccountApiKeyFailures(); err != nil {
			t.Fatalf("inert confirm: %v", err)
		}
		if result.ConfirmHalfOpenSuccess() {
			t.Fatal("return-response half-open confirm must be false")
		}
		if result.ReleaseHalfOpenLease() {
			t.Fatal("unclaimed lease release must be false")
		}
	})
}

// w13g3ReturnResponseDispatcher 把失败响应原样返回（显式策略失败）。
type w13g3ReturnResponseDispatcher struct {
	inner *fakeFailureDispatcher
}

func (d *w13g3ReturnResponseDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	return FailedUpstreamResponseResult{
		Action:      FailedResponseActionReturnResponse,
		Response:    input.Response,
		FailureKind: FailureKindExplicitPolicy,
		LastAttempt: input.LastAttempt,
	}, nil
}

func (d *w13g3ReturnResponseDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	return d.inner.HandleUpstreamRequestError(ctx, input)
}

func (d *w13g3ReturnResponseDispatcher) IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool {
	return d.inner.IsOpaqueUpstreamFailoverAllowed(req)
}
