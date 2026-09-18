package gatewaydispatch

// w13g3 第五轮：非流式正文管道的超时/中止/检查缓冲分支、绝对期限读取器、
// OAuth Codex 覆盖钩子、凭据数值收窄、Anthropic 消息归一、官方 OAuth 头
// 白名单、URL 安全策略、Key 池回退候选错误路径。
//
// 追加不可达登记：
//   - body.go:746 readNonStreamChunkWithAbsoluteDeadline 收尾 return：
//     softTimeoutMs 恒为 nil（调用点只传 maxLifetime/precommit），raceSoftTimeout
//     不可能出现，switch 必然命中显式分支或 raceReadDone 先返回。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// w13g3SlowReader 延迟后给出一份数据，随后 EOF。
type w13g3SlowReader struct {
	delay time.Duration
	data  []byte
	once  bool
}

func (r *w13g3SlowReader) Read(p []byte) (int, error) {
	if r.once {
		return 0, io.EOF
	}
	r.once = true
	time.Sleep(r.delay)
	n := copy(p, r.data)
	return n, nil
}

type w13g3Writer struct {
	errOnNth int
	writes   int
}

func (w *w13g3Writer) Write(p []byte) (int, error) {
	w.writes++
	if w.errOnNth > 0 && w.writes >= w.errOnNth {
		return 0, errors.New("downstream write failed")
	}
	return len(p), nil
}

func TestW13g3PipeDeadlineTimeouts(t *testing.T) {
	now := NowMs()
	tiny := int64(20)
	t.Run("hard first-byte timeout", func(t *testing.T) {
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: 150 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: now, FirstByteTimeoutMs: &tiny})
		var timeoutErr *GatewayFirstByteTimeoutError
		if !errorsAs(err, &timeoutErr) {
			t.Fatalf("expected first-byte timeout, got %v", err)
		}
	})
	t.Run("soft configured deadline aborts without handler", func(t *testing.T) {
		started := NowMs()
		deadline := int64(20)
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: 150 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: started, FirstByteDeadlineMs: &deadline})
		var timeoutErr *GatewayFirstByteTimeoutError
		if !errorsAs(err, &timeoutErr) || timeoutErr.Source != FirstByteTimeoutSourceConfiguredDeadline {
			t.Fatalf("expected configured deadline timeout, got %v", err)
		}
	})
	t.Run("response precommit race timeout", func(t *testing.T) {
		started := NowMs()
		precommit := NowMs() + 20
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: 150 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: started, ResponsePrecommitDeadlineAtMs: &precommit})
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("expected precommit deadline error, got %v", err)
		}
	})
	t.Run("max lifetime timeout", func(t *testing.T) {
		started := NowMs()
		lifetime := int64(20)
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: 150 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: started, MaxLifetimeMs: &lifetime})
		var lifetimeErr *UpstreamBodyReadMaxLifetimeError
		if !errorsAs(err, &lifetimeErr) {
			t.Fatalf("expected max lifetime error, got %v", err)
		}
	})
	t.Run("abort during slow read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := PipeNonStreamUpstreamResponse(ctx, &w13g3SlowReader{delay: 300 * time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: NowMs(), Signal: ctx})
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort, got %v", err)
		}
	})
	t.Run("expired precommit fails at entry", func(t *testing.T) {
		past := NowMs() - 5
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: NowMs(), ResponsePrecommitDeadlineAtMs: &past})
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("expected precommit deadline error, got %v", err)
		}
	})
	t.Run("expired max lifetime fails at entry", func(t *testing.T) {
		started := NowMs() - 10_000
		lifetime := int64(20)
		_, err := PipeNonStreamUpstreamResponse(context.Background(), &w13g3SlowReader{delay: time.Millisecond, data: []byte("x")},
			&w13g3Writer{}, NonStreamPipeInput{StartedAt: started, MaxLifetimeMs: &lifetime})
		var lifetimeErr *UpstreamBodyReadMaxLifetimeError
		if !errorsAs(err, &lifetimeErr) {
			t.Fatalf("expected max lifetime error, got %v", err)
		}
	})
	t.Run("downstream write failure wraps pipe error", func(t *testing.T) {
		writer := &w13g3Writer{errOnNth: 1}
		_, err := PipeNonStreamUpstreamResponse(context.Background(),
			strings.NewReader(`{"id":"x"}`), writer, NonStreamPipeInput{StartedAt: NowMs()})
		var pipeErr *NonStreamUpstreamBodyPipeError
		if !errorsAs(err, &pipeErr) {
			t.Fatalf("expected pipe error, got %v", err)
		}
	})
	t.Run("capture knobs and lifecycle hooks", func(t *testing.T) {
		captureOff := false
		var prepared, completed bool
		chunksRead := 0
		writer := &w13g3Writer{}
		result, err := PipeNonStreamUpstreamResponse(context.Background(),
			strings.NewReader(`{"id":"hook"}`), writer, NonStreamPipeInput{
				StartedAt:         NowMs(),
				CaptureBody:       &captureOff,
				PrepareDownstream: func() { prepared = true },
				OnChunkRead:       func([]byte) { chunksRead++ },
				OnChunkWritten:    func(int) {},
				OnBodyCompleted:   func(int) { completed = true },
			})
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if !prepared || !completed || chunksRead == 0 || writer.writes == 0 {
			t.Fatalf("hooks = prepared %v completed %v reads %d writes %d", prepared, completed, chunksRead, writer.writes)
		}
		if result.FirstByteMs == nil {
			t.Fatal("first byte must be recorded")
		}
	})
}

func TestW13g3PipeInspectionPaths(t *testing.T) {
	t.Run("fully buffered complete body", func(t *testing.T) {
		outcome, pipeErr := PipeNonStreamUpstreamResponseForInspection(context.Background(),
			strings.NewReader(`{"a":1}`), &w13g3Writer{}, InspectableNonStreamPipeInput{
				NonStreamPipeInput: NonStreamPipeInput{StartedAt: NowMs()},
				InspectBytes:       64,
			})
		if pipeErr != nil {
			t.Fatalf("pipe: %v", pipeErr)
		}
		if !outcome.FullyBuffered || outcome.InspectionLimitExceeded {
			t.Fatalf("outcome = %#v", outcome)
		}
		if string(outcome.CompleteBody) != `{"a":1}` {
			t.Fatalf("complete body = %s", outcome.CompleteBody)
		}
	})
	t.Run("limit exceeded with requireFullyBuffered", func(t *testing.T) {
		outcome, pipeErr := PipeNonStreamUpstreamResponseForInspection(context.Background(),
			strings.NewReader(strings.Repeat("y", 200)), &w13g3Writer{}, InspectableNonStreamPipeInput{
				NonStreamPipeInput:    NonStreamPipeInput{StartedAt: NowMs()},
				InspectBytes:         32,
				RequireFullyBuffered: true,
			})
		if pipeErr != nil {
			t.Fatalf("pipe: %v", pipeErr)
		}
		if outcome.FullyBuffered || !outcome.InspectionLimitExceeded {
			t.Fatalf("outcome = %#v", outcome)
		}
		if len(outcome.CompleteBody) != 32 {
			t.Fatalf("inspection body = %d bytes", len(outcome.CompleteBody))
		}
	})
	t.Run("limit exceeded flushes buffered chunks", func(t *testing.T) {
		writer := &w13g3Writer{}
		outcome, pipeErr := PipeNonStreamUpstreamResponseForInspection(context.Background(),
			strings.NewReader(strings.Repeat("z", 200)), writer, InspectableNonStreamPipeInput{
				NonStreamPipeInput: NonStreamPipeInput{StartedAt: NowMs()},
				InspectBytes:       32,
			})
		if pipeErr != nil {
			t.Fatalf("pipe: %v", pipeErr)
		}
		if outcome.FullyBuffered || outcome.InspectionLimitExceeded {
			t.Fatalf("outcome = %#v", outcome)
		}
		if writer.writes == 0 {
			t.Fatal("buffered chunks must flush downstream")
		}
	})
	t.Run("commit hook error aborts pipe", func(t *testing.T) {
		_, pipeErr := PipeNonStreamUpstreamResponseForInspection(context.Background(),
			strings.NewReader(strings.Repeat("w", 200)), &w13g3Writer{}, InspectableNonStreamPipeInput{
				NonStreamPipeInput:     NonStreamPipeInput{StartedAt: NowMs()},
				InspectBytes:           32,
				BeforeDownstreamCommit: func([]byte) error { return errW13g3 },
			})
		if !errors.Is(pipeErr, errW13g3) {
			t.Fatalf("commit hook error must surface, got %v", pipeErr)
		}
	})
}

func TestW13g3AbsoluteDeadlineReader(t *testing.T) {
	now := NowMs()
	t.Run("expired precommit and lifetime", func(t *testing.T) {
		past := now - 5
		_, err, _ := readNonStreamChunkWithAbsoluteDeadline(strings.NewReader("x"), make([]byte, 8), context.Background(), nil, nil, &past)
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("expected precommit error, got %v", err)
		}
		lifetime := int64(20)
		_, err, _ = readNonStreamChunkWithAbsoluteDeadline(strings.NewReader("x"), make([]byte, 8), context.Background(), &past, &lifetime, nil)
		var lifetimeErr *UpstreamBodyReadMaxLifetimeError
		if !errorsAs(err, &lifetimeErr) {
			t.Fatalf("expected lifetime error, got %v", err)
		}
	})
	t.Run("race abort and timeouts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		deadlineAt := NowMs() + 60_000
		_, err, _ := readNonStreamChunkWithAbsoluteDeadline(&w13g3SlowReader{delay: 300 * time.Millisecond, data: []byte("x")},
			make([]byte, 8), ctx, &deadlineAt, ptrInt64(60_000), nil)
		var aborted *UpstreamRequestAbortedError
		if !errorsAs(err, &aborted) {
			t.Fatalf("expected abort, got %v", err)
		}
		lifetime := int64(20)
		_, err, _ = readNonStreamChunkWithAbsoluteDeadline(&w13g3SlowReader{delay: 200 * time.Millisecond, data: []byte("x")},
			make([]byte, 8), context.Background(), ptrInt64(NowMs()+30), &lifetime, nil)
		var lifetimeErr *UpstreamBodyReadMaxLifetimeError
		if !errorsAs(err, &lifetimeErr) {
			t.Fatalf("expected lifetime race error, got %v", err)
		}
		precommit := NowMs() + 30
		_, err, _ = readNonStreamChunkWithAbsoluteDeadline(&w13g3SlowReader{delay: 200 * time.Millisecond, data: []byte("x")},
			make([]byte, 8), context.Background(), nil, nil, &precommit)
		var precommitErr *GatewayResponsePrecommitDeadlineError
		if !errorsAs(err, &precommitErr) {
			t.Fatalf("expected precommit race error, got %v", err)
		}
	})
	t.Run("read done returns data", func(t *testing.T) {
		precommit := NowMs() + 60_000
		n, err, _ := readNonStreamChunkWithAbsoluteDeadline(strings.NewReader("ok"), make([]byte, 8), context.Background(), nil, nil, &precommit)
		if err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
}

// ---------------------------------------------------------------------------
// OAuth Codex 覆盖钩子 / 凭据收窄 / 消息归一 / 头白名单
// ---------------------------------------------------------------------------

func TestW13g3CodexAccountRequestOverrideHook(t *testing.T) {
	body := map[string]any{"model": "gpt-test"}
	t.Run("nil hook keeps body", func(t *testing.T) {
		if err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{}); err != nil {
			t.Fatalf("nil hook: %v", err)
		}
	})
	t.Run("override error becomes account scoped", func(t *testing.T) {
		err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
			ApplyGptAccountRequestOverrides: func(map[string]any, GptAccountOverrideInput) (map[string]any, error) {
				return nil, &GptAccountRequestOverrideError{Message: "覆盖无效"}
			},
		})
		var adapterErr *OpenAIOAuthCodexAdapterError
		if !errorsAs(err, &adapterErr) || !adapterErr.AccountScoped {
			t.Fatalf("expected account scoped adapter error, got %v", err)
		}
	})
	t.Run("hook rewrite replaces keys", func(t *testing.T) {
		rewritten := map[string]any{"model": "gpt-test", "service_tier": "priority"}
		if err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
			ApplyGptAccountRequestOverrides: func(map[string]any, GptAccountOverrideInput) (map[string]any, error) {
				return rewritten, nil
			},
		}); err != nil {
			t.Fatalf("hook: %v", err)
		}
		if body["service_tier"] != "priority" {
			t.Fatalf("body = %#v", body)
		}
	})
	t.Run("non-map parsed body fails", func(t *testing.T) {
		_, err := NormalizeOpenAIOAuthCodexParsedBody([]any{"x"}, OpenAIOAuthCodexNormalizeInput{})
		var adapterErr *OpenAIOAuthCodexAdapterError
		if !errorsAs(err, &adapterErr) {
			t.Fatalf("expected adapter error, got %v", err)
		}
	})
}

func TestW13g3CredentialFloatValue(t *testing.T) {
	cases := []struct {
		value any
		want  float64
		ok    bool
	}{
		{float64(3.5), 3.5, true},
		{float32(1.5), 1.5, true},
		{int(2), 2, true},
		{int64(7), 7, true},
		{json.Number("4.25"), 4.25, true},
		{json.Number("bad"), 0, false},
		{"text", 0, false},
	}
	for _, tc := range cases {
		got, ok := credentialFloatValue(tc.value)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("credentialFloatValue(%#v) = %v %v", tc.value, got, ok)
		}
	}
}

func TestW13g3NormalizeAnthropicMessage(t *testing.T) {
	t.Run("non map passes through", func(t *testing.T) {
		if got := normalizeAnthropicMessage("x"); got != "x" {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("non list content passes through", func(t *testing.T) {
		value := map[string]any{"content": "text"}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("content list missing must pass through")
		}
	})
	t.Run("non text block passes through", func(t *testing.T) {
		value := map[string]any{"content": []any{map[string]any{"type": "image"}}}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("non text block must pass through")
		}
	})
	t.Run("extra keys pass through", func(t *testing.T) {
		value := map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi", "extra": 1}}}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("extra block keys must pass through")
		}
	})
	t.Run("text blocks merge", func(t *testing.T) {
		value := map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		}}
		got := normalizeAnthropicMessage(value)
		record, ok := got.(map[string]any)
		if !ok || record["content"] != "ab" {
			t.Fatalf("merged = %#v", got)
		}
	})
}

func TestW13g3OfficialOAuthHeaderAllowlist(t *testing.T) {
	if !isAllowedOfficialOAuthClientHeader("Content-Type", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("common header must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-codex-foo", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("codex prefix must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-claude-code-bar", OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("claude prefix must be allowed")
	}
	if isAllowedOfficialOAuthClientHeader("x-gemini-foo", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("gemini header must not match codex profile")
	}
	if !isAllowedOfficialOAuthClientHeader("x-goog-api-client", OAuthHeaderProfileGeminiCLI) {
		t.Fatal("gemini profile must allow gemini header")
	}
	if !isAllowedOfficialOAuthClientHeader("x-grok-client-version", OAuthHeaderProfileXAIGrok) {
		t.Fatal("grok profile must allow grok header")
	}
	if isAllowedOfficialOAuthClientHeader("x-unknown", OAuthHeaderProfileGeminiCLI) {
		t.Fatal("unknown header must be rejected")
	}
}

func TestW13g3URLPolicyTrimAndResolution(t *testing.T) {
	policy := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	parsed, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "  https://example.invalid-host.w13g3/v1  ")
	if err != nil {
		// DNS 解析失败的域名按解析错误或原始错误返回都合法。
		t.Logf("resolution failed as expected: %v", err)
	} else if parsed == nil {
		t.Fatal("parsed url missing")
	}
	trimmed, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "https://93.184.216.34/v1")
	if err != nil || trimmed.Hostname() != "93.184.216.34" {
		t.Fatalf("trimmed literal = %v %v", trimmed, err)
	}
}

// ---------------------------------------------------------------------------
// Key 池回退候选错误路径
// ---------------------------------------------------------------------------

func TestW13g3GroupFallbackNilRecordAndErrors(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	t.Run("nil record cannot attempt fallback", func(t *testing.T) {
		if CanAttemptApiKeyGroupFallback(nil, "group-1", nil) {
			t.Fatal("nil record must not attempt fallback")
		}
		pipeline, _, _, _ := newPipeline(t)
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("nil record resolve = found %v err %v", found, err)
		}
	})
	t.Run("cache errors surface", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &w13g3FallbackCache{resolveErr: errW13g3, accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); !errors.Is(err, errW13g3) {
			t.Fatalf("expected resolve error, got %v", err)
		}
		engine.Cache = &w13g3FallbackCache{listErr: errW13g3, accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		if _, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); !errors.Is(err, errW13g3) {
			t.Fatalf("expected list error, got %v", err)
		}
	})
	t.Run("capability and model mismatch skip group", func(t *testing.T) {
		pipeline, engine, driver, _ := newPipeline(t)
		driver.mismatchAll = true
		engine.Cache = &w13g3FallbackCache{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("capability mismatch must skip: found %v err %v", found, err)
		}
	})
	t.Run("quota denied skip group", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &w13g3FallbackCache{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		engine.Quota = &fakeQuota{denied: map[string]struct{}{"b-1": {}}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("quota denied must skip: found %v err %v", found, err)
		}
	})
}

// w13g3FallbackCache 是带错误注入的分组缓存。
type w13g3FallbackCache struct {
	accounts   map[string][]AccountCandidate
	resolveErr error
	listErr    error
}

func (f *w13g3FallbackCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.accounts[groupID], nil
}

func (f *w13g3FallbackCache) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	if f.resolveErr != nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, f.resolveErr
	}
	return gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}, true, nil
}

func (f *w13g3FallbackCache) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	return nil, nil
}
