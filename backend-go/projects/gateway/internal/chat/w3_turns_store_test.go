package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 轮次与存储窗口的店铺级契约：AcceptTurn 冲突矩阵、替换记账、终结降级、
// 条件停止状态机、消息分页与会话同步头。

// TestAcceptTurnConflictMatrixW3 表驱动覆盖 AcceptTurn 的冲突与幂等分支。
func TestAcceptTurnConflictMatrixW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_accept", "owner")

	t.Run("非法 now", func(t *testing.T) {
		_, err := f.store.AcceptTurn(AcceptTurnInput{ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c0", Now: "bad"})
		if _, ok := err.(*DomainError); !ok {
			t.Fatalf("应返回 DomainError: %v", err)
		}
	})
	t.Run("轮次上限", func(t *testing.T) {
		_, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c1",
			UserContent: "问题", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 0,
		})
		conflict, ok := err.(*ConflictError)
		if !ok || conflict.Code != ConflictTurnLimitExceeded {
			t.Fatalf("应返回轮次上限冲突: %v", err)
		}
	})
	t.Run("容量超限", func(t *testing.T) {
		_, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c2",
			UserContent: "问题", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1, RetentionDays: 30, MaxTurnsPerConversation: 100,
		})
		conflict, ok := err.(*ConflictError)
		if !ok || conflict.Code != ConflictStorageQuotaExceeded {
			t.Fatalf("应返回容量冲突: %v", err)
		}
	})
	t.Run("活动轮冲突与幂等重放", func(t *testing.T) {
		first := f.accept("owner", "conv_accept", "c3", "第一问")
		duplicated, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c3",
			UserContent: "第一问", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
		})
		if err != nil || !duplicated.Duplicate || duplicated.TurnID != first.TurnID {
			t.Fatalf("幂等重放失败: %+v err=%v", duplicated, err)
		}
		if duplicated.UserMessage == nil || duplicated.AssistantMessage == nil {
			t.Fatalf("重放应带回消息对")
		}
		// 活动轮未终结时新提交冲突。
		_, err = f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c5",
			UserContent: "第三问", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
		})
		conflict, ok := err.(*ConflictError)
		if !ok || conflict.Code != ConflictMessageInProgress {
			t.Fatalf("活动轮应冲突: %v", err)
		}
		f.complete("owner", "conv_accept", first.TurnID, "回答一")
	})
	t.Run("替换流", func(t *testing.T) {
		turn1 := f.accept("owner", "conv_accept", "c6", "将被替换")
		f.complete("owner", "conv_accept", turn1.TurnID, "旧回答")
		replacement, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c7",
			UserContent: "替换后", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
			ReplaceTurnID: turn1.TurnID,
		})
		if err != nil {
			t.Fatalf("替换失败: %v", err)
		}
		f.complete("owner", "conv_accept", replacement.TurnID, "新回答")
		list, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_accept", SystemAccountID: "owner", Now: f.nowISO, Limit: 100})
		if err != nil {
			t.Fatalf("列表失败: %v", err)
		}
		for _, message := range list {
			if message.TurnID == turn1.TurnID {
				t.Fatalf("被替换轮次应被删除")
			}
		}
		// 再替换一次校验幂 etc 键删除分支：替换 c7。
		turn2 := replacement
		replacement2, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c8",
			UserContent: "再次替换", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
			ReplaceTurnID: turn2.TurnID,
		})
		if err != nil {
			t.Fatalf("二次替换失败: %v", err)
		}
		f.complete("owner", "conv_accept", replacement2.TurnID, "更新回答")
	})
	t.Run("替换时活动轮冲突", func(t *testing.T) {
		active := f.accept("owner", "conv_accept", "c9", "进行中")
		_, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c10",
			UserContent: "替换", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
			ReplaceTurnID: active.TurnID,
		})
		conflict, ok := err.(*ConflictError)
		if !ok || conflict.Code != ConflictReplaceConflict {
			t.Fatalf("活动轮替换应冲突: %v", err)
		}
		f.complete("owner", "conv_accept", active.TurnID, "完成")
	})
	t.Run("替换不存在的轮次", func(t *testing.T) {
		if _, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_accept", SystemAccountID: "owner", ClientMessageID: "c11",
			UserContent: "替换", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
			ReplaceTurnID: "chat_turn_missing",
		}); err == nil {
			t.Fatalf("替换缺失轮次应报错")
		}
	})
}

// TestFinalizeTurnMatrixW3 覆盖终结路径：完成、失败、取消与存储上限降级。
func TestFinalizeTurnMatrixW3(t *testing.T) {
	t.Run("完成", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_final", "owner")
		accepted := f.accept("owner", "conv_final", "cmid-1", "问题")
		message, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_final", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "回答", FinishReason: "stop", Now: f.nowISO,
		})
		if err != nil || message.Status != StatusCompleted {
			t.Fatalf("完成失败: %+v err=%v", message, err)
		}
		// 终结后再次终结 → 活动回答不存在。
		if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_final", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "again", Now: f.nowISO,
		}); err == nil || !strings.Contains(err.Error(), "活动回答不存在") {
			t.Fatalf("重复终结应报错: %v", err)
		}
	})
	t.Run("存储上限降级为失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_limit", "owner")
		accepted := f.accept("owner", "conv_limit", "cmid-1", "问题")
		bigContent := strings.Repeat("答", int(AssistantStorageReservationBytes))
		_, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_limit", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: bigContent, Now: f.nowISO,
		})
		if _, ok := err.(*AssistantStorageLimitError); !ok {
			t.Fatalf("应返回存储上限错误: %v", err)
		}
		var status, errorCode string
		if err := f.db.QueryRow(`SELECT status, COALESCE(error_code,'') FROM chat_messages WHERE id = ?`, accepted.AssistantMessage.ID).Scan(&status, &errorCode); err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if status != string(StatusFailed) || errorCode != "chat_assistant_storage_limit_exceeded" {
			t.Fatalf("降级状态不正确: %s %s", status, errorCode)
		}
	})
	t.Run("失败与取消", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_fail", "owner")
		accepted := f.accept("owner", "conv_fail", "cmid-1", "问题")
		failed, err := f.store.FailChatTurn(FailTurnInput{
			ConversationID: "conv_fail", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "", ErrorCode: "upstream_http_error", ErrorMessage: "上游失败", Now: f.nowISO,
		})
		if err != nil || failed.Status != StatusFailed || failed.ErrorCode == nil {
			t.Fatalf("失败终结不正确: %+v err=%v", failed, err)
		}
		accepted2 := f.accept("owner", "conv_fail", "cmid-2", "问题二")
		canceled, err := f.store.CancelChatTurn(CancelTurnInput{
			ConversationID: "conv_fail", SystemAccountID: "owner", TurnID: accepted2.TurnID,
			AssistantContent: "", Now: f.nowISO,
		})
		if err != nil || canceled.Status != StatusCanceled {
			t.Fatalf("取消终结不正确: %+v err=%v", canceled, err)
		}
		// 会话不存在。
		if _, err := f.store.FailChatTurn(FailTurnInput{ConversationID: "none", SystemAccountID: "owner", TurnID: "t", Now: f.nowISO}); err == nil {
			t.Fatalf("缺失会话应报错")
		}
	})
}

// TestConditionalStopMatrixW3 覆盖条件停止状态机的全部状态。
func TestConditionalStopMatrixW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_stop", "owner")
	accepted := f.accept("owner", "conv_stop", "cmid-1", "问题")

	t.Run("匹配取消", func(t *testing.T) {
		result, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
		})
		if err != nil || result.State != CancelStateCanceled {
			t.Fatalf("取消失败: %+v err=%v", result, err)
		}
	})
	t.Run("已终结", func(t *testing.T) {
		result, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
		})
		if err != nil || result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusCanceled {
			t.Fatalf("已终结状态不正确: %+v err=%v", result, err)
		}
	})
	t.Run("turn 不匹配与缺失", func(t *testing.T) {
		active := f.accept("owner", "conv_stop", "cmid-2", "问题二")
		mismatch, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: "chat_turn_other", Now: f.nowISO,
		})
		if err != nil || mismatch.State != CancelStateTurnMismatch {
			t.Fatalf("不匹配状态不正确: %+v err=%v", mismatch, err)
		}
		f.complete("owner", "conv_stop", active.TurnID, "回答")
		missing, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: "chat_turn_missing", Now: f.nowISO,
		})
		if err != nil || missing.State != CancelStateNotFound {
			t.Fatalf("缺失状态不正确: %+v err=%v", missing, err)
		}
		if _, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_none", SystemAccountID: "owner", ExpectedTurnID: "t", Now: f.nowISO,
		}); err != nil {
			t.Fatalf("缺失会话应返回 not_found 而非错误: %v", err)
		}
	})
	t.Run("中断模式", func(t *testing.T) {
		interrupted := f.accept("owner", "conv_stop", "cmid-3", "问题三")
		result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: interrupted.TurnID, Now: f.nowISO,
		})
		if err != nil || result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusFailed {
			t.Fatalf("中断状态不正确: %+v err=%v", result, err)
		}
		if _, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_stop", SystemAccountID: "owner", ExpectedTurnID: interrupted.TurnID, Now: "bad",
		}); err == nil {
			t.Fatalf("非法 now 应报错")
		}
	})
}

// TestListMessagesPaginationW3 覆盖消息分页游标语义。
func TestListMessagesPaginationW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_page", "owner")
	f.seedTurns("owner", "conv_page", 3)
	all, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_page", SystemAccountID: "owner", Now: f.nowISO, Limit: 100})
	if err != nil || len(all) != 6 {
		t.Fatalf("全量列表失败: %d err=%v", len(all), err)
	}
	if all[0].SequenceNo != 1 || all[5].SequenceNo != 6 {
		t.Fatalf("默认升序不正确: %d..%d", all[0].SequenceNo, all[5].SequenceNo)
	}
	after := int64(4)
	suffix, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_page", SystemAccountID: "owner", Now: f.nowISO, Limit: 100, AfterSequenceNo: &after})
	if err != nil || len(suffix) != 2 {
		t.Fatalf("after 游标失败: %d err=%v", len(suffix), err)
	}
	before := int64(3)
	prefix, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_page", SystemAccountID: "owner", Now: f.nowISO, Limit: 100, BeforeSequenceNo: &before})
	if err != nil || len(prefix) != 2 {
		t.Fatalf("before 游标失败: %d err=%v", len(prefix), err)
	}
	from := int64(3)
	middle, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_page", SystemAccountID: "owner", Now: f.nowISO, Limit: 2, FromSequenceNo: &from})
	if err != nil || len(middle) != 2 || middle[0].SequenceNo != 3 {
		t.Fatalf("from+limit 失败: %d err=%v", len(middle), err)
	}
	both := append([]*int64{&after}, &before)
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_page", SystemAccountID: "owner", Now: f.nowISO, Limit: 10, AfterSequenceNo: both[0], BeforeSequenceNo: both[1]}); err == nil {
		t.Fatalf("双游标应报错")
	}
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "none", SystemAccountID: "owner", Now: f.nowISO, Limit: 10}); err == nil {
		t.Fatalf("缺失会话应报错")
	}
}

// TestConversationSyncHeadW3 覆盖同步头契约。
func TestConversationSyncHeadW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_sync", "owner")
	accepted := f.accept("owner", "conv_sync", "cmid-1", "问题")
	head, err := f.store.GetConversationSyncHead("conv_sync", "owner", f.nowISO)
	if err != nil {
		t.Fatalf("同步头失败: %v", err)
	}
	if head.ActiveTurnID == nil || *head.ActiveTurnID != accepted.TurnID {
		t.Fatalf("活动轮应可见: %+v", head)
	}
	if head.LastSequenceNo != 2 || len(head.Tail) != 2 {
		t.Fatalf("尾部不正确: %d %d", head.LastSequenceNo, len(head.Tail))
	}
	f.complete("owner", "conv_sync", accepted.TurnID, "回答")
	head2, err := f.store.GetConversationSyncHead("conv_sync", "owner", f.nowISO)
	if err != nil || head2.ActiveTurnID != nil {
		t.Fatalf("完成后活动轮应清空: %+v err=%v", head2, err)
	}
	// 缺失会话返回空同步头而非错误。
	missingHead, err := f.store.GetConversationSyncHead("none", "owner", f.nowISO)
	if err != nil {
		t.Fatalf("缺失会话不应报错: %v", err)
	}
	if missingHead != nil && missingHead.LastSequenceNo != 0 {
		t.Fatalf("缺失会话同步头应为空: %+v", missingHead)
	}
}

// TestConversationLifecycleRoutesW3 覆盖会话更新/删除/清空路由分支。
func TestConversationLifecycleRoutesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_lc", routeTestOwner)
	prefix := "/__aisys__/api/my-chat/conversations/chat_conv_lc"

	t.Run("更新标题与置顶", func(t *testing.T) {
		response := env.do("PATCH", prefix, routeTestOwner, `{"title":"新标题","isPinned":true}`)
		if response.status != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
		if response.dataMap()["title"] != "新标题" {
			t.Fatalf("标题未更新: %v", response.dataMap())
		}
	})
	t.Run("更新校验失败", func(t *testing.T) {
		if response := env.do("PATCH", prefix, routeTestOwner, `{"title":" "}`); response.status != http.StatusBadRequest {
			t.Fatalf("空标题应 400: %d", response.status)
		}
		if response := env.do("PATCH", prefix, routeTestOwner, `{"bogus":1}`); response.status != http.StatusBadRequest {
			t.Fatalf("未知键应 400: %d", response.status)
		}
		if response := env.do("PATCH", prefix, routeTestOwner, `{}`); response.status != http.StatusBadRequest {
			t.Fatalf("空更新应 400: %d", response.status)
		}
	})
	t.Run("清空与删除", func(t *testing.T) {
		env.fixture.seedTurns(routeTestOwner, "chat_conv_lc", 1)
		cleared := env.do("POST", prefix+"/clear", routeTestOwner, "")
		if cleared.status != http.StatusOK {
			t.Fatalf("清空失败: %d %s", cleared.status, cleared.rawString())
		}
		// 其他用户访问。
		if foreign := env.do("GET", prefix, routeTestOther, ""); foreign.status != http.StatusNotFound {
			t.Fatalf("跨用户应 404: %d", foreign.status)
		}
		deleted := env.do("DELETE", prefix, routeTestOwner, "")
		if deleted.status != http.StatusNoContent {
			t.Fatalf("删除失败: %d %s", deleted.status, deleted.rawString())
		}
		if again := env.do("DELETE", prefix, routeTestOwner, ""); again.status != http.StatusNotFound {
			t.Fatalf("重复删除应 404: %d %s", again.status, again.rawString())
		}
	})
	t.Run("会话列表分页", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			env.fixture.createConversation("chat_conv_list_"+string(rune('a'+i)), routeTestOwner)
		}
		response := env.do("GET", "/__aisys__/api/my-chat/conversations", routeTestOwner, "", "limit=2")
		if response.status != http.StatusOK {
			t.Fatalf("列表失败: %d", response.status)
		}
		if len(response.dataArray()) != 2 {
			t.Fatalf("limit 应生效: %d", len(response.dataArray()))
		}
		if bad := env.do("GET", "/__aisys__/api/my-chat/conversations", routeTestOwner, "", "limit=abc"); bad.status != http.StatusOK {
			t.Fatalf("非法 limit 应回退默认: %d", bad.status)
		}
	})
}

// TestGetConversationPayloadW3 覆盖会话详情与工具能力载荷。
func TestGetConversationPayloadW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_detail", routeTestOwner)
	response := env.do("GET", "/__aisys__/api/my-chat/conversations/chat_conv_detail", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("status=%d", response.status)
	}
	data := response.dataMap()
	if data["toolCapabilities"] == nil {
		t.Fatalf("应包含工具能力: %v", data)
	}
	capabilities := data["toolCapabilities"].(map[string]any)
	tools := capabilities["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("工具能力数量 = %d", len(tools))
	}
	if missing := env.do("GET", "/__aisys__/api/my-chat/conversations/none", routeTestOwner, ""); missing.status != http.StatusNotFound {
		t.Fatalf("缺失会话应 404: %d", missing.status)
	}
}

// TestSubmissionAndContextStatusW3 覆盖提交状态与上下文状态路由。
func TestSubmissionAndContextStatusW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_status", routeTestOwner)
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_status", "cmid-1", "问题")
	prefix := "/__aisys__/api/my-chat/conversations/chat_conv_status"
	submission := env.do("GET", prefix+"/submissions/cmid-1", routeTestOwner, "")
	if submission.status != http.StatusOK || submission.dataMap()["turnId"] != accepted.TurnID {
		t.Fatalf("提交状态不正确: %d %s", submission.status, submission.rawString())
	}
	if missing := env.do("GET", prefix+"/submissions/cmid-404", routeTestOwner, ""); missing.status != http.StatusOK || missing.dataMap() == nil {
		t.Fatalf("缺失提交应返回 200 空载荷: %d %s", missing.status, missing.rawString())
	}
	context := env.do("GET", prefix+"/context-status", routeTestOwner, "")
	if context.status != http.StatusOK || context.dataMap() == nil {
		t.Fatalf("上下文状态失败: %d %s", context.status, context.rawString())
	}
}

// TestImagePolicyRouteW3 覆盖图片策略路由。
func TestImagePolicyRouteW3(t *testing.T) {
	env := newGenerationEnv(t)
	response := env.do("GET", "/__aisys__/api/my-chat/image-policy", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("status=%d", response.status)
	}
	policy := response.dataMap()["input"].(map[string]any)
	if policy["mimeType"] != "image/webp" || policy["maxBytes"] == nil || policy["maxEdge"] == nil {
		t.Fatalf("策略载荷不完整: %v", policy)
	}
	if unauthenticated := env.do("GET", "/__aisys__/api/my-chat/image-policy", "", ""); unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", unauthenticated.status)
	}
}

// TestMapMessageRowW3 覆盖消息行映射的边界。
func TestMapMessageRowW3(t *testing.T) {
	raw := json.RawMessage(`[{"type":"input_text","order":0,"text":"hi"}]`)
	f := newChatFixture(t)
	f.createConversation("conv_map", "owner")
	accepted := f.accept("owner", "conv_map", "cmid-1", "带块问题")
	f.complete("owner", "conv_map", accepted.TurnID, "回答")
	messages, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_map", SystemAccountID: "owner", Now: f.nowISO, Limit: 10})
	if err != nil || len(messages) != 2 {
		t.Fatalf("列表失败: %v", err)
	}
	if messages[0].ContentBlocks == nil {
		t.Fatalf("输入块应解析: %+v", messages[0])
	}
	_ = raw
}
