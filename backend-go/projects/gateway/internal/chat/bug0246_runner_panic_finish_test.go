package chat

// BUG-0246 回归：生成 runner 在 orchestrator 之外的收尾代码 panic 时，
// completion 必须被 close、onSettled 必须触发恰好一次（hub 槽位释放），
// 且同会话可再次启动生成（不再永久 409 chat_stream_conflict）。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newB0246Runner(execute func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error), onUnexpectedError func(PublicChatGenerationError) error) *ChatGenerationRunner {
	return NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity:          ChatGenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t", AssistantMessageID: "m"},
		Execute:           execute,
		OnUnexpectedError: onUnexpectedError,
		Now:               func() string { return "2026-03-10T08:00:00.000Z" },
	}, context.Background(), func() {}, func() bool { return false })
}

func waitB0246Completion(t *testing.T, runner *ChatGenerationRunner) {
	t.Helper()
	select {
	case <-runner.Completion():
	case <-time.After(5 * time.Second):
		t.Fatal("completion 未关闭（panic 兜底收尾缺失）")
	}
}

// assertB0246OnSettledOnce 断言 onSettled 恰好一次：completion close 前必
// 已同步完成首次调用；再留一个宽限窗口捕捉潜在的双触发回归。
func assertB0246OnSettledOnce(t *testing.T, calls *atomic.Int32) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("onSettled 应恰好调用一次，实际 %d 次", got)
	}
}

// TestBug0246ExecutePanicStillFinishes：execute 闭包 panic（收尾段之前）
// 时 completion 关闭、onSettled 恰好一次。
func TestBug0246ExecutePanicStillFinishes(t *testing.T) {
	runner := newB0246Runner(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		panic("b0246 execute boom")
	}, nil)
	var calls atomic.Int32
	if !runner.Start(func() { calls.Add(1) }) {
		t.Fatal("runner 未启动")
	}
	waitB0246Completion(t, runner)
	assertB0246OnSettledOnce(t, &calls)
}

// TestBug0246FinalizerPanicStillFinishes：execute 返回错误后、onUnexpectedError
// 落库回调（orchestrator 之外的收尾代码）panic 时收尾契约同样成立。
func TestBug0246FinalizerPanicStillFinishes(t *testing.T) {
	runner := newB0246Runner(
		func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			return ChatGenerationTerminalResult{}, errors.New("b0246 upstream error")
		},
		func(PublicChatGenerationError) error { panic("b0246 finalizer boom") },
	)
	var calls atomic.Int32
	if !runner.Start(func() { calls.Add(1) }) {
		t.Fatal("runner 未启动")
	}
	waitB0246Completion(t, runner)
	assertB0246OnSettledOnce(t, &calls)
}

// TestBug0246NormalPathSettlesOnce：正常成功路径 onSettled 恰好一次
// （panic 兜底不与正常 finish 双触发）。
func TestBug0246NormalPathSettlesOnce(t *testing.T) {
	runner := newB0246Runner(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		return ChatGenerationTerminalResult{Status: asstCompleted}, nil
	}, nil)
	var calls atomic.Int32
	if !runner.Start(func() { calls.Add(1) }) {
		t.Fatal("runner 未启动")
	}
	waitB0246Completion(t, runner)
	if runner.State() != asstCompleted {
		t.Fatalf("正常路径终态应 completed: %q", runner.State())
	}
	assertB0246OnSettledOnce(t, &calls)
}

// TestBug0246PanicReleasesHubSlot：BUG-0246 用户可见行为——panic 后 hub
// 槽位释放，同会话可再次启动生成。
func TestBug0246PanicReleasesHubSlot(t *testing.T) {
	hub := NewGenerationHub(func() string { return "2026-03-10T08:00:00.000Z" })
	identity := GenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t"}
	runner := newB0246Runner(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		panic("b0246 hub slot boom")
	}, nil)
	if !hub.Register(runner) {
		t.Fatal("首次注册失败")
	}
	if !hub.Launch(runner) {
		t.Fatal("首次启动失败")
	}
	waitB0246Completion(t, runner)
	if _, active := hub.GetRunner(identity); active {
		t.Fatal("panic 后会话槽位仍被占用")
	}
	second := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t2", AssistantMessageID: "m2"},
		Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			return ChatGenerationTerminalResult{Status: asstCompleted}, nil
		},
		Now: func() string { return "2026-03-10T08:00:00.000Z" },
	}, context.Background(), func() {}, func() bool { return false })
	if !hub.Start(second) {
		t.Fatal("panic 后同会话应可再次启动（原为永久 409 chat_stream_conflict）")
	}
	waitB0246Completion(t, second)
	if second.State() != asstCompleted {
		t.Fatalf("第二次生成应正常完成: %q", second.State())
	}
}
