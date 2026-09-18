package chat

// w14l 覆盖率收尾（三）：流式路由的上下文装载上限→压缩重试链、请求体
// 超限→压缩重试链与压缩不可用时的硬拒绝臂。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// w14lSeedBigTurns 直接落库 n 轮大内容已完成轮次（绕过 store 的 256KiB
// 内容上限，仅服务装载/序列化上限测试）。
func w14lSeedBigTurns(t *testing.T, f *chatFixture, conversationID, content string, n int) {
	t.Helper()
	created := "2026-03-10T08:00:00.000Z"
	expires := "2027-03-10T08:00:00.000Z"
	for i := 1; i <= n; i++ {
		turnID := fmt.Sprintf("chat_turn_w14l_big_%d", i)
		userSeq := int64(2*i - 1)
		if _, err := f.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id,
			sequence_no, client_message_id, role, status, content_text, content_blocks_json, content_bytes,
			model, created_at, completed_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, 'user', 'completed', ?, '[]', ?, 'gpt-5', ?, ?, ?)`,
			fmt.Sprintf("chat_msg_w14l_u_%d", i), conversationID, routeTestOwner, turnID,
			userSeq, "w14l-big-cmid-"+fmt.Sprint(i), content, len(content), created, created, expires); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id,
			sequence_no, client_message_id, role, status, content_text, content_blocks_json, content_bytes,
			model, created_at, completed_at, expires_at)
			VALUES (?, ?, ?, ?, ?, NULL, 'assistant', 'completed', '回答', '[]', 6, 'gpt-5', ?, ?, ?)`,
			fmt.Sprintf("chat_msg_w14l_a_%d", i), conversationID, routeTestOwner, turnID,
			userSeq+1, created, created, expires); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(`UPDATE chat_conversations SET next_sequence_no = ?, user_turn_count = ? WHERE id = ?`,
		int64(2*n+1), n, conversationID); err != nil {
		t.Fatal(err)
	}
}

// w14lWaitIdle 等待会话活跃轮清空（运行器收口）。
func w14lWaitIdle(t *testing.T, f *chatFixture, conversationID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active sql.NullString
		if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err == nil && !active.Valid {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("会话未在时限内收口")
}

// fixtureTailTurnID 返回最近一条用户轮次 ID。
func fixtureTailTurnID(t *testing.T, f *chatFixture, conversationID string) string {
	t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT turn_id FROM chat_messages WHERE conversation_id = ? AND role = 'user'
		ORDER BY sequence_no DESC LIMIT 1`, conversationID).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	return turnID
}

// TestW14LStreamContextLimitCompactionRetry 装载超限 → 压缩一次 → 重试装载
// 成功 → 正常出流。
func TestW14LStreamContextLimitCompactionRetry(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_w14l_limit"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	// 压缩摘要走 executor 步骤（按目的头匹配）。
	env.executor.steps = append(env.executor.steps, w13g4CompactionStep(`{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`))
	big := strings.Repeat("史", 2200*1024)
	w14lSeedBigTurns(t, fixture, conversationID, big, 8)
	// 小尾轮：压缩覆盖大轮后，剩余后缀的 token 估算可以通过预算。
	fixture.accept(routeTestOwner, conversationID, "w14l-limit-tail", "尾问")
	fixture.complete(routeTestOwner, conversationID, fixtureTailTurnID(t, fixture, conversationID), "尾答")

	recorder := w14lStreamPostRaw(newChatRoutesForTest(env.deps), conversationID,
		streamPayload("w14l-limit-cmid", "问题", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.started") {
		t.Fatalf("压缩重试后应成功出流：%d %s", recorder.Code, body[:min(len(body), 300)])
	}
}

// w14lNoLimitCatalog 去掉上下文窗口限制，令请求体大小臂可独立触发。
type w14lNoLimitCatalog struct{ inner ModelCatalog }

func (c w14lNoLimitCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	return c.inner.ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily)
}

func (c w14lNoLimitCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	items := c.inner.ListProviderCatalog(providerCode, systemAccountID)
	for i := range items {
		items[i].ContextWindowTokens = nil
	}
	return items
}

// TestW14LStreamBodyBudgetCompactionRetry 请求体超限但装载未超限 → 压缩
// 一次 → 重建请求体成功。  在 JSON 序列化中固定放大 2 倍。
func TestW14LStreamBodyBudgetCompactionRetry(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_w14l_budget"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	env.deps.ModelCatalog = w14lNoLimitCatalog{inner: mockModelCatalog{}}
	env.executor.steps = append(env.executor.steps, w13g4CompactionStep(`{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`))
	big := strings.Repeat(" ", 750*1024)
	w14lSeedBigTurns(t, fixture, conversationID, big, 5)
	fixture.accept(routeTestOwner, conversationID, "w14l-budget-tail", "尾问")
	fixture.complete(routeTestOwner, conversationID, fixtureTailTurnID(t, fixture, conversationID), "尾答")

	recorder := w14lStreamPostRaw(newChatRoutesForTest(env.deps), conversationID,
		streamPayload("w14l-budget-cmid", "问题", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.started") {
		t.Fatalf("预算重试后应成功出流：%d %s", recorder.Code, body[:min(len(body), 300)])
	}
}

// TestW14LStreamBodyTooLargeWithoutCompaction 压缩端口不可用 → 硬拒绝臂。
func TestW14LStreamBodyTooLargeWithoutCompaction(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_w14l_toolarge"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	env.deps.ModelCatalog = w14lNoLimitCatalog{inner: mockModelCatalog{}}
	env.deps.Compactions = nil
	big := strings.Repeat(" ", 750*1024)
	w14lSeedBigTurns(t, fixture, conversationID, big, 5)

	recorder := w14lStreamPostRaw(newChatRoutesForTest(env.deps), conversationID,
		streamPayload("w14l-toolarge-cmid", "问题", "gpt-5"), routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "chat_request_body_too_large") &&
		!strings.Contains(recorder.Body.String(), "安全上限") {
		t.Fatalf("应拒绝超大请求体：%d %s", recorder.Code, recorder.Body.String()[:min(300, recorder.Body.Len())])
	}
}

var _ = context.Background
var _ = httptest.NewRecorder
