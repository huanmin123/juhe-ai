package openaicompatstorage

// 基础设施小文件的补充覆盖测试（storage 域半边，自 cov_infra_test.go 拆出）：
// 路由错误契约、文本索引分支。

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCovRoutesAndErrorsContract(t *testing.T) {
	t.Run("RequestError 文本与 401", func(t *testing.T) {
		err := badRequest("参数坏", "bad_param")
		if err.Error() != "参数坏" {
			t.Errorf("Error() = %q", err.Error())
		}
		recorder := httptest.NewRecorder()
		unauthorized := badRequest("缺少或无效的 API Key", "invalid_api_key")
		unauthorized.StatusCode = 401
		unauthorized.Write(recorder)
		if recorder.Code != 401 || !strings.Contains(recorder.Body.String(), "invalid_api_key") {
			t.Errorf("401 渲染 = %d %s", recorder.Code, recorder.Body.String())
		}
		// code 为空时 JSON omitempty 生效。
		noCode := httptest.NewRecorder()
		writeGatewayErrorPayload(noCode, 400, "消息", "invalid_request_error", "")
		if strings.Contains(noCode.Body.String(), `"code"`) {
			t.Errorf("空 code 不应输出：%s", noCode.Body.String())
		}
	})
	t.Run("unhandled 500 契约", func(t *testing.T) {
		if errUnhandled.Error() != "服务器内部错误" {
			t.Errorf("errUnhandled.Error() = %q", errUnhandled.Error())
		}
		recorder := httptest.NewRecorder()
		writeUnhandledError(recorder)
		if recorder.Code != 500 || recorder.Body.String() != `{"message":"服务器内部错误"}` {
			t.Errorf("500 渲染 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("handle 分派", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handle(recorder, func() error { return notFound("找不到", "missing") })
		if recorder.Code != 404 {
			t.Errorf("RequestError 应按状态渲染：%d", recorder.Code)
		}
		recorder2 := httptest.NewRecorder()
		handle(recorder2, func() error { return newIndexingError("索引失败", 422, "invalid_request_error", "index_failed") })
		if recorder2.Code != 422 {
			t.Errorf("IndexingError 应按状态渲染：%d", recorder2.Code)
		}
		recorder3 := httptest.NewRecorder()
		handle(recorder3, func() error { return errors.New("未知") })
		if recorder3.Code != 500 {
			t.Errorf("未知错误应 500：%d", recorder3.Code)
		}
		recorder4 := httptest.NewRecorder()
		handle(recorder4, func() error { return nil })
		if recorder4.Code != 200 {
			t.Errorf("无错误不应写响应：%d", recorder4.Code)
		}
	})
	t.Run("requireScope 401", func(t *testing.T) {
		deps := &Deps{}
		recorder := httptest.NewRecorder()
		if scope := deps.requireScope(recorder, httptest.NewRequest("GET", "/x", nil)); scope != nil {
			t.Fatal("无 Scope 解析器应拒绝")
		}
		if recorder.Code != 401 {
			t.Errorf("401 契约 = %d", recorder.Code)
		}
	})
	t.Run("isMultipartContentType 边界", func(t *testing.T) {
		cases := map[string]bool{
			"multipart/form-data":             true,
			"multipart/form-data; boundary=x": true,
			"multipart/form-datanope":         false,
			"multipart/form-data2":            false,
			"MULTIPART/FORM-DATA; BOUNDARY=y": true,
			"application/json":                false,
			"multipart/form-data_boundary":    false,
		}
		for contentType, want := range cases {
			if got := isMultipartContentType(contentType); got != want {
				t.Errorf("isMultipart(%q) = %v，期望 %v", contentType, got, want)
			}
		}
	})
}

func TestCovTextIndexerBranches(t *testing.T) {
	t.Run("IndexingError 契约", func(t *testing.T) {
		err := newIndexingError("索引失败", 422, "invalid_request_error", "index_failed")
		if err.Error() != "索引失败" {
			t.Errorf("Error() = %q", err.Error())
		}
		recorder := httptest.NewRecorder()
		err.write(recorder)
		if recorder.Code != 422 {
			t.Errorf("write 状态 = %d", recorder.Code)
		}
	})
	t.Run("媒体类型白名单", func(t *testing.T) {
		for mediaType, want := range map[string]bool{
			"": false, "text/plain": true, "application/json": true,
			"application/typescript": true, "application/x-sh": true, "image/png": false,
		} {
			if got := IsSupportedVectorStoreTextMediaType(mediaType); got != want {
				t.Errorf("IsSupported(%q) = %v，期望 %v", mediaType, got, want)
			}
		}
	})
	t.Run("BuildVectorStoreChunks 错误分支", func(t *testing.T) {
		root := t.TempDir()
		media := "image/png"
		if _, err := BuildVectorStoreChunks(root, FileRecord{ID: "f1", MediaType: &media}); err == nil {
			t.Fatal("不支持媒体类型应报错")
		} else if indexingErr, ok := err.(*IndexingError); !ok || indexingErr.Code != "openai_compatible_file_mime_unsupported" {
			t.Fatalf("err = %v", err)
		}
		text := "text/plain"
		// 空文件（内容全空白）。
		if _, err := BuildVectorStoreChunks(root, FileRecord{ID: "f2", MediaType: &text, StorageKey: "files/x/empty", Bytes: 3}); err == nil {
			t.Fatal("缺文件应报错")
		}
	})
	t.Run("超大文本分块过多", func(t *testing.T) {
		// >256 个 2400 字符窗口：约 2000 步进 x 300 = 600K 字符。
		huge := strings.Repeat("字", 300*2000+2400)
		chunks := chunkTextForVectorStore(huge)
		if len(chunks) <= 256 {
			t.Fatalf("构造的块数应超过 256：%d", len(chunks))
		}
	})
	t.Run("ReadFileTextForIndexing 边界", func(t *testing.T) {
		root := t.TempDir()
		media := "text/plain"
		if _, err := ReadFileTextForIndexing(root, FileRecord{ID: "big", Bytes: MaxVectorStoreTextIndexBytes + 1, StorageKey: "files/x/big"}); err == nil {
			t.Fatal("超限应报错")
		}
		escaped := FileRecord{ID: "esc", StorageKey: "../esc", MediaType: &media}
		if _, err := ReadFileTextForIndexing(root, escaped); err != StorageKeyEscapeError {
			t.Fatalf("逃逸 key 应报 StorageKeyEscapeError：%v", err)
		}
		missing := FileRecord{ID: "missing", StorageKey: "files/x/gone", MediaType: &media}
		if _, err := ReadFileTextForIndexing(root, missing); err == nil {
			t.Fatal("缺文件应报错")
		}
	})
	t.Run("NUL 清洗", func(t *testing.T) {
		if got := removeNULCharacters("a\x00b"); got != "ab" {
			t.Errorf("NUL 移除 = %q", got)
		}
		if got := removeNULCharacters("abc"); got != "abc" {
			t.Errorf("无 NUL 原样 = %q", got)
		}
	})
}
