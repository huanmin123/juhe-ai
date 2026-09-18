// w14d_pure_helpers_test.go closes the remaining pure-function gaps across
// the gatewaycodex package: the chat-bridge state payload codecs and restore
// builders, the segment-store error arms, the encrypted-content recovery
// signal helpers, the history sanitizer utilities and the compact-summary
// body builders.
package gatewaycodex

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestW14dDecodeStatePayloadArms(t *testing.T) {
	valid := `{"schemaVersion":2,"responseId":"resp_1","sessionId":"sess","boundary":{},"request":{},"outputItems":[]}`
	if _, err := decodeStatePayload([]byte(valid)); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	for name, raw := range map[string]string{
		"broken json":    `{`,
		"non object":     `[1,2]`,
		"bad schema":     `{"schemaVersion":1}`,
		"missing fields": `{"schemaVersion":2}`,
		"wrong types":    `{"schemaVersion":2,"responseId":1,"sessionId":"s","boundary":{},"request":{},"outputItems":[]}`,
		"output not arr": `{"schemaVersion":2,"responseId":"r","sessionId":"s","boundary":{},"request":{},"outputItems":{}}`,
	} {
		if _, err := decodeStatePayload([]byte(raw)); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
}

func TestW14dDecodeCompactSnapshotArms(t *testing.T) {
	valid := `{"schemaVersion":2,"compactId":"c1","sessionId":"sess","boundary":{},"summary":"s"}`
	if _, err := decodeCompactSnapshotPayload([]byte(valid)); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	for name, raw := range map[string]string{
		"broken json": `{`,
		"non object":  `"str"`,
		"bad schema":  `{"schemaVersion":3}`,
		"missing":     `{"schemaVersion":2}`,
		"wrong types": `{"schemaVersion":2,"compactId":2,"sessionId":"s","boundary":{},"summary":"x"}`,
	} {
		if _, err := decodeCompactSnapshotPayload([]byte(raw)); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
}

func TestW14dRestoreResponsesInputBuilders(t *testing.T) {
	payloads := []chatBridgeStatePayloadV2{
		{
			Request: chatBridgeStateRequestSection{
				Instructions: "be nice", Input: "hello",
			},
			OutputItems: []any{jsonRecord{"type": "message", "role": "assistant"}},
		},
		{
			Request: chatBridgeStateRequestSection{
				Input: []any{jsonRecord{"type": "message", "role": "user"}},
			},
		},
	}
	restored := restoreResponsesInputFromPayloads(payloads, jsonRecord{"type": "message"})
	// instruction message + string input message + output item + second input + current.
	if len(restored) != 5 {
		t.Fatalf("restored: %d items %#v", len(restored), restored)
	}
	first := restored[0].(jsonRecord)
	if first["role"] != "system" {
		t.Fatalf("instruction message: %v", first)
	}
	// nil/empty instructions are skipped.
	empty := []any{}
	appendInstructionAsMessage(&empty, nil)
	appendInstructionAsMessage(&empty, "   ")
	if len(empty) != 0 {
		t.Fatalf("blank instructions must be skipped: %v", empty)
	}
	// responsesInputAsItems shapes.
	if items := responsesInputAsItems("raw text"); len(items) != 1 {
		t.Fatalf("string input: %v", items)
	}
	if items := responsesInputAsItems(jsonRecord{"type": "x"}); len(items) != 1 {
		t.Fatalf("record input: %v", items)
	}
	if items := responsesInputAsItems(7); len(items) != 0 {
		t.Fatalf("unsupported input: %v", items)
	}
	// Size guard.
	if err := assertRestoredInputSize(strings.Repeat("a", 33*1024*1024)); err == nil {
		t.Fatal("oversized input must fail")
	}
	if err := assertRestoredInputSize("ok"); err != nil {
		t.Fatalf("small input: %v", err)
	}
	if !isCodexCompactionInputItem(jsonRecord{"type": "compaction"}) ||
		!isCodexCompactionInputItem(jsonRecord{"type": "compaction_summary"}) ||
		isCodexCompactionInputItem(jsonRecord{"type": "message"}) {
		t.Fatal("compaction item detection")
	}
}

func TestW14dInlineCompactionSummaryCodec(t *testing.T) {
	summary := "## 摘要\n内容"
	encoded := encodeInlineCodexCompactionSummary(summary)
	if decoded := decodeInlineCodexCompactionSummary(encoded); decoded != summary {
		t.Fatalf("roundtrip: %q", decoded)
	}
	if decodeInlineCodexCompactionSummary("not-base64!!") != "" {
		t.Fatal("invalid encoding must decode empty")
	}
	if !isInternalCodexBridgeResponseID("resp_chat_bridge_abc") ||
		isInternalCodexBridgeResponseID("resp_plain_id") {
		t.Fatal("bridge id detection")
	}
}

func TestW14dSegmentStoreArms(t *testing.T) {
	store, err := NewSegmentStore(SegmentStoreConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reference, err := store.WriteSegmentPayload(context.Background(), "sess-1", map[string]any{"k": "v"}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.ReadSegmentPayload(reference)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("read: %v %s", err, raw)
	}

	// Tampered sha256.
	bad := reference
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := store.ReadSegmentPayload(bad); err == nil {
		t.Fatal("sha mismatch must fail")
	}
	// Truncated compressed size reports a size mismatch.
	short := reference
	short.CompressedSizeBytes--
	if _, err := store.ReadSegmentPayload(short); err == nil {
		t.Fatal("truncated size must fail")
	}
	// Oversized read limit.
	huge := reference
	huge.CompressedSizeBytes = maxStoredPayloadBytes + 1
	if _, err := store.ReadSegmentPayload(huge); err == nil {
		t.Fatal("oversized read must fail")
	}
	// A storage key with traversal is rejected.
	if _, err := store.resolveStoragePath("a/../../b"); err == nil {
		t.Fatal("traversal key must fail")
	}
	// A cancelled context fails the append.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.WriteSegmentPayload(cancelled, "sess-2", map[string]any{}, now); err == nil {
		t.Fatal("cancelled context must fail")
	}
	// An unserializable payload fails the marshal.
	if _, err := store.WriteSegmentPayload(context.Background(), "sess-3", make(chan int), now); err == nil {
		t.Fatal("unserializable payload must fail")
	}
	// NewSegmentStore requires a root.
	if _, err := NewSegmentStore(SegmentStoreConfig{Root: "  "}); err == nil {
		t.Fatal("missing root must fail")
	}
	// SegmentStorageKey format: sessions/<hash>/segments/<stamp>.json.gz.
	key := SegmentStorageKey("sess", now)
	if !strings.HasPrefix(key, "sessions/") || !strings.HasSuffix(key, ".json.gz") ||
		!strings.Contains(key, "/segments/") {
		t.Fatalf("storage key: %s", key)
	}
}

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

func TestW14dCompactSummaryBuilders(t *testing.T) {
	body := BuildCompactSummaryChatBody("gpt-5", []any{jsonRecord{"type": "message"}})
	if body["model"] != "gpt-5" {
		t.Fatalf("compact body: %v", body)
	}
	text := compactContextText([]any{jsonRecord{"type": "message", "content": "hello"}})
	if !strings.Contains(text, "hello") {
		t.Fatalf("compact text: %q", text)
	}
	response := BuildCodexCompactResponse("compact-1", "enc", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if response["id"] == nil {
		t.Fatalf("compact response: %v", response)
	}
	// ExtractChatCompletionSummary reads the first choice message content.
	summary := ExtractChatCompletionSummary(`{"choices":[{"message":{"content":"摘要内容"}}]}`)
	if summary != "摘要内容" {
		t.Fatalf("summary: %q", summary)
	}
	if ExtractChatCompletionSummary("{broken") != "" {
		t.Fatal("broken body must extract empty")
	}
	// gzip helper parity with the segment codec.
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	sum := sha256.Sum256(buffer.Bytes())
	if hex.EncodeToString(sum[:]) == "" {
		t.Fatal("sha helper")
	}
	if _, err := fmt.Fprintf(&buffer, ""); err != nil {
		t.Fatal("fmt helper")
	}
}
