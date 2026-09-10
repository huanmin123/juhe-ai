package chat

import (
	"strings"
	"testing"
)

// 语句级故障注入：用 SQLite 触发器 RAISE(ABORT) 让事务中段的特定语句失败，
// 覆盖各存储方法内部的错误延续与回滚分支（生产代码不变，等价于数据库层
// 报错场景）。

type failTriggerW3 struct {
	name    string
	trigger string
}

func installFailTriggerW3(t *testing.T, f *chatFixture, target failTriggerW3) {
	t.Helper()
	if _, err := f.db.Exec(target.trigger); err != nil {
		t.Fatalf("安装触发器失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS ` + target.name)
	})
}

func acceptInputW3(clientMessageID string) AcceptTurnInput {
	return AcceptTurnInput{
		ConversationID: "conv_trigger", SystemAccountID: "owner", ClientMessageID: clientMessageID,
		UserContent: "问题", Model: "gpt-5", Now: "2026-03-10T08:00:00.000Z",
		StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
	}
}

// TestAcceptTurnMidFlowFailuresW3 覆盖 AcceptTurn 各写入点的失败延续。
func TestAcceptTurnMidFlowFailuresW3(t *testing.T) {
	cases := []struct {
		name    string
		trigger failTriggerW3
	}{
		{"用户消息插入失败", failTriggerW3{"fail_user_msg", `CREATE TRIGGER fail_user_msg BEFORE INSERT ON chat_messages WHEN NEW.role = 'user' BEGIN SELECT RAISE(ABORT, 'injected'); END`}},
		{"助手消息插入失败", failTriggerW3{"fail_asst_msg", `CREATE TRIGGER fail_asst_msg BEFORE INSERT ON chat_messages WHEN NEW.role = 'assistant' BEGIN SELECT RAISE(ABORT, 'injected'); END`}},
		{"幂等键插入失败", failTriggerW3{"fail_idem", `CREATE TRIGGER fail_idem BEFORE INSERT ON chat_message_idempotency BEGIN SELECT RAISE(ABORT, 'injected'); END`}},
		{"容量窗口递增失败", failTriggerW3{"fail_window", `CREATE TRIGGER fail_window BEFORE UPDATE ON chat_user_storage_windows BEGIN SELECT RAISE(ABORT, 'injected'); END`}},
		{"会话更新失败", failTriggerW3{"fail_conv", `CREATE TRIGGER fail_conv BEFORE UPDATE ON chat_conversations BEGIN SELECT RAISE(ABORT, 'injected'); END`}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newChatFixture(t)
			f.createConversation("conv_trigger", "owner")
			// 容量窗口 UPDATE 触发器要求已存在日桶行：先成功写入并完成一轮。
			if testCase.trigger.name == "fail_window" {
				warmup := f.accept("owner", "conv_trigger", "cmid-0", "预热")
				f.complete("owner", "conv_trigger", warmup.TurnID, "回答")
			}
			installFailTriggerW3(t, f, testCase.trigger)
			if _, err := f.store.AcceptTurn(acceptInputW3("cmid-1")); err == nil {
				t.Fatalf("注入失败未生效")
			}
			// 回滚完整性：会话仍无可提交痕迹。
			var active any
			if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = 'conv_trigger'`).Scan(&active); err != nil || active != nil {
				t.Fatalf("失败事务应完全回滚: %v %v", active, err)
			}
		})
	}
	t.Run("容量窗口插入失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		if _, err := f.db.Exec(`DELETE FROM chat_user_storage_windows`); err != nil {
			t.Fatalf("清理失败: %v", err)
		}
		installFailTriggerW3(t, f, failTriggerW3{"fail_window_ins", `CREATE TRIGGER fail_window_ins BEFORE INSERT ON chat_user_storage_windows BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.AcceptTurn(acceptInputW3("cmid-1")); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
}

// TestFinalizeAndStopMidFlowFailuresW3 覆盖终结与条件停止的中段失败。
func TestFinalizeAndStopMidFlowFailuresW3(t *testing.T) {
	t.Run("终结消息更新失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		accepted := f.accept("owner", "conv_trigger", "cmid-1", "问题")
		installFailTriggerW3(t, f, failTriggerW3{"fail_final", `CREATE TRIGGER fail_final BEFORE UPDATE ON chat_messages BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_trigger", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "回答", Now: f.nowISO,
		}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("终结容量结算失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		accepted := f.accept("owner", "conv_trigger", "cmid-1", "问题")
		installFailTriggerW3(t, f, failTriggerW3{"fail_settle", `CREATE TRIGGER fail_settle BEFORE UPDATE ON chat_user_storage_windows BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_trigger", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "回答", Now: f.nowISO,
		}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("终结会话收口失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		accepted := f.accept("owner", "conv_trigger", "cmid-1", "问题")
		installFailTriggerW3(t, f, failTriggerW3{"fail_conv_final", `CREATE TRIGGER fail_conv_final BEFORE UPDATE ON chat_conversations BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: "conv_trigger", SystemAccountID: "owner", TurnID: accepted.TurnID,
			AssistantContent: "回答", Now: f.nowISO,
		}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("条件停止消息更新失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		accepted := f.accept("owner", "conv_trigger", "cmid-1", "问题")
		installFailTriggerW3(t, f, failTriggerW3{"fail_stop", `CREATE TRIGGER fail_stop BEFORE UPDATE ON chat_messages BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_trigger", SystemAccountID: "owner", ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
		}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
}

// TestConversationLifecycleMidFlowFailuresW3 覆盖清空/删除的中段失败。
func TestConversationLifecycleMidFlowFailuresW3(t *testing.T) {
	t.Run("清空消息删除失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		f.seedTurns("owner", "conv_trigger", 1)
		installFailTriggerW3(t, f, failTriggerW3{"fail_clear", `CREATE TRIGGER fail_clear BEFORE DELETE ON chat_messages BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "conv_trigger", SystemAccountID: "owner", Now: f.nowISO}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("删除会话失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		installFailTriggerW3(t, f, failTriggerW3{"fail_del", `CREATE TRIGGER fail_del BEFORE DELETE ON chat_conversations BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.DeleteConversation("conv_trigger", "owner"); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("资产过期更新失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		// 先造一条资产行，让 expireChatAssetsForConversation 的 UPDATE 命中行。
		if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
			SystemAccountID: "owner", ConversationID: "conv_trigger", SourceKind: "user_upload",
			OriginalBytes: 10, OriginalSha256: strings.Repeat("e", 64), QuotaBytes: 10, Now: f.nowISO, RetentionDays: 30,
		}); err != nil {
			t.Fatalf("造资产失败: %v", err)
		}
		installFailTriggerW3(t, f, failTriggerW3{"fail_expire", `CREATE TRIGGER fail_expire BEFORE UPDATE ON chat_assets BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "conv_trigger", SystemAccountID: "owner", Now: f.nowISO}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
	t.Run("替换轮次删除失败", func(t *testing.T) {
		f := newChatFixture(t)
		f.createConversation("conv_trigger", "owner")
		turn1 := f.accept("owner", "conv_trigger", "cmid-1", "第一问")
		f.complete("owner", "conv_trigger", turn1.TurnID, "回答")
		installFailTriggerW3(t, f, failTriggerW3{"fail_replace", `CREATE TRIGGER fail_replace BEFORE DELETE ON chat_messages BEGIN SELECT RAISE(ABORT, 'injected'); END`})
		if _, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "conv_trigger", SystemAccountID: "owner", ClientMessageID: "cmid-2",
			UserContent: "替换", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
			ReplaceTurnID: turn1.TurnID,
		}); err == nil {
			t.Fatalf("注入失败未生效")
		}
	})
}

// TestCompactionTriggerFailureW3 覆盖压缩请求/认领的失败延续。
func TestCompactionTriggerFailureW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_trigger", "owner")
	f.seedTurns("owner", "conv_trigger", 2)
	// context_revision 不匹配 → 请求被拒绝 → skipped compaction_conflict。
	requested, err := f.store.RequestContextCompaction(RequestCompactionInput{
		ConversationID: "conv_trigger", SystemAccountID: "owner",
		ExpectedRevision: 999, SourceThroughSequence: 3, Now: f.nowISO,
	})
	if err != nil {
		t.Fatalf("请求压缩失败: %v", err)
	}
	if requested {
		t.Fatalf("版本不匹配应拒绝")
	}
	// 认领失败路径：claim 竞争（revision 不匹配）→ nil。
	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "conv_trigger", SystemAccountID: "owner",
		ExpectedRevision: 999, SourceThroughSequence: 3, Now: f.nowISO,
		StaleClaimBefore: f.nowISO,
	})
	if err != nil {
		t.Fatalf("认领不应报错: %v", err)
	}
	if claim != nil {
		t.Fatalf("版本不匹配认领应为 nil")
	}
	// RecordCompactionProgress 对未知认领返回 false。
	progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: "conv_trigger", SystemAccountID: "owner",
		ClaimID: "chat_claim_missing", ThroughSequence: 1, EarliestExpiresAt: f.nowISO, Now: f.nowISO,
	})
	if err != nil {
		t.Fatalf("进度记录不应报错: %v", err)
	}
	if progressed {
		t.Fatalf("未知认领不应推进")
	}
}

// TestRecordContextUsageBranchesW3 覆盖用量记录的冲突分支。
func TestRecordContextUsageBranchesW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_usage", "owner")
	accepted := f.accept("owner", "conv_usage", "cmid-1", "问题")
	_ = accepted
	// 正确 revision。
	head, err := f.store.GetContextHead("conv_usage", "owner")
	if err != nil {
		t.Fatalf("读取头失败: %v", err)
	}
	limit := int64(100000)
	ok, err := f.store.RecordContextUsage(RecordContextUsageInput{
		ConversationID: "conv_usage", SystemAccountID: "owner",
		ExpectedContextRevision: head.ContextRevision, ActiveContextTokens: 100,
		EffectiveContextLimitTokens: &limit, UsageEstimated: true, Now: f.nowISO,
	})
	if err != nil || !ok {
		t.Fatalf("记录失败: %v err=%v", ok, err)
	}
	// 错误 revision → 拒绝。
	rejected, err := f.store.RecordContextUsage(RecordContextUsageInput{
		ConversationID: "conv_usage", SystemAccountID: "owner",
		ExpectedContextRevision: head.ContextRevision + 99, ActiveContextTokens: 100,
		Now: f.nowISO,
	})
	if err != nil {
		t.Fatalf("拒绝不应报错: %v", err)
	}
	if rejected {
		t.Fatalf("错误版本应拒绝")
	}
}

// TestFailCompactionAndPendingW3 覆盖压缩失败登记与待处理失败。
func TestFailCompactionAndPendingW3(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("conv_compact_fail", "owner")
	// FailCompaction / FailPendingCompaction 对无压缩态会话为 no-op。
	retryAt := f.nowISO
	if _, err := f.store.FailCompaction(FailCompactionInput{
		ConversationID: "conv_compact_fail", SystemAccountID: "owner",
		ClaimID: "chat_claim_missing", ErrorCode: "injected", RetryAt: &retryAt, Now: f.nowISO,
	}); err != nil {
		t.Fatalf("FailCompaction 不应报错: %v", err)
	}
	if _, err := f.store.FailPendingCompaction(FailPendingCompactionInput{
		ConversationID: "conv_compact_fail", SystemAccountID: "owner",
		ExpectedRevision: 0, ErrorCode: "injected", RetryAt: &retryAt, Now: f.nowISO,
	}); err != nil {
		t.Fatalf("FailPendingCompaction 不应报错: %v", err)
	}
}
