package publicapilog

import (
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestBuildPublicAPILogInput(t *testing.T) {
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(123 * time.Millisecond)
	input := BuildPublicAPILogInput(BuildInput{
		ID:         "publog_1",
		TraceID:    "trace_1",
		Source:     &SourceContext{SourceRefID: "source", SourceName: "Source", TokenID: "token", TokenName: "Token", TokenPrefix: "juis_abc", IsTestToken: true},
		Method:     "get",
		Path:       "/__aipublic__/group/list",
		StatusCode: 200,
		DurationMs: 123,
		RequestSnapshot: Snapshot{
			Data:      map[string]any{"body": map[string]any{}},
			Status:    port.PublicAPILogCaptureEmpty,
			SizeBytes: 0,
		},
		ResponseSnapshot: Snapshot{
			Data:      map[string]any{"body": map[string]any{"ok": true}},
			Status:    port.PublicAPILogCaptureComplete,
			SizeBytes: 12,
		},
		StartedAt: startedAt,
		EndedAt:   endedAt,
	})

	if input.Method != "GET" || input.Path != "/__aipublic__/group/list" {
		t.Fatalf("method/path = %s %s", input.Method, input.Path)
	}
	if input.StatusCode == nil || *input.StatusCode != 200 || !input.Success {
		t.Fatalf("status/success = %v/%v", input.StatusCode, input.Success)
	}
	if input.DurationMs == nil || *input.DurationMs != 123 {
		t.Fatalf("duration = %v", input.DurationMs)
	}
	if input.SourceRefID != "source" || input.TokenPrefix != "juis_abc" || !input.IsTestToken {
		t.Fatalf("source fields = %+v", input)
	}
	if !input.CreatedAt.Equal(endedAt) {
		t.Fatalf("created at = %v, want %v", input.CreatedAt, endedAt)
	}
}

func TestBuildPublicAPILogInputClosed(t *testing.T) {
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	input := BuildPublicAPILogInput(BuildInput{
		Method:     "GET",
		Path:       "/__aipublic__/slow-close",
		StatusCode: 200,
		DurationMs: 50,
		RequestSnapshot: Snapshot{
			Data:   map[string]any{},
			Status: port.PublicAPILogCaptureEmpty,
		},
		ResponseSnapshot: Snapshot{
			Data:   map[string]any{},
			Status: port.PublicAPILogCaptureEmpty,
		},
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Second),
		Closed:    true,
	})

	if input.StatusCode == nil || *input.StatusCode != 499 {
		t.Fatalf("status = %v, want 499", input.StatusCode)
	}
	if input.Success {
		t.Fatal("closed log should not be success")
	}
	if input.ErrorCode != "public_api_client_closed" || input.ErrorMessage != "客户端连接提前关闭" {
		t.Fatalf("error = %s / %s", input.ErrorCode, input.ErrorMessage)
	}
}

func TestErrorInfoFromResponse(t *testing.T) {
	code, message := ErrorInfoFromResponse(map[string]any{
		"message": "来源系统没有调用该接口的权限",
		"code":    "external_source_scope_forbidden",
	}, 403)
	if code != "external_source_scope_forbidden" || message != "来源系统没有调用该接口的权限" {
		t.Fatalf("error info = %q / %q", code, message)
	}

	code, message = ErrorInfoFromResponse("plain error", 500)
	if code != "" || message != "plain error" {
		t.Fatalf("string error info = %q / %q", code, message)
	}

	code, message = ErrorInfoFromResponse(nil, 502)
	if code != "" || message != "服务器内部错误" {
		t.Fatalf("fallback error info = %q / %q", code, message)
	}
}
