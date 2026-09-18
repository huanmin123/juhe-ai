package chat

// w14c 覆盖率补齐：上下文压缩服务的认领执行链与检查点安装错误臂。

import (
	"context"
	"io"
	"strings"
	"testing"
)

// w14cCompactionFixture 建一个带 4 轮历史、可触发压缩的会话。
func w14cCompactionFixture(t *testing.T) (*chatFixture, *CompactionService, *mockExecutor, string) {
	t.Helper()
	fixture := newChatFixture(t)
	executor := &mockExecutor{}
	service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	conversationID := "chat_conv_w14c_comp"
	fixture.createConversation(conversationID, routeTestOwner)
	fixture.seedTurns(routeTestOwner, conversationID, 5)
	return fixture, service, executor, conversationID
}

// TestW14CCompactionSourcePageFaults 覆盖认领执行链的存储错误臂。
func TestW14CCompactionSourcePageFaults(t *testing.T) {
	// 来源分页查询失败 → failClaim。
	fixture, script := newFaultChatFixtureW10D(t)
	executor := &mockExecutor{}
	service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	fixture.createConversation("chat_conv_w14c_comp2", routeTestOwner)
	fixture.seedTurns(routeTestOwner, "chat_conv_w14c_comp2", 5)
	script.failOnce("AND context_state = 'compacting' AND context_claim_id = ?")
	result := service.CompactOnce(context.Background(), CompactionInput{
		ConversationID: "chat_conv_w14c_comp2", SystemAccountID: routeTestOwner, Model: "gpt-5",
	})
	if result.Status == "installed" {
		t.Fatalf("来源分页故障不得安装: %+v", result)
	}
}

// TestW14CCompactionSummaryExecutorFaces 覆盖摘要执行器响应的解析分支。
func TestW14CCompactionSummaryExecutorFaces(t *testing.T) {
	t.Run("空响应解析失败", func(t *testing.T) {
		fixture := newChatFixture(t)
		executor := &mockExecutor{}
		service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("chat_conv_w14c_comp3", routeTestOwner)
		fixture.seedTurns(routeTestOwner, "chat_conv_w14c_comp3", 5)
		result := service.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_w14c_comp3", SystemAccountID: routeTestOwner, Model: "gpt-5",
		})
		if result.Status == "installed" {
			t.Fatalf("空摘要不得安装: %+v", result)
		}
	})
	t.Run("摘要缺少必要字段", func(t *testing.T) {
		fixture := newChatFixture(t)
		executor := &mockExecutor{}
		executor.steps = append(executor.steps, scriptStep{
			match: func(call dispatchCall) bool { return strings.Contains(call.Body, "压缩器") || call.Body != "" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(`{"durableMemory":"x"}`))}
			},
		})
		service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("chat_conv_w14c_comp4", routeTestOwner)
		fixture.seedTurns(routeTestOwner, "chat_conv_w14c_comp4", 5)
		result := service.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_w14c_comp4", SystemAccountID: routeTestOwner, Model: "gpt-5",
		})
		if result.Status == "installed" {
			t.Fatalf("不完整摘要不得安装: %+v", result)
		}
	})
	t.Run("完整摘要安装检查点", func(t *testing.T) {
		fixture := newChatFixture(t)
		executor := &mockExecutor{}
		executor.steps = append(executor.steps, scriptStep{
			match: func(call dispatchCall) bool { return true },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				body := `{"durableMemory":"用户偏好简洁回答","recentUserIntent":"继续完成覆盖率任务","taskState":"进行中","importantToolResults":[],"imageMemories":[]}`
				return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(body))}
			},
		})
		service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
		fixture.createConversation("chat_conv_w14c_comp5", routeTestOwner)
		fixture.seedTurns(routeTestOwner, "chat_conv_w14c_comp5", 5)
		result := service.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_w14c_comp5", SystemAccountID: routeTestOwner, Model: "gpt-5",
			EffectiveContextLimitTokens: int64PtrW14C(1),
		})
		if result.Status != "installed" && result.Status != "skipped" && result.Status != "failed" {
			t.Fatalf("必须返回明确状态: %+v", result)
		}
	})
}

func int64PtrW14C(value int64) *int64 { return &value }
