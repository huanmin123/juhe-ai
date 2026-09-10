package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 断连故障注入：关闭数据库后打全部管理路由，验证每个 handler 将存储错误
// 映射为 500（internal_generation_failed）而不是 panic 或静默成功。这批用例
// 覆盖各路由内部的存储错误延续分支。

func closedEnvW3(t *testing.T) *generationEnv {
	t.Helper()
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_closed", routeTestOwner)
	if err := env.fixture.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	return env
}

func expectInternalW3(t *testing.T, name string, response routeResponse) {
	t.Helper()
	if response.status != http.StatusInternalServerError {
		t.Fatalf("%s = %d %s, 期望断连 500", name, response.status, response.rawString())
	}
	if !strings.Contains(response.rawString(), "internal_generation_failed") {
		t.Fatalf("%s 错误码不正确: %s", name, response.rawString())
	}
}

// TestClosedDatabaseRouteSweepW3 断连后的路由矩阵。
func TestClosedDatabaseRouteSweepW3(t *testing.T) {
	env := closedEnvW3(t)
	prefix := "/__aisys__/api/my-chat"

	expectInternalW3(t, "GET /conversations", env.do("GET", prefix+"/conversations", routeTestOwner, ""))
	expectInternalW3(t, "POST /conversations", env.do("POST", prefix+"/conversations", routeTestOwner, "{}"))
	expectInternalW3(t, "GET /conversations/{id}", env.do("GET", prefix+"/conversations/chat_conv_closed", routeTestOwner, ""))
	expectInternalW3(t, "PATCH /conversations/{id}", env.do("PATCH", prefix+"/conversations/chat_conv_closed", routeTestOwner, `{"title":"x"}`))
	expectInternalW3(t, "DELETE /conversations/{id}", env.do("DELETE", prefix+"/conversations/chat_conv_closed", routeTestOwner, ""))
	expectInternalW3(t, "POST clear", env.do("POST", prefix+"/conversations/chat_conv_closed/clear", routeTestOwner, ""))
	expectInternalW3(t, "GET messages", env.do("GET", prefix+"/conversations/chat_conv_closed/messages", routeTestOwner, ""))
	expectInternalW3(t, "GET submission", env.do("GET", prefix+"/conversations/chat_conv_closed/submissions/cmid-1", routeTestOwner, ""))
	expectInternalW3(t, "POST compactions", env.do("POST", prefix+"/conversations/chat_conv_closed/context/compactions", routeTestOwner, `{"model":"gpt-5"}`))
	expectInternalW3(t, "GET context-status", env.do("GET", prefix+"/conversations/chat_conv_closed/context-status", routeTestOwner, ""))
	expectInternalW3(t, "GET models", env.do("GET", prefix+"/conversations/chat_conv_closed/models", routeTestOwner, ""))
	expectInternalW3(t, "GET models/{id}", env.do("GET", prefix+"/conversations/chat_conv_closed/models/gpt-5", routeTestOwner, ""))
}

// TestClosedDatabaseStreamRouteW3 断连后的流式路由快速失败链。
func TestClosedDatabaseStreamRouteW3(t *testing.T) {
	env := closedEnvW3(t)
	response := env.streamPost("chat_conv_closed", routeTestOwner, streamPayload("cmid-closed", "内容", "gpt-5"))
	if response.status != http.StatusInternalServerError {
		t.Fatalf("断连流式 = %d %s", response.status, response.rawString())
	}
}

// TestReplaceWithRetainedImagesW3 覆盖替换时保留图片资产的记账分支。
func TestReplaceWithRetainedImagesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_retain", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	created := uploadAssetW3(t, server.URL+"/conversations/chat_conv_retain/assets", routeTestOwner, "cat.png", mustDecodeBase64W3(testTinyPNGBase64))
	assetID, _ := created.dataMap()["id"].(string)
	if assetID == "" {
		t.Fatalf("上传失败: %s", created.rawString())
	}
	env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
		return sseResponse(responsesTextSSE("第一轮"))
	}}}
	firstPayload := `{"clientMessageId":"cmid-r1","content":"看图","model":"gpt-5","contentBlocks":[{"type":"input_text","text":"看图"},{"type":"input_image","assetId":"` + assetID + `"}]}`
	first := env.streamPost("chat_conv_retain", routeTestOwner, firstPayload)
	if first.status != http.StatusOK {
		t.Fatalf("首轮失败: %d %s", first.status, first.rawString())
	}
	events := sseEvents(first.rawString())
	var started struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &started); err != nil || started.TurnID == "" {
		t.Fatalf("started 事件解析失败: %v %s", err, events[0].data)
	}
	// 替换该轮且保留同一图片：走 retainedAssetIDs 过滤分支。
	env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
		return sseResponse(responsesTextSSE("替换后"))
	}}}
	replacePayload := `{"clientMessageId":"cmid-r2","content":"再看","model":"gpt-5","contentBlocks":[{"type":"input_text","text":"再看"},{"type":"input_image","assetId":"` + assetID + `"}],"replaceTurnId":"` + started.TurnID + `"}`
	replacement := env.streamPost("chat_conv_retain", routeTestOwner, replacePayload)
	if replacement.status != http.StatusOK {
		t.Fatalf("替换流失败: %d %s", replacement.status, replacement.rawString())
	}
	// 资产应已改绑到新轮次。
	var boundTurn string
	if err := env.fixture.db.QueryRow(`SELECT turn_id FROM chat_assets WHERE id = ?`, assetID).Scan(&boundTurn); err != nil || boundTurn == started.TurnID {
		t.Fatalf("资产应改绑新轮次: %q err=%v", boundTurn, err)
	}
}
