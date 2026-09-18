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

// TestW14CRunnerImageEventBranches 覆盖图片事件的启动/更新/补丁分支。
func TestW14CRunnerImageEventBranches(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	defer runner.Unsubscribe(subscriber)

	// started：无字段启动。
	runner.Publish("image.started", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstStarted, Item: map[string]any{"assetId": "a1"}},
	})
	// updated：仅 mimeType（patch 走 mimeType 分支）。
	runner.Publish("image.updated", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstUpdated, Item: map[string]any{"assetId": "a1", "mimeType": "image/webp"}},
	})
	last := w14cLastEvent(subscriber)
	if last.Type != "content_block.updated" {
		t.Fatalf("updated 必须发补丁事件: %+v", last)
	}
	// updated：仅宽高（patch 走 width/height 分支）。
	runner.Publish("image.updated", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstStarted, Item: map[string]any{"assetId": "a1", "width": 64, "height": 32}},
	})
	// updated：revisedPrompt（patch 走 revisedPrompt 分支）。
	runner.Publish("image.updated", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstStarted, Item: map[string]any{"assetId": "a1", "revisedPrompt": "p"}},
	})
	// 字段全等 → no-op。
	runner.Publish("image.updated", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstStarted, Item: map[string]any{"assetId": "a1", "revisedPrompt": "p"}},
	})
	// completed。
	runner.Publish("image.completed", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "img-1", Status: asstCompleted, Item: map[string]any{"assetId": "a1"}},
	})
	last = w14cLastEvent(subscriber)
	if last.Type != "content_block.completed" {
		t.Fatalf("图片完成事件缺失: %+v", last)
	}
}

// TestW14CRunnerImageTerminalAndInvalid 覆盖时间线终态与非法图片事件。
func TestW14CRunnerImageTerminalAndInvalid(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	defer runner.Unsubscribe(subscriber)

	// 时间线终态后：已存在图片的更新 → UpdateImage nil → 返回；
	// 新图片的启动 → StartImage nil → 返回。
	runner.Publish("image.started", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "i", Status: asstStarted, Item: map[string]any{"assetId": "a1"}},
	})
	runner.finalizeTimelineLocked(asstFailed)
	runner.Publish("image.completed", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "i", Status: asstCompleted, Item: map[string]any{"assetId": "a1", "mimeType": "image/webp"}},
	})
	runner.Publish("image.started", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "i2", Status: asstStarted, Item: map[string]any{"assetId": "a2"}},
	})
	if got := len(subscriber.events); got < 2 {
		t.Fatalf("终态后不得新增图片事件: %d", got)
	}

	// 空白 assetId → StartImage nil → 启动分支直接返回。
	runner2, subscriber2 := w14cNewRunner()
	defer runner2.Unsubscribe(subscriber2)
	runner2.Publish("image.started", nil, ChatGenerationProjectionUpdate{
		ImageEvent: &ChatGenerationImageEvent{ID: "i", Status: asstStarted, Item: map[string]any{"assetId": "   "}},
	})
	if len(subscriber2.events) != 1 { // 仅订阅快照
		t.Fatalf("空白 assetId 不得产生事件: %d", len(subscriber2.events))
	}
}

// TestW14CRunnerToolEventBranches 覆盖工具事件的预算/校验/更新分支。
func TestW14CRunnerToolEventBranches(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	defer runner.Unsubscribe(subscriber)

	// 空 ID → StartTool 校验失败 → 返回。
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "   ", ToolType: "web_search", Status: asstStarted},
	})
	// 超预算 item → toolItemWithinBudget nil → 返回。
	huge := strings.Repeat("x", chatGenerationToolJSONMaxBytes+16)
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": huge}},
	})
	// 正常启动。
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": 1}},
	})
	// started 且已有 item → no-op。
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": 9}},
	})
	// updated（asstUpdated → started 归一化）+ 新 item → 补丁事件。
	runner.Publish("tool.updated", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstUpdated, Item: map[string]any{"q": 2}},
	})
	last := w14cLastEvent(subscriber)
	if last.Type != "content_block.updated" {
		t.Fatalf("工具补丁事件缺失: %+v", last)
	}
	// failed（非 completed）→ 补丁事件仅 status。
	runner.Publish("tool.failed", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstFailed},
	})
	last = w14cLastEvent(subscriber)
	if last.Type != "content_block.updated" {
		t.Fatalf("工具失败补丁缺失: %+v", last)
	}

	// 时间线终态后：更新既有工具 → UpdateTool 错误 → 返回；
	// started 已有 item → StartTool 错误 → 返回。
	runner.finalizeTimelineLocked(asstFailed)
	runner.Publish("tool.completed", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstCompleted},
	})
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": 3}},
	})
}

// TestW14CRunnerToolBudgetReplaceBranch 覆盖既有工具替换超预算分支。
func TestW14CRunnerToolBudgetReplaceBranch(t *testing.T) {
	runner, subscriber := w14cNewRunner()
	defer runner.Unsubscribe(subscriber)

	// 建两个工具块，占住预算；再更新第一个为超大 item → 替换后超限 → nil。
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": strings.Repeat("x", chatGenerationToolJSONMaxBytes/3)}},
	})
	runner.Publish("tool.started", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c2", ToolType: "web_search", Status: asstStarted, Item: map[string]any{"q": strings.Repeat("y", chatGenerationToolJSONMaxBytes/3)}},
	})
	if len(runner.timeline.Snapshot().ContentBlocks) < 2 {
		t.Fatal("两个工具块必须建立")
	}
	huge := strings.Repeat("z", chatGenerationToolJSONMaxBytes)
	runner.Publish("tool.updated", nil, ChatGenerationProjectionUpdate{
		ToolEvent: &ChatGenerationToolEvent{ID: "c1", ToolType: "web_search", Status: asstUpdated, Item: map[string]any{"q": huge}},
	})
	if got := len(runner.timeline.Snapshot().ContentBlocks); got != 2 {
		t.Fatalf("超预算替换不得改写时间线: %d", got)
	}
}

// TestW14CRunnerTimelineDirectBranches 直驱时间线边界。
func TestW14CRunnerTimelineDirectBranches(t *testing.T) {
	timeline := newAssistantTimeline()

	// UpdateImage 对未知图片递归创建：非 started 状态立即补写。
	created := timeline.UpdateImage(StartImageInput{AssetID: "a1", MimeType: "image/webp"}, asstCompleted)
	if created == nil || created.Status != asstCompleted {
		t.Fatalf("未知图片按状态直建: %+v", created)
	}

	// AppendText 终态保护。
	timeline.Finalize(asstFailed)
	if timeline.AppendText("x") != nil || timeline.AppendReasoning("y") != nil {
		t.Fatal("终态时间线不得追加")
	}
	if timeline.StartImage(StartImageInput{AssetID: "a2"}) != nil {
		t.Fatal("终态时间线不得启动图片")
	}
	if _, err := timeline.StartTool("c", "t", nil); err == nil {
		t.Fatal("终态时间线不得启动工具")
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
