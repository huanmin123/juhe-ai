package chat

// w14l 覆盖率收尾：store 查询错误臂的第二轮扫描。复用 w10d 故障驱动与
// w14c 扫描框架，补上替换状态机、条件停止（取消/中断）、会话读取、
// 同步头/用量记录与“安装检查点后再加载”等序列的第 n 条语句失败，
// 以及 Commit/Rollback 边界注入。

import (
	"context"
	"fmt"
	"testing"
)

func w14lSequences() map[string]func(f *chatFixture, conversationID string) error {
	// replaceTurnID 取最近一条已完成用户轮次。
	lastUserTurn := func(f *chatFixture, conversationID string) string {
		var turnID string
		if err := f.db.QueryRow(`SELECT turn_id FROM chat_messages
			WHERE conversation_id = ? AND role = 'user' ORDER BY sequence_no DESC LIMIT 1`,
			conversationID).Scan(&turnID); err != nil {
			return ""
		}
		return turnID
	}
	return map[string]func(f *chatFixture, conversationID string) error{
		"replace-flow": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			turnID := lastUserTurn(f, conversationID)
			if turnID == "" {
				return errW14LNotFound
			}
			_, err := f.store.AcceptTurn(acceptReplacementInputW10D(f, conversationID, "w14l-rep", turnID))
			return err
		},
		"assert-replaceable": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			return f.store.AssertTurnReplaceable(AssertReplaceableInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ReplaceTurnID: lastUserTurn(f, conversationID), Now: f.nowISO,
			})
		},
		"cancel-active": func(f *chatFixture, conversationID string) error {
			accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14l-cancel"))
			if err != nil {
				return err
			}
			_, err = f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
			})
			return err
		},
		"cancel-mismatch": func(f *chatFixture, conversationID string) error {
			accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14l-mismatch"))
			if err != nil {
				return err
			}
			_, err = f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedTurnID: accepted.TurnID + "-other", Now: f.nowISO,
			})
			return err
		},
		"interrupt-active": func(f *chatFixture, conversationID string) error {
			accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14l-int"))
			if err != nil {
				return err
			}
			_, err = f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedTurnID: accepted.TurnID, Now: f.nowISO,
			})
			return err
		},
		"conversation-reads": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			if _, err := f.store.GetConversation(conversationID, routeTestOwner); err != nil {
				return err
			}
			_, err := f.store.ListConversations(ListConversationsInput{SystemAccountID: routeTestOwner, Limit: 10})
			return err
		},
		"sync-head-two-turns": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			_, err := f.store.GetConversationSyncHead(conversationID, routeTestOwner, f.nowISO)
			return err
		},
		"record-usage": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			head, err := f.store.GetContextHead(conversationID, routeTestOwner)
			if err != nil {
				return err
			}
			limit := int64(4096)
			_, err = f.store.RecordContextUsage(RecordContextUsageInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedContextRevision: head.ContextRevision, ActiveContextTokens: 120,
				EffectiveContextLimitTokens: &limit, UsageEstimated: true, Now: f.nowISO,
			})
			return err
		},
		"compact-once": func(f *chatFixture, conversationID string) error {
			for i := 1; i <= 5; i++ {
				accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, fmt.Sprintf("w14l-cm-%d", i)))
				if err != nil {
					return err
				}
				if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
					ConversationID: conversationID, SystemAccountID: routeTestOwner,
					TurnID: accepted.TurnID, AssistantContent: "回答", FinishReason: "stop", Now: f.nowISO,
				}); err != nil {
					return err
				}
			}
			executor := &mockExecutor{}
			executor.steps = append(executor.steps, scriptStep{
				match: func(call dispatchCall) bool { return true },
				respond: func(call dispatchCall) *GenerationDispatchResponse {
					summary := `{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`
					return jsonStatusResponse(200, `{"choices":[{"message":{"content":` + jsonQuote(summary) + `}}]}`)
				},
			})
			service := NewCompactionService(f.store, executor, func(text string) int { return len(text) / 4 }, func() string { return f.nowISO })
			service.CompactOnce(context.Background(), CompactionInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner, Model: "gpt-5",
			})
			return nil
		},
		"load-after-checkpoint": func(f *chatFixture, conversationID string) error {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return err
			}
			head, err := f.store.GetContextHead(conversationID, routeTestOwner)
			if err != nil {
				return err
			}
			claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			if err != nil {
				return err
			}
			if claim == nil {
				return errW14LNotFound
			}
			if _, err := f.store.InstallContextCheckpoint(installInputW14C(f, conversationID, claim, head.ContextRevision)); err != nil {
				return err
			}
			_, err = f.store.LoadModelContext(conversationID, routeTestOwner, f.nowISO, maxContextLoadRows, maxContextLoadBytes)
			return err
		},
	}
}

// errW14LNotFound 是序列内认领缺失的占位错误。
type w14lNotFoundError struct{}

func (w14lNotFoundError) Error() string { return "w14l 认领缺失" }

var errW14LNotFound error = w14lNotFoundError{}

// TestW14LStoreFaultSweep 第二轮“第 n 条语句失败”扫描。
func TestW14LStoreFaultSweep(t *testing.T) {
	for name, sequence := range w14lSequences() {
		sequence := sequence
		t.Run(name, func(t *testing.T) {
			for n := 1; n <= 64; n++ {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					fixture, script := newFaultChatFixtureW10D(t)
					conversationID := fmt.Sprintf("chat_conv_w14l_sweep_%s_%d", name, n)
					fixture.createConversation(conversationID, routeTestOwner)
					// 故障在建库/建会话之后注入，只影响序列内的查询。
					script.failNth("chat_", n)
					_ = sequence(fixture, conversationID)
				})
			}
		})
	}
}

// TestW14LStoreCommitFaultDirect 对第二轮序列直接注入 Commit 失败。
func TestW14LStoreCommitFaultDirect(t *testing.T) {
	for name, sequence := range w14lSequences() {
		sequence := sequence
		t.Run(name, func(t *testing.T) {
			fixture, script := newFaultChatFixtureW10D(t)
			conversationID := fmt.Sprintf("chat_conv_w14l_cd_%s", name)
			fixture.createConversation(conversationID, routeTestOwner)
			script.fail(commitFaultKeyW10D)
			_ = sequence(fixture, conversationID)
		})
	}
}

// TestW14LStoreRollbackFaultDirect 在“第 n 条失败 + 回滚失败”组合下命中
// 显式检查回滚错误的臂。
func TestW14LStoreRollbackFaultDirect(t *testing.T) {
	for name, sequence := range w14lSequences() {
		sequence := sequence
		t.Run(name, func(t *testing.T) {
			for n := 2; n <= 10; n += 2 {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					fixture, script := newFaultChatFixtureW10D(t)
					conversationID := fmt.Sprintf("chat_conv_w14l_rb_%s_%d", name, n)
					fixture.createConversation(conversationID, routeTestOwner)
					script.fail(rollbackFaultKeyW10D)
					script.failNth("chat_", n)
					_ = sequence(fixture, conversationID)
				})
			}
		})
	}
}
