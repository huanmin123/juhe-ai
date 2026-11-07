// w14d_pure_helpers_test.go closes the remaining pure-function gaps across
// the gatewaycodex package: the encrypted-content recovery signal helpers
// and the history sanitizer utilities. 2026-11-07 桥退役批删除了本文件原
// chat-bridge payload codec / segment store / compact-summary builder 用例。
package gatewaycodex

import (
	"testing"
)

// jsonRecord 是纯函数测试共用的 JSON 对象别名（原定义在已退役的
// chatbridgestate.go，2026-11-07 桥删除批迁入本文件）。
type jsonRecord = map[string]any

func TestW14dEncryptedContentSignalHelpers(t *testing.T) {
	// structuredErrorPayloads extracts direct JSON plus SSE data lines.
	mixed := "{\"a\":1}\nevent: response.failed\ndata: {\"b\":2}\n\nplain"
	payloads := structuredErrorPayloads(mixed)
	if len(payloads) != 1 || payloads[0]["b"] != float64(2) {
		t.Fatalf("payloads: %v", payloads)
	}
	if got := splitLines("a\nb\r\n c"); len(got) != 3 {
		t.Fatalf("splitLines: %v", got)
	}
	if jsTrimStart("  x") != "x" || jsTrimStart("") != "" {
		t.Fatal("jsTrimStart")
	}
	if !looksLikeEncryptedContentDecryptionFailure("encrypted content could not be decrypted") {
		t.Fatal("decryption failure detection")
	}
	if looksLikeEncryptedContentDecryptionFailure("encrypted content is fine") {
		t.Fatal("healthy content must not look like a failure")
	}
	if parseJSONRecord("{bad") != nil {
		t.Fatal("broken JSON must return nil")
	}
	if _, ok := parseJSONObjectBody([]byte("not-json")); ok {
		t.Fatal("parseJSONObjectBody must reject broken JSON")
	}
	// The signal classification matches the exact error codes verbatim.
	if signal := ClassifyCodexEncryptedContentRecoverySignal("invalid_encrypted_content"); signal != SignalInvalidEncryptedContent {
		t.Fatalf("exact code signal: %q", signal)
	}
	if signal := ClassifyCodexEncryptedContentRecoverySignal("thinking_signature_invalid"); signal != SignalThinkingSignatureInvalid {
		t.Fatalf("signature signal: %q", signal)
	}
	if signal := ClassifyCodexEncryptedContentRecoverySignal("no signal here"); signal != "" {
		t.Fatalf("plain text signal: %q", signal)
	}
}

func TestW14dHistorySanitizerHelpers(t *testing.T) {
	// Replayable item detection.
	if !IsReplayableCodexHistoryItem(jsonRecord{"type": "message", "role": "user", "content": []any{}}) {
		t.Fatal("message item is replayable")
	}
	if IsReplayableCodexHistoryItem(jsonRecord{"type": "message", "content": []any{}}) {
		t.Fatal("message without role is not replayable")
	}
	if IsReplayableCodexHistoryItem(jsonRecord{"type": "reasoning"}) {
		t.Fatal("reasoning-only items are not replayable")
	}
	if hasArrayContent([]any{}) {
		t.Fatal("an empty array has no content")
	}
	if !hasArrayContent([]any{"x"}) {
		t.Fatal("a non-empty array has content")
	}
	if hasNonEmptyPrefixAndSuffix("") || hasNonEmptyPrefixAndSuffix("a") {
		t.Fatal("prefix/suffix needs both ends")
	}
	if startsWith("abc", "ab") == false || startsWith("ab", "abc") {
		t.Fatal("startsWith")
	}
	if indexOfByte("abc", 'b') != 1 || indexOfByte("abc", 'z') != -1 {
		t.Fatal("indexOfByte")
	}
	if stringValue("v") != "v" || stringValue(7) != "" {
		t.Fatal("stringValue")
	}
	original := jsonRecord{"a": 1, "nested": jsonRecord{"b": []any{1, "x"}}}
	cloned := cloneJSONMap(original)
	cloned["a"] = 2
	if original["a"] != 1 {
		t.Fatal("clone must not share the map")
	}
	if cloneJSONValue("s") != "s" {
		t.Fatal("clone scalar")
	}
	// pushIssueCode dedupes.
	codes := pushIssueCode(pushIssueCode(nil, "a"), "a")
	if len(codes) != 1 {
		t.Fatalf("issue codes: %v", codes)
	}
}
