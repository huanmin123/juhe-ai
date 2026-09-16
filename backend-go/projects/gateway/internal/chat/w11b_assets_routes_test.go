package chat

// w11b 波次：chat 资产路由（上传/内容/删除）的校验、配额与状态分支，
// 以及 multipart 读取、ETag、下载头与对象存储失败路径。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const w11bPrefix = "/__aisys__/api/my-chat"

func w11bMultipartRequest(t *testing.T, body func(writer *multipart.Writer)) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	body(writer)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/conversations/chat_conv_w11b/assets", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func w11bAuthed(request *http.Request) *http.Request {
	return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
}

func w11bRoutesAndConversation(t *testing.T) (*chatRoutes, *generationEnv, string) {
	t.Helper()
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w11b"
	env.fixture.createConversation(conversationID, routeTestOwner)
	return newChatRoutesForTest(env.deps), env, conversationID
}

func w11bInvokeUpload(rt *chatRoutes, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	rt.uploadAsset(recorder, w11bAuthed(request))
	return recorder
}

func w11bUploadPNG(t *testing.T, rt *chatRoutes, filename, contentType string, data []byte, extraField bool) *httptest.ResponseRecorder {
	t.Helper()
	request := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		if extraField {
			if err := writer.WriteField("note", "x"); err != nil {
				t.Fatal(err)
			}
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		if contentType == "" {
			contentType = "image/png"
		}
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	})
	request.SetPathValue("conversationId", "chat_conv_w11b")
	recorder := httptest.NewRecorder()
	rt.uploadAsset(recorder, w11bAuthed(request))
	return recorder
}

func w11bAssetCode(recorder *httptest.ResponseRecorder) string {
	var payload struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return payload.Code
}

func TestW11BUploadAssetRouteValidation(t *testing.T) {
	rt, _, _ := w11bRoutesAndConversation(t)

	// 非 multipart。
	plain := httptest.NewRequest("POST", "/conversations/chat_conv_w11b/assets", strings.NewReader("{}"))
	plain.Header.Set("Content-Type", "application/json")
	plain.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, plain); recorder.Code != http.StatusBadRequest || w11bAssetCode(recorder) != "chat_asset_invalid_request" {
		t.Fatalf("plain = %d %s", recorder.Code, recorder.Body.String())
	}

	// 无 file 字段。
	empty := w11bMultipartRequest(t, func(writer *multipart.Writer) {})
	empty.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, empty); recorder.Code != http.StatusBadRequest || w11bAssetCode(recorder) != "chat_asset_invalid_request" {
		t.Fatalf("empty = %d %s", recorder.Code, recorder.Body.String())
	}

	// 额外表单字段（file 前出现 note）。
	if recorder := w11bUploadPNG(t, rt, "a.png", "", []byte("png"), true); recorder.Code != http.StatusBadRequest {
		t.Fatalf("extra field = %d %s", recorder.Code, recorder.Body.String())
	}

	// 两个 file 字段。
	request := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		_ = writer
		for range []int{0, 1} {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", `form-data; name="file"; filename="a.png"`)
			header.Set("Content-Type", "image/png")
			part, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("png")); err != nil {
				t.Fatal(err)
			}
		}
	})
	request.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, request); recorder.Code != http.StatusBadRequest {
		t.Fatalf("two files = %d %s", recorder.Code, recorder.Body.String())
	}

	// 空文件。
	if recorder := w11bUploadPNG(t, rt, "a.png", "", nil, false); recorder.Code != http.StatusBadRequest || w11bAssetCode(recorder) != "chat_asset_invalid_request" {
		t.Fatalf("empty file = %d %s", recorder.Code, recorder.Body.String())
	}

	// 空文件名。
	request = w11bMultipartRequest(t, func(writer *multipart.Writer) {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename=""`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("png")); err != nil {
			t.Fatal(err)
		}
	})
	request.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, request); recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty filename = %d %s", recorder.Code, recorder.Body.String())
	}

	// 成功上传（stub 处理器回写 webp）。
	recorder := w11bUploadPNG(t, rt, "a.png", "", []byte("png-data"), false)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("ok = %d %s", recorder.Code, recorder.Body.String())
	}
	var metadata struct {
		Data struct {
			ID       string `json:"id"`
			MimeType string `json:"mimeType"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if metadata.Data.ID == "" || metadata.Data.MimeType != "image/webp" {
		t.Fatalf("metadata = %+v", metadata.Data)
	}

	// 超过 3 MiB。
	huge := bytes.Repeat([]byte("x"), chatAssetOriginalMaxBytes+1)
	recorder = w11bUploadPNG(t, rt, "big.png", "", huge, false)
	if recorder.Code != http.StatusRequestEntityTooLarge || w11bAssetCode(recorder) != "chat_asset_too_large" {
		t.Fatalf("huge = %d %s", recorder.Code, recorder.Body.String())
	}

	// 会话不存在。
	missing := w11bMultipartRequest(t, func(writer *multipart.Writer) {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename="a.png"`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("png")); err != nil {
			t.Fatal(err)
		}
	})
	missing.SetPathValue("conversationId", "chat_conv_missing")
	recorder = httptest.NewRecorder()
	rt.uploadAsset(recorder, w11bAuthed(missing))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing conversation = %d %s", recorder.Code, recorder.Body.String())
	}

	// 未登录。
	anonymous := httptest.NewRequest("POST", "/conversations/chat_conv_w11b/assets", strings.NewReader("{}"))
	recorder = httptest.NewRecorder()
	rt.uploadAsset(recorder, anonymous)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("anonymous = %d", recorder.Code)
	}
}

// w11bMimePartWriter 构造带显式 Content-Type 的 file part。
func w11bMimePartRequest(t *testing.T, filename, partContentType string, data []byte) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	if partContentType != "" {
		header.Set("Content-Type", partContentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/conversations/chat_conv_w11b/assets", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestW11BUploadAssetMimeHandling(t *testing.T) {
	rt, _, _ := w11bRoutesAndConversation(t)

	// 显式 image/jpg 归一化为 image/jpeg 且通过 stub。
	jpgRequest := w11bMimePartRequest(t, "a.jpg", "image/jpg", []byte("jpg"))
	jpgRequest.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, jpgRequest); recorder.Code != http.StatusCreated {
		t.Fatalf("jpg = %d %s", recorder.Code, recorder.Body.String())
	}
	// pjpeg 归一化。
	pjpegRequest := w11bMimePartRequest(t, "b.jpg", "image/pjpeg", []byte("jpg2"))
	pjpegRequest.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, pjpegRequest); recorder.Code != http.StatusCreated {
		t.Fatalf("pjpeg = %d %s", recorder.Code, recorder.Body.String())
	}
	// 不支持的 MIME。
	txtRequest := w11bMimePartRequest(t, "c.txt", "text/plain", []byte("txt"))
	txtRequest.SetPathValue("conversationId", "chat_conv_w11b")
	if recorder := w11bInvokeUpload(rt, txtRequest); recorder.Code != http.StatusUnsupportedMediaType || w11bAssetCode(recorder) != "chat_asset_unsupported_type" {
		t.Fatalf("text = %d %s", recorder.Code, recorder.Body.String())
	}
	// webp/gif 直通。
	if recorder := w11bUploadPNG(t, rt, "d.webp", "image/webp", []byte("webp"), false); recorder.Code != http.StatusCreated {
		t.Fatalf("webp = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := w11bUploadPNG(t, rt, "e.gif", "image/gif", []byte("gif"), false); recorder.Code != http.StatusCreated {
		t.Fatalf("gif = %d %s", recorder.Code, recorder.Body.String())
	}
}

type w11bInconsistentProcessor struct{}

func (w11bInconsistentProcessor) ProcessUpload(data []byte, declaredMimeType string) (*ProcessedImage, error) {
	return &ProcessedImage{Buffer: data, OriginalMimeType: "image/png", MimeType: "image/webp", ByteSize: int64(len(data))}, nil
}

func (w11bInconsistentProcessor) CreatePreview(data []byte) (*ProcessedImage, error) {
	return &ProcessedImage{Buffer: data, OriginalMimeType: "image/png", MimeType: "image/webp"}, nil
}

type w11bFailingObjectStore struct {
	inner ObjectStore
}

func (s *w11bFailingObjectStore) Write(string, []byte, int64, string) error {
	return &DomainError{Message: "对象存储写入失败"}
}

func (s *w11bFailingObjectStore) Open(storageKey string, maxBytes int64) ([]byte, int64, error) {
	return s.inner.Open(storageKey, maxBytes)
}

func (s *w11bFailingObjectStore) Delete(keys []string) error { return s.inner.Delete(keys) }

func TestW11BUploadChatAssetProcessingFailures(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w11b_pf"
	env.fixture.createConversation(conversationID, routeTestOwner)

	// 实际格式与声明不一致。
	env.deps.ImageProcessor = w11bInconsistentProcessor{}
	rt := newChatRoutesForTest(env.deps)
	request := w11bMimePartRequest(t, "a.png", "image/webp", []byte("x"))
	request.SetPathValue("conversationId", "chat_conv_w11b_pf")
	if recorder := w11bInvokeUpload(rt, request); recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("mismatch = %d %s", recorder.Code, recorder.Body.String())
	}

	// 对象存储写入失败 → 处理失败收尾。
	env2 := newGenerationEnv(t)
	env2.fixture.createConversation("chat_conv_w11b_pf2", routeTestOwner)
	env2.deps.ObjectStore = &w11bFailingObjectStore{inner: env2.deps.ObjectStore}
	rt2 := newChatRoutesForTest(env2.deps)
	request2 := w11bMimePartRequest(t, "a.png", "image/png", []byte("png"))
	request2.SetPathValue("conversationId", "chat_conv_w11b_pf2")
	if recorder := w11bInvokeUpload(rt2, request2); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("store write fail = %d %s", recorder.Code, recorder.Body.String())
	}

	// 处理器缺失。
	env3 := newGenerationEnv(t)
	env3.fixture.createConversation("chat_conv_w11b_pf3", routeTestOwner)
	env3.deps.ImageProcessor = nil
	rt3 := newChatRoutesForTest(env3.deps)
	request3 := w11bMimePartRequest(t, "a.png", "image/png", []byte("png"))
	request3.SetPathValue("conversationId", "chat_conv_w11b_pf3")
	if recorder := w11bInvokeUpload(rt3, request3); recorder.Code != http.StatusBadRequest {
		t.Fatalf("no processor = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW11BAssetContentRouteVariants(t *testing.T) {
	rt, env, conversationID := w11bRoutesAndConversation(t)
	// 上传一个资产。
	recorder := w11bUploadPNG(t, rt, "a.png", "", []byte("w11b-png"), false)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", recorder.Code, recorder.Body.String())
	}
	var metadata struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}

	invoke := func(query string, headers map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", w11bPrefix+"/conversations/"+conversationID+"/assets/"+metadata.Data.ID+"/content"+query, nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("assetId", metadata.Data.ID)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		rt.assetContent(response, w11bAuthed(request))
		return response
	}

	// 枚举错误。
	if response := invoke("?variant=thumb", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("variant = %d", response.Code)
	}
	if response := invoke("?download=2", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("download = %d", response.Code)
	}
	// 未知 query key。
	if response := invoke("?other=1", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("strict query = %d", response.Code)
	}
	// 原图。
	response := invoke("", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/webp" || response.Body.String() != "w11b-png" {
		t.Fatalf("original = %d ct=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if response.Header().Get("Content-Disposition") != "inline" {
		t.Fatalf("disposition = %q", response.Header().Get("Content-Disposition"))
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("etag 缺失")
	}
	// ETag 命中 → 304。
	if response := invoke("", map[string]string{"If-None-Match": etag}); response.Code != http.StatusNotModified {
		t.Fatalf("etag = %d", response.Code)
	}
	if response := invoke("", map[string]string{"If-None-Match": "*"}); response.Code != http.StatusNotModified {
		t.Fatalf("star etag = %d", response.Code)
	}
	// W/ 前缀。
	if response := invoke("", map[string]string{"If-None-Match": "W/" + etag}); response.Code != http.StatusNotModified {
		t.Fatalf("weak etag = %d", response.Code)
	}
	// preview 缺失时回退原图。
	if response := invoke("?variant=preview", nil); response.Code != http.StatusOK {
		t.Fatalf("preview fallback = %d", response.Code)
	}
	// 不存在的资产。
	missingRequest := httptest.NewRequest("GET", w11bPrefix+"/conversations/"+conversationID+"/assets/chat_asset_none/content", nil)
	missingRequest.SetPathValue("conversationId", conversationID)
	missingRequest.SetPathValue("assetId", "chat_asset_none")
	missingResponse := httptest.NewRecorder()
	rt.assetContent(missingResponse, w11bAuthed(missingRequest))
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d", missingResponse.Code)
	}
	// 对象存储不可用。
	previousStore := env.deps.ObjectStore
	env.deps.ObjectStore = nil
	rtNoStore := newChatRoutesForTest(env.deps)
	request := httptest.NewRequest("GET", w11bPrefix+"/conversations/"+conversationID+"/assets/"+metadata.Data.ID+"/content", nil)
	request.SetPathValue("conversationId", conversationID)
	request.SetPathValue("assetId", metadata.Data.ID)
	noStoreResponse := httptest.NewRecorder()
	rtNoStore.assetContent(noStoreResponse, w11bAuthed(request))
	if noStoreResponse.Code != http.StatusInternalServerError {
		t.Fatalf("no store = %d", noStoreResponse.Code)
	}
	env.deps.ObjectStore = previousStore
}

func TestW11BAssetContentDownloadHeaders(t *testing.T) {
	rt, _, conversationID := w11bRoutesAndConversation(t)
	// 造 assistant_generated 资产：直接写库 + 对象存储。
	asset, err := rt.deps.Store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: routeTestOwner, ConversationID: conversationID,
		SourceKind: "assistant_generated", OriginalFilename: "gen.png",
		OriginalMimeType: "image/png", OriginalSha256: strings.Repeat("a", 64), OriginalBytes: 8, QuotaBytes: 8, Now: rt.now(), RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	storageKey := "generated/w11b/" + asset.ID + ".png"
	if err := rt.deps.ObjectStore.Write(storageKey, []byte("png-bytes"), chatAssetGeneratedMaxBytes, ""); err != nil {
		t.Fatal(err)
	}
	completed, err := rt.deps.Store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: asset.ID, SystemAccountID: routeTestOwner, ConversationID: conversationID,
		ProcessedMimeType: "image/png", ProcessedWidth: 1, ProcessedHeight: 1, ProcessedBytes: 9,
		ProcessedSha256: "sha-w11b", StorageKey: storageKey, Now: rt.now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", w11bPrefix+"/conversations/"+conversationID+"/assets/"+completed.ID+"/content?download=1", nil)
	request.SetPathValue("conversationId", conversationID)
	request.SetPathValue("assetId", completed.ID)
	response := httptest.NewRecorder()
	rt.assetContent(response, w11bAuthed(request))
	if response.Code != http.StatusOK {
		t.Fatalf("download = %d %s", response.Code, response.Body.String())
	}
	disposition := response.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, ".png") {
		t.Fatalf("disposition = %q", disposition)
	}
}

func TestW11BDeleteAssetRoute(t *testing.T) {
	rt, _, conversationID := w11bRoutesAndConversation(t)
	recorder := w11bUploadPNG(t, rt, "del.png", "", []byte("del"), false)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", recorder.Code, recorder.Body.String())
	}
	var metadata struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	invoke := func(assetID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("DELETE", w11bPrefix+"/conversations/"+conversationID+"/assets/"+assetID, nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("assetId", assetID)
		response := httptest.NewRecorder()
		rt.deleteAsset(response, w11bAuthed(request))
		return response
	}
	// 成功删除。
	if response := invoke(metadata.Data.ID); response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", response.Code, response.Body.String())
	}
	// 再次删除 → 冲突。
	if response := invoke(metadata.Data.ID); response.Code != http.StatusConflict {
		t.Fatalf("redelete = %d %s", response.Code, response.Body.String())
	}
	// 会话不存在。
	request := httptest.NewRequest("DELETE", w11bPrefix+"/conversations/chat_conv_none/assets/x", nil)
	request.SetPathValue("conversationId", "chat_conv_none")
	request.SetPathValue("assetId", "x")
	response := httptest.NewRecorder()
	rt.deleteAsset(response, w11bAuthed(request))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing conv = %d", response.Code)
	}
}

func TestW11BReleaseRetryAtAndFilenameNormalization(t *testing.T) {
	rt, _, _ := w11bRoutesAndConversation(t)
	if value := rt.releaseRetryAt(); value == "" || !strings.Contains(value, "T") {
		t.Fatalf("releaseRetryAt = %q", value)
	}
	// 文件名规范化：路径段取末段、控制字符清除、超长截断。
	name, err := normalizedUploadFilename(`C:\fake\dir\ph oto.png`)
	if err != nil || name != "ph oto.png" {
		t.Fatalf("name = %q err = %v", name, err)
	}
	name, err = normalizedUploadFilename(strings.Repeat("长", 300))
	if err != nil || len([]rune(name)) != 255 {
		t.Fatalf("truncated = %d", len([]rune(name)))
	}
	if _, err := normalizedUploadFilename("///"); err == nil {
		t.Fatal("全分隔符应报错")
	}
	// MIME 归一化。
	if value, err := normalizedDeclaredMimeType("IMAGE/JPG; charset=utf-8"); err != nil || value != "image/jpeg" {
		t.Fatalf("mime = %q err = %v", value, err)
	}
	if value, err := normalizedDeclaredMimeType(" image/png "); err != nil || value != "image/png" {
		t.Fatalf("mime trim = %q err = %v", value, err)
	}
	if _, err := normalizedDeclaredMimeType("image/bmp"); err == nil {
		t.Fatal("bmp 不支持")
	}
	if nilIfZero(0) != nil || nilIfZero(5) == nil || *nilIfZero(5) != 5 {
		t.Fatal("nilIfZero 语义")
	}
	// requestEtagMatches：空/不匹配/多值。
	if requestEtagMatches(nil, `"x"`) || requestEtagMatches([]string{`"y"`}, `"x"`) {
		t.Fatal("etag 不匹配")
	}
	if !requestEtagMatches([]string{`"y", "x"`}, `"x"`) {
		t.Fatal("多值命中")
	}
}
