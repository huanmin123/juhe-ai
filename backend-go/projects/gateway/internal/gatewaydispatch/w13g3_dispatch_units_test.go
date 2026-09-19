package gatewaydispatch

// w13g3 第三轮：handleUpstreamAttemptError 直测补支（不安全 URL 账户标记、
// 失败处理器错误、中止记录错误、未知动作落点）、API Key 轮换策略选择、
// rolling capture 压缩、reader 关闭、模型能力解析、映射过滤、OAuth Codex
// 请求体解析、首字节读后决策、高并发排队收尾分支。全部 fake 驱动。
//
// 追加不可达登记：
//   - preparation.go:714-716 clientIpConcurrencyAuditMetadata 的
//     !decision.Enabled 分支：唯一调用点 preparation.go:600 已用
//     clientIpConcurrency.Enabled 守卫，恒传 Enabled=true。
//   - preparation.go:635-637 排队等待的 AvailableDecisionMs 错误：不传
//     FinalResponseReserveMs，规范化不会报错。

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// handleUpstreamAttemptError 直测补支
// ---------------------------------------------------------------------------

// w13g3ErrorDispatcher 在请求错误处理时注入失败。
type w13g3ErrorDispatcher struct {
	inner        *fakeFailureDispatcher
	requestErr   error
	resultAction string
}

func (d *w13g3ErrorDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	return d.inner.HandleFailedUpstreamResponse(ctx, input)
}

func (d *w13g3ErrorDispatcher) HandleUpstreamRequestError(ctx context.Context, input UpstreamRequestErrorInput) (UpstreamRequestErrorResult, error) {
	if d.requestErr != nil {
		return UpstreamRequestErrorResult{}, d.requestErr
	}
	result, err := d.inner.HandleUpstreamRequestError(ctx, input)
	if err != nil {
		return result, err
	}
	if d.resultAction != "" {
		result.Action = d.resultAction
	}
	return result, nil
}

func (d *w13g3ErrorDispatcher) IsOpaqueUpstreamFailoverAllowed(req *gatewaypreauth.GatewayRequest) bool {
	return d.inner.IsOpaqueUpstreamFailoverAllowed(req)
}

// 原 TestW13g3AttemptErrorUnsafeURLMarksAccount 断言的账户临时不可用标记
// 已随 engine.AccountState 端口删除（生产组合根按设计不装配，分支受 nil
// 守卫从未执行）；本用例改断言当前语义：unsafe URL 直接 rethrow。
func TestW13g3AttemptErrorUnsafeURLRethrows(t *testing.T) {
	h := newAttemptErrorHarness(t)
	failure := &UnsafeResolvedUpstreamURLError{Message: "unsafe dns"}
	_, kind, stop, err := h.errorContext(testAccounts("a-1")[0], failure, nil)
	if kind != errorKindRethrow || stop.rethrown != failure || err != nil {
		t.Fatalf("unsafe url must rethrow: kind=%v stop=%v err=%v", kind, stop, err)
	}
}

func TestW13g3AttemptErrorTransportHandlerFailures(t *testing.T) {
	proven := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}

	t.Run("handler error surfaces", func(t *testing.T) {
		h := newAttemptErrorHarness(t)
		h.engine.FailureDispatcher = &w13g3ErrorDispatcher{inner: h.engine.FailureDispatcher.(*fakeFailureDispatcher), requestErr: errW13g3}
		_, kind, _, err := h.errorContext(testAccounts("a-1")[0], proven, nil)
		if kind != errorKindHandled || !errors.Is(err, errW13g3) {
			t.Fatalf("expected handled handler error, got kind=%v err=%v", kind, err)
		}
	})

	t.Run("unknown action ends with skip", func(t *testing.T) {
		h := newAttemptErrorHarness(t)
		h.engine.FailureDispatcher = &w13g3ErrorDispatcher{inner: h.engine.FailureDispatcher.(*fakeFailureDispatcher), resultAction: ""}
		_, kind, stop, err := h.errorContext(testAccounts("a-1")[0], proven, nil)
		if kind != errorKindHandled || err != nil || stop.kind != errorStopSkipAccount {
			t.Fatalf("expected skip-account stop, got kind=%v stop=%v err=%v", kind, stop, err)
		}
	})

	t.Run("abort record handler error surfaces", func(t *testing.T) {
		h := newAttemptErrorHarness(t)
		h.engine.FailureDispatcher = &w13g3ErrorDispatcher{inner: h.engine.FailureDispatcher.(*fakeFailureDispatcher), requestErr: errW13g3}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		failure := &UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true}
		_, kind, _, err := h.errorContext(testAccounts("a-1")[0], failure, func(ctx *upstreamAttemptErrorContext) {
			ctx.loop.signal = canceled
		})
		if kind != errorKindHandled || !errors.Is(err, errW13g3) {
			t.Fatalf("expected abort record error, got kind=%v err=%v", kind, err)
		}
	})
}

// ---------------------------------------------------------------------------
// API Key 轮换策略选择
// ---------------------------------------------------------------------------

func TestW13g3ApiKeyRotationStrategySelection(t *testing.T) {
	entries := []apiKeyEntry{
		{key: "k1", fingerprint: "fp1", index: 0, weight: 3},
		{key: "k2", fingerprint: "fp2", index: 1, weight: 1},
	}
	strategy := map[string]any{"rotation_strategy": "weighted_round_robin"}

	t.Run("weighted selection lands by weight cursor", func(t *testing.T) {
		counter := &w13g3Counter{index: 0}
		selected, err := selectWeightedAPIKeyEntry(context.Background(), counter, "a-1", entries)
		if err != nil || selected.fingerprint != "fp1" {
			t.Fatalf("selected = %#v err = %v", selected, err)
		}
		counter.index = 3
		selected, err = selectWeightedAPIKeyEntry(context.Background(), counter, "a-1", entries)
		if err != nil || selected.fingerprint != "fp2" {
			t.Fatalf("selected = %#v err = %v", selected, err)
		}
	})

	t.Run("counter error surfaces", func(t *testing.T) {
		counter := &w13g3Counter{err: errW13g3}
		if _, err := selectWeightedAPIKeyEntry(context.Background(), counter, "a-1", entries); !errors.Is(err, errW13g3) {
			t.Fatalf("expected counter error, got %v", err)
		}
	})

	t.Run("runtime selection filters and recovers", func(t *testing.T) {
		cooldown := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		input := apiKeySelectionInput{
			AccountID:   "a-1",
			entries:     entries,
			credentials: strategy,
			RuntimeStates: []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
				{Fingerprint: "fp2", Disabled: true},
			},
			ExcludeFingerprints: map[string]struct{}{"fp1": {}},
		}
		// 全部候选被排除/禁用 → 无选择。
		selected, err := selectAccountRuntimeApiKeyEntry(context.Background(), input)
		if err != nil || selected != nil {
			t.Fatalf("selected = %#v err = %v", selected, err)
		}
		// 冷却中的候选作为 recovery 兜底：fp2 被禁用、fp1 冷却且未被排除。
		input.RuntimeStates = []gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
			{Fingerprint: "fp1", CooldownUntil: &cooldown},
			{Fingerprint: "fp2", Disabled: true},
		}
		input.ExcludeFingerprints = nil
		selected, err = selectAccountRuntimeApiKeyEntry(context.Background(), input)
		if err != nil || selected == nil || selected.fingerprint != "fp1" {
			t.Fatalf("recovery selection = %#v err = %v", selected, err)
		}
		// continuation 环回到下一把候选。
		input.RuntimeStates = nil
		input.ExcludeFingerprints = map[string]struct{}{"fp2": {}}
		input.ContinueAfterFingerprint = "fp2"
		selected, err = selectAccountRuntimeApiKeyEntry(context.Background(), input)
		if err != nil || selected == nil || selected.fingerprint != "fp1" {
			t.Fatalf("continuation selection = %#v err = %v", selected, err)
		}
	})
}

// w13g3Counter 是确定性的轮换计数器。
type w13g3Counter struct {
	index int
	err   error
}

func (c *w13g3Counter) NextIndex(ctx context.Context, accountID, scope string, total int) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.index, nil
}

// ---------------------------------------------------------------------------
// capture / reader / 模型能力 / 映射过滤 单元
// ---------------------------------------------------------------------------

func TestW13g3RollingCaptureCompaction(t *testing.T) {
	capture := &rollingBufferCapture{Limit: 1 << 20}
	capture.Push([]byte("ab"))
	if capture.HeadIndex != 0 {
		t.Fatal("compact at head 0 must be a no-op")
	}
	// headIndex 越界 → 清空。
	empty := &rollingBufferCapture{Chunks: nil, HeadIndex: 3, Limit: 10}
	empty.CompactConsumedChunks()
	if empty.Chunks != nil || empty.HeadIndex != 0 {
		t.Fatalf("out-of-range compaction = %+v", empty)
	}
	// 大头索引 → 截断复制。
	big := &rollingBufferCapture{Limit: 1 << 20}
	for i := 0; i < 80; i++ {
		big.Push([]byte("x"))
	}
	big.HeadIndex = 70
	big.CompactConsumedChunks()
	if big.HeadIndex != 0 || len(big.Chunks) != 10 {
		t.Fatalf("compaction = head %d chunks %d", big.HeadIndex, len(big.Chunks))
	}
}

func TestW13g3CloseReaderVariants(t *testing.T) {
	// strings.Reader 有 Read 无 Close → 非 Closer 分支；自定义实现走 Close。
	if err := closeReader((io.Reader)(strings.NewReader("x"))); err != nil {
		t.Fatalf("non-closer reader: %v", err)
	}
	reader := &w13g3ClosableReader{}
	if err := closeReader(reader); err != nil || !reader.closed {
		t.Fatalf("closer = closed %v err %v", reader.closed, err)
	}
}

type w13g3ClosableReader struct{ closed bool }

func (r *w13g3ClosableReader) Read(p []byte) (int, error) { return 0, io.EOF }
func (r *w13g3ClosableReader) Close() error               { r.closed = true; return nil }

func TestW13g3KeyModelCapabilityForRoute(t *testing.T) {
	revision := int64(2)
	fingerprint := "fp-1"
	t.Run("requires fingerprint and revision", func(t *testing.T) {
		if gatewayKeyModelCapabilityForRoute(AccountCandidate{DispatchRevision: &revision}, "m", "chat_completions", false) != nil {
			t.Fatal("missing fingerprint must disable the capability")
		}
		if gatewayKeyModelCapabilityForRoute(AccountCandidate{SelectedAPIKeyFingerprint: &fingerprint}, "m", "chat_completions", false) != nil {
			t.Fatal("missing revision must disable the capability")
		}
	})
	t.Run("direct route resolves upstream mode", func(t *testing.T) {
		capability := gatewayKeyModelCapabilityForRoute(AccountCandidate{
			SelectedAPIKeyFingerprint: &fingerprint,
			DispatchRevision:          &revision,
		}, "gpt-test", "chat_completions", true)
		if capability == nil || capability.FinalUpstreamModel != "gpt-test" || capability.KeyFingerprint != "fp-1" || capability.DispatchRevision != 2 {
			t.Fatalf("capability = %#v", capability)
		}
	})
	t.Run("mapping rewrites model and family", func(t *testing.T) {
		account := AccountCandidate{
			ProviderCode:              "openai",
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			SelectedAPIKeyFingerprint: &fingerprint,
			DispatchRevision:          &revision,
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   "responses",
				UpstreamModel:          "gpt-upstream",
				UpstreamEndpointFamily: "chat_completions",
				Enabled:                true,
			}},
		}
		capability := gatewayKeyModelCapabilityForRoute(account, "gpt-test", "responses", true)
		if capability == nil || capability.FinalUpstreamModel != "gpt-upstream" || capability.UpstreamEndpointMode == "" {
			t.Fatalf("capability = %#v", capability)
		}
		if gatewayKeyModelCapabilityForRoute(account, "gpt-test", "unknown_family", true) != nil {
			t.Fatal("unknown upstream family must disable the capability")
		}
	})
	t.Run("resolve disables without model or fingerprint", func(t *testing.T) {
		if ResolveGatewayKeyModelAttemptCapability(newTestRequest(t, `{"model":"gpt-test"}`), testAccounts("a-1")[0]) != nil {
			t.Fatal("accounts without selected keys must stay disabled")
		}
	})
}

func TestW13g3MappingAllowedBySupportedModels(t *testing.T) {
	if isMappingAllowedBySupportedModels("m", nil) {
		t.Fatal("unsupported mapping with no model evidence must be rejected")
	}
	if !isMappingAllowedBySupportedModels("m", []string{"m"}) {
		t.Fatal("supported upstream model must be allowed")
	}
	if isMappingAllowedBySupportedModels("m", []string{"other"}) {
		t.Fatal("unlisted upstream model must be rejected")
	}
}

func TestW13g3AfterDeadlineDecisionBranches(t *testing.T) {
	input := firstByteDeadlineReadInput{StartedAt: gatewayupstream.NowMs(), FirstByteDeadlineMs: ptrInt64(1_000)}
	t.Run("superseded pending read returns raw chunk", func(t *testing.T) {
		superseded := false
		local := input
		local.PendingReadSupersedesDeadline = true
		local.OnFirstByteDeadlineSuperseded = func() { superseded = true }
		chunk, observed, err := firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, Read: chunkResult{N: 3}}, false, local)
		if err != nil || chunk.N != 3 || observed || !superseded {
			t.Fatalf("chunk=%+v observed=%v superseded=%v err=%v", chunk, observed, superseded, err)
		}
		eof, _, err := firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, Read: chunkResult{Err: io.EOF}}, false, local)
		if err != nil || !eof.Done {
			t.Fatalf("eof chunk = %+v err = %v", eof, err)
		}
	})
	t.Run("decision error and abort", func(t *testing.T) {
		chunk, _, err := firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, DecisionErr: errW13g3}, true, input)
		if !errors.Is(err, errW13g3) || chunk.N != 0 {
			t.Fatalf("decision error = %+v %v", chunk, err)
		}
		_, _, err = firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, Action: FirstByteDeadlineActionAbort}, true, input)
		var timeoutErr *GatewayFirstByteTimeoutError
		if !errorsAs(err, &timeoutErr) || timeoutErr.Source != FirstByteTimeoutSourceConfiguredDeadline {
			t.Fatalf("expected configured deadline timeout, got %v", err)
		}
	})
	t.Run("read result and eof without supersede", func(t *testing.T) {
		chunk, _, err := firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, Read: chunkResult{N: 5}}, true, input)
		if err != nil || chunk.N != 5 {
			t.Fatalf("read chunk = %+v err = %v", chunk, err)
		}
		eof, _, err := firstNonStreamReadAfterDeadlineDecision(deadlineDecision{HasRead: true, Read: chunkResult{Err: io.EOF}}, true, input)
		if err != nil || !eof.Done {
			t.Fatalf("eof = %+v err = %v", eof, err)
		}
	})
}

// ---------------------------------------------------------------------------
// OAuth Codex 请求体解析
// ---------------------------------------------------------------------------

func TestW13g3ParseCodexJsonObjectBody(t *testing.T) {
	t.Run("non-object parsed body passes through", func(t *testing.T) {
		req := newTestRequest(t, `{"model":"gpt-test"}`)
		req.Body.Body = []any{"a"}
		parsed, err := parseOpenAIOAuthCodexJsonObjectBody(req)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, ok := parsed.([]any); !ok {
			t.Fatalf("parsed = %#v", parsed)
		}
	})
	t.Run("invalid json state fails", func(t *testing.T) {
		req := newTestRequest(t, `{"model":"gpt-test"}`)
		req.Body.Body = nil
		req.Body.State = &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusInvalidJSON}
		parsed, err := parseOpenAIOAuthCodexJsonObjectBody(req)
		var adapterErr *OpenAIOAuthCodexAdapterError
		if parsed != nil || !errorsAs(err, &adapterErr) {
			t.Fatalf("expected adapter error, got %v %#v", err, parsed)
		}
	})
	t.Run("raw body invalid json fails", func(t *testing.T) {
		req := newTestRequest(t, `{"model":"gpt-test"}`)
		req.Body.Body = nil
		req.Body.State = nil
		req.Body.RawBody = []byte("not json")
		parsed, err := parseOpenAIOAuthCodexJsonObjectBody(req)
		var adapterErr *OpenAIOAuthCodexAdapterError
		if parsed != nil || !errorsAs(err, &adapterErr) {
			t.Fatalf("expected adapter error, got %v %#v", err, parsed)
		}
	})
	t.Run("raw body non-object passes through", func(t *testing.T) {
		req := newTestRequest(t, `{"model":"gpt-test"}`)
		req.Body.Body = nil
		req.Body.State = nil
		req.Body.RawBody = []byte(`[1,2]`)
		parsed, err := parseOpenAIOAuthCodexJsonObjectBody(req)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, ok := parsed.([]any); !ok {
			t.Fatalf("parsed = %#v", parsed)
		}
	})
	t.Run("empty body yields empty object", func(t *testing.T) {
		req := newTestRequest(t, `{"model":"gpt-test"}`)
		req.Body.Body = nil
		req.Body.State = nil
		req.Body.RawBody = nil
		parsed, err := parseOpenAIOAuthCodexJsonObjectBody(req)
		if err != nil || parsed == nil {
			t.Fatalf("empty body = %#v err %v", parsed, err)
		}
	})
}
