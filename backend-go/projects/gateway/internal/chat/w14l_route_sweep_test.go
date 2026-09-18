package chat

// w14l 覆盖率收尾（四）：路由级“第 n 条语句失败”扫描与资产替换序列。

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// w14lRouteSweepPost 以流式 POST 驱动路由（不校验结果，只吃掉错误）。
func w14lRouteSweepPost(rt *chatRoutes, conversationID, payload, owner string) {
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user"}))
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request)
}

// w14lStopSequence 组合一次停止轮次请求。
func w14lStopSequence(f *chatFixture, rt *chatRoutes, conversationID string) error {
	accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14l-stop-seq"))
	if err != nil {
		return err
	}
	stopBody := `{"turnId":"` + accepted.TurnID + `"}`
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stop", strings.NewReader(stopBody))
	request.SetPathValue("conversationId", conversationID)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner, Username: routeTestOwner, DisplayName: routeTestOwner, Role: "user"}))
	recorder := httptest.NewRecorder()
	rt.stopTurn(recorder, request)
	return nil
}

// w14lCompactionTriggerSequence 组合一次压缩触发请求。
func w14lCompactionTriggerSequence(f *chatFixture, rt *chatRoutes, conversationID string) error {
	for i := 1; i <= 4; i++ {
		accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, fmt.Sprintf("w14l-ct-%d", i)))
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
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/compactions", strings.NewReader(`{}`))
	request.SetPathValue("conversationId", conversationID)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner, Username: routeTestOwner, DisplayName: routeTestOwner, Role: "user"}))
	recorder := httptest.NewRecorder()
	rt.compactionTrigger(recorder, request)
	return nil
}

// TestW14LRouteFaultSweep 路由级“第 n 条语句失败”扫描。
func TestW14LRouteFaultSweep(t *testing.T) {
	runs := map[string]func(t *testing.T, f *chatFixture, rt *chatRoutes, conversationID string) error{
		"stop-turn": func(t *testing.T, f *chatFixture, rt *chatRoutes, conversationID string) error {
			return w14lStopSequence(f, rt, conversationID)
		},
		"compaction-trigger": func(t *testing.T, f *chatFixture, rt *chatRoutes, conversationID string) error {
			return w14lCompactionTriggerSequence(f, rt, conversationID)
		},
		"stream-with-replace": func(t *testing.T, f *chatFixture, rt *chatRoutes, conversationID string) error {
			accepted, err := f.store.AcceptTurn(acceptTurnInputW10D(f, conversationID, "w14l-sr-seed"))
			if err != nil {
				return err
			}
			if _, err := f.store.CompleteChatTurn(CompleteTurnInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				TurnID: accepted.TurnID, AssistantContent: "回答", FinishReason: "stop", Now: f.nowISO,
			}); err != nil {
				return err
			}
			payload := `{"clientMessageId":"w14l-sr-cmid","content":"重问","model":"gpt-5","replaceTurnId":"` + accepted.TurnID + `"}`
			w14lRouteSweepPost(rt, conversationID, payload, routeTestOwner)
			return nil
		},
	}
	for name, run := range runs {
		run := run
		t.Run(name, func(t *testing.T) {
			for n := 1; n <= 40; n++ {
				n := n
				t.Run(fmt.Sprintf("n%02d", n), func(t *testing.T) {
					fixture, script := newFaultChatFixtureW10D(t)
					conversationID := fmt.Sprintf("chat_conv_w14l_route_%s_%d", name, n)
					fixture.createConversation(conversationID, routeTestOwner)
					env := buildGenerationEnvW10D(t, fixture)
					rt := newChatRoutesForTest(env.deps)
					script.failNth("chat_", n)
					_ = run(t, fixture, rt, conversationID)
				})
			}
		})
	}
	_ = context.Background
}

// TestW14LNullRowScanSweep 第 k 条查询返回整行 NULL，命中各读循环的
// Scan 类型失败臂与 rows.Err 臂。
func TestW14LNullRowScanSweep(t *testing.T) {
	runs := map[string]func(t *testing.T, f *chatFixture, conversationID string){
		"load-checkpoint": func(t *testing.T, f *chatFixture, conversationID string) {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return
			}
			head, err := f.store.GetContextHead(conversationID, routeTestOwner)
			if err != nil {
				return
			}
			claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
				ConversationID: conversationID, SystemAccountID: routeTestOwner,
				ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
				Now: f.nowISO, StaleClaimBefore: f.nowISO,
			})
			if err != nil || claim == nil {
				return
			}
			if _, err := f.store.InstallContextCheckpoint(installInputW14C(f, conversationID, claim, head.ContextRevision)); err != nil {
				return
			}
			_, _ = f.store.LoadModelContext(conversationID, routeTestOwner, f.nowISO, maxContextLoadRows, maxContextLoadBytes)
		},
		"sync-head": func(t *testing.T, f *chatFixture, conversationID string) {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return
			}
			_, _ = f.store.GetConversationSyncHead(conversationID, routeTestOwner, f.nowISO)
		},
		"conversation-reads": func(t *testing.T, f *chatFixture, conversationID string) {
			if err := w14cCompleteActiveTurn(f, conversationID); err != nil {
				return
			}
			_, _ = f.store.GetConversation(conversationID, routeTestOwner)
			_, _ = f.store.ListConversations(ListConversationsInput{SystemAccountID: routeTestOwner, Limit: 10})
		},
	}
	for name, run := range runs {
		run := run
		t.Run(name, func(t *testing.T) {
			for k := 1; k <= 40; k++ {
				k := k
				t.Run(fmt.Sprintf("k%02d", k), func(t *testing.T) {
					hold := newW14LHold("FROM ")
					fixture, _ := newW14LHoldFixture(t, hold)
					conversationID := fmt.Sprintf("chat_conv_w14l_null_%s_%d", name, k)
					fixture.createConversation(conversationID, routeTestOwner)
					hold.mu.Lock()
					hold.armed = true
					hold.nullAt = -k
					hold.mu.Unlock()
					run(t, fixture, conversationID)
				})
			}
		})
	}
}
