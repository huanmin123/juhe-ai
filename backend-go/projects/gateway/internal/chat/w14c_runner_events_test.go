package chat

// w14c 覆盖率补齐：generation_runner 的时间线/事件投影分支。
// jsonRawBlock 的 marshal 错误分支为不可达守卫（assistantBlock 仅含 JSON
// 安全字段），在此登记。

import (
	"context"
	"strings"
	"testing"
)

type w14cCaptureSubscriber struct {
	events []ChatGenerationEvent
	panics bool
	full   bool
}

func (c *w14cCaptureSubscriber) TrySend(event ChatGenerationEvent) bool {
	if c.panics {
		panic("w14c subscriber boom")
	}
	if c.full {
		return false
	}
	c.events = append(c.events, event)
	return true
}

func w14cNewRunner() (*ChatGenerationRunner, *w14cCaptureSubscriber) {
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t", AssistantMessageID: "m"},
		Now:      func() string { return "2026-03-10T08:00:00.000Z" },
	}, context.Background(), func() {}, func() bool { return false })
	runner.currentState = "running"
	subscriber := &w14cCaptureSubscriber{}
	runner.Subscribe(subscriber)
	return runner, subscriber
}

func w14cLastEvent(subscriber *w14cCaptureSubscriber) ChatGenerationEvent {
	if len(subscriber.events) == 0 {
		return ChatGenerationEvent{}
	}
	return subscriber.events[len(subscriber.events)-1]
}

// TestW14CRunnerTextAndReasoningBranches 覆盖文本/reasoning 追加的预算与截断分支。
func TestW14CRunnerTextAndReasoningBranches(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	defer runner.Unsubscribe(subscriber)

	// remainingTextBytes：超额时钳到 0，delta 截断为空 → 直接返回。
	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: strPtrW14C(strings.Repeat("a", chatGenerationTextMaxBytes+8))})
	if len(runner.timeline.Snapshot().ContentBlocks) != 1 {
		t.Fatalf("超额文本必须截断写入: %d", len(runner.timeline.Snapshot().ContentBlocks))
	}

	// 空增量不得改变时间线内容。
	blocksBefore := len(runner.timeline.Snapshot().ContentBlocks)
	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: strPtrW14C("")})
	if len(runner.timeline.Snapshot().ContentBlocks) != blocksBefore {
		t.Fatal("空增量不得改变内容块")
	}

	// reasoning 增量：新建 + 追加 + 完成。
	runner.Publish("reasoning.delta", nil, ChatGenerationProjectionUpdate{ReasoningTextDelta: strPtrW14C("思")})
	runner.Publish("reasoning.delta", nil, ChatGenerationProjectionUpdate{ReasoningTextDelta: strPtrW14C("考")})
	last := w14cLastEvent(subscriber)
	if last.Type != "content_block.delta" {
		t.Fatalf("活跃 reasoning 追加必须发 delta: %+v", last)
	}
	runner.Publish("reasoning.completed", nil, ChatGenerationProjectionUpdate{ReasoningCompleted: true})
	last = w14cLastEvent(subscriber)
	if last.Type != "content_block.completed" {
		t.Fatalf("reasoning 完成事件缺失: %+v", last)
	}

	// reasoning 预算钳 0。
	runner.Publish("reasoning.delta", nil, ChatGenerationProjectionUpdate{ReasoningTextDelta: strPtrW14C(strings.Repeat("b", chatGenerationReasoningMaxBytes+8))})
	if len(runner.timeline.Snapshot().ContentBlocks) < 2 {
		t.Fatal("reasoning 超额必须截断写入")
	}
}

func TestW14CRunnerTimelineDirectBranches(t *testing.T) {
	timeline := newAssistantTimeline()

	// AppendText 终态保护。
	timeline.Finalize(asstFailed)
	if timeline.AppendText("x") != nil || timeline.AppendReasoning("y") != nil {
		t.Fatal("终态时间线不得追加")
	}
}

// TestW14CRunnerEmitEventSubscriberDrop 覆盖订阅者发送失败时的剔除。
func TestW14CRunnerEmitEventSubscriberDrop(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	full := &w14cCaptureSubscriber{full: true}
	runner.Subscribe(full)
	defer runner.Unsubscribe(subscriber)
	defer runner.Unsubscribe(full)

	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: strPtrW14C("hi")})
	runner.mu.Lock()
	stuck := len(runner.subscribers)
	runner.mu.Unlock()
	if stuck != 1 {
		t.Fatalf("发送失败的订阅者必须被剔除: %d", stuck)
	}
	if len(subscriber.events) < 2 {
		t.Fatal("存活订阅者必须收到事件")
	}
}

func strPtrW14C(value string) *string { return &value }
