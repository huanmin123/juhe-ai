package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 收尾补齐：codex 归一化输入/指令、管道检查缓冲、有限读取变体、
// 确认电路下的传输失败换 Key、取消信号的容量分支。

// ---------------------------------------------------------------------------
// oauthnormalizer 输入与指令归一化
// ---------------------------------------------------------------------------

func TestNormalizeOpenAIOAuthCodexInstructions(t *testing.T) {
	body := map[string]any{}
	if err := gatewayoauthcodex.NormalizeOpenAIOAuthCodexInstructions(body); err != nil || body["instructions"] != "" {
		t.Fatalf("缺省 instructions = %#v err=%v", body["instructions"], err)
	}
	body = map[string]any{"instructions": "custom"}
	if err := gatewayoauthcodex.NormalizeOpenAIOAuthCodexInstructions(body); err != nil || body["instructions"] != "custom" {
		t.Fatal("字符串 instructions 保留")
	}
	body = map[string]any{"instructions": 42}
	if err := gatewayoauthcodex.NormalizeOpenAIOAuthCodexInstructions(body); !IsOpenAIOAuthCodexAdapterError(err) {
		t.Fatalf("expected adapter error, got %v", err)
	}
}

func TestNormalizeOpenAIOAuthCodexInputVariants(t *testing.T) {
	// 字符串输入包装为用户消息。
	body := map[string]any{"input": "hello"}
	normalizeOpenAIOAuthCodexInput(body)
	items := body["input"].([]any)
	message := items[0].(map[string]any)
	if message["role"] != "user" || message["type"] != "message" {
		t.Fatalf("message = %#v", message)
	}
	// system 角色转换为 developer 并合并 instructions。
	body = map[string]any{
		"instructions": "base",
		"input": []any{
			map[string]any{"role": "system", "content": "be nice"},
			map[string]any{"role": "user", "content": "hi"},
			"raw-item",
		},
	}
	normalizeOpenAIOAuthCodexInput(body)
	input := body["input"].([]any)
	if input[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("system → developer 失败: %#v", input[0])
	}
	if body["instructions"] != "be nice\n\nbase" {
		t.Fatalf("instructions = %q", body["instructions"])
	}
	if _, ok := input[1].(map[string]any); !ok {
		t.Fatal("非 system 项保持原样")
	}
	if input[2] != "raw-item" {
		t.Fatal("非对象项保持原样")
	}
	// 空内容 system 不产生 instructions。
	body = map[string]any{"input": []any{map[string]any{"role": "system", "content": ""}}}
	normalizeOpenAIOAuthCodexInput(body)
	if _, ok := body["instructions"]; ok {
		t.Fatal("空 system 内容不应产生 instructions")
	}
	// input 既非字符串也非数组 → 原样。
	untouched := map[string]any{"input": 7}
	normalizeOpenAIOAuthCodexInput(untouched)
	if untouched["input"] != 7 {
		t.Fatal("非字符串/数组 input 保持原样")
	}
}

// ---------------------------------------------------------------------------
// 管道检查缓冲（多块 → 缓冲 → EOF 全量提交）
// ---------------------------------------------------------------------------

type multiChunkReader struct {
	chunks [][]byte
	index  int
}

func (m *multiChunkReader) Read(buffer []byte) (int, error) {
	if m.index >= len(m.chunks) {
		return 0, io.EOF
	}
	chunk := m.chunks[m.index]
	m.index++
	copy(buffer, chunk)
	return len(chunk), nil
}

func TestPipeInspectionBuffersThenCommits(t *testing.T) {
	reader := &multiChunkReader{chunks: [][]byte{[]byte("aa"), []byte("bb"), []byte("cc")}}
	var downstream strings.Builder
	var chunkReads []string
	result, err := PipeNonStreamUpstreamResponseForInspection(context.Background(), reader, &downstream, InspectableNonStreamPipeInput{
		NonStreamPipeInput: NonStreamPipeInput{
			StartedAt:   gatewayupstream.NowMs(),
			Signal:      context.Background(),
			OnChunkRead: func(chunk []byte) { chunkReads = append(chunkReads, string(chunk)) },
		},
		InspectBytes: 64,
	})
	if err != nil {
		t.Fatalf("inspection: %v", err)
	}
	if !result.FullyBuffered {
		t.Fatal("小载荷必须全量缓冲")
	}
	if string(result.CompleteBody) != "aabbcc" {
		t.Fatalf("complete = %q", result.CompleteBody)
	}
	// 全缓冲成功时下游未被写入（调用方负责提交 CompleteBody）。
	if downstream.String() != "" {
		t.Fatalf("downstream = %q", downstream.String())
	}
	if result.CompleteBodyText == nil || *result.CompleteBodyText != "aabbcc" {
		t.Fatalf("complete text = %#v", result.CompleteBodyText)
	}
	if len(chunkReads) != 3 {
		t.Fatalf("chunk reads = %#v", chunkReads)
	}
}

// TestPipeUsageTailAndCaptureBodyDisabled: 关闭捕获时 diagnostic 仅有传输统计。
func TestPipeUsageTailAndCaptureBodyDisabled(t *testing.T) {
	disabled := false
	var downstream strings.Builder
	result, err := PipeNonStreamUpstreamResponse(context.Background(), strings.NewReader("abcdef"), &downstream, NonStreamPipeInput{
		StartedAt:      gatewayupstream.NowMs(),
		Signal:         context.Background(),
		CaptureBody:    &disabled,
		UsageTailBytes: ptrInt64(2),
		OnChunkWritten: func(int) {},
		OnBodyCompleted: func(transferred int) {
			if transferred != 6 {
				t.Fatalf("transferred = %d", transferred)
			}
		},
	})
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if result.CapturedBodyText != nil {
		t.Fatal("关闭捕获后不应有捕获文本")
	}
	if result.UsageTailText == nil || *result.UsageTailText != "ef" {
		t.Fatalf("usage tail = %#v", result.UsageTailText)
	}
}

// zeroChunkReader 先返回 0,nil 再 EOF（触发 n==0 continue 分支）。
type zeroChunkReader struct {
	zero bool
}

func (z *zeroChunkReader) Read([]byte) (int, error) {
	if !z.zero {
		z.zero = true
		return 0, nil
	}
	return 0, io.EOF
}

func TestReadUpstreamBodyLimitedVariants(t *testing.T) {
	// 零字节块跳过且无 StartedAt 时不算首字节。
	result, err := ReadUpstreamBodyLimited(context.Background(), &zeroChunkReader{}, LimitedBodyReadInput{
		Signal: context.Background(),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.FirstByteMs != nil {
		t.Fatal("无 StartedAt 不产生首字节耗时")
	}
	// closeReader 对非 Closer 的 reader 是 no-op。
	if err := closeReader(struct{ io.Reader }{}); err != nil {
		t.Fatalf("closeReader: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 确认电路下的传输失败换 Key（attemptoutcomes 确认质量分支）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 取消信号与容量分支
// ---------------------------------------------------------------------------

func TestPrepareDispatchAccountsClientIPAbortedSignal(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{}}
	engine.Affinity = &configurableAffinity{busy: false}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: true}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	input.Signal = canceled
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("取消信号应直接完成, got %s", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// 失败响应处理中取消信号 → 同账户重试等待中止
// ---------------------------------------------------------------------------

// cancelingFailureDispatcher 在失败响应处理后取消请求信号。
type cancelingFailureDispatcher struct {
	fakeFailureDispatcher
	cancel context.CancelFunc
}

func (c *cancelingFailureDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	c.cancel()
	return FailedUpstreamResponseResult{
		Action:      FailedResponseActionSkipAccount,
		LastAttempt: input.LastAttempt,
	}, nil
}

func TestReserveSameAccountRetrySecondWaitAbortedBySignal(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: true, LeaseID: "lease-1", WaitMs: 5},
	}}
	engine.Locks = locks
	ctx, cancel := context.WithCancel(context.Background())
	engine.FailureDispatcher = &cancelingFailureDispatcher{cancel: cancel}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.Signal = ctx
	_, err := engine.FetchFirstAvailableUpstream(ctx, args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 失败响应处理后取消：第二等待 aborted → 放弃预留 → 跳过账户。
	if locks.calls.Load() != 1 {
		t.Fatalf("锁租约调用 = %d", locks.calls.Load())
	}
	if locks.consumeCalls.Load() != 0 {
		t.Fatalf("中止后不应消耗租约, consume = %d", locks.consumeCalls.Load())
	}
	cancel()
}
