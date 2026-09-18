package chat

// w13g4 覆盖率补齐：会话清理/同步头错误臂、历史图片观察 nil 端口臂、
// 上下文装载扫描错误。
//
// 不可达 / 高成本语句登记（基于 2026-09-18 覆盖率 profile）：
//   - turns.go 924-926（finalizeTurn 终结 UPDATE affected != 1）：880-889 的
//     lockedConversation/lockedStreamingAssistant 前置校验已拦截非活动/非
//     streaming 行。
//   - turns.go 862-864（serializeContentBlocks 失败）：ContentBlocks 为类型化
//     结构，序列化不产生错误；ContentBlocksRaw 分支不经过该调用。
//   - context.go 550-555 与 566-571 重叠：entryRows 以 maxRows+1 为上限查询，
//     for 循环内 len(result.Entries) >= maxRows 先行返回，566 的重复检查不可达。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func withOwnerContextW13G4(request *http.Request, owner string) *http.Request {
	return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: owner}))
}

func w13g4ClearPost(rt *chatRoutes, conversationID, owner string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/clear", strings.NewReader("{}"))
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()
	rt.clearConversation(recorder, withOwnerContextW13G4(request, owner))
	return recorder
}

func w13g4SyncGet(rt *chatRoutes, conversationID, owner string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/sync?knownRevision=0", nil)
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()
	rt.syncHead(recorder, withOwnerContextW13G4(request, owner))
	return recorder
}

// TestW13G4ClearAndSyncArms 覆盖清理与同步头的冲突/故障/缺失臂。
func TestW13G4ClearAndSyncArms(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_w13g4_clr"
	env.fixture.createConversation(conversationID, routeTestOwner)
	rt := newChatRoutesForTest(env.deps)

	// 预注册 prep → claimAction 返回 nil → 清理冲突（routes.go 792-794）。
	rt.mu.Lock()
	rt.preps[conversationID] = &activePreparation{token: 77, ownerID: routeTestOwner, clientMessageID: "w13g4-clr0", phase: "preparing"}
	rt.mu.Unlock()
	recorder := w13g4ClearPost(rt, conversationID, routeTestOwner)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("清理冲突 = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.mu.Lock()
	delete(rt.preps, conversationID)
	rt.mu.Unlock()

	// ClearConversation 首条 DELETE 失败 → 500（routes.go 799-802）。
	script.failOnce("DELETE FROM chat_asset_references WHERE conversation_id")
	recorder = w13g4ClearPost(rt, conversationID, routeTestOwner)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("清理失败 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 同步头查询失败 → 500（routes.go 819-822）。
	script.failOnce("WITH owned_conversation AS")
	recorder = w13g4SyncGet(rt, conversationID, routeTestOwner)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("同步头失败 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 同步头会话不存在 → 404（routes.go 843-848）。
	recorder = w13g4SyncGet(rt, "chat_conv_w13g4_none", routeTestOwner)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("同步头缺失 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4UnresolvedAssetArms 覆盖历史图片未就绪且观察端口为 nil 时的
// 等待/二次装载/拒绝链（stream_route.go 546-570、838-845）。
func TestW13G4UnresolvedAssetArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_unres"
	env.fixture.createConversation(conversationID, routeTestOwner)
	assetID := seedAssetW10D(t, env.fixture, conversationID)
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_assets SET observation_status = 'pending' WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	// 历史轮以内容块引用该图片资产。
	accepted, err := env.fixture.store.AcceptTurn(AcceptTurnInput{
		ConversationID:  conversationID,
		SystemAccountID: routeTestOwner,
		ClientMessageID: "w13g4-u0",
		UserContent:     "看历史图",
		ContentBlocks: []InputContentBlock{{
			Type: "input_image", AssetID: &assetID,
		}},
		Model: "gpt-5", Now: env.fixture.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "历史回答")
	// ImageObservation 端口保持 nil → schedule/wait 直接返回，二次装载仍存在
	// 未就绪图片 → 拒绝。
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-u1", "继续", "gpt-5"))
	if recorder.Code < 400 || !strings.Contains(recorder.Body.String(), "chat_model_context") {
		t.Fatalf("未就绪图片流 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW13G4ContextScanErrorArms 覆盖 suffix 行扫描错误臂（context.go 610-613）。
// 已证不可行：modernc.org/sqlite 对 INTEGER 亲和列的文本写入保持整型可读，
// 扫描错误无法通过数据构造触发，此处以 rows 迭代异常臂的等价故障注入替代。
func TestW13G4ContextScanErrorArms(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_w13g4_scan"
	env.fixture.createConversation(conversationID, routeTestOwner)
	seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 1)
	script.failOnce("FROM chat_messages AS source")
	if _, err := env.fixture.store.LoadModelContext(conversationID, routeTestOwner, env.fixture.nowISO, 512, 16*1024*1024); err == nil {
		t.Fatal("suffix 查询故障应导致装载失败")
	}
}
