package chat

// w10d 覆盖收尾：store 层单条语句故障注入（脚本化 driver）。
//
// 断连（关闭数据库）只能覆盖首条语句失败；本文件按查询子串注入一次性/
// 第 n 次失败，驱动 store 方法中后置的真实可失败 DB 错误臂。每个用例：
// 注入后调用必须失败，故障消耗后重放必须恢复（证明一次性语义且回滚干净）。

import (
	"testing"
)

// runFaultCaseW10D 注入 → 调用失败 → 故障消耗后重放恢复。
// persistent 用例（持久数据腐化臂）两次调用都必须失败。
func runFaultCaseW10D(t *testing.T, name string, seed func(f *chatFixture, script *faultScriptW10D), act func(f *chatFixture) error) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		if seed != nil {
			seed(fixture, script)
		}
		if err := act(fixture); err == nil {
			t.Fatalf("注入故障未生效")
		}
		if err := act(fixture); err == nil {
			t.Fatalf("故障消耗后未恢复")
		}
	})
}

// runRecoverableFaultCaseW10D 注入 → 调用失败 → 重放恢复。
func runRecoverableFaultCaseW10D(t *testing.T, name string, seed func(f *chatFixture, script *faultScriptW10D), act func(f *chatFixture) error) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		fixture, script := newFaultChatFixtureW10D(t)
		if seed != nil {
			seed(fixture, script)
		}
		if err := act(fixture); err == nil {
			t.Fatalf("注入故障未生效")
		}
		if err := act(fixture); err != nil {
			t.Fatalf("故障消耗后未恢复: %v", err)
		}
	})
}

// acceptTurnInputW10D 构造普通接收输入。
func acceptTurnInputW10D(f *chatFixture, conversationID, cmid string) AcceptTurnInput {
	return AcceptTurnInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner, ClientMessageID: cmid,
		UserContent: "问题内容", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 1 << 30, RetentionDays: 30, MaxTurnsPerConversation: 100,
	}
}

// acceptReplacementInputW10D 构造替换轮次输入。
func acceptReplacementInputW10D(f *chatFixture, conversationID, cmid, replaceTurnID string) AcceptTurnInput {
	input := acceptTurnInputW10D(f, conversationID, cmid)
	input.ReplaceTurnID = replaceTurnID
	return input
}

// TestW10DAcceptTurnFaultArms 覆盖 AcceptTurn 普通路径的存储错误臂。
func TestW10DAcceptTurnFaultArms(t *testing.T) {
	cases := []struct {
		name   string
		substr string
		nth    int
		commit bool
	}{
		{"会话锁查询失败", "FROM chat_conversations", 1, false},
		{"幂等查询失败", "FROM chat_message_idempotency", 1, false},
		{"容量统计失败", "bucket_date >= ?", 1, false},
		{"用户消息写入失败", "'user', 'completed', ?, ?, ?, ?, ?, ?, ?)", 1, false},
		{"助手消息写入失败", "'assistant', 'streaming', '', 0, ?, ?, ?, ?)", 1, false},
		{"幂等写入失败", "INSERT INTO chat_message_idempotency", 1, false},
		{"容量窗口递增失败", "excluded.content_bytes", 1, false},
		{"会话头更新失败", "SET title = CASE WHEN next_sequence_no = 1", 1, false},
		{"轮次对读取失败", "ORDER BY sequence_no ASC", 1, false},
		{"提交失败", commitFaultKeyW10D, 0, true},
	}
	for _, item := range cases {
		runRecoverableFaultCaseW10D(t, "AcceptTurn/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_accept", routeTestOwner)
			if item.commit {
				script.failOnce(item.substr)
			} else if item.nth == 1 {
				script.failOnce(item.substr)
			} else {
				script.failNth(item.substr, item.nth)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.AcceptTurn(acceptTurnInputW10D(f, "conv_w10d_accept", "cmid-w10d"))
			return err
		})
	}
}

// TestW10DAcceptTurnReplacementFaultArms 覆盖替换路径的存储错误臂。
func TestW10DAcceptTurnReplacementFaultArms(t *testing.T) {
	cases := []struct {
		name   string
		substr string
		nth    int
	}{
		{"替换轮次读取失败", "ORDER BY sequence_no ASC", 1},
		{"替换最大序号失败", "MAX(sequence_no)", 1},
		{"替换幂等行读取失败", "WHERE conversation_id = ? AND system_account_id = ? AND turn_id = ?", 1},
		{"替换容量统计失败", "bucket_date >= ?", 1},
		{"替换容量窗口扣减失败", "SET content_bytes = content_bytes - ?", 1},
		{"替换幂等删除失败", "DELETE FROM chat_message_idempotency", 1},
		{"用户资产保留更新失败", "AND source_kind = 'user_upload'", 1},
		{"用户资产解绑失败", "SET turn_id = NULL, message_id = NULL", 1},
		{"用户输入资产读取失败", "reference_kind = 'user_input' AND expires_at > ?", 1},
		{"资产引用删除失败", "DELETE FROM chat_asset_references AS reference", 1},
		{"助手资产保留更新失败", "SET expires_at = CASE WHEN expires_at > ?", 2},
		{"助手资产解绑失败", "SET turn_id = NULL, message_id = NULL", 2},
		{"替换消息删除失败", "DELETE FROM chat_messages", 1},
		{"替换会话头更新失败", "SET title = CASE WHEN title_source_message_id = ?", 1},
	}
	for _, item := range cases {
		runRecoverableFaultCaseW10D(t, "AcceptTurnReplace/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_replace", routeTestOwner)
			turn := f.accept(routeTestOwner, "conv_w10d_replace", "cmid-base", "第一问")
			f.complete(routeTestOwner, "conv_w10d_replace", turn.TurnID, "第一答")
			if item.nth == 1 {
				script.failOnce(item.substr)
			} else {
				script.failNth(item.substr, item.nth)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.AcceptTurn(acceptReplacementInputW10D(f, "conv_w10d_replace", "cmid-w10d-replace", w10dReplaceTurnID(f)))
			return err
		})
	}
}

// w10dReplaceTurnID 读取替换基准轮次 ID。
func w10dReplaceTurnID(f *chatFixture) string {
	f.t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT turn_id FROM chat_messages WHERE role = 'user' LIMIT 1`).Scan(&turnID); err != nil {
		f.t.Fatalf("读取替换轮次失败: %v", err)
	}
	return turnID
}

// TestW10DFinalizeTurnFaultArms 覆盖回答终结的存储错误臂。
func TestW10DFinalizeTurnFaultArms(t *testing.T) {
	cases := []struct {
		name       string
		substr     string
		persistent bool
		setup      func(f *chatFixture, turnID string)
	}{
		{"会话锁查询失败", "FROM chat_conversations", false, nil},
		{"流式回答查询失败", "AND role = 'assistant' AND status = 'streaming'", false, nil},
		{"消息更新失败", "SET status = ?, content_text = ?", false, nil},
		{"预留结算失败", "SET content_bytes = content_bytes + ?", false, nil},
		{"会话收口失败", "SET active_turn_id = NULL, active_started_at = NULL", false, nil},
		{"轮次对读取失败", "ORDER BY sequence_no ASC", false, nil},
		{"提交失败", commitFaultKeyW10D, false, nil},
		{"流式回答缺失", "", true, func(f *chatFixture, turnID string) {
			// 助手已终结但会话仍指向该轮次 → ErrNoRows → 活动回答不存在。
			// CHECK 要求非 streaming 助手消息 reserved = 0。
			if _, err := f.db.Exec(`UPDATE chat_messages SET status = 'failed', storage_reserved_bytes = 0 WHERE turn_id = ? AND role = 'assistant'`, turnID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"预留不一致", "", true, func(f *chatFixture, turnID string) {
			// streaming 助手消息预留必须 > 0；改成非预留值触发预留校验错误。
			if _, err := f.db.Exec(`UPDATE chat_messages SET storage_reserved_bytes = 1 WHERE turn_id = ? AND role = 'assistant'`, turnID); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, item := range cases {
		item := item
		var turnID string
		runner := runRecoverableFaultCaseW10D
		if item.persistent {
			runner = runFaultCaseW10D
		}
		runner(t, "FinalizeTurn/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_fin", routeTestOwner)
			turn := f.accept(routeTestOwner, "conv_w10d_fin", "cmid-fin", "问题")
			turnID = turn.TurnID
			if item.setup != nil {
				item.setup(f, turnID)
			}
			if item.substr != "" {
				script.failOnce(item.substr)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.CompleteChatTurn(CompleteTurnInput{
				ConversationID: "conv_w10d_fin", SystemAccountID: routeTestOwner,
				TurnID: turnID, AssistantContent: "回答", FinishReason: "stop",
				Now: f.nowISO,
			})
			return err
		})
	}
}

// w10dActiveTurnID 读取会话当前活动轮次。
func w10dActiveTurnID(f *chatFixture) string {
	f.t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = 'conv_w10d_fin'`).Scan(&turnID); err != nil || turnID == "" {
		f.t.Fatalf("读取活动轮次失败: %v", err)
	}
	return turnID
}

// TestW10DCancelTurnFaultArms 覆盖轮次取消/中断的存储错误臂。
func TestW10DCancelTurnFaultArms(t *testing.T) {
	cancelCases := []struct {
		name   string
		substr string
		nth    int
	}{
		{"会话锁查询失败", "FROM chat_conversations", 1},
		{"助手读取失败", "AND role = 'assistant'", 1},
		{"窗口预留释放失败", "SET reserved_bytes = reserved_bytes - ?", 1},
		{"会话收口失败", "message_revision = message_revision + 1, last_message_at = ?", 1},
		{"提交失败", commitFaultKeyW10D, 0},
	}
	for _, item := range cancelCases {
		item := item
		var turnID string
		runRecoverableFaultCaseW10D(t, "CancelTurn/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_cancel", routeTestOwner)
			turn := f.accept(routeTestOwner, "conv_w10d_cancel", "cmid-cancel", "问题")
			turnID = turn.TurnID
			if item.nth == 1 {
				script.failOnce(item.substr)
			} else if item.nth > 1 {
				script.failNth(item.substr, item.nth)
			} else if item.substr != "" {
				script.failOnce(item.substr)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
				ConversationID: "conv_w10d_cancel", SystemAccountID: routeTestOwner,
				ExpectedTurnID: turnID, Now: f.nowISO,
			})
			return err
		})
	}

	// 中断模式成功路径：interrupted 状态登记与 already_terminal 返回。
	t.Run("CancelTurn/中断模式收口", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_intr", routeTestOwner)
		fixture.accept(routeTestOwner, "conv_w10d_intr", "cmid-intr", "问题")
		turnID := w10dIntrTurnID(fixture)
		result, err := fixture.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_w10d_intr", SystemAccountID: routeTestOwner,
			ExpectedTurnID: turnID, Now: fixture.nowISO,
		})
		if err != nil || result.State != CancelStateAlreadyTerminal {
			t.Fatalf("中断结果 = %+v err=%v", result, err)
		}
		// 会话应已解除活动轮次，助手消息登记为 failed。
		var status string
		if err := fixture.db.QueryRow(`SELECT status FROM chat_messages WHERE conversation_id = 'conv_w10d_intr' AND role = 'assistant'`).Scan(&status); err != nil || status != "failed" {
			t.Fatalf("中断后助手状态 = %q err=%v", status, err)
		}
	})

	// 轮次不匹配：助手流式中但会话指向其他轮次 → classify turn mismatch。
	t.Run("CancelTurn/轮次不匹配", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_w10d_mismatch", routeTestOwner)
		turn := fixture.accept(routeTestOwner, "conv_w10d_mismatch", "cmid-mis", "问题")
		if _, err := fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'turn-other' WHERE id = 'conv_w10d_mismatch'`); err != nil {
			t.Fatal(err)
		}
		result, err := fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
			ConversationID: "conv_w10d_mismatch", SystemAccountID: routeTestOwner,
			ExpectedTurnID: turn.TurnID, Now: fixture.nowISO,
		})
		if err != nil || result.State != CancelStateTurnMismatch {
			t.Fatalf("轮次不匹配 = %+v err=%v", result, err)
		}
	})
}

// w10dCancelTurnID 读取取消用例的活动轮次。
func w10dCancelTurnID(f *chatFixture) string {
	f.t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = 'conv_w10d_cancel'`).Scan(&turnID); err != nil || turnID == "" {
		f.t.Fatalf("读取活动轮次失败: %v", err)
	}
	return turnID
}

// w10dIntrTurnID 读取中断用例的活动轮次。
func w10dIntrTurnID(f *chatFixture) string {
	f.t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = 'conv_w10d_intr'`).Scan(&turnID); err != nil || turnID == "" {
		f.t.Fatalf("读取活动轮次失败: %v", err)
	}
	return turnID
}

// TestW10DConversationLifecycleFaultArms 覆盖创建/删除/清空的存储错误臂。
func TestW10DConversationLifecycleFaultArms(t *testing.T) {
	createCases := []struct {
		name   string
		substr string
	}{
		{"会话计数查询失败", "SELECT COUNT(*)"},
		{"会话写入失败", "'新对话', ?, 'gpt-image-2'"},
		{"会话读取失败", "WHERE id = ? AND system_account_id = ?"},
		{"提交失败", commitFaultKeyW10D},
	}
	for _, item := range createCases {
		runRecoverableFaultCaseW10D(t, "CreateConversation/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			script.failOnce(item.substr)
		}, func(f *chatFixture) error {
			_, err := f.store.CreateConversation(CreateConversationInput{
				ID: "conv_w10d_create", SystemAccountID: routeTestOwner, APIKeyID: "k1",
				DefaultModel: "gpt-5", Now: f.nowISO, MaxConversationsPerUser: 30,
			})
			return err
		})
	}

	deleteCases := []struct {
		name       string
		substr     string
		persistent bool
		setup      func(f *chatFixture)
	}{
		{"会话锁查询失败", "FROM chat_conversations", false, nil},
		{"last_message_at 校验失败", "", true, func(f *chatFixture) {
			if _, err := f.db.Exec(`UPDATE chat_conversations SET last_message_at = 'x' WHERE id = 'conv_w10d_del'`); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"created_at 校验失败", "", true, func(f *chatFixture) {
			if _, err := f.db.Exec(`UPDATE chat_conversations SET created_at = 'x' WHERE id = 'conv_w10d_del'`); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"updated_at 校验失败", "", true, func(f *chatFixture) {
			if _, err := f.db.Exec(`UPDATE chat_conversations SET updated_at = 'x' WHERE id = 'conv_w10d_del'`); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"存储释放失败", "SELECT created_at, content_bytes, storage_reserved_bytes", false, func(f *chatFixture) {
			turn := f.accept(routeTestOwner, "conv_w10d_del", "cmid-del", "问题")
			f.complete(routeTestOwner, "conv_w10d_del", turn.TurnID, "回答")
		}},
		{"硬删除失败", "DELETE FROM chat_conversations WHERE id = ? AND system_account_id = ?", false, nil},
		{"提交失败", commitFaultKeyW10D, false, nil},
	}
	for _, item := range deleteCases {
		item := item
		runner := runRecoverableFaultCaseW10D
		if item.persistent {
			runner = runFaultCaseW10D
		}
		runner(t, "DeleteConversation/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_del", routeTestOwner)
			if item.setup != nil {
				item.setup(f)
			}
			if item.substr != "" {
				script.failOnce(item.substr)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.DeleteConversation("conv_w10d_del", routeTestOwner)
			return err
		})
	}

	clearCases := []struct {
		name       string
		substr     string
		nth        int
		persistent bool
		setup      func(f *chatFixture)
	}{
		{"会话锁查询失败", "FROM chat_conversations", 1, false, nil},
		{"压缩中冲突", "", 0, true, func(f *chatFixture) {
			// request + claim 走合法 compacting 状态登记。
			f.seedTurns(routeTestOwner, "conv_w10d_clear", 2)
			head, err := f.store.GetContextHead("conv_w10d_clear", routeTestOwner)
			if err != nil {
				f.t.Fatal(err)
			}
			if !requestCompaction(t, f, "conv_w10d_clear", routeTestOwner, head.ContextRevision, 2) {
				f.t.Fatal("请求压缩失败")
			}
			claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: "conv_w10d_clear", SystemAccountID: routeTestOwner,
				ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			if err != nil || claim == nil {
				f.t.Fatalf("认领压缩失败: %v %v", claim, err)
			}
		}},
		{"存储释放失败", "SELECT created_at, content_bytes, storage_reserved_bytes", 1, false, func(f *chatFixture) {
			turn := f.accept(routeTestOwner, "conv_w10d_clear", "cmid-clear", "问题")
			f.complete(routeTestOwner, "conv_w10d_clear", turn.TurnID, "回答")
		}},
		{"窗口清零删除失败", "WHERE system_account_id = ? AND content_bytes = 0 AND reserved_bytes = 0", 1, false, func(f *chatFixture) {
			turn := f.accept(routeTestOwner, "conv_w10d_clear", "cmid-clear", "问题")
			f.complete(routeTestOwner, "conv_w10d_clear", turn.TurnID, "回答")
		}},
		{"幂等删除失败", "DELETE FROM chat_message_idempotency WHERE conversation_id", 1, false, func(f *chatFixture) {
			turn := f.accept(routeTestOwner, "conv_w10d_clear", "cmid-clear", "问题")
			f.complete(routeTestOwner, "conv_w10d_clear", turn.TurnID, "回答")
		}},
		{"会话重置失败", "SET title = '新对话'", 1, false, nil},
		{"清空后读取失败", "WHERE id = ? AND system_account_id = ?", 3, false, func(f *chatFixture) {
			turn := f.accept(routeTestOwner, "conv_w10d_clear", "cmid-clear", "问题")
			f.complete(routeTestOwner, "conv_w10d_clear", turn.TurnID, "回答")
		}},
		{"提交失败", commitFaultKeyW10D, 0, false, nil},
	}
	for _, item := range clearCases {
		item := item
		runner := runRecoverableFaultCaseW10D
		if item.persistent {
			runner = runFaultCaseW10D
		}
		runner(t, "ClearConversation/"+item.name, func(f *chatFixture, script *faultScriptW10D) {
			f.createConversation("conv_w10d_clear", routeTestOwner)
			if item.setup != nil {
				item.setup(f)
			}
			if item.nth == 1 {
				script.failOnce(item.substr)
			} else if item.nth > 1 {
				script.failNth(item.substr, item.nth)
			} else if item.substr != "" {
				script.failOnce(item.substr)
			}
		}, func(f *chatFixture) error {
			_, err := f.store.ClearConversation(ClearConversationInput{
				ConversationID: "conv_w10d_clear", SystemAccountID: routeTestOwner, Now: f.nowISO,
			})
			return err
		})
	}
}
