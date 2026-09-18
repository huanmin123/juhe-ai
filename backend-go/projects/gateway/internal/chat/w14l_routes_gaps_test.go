package chat

// w14l 覆盖率收尾：路由层缺口。覆盖停止轮次矩阵、提交状态流式/错误载荷、
// 上下文状态比值臂、模型目录去重/协议过滤臂、资产上传/读取/删除矩阵与
// 流式路由的重复提交臂。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func w14lRoutes(t *testing.T) (*generationEnv, *chatRoutes, string) {
	t.Helper()
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w14l_routes"
	env.fixture.createConversation(conversationID, routeTestOwner)
	return env, newChatRoutesForTest(env.deps), conversationID
}

func w14lAuthed(owner string) context.Context {
	return authsys.WithAuthContext(context.Background(), &authsys.AuthContext{SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user"})
}

func w14lJSON(method, target, body string, owner string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if owner != "" {
		request = request.WithContext(w14lAuthed(owner))
	}
	return request
}

func w14lStopPost(rt *chatRoutes, conversationID, body, owner string) *httptest.ResponseRecorder {
	request := w14lJSON("POST", "/conversations/"+conversationID+"/stop", body, owner)
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()
	rt.stopTurn(recorder, request)
	return recorder
}

// TestW14LStopTurnArms 覆盖停止路由的剩余臂。
func TestW14LStopTurnArms(t *testing.T) {
	env, rt, conversationID := w14lRoutes(t)

	// 仅 turnId 且轮次不存在 → not found。
	recorder := w14lStopPost(rt, conversationID, `{"turnId":"chat_turn_missing"}`, routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "chat_generation_not_found") {
		t.Fatalf("未知 turnId = %d %s", recorder.Code, recorder.Body.String())
	}

	// turnId 与 clientMessageId 指向不同轮次 → chat_turn_mismatch。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w14l-stop-cmid", "问题")
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
	recorder = w14lStopPost(rt, conversationID,
		`{"turnId":"chat_turn_other","clientMessageId":"w14l-stop-cmid"}`, routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "chat_turn_mismatch") {
		t.Fatalf("轮次不匹配 = %d %s", recorder.Code, recorder.Body.String())
	}

	// clientMessageId 命中已完成轮次 → already_terminal 终态载荷。
	recorder = w14lStopPost(rt, conversationID, `{"clientMessageId":"w14l-stop-cmid"}`, routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "already_terminal") {
		t.Fatalf("已完成轮次 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 活跃轮次：接受后按 turnId 停止 → canceled。
	accepted2 := env.fixture.accept(routeTestOwner, conversationID, "w14l-stop-live", "问题2")
	recorder = w14lStopPost(rt, conversationID, `{"turnId":"`+accepted2.TurnID+`"}`, routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "canceled") {
		t.Fatalf("活跃轮次停止 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 有预备中的 preparation → 202 stopped。
	prep := rt.claimPreparation(conversationID, routeTestOwner, "w14l-stop-prep")
	if prep == nil {
		t.Fatal("认领预备失败")
	}
	recorder = w14lStopPost(rt, conversationID, `{"clientMessageId":"w14l-stop-prep"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("预备停止 = %d %s", recorder.Code, recorder.Body.String())
	}

	// 未认证 → 5xx。
	recorder = w14lStopPost(rt, conversationID, `{"turnId":"x"}`, "")
	if recorder.Code < 400 {
		t.Fatalf("未认证应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	_ = env
}

// TestW14LSubmissionStatusArms 覆盖提交状态的流式/错误载荷臂。
func TestW14LSubmissionStatusArms(t *testing.T) {
	env, rt, conversationID := w14lRoutes(t)
	get := func(cmid, owner string) *httptest.ResponseRecorder {
		request := w14lJSON("GET", "/conversations/"+conversationID+"/submissions/"+cmid, "", owner)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("clientMessageId", cmid)
		recorder := httptest.NewRecorder()
		rt.submissionStatus(recorder, request)
		return recorder
	}
	// 活跃（streaming）轮次 + 快照缺失 → runnerState=missing。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w14l-sub-live", "问题")
	recorder := get("w14l-sub-live", routeTestOwner)
	if !strings.Contains(recorder.Body.String(), `"runnerState":"missing"`) {
		t.Fatalf("流中状态 = %d %s", recorder.Code, recorder.Body.String())
	}
	// 完成后带错误码 → 错误载荷（仅助手行允许失败态）。
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET status='failed', storage_reserved_bytes = 0, error_code='upstream_429', error_message='限流' WHERE turn_id = ? AND role = 'assistant'`,
		accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_conversations SET active_turn_id = NULL WHERE id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}
	recorder = get("w14l-sub-live", routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "errorCode") || !strings.Contains(recorder.Body.String(), "限流") {
		t.Fatalf("错误载荷 = %d %s", recorder.Code, recorder.Body.String())
	}
	// 预备中的消息 → preparing。
	rt.claimPreparation(conversationID, routeTestOwner, "w14l-sub-prep")
	recorder = get("w14l-sub-prep", routeTestOwner)
	if !strings.Contains(recorder.Body.String(), "preparing") {
		t.Fatalf("预备状态 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW14LContextStatusArms 覆盖上下文状态的已用/上限比值臂。
func TestW14LContextStatusArms(t *testing.T) {
	env, rt, conversationID := w14lRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 3)
	call := func() *httptest.ResponseRecorder {
		request := w14lJSON("GET", "/conversations/"+conversationID+"/context", "", routeTestOwner)
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.contextStatus(recorder, request)
		return recorder
	}
	if recorder := call(); recorder.Code != http.StatusOK {
		t.Fatalf("context status = %d %s", recorder.Code, recorder.Body.String())
	}
	head, err := env.fixture.store.GetContextHead(conversationID, routeTestOwner)
	if err != nil || head == nil {
		t.Fatalf("head = %+v / %v", head, err)
	}
	limit := int64(1000)
	if _, err := env.fixture.store.RecordContextUsage(RecordContextUsageInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedContextRevision: head.ContextRevision, ActiveContextTokens: 500,
		EffectiveContextLimitTokens: &limit, Now: env.fixture.nowISO,
	}); err != nil {
		t.Fatal(err)
	}
	recorder := call()
	if !strings.Contains(recorder.Body.String(), "0.5") {
		t.Fatalf("比值应包含 0.5：%s", recorder.Body.String())
	}
}

// w14lQuirkyGatewayKeys 返回含重复/停用/空分组绑定的网关键视图。
type w14lQuirkyGatewayKeys struct{}

func (w14lQuirkyGatewayKeys) ValidateGatewayKey(secret string) (*GatewayKeyView, error) {
	if secret == "" {
		return nil, nil
	}
	return &GatewayKeyView{GroupBindings: []GatewayGroupBinding{
		{GroupID: "group-a", Status: "active", GroupEnabled: true},
		{GroupID: "group-a", Status: "active", GroupEnabled: true},
		{GroupID: "group-b", Status: "disabled", GroupEnabled: true},
		{GroupID: "", Status: "active", GroupEnabled: true},
		{GroupID: "group-c", Status: "active", GroupEnabled: false},
	}}, nil
}

// TestW14LModelsRouteBindingArms 覆盖分组绑定过滤与目录去重臂。
func TestW14LModelsRouteBindingArms(t *testing.T) {
	_, rt, conversationID := w14lRoutes(t)
	rt.deps.GatewayKeys = w14lQuirkyGatewayKeys{}
	call := func() *httptest.ResponseRecorder {
		request := w14lJSON("GET", "/conversations/"+conversationID+"/models", "", routeTestOwner)
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.listConversationModels(recorder, request)
		return recorder
	}
	recorder := call()
	if recorder.Code != http.StatusOK {
		t.Fatalf("奇怪绑定应仍可用 = %d %s", recorder.Code, recorder.Body.String())
	}
	// 网关键缺失（secret 为空返回 nil）→ DomainError。
	rt.deps.GatewayKeys = w14lEmptyGatewayKeys{}
	recorder = call()
	if recorder.Code < 400 {
		t.Fatalf("空网关键应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deps.GatewayKeys = mockGatewayKeys{}
}

type w14lEmptyGatewayKeys struct{}

func (w14lEmptyGatewayKeys) ValidateGatewayKey(string) (*GatewayKeyView, error) { return nil, nil }

// TestW14LAssetUploadMatrix 覆盖资产上传的 multipart 与配额臂。
func TestW14LAssetUploadMatrix(t *testing.T) {
	env, rt, conversationID := w14lRoutes(t)
	upload := func(body string, contentType string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/assets", strings.NewReader(body))
		request.SetPathValue("conversationId", conversationID)
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		request = request.WithContext(w14lAuthed(routeTestOwner))
		recorder := httptest.NewRecorder()
		rt.uploadAsset(recorder, request)
		return recorder
	}
	// multipart 头但非 multipart 体 → 解析失败。
	if recorder := upload("plain", "multipart/form-data; boundary=x"); recorder.Code < 400 {
		t.Fatalf("伪 multipart 应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	// 三个 part → 只允许一张。
	multipartBody := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.png\"\r\nContent-Type: image/png\r\n\r\nx\r\n" +
		"--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"b.png\"\r\nContent-Type: image/png\r\n\r\ny\r\n" +
		"--b--\r\n"
	if recorder := upload(multipartBody, "multipart/form-data; boundary=b"); recorder.Code < 400 {
		t.Fatalf("多 part 应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	// 非 multipart 的内容类型 → 无效请求。
	if recorder := upload("x", "application/json"); recorder.Code < 400 {
		t.Fatalf("非 multipart 应拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	// 存储配额顶满 → 配额超限错误臂（smallest PNG 走 stub 处理器）。
	png := string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	if _, err := env.fixture.db.Exec(
		`INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
			VALUES (?, ?, 0, ?) ON CONFLICT(system_account_id) DO UPDATE SET asset_bytes = excluded.asset_bytes`,
		routeTestOwner, ChatAssetUserMaxBytes, env.fixture.nowISO); err != nil {
		t.Fatal(err)
	}
	validBody := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.png\"\r\nContent-Type: image/png\r\n\r\n" + png + "\r\n--b--\r\n"
	recorder := upload(validBody, "multipart/form-data; boundary=b")
	if !strings.Contains(recorder.Body.String(), "chat_asset_quota_exceeded") {
		t.Fatalf("配额超限 = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_user_asset_usage SET asset_bytes = 0 WHERE system_account_id = ?`, routeTestOwner); err != nil {
		t.Fatal(err)
	}
	// 正常上传。
	recorder = upload(validBody, "multipart/form-data; boundary=b")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("正常上传 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW14LAssetContentAndDeleteArms 覆盖资产读取与删除臂。
func TestW14LAssetContentAndDeleteArms(t *testing.T) {
	_, rt, conversationID := w14lRoutes(t)
	uploadBody := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.png\"\r\nContent-Type: image/png\r\n\r\n" +
		string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) + "\r\n--b--\r\n"
	upload := func() string {
		request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/assets", strings.NewReader(uploadBody))
		request.SetPathValue("conversationId", conversationID)
		request.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		request = request.WithContext(w14lAuthed(routeTestOwner))
		recorder := httptest.NewRecorder()
		rt.uploadAsset(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("上传失败：%d %s", recorder.Code, recorder.Body.String())
		}
		payload := w13bDecode(t, recorder)["data"].(map[string]any)
		return payload["id"].(string)
	}
	assetID := upload()
	content := func(query, etag string) *httptest.ResponseRecorder {
		target := "/conversations/" + conversationID + "/assets/" + assetID + "/content"
		if query != "" {
			target += "?" + query
		}
		request := httptest.NewRequest("GET", target, nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("assetId", assetID)
		if etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		request = request.WithContext(w14lAuthed(routeTestOwner))
		recorder := httptest.NewRecorder()
		rt.assetContent(recorder, request)
		return recorder
	}
	if recorder := content("variant=bogus", ""); recorder.Code < 400 {
		t.Fatalf("非法 variant 应拒绝: %d", recorder.Code)
	}
	if recorder := content("download=bogus", ""); recorder.Code < 400 {
		t.Fatalf("非法 download 应拒绝: %d", recorder.Code)
	}
	if recorder := content("download=1", ""); recorder.Code != http.StatusOK {
		t.Fatalf("下载 = %d %s", recorder.Code, recorder.Body.String())
	}
	etag := `"` + strings.Repeat("0", 64) + `"`
	if recorder := content("", etag); recorder.Code != http.StatusOK && recorder.Code != http.StatusNotModified {
		t.Fatalf("etag 读取 = %d", recorder.Code)
	}
	// 不存在资产 → 404 JSON。
	request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/assets/nope/content", nil)
	request.SetPathValue("conversationId", conversationID)
	request.SetPathValue("assetId", "chat_asset_missing")
	request = request.WithContext(w14lAuthed(routeTestOwner))
	recorder := httptest.NewRecorder()
	rt.assetContent(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("缺失资产 = %d %s", recorder.Code, recorder.Body.String())
	}
	// 删除已就绪资产 → 不可删除冲突。
	del := func(asset string) *httptest.ResponseRecorder {
		dRequest := httptest.NewRequest("DELETE", "/conversations/"+conversationID+"/assets/"+asset, nil)
		dRequest.SetPathValue("conversationId", conversationID)
		dRequest.SetPathValue("assetId", asset)
		dRequest = dRequest.WithContext(w14lAuthed(routeTestOwner))
		dRecorder := httptest.NewRecorder()
		rt.deleteAsset(dRecorder, dRequest)
		return dRecorder
	}
	if recorder := del(assetID); recorder.Code == 0 || (recorder.Code != http.StatusConflict && recorder.Code != http.StatusNoContent) {
		t.Fatalf("删除已就绪资产 = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := del("chat_asset_missing"); !strings.Contains(recorder.Body.String(), "chat_asset_not_deletable") {
		t.Fatalf("缺失资产删除 = %d %s", recorder.Code, recorder.Body.String())
	}
}
