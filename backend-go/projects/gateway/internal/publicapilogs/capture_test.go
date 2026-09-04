package publicapilogs

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixedTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("fixed time: %v", err)
	}
	return parsed
}

// TestBuildInputParity mirrors the Node middleware assertions in
// scripts/regression/public-api-logs-regression.ts field by field.
func TestBuildInputParity(t *testing.T) {
	started := fixedTime(t, "2026-09-01T10:00:00Z")
	ended := fixedTime(t, "2026-09-01T10:00:00.123Z")

	tests := []struct {
		name string
		spec CaptureSpec
		want Input
	}{
		{
			name: "success request keeps raw query and source snapshot",
			spec: CaptureSpec{
				Method:        "post",
				BaseURL:       "",
				Path:          "/__aipublic__/group/list",
				OriginalURL:   "/__aipublic__/group/list?targetUsername=huanmin&keyword=secret",
				Query:         map[string]any{"targetUsername": "huanmin", "keyword": "secret"},
				Body:          Undefined,
				ContentType:   "",
				ContentLength: "",
				UserAgent:     "ua-1",
				StatusCode:    200,
				StartedAt:     started,
				EndedAt:       ended,
				DurationMS:    123,
				TraceID:       "trace-success",
				ClientIP:      "203.0.113.88",
				Source: &SourceContext{
					SourceRefID: "ref-1", SourceName: "内置测试来源", TokenID: "tok-1",
					TokenName: "Token", TokenPrefix: "sk-pre", IsTestToken: true,
				},
			},
			want: Input{
				TraceID: "trace-success", SourceRefID: "ref-1", SourceName: "内置测试来源",
				TokenID: "tok-1", TokenName: "Token", TokenPrefix: "sk-pre", IsTestToken: true,
				Method: "POST", Path: "/__aipublic__/group/list",
				QueryString: "targetUsername=huanmin&keyword=secret",
				ClientIP:    "203.0.113.88", UserAgent: "ua-1",
				StatusCode: 200, Success: true, DurationMS: int64(123),
				RequestSizeBytes: 0, ResponseSizeBytes: 0,
				RequestCaptureStatus:  CaptureStatusComplete,
				ResponseCaptureStatus: CaptureStatusEmpty,
				ErrorCode:             "",
				StartedAt:             "2026-09-01T10:00:00.000Z",
				EndedAt:               "2026-09-01T10:00:00.123Z",
				CreatedAt:             "2026-09-01T10:00:00.123Z",
			},
		},
		{
			name: "client closed maps to 499 with fixed error code",
			spec: CaptureSpec{
				Method: "POST", Path: "/__aipublic__/slow",
				OriginalURL: "/__aipublic__/slow",
				StatusCode:  200,
				Closed:      true,
				StartedAt:   started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/__aipublic__/slow",
				StatusCode: 499, Success: false,
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusEmpty,
				ErrorCode:             "public_api_client_closed",
				ErrorMessage:          "客户端连接提前关闭",
				StartedAt:             started.Format("2006-01-02T15:04:05.000Z07:00"),
				EndedAt:               "2026-09-01T10:00:00.123Z",
				CreatedAt:             "2026-09-01T10:00:00.123Z",
			},
		},
		{
			name: "nested error payload extracts code and message",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				StatusCode: 400,
				ResponsePayload: map[string]any{
					"error": map[string]any{
						"code":    "invalid_request_error",
						"type":    "invalid_request_error",
						"message": "参数错误",
					},
				},
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 400, Success: false,
				ErrorCode: "invalid_request_error", ErrorMessage: "参数错误",
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusComplete,
			},
		},
		{
			name: "top level code and message win over nested",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				StatusCode: 400,
				ResponsePayload: map[string]any{
					"code":    "quota_exhausted",
					"message": "配额不足",
					"error":   map[string]any{"code": "nested", "message": "nested"},
				},
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 400, Success: false,
				ErrorCode: "quota_exhausted", ErrorMessage: "配额不足",
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusComplete,
			},
		},
		{
			name: "string error payload is trimmed-kept raw up to 1000 chars",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				StatusCode:      500,
				ResponsePayload: strings.Repeat("x", 1200),
				StartedAt:       started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 500, Success: false,
				ErrorMessage:          strings.Repeat("x", 1000),
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusComplete,
			},
		},
		{
			name: "non-string payload at 5xx falls back to the fixed message",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				StatusCode: 502, ResponsePayload: []any{"nope"},
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 502, Success: false,
				ErrorMessage:          "服务器内部错误",
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusComplete,
			},
		},
		{
			name: "non-string payload at 4xx falls back to the http message",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				StatusCode: 404, ResponsePayload: []any{"nope"},
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 404, Success: false,
				ErrorMessage:          "请求失败：HTTP 404",
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusComplete,
			},
		},
		{
			name: "rejected body marks dropped with reason",
			spec: CaptureSpec{
				Method: "POST", Path: "/account/add", OriginalURL: "/account/add",
				Body:         Undefined,
				StatusCode:   400,
				BodyRejected: &BodyRejection{StatusCode: 400},
				StartedAt:    started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/account/add", StatusCode: 400, Success: false,
				RequestCaptureStatus:  CaptureStatusDropped,
				RequestSizeBytes:      0,
				ResponseCaptureStatus: CaptureStatusEmpty,
				ErrorMessage:          "请求失败：HTTP 400",
			},
		},
		{
			name: "inferred parse failure on 400 with body absent",
			spec: CaptureSpec{
				Method: "POST", Path: "/x", OriginalURL: "/x",
				Body: nil, StatusCode: 400, ContentLength: "17",
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "POST", Path: "/x", StatusCode: 400, Success: false,
				RequestCaptureStatus:  CaptureStatusDropped,
				ResponseCaptureStatus: CaptureStatusEmpty,
				ErrorMessage:          "请求失败：HTTP 400",
			},
		},
		{
			name: "inferred too large on 413",
			spec: CaptureSpec{
				Method: "PUT", Path: "/x", OriginalURL: "/x",
				Body: nil, StatusCode: 413, ContentLength: "999999",
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "PUT", Path: "/x", StatusCode: 413, Success: false,
				RequestCaptureStatus:  CaptureStatusDropped,
				ResponseCaptureStatus: CaptureStatusEmpty,
				ErrorMessage:          "请求失败：HTTP 413",
			},
		},
		{
			name: "inference skips non-body methods and 2xx",
			spec: CaptureSpec{
				Method: "GET", Path: "/x", OriginalURL: "/x",
				Body: nil, StatusCode: 200, ContentLength: "5",
				StartedAt: started, EndedAt: ended,
			},
			want: Input{
				Method: "GET", Path: "/x", StatusCode: 200, Success: true,
				RequestCaptureStatus:  CaptureStatusEmpty,
				ResponseCaptureStatus: CaptureStatusEmpty,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildInput(tt.spec)
			if got.TraceID != tt.want.TraceID || got.SourceRefID != tt.want.SourceRefID ||
				got.SourceName != tt.want.SourceName || got.TokenID != tt.want.TokenID ||
				got.TokenName != tt.want.TokenName || got.TokenPrefix != tt.want.TokenPrefix ||
				got.IsTestToken != tt.want.IsTestToken {
				t.Fatalf("source fields: %+v", got)
			}
			if got.Method != tt.want.Method || got.Path != tt.want.Path || got.QueryString != tt.want.QueryString {
				t.Fatalf("routing fields: %+v", got)
			}
			if got.ClientIP != tt.want.ClientIP || got.UserAgent != tt.want.UserAgent {
				t.Fatalf("client fields: %+v", got)
			}
			if got.StatusCode != tt.want.StatusCode || got.Success != tt.want.Success {
				t.Fatalf("status fields: %+v", got)
			}
			if tt.want.DurationMS != nil && got.DurationMS != tt.want.DurationMS {
				t.Fatalf("duration: %v want %v", got.DurationMS, tt.want.DurationMS)
			}
			if got.RequestCaptureStatus != tt.want.RequestCaptureStatus || got.ResponseCaptureStatus != tt.want.ResponseCaptureStatus {
				t.Fatalf("capture status: %q/%q", got.RequestCaptureStatus, got.ResponseCaptureStatus)
			}
			if got.ErrorCode != tt.want.ErrorCode || got.ErrorMessage != tt.want.ErrorMessage {
				t.Fatalf("error fields: %q/%q want %q/%q", got.ErrorCode, got.ErrorMessage, tt.want.ErrorCode, tt.want.ErrorMessage)
			}
			if got.StartedAt != tt.want.StartedAt && tt.want.StartedAt != "" {
				t.Fatalf("startedAt: %s want %s", got.StartedAt, tt.want.StartedAt)
			}
			if tt.want.EndedAt != "" && (got.EndedAt != tt.want.EndedAt || got.CreatedAt != tt.want.CreatedAt) {
				t.Fatalf("time fields: %s/%s", got.EndedAt, got.CreatedAt)
			}
		})
	}
}

// TestBuildInputSnapshotJSON pins the exact stored snapshot bytes for the
// deterministic shapes.
func TestBuildInputSnapshotJSON(t *testing.T) {
	started := fixedTime(t, "2026-09-01T10:00:00Z")

	spec := CaptureSpec{
		Method:        "POST",
		Path:          "/api-key/add",
		OriginalURL:   "/api-key/add?verbose=1",
		Query:         map[string]any{"verbose": "1"},
		Body:          map[string]any{"name": "key", "apiKey": "sk-secret"},
		ContentType:   "application/json",
		ContentLength: "28",
		UserAgent:     "ua",
		StatusCode:    201,
		ResponsePayload: map[string]any{
			"apiKey": map[string]any{"key": "juis_mock_public_api_key"},
		},
		StartedAt: started, EndedAt: started.Add(10 * time.Millisecond), DurationMS: 10,
	}
	input := BuildInput(spec)

	requestJSON, err := marshalCompact(input.RequestData)
	if err != nil {
		t.Fatal(err)
	}
	wantRequest := `{"method":"POST","path":"/api-key/add","query":{"verbose":"1"},` +
		`"body":{"apiKey":"sk-secret","name":"key"},"headers":{"contentType":"application/json","contentLength":"28"}}`
	if requestJSON != wantRequest {
		t.Fatalf("requestData:\n got %s\nwant %s", requestJSON, wantRequest)
	}
	if !strings.Contains(requestJSON, "sk-secret") {
		t.Fatal("捕获必须保存请求原文，不做字段名脱敏")
	}
	if strings.Contains(requestJSON, "[redacted]") {
		t.Fatal("普通请求日志不得写入脱敏占位")
	}

	responseJSON, err := marshalCompact(input.ResponseData)
	if err != nil {
		t.Fatal(err)
	}
	wantResponse := `{"statusCode":201,"body":{"apiKey":{"key":"juis_mock_public_api_key"}}}`
	if responseJSON != wantResponse {
		t.Fatalf("responseData:\n got %s\nwant %s", responseJSON, wantResponse)
	}
	if input.RequestCaptureStatus != CaptureStatusComplete || input.ResponseCaptureStatus != CaptureStatusComplete {
		t.Fatalf("capture status: %q/%q", input.RequestCaptureStatus, input.ResponseCaptureStatus)
	}
	// sizeBytes: request = content-length + query text; response starts at 0.
	if input.RequestSizeBytes != int64(len("verbose=1")+28) {
		t.Fatalf("requestSizeBytes: %d", input.RequestSizeBytes)
	}
}

// TestBuildInputEmptySnapshots mirrors the isSnapshotEmpty truth table.
func TestBuildInputEmptySnapshots(t *testing.T) {
	tests := []struct {
		name           string
		spec           CaptureSpec
		requestStatus  CaptureStatus
		responseStatus CaptureStatus
	}{
		{"bare GET is empty on both sides", CaptureSpec{Method: "GET", OriginalURL: "/x", StatusCode: 200},
			CaptureStatusEmpty, CaptureStatusEmpty},
		{"query only keeps request complete", CaptureSpec{Method: "GET", OriginalURL: "/x?a=1", Query: map[string]any{"a": "1"}, StatusCode: 200},
			CaptureStatusComplete, CaptureStatusEmpty},
		{"explicit empty body object is complete", CaptureSpec{Method: "POST", OriginalURL: "/x", Body: map[string]any{}, StatusCode: 200},
			CaptureStatusComplete, CaptureStatusEmpty},
		{"null response body is empty", CaptureSpec{Method: "GET", OriginalURL: "/x", StatusCode: 200, ResponsePayload: nil},
			CaptureStatusEmpty, CaptureStatusEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := BuildInput(tt.spec)
			if input.RequestCaptureStatus != tt.requestStatus || input.ResponseCaptureStatus != tt.responseStatus {
				t.Fatalf("status: %q/%q want %q/%q", input.RequestCaptureStatus, input.ResponseCaptureStatus, tt.requestStatus, tt.responseStatus)
			}
		})
	}
}

// TestBoundedSnapshotTruncation drives the 32 KiB budget: oversized payloads
// keep a bounded preview plus the size fallback, mirroring Node
// (sanitizedSizeBytes || max(jsonSize, maxBytes+1) — the response snapshot
// carries raw size 0, so the floor applies there; the request snapshot keeps
// the true raw size from content-length + query bytes).
func TestBoundedSnapshotTruncation(t *testing.T) {
	big := strings.Repeat("中", 12000) // 36000 UTF-8 bytes, above the 32 KiB budget
	spec := CaptureSpec{
		Method: "POST", OriginalURL: "/x", Body: Undefined, StatusCode: 200,
		ResponsePayload: big,
	}
	input := BuildInput(spec)
	if input.ResponseCaptureStatus != CaptureStatusTruncated {
		t.Fatalf("response status: %q", input.ResponseCaptureStatus)
	}
	if input.ResponseSizeBytes < publicAPISnapshotMaxBytes {
		t.Fatalf("responseSizeBytes must fall back to max(jsonSize, budget+1), got %d", input.ResponseSizeBytes)
	}
	responseJSON, err := marshalCompact(input.ResponseData)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(responseJSON), &parsed); err != nil {
		t.Fatalf("truncated response data is not an object: %v", err)
	}
	if parsed["truncated"] != true {
		t.Fatalf("truncated marker missing: %s", responseJSON)
	}
	originalSize, ok := parsed["originalJsonSizeBytes"].(float64)
	if !ok || int64(originalSize) != input.ResponseSizeBytes {
		t.Fatalf("originalJsonSizeBytes %v vs size %d", parsed["originalJsonSizeBytes"], input.ResponseSizeBytes)
	}
	preview, ok := parsed["preview"].(string)
	if !ok || len(preview) > publicAPISnapshotMaxBytes {
		t.Fatalf("preview exceeds budget: %d", len(preview))
	}

	// A truncated request snapshot keeps the true original raw size.
	wideBody := map[string]any{"blob": strings.Repeat("x", 60000)}
	spec = CaptureSpec{
		Method: "POST", OriginalURL: "/x?a=1", Query: map[string]any{"a": "1"},
		Body: wideBody, StatusCode: 200, ContentLength: "60006",
	}
	input = BuildInput(spec)
	if input.RequestCaptureStatus != CaptureStatusTruncated {
		t.Fatalf("request status: %q", input.RequestCaptureStatus)
	}
	if input.RequestSizeBytes != 60009 { // content-length 60006 + query "a=1" (3 bytes)
		t.Fatalf("truncated request must keep the original raw size, got %d", input.RequestSizeBytes)
	}
}

// TestBoundedSnapshotDepthAndEntries exercises the depth/entry caps.
func TestBoundedSnapshotDepthAndEntries(t *testing.T) {
	deep := map[string]any{}
	current := deep
	for i := 0; i < 20; i++ {
		next := map[string]any{}
		current["n"] = next
		current = next
	}
	spec := CaptureSpec{Method: "POST", OriginalURL: "/x", Body: deep, StatusCode: 200}
	input := BuildInput(spec)
	requestJSON, err := marshalCompact(input.RequestData)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestJSON, "[truncated]") {
		t.Fatalf("depth cap should insert the truncated marker: %s", requestJSON)
	}

	// 300 short-key entries stay under the byte budget, so the per-object
	// entry cap (not the byte budget) triggers __truncated. Node then marks
	// the whole snapshot truncated (bounded.truncated) even though the JSON
	// fits.
	wide := map[string]any{}
	for i := 0; i < 300; i++ {
		wide["k"+strconv.Itoa(i)] = i
	}
	spec = CaptureSpec{Method: "POST", OriginalURL: "/x", Body: wide, StatusCode: 200}
	input = BuildInput(spec)
	requestJSON, err = marshalCompact(input.RequestData)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestJSON, "__truncated") {
		t.Fatalf("entry cap should set __truncated: %s", requestJSON)
	}
	if input.RequestCaptureStatus != CaptureStatusTruncated {
		t.Fatalf("entry cap must mark the snapshot truncated: %q", input.RequestCaptureStatus)
	}
}

// TestCaptureExactlyOnce mirrors the Node middleware recorded flag.
func TestCaptureExactlyOnce(t *testing.T) {
	spec := CaptureSpec{Method: "GET", OriginalURL: "/x", StatusCode: 200,
		StartedAt: fixedTime(t, "2026-09-01T10:00:00Z")}
	enqueued := 0
	capture := NewCapture(spec, func(Input) bool {
		enqueued++
		return true
	})
	if !capture.RecordFinish(200, nil) {
		t.Fatal("first finish should record")
	}
	if capture.RecordClosed(499, nil) || capture.RecordFinish(200, nil) {
		t.Fatal("double signalling must be ignored")
	}
	if enqueued != 1 {
		t.Fatalf("enqueued %d times", enqueued)
	}
	if !capture.Recorded() {
		t.Fatal("recorded flag")
	}
}

// TestCaptureClosedBeforeFinish pins the 499 record path.
func TestCaptureClosedBeforeFinish(t *testing.T) {
	var got Input
	capture := NewCapture(CaptureSpec{Method: "POST", OriginalURL: "/slow",
		StartedAt: fixedTime(t, "2026-09-01T10:00:00Z")}, func(input Input) bool {
		got = input
		return true
	})
	if !capture.RecordClosed(200, nil) {
		t.Fatal("close should record")
	}
	if got.StatusCode != 499 || got.Success || got.ErrorCode != "public_api_client_closed" {
		t.Fatalf("closed record: %+v", got)
	}
}

// TestSanitizeURLForLog mirrors sanitizeUrlForLog.
func TestSanitizeURLForLog(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"non oauth path untouched", "/v1/chat?state=abc&nonce=x", "/v1/chat?state=abc&nonce=x"},
		{"authorize redacts state", "/oauth/authorize?client_id=c&state=s",
			"/oauth/authorize?client_id=c&state=%5Bredacted%5D"},
		{"authorize redacts all sensitive names", "/oauth/authorize?state=s&nonce=n&code_challenge=c&transaction_id=t&user_code=u",
			"/oauth/authorize?code_challenge=%5Bredacted%5D&nonce=%5Bredacted%5D&state=%5Bredacted%5D&transaction_id=%5Bredacted%5D&user_code=%5Bredacted%5D"},
		{"device redacts user_code", "/oauth/device?user_code=ABC-DEF", "/oauth/device?user_code=%5Bredacted%5D"},
		{"authorize without sensitive params keeps query", "/oauth/authorize?client_id=c", "/oauth/authorize?client_id=c"},
		{"authorize without query", "/oauth/authorize", "/oauth/authorize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeURLForLog(tt.in); got != tt.want {
				t.Fatalf("sanitizeURLForLog(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExtractErrorInfoEdgeCases covers firstString semantics.
func TestExtractErrorInfoEdgeCases(t *testing.T) {
	if code, message := extractPublicAPIErrorInfo(map[string]any{"code": "  ", "message": "ok"}, 400); code != "" || message != "ok" {
		t.Fatalf("blank code should be skipped: %q/%q", code, message)
	}
	if code, message := extractPublicAPIErrorInfo(map[string]any{"error": "plain string error"}, 503); code != "" || message != "plain string error" {
		t.Fatalf("string error member: %q/%q", code, message)
	}
	if code, message := extractPublicAPIErrorInfo(nil, 500); code != "" || message != "服务器内部错误" {
		t.Fatalf("nil payload 500: %q/%q", code, message)
	}
	if _, message := extractPublicAPIErrorInfo(nil, 200); message != "" {
		t.Fatalf("2xx has no error info: %q", message)
	}
}
