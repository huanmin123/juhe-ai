package chat

import (
	"context"
	"testing"
)
// TestRecoverChatTurnFinalizationW3 覆盖终结恢复路径。
func TestRecoverChatTurnFinalizationW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_recover", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_recover", "cmid-1", "问题")
	env.fixture.complete(routeTestOwner, "chat_conv_recover", accepted.TurnID, "回答")
	status := rt.recoverChatTurnFinalization("chat_conv_recover", routeTestOwner, accepted.TurnID, "cmid-1", "", context.DeadlineExceeded)
	if status != string(StatusCompleted) {
		t.Fatalf("已终结轮次应返回其状态: %s", status)
	}
	// 无法恢复 → 迭代后返回 failed。
	unknown := rt.recoverChatTurnFinalization("chat_conv_recover", routeTestOwner, "chat_turn_missing", "cmid-missing", "", context.DeadlineExceeded)
	if unknown != string(StatusFailed) {
		t.Fatalf("未知轮次应返回 failed: %s", unknown)
	}
}

// TestMergeStableToolFieldW3 覆盖工具字段合并契约。
func TestMergeStableToolFieldW3(t *testing.T) {
	if got := mergeStableToolField("", "abc"); got != "abc" {
		t.Fatalf("空值合并失败: %q", got)
	}
	if got := mergeStableToolField("abc", "abc"); got != "abc" {
		t.Fatalf("相同值应去重: %q", got)
	}
	if got := mergeStableToolField("abc", "def"); got != "abcdef" {
		t.Fatalf("增量应拼接: %q", got)
	}
	if got := mergeStableToolField("abcdef", "def"); got != "abcdef" {
		t.Fatalf("后缀重复应忽略: %q", got)
	}
	if got := mergeStableToolField("abc", ""); got != "abc" {
		t.Fatalf("空块应忽略: %q", got)
	}
}

// TestGenerationParameterProvidersW3 补充供应商参数表分支。
func TestGenerationParameterProvidersW3(t *testing.T) {
	deepseek := generationParameterCapabilitiesForModel("deepseek", "deepseek-reasoner", nil)
	if len(deepseek["chat_completions"]) != 1 {
		t.Fatalf("非 chat 模型应只保留 maxOutputTokens: %v", deepseek)
	}
	xai := generationParameterCapabilitiesForModel("xai", "grok-4", nil)
	if len(xai["chat_completions"]) != 6 {
		t.Fatalf("非 reasoning 模型应支持全部参数: %d", len(xai["chat_completions"]))
	}
	anthropic := generationParameterCapabilitiesForModel("anthropic", "claude-sonnet-4.6", nil)
	if len(anthropic["chat_completions"]) != 3 {
		t.Fatalf("常规 claude 应保留三参数: %d", len(anthropic["chat_completions"]))
	}
	gemini := generationParameterCapabilitiesForModel("gemini", "gemini-2.5-pro", nil)
	if len(gemini["chat_completions"]) != 3 || len(gemini["responses"]) != 3 {
		t.Fatalf("常规 gemini 应支持三参数: %v", gemini)
	}
	glmEntry, _ := findCapability(generationParameterCapabilitiesForModel("glm", "glm-5", nil)["chat_completions"], "temperature")
	if glmEntry.Max != 1 {
		t.Fatalf("glm temperature 上限应为 1: %+v", glmEntry)
	}
	// 未知参数名走零值能力。
	if caps := generationParameterCapabilitiesForModel("gpt", "gpt-5", nil); caps != nil {
		if _, ok := caps["chat_completions"]; !ok {
			t.Fatalf("gpt-5 应有 chat 表")
		}
	}
	// 路由能力：禁用映射跳过、上游桥只保留桥接参数。
	enabled := true
	bridged := ChatTransportAccount{
		ID: "a", ProviderCode: "deepseek", Type: "api_key",
		ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "responses", UpstreamModel: "gpt-5"}},
	}
	kept, ok := routeCapabilityForAccount(bridged, ChatGenerationParameterCapability{Parameter: "temperature"}, "gpt-5", ProtocolChatCompletions)
	if !ok || kept.Parameter != "temperature" {
		t.Fatalf("桥接应保留 temperature: %+v %v", kept, ok)
	}
	_, seedOK := routeCapabilityForAccount(bridged, ChatGenerationParameterCapability{Parameter: "seed"}, "gpt-5", ProtocolChatCompletions)
	if seedOK {
		t.Fatalf("桥接不应保留 seed")
	}
	sameModel := ChatTransportAccount{
		ID: "b", ProviderCode: "deepseek", Type: "api_key",
		ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "deepseek-chat", SourceEndpointFamily: "chat_completions", UpstreamModel: "deepseek-reasoner"}},
	}
	_, freqOK := routeCapabilityForAccount(sameModel, ChatGenerationParameterCapability{Parameter: "frequencyPenalty"}, "deepseek-chat", ProtocolChatCompletions)
	if freqOK {
		t.Fatalf("上游模型不支持该参数时应丢弃")
	}
	unmapped := ChatTransportAccount{ID: "c", ProviderCode: "deepseek", Type: "api_key"}
	if _, ok := routeCapabilityForAccount(unmapped, ChatGenerationParameterCapability{Parameter: "temperature"}, "gpt-5", ProtocolChatCompletions); !ok {
		t.Fatalf("无映射账户应原样保留能力")
	}
}

// TestStorageWindowStrictBranchesW3 覆盖存储窗口的严格错误分支。
func TestStorageWindowStrictBranchesW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_window", "owner")
	if err := f.store.incrementStorageWindow(f.db, "owner", "bad", 1, 1); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if err := f.store.settleStorageWindowReservationStrict(f.db, "owner", f.nowISO, 100, 200, f.nowISO); err == nil {
		t.Fatalf("内容超出预留应报错")
	}
	if err := f.store.settleStorageWindowReservationStrict(f.db, "owner", "bad", 100, 10, f.nowISO); err == nil {
		t.Fatalf("非法 created_at 应报错")
	}
	if err := f.store.settleStorageWindowReservationStrict(f.db, "owner", f.nowISO, 100, 10, f.nowISO); err == nil {
		t.Fatalf("缺失日桶应报错")
	}
	if err := f.store.releaseStorageWindowReservationStrict(f.db, "owner", f.nowISO, 100, f.nowISO); err == nil {
		t.Fatalf("缺失预留应报错")
	}
	if err := f.store.releaseStorageWindowReservationStrict(f.db, "owner", "bad", 100, f.nowISO); err == nil {
		t.Fatalf("非法 created_at 应报错")
	}
	if err := f.store.decrementStorageWindowStrict(f.db, "owner", "2026-03-10", 10, f.nowISO); err == nil {
		t.Fatalf("缺失日桶应报错")
	}
	if _, err := f.store.recentStorageBytes(f.db, "owner", "bad", 30); err == nil {
		t.Fatalf("非法 now 应报错")
	}
}

// TestPoisonSchemaErrorsW3 以删表模拟 schema 损坏，覆盖事务内错误延续与回滚。
func TestPoisonSchemaErrorsW3(t *testing.T) {
	t.Run("chat_messages 缺失", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_poison", "owner")
		if _, err := f.db.Exec(`DROP TABLE chat_messages`); err != nil {
			t.Fatalf("删表失败: %v", err)
		}
		if _, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_poison", SystemAccountID: "owner", ClientMessageID: "c1",
			UserContent: "问题", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
		}); err == nil {
			t.Fatalf("缺表时 AcceptTurn 应报错")
		}
		if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "conv_poison", SystemAccountID: "owner", Now: f.nowISO, Limit: 1}); err == nil {
			t.Fatalf("缺表时 ListMessages 应报错")
		}
		if _, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_poison", SystemAccountID: "owner", ExpectedTurnID: "t", Now: f.nowISO,
		}); err == nil {
			t.Fatalf("缺表时条件停止应报错")
		}
		if _, err := f.store.FindTurnByClientMessageID("conv_poison", "owner", "c1"); err == nil {
			t.Fatalf("缺表时幂等查询应报错")
		}
	})
	t.Run("chat_user_storage_windows 缺失", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_poison2", "owner")
		if _, err := f.db.Exec(`DROP TABLE chat_user_storage_windows`); err != nil {
			t.Fatalf("删表失败: %v", err)
		}
		// 消息插入成功但窗口递增失败 → 事务回滚。
		if _, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_poison2", SystemAccountID: "owner", ClientMessageID: "c1",
			UserContent: "问题", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
		}); err == nil {
			t.Fatalf("窗口表缺失应报错")
		}
		var count int
		// 表已删除则查询报错，同样视为回滚证据。
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM chat_messages`).Scan(&count); err == nil && count != 0 {
			t.Fatalf("失败事务应回滚: %d", count)
		}
	})
}
