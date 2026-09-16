package chat

// w10d 覆盖收尾：renderUserContextMessage 分支 + streamTurn 错误分支（路由故障注入）。

import (
	"net/http"
	"strings"
	"testing"
)

// TestW10DRenderUserContextMessageBranches 覆盖 renderUserContextMessage 分支。
func TestW10DRenderUserContextMessageBranches(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("conv_w10d_rc", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	unresolved := map[string]ObservationTarget{}

	// 无标记。
	msg := &contextSourceMessage{contentText: "纯文本", contentBlocksJSON: "[]"}
	if out, err := rt.renderUserContextMessage(ProtocolResponses, msg, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved); err != nil || out.([]map[string]any)[0]["text"] != "纯文本" {
		t.Fatalf("responses 无标记 = %+v/%v", out, err)
	}
	if out, err := rt.renderUserContextMessage(ProtocolChatCompletions, msg, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved); err != nil || out.(string) != "纯文本" {
		t.Fatalf("chat 无标记 = %+v/%v", out, err)
	}

	// 缺省 contentBlocksJSON 解析为空的 case。
	msgEmpty := &contextSourceMessage{contentText: "x", contentBlocksJSON: ""}
	if _, err := rt.renderUserContextMessage(ProtocolChatCompletions, msgEmpty, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved); err != nil {
		t.Fatalf("空标记 = %v", err)
	}

	// 图片资产未就绪 → unsupported_image（chat 协议）。
	assetID := seedAssetW10D(t, env.fixture, "conv_w10d_rc")
	if _, err := env.fixture.db.Exec(`UPDATE chat_assets SET observation_status = 'pending' WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	markers := `[{"type":"input_text","order":0,"text":"问题"},{"type":"input_image","order":1,"assetId":"` + assetID + `"}]`
	msgImg := &contextSourceMessage{contentText: "问题", contentBlocksJSON: markers, turnID: "t1", id: "m1"}
	unresolved2 := map[string]ObservationTarget{}
	if _, err := rt.renderUserContextMessage(ProtocolChatCompletions, msgImg, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved2); err == nil {
		t.Fatal("未就绪图片 + chat 应报 unsupported_image")
	}
	if len(unresolved2) != 1 {
		t.Fatalf("未解决观察登记 = %d", len(unresolved2))
	}
	// responses 协议：未就绪图片 → 渲染占位。
	unresolved3 := map[string]ObservationTarget{}
	out, err := rt.renderUserContextMessage(ProtocolResponses, msgImg, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved3)
	if err != nil {
		t.Fatalf("responses 未就绪图片 = %v", err)
	}
	if !strings.Contains(out.([]map[string]any)[1]["text"].(string), "说明生成中") {
		t.Fatalf("responses 占位 = %+v", out)
	}

	// 图片已过期（资产不存在）→ image_expired。
	markersExpired := `[{"type":"input_image","order":0,"assetId":"chat_asset_0000000000000000000000000000000b"}]`
	msgExp := &contextSourceMessage{contentText: "x", contentBlocksJSON: markersExpired}
	unresolved4 := map[string]ObservationTarget{}
	if _, err := rt.renderUserContextMessage(ProtocolResponses, msgExp, "conv_w10d_rc", routeTestOwner, env.fixture.nowISO, unresolved4); err == nil {
		t.Fatal("过期图片应报 image_expired")
	}
}

// TestW10DStreamTurnStoreErrorBranches 覆盖 streamTurn 的存储错误分支（路由故障注入）。
func TestW10DStreamTurnStoreErrorBranches(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	env.fixture.createConversation("conv_w10d_r", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	// GetConversation 查询失败 → streamTurn 329 分支。
	script.failOnce("FROM chat_conversations")
	recorder := w9fInvokeStream(rt, "conv_w10d_r", streamPayload("w10d-s1", "hi", "gpt-5"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("GetConversation 失败 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW10DUploadAssetErrorBranches 覆盖上传配额/会话错误臂。
func TestW10DUploadAssetErrorBranches(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("conv_w10d_r", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	// 非所有者 → 会话不存在（404）。
	upload := multipartRequestW3(t, server.URL+"/conversations/conv_w10d_r/assets", []multipartPartW3{{fieldName: "file", filename: "a.png", contentType: "image/png", data: mustDecodeBase64W3(testTinyPNGBase64)}}, "someone-else")
	response := doRequestW3(t, upload)
	if response.status != http.StatusNotFound {
		t.Fatalf("他人上传 = %d %s", response.status, response.rawString())
	}
	// 会话不存在。
	upload = multipartRequestW3(t, server.URL+"/conversations/chat_conv_none/assets", []multipartPartW3{{fieldName: "file", filename: "a.png", contentType: "image/png", data: mustDecodeBase64W3(testTinyPNGBase64)}}, routeTestOwner)
	response = doRequestW3(t, upload)
	if response.status != http.StatusNotFound {
		t.Fatalf("无会话上传 = %d %s", response.status, response.rawString())
	}
}
