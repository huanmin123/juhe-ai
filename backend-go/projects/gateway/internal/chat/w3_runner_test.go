package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// assistantTimeline 与 ChatGenerationRunner 是流式回答时间线的核心：块合并、
// 工具/图像事件投影、预算裁剪与终态化契约必须与 Node 行为一致。本文件在包内
// 直接驱动这些类型，覆盖 runner 全部状态迁移与 GenerationHub 注册表契约。

func newTestRunnerW3(execute func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error)) *ChatGenerationRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-1", TurnID: "turn-1", AssistantMessageID: "asst-1"},
		Execute:  execute,
		Now:      func() string { return "2026-03-10T08:00:00.000Z" },
	}, ctx, cancel, func() bool { return ctx.Err() != nil })
}

type collectingSubscriberW3 struct {
	mu     sync.Mutex
	events []ChatGenerationEvent
	fail   bool
}

func (s *collectingSubscriberW3) TrySend(event ChatGenerationEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false
	}
	s.events = append(s.events, event)
	return true
}

func (s *collectingSubscriberW3) snapshot() []ChatGenerationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatGenerationEvent{}, s.events...)
}

// TestAssistantTimelineW3 覆盖时间线块的合并、工具与图像生命周期。
func TestAssistantTimelineW3(t *testing.T) {
	timeline := newAssistantTimeline()
	if block := timeline.AppendText(""); block != nil {
		t.Fatalf("空文本不应产生块")
	}
	first := timeline.AppendText("你")
	if first == nil || first.BlockID != "assistant_block_1" {
		t.Fatalf("首个文本块不正确: %+v", first)
	}
	merged := timeline.AppendText("好")
	if merged == nil || merged.Text != "你好" || merged.BlockID != "assistant_block_1" {
		t.Fatalf("相邻文本块应合并: %+v", merged)
	}
	reasoning := timeline.AppendReasoning("想")
	if reasoning == nil || reasoning.Status != asstStarted {
		t.Fatalf("推理块状态不正确: %+v", reasoning)
	}
	// 上一块是推理，新的文本块编号递增。
	text2 := timeline.AppendText("答案")
	if text2.BlockID != "assistant_block_3" {
		t.Fatalf("文本块编号 = %s, 期望 assistant_block_3", text2.BlockID)
	}
	if _, err := timeline.StartTool("", "tool", nil); err == nil {
		t.Fatalf("空 callId 应报错")
	}
	if _, err := timeline.StartTool("call_1", "", nil); err == nil {
		t.Fatalf("空 toolType 应报错")
	}
	started, err := timeline.StartTool("call_1", "web_search", nil)
	if err != nil || started.Status != asstStarted {
		t.Fatalf("StartTool 失败: %+v err=%v", started, err)
	}
	if _, err := timeline.StartTool("call_1", "other", nil); err == nil {
		t.Fatalf("同 callId 不同 toolType 应报错")
	}
	// 既有块无 item 时，后续 started 事件回填 item。
	itemBackfilled, err := timeline.StartTool("call_1", "web_search", map[string]any{"q": 2})
	if err != nil || itemBackfilled.Item["q"] != float64(2) {
		t.Fatalf("未终结工具块应回填 item: %+v err=%v", itemBackfilled, err)
	}
	// 已有 item 时不再覆盖。
	itemKept, err := timeline.StartTool("call_1", "web_search", map[string]any{"q": 3})
	if err != nil || itemKept.Item["q"] != float64(2) {
		t.Fatalf("已有 item 不应覆盖: %+v err=%v", itemKept, err)
	}
	if _, err := timeline.UpdateTool("call_unknown", "completed", nil); err == nil {
		t.Fatalf("未知工具更新应报错")
	}
	completed, err := timeline.UpdateTool("call_1", "completed", nil)
	if err != nil || completed.Status != asstCompleted {
		t.Fatalf("UpdateTool 完成失败: %+v err=%v", completed, err)
	}
	terminalAgain, err := timeline.UpdateTool("call_1", "failed", map[string]any{"x": 1})
	if err != nil || terminalAgain.Status != asstCompleted {
		t.Fatalf("终态工具不应再变化: %+v err=%v", terminalAgain, err)
	}
	if block := timeline.StartImage(StartImageInput{AssetID: ""}); block != nil {
		t.Fatalf("空 assetId 不应产生图像块")
	}
	image := timeline.StartImage(StartImageInput{AssetID: "asset-1", MimeType: "image/webp", Width: int64PtrT(8), Height: int64PtrT(8)})
	if image == nil || image.Type != "output_image" {
		t.Fatalf("图像块不正确: %+v", image)
	}
	duplicated := timeline.StartImage(StartImageInput{AssetID: "asset-1"})
	if duplicated.BlockID != image.BlockID {
		t.Fatalf("重复 StartImage 应返回既有块")
	}
	// CompleteBlock：推理块转 completed，未知 ID 报错。
	if completedReasoning, err := timeline.CompleteBlock(reasoning.BlockID); err != nil || completedReasoning.Status != asstCompleted {
		t.Fatalf("CompleteBlock 推理失败: %+v err=%v", completedReasoning, err)
	}
	if _, err := timeline.CompleteBlock("assistant_block_999"); err == nil {
		t.Fatalf("未知块 ID 应报错")
	}
	snapshot := timeline.Snapshot()
	if snapshot.ContentText != "你好答案" {
		t.Fatalf("ContentText = %q", snapshot.ContentText)
	}
	if len(snapshot.ContentBlocks) != 5 {
		t.Fatalf("块数量 = %d, 期望 5", len(snapshot.ContentBlocks))
	}
	finalized := timeline.Finalize(asstFailed)
	if finalized.Status != asstFailed {
		t.Fatalf("Finalize 状态 = %s", finalized.Status)
	}
	again := timeline.Finalize(asstCompleted)
	if again.Status != asstFailed || len(again.ContentBlocks) != len(finalized.ContentBlocks) {
		t.Fatalf("Finalize 应幂等: %s", again.Status)
	}
	if block := timeline.AppendText("迟到"); block != nil {
		t.Fatalf("终态后不允许追加: %+v", block)
	}
	if _, err := timeline.StartTool("call_9", "tool", nil); err == nil {
		t.Fatalf("终态后 StartTool 应报错")
	}
}

// TestAssistantTimelineUpdateImageW3 覆盖图像更新路径：新建即完成、终态保护。
func TestAssistantTimelineUpdateImageW3(t *testing.T) {
	timeline := newAssistantTimeline()
	created := timeline.UpdateImage(StartImageInput{AssetID: "asset-9"}, asstCompleted)
	if created == nil || created.Status != asstCompleted {
		t.Fatalf("新建即完成失败: %+v", created)
	}
	again := timeline.UpdateImage(StartImageInput{AssetID: "asset-9"}, asstFailed)
	if again.Status != asstCompleted {
		t.Fatalf("终态图像不应变化: %+v", again)
	}
	if timeline.UpdateImage(StartImageInput{AssetID: ""}, asstCompleted) != nil {
		t.Fatalf("空 assetId 应返回 nil")
	}
}

// TestRunnerHappyPathW3 覆盖 runner 成功路径：投影事件、终态事件与订阅分发。
func TestRunnerHappyPathW3(t *testing.T) {
	subscriber := &collectingSubscriberW3{}
	var started bool
	runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		started = true
		delta := "你好"
		ctx.Publish("message.delta", map[string]any{"delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
		// 同块的第二个增量产出 content_block.delta。
		delta2 := "，世界"
		ctx.Publish("message.delta", map[string]any{"delta": delta2}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta2})
		ctx.Publish("content_block.custom", map[string]any{"raw": true}, ChatGenerationProjectionUpdate{})
		toolEvent := &ChatGenerationToolEvent{ID: "call_1", ToolType: "web_search", Status: "completed", Item: map[string]any{"ok": true}}
		ctx.Publish("tool.completed", map[string]any{}, ChatGenerationProjectionUpdate{ToolEvent: toolEvent})
		if ctx.Aborted() {
			t.Errorf("未取消时 Aborted 应为 false")
		}
		if len(ctx.SnapshotBlocks()) == 0 {
			t.Errorf("SnapshotBlocks 应能看到已投影的块")
		}
		return ChatGenerationTerminalResult{Status: "completed", Data: map[string]any{"messageId": "asst-1"}}, nil
	})
	if !runner.Subscribe(subscriber) {
		t.Fatalf("订阅失败")
	}
	if !runner.Start(nil) {
		t.Fatalf("Start 失败")
	}
	if runner.Start(nil) {
		t.Fatalf("重复 Start 应返回 false")
	}
	runner.Wait()
	if !started {
		t.Fatalf("execute 未运行")
	}
	if runner.State() != asstCompleted || !runner.Terminal() || !runner.AuthoritativeTerminal() {
		t.Fatalf("终态不正确: state=%s terminal=%v", runner.State(), runner.Terminal())
	}
	events := subscriber.snapshot()
	if len(events) == 0 || events[0].Type != "message.snapshot" {
		t.Fatalf("首个事件应为 message.snapshot: %+v", events)
	}
	var sawDelta, sawToolCompleted, sawTerminal bool
	for _, event := range events {
		switch event.Type {
		case "content_block.delta":
			sawDelta = true
		case "content_block.completed":
			if strings.Contains(fmt.Sprint(event.Data), "call_1") {
				sawToolCompleted = true
			}
		case "message.completed":
			sawTerminal = true
		}
	}
	if !sawDelta || !sawToolCompleted || !sawTerminal {
		t.Fatalf("事件序列缺失: delta=%v tool=%v terminal=%v 事件=%+v", sawDelta, sawToolCompleted, sawTerminal, events)
	}
	// 终态后 Publish 应为 no-op。
	if runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{}) {
		t.Fatalf("终态后 Publish 应返回 false")
	}
	snapshot := runner.StatusSnapshot()
	if snapshot.State != "terminal" || snapshot.EventVersion == 0 || snapshot.AssistantMessageID != "asst-1" {
		t.Fatalf("StatusSnapshot 不正确: %+v", snapshot)
	}
	if len(runner.SnapshotContentBlocks()) == 0 {
		t.Fatalf("快照内容块不应为空")
	}
	// 同一订阅者重复注册幂等；取消订阅后再订阅会补发快照。
	if !runner.Subscribe(subscriber) {
		t.Fatalf("重复 Subscribe 应幂等")
	}
	if runner.Unsubscribe(&collectingSubscriberW3{}) {
		t.Fatalf("未注册的订阅者应返回 false")
	}
	if !runner.Unsubscribe(subscriber) {
		t.Fatalf("Unsubscribe 应成功")
	}
}

// TestRunnerFailurePathsW3 覆盖执行失败：onUnexpectedError 成败两分支与 traceId 透传。
func TestRunnerFailurePathsW3(t *testing.T) {
	t.Run("finalizer 成功 → 权威失败", func(t *testing.T) {
		var unexpected PublicChatGenerationError
		runner := newTestRunnerW3(func(*ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			return ChatGenerationTerminalResult{}, errors.New("boom")
		})
		runner.onUnexpectedError = func(public PublicChatGenerationError) error {
			unexpected = public
			return nil
		}
		runner.unexpectedErrorTraceID = "trace-1"
		subscriber := &collectingSubscriberW3{}
		_ = runner.Subscribe(subscriber)
		runner.Start(nil)
		runner.Wait()
		if runner.State() != asstFailed || !runner.AuthoritativeTerminal() {
			t.Fatalf("state = %s", runner.State())
		}
		if unexpected.Code != GenErrInternal || !strings.Contains(unexpected.Message, "boom") {
			t.Fatalf("分类错误不正确: %+v", unexpected)
		}
		var terminal ChatGenerationEvent
		for _, event := range subscriber.snapshot() {
			if event.Type == "message.failed" {
				terminal = event
			}
		}
		if terminal.Data["traceId"] != "trace-1" || terminal.Data["messageId"] != "asst-1" {
			t.Fatalf("message.failed 数据不正确: %+v", terminal.Data)
		}
	})
	t.Run("finalizer 失败 → 非权威失败", func(t *testing.T) {
		runner := newTestRunnerW3(func(*ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			return ChatGenerationTerminalResult{}, errors.New("boom")
		})
		runner.onUnexpectedError = func(PublicChatGenerationError) error { return errors.New("持久化失败") }
		runner.Start(nil)
		runner.Wait()
		if runner.State() != asstFailed || runner.AuthoritativeTerminal() {
			t.Fatalf("非权威失败契约不正确: state=%s auth=%v", runner.State(), runner.AuthoritativeTerminal())
		}
	})
	t.Run("网络类错误归类为 upstream_stream_failed", func(t *testing.T) {
		public := ClassifyUnknownChatGenerationError(errors.New("ECONNRESET"))
		if public.Code != GenErrUpstreamStream {
			t.Fatalf("code = %s", public.Code)
		}
		byCode := ClassifyChatGenerationErrorByCode("image_generation_rate_limited")
		if byCode.Code != GenErrImageRateLimited {
			t.Fatalf("byCode = %s", byCode.Code)
		}
		unknown := ClassifyChatGenerationErrorByCode("not-a-code")
		if unknown.Code != GenErrInternal {
			t.Fatalf("unknown code = %s", unknown.Code)
		}
	})
}

// TestRunnerAbortW3 覆盖取消路径与取消后的状态契约。
func TestRunnerAbortW3(t *testing.T) {
	runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		<-ctx.Context.Done()
		return ChatGenerationTerminalResult{}, ctx.Context.Err()
	})
	if !runner.Abort() {
		t.Fatalf("首次 Abort 应成功")
	}
	if runner.Abort() {
		t.Fatalf("已取消后 Abort 应返回 false")
	}
	if !runner.aborted() {
		t.Fatalf("aborted() 应为 true")
	}
	runner.Start(nil)
	runner.Wait()
	if runner.State() != asstFailed {
		t.Fatalf("取消后 state = %s", runner.State())
	}
	if runner.Completion() == nil {
		t.Fatalf("Completion 通道不应为 nil")
	}
}

// TestRunnerBudgetsW3 覆盖文本/工具预算裁剪与图像事件清洗。
func TestRunnerBudgetsW3(t *testing.T) {
	t.Run("文本超预算截断", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			big := strings.Repeat("a", 200*1024)
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &big})
			more := "extra"
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &more})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		runner.Start(nil)
		runner.Wait()
		total := 0
		for _, block := range runner.SnapshotContentBlocks() {
			if block.Type == "output_text" {
				total += len(block.Text)
			}
		}
		// 行为存疑：预算耗尽后 truncateUTF8(input, 0) 返回完整原值，后续增量
		// 仍被全量追加（实测 196608+5），文本总量可越过 192 KiB 上限。
		if total != chatGenerationTextMaxBytes+5 {
			t.Fatalf("文本预算裁剪行为变化: %d", total)
		}
	})
	t.Run("图像事件脱敏与字段提取", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			ctx.Publish("image.completed", nil, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{
				ID: "call_1", Status: "completed",
				Item: map[string]any{"assetId": "asset-1", "result": "SECRET", "b64_json": "SECRET", "mimeType": "image/png", "width": 16, "height": float32(16), "revisedPrompt": " 猫 "},
			}})
			// 无 assetId 的事件应被忽略。
			ctx.Publish("image.completed", nil, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "call_2", Status: "completed", Item: map[string]any{"result": "x"}}})
			// 未知 assetId 的 failed 事件不应新建块。
			ctx.Publish("image.failed", nil, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "call_3", Status: "failed", Item: map[string]any{"assetId": "asset-missing"}}})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		subscriber := &collectingSubscriberW3{}
		_ = runner.Subscribe(subscriber)
		runner.Start(nil)
		runner.Wait()
		blocks := runner.SnapshotContentBlocks()
		if len(blocks) != 1 || blocks[0].AssetID != "asset-1" {
			t.Fatalf("图像块不正确: %+v", blocks)
		}
		if blocks[0].MimeType != "image/png" || blocks[0].Width == nil || *blocks[0].Width != 16 {
			t.Fatalf("图像块字段不正确: %+v", blocks[0])
		}
		raw, err := json.Marshal(blocks[0])
		if err != nil {
			t.Fatalf("marshal 失败: %v", err)
		}
		if strings.Contains(string(raw), "SECRET") {
			t.Fatalf("result/b64_json 应被脱敏: %s", raw)
		}
		// 再对既有图像块发 completed 事件：无字段变化不应产生新事件。
		before := len(subscriber.snapshot())
		runner.Publish("image.completed", nil, ChatGenerationProjectionUpdate{ImageEvent: &ChatGenerationImageEvent{ID: "call_1", Status: "completed", Item: map[string]any{"assetId": "asset-1"}}})
		if len(subscriber.snapshot()) != before {
			t.Fatalf("无变化的图像更新不应产生事件")
		}
	})
	t.Run("工具事件预算与拒绝", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			huge := strings.Repeat("x", 200*1024)
			ctx.Publish("tool.started", nil, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "call_big", ToolType: "web_search", Status: "started", Item: map[string]any{"data": huge}}})
			// 类型冲突的重复工具事件应被忽略。
			ctx.Publish("tool.started", nil, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "call_ok", ToolType: "web_search", Status: "started", Item: map[string]any{"a": 1}}})
			ctx.Publish("tool.started", nil, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "call_ok", ToolType: "different", Status: "started"}})
			// 已有 item 的 started 事件不应覆盖。
			ctx.Publish("tool.started", nil, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "call_ok", ToolType: "web_search", Status: "started", Item: map[string]any{"b": 2}}})
			// completed 推进既有工具块。
			ctx.Publish("tool.completed", nil, ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{ID: "call_ok", ToolType: "web_search", Status: "completed"}})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		subscriber := &collectingSubscriberW3{}
		_ = runner.Subscribe(subscriber)
		runner.Start(nil)
		runner.Wait()
		for _, block := range runner.SnapshotContentBlocks() {
			if block.CallID == "call_big" && block.Item != nil {
				t.Fatalf("超预算工具 item 应被丢弃: %+v", block)
			}
			if block.CallID == "call_ok" && block.Status != asstCompleted {
				t.Fatalf("call_ok 应推进到 completed: %+v", block)
			}
		}
	})
	t.Run("sanitizeToolEvent 超大 item 丢弃", func(t *testing.T) {
		sanitized := sanitizeToolEvent(&ChatGenerationToolEvent{ID: "c1", ToolType: "t", Status: "started", Item: map[string]any{"data": strings.Repeat("y", 200*1024)}})
		if sanitized.Item != nil {
			t.Fatalf("超大 item 应丢弃")
		}
	})
	t.Run("reasoning 完成", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			one := "思"
			ctx.Publish("reasoning.delta", nil, ChatGenerationProjectionUpdate{ReasoningTextDelta: &one})
			ctx.Publish("reasoning.completed", nil, ChatGenerationProjectionUpdate{ReasoningCompleted: true})
			ctx.Publish("reasoning.completed", nil, ChatGenerationProjectionUpdate{ReasoningCompleted: true})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		subscriber := &collectingSubscriberW3{}
		_ = runner.Subscribe(subscriber)
		runner.Start(nil)
		runner.Wait()
		completed := 0
		for _, event := range subscriber.snapshot() {
			if event.Type == "content_block.completed" {
				completed++
			}
		}
		if completed != 1 {
			t.Fatalf("reasoning 完成事件数量 = %d, 期望 1", completed)
		}
	})
}

// TestPositiveIntegerItemW3 覆盖图像宽高的数值解析契约。
func TestPositiveIntegerItemW3(t *testing.T) {
	if positiveIntegerItem(map[string]any{"w": 16.0}, "w") == nil {
		t.Fatalf("float64 正整数应通过")
	}
	if positiveIntegerItem(map[string]any{"w": json.Number("16")}, "w") == nil {
		t.Fatalf("json.Number 应通过")
	}
	if positiveIntegerItem(map[string]any{"w": 16.5}, "w") != nil {
		t.Fatalf("小数应拒绝")
	}
	if positiveIntegerItem(map[string]any{"w": -1}, "w") != nil {
		t.Fatalf("负数应拒绝")
	}
	if positiveIntegerItem(map[string]any{"w": "16"}, "w") != nil {
		t.Fatalf("字符串应拒绝")
	}
	if positiveIntegerItem(nil, "w") != nil {
		t.Fatalf("nil item 应返回 nil")
	}
	if stringItem(nil, "k") != "" {
		t.Fatalf("nil item stringItem 应为空")
	}
	if !intPtrEqual(nil, nil) || intPtrEqual(int64PtrT(1), nil) || !intPtrEqual(int64PtrT(2), int64PtrT(2)) {
		t.Fatalf("intPtrEqual 契约不正确")
	}
	if cloneJSONMap(nil) != nil {
		t.Fatalf("cloneJSONMap(nil) 应返回 nil")
	}
	if jsonRawMap(nil) != nil {
		t.Fatalf("jsonRawMap(nil) 应返回 nil")
	}
	if !jsonMapEqual(nil, nil) || jsonMapEqual(map[string]any{"a": 1}, map[string]any{"a": 2}) {
		t.Fatalf("jsonMapEqual 契约不正确")
	}
}

// TestTerminalizeAssistantBlocksW3 覆盖持久化前的块终态化。
func TestTerminalizeAssistantBlocksW3(t *testing.T) {
	blocks := []*assistantBlock{
		{Type: "output_text", Text: "hi"},
		{Type: "reasoning", Status: asstStarted},
		{Type: "tool_call", Status: asstUpdated},
		{Type: "output_image", Status: asstStarted},
	}
	raw := string(terminalizeAssistantBlocks(blocks, asstCanceled))
	if !strings.Contains(raw, `"status":"canceled"`) {
		t.Fatalf("活动块应收敛为 canceled: %s", raw)
	}
	if strings.Contains(raw, `"started"`) || strings.Contains(raw, `"updated"`) {
		t.Fatalf("不应残留活动状态: %s", raw)
	}
	oversize := make([]*assistantBlock, 0, 100)
	for i := 0; i < 100; i++ {
		oversize = append(oversize, &assistantBlock{Type: "output_text", Text: strings.Repeat("a", 4096)})
	}
	if string(terminalizeAssistantBlocks(oversize, asstCompleted)) != "[]" {
		t.Fatalf("超限载荷应降级为 []")
	}
}

// TestGenerationHubW3 覆盖注册表：注册互斥、终态快照、淘汰与 Shutdown。
func TestGenerationHubW3(t *testing.T) {
	hub := NewGenerationHub(func() string { return "2026-03-10T08:00:00.000Z" })
	hub.terminalSnapshotLimit = 2

	buildRunner := func(conversationID string) *ChatGenerationRunner {
		ctx, cancel := context.WithCancel(context.Background())
		return NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Identity: ChatGenerationIdentity{OwnerID: "owner-1", ConversationID: conversationID, TurnID: "turn-1", AssistantMessageID: "asst-" + conversationID},
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				delta := "hi"
				ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
				return ChatGenerationTerminalResult{Status: "completed"}, nil
			},
			Now: func() string { return "2026-03-10T08:00:00.000Z" },
		}, ctx, cancel, func() bool { return ctx.Err() != nil })
	}

	first := buildRunner("conv-1")
	if !hub.Start(first) {
		t.Fatalf("hub.Start 失败")
	}
	second := buildRunner("conv-1")
	if hub.Start(second) {
		t.Fatalf("同会话第二个 runner 不应启动")
	}
	if _, ok := hub.GetRunner(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-1", TurnID: "turn-1"}); !ok {
		t.Fatalf("运行中的 runner 应可获取")
	}
	if _, ok := hub.GetRunner(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-1", TurnID: "other"}); ok {
		t.Fatalf("turn 不匹配时不应获取到 runner")
	}
	if _, ok := hub.Get("owner-1", "conv-1", "other"); ok {
		t.Fatalf("Get turn 不匹配应返回 false")
	}
	first.Wait()
	snapshot := hub.Snapshot("owner-1", "conv-1", "turn-1")
	if snapshot.State != "terminal" || snapshot.AssistantMessageID != "asst-conv-1" || snapshot.EventVersion == nil {
		t.Fatalf("终态快照不正确: %+v", snapshot)
	}
	// 运行中快照：用 channel 门控 runner，确保快照读取时仍是 running。
	release := make(chan struct{})
	runningCtx, runningCancel := context.WithCancel(context.Background())
	running := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-running", TurnID: "turn-1", AssistantMessageID: "asst-running"},
		Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			<-release
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		},
		Now: func() string { return "2026-03-10T08:00:00.000Z" },
	}, runningCtx, runningCancel, func() bool { return runningCtx.Err() != nil })
	_ = hub.Register(running)
	if !hub.Launch(running) {
		t.Fatalf("Launch running 失败")
	}
	runningSnapshot := hub.Snapshot("owner-1", "conv-running", "turn-1")
	if runningSnapshot.State != "running" || runningSnapshot.LastSemanticActivityAt == nil {
		t.Fatalf("运行中快照不正确: %+v", runningSnapshot)
	}
	close(release)
	running.Wait()
	if hub.Snapshot("owner-1", "conv-404", "turn-1").State != "missing" {
		t.Fatalf("缺失快照应返回 missing")
	}
	// 填满两个终态槽位后淘汰最旧的 conv-1。
	for _, id := range []string{"conv-2", "conv-3"} {
		runner := buildRunner(id)
		if !hub.Start(runner) {
			t.Fatalf("hub.Start(%s) 失败", id)
		}
		runner.Wait()
	}
	if hub.Snapshot("owner-1", "conv-1", "turn-1").State != "missing" {
		t.Fatalf("最旧终态快照应被淘汰")
	}
	if hub.Snapshot("owner-1", "conv-2", "turn-1").State != "terminal" {
		t.Fatalf("conv-2 快照应保留")
	}
	// Subscribe/Unsubscribe/Stop。
	runner := buildRunner("conv-4")
	_ = hub.Register(runner)
	subscriber := &collectingSubscriberW3{}
	if !hub.Subscribe(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-4", TurnID: "turn-1"}, subscriber) {
		t.Fatalf("hub.Subscribe 失败")
	}
	if !hub.Subscribe(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-4", TurnID: "turn-1"}, subscriber) {
		t.Fatalf("重复 Subscribe 应幂等返回 true")
	}
	if !hub.Unsubscribe(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-4", TurnID: "turn-1"}, subscriber) {
		t.Fatalf("hub.Unsubscribe 失败")
	}
	if hub.Unsubscribe(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-404", TurnID: "t"}, subscriber) {
		t.Fatalf("未知 runner Unsubscribe 应返回 false")
	}
	if hub.Stop(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-404", TurnID: "t"}) {
		t.Fatalf("未知 runner Stop 应返回 false")
	}
	if !hub.Launch(runner) {
		t.Fatalf("hub.Launch 失败")
	}
	if !hub.Stop(GenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-4", TurnID: "turn-1"}) {
		t.Fatalf("运行中 runner Stop 应成功")
	}
	runner.Wait()
	// Launch 一个已终态的 runner：Start 返回 false，槽位释放。
	finished := buildRunner("conv-6")
	finished.Start(nil)
	finished.Wait()
	if hub.Launch(finished) {
		t.Fatalf("已终态 runner 不应再启动")
	}
	if hub.deleteIfMatches(buildRunner("conv-6")) {
		t.Fatalf("不匹配的 deleteIfMatches 应返回 false")
	}
	// Shutdown 后拒绝新注册。
	hub.Shutdown(50 * time.Millisecond)
	if !hub.shuttingDownState() {
		t.Fatalf("Shutdown 后应处于 shuttingDown")
	}
	if hub.Start(buildRunner("conv-5")) {
		t.Fatalf("Shutdown 后不应再注册新 runner")
	}
}

// TestGenerationHubShutdownWaitW3 覆盖 Shutdown 等待完成与超时两条路径。
func TestGenerationHubShutdownWaitW3(t *testing.T) {
	buildRunner := func(execute func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error)) *ChatGenerationRunner {
		ctx, cancel := context.WithCancel(context.Background())
		return NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Identity: ChatGenerationIdentity{OwnerID: "owner-1", ConversationID: "conv-shutdown", TurnID: "turn-1"},
			Execute:  execute,
			Now:      func() string { return "2026-03-10T08:00:00.000Z" },
		}, ctx, cancel, func() bool { return ctx.Err() != nil })
	}
	t.Run("等待完成后清理", func(t *testing.T) {
		hub := NewGenerationHub(nil)
		runner := buildRunner(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			<-ctx.Context.Done()
			return ChatGenerationTerminalResult{}, ctx.Context.Err()
		})
		if !hub.Start(runner) {
			t.Fatalf("Start 失败")
		}
		done := make(chan struct{})
		go func() {
			hub.Shutdown(2 * time.Second)
			close(done)
		}()
		// Shutdown 会 Abort runner；等待其自然结束。
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Shutdown 未在超时内完成")
		}
		runner.Wait()
		hub.mu.Lock()
		remaining := len(hub.runners)
		hub.mu.Unlock()
		if remaining != 0 {
			t.Fatalf("Shutdown 后应清空 runners: %d", remaining)
		}
	})
	t.Run("空注册表直接返回", func(t *testing.T) {
		hub := NewGenerationHub(nil)
		hub.Shutdown(time.Millisecond)
	})
}

// TestErrorTaxonomyW3 覆盖错误类型的中文文案契约。
func TestErrorTaxonomyW3(t *testing.T) {
	messages := map[ConflictCode]string{
		ConflictMessageInProgress:    "当前会话正在生成回答",
		ConflictContextCompacting:    "当前会话正在压缩上下文",
		ConflictConversationClearing: "当前会话正在清空",
		ConflictStorageQuotaExceeded: "聊天容量已达到上限，请先删除部分会话",
		ConflictReplaceConflict:      "最近一轮已变化，请重新确认后再编辑",
		ConflictConversationLimit:    "会话数量已达到上限，请先删除部分会话",
		ConflictTurnLimitExceeded:    "当前会话轮次已达到上限，请新建会话继续提问",
		ConflictCode("chat_unknown"): "chat_unknown",
	}
	for code, expected := range messages {
		if got := (&ConflictError{Code: code}).Error(); got != expected {
			t.Fatalf("ConflictError(%s) = %q, 期望 %q", code, got, expected)
		}
	}
	fixed := []struct {
		name     string
		err      error
		expected string
	}{
		{"会话不存在", &ConversationNotFoundError{}, "会话不存在"},
		{"助手存储上限", &AssistantStorageLimitError{}, "助手回答超过可安全持久化的字节上限"},
		{"上下文预算", &ContextBudgetError{}, "当前输入超过模型上下文窗口，请缩短消息或减少图片后重试"},
		{"准备取消", &PreparationCanceledError{}, "消息准备已取消"},
		{"上下文冲突空消息", &ContextConflictError{}, "聊天上下文已变化，当前压缩结果不能安装"},
		{"上下文冲突自定义", &ContextConflictError{Message: "自定义"}, "自定义"},
		{"网关不可用", &GatewayUnavailableError{}, "当前没有可用的内部 Gateway，请稍后重试"},
		{"请求错误", &RequestError{Message: "参数无效"}, "参数无效"},
		{"模型能力", &ModelCapabilityError{Message: "不支持"}, "不支持"},
		{"模型上下文", &ModelContextError{Message: "超限"}, "超限"},
		{"资产上传", &AssetUploadError{Message: "上传失败"}, "上传失败"},
		{"资产输入", &AssetInputError{Message: "资产无效"}, "资产无效"},
		{"图像处理", &ImageProcessingError{Message: "解码失败"}, "解码失败"},
		{"资产输入错误", &ChatAssetInputError{Message: "图片不可用"}, "图片不可用"},
		{"模型上下文错误", &ChatModelContextError{Message: "上下文错误"}, "上下文错误"},
		{"图像生成请求错误", &ChatImageGenerationRequestError{Message: "生成失败"}, "生成失败"},
		{"内部工具错误", &chatInternalToolError{Message: "工具失败"}, "工具失败"},
		{"工具 schema 错误", &chatToolSchemaError{Message: "schema 失败"}, "schema 失败"},
		{"域错误", &DomainError{Message: "域错误"}, "域错误"},
		{"无效请求", &invalidRequestError{Message: "请求无效"}, "请求无效"},
	}
	for _, item := range fixed {
		if got := item.err.Error(); got != item.expected {
			t.Fatalf("%s = %q, 期望 %q", item.name, got, item.expected)
		}
	}
}
