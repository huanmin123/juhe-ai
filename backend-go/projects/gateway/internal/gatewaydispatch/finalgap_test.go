package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 最后一批定向覆盖：codex 归一化校验/reasoning include、gemini 端点族、
// 首块读取的墙钟归因（脚本化时钟）与上游失败处理的取消耗键。

func TestValidateOpenAIOAuthCodexBodyVariants(t *testing.T) {
	// model 缺失。
	if err := validateOpenAIOAuthCodexBody(map[string]any{}, false); err == nil {
		t.Fatal("缺 model 必须报错")
	}
	// compact 时不要求 input。
	if err := validateOpenAIOAuthCodexBody(map[string]any{"model": "gpt-test"}, true); err != nil {
		t.Fatalf("compact 不要求 input: %v", err)
	}
	// 缺 input。
	if err := validateOpenAIOAuthCodexBody(map[string]any{"model": "gpt-test"}, false); err == nil {
		t.Fatal("缺 input 必须报错")
	}
	// input 字符串/数组合法。
	if err := validateOpenAIOAuthCodexBody(map[string]any{"model": "gpt-test", "input": "hi"}, false); err != nil {
		t.Fatalf("字符串 input: %v", err)
	}
	if err := validateOpenAIOAuthCodexBody(map[string]any{"model": "gpt-test", "input": []any{1}}, false); err != nil {
		t.Fatalf("数组 input: %v", err)
	}
	// input 其他类型非法。
	if err := validateOpenAIOAuthCodexBody(map[string]any{"model": "gpt-test", "input": 7}, false); err == nil {
		t.Fatal("数字 input 必须报错")
	}
}

func TestEnsureOpenAIOAuthCodexReasoningInclude(t *testing.T) {
	// 无 reasoning 不处理。
	body := map[string]any{}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	if _, ok := body["include"]; ok {
		t.Fatal("无 reasoning 不注入 include")
	}
	// 空 reasoning 不处理。
	body = map[string]any{"reasoning": map[string]any{}}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	if _, ok := body["include"]; ok {
		t.Fatal("空 reasoning 不注入 include")
	}
	// include 为 nil → 新列表。
	body = map[string]any{"reasoning": map[string]any{"effort": "high"}, "include": nil}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	items := body["include"].([]any)
	if len(items) != 1 || items[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", items)
	}
	// include 已有内容且缺失 → 追加。
	body = map[string]any{"reasoning": map[string]any{"effort": "high"}, "include": []any{"other"}}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	items = body["include"].([]any)
	if len(items) != 2 || items[1] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", items)
	}
	// 已包含 → 不重复。
	body = map[string]any{"reasoning": map[string]any{"effort": "high"}, "include": []any{"reasoning.encrypted_content"}}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	if got := len(body["include"].([]any)); got != 1 {
		t.Fatalf("不应重复追加, got %d", got)
	}
	// include 已包含（切片展开路径）→ 不重复。
	body = map[string]any{"reasoning": map[string]any{"effort": "high"}, "include": []any{"reasoning.encrypted_content", "other"}}
	ensureOpenAIOAuthCodexReasoningInclude(body)
	items = body["include"].([]any)
	if got := len(items); got != 2 {
		t.Fatalf("include = %#v", items)
	}
}

func TestNormalizeOpenAIOAuthCodexParsedBodyCompactWithSanitize(t *testing.T) {
	previous := gatewayoauthcodex.SanitizeCodexHistory
	gatewayoauthcodex.SanitizeCodexHistory = func(items []any, options SanitizeCodexHistoryOptions) CodexHistorySanitizeResult {
		return CodexHistorySanitizeResult{Items: []any{"compact-sanitized"}, Changed: true}
	}
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = previous })
	input := OpenAIOAuthCodexNormalizeInput{
		Account:              OpenAIOAuthCodexAccount{ID: "acc-9"},
		Compact:              true,
		SanitizeCodexHistory: true,
		ModelOverride:        "gpt-override",
		InputHeaders:         http.Header{"Session-Id": []string{"sess-1"}},
	}
	result, err := NormalizeOpenAIOAuthCodexParsedBody(map[string]any{"model": "gpt-test", "input": []any{"x"}}, input)
	if err != nil {
		t.Fatalf("compact normalize: %v", err)
	}
	if !result.CodexHistorySanitized || result.BodyBytes == nil {
		t.Fatalf("result = %#v", result)
	}
	if !containsBytes(result.BodyBytes, "compact-sanitized") || !containsBytes(result.BodyBytes, "gpt-override") {
		t.Fatalf("body = %s", result.BodyBytes)
	}
	if result.Stream {
		t.Fatal("compact 结果非流式")
	}
}

func containsBytes(haystack []byte, needle string) bool {
	return len(needle) == 0 || indexOfBytes(haystack, needle) >= 0
}

func indexOfBytes(haystack []byte, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestGeminiRequestEndpointFamily(t *testing.T) {
	// generateContent 动作端点。
	generate := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2:generateContent", nil))
	if family := geminiRequestEndpointFamilyOf(generate); family != "generate_content" {
		t.Fatalf("family = %q", family)
	}
	// models 列表端点不算族。
	models := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1beta/models", nil))
	if family := geminiRequestEndpointFamilyOf(models); family != "" {
		t.Fatalf("models family = %q", family)
	}
}

// ---------------------------------------------------------------------------
// 首块读取：脚本化时钟下的墙钟归因
// ---------------------------------------------------------------------------

func TestReadFirstNonStreamChunkPrecommitSettledAfterDeadline(t *testing.T) {
	// 脚本时钟：前两次 now=base（计算截止），读结算时 now=base+20（晚于截止）。
	base := int64(100_000)
	started := time.Now()
	// 前 5ms 返回 base（截止=base+5），读结算时（~10ms）返回 base+20 → 结算晚于截止。
	injectNowMs(t, func() int64 {
		if time.Since(started) < 5*time.Millisecond {
			return base
		}
		return base + 20
	})
	reader := &sleepSettleReader{}
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-1_000, firstByteDeadlineReadInput{
		StartedAt:                     base - 1_000,
		Signal:                        context.Background(),
		ResponsePrecommitDeadlineAtMs: ptrInt64(base + 5),
		PendingReadSupersedesDeadline: true,
	})
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(err, &deadlineErr) || deadlineErr.DeadlineAtMs != base+5 {
		t.Fatalf("err = %v", err)
	}
}

// sleepSettleReader 延迟 10ms 后返回数据（先于 20ms 的墙钟计时器，
// 但结算时刻晚于墙钟截止）。
type sleepSettleReader struct {
	done bool
}

func (s *sleepSettleReader) Read(buffer []byte) (int, error) {
	if s.done {
		return 0, io.EOF
	}
	s.done = true
	time.Sleep(10 * time.Millisecond)
	copy(buffer, "abc")
	return 3, nil
}

func TestReadFirstNonStreamChunkMaxLifetimeRace(t *testing.T) {
	base := int64(200_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-1_000, firstByteDeadlineReadInput{
		StartedAt:             base - 1_000,
		Signal:                context.Background(),
		MaxLifetimeDeadlineAt: ptrInt64(base + 1),
		MaxLifetimeMs:         ptrInt64(2_000),
	})
	var lifetimeErr *UpstreamBodyReadMaxLifetimeError
	if !errorsAs(err, &lifetimeErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFirstNonStreamChunkRacePrecommitAttribution(t *testing.T) {
	base := int64(300_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-1_000, firstByteDeadlineReadInput{
		StartedAt:                     base - 1_000,
		Signal:                        context.Background(),
		ResponsePrecommitDeadlineAtMs: ptrInt64(base + 1),
	})
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(err, &deadlineErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFirstNonStreamChunkSoftDeadlineContinue(t *testing.T) {
	base := int64(400_000)
	injectNowMs(t, func() int64 { return base })
	// 软截止先触发 + handler continue：循环重进后读完成 → 正常读出。
	reader := &sleepSettleReader{}
	read, observed, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-1_000, firstByteDeadlineReadInput{
		StartedAt:           base - 1_000,
		Signal:              context.Background(),
		FirstByteDeadlineMs: ptrInt64(1_000),
		OnFirstByteDeadline: func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionContinue
		},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.N != 3 || !observed {
		t.Fatalf("read = %#v observed=%v", read, observed)
	}
}

// ---------------------------------------------------------------------------
// 上游尝试循环的取消耗键（请求级换 Key 消耗请求尝试计数）
// ---------------------------------------------------------------------------

func TestDispatchTryNextKeyConsumesRequestAttemptCount(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &tryNextKeyDispatcher{}
	engine.Config.AccountApiKeyRequestAttemptSafetyLimit = 2
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b", "key-c")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 请求级安全上限=2：第二把 Key 后预算耗尽（是否以去重或池穷尽收尾均为合法终态）。
	if attemptErr.LastAttempt == nil {
		t.Fatal("最后尝试必须保留")
	}
}

// TestFetchWallBudgetCoordinationErrorText: 协调预算错误文案与代码。
func TestFetchWallBudgetCoordinationErrorText(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	harness.input.signal = func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}()
	abortErr := &UpstreamRequestAbortedError{Message: "请求已取消"}
	_, kind, stop, err := harness.errorContext(testAccounts("a-1")[0], abortErr, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if kind != errorKindRethrow || stop.rethrown != abortErr {
		t.Fatalf("kind=%v rethrown=%v", kind, stop.rethrown)
	}
}

func TestReadFirstNonStreamChunkPrecommitAfterMaxLifetime(t *testing.T) {
	// precommit 与 maxLifetime 均已过期且 maxLifetime 更早 → 上限错误归因。
	base := int64(500_000)
	injectNowMs(t, func() int64 { return base })
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	_, _, err := readFirstNonStreamChunkWithDeadlines(reader, make([]byte, 8), base-1_000, firstByteDeadlineReadInput{
		StartedAt:                     base - 1_000,
		Signal:                        context.Background(),
		ResponsePrecommitDeadlineAtMs: ptrInt64(base - 50),
		MaxLifetimeDeadlineAt:         ptrInt64(base - 100),
		MaxLifetimeMs:                 ptrInt64(4_000),
	})
	var lifetimeErr *UpstreamBodyReadMaxLifetimeError
	if !errorsAs(err, &lifetimeErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestRaceReadWithDeadlinesNilSignalSoftOnly(t *testing.T) {
	reader := newBlockingReader()
	t.Cleanup(reader.close)
	pendingRead := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		buffer := make([]byte, 8)
		n, err := reader.Read(buffer)
		return chunkResult{N: n, Err: err}, err
	})
	raceType, _, _ := raceReadWithDeadlines(pendingRead, nil, ptrInt64(-1), nil, nil, nil)
	if raceType != raceSoftTimeout {
		t.Fatalf("raceType = %v", raceType)
	}
}

// leaseSuppression 在账户级过滤时发放 half-open 租约。
type leaseSuppression struct {
	lease    fakeHalfOpenLease
	released *bool
}

func (l leaseSuppression) FilterAsync(_ context.Context, accounts []AccountCandidate, options SuppressionFilterOptions) (SuppressionFilterResult, error) {
	if !options.AcquireHalfOpenLease {
		return localSuppressionBypassResult(accounts), nil
	}
	release := func() (bool, error) {
		if l.released != nil {
			*l.released = true
		}
		return true, nil
	}
	lease := countedHalfOpenLease{generation: nil, releaseFn: release}
	result := localSuppressionBypassResult(accounts)
	result.AcquiredHalfOpenLeases = []HalfOpenLease{lease}
	return result, nil
}

func (l leaseSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// countedHalfOpenLease 用注入的释放函数实现租约。
type countedHalfOpenLease struct {
	generation *int64
	releaseFn  func() (bool, error)
}

func (c countedHalfOpenLease) RuntimeKey() string             { return "rk:lease" }
func (c countedHalfOpenLease) Generation() *int64             { return c.generation }
func (c countedHalfOpenLease) Release() (bool, error)         { return c.releaseFn() }
func (c countedHalfOpenLease) CompleteSuccess() (bool, error) { return true, nil }

// TestDispatchReturnResponseReleasesHalfOpenLease: 账户级过滤发放的 half-open
// 租约经结果闭包释放。
func TestDispatchReturnResponseReleasesHalfOpenLease(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &returnResponseDispatcher{}
	released := false
	engine.Suppression = leaseSuppression{released: &released}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.AllowPrecheckHalfOpen = false
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !result.ReleaseHalfOpenLease() {
		t.Fatal("租约释放应返回 true")
	}
	if !released {
		t.Fatal("租约释放函数未被调用")
	}
}
