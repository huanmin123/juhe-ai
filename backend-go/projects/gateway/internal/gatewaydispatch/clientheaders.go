package gatewaydispatch

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Codex client headers, migrated from adapters/gpt-codex/client-headers.ts.

// Codex header constants mirror the Node exports.
const (
	OpenAICodexOriginator = "Codex Desktop"
	OpenAICodexVersion    = "0.145.0"
	// OpenAICodexUserAgent mirrors the composed UA string.
	OpenAICodexUserAgent = "Codex Desktop/" + OpenAICodexVersion + " (Windows 10.0.22621; x86_64) unknown (codex_exec; " + OpenAICodexVersion + ")"
	OpenAICodexResponsesLiteHeader = "x-openai-internal-codex-responses-lite"
)

// openAICodexResponsesLiteModels mirrors the lite model set.
var openAICodexResponsesLiteModels = map[string]struct{}{
	"gpt-5.6-sol":   {},
	"gpt-5.6-terra": {},
	"gpt-5.6-luna":  {},
}

// CodexTurnMetadata mirrors CodexTurnMetadata with the free-form extension
// keys carried verbatim.
type CodexTurnMetadata struct {
	InstallationID      string `json:"installation_id"`
	SessionID           string `json:"session_id"`
	ThreadID            string `json:"thread_id"`
	TurnID              string `json:"turn_id"`
	WindowID            string `json:"window_id"`
	RequestKind         string `json:"request_kind"`
	ThreadSource        string `json:"thread_source"`
	Sandbox             string `json:"sandbox"`
	TurnStartedAtUnixMs int64  `json:"turn_started_at_unix_ms"`

	// Extra carries unknown keys from an existing metadata payload (the
	// Node ...current spread).
	Extra map[string]any `json:"-"`
}

// MarshalJSON merges Extra with the typed fields (Node spread semantics).
func (m CodexTurnMetadata) MarshalJSON() ([]byte, error) {
	object := make(map[string]any, len(m.Extra)+9)
	for key, value := range m.Extra {
		object[key] = value
	}
	object["installation_id"] = m.InstallationID
	object["session_id"] = m.SessionID
	object["thread_id"] = m.ThreadID
	object["turn_id"] = m.TurnID
	object["window_id"] = m.WindowID
	object["request_kind"] = m.RequestKind
	object["thread_source"] = m.ThreadSource
	object["sandbox"] = m.Sandbox
	object["turn_started_at_unix_ms"] = m.TurnStartedAtUnixMs
	return json.Marshal(object)
}

// IsOpenAICodexClientHeaders mirrors isOpenAICodexClientHeaders.
func IsOpenAICodexClientHeaders(headers http.Header) bool {
	return isCodexIdentity(headerGetTrimmed(headers, "Originator")) ||
		isCodexIdentity(headerGetTrimmed(headers, "User-Agent"))
}

func headerGetTrimmed(headers http.Header, name string) string {
	value := headers.Get(name)
	return strings.TrimSpace(value)
}

// NormalizeOpenAICodexClientHeaders mirrors normalizeOpenAICodexClientHeaders.
func NormalizeOpenAICodexClientHeaders(headers http.Header, model string) {
	if IsOpenAICodexClientHeaders(headers) {
		return
	}

	metadata := syntheticCodexTurnMetadata(headers)
	headers.Set("Originator", OpenAICodexOriginator)
	headers.Set("User-Agent", OpenAICodexUserAgent)
	setHeaderIfMissing(headers, "Session-Id", metadata.SessionID)
	setHeaderIfMissing(headers, "Thread-Id", metadata.ThreadID)
	setHeaderIfMissing(headers, "X-Client-Request-Id", metadata.SessionID)
	setHeaderIfMissing(headers, "X-Codex-Beta-Features", "remote_compaction_v2")
	encoded, err := json.Marshal(metadata)
	if err == nil {
		headers.Set("X-Codex-Turn-Metadata", string(encoded))
	}
	setHeaderIfMissing(headers, "X-Codex-Window-Id", metadata.WindowID)
	if UsesOpenAICodexResponsesLite(model) {
		headers.Set(OpenAICodexResponsesLiteHeader, "true")
	} else {
		headers.Del(OpenAICodexResponsesLiteHeader)
	}
}

// UsesOpenAICodexResponsesLite mirrors usesOpenAICodexResponsesLite.
func UsesOpenAICodexResponsesLite(model string) bool {
	_, ok := openAICodexResponsesLiteModels[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

// NormalizeOpenAICodexResponsesLiteBody mirrors
// normalizeOpenAICodexResponsesLiteBody.
func NormalizeOpenAICodexResponsesLiteBody(body map[string]any, model string, headers http.Header) {
	if headers != nil && !IsOpenAICodexClientHeaders(headers) {
		NormalizeOpenAICodexClientHeaders(headers, model)
		metadata := parsedCodexTurnMetadata(headers.Get("X-Codex-Turn-Metadata"))
		if metadata != nil {
			clientMetadata, _ := body["client_metadata"].(map[string]any)
			merged := make(map[string]any, len(clientMetadata)+6)
			for key, value := range clientMetadata {
				merged[key] = value
			}
			encoded, _ := json.Marshal(metadata)
			merged["x-codex-window-id"] = textValue(currentText(metadata, "window_id"))
			merged["turn_id"] = textValue(currentText(metadata, "turn_id"))
			merged["session_id"] = textValue(currentText(metadata, "session_id"))
			merged["x-codex-turn-metadata"] = string(encoded)
			merged["x-codex-installation-id"] = textValue(currentText(metadata, "installation_id"))
			merged["thread_id"] = textValue(currentText(metadata, "thread_id"))
			body["client_metadata"] = merged
			if _, ok := body["prompt_cache_key"]; !ok {
				body["prompt_cache_key"] = textValue(currentText(metadata, "session_id"))
			}
		}
	}
	if !UsesOpenAICodexResponsesLite(model) {
		return
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	mergedReasoning := make(map[string]any, len(reasoning)+1)
	for key, value := range reasoning {
		mergedReasoning[key] = value
	}
	mergedReasoning["context"] = "all_turns"
	body["reasoning"] = mergedReasoning
	body["parallel_tool_calls"] = false
}

func syntheticCodexTurnMetadata(headers http.Header) CodexTurnMetadata {
	current := parsedCodexTurnMetadata(headers.Get("X-Codex-Turn-Metadata"))
	sessionID := firstNonEmptyString(textValue(currentText(current, "session_id")), headerGetTrimmed(headers, "Session-Id"), randomUUID())
	threadID := firstNonEmptyString(textValue(currentText(current, "thread_id")), headerGetTrimmed(headers, "Thread-Id"), sessionID)
	turnID := firstNonEmptyString(textValue(currentText(current, "turn_id")), headerGetTrimmed(headers, "X-Client-Request-Id"), randomUUID())
	installationID := firstNonEmptyString(textValue(currentText(current, "installation_id")), headerGetTrimmed(headers, "X-Codex-Installation-Id"), randomUUID())
	windowID := firstNonEmptyString(textValue(currentText(current, "window_id")), headerGetTrimmed(headers, "X-Codex-Window-Id"), threadID+":0")

	metadata := CodexTurnMetadata{
		InstallationID: installationID,
		SessionID:      sessionID,
		ThreadID:       threadID,
		TurnID:         turnID,
		WindowID:       windowID,
		RequestKind:    firstNonEmptyString(textValue(currentText(current, "request_kind")), "turn"),
		ThreadSource:   firstNonEmptyString(textValue(currentText(current, "thread_source")), "user"),
		Sandbox:        firstNonEmptyString(textValue(currentText(current, "sandbox")), "none"),
		TurnStartedAtUnixMs: NowMs(),
	}
	if current != nil {
		if startedAt, ok := current["turn_started_at_unix_ms"].(float64); ok {
			metadata.TurnStartedAtUnixMs = int64(startedAt)
		}
		metadata.Extra = extraMetadataKeys(current)
	}
	return metadata
}

func extraMetadataKeys(current map[string]any) map[string]any {
	known := map[string]struct{}{
		"installation_id": {}, "session_id": {}, "thread_id": {}, "turn_id": {},
		"window_id": {}, "request_kind": {}, "thread_source": {}, "sandbox": {},
		"turn_started_at_unix_ms": {},
	}
	var extra map[string]any
	for key, value := range current {
		if _, ok := known[key]; ok {
			continue
		}
		if extra == nil {
			extra = map[string]any{}
		}
		extra[key] = value
	}
	return extra
}

func currentText(current map[string]any, key string) any {
	if current == nil {
		return nil
	}
	return current[key]
}

func textValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func setHeaderIfMissing(headers http.Header, name, value string) {
	if headers.Get(name) == "" {
		headers.Set(name, value)
	}
}

func isCodexIdentity(value string) bool {
	if value == "" {
		return false
	}
	return codexIdentityPrefix(value)
}

// codexIdentityPrefix mirrors /^codex(?:[\s_/-]|$)/i.
func codexIdentityPrefix(value string) bool {
	lowered := strings.ToLower(value)
	if !strings.HasPrefix(lowered, "codex") {
		return false
	}
	rest := lowered[len("codex"):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ' ', '\t', '\n', '\r', '\v', '\f', '_', '/', '-':
		return true
	}
	return false
}

func parsedCodexTurnMetadata(value string) map[string]any {
	if value == "" {
		return nil
	}
	parsed, ok := decodeJSONObject([]byte(value))
	if !ok {
		return nil
	}
	return parsed
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func randomUUID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
