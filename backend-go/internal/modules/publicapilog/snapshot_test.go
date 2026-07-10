package publicapilog

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestBuildRequestSnapshotDropsRejectedBody(t *testing.T) {
	bodySize := int64(256 * 1024)
	snapshot := BuildRequestSnapshot(RequestSnapshotInput{
		Method:             "post",
		Path:               "/__aipublic__/account/add",
		Query:              map[string]any{"targetUsername": "admin"},
		Body:               map[string]any{"apiKey": "secret"},
		ContentType:        "application/json",
		ContentLength:      "262144",
		BodySizeBytes:      &bodySize,
		QueryString:        "targetUsername=admin",
		BodyRejectedReason: "request_body_too_large",
	})

	if snapshot.Status != port.PublicAPILogCaptureDropped {
		t.Fatalf("status = %q, want dropped", snapshot.Status)
	}
	body, ok := snapshot.Data["body"].(map[string]any)
	if !ok {
		t.Fatalf("body = %#v, want object", snapshot.Data["body"])
	}
	if body["reason"] != "request_body_too_large" || body["dropped"] != true {
		t.Fatalf("body = %#v", body)
	}
	headers, ok := snapshot.Data["headers"].(map[string]any)
	if !ok || headers["contentType"] != "application/json" || headers["contentLength"] != "262144" {
		t.Fatalf("headers = %#v", snapshot.Data["headers"])
	}
}

func TestBoundedSnapshotTruncatesLargePayload(t *testing.T) {
	snapshot := BoundedSnapshot(map[string]any{
		"body": strings.Repeat("你", SnapshotMaxBytes),
	}, int64(SnapshotMaxBytes*3))

	if snapshot.Status != port.PublicAPILogCaptureTruncated {
		t.Fatalf("status = %q, want truncated", snapshot.Status)
	}
	if snapshot.SizeBytes != int64(SnapshotMaxBytes*3) {
		t.Fatalf("size = %d", snapshot.SizeBytes)
	}
	if snapshot.Data["truncated"] != true {
		t.Fatalf("snapshot data = %#v", snapshot.Data)
	}
	preview, ok := snapshot.Data["preview"].(string)
	if !ok || len([]byte(preview)) > SnapshotMaxBytes {
		t.Fatalf("preview length = %d, ok=%v", len([]byte(preview)), ok)
	}
}

func TestBuildResponseSnapshotEmpty(t *testing.T) {
	snapshot := BuildResponseSnapshot(ResponseSnapshotInput{StatusCode: 204})
	if snapshot.Status != port.PublicAPILogCaptureEmpty {
		t.Fatalf("status = %q, want empty", snapshot.Status)
	}
	if snapshot.SizeBytes != 0 {
		t.Fatalf("size = %d, want 0", snapshot.SizeBytes)
	}
}

func TestEstimatePayloadSizeBytesHandlesBytesAndStrings(t *testing.T) {
	if got := EstimatePayloadSizeBytes([]byte("hello")); got != 5 {
		t.Fatalf("bytes size = %d, want 5", got)
	}
	if got := EstimatePayloadSizeBytes("你好"); got != 6 {
		t.Fatalf("string size = %d, want 6", got)
	}
}

func TestBoundedSnapshotPreservesLargeIntegerShape(t *testing.T) {
	const largeID int64 = 9223372036854775807
	snapshot := BoundedSnapshot(map[string]any{
		"body": map[string]any{"id": largeID},
	}, 0)

	body, ok := snapshot.Data["body"].(map[string]any)
	if !ok {
		t.Fatalf("body = %#v, want object", snapshot.Data["body"])
	}
	if body["id"] != largeID {
		t.Fatalf("id = %#v, want int64 %d", body["id"], largeID)
	}
}

func TestSnapshotDoesNotIncludeAuthorizationHeaders(t *testing.T) {
	snapshot := BuildRequestSnapshot(RequestSnapshotInput{
		Method:        "GET",
		Path:          "/__aipublic__/group/list",
		Query:         map[string]any{},
		ContentType:   "application/json",
		ContentLength: "0",
	})
	headers, ok := snapshot.Data["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers = %#v", snapshot.Data["headers"])
	}
	if _, ok := headers["authorization"]; ok {
		t.Fatalf("headers should not include authorization: %#v", headers)
	}
	if _, ok := headers["cookie"]; ok {
		t.Fatalf("headers should not include cookie: %#v", headers)
	}
}

func TestSnapshotRedactsRecursiveSecrets(t *testing.T) {
	hash := strings.Repeat("a", 64)
	snapshot := BuildResponseSnapshot(ResponseSnapshotInput{
		StatusCode: 201,
		Body: map[string]any{
			"data": map[string]any{
				"apiKey": map[string]any{
					"id":        "key_public",
					"keyPrefix": "sk-12345",
					"key":       "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				},
				"sourceToken": "juis_plain_secret_value",
				"tokenHash":   hash,
				"proxyUrl":    "http://proxy-user:proxy-pass@example.com:8080",
			},
			"url":     "/callback?token=plain_secret_value",
			"baseUrl": "https://user:password@example.com/v1",
			"ids":     []any{"not-secret", hash},
		},
	})

	data := snapshot.Data["body"].(map[string]any)["data"].(map[string]any)
	apiKey := data["apiKey"].(map[string]any)
	if apiKey["key"] != "[redacted]" {
		t.Fatalf("api key secret = %#v, want redacted", apiKey["key"])
	}
	if data["sourceToken"] != "[redacted]" {
		t.Fatalf("source token = %#v, want redacted", data["sourceToken"])
	}
	if data["tokenHash"] != "[redacted]" {
		t.Fatalf("token hash = %#v, want redacted", data["tokenHash"])
	}
	if data["proxyUrl"] != "[redacted]" {
		t.Fatalf("proxyUrl = %#v, want redacted", data["proxyUrl"])
	}
	if snapshot.Data["body"].(map[string]any)["url"] != "[redacted]" {
		t.Fatalf("url secret = %#v, want redacted", snapshot.Data["body"].(map[string]any)["url"])
	}
	if snapshot.Data["body"].(map[string]any)["baseUrl"] != "[redacted]" {
		t.Fatalf("baseUrl userinfo = %#v, want redacted", snapshot.Data["body"].(map[string]any)["baseUrl"])
	}
	ids := snapshot.Data["body"].(map[string]any)["ids"].([]any)
	if ids[1] != "[redacted]" {
		t.Fatalf("hash string = %#v, want redacted", ids[1])
	}
	if apiKey["keyPrefix"] != "sk-12345" {
		t.Fatalf("keyPrefix = %#v, want preserved", apiKey["keyPrefix"])
	}
}

func TestSanitizeQueryStringRedactsSecrets(t *testing.T) {
	hash := strings.Repeat("a", 64)
	got := SanitizeQueryString("targetUsername=admin&keyword=sk-0123456789abcdef0123456789abcdef&authorization=Bearer%20abcdefghijklmnop&tokenHash=" + hash + "&empty=&plain=value")
	if strings.Contains(got, "sk-0123456789abcdef0123456789abcdef") ||
		strings.Contains(got, "Bearer") ||
		strings.Contains(got, hash) {
		t.Fatalf("sanitized query leaked secret: %s", got)
	}
	for _, want := range []string{
		"targetUsername=admin",
		"keyword=[redacted]",
		"authorization=[redacted]",
		"tokenHash=[redacted]",
		"empty=",
		"plain=value",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized query = %s, want to contain %s", got, want)
		}
	}
}
