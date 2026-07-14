package publicapilog

import (
	"encoding/json"
	"fmt"
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

func TestBoundedSnapshotPreservesJSONNumberShape(t *testing.T) {
	snapshot := BoundedSnapshot(map[string]any{
		"body": map[string]any{
			"weight": json.Number("10"),
			"ratio":  json.Number("1.25"),
		},
	}, 0)

	encoded, err := json.Marshal(snapshot.Data)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"weight":10`) || !strings.Contains(text, `"ratio":1.25`) {
		t.Fatalf("snapshot JSON numbers changed shape: %s", text)
	}
}

func TestBoundedSnapshotUsesDeterministicEntryWindow(t *testing.T) {
	data := make(map[string]any, SnapshotMaxEntries+1)
	for index := SnapshotMaxEntries; index >= 0; index-- {
		data[fmt.Sprintf("key%03d", index)] = index
	}

	snapshot := BoundedSnapshot(map[string]any{"body": data}, 0)
	if snapshot.Status != port.PublicAPILogCaptureTruncated {
		t.Fatalf("status = %q, want truncated", snapshot.Status)
	}
	preview, ok := snapshot.Data["preview"].(string)
	if !ok {
		t.Fatalf("preview = %#v, want string", snapshot.Data["preview"])
	}
	if !strings.Contains(preview, `"key000":0`) || !strings.Contains(preview, `"key199":199`) {
		t.Fatalf("preview did not retain deterministic first window: %s", preview)
	}
	if strings.Contains(preview, `"key200":200`) {
		t.Fatalf("preview retained entry outside deterministic window: %s", preview)
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

func TestSnapshotPreservesCapturedValues(t *testing.T) {
	hash := strings.Repeat("a", 64)
	apiKey := "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sourceToken := "juis_plain_secret_value"
	proxyURL := "http://proxy-user:proxy-pass@example.com:8080"
	callbackURL := "/callback?token=plain_secret_value"
	baseURL := "https://user:password@example.com/v1"
	snapshot := BuildResponseSnapshot(ResponseSnapshotInput{
		StatusCode: 201,
		Body: map[string]any{
			"data": map[string]any{
				"apiKey": map[string]any{
					"id":        "key_public",
					"keyPrefix": "sk-12345",
					"key":       apiKey,
				},
				"sourceToken": sourceToken,
				"tokenHash":   hash,
				"proxyUrl":    proxyURL,
			},
			"url":     callbackURL,
			"baseUrl": baseURL,
			"ids":     []any{"not-secret", hash},
		},
	})

	data := snapshot.Data["body"].(map[string]any)["data"].(map[string]any)
	apiKeyData := data["apiKey"].(map[string]any)
	if apiKeyData["key"] != apiKey {
		t.Fatalf("api key = %#v, want original value", apiKeyData["key"])
	}
	if data["sourceToken"] != sourceToken {
		t.Fatalf("source token = %#v, want original value", data["sourceToken"])
	}
	if data["tokenHash"] != hash {
		t.Fatalf("token hash = %#v, want original value", data["tokenHash"])
	}
	if data["proxyUrl"] != proxyURL {
		t.Fatalf("proxyUrl = %#v, want original value", data["proxyUrl"])
	}
	if snapshot.Data["body"].(map[string]any)["url"] != callbackURL {
		t.Fatalf("url = %#v, want original value", snapshot.Data["body"].(map[string]any)["url"])
	}
	if snapshot.Data["body"].(map[string]any)["baseUrl"] != baseURL {
		t.Fatalf("baseUrl = %#v, want original value", snapshot.Data["body"].(map[string]any)["baseUrl"])
	}
	ids := snapshot.Data["body"].(map[string]any)["ids"].([]any)
	if ids[1] != hash {
		t.Fatalf("hash string = %#v, want original value", ids[1])
	}
	if apiKeyData["keyPrefix"] != "sk-12345" {
		t.Fatalf("keyPrefix = %#v, want original value", apiKeyData["keyPrefix"])
	}
}
