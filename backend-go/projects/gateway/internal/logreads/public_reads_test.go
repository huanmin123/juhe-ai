package logreads

import (
	"net/http"
	"strings"
	"testing"
)

var publicReadsDDL = []string{
	`CREATE TABLE IF NOT EXISTS public_api_logs (
	  id TEXT PRIMARY KEY,
	  trace_id TEXT,
	  source_ref_id TEXT,
	  source_name TEXT,
	  token_id TEXT,
	  token_name TEXT,
	  token_prefix TEXT,
	  is_test_token INTEGER NOT NULL DEFAULT 0,
	  method TEXT NOT NULL,
	  path TEXT NOT NULL,
	  query_string TEXT,
	  client_ip TEXT,
	  user_agent TEXT,
	  status_code INTEGER,
	  success INTEGER NOT NULL DEFAULT 0,
	  duration_ms INTEGER,
	  request_size_bytes INTEGER NOT NULL DEFAULT 0,
	  response_size_bytes INTEGER NOT NULL DEFAULT 0,
	  request_capture_status TEXT NOT NULL DEFAULT 'empty',
	  response_capture_status TEXT NOT NULL DEFAULT 'empty',
	  request_data_json TEXT NOT NULL DEFAULT '{}',
	  response_data_json TEXT NOT NULL DEFAULT '{}',
	  error_code TEXT,
	  error_message TEXT,
	  started_at TEXT NOT NULL,
	  ended_at TEXT NOT NULL,
	  created_at TEXT NOT NULL
	)`,
}

func seedPublicReads(t *testing.T, env *readsTestEnv) {
	t.Helper()
	env.exec(t, `INSERT INTO public_api_logs (id, trace_id, source_ref_id, source_name, token_id, token_name,
		token_prefix, is_test_token, method, path, query_string, client_ip, user_agent, status_code, success,
		duration_ms, request_size_bytes, response_size_bytes, request_capture_status, response_capture_status,
		request_data_json, response_data_json, error_code, error_message, started_at, ended_at, created_at)
		VALUES ('p-2', 'ptrace-ab', 'ref-7', 'billing-service', 'tok-1', 'Billing Token', 'sk-bil', 1, 'POST',
		'/v1/public/echo', 'q=1', '10.1.1.1', 'ua-2', 200, 1, 88, 120, 340, 'complete', 'complete',
		'{"echo":true}', '{"ok":1}', NULL, NULL, '2026-06-02T10:00:00.000Z', '2026-06-02T10:00:00.100Z',
		'2026-06-02T10:00:01.000Z')`)
	env.exec(t, `INSERT INTO public_api_logs (id, trace_id, source_ref_id, source_name, method, path, client_ip,
		user_agent, status_code, success, duration_ms, request_size_bytes, response_size_bytes,
		request_capture_status, response_capture_status, request_data_json, response_data_json, error_code,
		error_message, started_at, ended_at, created_at)
		VALUES ('p-1', 'ptrace-cd', 'ref-9', 'sdk-client', 'POST', '/v1/public/echo', '10.2.2.2', 'ua-1', 500, 0,
		45, 60, 15, 'complete', 'truncated', '{"foo":"bar"}', '{"baz":1}', 'internal_error', 'boom',
		'2026-06-01T10:00:00.000Z', '2026-06-01T10:00:00.050Z', '2026-06-01T10:00:01.000Z')`)
}

func TestPublicApiLogReadsList(t *testing.T) {
	env := newReadsTestEnv(t, publicReadsDDL, nil, true)
	seedPublicReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs", "")
	if code != http.StatusOK {
		t.Fatalf("list status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	items := wantItems(t, data)
	if len(items) != 2 {
		t.Fatalf("items: %v", items)
	}
	if id := wantString(t, items[0], "id"); id != "p-2" {
		t.Fatalf("expected created_at DESC order (p-2 first), got %s", id)
	}
	first := items[0]
	if wantString(t, first, "createdAt") != "2026-06-02T10:00:01.000Z" ||
		wantString(t, first, "sourceName") != "billing-service" ||
		wantString(t, first, "method") != "POST" || wantString(t, first, "path") != "/v1/public/echo" ||
		!wantBool(t, first, "success") || wantFloat(t, first, "statusCode") != 200 ||
		wantFloat(t, first, "durationMs") != 88 || wantString(t, first, "traceId") != "ptrace-ab" {
		t.Fatalf("item mapping: %v", first)
	}
	// Default page size is 50 for public api logs.
	if wantFloat(t, data, "total") != 2 || wantFloat(t, data, "pageSize") != 50 || wantBool(t, data, "hasMore") {
		t.Fatalf("pagination envelope: %v", data)
	}

	// result filter success/failed; unknown values are ignored.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?result=success", "")
	if code != http.StatusOK {
		t.Fatalf("result filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-2" {
		t.Fatalf("result=success items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?result=failed", "")
	if code != http.StatusOK {
		t.Fatalf("result=failed status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-1" {
		t.Fatalf("result=failed items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?result=bogus", "")
	if code != http.StatusOK {
		t.Fatalf("result=bogus status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 2 {
		t.Fatalf("result=bogus should be ignored: %v", items)
	}

	// Prefix, exact and statusCode filters; path drops method prefix + query.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?traceId=ptrace-c", "")
	if code != http.StatusOK {
		t.Fatalf("traceId filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-1" {
		t.Fatalf("traceId filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?statusCode=200", "")
	if code != http.StatusOK {
		t.Fatalf("statusCode filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-2" {
		t.Fatalf("statusCode filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?path=POST%20%2Fv1%2Fpublic%2Fecho%3Fx%3D1", "")
	if code != http.StatusOK {
		t.Fatalf("path filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 2 {
		t.Fatalf("normalized path filter items: %v", items)
	}

	// Strict time range: invalid bound is 400, reversed pair is swapped.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?startAt=yesterday", "")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid startAt status: %d %v", code, payload)
	}
	if message := wantString(t, payload, "message"); !strings.Contains(message, "开始时间") {
		t.Fatalf("invalid startAt message: %q", message)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?startAt=2026-06-02T00:00:00Z&endAt=2026-06-01T00:00:00Z", "")
	if code != http.StatusOK {
		t.Fatalf("reversed range status: %d", code)
	}
	// After the swap the window is [06-01, 06-02] → p-1; unswapped → p-2.
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-1" {
		t.Fatalf("reversed range should be swapped (p-1 expected): %v", items)
	}

	// sourceRefId exact match.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs?sourceRefId=ref-9", "")
	if code != http.StatusOK {
		t.Fatalf("sourceRefId filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "p-1" {
		t.Fatalf("sourceRefId filter items: %v", items)
	}
}

func TestPublicApiLogReadsDetailSupplement(t *testing.T) {
	env := newReadsTestEnv(t, publicReadsDDL, nil, true)
	seedPublicReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs/p-1", "")
	if code != http.StatusOK {
		t.Fatalf("detail status: %d %v", code, payload)
	}
	detail := wantData(t, payload)
	if wantString(t, detail, "sourceRefId") != "ref-9" || wantBool(t, detail, "isTestToken") ||
		wantString(t, detail, "errorCode") != "internal_error" || wantString(t, detail, "errorMessage") != "boom" ||
		wantFloat(t, detail, "requestSizeBytes") != 60 || wantFloat(t, detail, "responseSizeBytes") != 15 ||
		wantString(t, detail, "requestCaptureStatus") != "complete" ||
		wantString(t, detail, "responseCaptureStatus") != "truncated" {
		t.Fatalf("detail mapping: %v", detail)
	}
	requestData, ok := detail["requestData"].(map[string]any)
	if !ok || wantString(t, requestData, "foo") != "bar" {
		t.Fatalf("requestData: %v", detail["requestData"])
	}
	responseData, ok := detail["responseData"].(map[string]any)
	if !ok || wantFloat(t, responseData, "baz") != 1 {
		t.Fatalf("responseData: %v", detail["responseData"])
	}
	if _, exists := detail["tokenId"]; exists {
		t.Fatalf("absent token fields must be omitted: %v", detail)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs/p-2", "")
	if code != http.StatusOK {
		t.Fatalf("p-2 detail status: %d", code)
	}
	detail = wantData(t, payload)
	if !wantBool(t, detail, "isTestToken") || wantString(t, detail, "tokenId") != "tok-1" ||
		wantString(t, detail, "tokenName") != "Billing Token" || wantString(t, detail, "tokenPrefix") != "sk-bil" ||
		wantString(t, detail, "queryString") != "q=1" || wantString(t, detail, "userAgent") != "ua-2" {
		t.Fatalf("p-2 detail mapping: %v", detail)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/public-api-logs/missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing detail status: %d", code)
	}
	if message := wantString(t, payload, "message"); message != "公开接口日志不存在" {
		t.Fatalf("missing detail message: %q", message)
	}
}
