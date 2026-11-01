package openaicompatstorage

// DD2-C4 钉住测试：multipart purpose 字段读取必须带 busboy 默认 fieldSize
// （1 MiB）上限——超限报 400 field_too_large（形态与既有字段校验一致），
// 恰好 1 MiB 不触发上限。纯单元：readMultipartUpload 在 Store 访问前即返回，
// Deps 只需 Config。

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func dd2C4UploadRequest(t *testing.T, purposeBytes int) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	field, err := writer.CreateFormField("purpose")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := field.Write(bytes.Repeat([]byte("a"), purposeBytes)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/files", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestDD2C4PurposeFieldOverLimitReturns400(t *testing.T) {
	deps := &Deps{Config: Config{MaxFileBytes: DefaultMaxFileBytes}}
	_, _, err := deps.readMultipartUpload(dd2C4UploadRequest(t, int(multipartFieldMaxBytes)+1))
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.StatusCode != http.StatusBadRequest || requestErr.Code != "field_too_large" {
		t.Fatalf("超限字段必须 400 field_too_large，got %v", err)
	}
}

func TestDD2C4PurposeFieldAtLimitPassesCap(t *testing.T) {
	deps := &Deps{Config: Config{MaxFileBytes: DefaultMaxFileBytes}}
	// 恰好 1 MiB：不触发字段上限，走缺 file 的 400 missing_file。
	_, _, err := deps.readMultipartUpload(dd2C4UploadRequest(t, int(multipartFieldMaxBytes)))
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != "missing_file" {
		t.Fatalf("恰好 1MiB 不得触发字段上限（应缺 file 400），got %v", err)
	}
}
