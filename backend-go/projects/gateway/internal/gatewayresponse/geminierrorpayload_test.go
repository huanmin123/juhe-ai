package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func decodeJSONObject(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return value
}

// BUG-0174 M-6：Gemini 错误体解析对齐 gemini-v1beta/error-payload.ts +
// _shared/error-payload.ts——error 信封缺失回退根对象、message 别名集、
// 数值 code/status 转字符串。
func TestParseGeminiErrorPayloadMatrix(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		want        gatewayproto.ErrorPayload
	}{
		{
			name:        "standard envelope",
			body:        `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "429", Type: "RESOURCE_EXHAUSTED", Message: "quota exceeded"},
		},
		{
			name:        "string code envelope",
			body:        `{"error":{"code":"INVALID_ARGUMENT","message":"bad","status":"INVALID_ARGUMENT"}}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "INVALID_ARGUMENT", Type: "INVALID_ARGUMENT", Message: "bad"},
		},
		{
			name:        "flat body falls back to root object",
			body:        `{"code":404,"message":"not found","status":"NOT_FOUND"}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "404", Type: "NOT_FOUND", Message: "not found"},
		},
		{
			name:        "flat numeric code only",
			body:        `{"code":429}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "429"},
		},
		{
			name:        "code falls back to status when missing",
			body:        `{"error":{"status":"UNAVAILABLE"}}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "UNAVAILABLE", Type: "UNAVAILABLE"},
		},
		{
			name:        "message alias msg on root",
			body:        `{"error":{"code":1},"msg":"alias hit"}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "1", Message: "alias hit"},
		},
		{
			name:        "message alias error_message and error_description",
			body:        `{"error":{"code":2},"error_message":"","error_description":"description hit"}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "2", Message: "description hit"},
		},
		{
			name:        "message alias detail",
			body:        `{"error":{"code":3},"detail":"detail hit"}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "3", Message: "detail hit"},
		},
		{
			name:        "error object fields take priority over root",
			body:        `{"error":{"message":"envelope","status":"A"},"message":"root","status":"B"}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "A", Type: "A", Message: "envelope"},
		},
		{
			name:        "nested message object recurses into message field",
			body:        `{"error":{"code":9,"message":{"reason":"nested hit"}}}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "9", Message: "nested hit"},
		},
		{
			name:        "boolean and float code stringify",
			body:        `{"error":{"code":true},"status":503.0}`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{Code: "true", Type: "503"},
		},
		{
			name:        "text body without json content type ignored",
			body:        `upstream exploded`,
			contentType: "text/plain",
			want:        gatewayproto.ErrorPayload{},
		},
		{
			name:        "invalid json ignored",
			body:        `{"error":`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{},
		},
		{
			name:        "empty body ignored",
			body:        "   ",
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{},
		},
		{
			name:        "non object json ignored",
			body:        `[1,2,3]`,
			contentType: "application/json",
			want:        gatewayproto.ErrorPayload{},
		},
		{
			name:        "plain text body with brace prefix still parsed",
			body:        `{"message":"brace sniffed"}`,
			contentType: "text/plain",
			want:        gatewayproto.ErrorPayload{Message: "brace sniffed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.contentType != "" {
				header.Set("Content-Type", tc.contentType)
			}
			got := parseGeminiErrorPayload(tc.body, header)
			if got != tc.want {
				t.Fatalf("parseGeminiErrorPayload(%q) = %#v, want %#v", tc.body, got, tc.want)
			}
		})
	}
}

func TestParseGeminiErrorPayloadFromValueMatrix(t *testing.T) {
	cases := []struct {
		name string
		body string
		want gatewayproto.ErrorPayload
	}{
		{
			name: "envelope numeric code",
			body: `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`,
			want: gatewayproto.ErrorPayload{Code: "429", Type: "RESOURCE_EXHAUSTED", Message: "quota"},
		},
		{
			name: "flat root fallback",
			body: `{"code":400,"status":"INVALID_ARGUMENT","msg":"flat"}`,
			want: gatewayproto.ErrorPayload{Code: "400", Type: "INVALID_ARGUMENT", Message: "flat"},
		},
		{
			name: "missing fields yields empty payload",
			body: `{"error":{"other":1}}`,
			want: gatewayproto.ErrorPayload{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGeminiErrorPayloadFromValue(decodeJSONObject(t, tc.body))
			if got != tc.want {
				t.Fatalf("parseGeminiErrorPayloadFromValue(%s) = %#v, want %#v", tc.body, got, tc.want)
			}
		})
	}
}
