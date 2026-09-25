package gatewayresponse

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 2026-09-25 回归锁（排查项「流式 first-byte deadline 的 continue 路径丢
// chunk 竞态」）：deadline 到点且决策为 Continue 时，读取循环必须继续复用
// 同一个 pendingRead 桥——chunk 在 deadline 之后到达仍必须被本次/后续读取
// 拿到，不得因重建桥 goroutine 而被旧桥吞掉（SSE 缺帧）。
//
// 当前实现中 newPendingRead 位于 readNextStreamChunk 的 for 循环之外，
// continue 语义即「复用同一 pending 继续等」；本测试锁定该不变量。

type deadlineProbeBody struct {
	ch chan ChunkResult
}

func (b *deadlineProbeBody) Next() <-chan ChunkResult { return b.ch }

func (b *deadlineProbeBody) Close() {}

func TestStreamFirstByteDeadlineContinueDoesNotLoseChunk(t *testing.T) {
	// 时钟越过 startedAt(1000) + deadline(30ms)：deadline 分支立即到点。
	clock := &w9cClock{now: 1031}
	body := &deadlineProbeBody{ch: make(chan ChunkResult, 4)}
	deadlineMs := int64(30)
	handlerCalled := make(chan struct{})
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs: 1000,
		Options: StreamPipeOptions{
			NowMs:               clock.NowMs,
			FirstByteDeadlineMs: &deadlineMs,
			OnFirstByteDeadline: func(FirstByteDeadlineInput) (FirstByteDeadlineAction, error) {
				select {
				case <-handlerCalled:
				default:
					close(handlerCalled)
				}
				return FirstByteDeadlineContinue, nil
			},
		},
	})
	pipe.startedAt = 1000

	type readOutcome struct {
		result streamChunkReadResult
		err    error
	}
	resultCh := make(chan readOutcome, 1)
	go func() {
		result, err := pipe.readNextStreamChunk()
		resultCh <- readOutcome{result: result, err: err}
	}()

	// 决策回调触发：deadline 分支已走过且选择继续等待（未返回错误）。
	select {
	case <-handlerCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("first-byte deadline 决策回调未触发")
	}
	// chunk 未到时读取不得提前返回。
	select {
	case outcome := <-resultCh:
		t.Fatalf("deadline 后未等 chunk 就返回: %+v %v", outcome.result, outcome.err)
	case <-time.After(20 * time.Millisecond):
	}

	// deadline 之后到达的 chunk：必须被同一 pending 读取拿到，不缺帧。
	body.ch <- ChunkResult{Data: []byte("after-deadline")}
	var outcome readOutcome
	select {
	case outcome = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline 后到达的 chunk 未被读取")
	}
	if outcome.err != nil {
		t.Fatalf("readNextStreamChunk err = %v", outcome.err)
	}
	if string(outcome.result.chunk.Data) != "after-deadline" {
		t.Fatalf("chunk = %q, want after-deadline（疑似被旧桥吞掉）", outcome.result.chunk.Data)
	}
	if !outcome.result.firstByteDeadlineObserved {
		t.Fatal("firstByteDeadlineObserved 必须置位")
	}

	// 对齐 pipe.loop：记录 observed 标记后继续读下一 chunk，读取链持续可用。
	pipe.firstByteDeadlineObserved = outcome.result.firstByteDeadlineObserved
	body.ch <- ChunkResult{Data: []byte("next")}
	second, err := pipe.readNextStreamChunk()
	if err != nil {
		t.Fatalf("second read err = %v", err)
	}
	if string(second.chunk.Data) != "next" {
		t.Fatalf("second chunk = %q, want next", second.chunk.Data)
	}
}
