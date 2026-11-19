package aipublic

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// TestParseApiKeyClientIPConcurrency 固定公开面 API Key add/update 对
// clientIpConcurrencyLimit 的白名单、默认值、整数范围与 null 语义。
func TestParseApiKeyClientIPConcurrency(t *testing.T) {
	baseAdd := func(extra map[string]any) map[string]any {
		body := map[string]any{"targetUsername": "alice", "name": "key", "routeStrategyId": "rs-1"}
		for key, value := range extra {
			body[key] = value
		}
		return body
	}

	add, issue := parseApiKeyAddBody(baseAdd(map[string]any{"clientIpConcurrencyLimit": float64(7)}))
	if issue != "" || add.ClientIPConcurrencyLimit != 7 {
		t.Fatalf("add limit: %+v %q", add, issue)
	}
	add, issue = parseApiKeyAddBody(baseAdd(map[string]any{"clientIpConcurrencyLimit": nil}))
	if issue != "" || add.ClientIPConcurrencyLimit != 0 {
		t.Fatalf("add null default: %+v %q", add, issue)
	}
	add, issue = parseApiKeyAddBody(baseAdd(nil))
	if issue != "" || add.ClientIPConcurrencyLimit != 0 {
		t.Fatalf("add absent default: %+v %q", add, issue)
	}

	for _, testCase := range []struct {
		name  string
		value any
		issue string
	}{
		{"string", "5", zodInvalidType("number", "5")},
		{"float", float64(1.5), "Expected integer, received float"},
		{"negative", float64(-1), zodNumberMin(0)},
		{"over max", float64(apikeys.MaxClientIPConcurrencyLimit + 1), zodNumberMax(apikeys.MaxClientIPConcurrencyLimit)},
	} {
		if _, issue := parseApiKeyAddBody(baseAdd(map[string]any{"clientIpConcurrencyLimit": testCase.value})); issue != testCase.issue {
			t.Fatalf("add %s: %q (want %q)", testCase.name, issue, testCase.issue)
		}
	}

	update, issue := parseApiKeyUpdateBody(map[string]any{
		"apiKeyId": "key-1", "clientIpConcurrencyLimit": float64(0),
	})
	if issue != "" || !update.HasClientIPConcurrencyLimit || update.ClientIPConcurrencyLimit != 0 {
		t.Fatalf("update explicit zero: %+v %q", update, issue)
	}
	update, issue = parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "name": "renamed"})
	if issue != "" || update.HasClientIPConcurrencyLimit {
		t.Fatalf("update absent keeps unset: %+v %q", update, issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "clientIpConcurrencyLimit": nil}); issue != zodInvalidType("number", nil) {
		t.Fatalf("update null: %q", issue)
	}
	if _, issue := parseApiKeyUpdateBody(map[string]any{"apiKeyId": "key-1", "clientIpConcurrencyLimit": float64(1.25)}); issue != "Expected integer, received float" {
		t.Fatalf("update float: %q", issue)
	}

	if summary := sanitizeApiKeyItem(&apikeys.ListItem{ID: "key-1", ClientIPConcurrencyLimit: 9}); summary.ClientIPConcurrencyLimit != 9 {
		t.Fatalf("sanitize limit: %+v", summary)
	}
}
