package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// 路由环境集成测试：会话创建、模型目录、压缩触发、资产上传/读取/删除，
// 以及带图片输入的完整流式链路（资产解析 → 上下文装载 → Responses 流）。

func mountAssetMuxW3(t *testing.T, env *generationEnv) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	requireOwner := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner := r.Header.Get("X-Test-Owner")
			if owner == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{
				SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user",
			})))
		})
	}
	rt := newChatRoutesForTest(env.deps)
	mux.Handle("POST /conversations/{conversationId}/assets", requireOwner(rt.uploadAsset))
	mux.Handle("GET /conversations/{conversationId}/assets/{assetId}/content", requireOwner(rt.assetContent))
	mux.Handle("DELETE /conversations/{conversationId}/assets/{assetId}", requireOwner(rt.deleteAsset))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func uploadAssetW3(t *testing.T, base, owner, filename string, data []byte) routeResponse {
	t.Helper()
	parts := []multipartPartW3{}
	if filename != "" {
		parts = append(parts, multipartPartW3{fieldName: "file", filename: filename, contentType: mimeForW3(filename), data: data})
	}
	return doRequestW3(t, multipartRequestW3(t, base, parts, owner))
}

func doRequestW3(t *testing.T, request *http.Request) routeResponse {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload := make([]byte, 0)
	buffer := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buffer)
		payload = append(payload, buffer[:n]...)
		if readErr != nil {
			break
		}
	}
	parsed := map[string]any{}
	_ = json.Unmarshal(payload, &parsed)
	return routeResponse{status: response.StatusCode, body: payload, jsonMap: parsed}
}

// TestAssetUploadExtraBranchesW3 覆盖 multipart 解析的剩余拒绝分支与下载头。
func TestAssetUploadExtraBranchesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_asset2", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	base := server.URL + "/conversations/chat_conv_asset2/assets"
	pngBytes := mustDecodeBase64W3(testTinyPNGBase64)

	t.Run("非 multipart 请求", func(t *testing.T) {
		request, _ := http.NewRequest("POST", base, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Test-Owner", routeTestOwner)
		response := doRequestW3(t, request)
		if response.status != http.StatusBadRequest || response.message() != "图片上传必须使用 multipart/form-data" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("额外表单字段", func(t *testing.T) {
		request := multipartRequestW3(t, base, []multipartPartW3{
			{fieldName: "note", value: "多余字段"},
			{fieldName: "file", filename: "cat.png", contentType: "image/png", data: pngBytes},
		}, routeTestOwner)
		response := doRequestW3(t, request)
		if response.status != http.StatusBadRequest || response.message() != "图片上传不能包含额外表单字段" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("两个 file 字段", func(t *testing.T) {
		request := multipartRequestW3(t, base, []multipartPartW3{
			{fieldName: "file", filename: "a.png", contentType: "image/png", data: pngBytes},
			{fieldName: "file", filename: "b.png", contentType: "image/png", data: pngBytes},
		}, routeTestOwner)
		response := doRequestW3(t, request)
		if response.status != http.StatusBadRequest || response.message() != "每次只能上传一个 file 图片字段" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("空文件名", func(t *testing.T) {
		request := multipartRequestW3(t, base, []multipartPartW3{
			{fieldName: "file", filename: "   ", contentType: "image/png", data: pngBytes},
		}, routeTestOwner)
		response := doRequestW3(t, request)
		if response.status != http.StatusBadRequest || response.message() != "图片文件名不能为空" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("会话不存在上传", func(t *testing.T) {
		response := uploadAssetW3(t, server.URL+"/conversations/chat_conv_none/assets", routeTestOwner, "cat.png", pngBytes)
		if response.status != http.StatusNotFound {
			t.Fatalf("status = %d", response.status)
		}
	})
	t.Run("未登录上传", func(t *testing.T) {
		response := uploadAssetW3(t, base, "", "cat.png", pngBytes)
		if response.status == http.StatusCreated {
			t.Fatalf("未登录不应成功")
		}
	})
	t.Run("上传并下载与预览变体", func(t *testing.T) {
		created := uploadAssetW3(t, base, routeTestOwner, "dog.png", pngBytes)
		if created.status != http.StatusCreated {
			t.Fatalf("上传失败: %s", created.rawString())
		}
		assetID, _ := created.dataMap()["id"].(string)
		if assetID == "" {
			t.Fatalf("缺少资产 ID: %s", created.rawString())
		}
		previewQuery := doRequestW3(t, mustGetW3(t, base+"/"+assetID+"/content?variant=preview&download=1", routeTestOwner))
		if previewQuery.status != http.StatusOK {
			t.Fatalf("preview 变体应回退原图: %d", previewQuery.status)
		}
		disposition := previewQueryHeaderW3(t, base+"/"+assetID+"/content?download=1", routeTestOwner, "Content-Disposition")
		if !strings.HasPrefix(disposition, "attachment; filename=\"generated-") {
			t.Fatalf("下载头不正确: %q", disposition)
		}
		invalidVariant := doRequestW3(t, mustGetW3(t, base+"/"+assetID+"/content?variant=bogus", routeTestOwner))
		if invalidVariant.status != http.StatusBadRequest {
			t.Fatalf("非法 variant 应 400: %d", invalidVariant.status)
		}
		invalidDownload := doRequestW3(t, mustGetW3(t, base+"/"+assetID+"/content?download=yes", routeTestOwner))
		if invalidDownload.status != http.StatusBadRequest {
			t.Fatalf("非法 download 应 400: %d", invalidDownload.status)
		}
		missingAsset := doRequestW3(t, mustGetW3(t, base+"/chat_asset_"+strings.Repeat("0", 32)+"/content", routeTestOwner))
		if missingAsset.status != http.StatusNotFound || missingAsset.message() != "图片不存在或已过期" {
			t.Fatalf("缺失资产 = %d %s", missingAsset.status, missingAsset.rawString())
		}
	})
}

type multipartPartW3 struct {
	fieldName   string
	filename    string
	contentType string
	data        []byte
	value       string
}

// multipartRequestW3 构造 multipart 请求，与 readMultipartImage 的解析契约对应。
func multipartRequestW3(t *testing.T, url string, parts []multipartPartW3, owner string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, part := range parts {
		if part.filename != "" {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", "form-data; name=\""+part.fieldName+"\"; filename=\""+part.filename+"\"")
			header.Set("Content-Type", part.contentType)
			content, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := content.Write(part.data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := writer.WriteField(part.fieldName, part.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", url, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if owner != "" {
		request.Header.Set("X-Test-Owner", owner)
	}
	return request
}

func mustGetW3(t *testing.T, url, owner string) *http.Request {
	t.Helper()
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if owner != "" {
		request.Header.Set("X-Test-Owner", owner)
	}
	return request
}

func previewQueryHeaderW3(t *testing.T, url, owner, header string) string {
	t.Helper()
	request := mustGetW3(t, url, owner)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	value := response.Header.Get(header)
	return value
}

func mimeForW3(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".png"):
		return "image/png"
	case strings.HasSuffix(filename, ".jpg"):
		return "image/jpeg"
	case strings.HasSuffix(filename, ".webp"):
		return "image/webp"
	}
	return "application/octet-stream"
}

// TestGenerationDepsRoutesW3 覆盖会话创建、模型目录与压缩触发路由。
func TestGenerationDepsRoutesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_models", routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, "chat_conv_models", 1)
	smallConversation := env.fixture.createConversation("chat_conv_compact", routeTestOwner)
	_ = smallConversation
	env.fixture.seedTurns(routeTestOwner, "chat_conv_compact", 1)
	prefix := "/__aisys__/api/my-chat"

	t.Run("创建会话默认模型", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, "{}")
		if response.status != http.StatusCreated {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
		data := response.dataMap()
		if data["defaultModel"] == nil {
			t.Fatalf("应返回默认模型: %v", data)
		}
	})
	t.Run("创建会话带 apiKeyId", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, `{"apiKeyId":"chat_key_provisioned"}`)
		if response.status != http.StatusCreated {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("创建会话未知键", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bogus":1}`)
		if response.status != http.StatusBadRequest || response.code() != "chat_invalid_request" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("创建会话非对象体", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, `[1]`)
		if response.status != http.StatusBadRequest {
			t.Fatalf("status=%d", response.status)
		}
	})
	t.Run("模型列表", func(t *testing.T) {
		response := env.do("GET", prefix+"/conversations/chat_conv_models/models", routeTestOwner, "")
		if response.status != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
		models := response.dataArray()
		if len(models) != 2 {
			t.Fatalf("模型数量 = %d", len(models))
		}
	})
	t.Run("模型详情", func(t *testing.T) {
		response := env.do("GET", prefix+"/conversations/chat_conv_models/models/gpt-5", routeTestOwner, "")
		if response.status != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
		data := response.dataMap()
		if data["id"] != "gpt-5" || data["name"] != "gpt-5" {
			t.Fatalf("能力载荷不正确: %v", data)
		}
	})
	t.Run("未知模型 404", func(t *testing.T) {
		response := env.do("GET", prefix+"/conversations/chat_conv_models/models/gpt-99", routeTestOwner, "")
		if response.status != http.StatusNotFound || response.code() != "chat_model_not_found" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("缺失会话模型 404", func(t *testing.T) {
		response := env.do("GET", prefix+"/conversations/none/models", routeTestOwner, "")
		if response.status != http.StatusNotFound {
			t.Fatalf("status=%d", response.status)
		}
	})
	t.Run("压缩触发缺模型", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations/chat_conv_models/context/compactions", routeTestOwner, "{}")
		if response.status != http.StatusBadRequest || response.message() != "请选择模型" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("压缩触发无可压缩内容", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations/chat_conv_compact/context/compactions", routeTestOwner, `{"model":"gpt-5"}`)
		if response.status != http.StatusConflict || response.code() != "no_compactable_turn" {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("压缩触发会话缺失", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations/none/context/compactions", routeTestOwner, `{"model":"gpt-5"}`)
		if response.status != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("压缩触发未知键", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations/chat_conv_models/context/compactions", routeTestOwner, `{"bogus":1}`)
		if response.status != http.StatusBadRequest {
			t.Fatalf("status=%d", response.status)
		}
	})
	t.Run("压缩服务未配置", func(t *testing.T) {
		previous := env.deps.Compactions
		env.deps.Compactions = nil
		defer func() { env.deps.Compactions = previous }()
		response := env.do("POST", prefix+"/conversations/chat_conv_models/context/compactions", routeTestOwner, `{"model":"gpt-5"}`)
		// DomainError 走 internal 分类：公共文案附净化详情。
		if response.status != http.StatusInternalServerError || !strings.Contains(response.message(), "上下文压缩启动失败") {
			t.Fatalf("status=%d body=%s", response.status, response.rawString())
		}
	})
	t.Run("压缩已进行中", func(t *testing.T) {
		previous := env.deps.Compactions
		env.deps.Compactions = nil
		rt := newChatRoutesForTest(env.deps)
		rt.claimAction("chat_conv_models", routeTestOwner, "compacting")
		request := httptest.NewRequest("POST", "/stream", strings.NewReader(`{"model":"gpt-5"}`))
		recorder := httptest.NewRecorder()
		request.SetPathValue("conversationId", "chat_conv_models")
		request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
		rt.compactionTrigger(recorder, request)
		env.deps.Compactions = previous
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("已进行中应 202: %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

// TestWriteChatRouteErrorW3 表驱动覆盖错误到 HTTP 的映射。
func TestWriteChatRouteErrorW3(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		status     int
		code       string
	}{
		{"无效请求", &invalidRequestError{Message: "坏请求"}, 400, "chat_invalid_request"},
		{"资产上传", &AssetUploadError{Code: "chat_asset_too_large", StatusCode: 413, Message: "太大"}, 413, "chat_asset_too_large"},
		{"会话不存在", &ConversationNotFoundError{}, 404, "chat_conversation_not_found"},
		{"冲突", &ConflictError{Code: ConflictMessageInProgress}, 409, "chat_message_in_progress"},
		{"模型能力", &ModelCapabilityError{Message: "不支持"}, 422, "chat_model_capability_unavailable"},
		{"未知错误", errors.New("boom"), 500, "internal_generation_failed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeChatRouteError(recorder, testCase.err)
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, 期望 %d", recorder.Code, testCase.status)
			}
			var payload messageCodePayload
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("载荷解析失败: %v", err)
			}
			if payload.Code != testCase.code {
				t.Fatalf("code = %s, 期望 %s", payload.Code, testCase.code)
			}
		})
	}
}

// TestStreamWithImageInputW3 覆盖带图片输入的完整流式链路。
func TestStreamWithImageInputW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_img", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	created := uploadAssetW3(t, server.URL+"/conversations/chat_conv_img/assets", routeTestOwner, "cat.png", mustDecodeBase64W3(testTinyPNGBase64))
	if created.status != http.StatusCreated {
		t.Fatalf("资产上传失败: %s", created.rawString())
	}
	assetID, _ := created.dataMap()["id"].(string)
	env.executor.steps = []scriptStep{{
		match:   func(call dispatchCall) bool { return call.Path == "/v1/responses" },
		respond: func(dispatchCall) *GenerationDispatchResponse { return sseResponse(responsesTextSSE("我看到了图片")) },
	}}
	payload := `{"clientMessageId":"cmid-img-1","content":"看这张图","model":"gpt-5","contentBlocks":[{"type":"input_text","text":"看这张图"},{"type":"input_image","assetId":"` + assetID + `"}]}`
	response := env.streamPost("chat_conv_img", routeTestOwner, payload)
	if response.status != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	if len(events) == 0 || events[0].event != "message.started" {
		t.Fatalf("事件流不正确: %+v", events)
	}
	var completed bool
	for _, event := range events {
		if event.event == "message.completed" {
			completed = true
		}
	}
	if !completed {
		t.Fatalf("缺少完成事件: %+v", events)
	}
	// 资产应已绑定到该轮。
	var turnID string
	if err := env.fixture.db.QueryRow(`SELECT turn_id FROM chat_assets WHERE id = ?`, assetID).Scan(&turnID); err != nil || turnID == "" {
		t.Fatalf("资产未绑定轮次: %v %q", err, turnID)
	}
	// 已绑定资产不能删除。
	deleted := doRequestW3(env.t, mustDeleteW3(env.t, server.URL+"/conversations/chat_conv_img/assets/"+assetID, routeTestOwner))
	if deleted.status != http.StatusConflict || deleted.code() != "chat_asset_not_deletable" {
		t.Fatalf("已绑定资产删除 = %d %s", deleted.status, deleted.rawString())
	}
	// 第二轮：上传第二张图以保持 responses 协议；历史图片说明未完成 → 422 image_pending。
	created2 := uploadAssetW3(t, server.URL+"/conversations/chat_conv_img/assets", routeTestOwner, "dog.png", mustDecodeBase64W3(testTinyPNGBase64))
	assetID2, _ := created2.dataMap()["id"].(string)
	if assetID2 == "" {
		t.Fatalf("第二张资产上传失败: %s", created2.rawString())
	}
	env.executor.steps = []scriptStep{}
	second := env.streamPost("chat_conv_img", routeTestOwner, `{"clientMessageId":"cmid-img-2","content":"再看","model":"gpt-5","contentBlocks":[{"type":"input_text","text":"再看"},{"type":"input_image","assetId":"` + assetID2 + `"}]}`)
	if second.status != http.StatusUnprocessableEntity || second.code() != "chat_model_context_image_pending" {
		t.Fatalf("第二轮 = %d %s", second.status, second.rawString())
	}
}

func mustDeleteW3(t *testing.T, url, owner string) *http.Request {
	t.Helper()
	request, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test-Owner", owner)
	return request
}

// TestResolveChatAssetInputBranchesW3 直接驱动资产输入解析的错误分支。
func TestResolveChatAssetInputBranchesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_resolve", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	empty, err := rt.resolveChatAssetInput(nil, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO)
	if err != nil || len(empty.Blocks) != 0 {
		t.Fatalf("空输入应返回空解析: %+v err=%v", empty, err)
	}
	if _, err := rt.resolveChatAssetInput([]InputContentBlock{{Type: "input_image", AssetID: strPtrT(" ")}}, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO); err == nil {
		t.Fatalf("空资产 ID 应报错")
	}
	ids := make([]InputContentBlock, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, InputContentBlock{Type: "input_image", AssetID: strPtrT("chat_asset_" + strings.Repeat(string(rune('a'+i)), 32))})
	}
	if _, err := rt.resolveChatAssetInput(ids, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO); err == nil {
		t.Fatalf("超 5 张应报错")
	}
	duplicated := []InputContentBlock{
		{Type: "input_image", AssetID: strPtrT("chat_asset_" + strings.Repeat("a", 32))},
		{Type: "input_image", AssetID: strPtrT("chat_asset_" + strings.Repeat("a", 32))},
	}
	if _, err := rt.resolveChatAssetInput(duplicated, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO); err == nil {
		t.Fatalf("重复资产应报错")
	}
	missing := []InputContentBlock{{Type: "input_image", AssetID: strPtrT("chat_asset_" + strings.Repeat("e", 32))}}
	if _, err := rt.resolveChatAssetInput(missing, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO); err == nil {
		t.Fatalf("缺失资产应报错")
	}
	// 非法格式 ID 走 DomainError。
	malformed := []InputContentBlock{{Type: "input_image", AssetID: strPtrT("not-an-id")}}
	if _, err := rt.resolveChatAssetInput(malformed, routeTestOwner, "chat_conv_resolve", env.fixture.nowISO); err == nil {
		t.Fatalf("非法 ID 应报错")
	}
}

// TestReadVerifiedChatAssetTamperW3 覆盖资产完整性校验失败分支。
func TestReadVerifiedChatAssetTamperW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_tamper", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	created := uploadAssetW3(t, server.URL+"/conversations/chat_conv_tamper/assets", routeTestOwner, "cat.png", mustDecodeBase64W3(testTinyPNGBase64))
	assetID, _ := created.dataMap()["id"].(string)
	if assetID == "" {
		t.Fatalf("缺少资产 ID: %s", created.rawString())
	}
	asset, err := env.deps.Store.GetAsset(assetID, routeTestOwner, "chat_conv_tamper", env.fixture.nowISO)
	if err != nil || asset == nil {
		t.Fatalf("读取资产失败: %v", err)
	}
	rt := newChatRoutesForTest(env.deps)
	if _, err := rt.readVerifiedChatAsset(asset); err != nil {
		t.Fatalf("完整资产校验应通过: %v", err)
	}
	// 篡改 SHA 后完整性校验失败。
	tampered := *asset
	badSHA := strings.Repeat("0", 64)
	tampered.ProcessedSha256 = &badSHA
	if _, err := rt.readVerifiedChatAsset(&tampered); err == nil || !strings.Contains(err.Error(), "完整性校验失败") {
		t.Fatalf("篡改哈希应报错: %v", err)
	}
	// 字节数不一致 → 大小校验失败。
	wrongSize := *asset
	sizeMismatch := int64(12345)
	wrongSize.ProcessedBytes = &sizeMismatch
	if _, err := rt.readVerifiedChatAsset(&wrongSize); err == nil || !strings.Contains(err.Error(), "大小校验失败") {
		t.Fatalf("字节数不一致应报错: %v", err)
	}
	// 处理字段缺失 → 不完整。
	incomplete := Asset{ID: asset.ID}
	if _, err := rt.readVerifiedChatAsset(&incomplete); err == nil || !strings.Contains(err.Error(), "不完整") {
		t.Fatalf("字段缺失应报错: %v", err)
	}
}

// TestStreamTurnErrorBranchesW3 覆盖流式路由的快速失败分支。
func TestStreamTurnErrorBranchesW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_stream", routeTestOwner)
	prefix := "/__aisys__/api/my-chat"

	t.Run("请求体非法", func(t *testing.T) {
		response := env.do("POST", prefix+"/conversations/chat_conv_stream/stream", routeTestOwner, `{"clientMessageId":1}`)
		if response.status != http.StatusBadRequest {
			t.Fatalf("status=%d", response.status)
		}
	})
	t.Run("会话不存在", func(t *testing.T) {
		response := env.streamPost("chat_conv_none", routeTestOwner, streamPayload("cmid-1", "内容", "gpt-5"))
		if response.status != http.StatusNotFound {
			t.Fatalf("status=%d", response.status)
		}
	})
	t.Run("重复提交", func(t *testing.T) {
		env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("好", true))
		}}}
		first := env.streamPost("chat_conv_stream", routeTestOwner, streamPayload("cmid-dup", "内容", "gpt-5"))
		if first.status != http.StatusOK {
			t.Fatalf("首轮应成功: %d %s", first.status, first.rawString())
		}
		second := env.streamPost("chat_conv_stream", routeTestOwner, streamPayload("cmid-dup", "内容", "gpt-5"))
		if second.status != http.StatusConflict || second.code() != "chat_message_already_exists" {
			t.Fatalf("重复提交 = %d %s", second.status, second.rawString())
		}
	})
	t.Run("轮次上限", func(t *testing.T) {
		previous := env.deps.MaxTurnsPerConversation
		env.deps.MaxTurnsPerConversation = 0
		response := env.streamPost("chat_conv_stream", routeTestOwner, streamPayload("cmid-limit", "内容", "gpt-5"))
		env.deps.MaxTurnsPerConversation = previous
		if response.status != http.StatusConflict || response.code() != "chat_turn_limit_exceeded" {
			t.Fatalf("轮次上限 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("网关密钥不可用", func(t *testing.T) {
		previous := env.deps.GatewayKeys
		env.deps.GatewayKeys = nil
		response := env.streamPost("chat_conv_stream", routeTestOwner, streamPayload("cmid-nokey", "内容", "gpt-5"))
		env.deps.GatewayKeys = previous
		if response.status != http.StatusInternalServerError {
			t.Fatalf("网关密钥缺失 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("思考级别不支持", func(t *testing.T) {
		response := env.streamPost("chat_conv_stream", routeTestOwner, `{"clientMessageId":"cmid-effort","content":"内容","model":"gpt-5","reasoningEffort":"max"}`)
		if response.status != http.StatusUnprocessableEntity || response.message() != "当前模型不支持所选思考级别，请重新选择" {
			t.Fatalf("思考级别 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("生成参数超范围", func(t *testing.T) {
		response := env.streamPost("chat_conv_stream", routeTestOwner, `{"clientMessageId":"cmid-param","content":"内容","model":"gpt-5","generationParameters":{"temperature":0.5,"topP":0.5}}`)
		if response.status != http.StatusUnprocessableEntity || response.message() != "温度和 Top P 只能设置其中一个" {
			t.Fatalf("参数冲突 = %d %s", response.status, response.rawString())
		}
	})
}

// TestWriteStreamRouteErrorW3 覆盖流式错误映射分支。
func TestWriteStreamRouteErrorW3(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"准备取消", &PreparationCanceledError{}, 499},
		{"上下文压缩中", &ConflictError{Code: ConflictContextCompacting}, 409},
		{"模型能力", &ModelCapabilityError{Message: "不支持"}, 422},
		{"模型上下文", &ChatModelContextError{Message: "超限", Reason: ModelContextLoadLimit}, 422},
		{"未知", errors.New("boom"), 500},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeStreamRouteError(recorder, testCase.err)
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, 期望 %d body=%s", recorder.Code, testCase.status, recorder.Body.String())
			}
		})
	}
	// 模型上下文错误的 code 后缀。
	recorder := httptest.NewRecorder()
	writeStreamRouteError(recorder, &ChatModelContextError{Message: "超限", Reason: ModelContextLoadLimit})
	if !strings.Contains(recorder.Body.String(), "chat_model_context_load_limit") {
		t.Fatalf("code 后缀不正确: %s", recorder.Body.String())
	}
	// 行为存疑：writeStreamRouteError 的 budget/request/assetInput 分支委托给
	// writeChatRouteError，而后者不识别这三类错误，实际降级为 500
	// internal_generation_failed（Node 契约为 422 + 专用 code）。
	for name, err := range map[string]error{
		"上下文预算": &ContextBudgetError{},
		"请求错误":  &RequestError{Code: RequestImageNotSupported, Message: "不支持图片"},
		"资产输入":  &ChatAssetInputError{Message: "资产不可用"},
	} {
		t.Run("降级500/"+name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeStreamRouteError(recorder, err)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("当前实际行为应为 500: %d", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), "internal_generation_failed") {
				t.Fatalf("应降级为内部错误: %s", recorder.Body.String())
			}
		})
	}
}

// TestQueryHelpersW3 覆盖查询参数与请求体 helper。
func TestQueryHelpersW3(t *testing.T) {
	request := httptest.NewRequest("POST", "/x", strings.NewReader(`{"a":1}`))
	raw, err := readJSONBody(request)
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("readJSONBody 失败: %s %v", raw, err)
	}
	empty := httptest.NewRequest("POST", "/x", strings.NewReader(""))
	body, err := readJSONBody(empty)
	if err != nil || string(body) != "{}" {
		t.Fatalf("空体应渲染为空对象: %s %v", body, err)
	}
	invalid := httptest.NewRequest("POST", "/x", strings.NewReader("{bad"))
	if _, err := readJSONBody(invalid); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	if _, err := decodeObjectBody(json.RawMessage("[]")); err == nil {
		t.Fatalf("数组体应报错")
	}
	if got := jsonValueTypeName(json.RawMessage(`"x"`)); got != "string" {
		t.Fatalf("jsonValueTypeName(string) = %s", got)
	}
	if got := jsonValueTypeName(json.RawMessage(" true ")); got != "boolean" {
		t.Fatalf("jsonValueTypeName(bool) = %s", got)
	}
	if got := jsonValueTypeName(json.RawMessage(" null")); got != "null" {
		t.Fatalf("jsonValueTypeName(null) = %s", got)
	}
	if got := jsonValueTypeName(json.RawMessage("  ")); got != "undefined" {
		t.Fatalf("jsonValueTypeName(empty) = %s", got)
	}
	if value, err := boundedTrimmedString(json.RawMessage(`"  hi  "`), 10); err != nil || *value != "hi" {
		t.Fatalf("boundedTrimmedString 失败: %v %v", value, err)
	}
	if _, err := boundedTrimmedString(json.RawMessage(`" "`), 10); err == nil {
		t.Fatalf("空白应报错")
	}
	if _, err := boundedTrimmedString(json.RawMessage(`"超长"`), 1); err == nil {
		t.Fatalf("超长应报错")
	}
	if err := ensureStrictQueryKeys(map[string][]string{"limit": {"1"}}, "limit"); err != nil {
		t.Fatalf("合法键应通过: %v", err)
	}
	if err := ensureStrictQueryKeys(map[string][]string{"bogus": {"1"}}, "limit"); err == nil {
		t.Fatalf("未知键应报错")
	}
	if value, ok, err := queryScalarInteger(map[string][]string{"limit": {" 5 "}}, "limit"); err != nil || !ok || value != 5 {
		t.Fatalf("queryScalarInteger 失败: %v %v %v", value, ok, err)
	}
	if _, ok, _ := queryScalarInteger(map[string][]string{"limit": {""}}, "limit"); ok {
		t.Fatalf("空值应视为缺失")
	}
	if _, _, err := queryScalarInteger(map[string][]string{"limit": {"x"}}, "limit"); err == nil {
		t.Fatalf("非数字应报错")
	}
	if textQuery("  ") != nil || *textQuery(" a ") != "a" {
		t.Fatalf("textQuery 契约不正确")
	}
	if optionalBooleanQuery("TRUE") == nil || *optionalBooleanQuery("0") != false || optionalBooleanQuery("bogus") != nil {
		t.Fatalf("optionalBooleanQuery 契约不正确")
	}
	if integerQuery("", 30, 1, 50) != 30 || integerQuery("x", 30, 1, 50) != 30 || integerQuery("0", 30, 1, 50) != 30 || integerQuery("200", 30, 1, 50) != 50 || integerQuery("5", 30, 1, 50) != 5 {
		t.Fatalf("integerQuery 契约不正确")
	}
}

// TestParseUpdateConversationBodyW3 覆盖会话更新体校验。
func TestParseUpdateConversationBodyW3(t *testing.T) {
	fields, err := parseUpdateConversationBody(map[string]json.RawMessage{
		"title": json.RawMessage(`" 新标题 "`), "isPinned": json.RawMessage("true"), "defaultImageModel": json.RawMessage(`"gpt-image-2"`),
	})
	if err != nil || *fields.title != "新标题" || *fields.isPinned != true {
		t.Fatalf("合法更新解析失败: %+v err=%v", fields, err)
	}
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"title 非字符串", `{"title":1}`, "Expected string"},
		{"title 空", `{"title":" "}`, "请输入会话标题"},
		{"title 超长", `{"title":"` + strings.Repeat("题", 61) + `"}`, "会话标题最多 60 个字符"},
		{"isPinned 非布尔", `{"isPinned":"x"}`, "Expected boolean"},
		{"defaultImageModel 非法", `{"defaultImageModel":"dall-e"}`, "Invalid enum value"},
		{"未知键", `{"bogus":1}`, "Unrecognized key"},
		{"无字段", `{}`, "没有可更新的会话字段"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(testCase.raw), &raw); err != nil {
				t.Fatalf("载荷无效: %v", err)
			}
			_, err := parseUpdateConversationBody(raw)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, 期望包含 %q", err, testCase.wantErr)
			}
		})
	}
}

// TestContextBudgetHelpersW3 覆盖上下文预算估算。
func TestContextBudgetHelpersW3(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{TokenCount: func(text string) int { return len(text) }}}
	if rt.tokenCount("abcd") != 4 {
		t.Fatalf("注入计数器应生效")
	}
	defaultRoutes := &chatRoutes{deps: &Deps{}}
	if defaultRoutes.tokenCount("abcdef") != 2 {
		t.Fatalf("默认计数器应 len/4: %d", defaultRoutes.tokenCount("abcdef"))
	}
	if rt.estimateChatTokens("") != 1 || rt.estimateChatTokens("abcd") != 4 {
		t.Fatalf("estimateChatTokens 契约不正确")
	}
	if rt.estimateContentTokens("abcd") != 4 {
		t.Fatalf("字符串内容应直接计数")
	}
	if rt.estimateContentTokens(map[string]any{"a": 1}) <= 0 {
		t.Fatalf("对象内容应按 JSON 计数")
	}
	input := fixedChatBudgetInput{
		CurrentUserContent: "abcd", Instructions: "abcd",
		EffectiveTools: []string{"web_search"}, InternalTools: []*toolDefinition{testToolW3("t")},
		ImageTokenEstimate: 100,
	}
	total := rt.fixedChatInputTokens(input)
	if total < protocolReserveTokens+8+24+toolDefinitionReserveTokens+100 {
		t.Fatalf("预算构成不正确: %d", total)
	}
	if err := rt.validateFixedChatInputBudget(fixedChatBudgetInput{MaxInputTokens: nil}); err != nil {
		t.Fatalf("无上限应通过")
	}
	if err := rt.validateFixedChatInputBudget(fixedChatBudgetInput{MaxInputTokens: int64PtrT(1), CurrentUserContent: "a"}); err == nil {
		t.Fatalf("超预算应报错")
	}
	history := []ChatTransportMessage{{Role: "user", Content: "abcd"}, {Role: "assistant", Content: map[string]any{"x": 1}}}
	estimated := rt.estimateChatInputTokens(input, history)
	if estimated <= total {
		t.Fatalf("历史应累加: %d <= %d", estimated, total)
	}
	if estimateChatImageTokens(nil, nil) != 2500 || estimateChatImageTokens(int64PtrT(0), int64PtrT(8)) != 2500 {
		t.Fatalf("未知尺寸应为 2500")
	}
	if got := estimateChatImageTokens(int64PtrT(32), int64PtrT(32)); got != 85+1*1 {
		t.Fatalf("32x32 token = %d", got)
	}
	if completeMessagePairs(nil, "turn-1") == nil {
		t.Fatalf("空输入应返回空切片")
	}
	pairs := completeMessagePairs([]contextSourceMessage{
		{role: "assistant", turnID: "t1", sequenceNo: 4},
		{role: "user", turnID: "t2", sequenceNo: 5},
		{role: "assistant", turnID: "t2", sequenceNo: 6},
	}, "")
	if len(pairs) != 2 || pairs[0].sequenceNo != 5 {
		t.Fatalf("完整消息对筛选不正确: %+v", pairs)
	}
	excluded := completeMessagePairs([]contextSourceMessage{
		{role: "user", turnID: "t1", sequenceNo: 1},
		{role: "assistant", turnID: "t1", sequenceNo: 2},
	}, "t1")
	if len(excluded) != 0 {
		t.Fatalf("排除轮次应过滤: %+v", excluded)
	}
	if got := truncateControlChars("a\x00b\x7fc", 10); got != "a b c" {
		t.Fatalf("控制字符应替换: %q", got)
	}
	if got := truncateControlChars(strings.Repeat("字", 20), 3); got != "字字字" {
		t.Fatalf("截断契约不正确: %q", got)
	}
	entries := formatCheckpointEntries([]contextEntry{{kind: "task_state", provenance: "assistant", contentJSON: `{"a":1}`}})
	if !strings.Contains(entries, "压缩记忆") || !strings.Contains(entries, "task_state") {
		t.Fatalf("checkpoint 渲染不正确: %q", entries)
	}
	imageIndex := formatChatImageContextIndex([]ImageGenerationRecord{{AssetID: "asset-1", Operation: "generate", Model: "gpt-image-2", Prompt: "提示\x01词"}})
	if !strings.Contains(imageIndex, "谱系索引") || strings.Contains(imageIndex, "\x01") {
		t.Fatalf("图像谱系渲染不正确: %q", imageIndex)
	}
	_ = context.Background()
}
