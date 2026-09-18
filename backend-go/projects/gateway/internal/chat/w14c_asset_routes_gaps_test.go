package chat

// w14c 覆盖率补齐：chat 资产路由的错误分支（multipart 边界、存储故障、
// 内容变体与删除认领链路）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func w14cSha256(data []byte) string {
	digest := sha256.Sum256(data)
	return hexEncode(digest[:])
}

// w14cFailProcessor 可编程失败的图片处理器。
type w14cFailProcessor struct {
	result *ProcessedImage
	err    error
}

func (p w14cFailProcessor) ProcessUpload([]byte, string) (*ProcessedImage, error) {
	return p.result, p.err
}
func (p w14cFailProcessor) CreatePreview(data []byte) (*ProcessedImage, error) {
	return stubImageProcessor{}.CreatePreview(data)
}

func w14cFaultRoutes(t *testing.T) (*chatRoutes, *generationEnv, *faultScriptW10D, string) {
	t.Helper()
	fixture, script := newFaultChatFixtureW10D(t)
	env := buildGenerationEnvW10D(t, fixture)
	conversationID := "chat_conv_w14c"
	env.fixture.createConversation(conversationID, routeTestOwner)
	return newChatRoutesForTest(env.deps), env, script, conversationID
}

func w14cFilePart(writer *multipart.Writer, filename, contentType string, data []byte) {
	t := &testing.T{}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
}

func w14cInvokeUpload(rt *chatRoutes, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	rt.uploadAsset(recorder, request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner})))
	return recorder
}

func w14cUploadSimple(t *testing.T, rt *chatRoutes, conversationID string) *httptest.ResponseRecorder {
	t.Helper()
	request := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		w14cFilePart(writer, "a.png", "image/png", []byte("w14c-png-data"))
	})
	request.SetPathValue("conversationId", conversationID)
	return w14cInvokeUpload(rt, request)
}

// TestW14CUploadAssetStoreFaults 覆盖上传链路的存储/处理器错误分支。
func TestW14CUploadAssetStoreFaults(t *testing.T) {
	rt, env, script, conversationID := w14cFaultRoutes(t)

	// 槽位断言存储错误 → 通用错误透传。
	script.failOnce("AND source_kind = 'user_upload'")
	if recorder := w14cUploadSimple(t, rt, conversationID); recorder.Code == 0 || recorder.Code == http.StatusCreated {
		t.Fatalf("槽位断言错误必须失败: %d %s", recorder.Code, recorder.Body.String())
	}

	// 每消息 5 张上限：预置 5 张未提交资产 → availableSlots<=0。
	for i := 0; i < 5; i++ {
		if _, err := env.fixture.store.CreateChatAsset(CreateChatAssetInput{
			SystemAccountID:  routeTestOwner,
			ConversationID:   conversationID,
			SourceKind:       "user_upload",
			OriginalFilename: "w14c.png",
			OriginalMimeType: "image/png",
			OriginalBytes:    4,
			OriginalSha256:   w14cSha256([]byte("w14c-seed")),
			QuotaBytes:       4,
			Now:              env.fixture.nowISO,
			RetentionDays:    30,
		}); err != nil {
			t.Fatal(err)
		}
	}
	recorder := w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusBadRequest || w11bAssetCode(recorder) != "chat_asset_count_exceeded" {
		t.Fatalf("slots = %d %s", recorder.Code, recorder.Body.String())
	}
	// 清空预置资产，恢复后续场景的可用槽位。
	if _, err := env.fixture.db.Exec(`DELETE FROM chat_assets WHERE conversation_id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.db.Exec(`UPDATE chat_user_asset_usage SET asset_bytes = 0, asset_count = 0 WHERE system_account_id = ?`, routeTestOwner); err != nil {
		t.Fatal(err)
	}

	// 处理器返回 ImageProcessingError → 415 透传原因。
	env.deps.ImageProcessor = w14cFailProcessor{err: &ImageProcessingError{Message: "w14c 解码失败"}}
	recorder = w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusUnsupportedMediaType || !strings.Contains(recorder.Body.String(), "w14c 解码失败") {
		t.Fatalf("processing = %d %s", recorder.Code, recorder.Body.String())
	}

	// 处理器返回通用错误 → 415 通用文案。
	env.deps.ImageProcessor = w14cFailProcessor{err: errInjectedW10D}
	recorder = w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusUnsupportedMediaType || w11bAssetCode(recorder) != "chat_asset_unsupported_type" {
		t.Fatalf("generic processing = %d %s", recorder.Code, recorder.Body.String())
	}

	// 处理器声明的实际格式与上传 MIME 不一致 → 415。
	env.deps.ImageProcessor = w14cFailProcessor{result: &ProcessedImage{
		Buffer: []byte("w14c"), OriginalMimeType: "image/webp", MimeType: "image/webp",
		Width: 2, Height: 2, ByteSize: 4, SHA256: "w14c-sha",
	}}
	recorder = w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusUnsupportedMediaType || !strings.Contains(recorder.Body.String(), "不一致") {
		t.Fatalf("mime mismatch = %d %s", recorder.Code, recorder.Body.String())
	}

	// 建档后的处理完成更新失败 → 错误透传。
	env.deps.ImageProcessor = stubImageProcessor{}
	script.failOnce("SET processed_mime_type = ?")
	recorder = w14cUploadSimple(t, rt, conversationID)
	if recorder.Code == 0 || recorder.Code == http.StatusCreated {
		t.Fatalf("处理完成更新失败必须报错: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW14CReadMultipartImageEdges 覆盖 multipart 解析的边界分支。
func TestW14CReadMultipartImageEdges(t *testing.T) {
	rt, _, _, conversationID := w14cFaultRoutes(t)

	// multipart 头但参数非法 → ParseMediaType 失败。
	broken := httptest.NewRequest("POST", "/conversations/x/assets", strings.NewReader("junk"))
	broken.Header.Set("Content-Type", "multipart/form-data;boundary")
	broken.SetPathValue("conversationId", conversationID)
	if recorder := w14cInvokeUpload(rt, broken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("broken content-type = %d", recorder.Code)
	}

	// multipart 头合法但正文不是 multipart → MultipartReader 失败。
	plain := httptest.NewRequest("POST", "/conversations/x/assets", strings.NewReader("junk"))
	plain.Header.Set("Content-Type", "multipart/form-data; boundary=NeitherHere")
	plain.SetPathValue("conversationId", conversationID)
	if recorder := w14cInvokeUpload(rt, plain); recorder.Code != http.StatusBadRequest {
		t.Fatalf("plain body = %d", recorder.Code)
	}

	// 三个 part → partCount>2。
	triple := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		if err := writer.WriteField("note", "1"); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteField("note2", "2"); err != nil {
			t.Fatal(err)
		}
		w14cFilePart(writer, "a.png", "image/png", []byte("x"))
	})
	triple.SetPathValue("conversationId", conversationID)
	if recorder := w14cInvokeUpload(rt, triple); recorder.Code != http.StatusBadRequest {
		t.Fatalf("three parts = %d", recorder.Code)
	}

	// file 之后再出现普通字段 → “每次只能上传一张图片”。
	fileThenField := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		w14cFilePart(writer, "a.png", "image/png", []byte("x"))
		if err := writer.WriteField("note", "late"); err != nil {
			t.Fatal(err)
		}
	})
	fileThenField.SetPathValue("conversationId", conversationID)
	if recorder := w14cInvokeUpload(rt, fileThenField); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "每次只能上传一张图片") {
		t.Fatalf("file then note = %d %s", recorder.Code, recorder.Body.String())
	}

	// 正文截断：part 头之后被切断 → 读取中断。
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	w14cFilePart(writer, "a.png", "image/png", bytes.Repeat([]byte("y"), 4096))
	_ = writer.Close()
	truncatedBody := buffer.Bytes()[:len(buffer.Bytes())/2]
	truncated := httptest.NewRequest("POST", "/conversations/x/assets", bytes.NewReader(truncatedBody))
	truncated.Header.Set("Content-Type", writer.FormDataContentType())
	truncated.SetPathValue("conversationId", conversationID)
	if recorder := w14cInvokeUpload(rt, truncated); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "中断") {
		t.Fatalf("truncated = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW14CAssetContentVariants 覆盖内容路由的变体、下载头与读取失败。
func TestW14CAssetContentVariants(t *testing.T) {
	rt, env, script, conversationID := w14cFaultRoutes(t)

	recorder := w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", recorder.Code, recorder.Body.String())
	}
	var metadata struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &metadata)
	assetID := metadata.Data.ID
	if assetID == "" {
		t.Fatal("资产 ID 缺失")
	}

	getContent := func(asset, query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/assets/"+asset+"/content"+query, nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("assetId", asset)
		response := httptest.NewRecorder()
		rt.assetContent(response, request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner})))
		return response
	}

	// 未知资产 → 404 文案。
	if response := getContent("chat_asset_missing", ""); response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), "图片不存在或已过期") {
		t.Fatalf("missing asset = %d %s", response.Code, response.Body.String())
	}

	// download=1 + JPEG → jpg 扩展名。
	if _, err := env.fixture.db.Exec(`UPDATE chat_assets SET processed_mime_type = 'image/jpeg' WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	response := getContent(assetID, "?download=1")
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, ".jpg") {
		t.Fatalf("jpg 下载头缺失: %q", disposition)
	}

	// 预览变体：预置预览键并写对象。
	previewKey := "w14c/preview.webp"
	previewData := []byte("w14c-preview")
	previewSha := w14cSha256(previewData)
	if err := env.deps.ObjectStore.Write(previewKey, previewData, chatAssetPreviewMaxBytes, previewSha); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.db.Exec(`UPDATE chat_assets SET
		preview_storage_key = ?, preview_mime_type = 'image/webp', preview_sha256 = ?, preview_bytes = ?
		WHERE id = ?`, previewKey, previewSha, len(previewData), assetID); err != nil {
		t.Fatal(err)
	}
	response = getContent(assetID, "?variant=preview")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("preview = %d %s", response.Code, response.Body.String())
	}

	// assistant_generated 源：assistantGenerated 上限分支。
	if _, err := env.fixture.db.Exec(`UPDATE chat_assets SET source_kind = 'assistant_generated' WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	response = getContent(assetID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("generated = %d %s", response.Code, response.Body.String())
	}

	// 对象读取失败：存储键不存在。
	if _, err := env.fixture.db.Exec(`UPDATE chat_assets SET storage_key = 'w14c/missing.bin' WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	if response = getContent(assetID, ""); response.Code == http.StatusOK {
		t.Fatalf("缺失对象必须报错: %d", response.Code)
	}

	// 资产查询故障。
	script.failOnce("AND cleanup_status = 'active' AND expires_at > ?")
	response = getContent(assetID, "")
	if response.Code == 0 || response.Code == http.StatusOK {
		t.Fatalf("资产查询故障必须报错: %d", response.Code)
	}

	// 会话查询故障。
	script.failOnce("FROM chat_conversations")
	if response = getContent(assetID, ""); response.Code == http.StatusOK {
		t.Fatalf("会话查询故障必须报错: %d", response.Code)
	}
}

// TestW14CDeleteAssetFaultArms 覆盖删除链路的认领与完成故障。
func TestW14CDeleteAssetFaultArms(t *testing.T) {
	rt, _, script, conversationID := w14cFaultRoutes(t)

	recorder := w14cUploadSimple(t, rt, conversationID)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload = %d", recorder.Code)
	}
	var metadata struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &metadata)
	assetID := metadata.Data.ID

	deleteAsset := func(asset string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("DELETE", "/conversations/"+conversationID+"/assets/"+asset, nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("assetId", asset)
		response := httptest.NewRecorder()
		rt.deleteAsset(response, request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner})))
		return response
	}

	// 认领失败。
	script.failOnce("AS asset")
	if response := deleteAsset(assetID); response.Code == http.StatusNoContent {
		t.Fatal("认领失败必须报错")
	}

	// 完成删除失败 → 回滚认领并报错。
	script.failOnce("DELETE FROM chat_assets")
	if response := deleteAsset(assetID); response.Code == http.StatusNoContent {
		t.Fatal("完成删除失败必须报错")
	}

	// 正常删除。
	if response := deleteAsset(assetID); response.Code != http.StatusNoContent {
		t.Fatalf("删除 = %d %s", response.Code, response.Body.String())
	}

	// 会话查询故障。
	script.failOnce("FROM chat_conversations")
	if response := deleteAsset(assetID); response.Code == http.StatusNoContent {
		t.Fatal("会话查询故障必须报错")
	}
}


