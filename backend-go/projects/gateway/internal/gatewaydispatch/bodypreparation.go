package gatewaydispatch

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// prepareAnthropicMessagesBodyForAttempt + preparedUpstreamBodyMetadata,
// migrated from upstream/body-preparation.ts.

// PrepareAnthropicMessagesBodyForAttempt mirrors
// prepareAnthropicMessagesBodyForAttempt: Anthropic /messages requests get a
// normalized body (stream:false dropped, all-text content blocks joined).
func PrepareAnthropicMessagesBodyForAttempt(
	req *gatewaypreauth.GatewayRequest,
	headers http.Header,
	upstreamURL string,
	body []byte,
) []byte {
	if body == nil {
		return nil
	}
	if !isAnthropicMessagesRequest(headers, upstreamURL) {
		return body
	}
	if req != nil && req.Body != nil && bytesEqual(req.Body.RawBody, body) {
		return body
	}
	parsed, ok := decodeJSONObject(body)
	if !ok {
		return body
	}
	normalized, changed := normalizeAnthropicMessagesBody(parsed)
	if !changed {
		return body
	}
	serialized, err := json.Marshal(normalized)
	if err != nil {
		return body
	}
	return serialized
}

// PreparedUpstreamBodyMetadata mirrors preparedUpstreamBodyMetadata: the JSON
// body metadata used for service-tier / reasoning-effort propagation.
func PreparedUpstreamBodyMetadata(req *gatewaypreauth.GatewayRequest, body []byte) *gatewaybody.JSONBodyMetadata {
	if body == nil {
		return nil
	}
	if req != nil && req.Body != nil && bytesEqual(req.Body.RawBody, body) {
		state := req.BodyState()
		// Scanned bodies already received the complete top-level metadata scan
		// in body admission; parsed/synthetic states keep the exact-body
		// fallback.
		if state != nil && gatewaybody.IsScannedJSONBody(req.Body) {
			return &gatewaybody.JSONBodyMetadata{
				Model:                   state.Model,
				Stream:                  state.Stream,
				ServiceTier:             &state.ServiceTier,
				ReasoningEffort:         state.ReasoningEffort,
				MaxOutputTokens:         state.MaxOutputTokens,
				ImageGeneration:         state.ImageGeneration,
				ImageGenerationForced:   state.ImageGenerationForced,
				StrictOutputRequirement: state.StrictOutputRequirement,
				CodexCompactionTrigger:  state.CodexCompactionTrigger,
			}
		}
	}
	metadata := gatewaybody.ExtractJSONBodyMetadata(body)
	return &metadata
}

func normalizeAnthropicMessagesBody(body map[string]any) (map[string]any, bool) {
	changed := false
	if stream, ok := body["stream"].(bool); ok && !stream {
		delete(body, "stream")
		changed = true
	}
	if messages, ok := body["messages"].([]any); ok {
		next := make([]any, len(messages))
		copy(next, messages)
		messagesChanged := false
		for index, item := range next {
			normalized := normalizeAnthropicMessage(item)
			if !jsonValueEqual(normalized, item) {
				next[index] = normalized
				messagesChanged = true
			}
		}
		if messagesChanged {
			body["messages"] = next
			changed = true
		}
	}
	return body, changed
}

func normalizeAnthropicMessage(value any) any {
	record, ok := value.(map[string]any)
	if !ok {
		return value
	}
	content, ok := record["content"].([]any)
	if !ok {
		return value
	}
	texts := make([]string, 0, len(content))
	for _, block := range content {
		blockObject, ok := block.(map[string]any)
		if !ok {
			return value
		}
		if blockObject["type"] != "text" {
			return value
		}
		text, ok := blockObject["text"].(string)
		if !ok {
			return value
		}
		for key := range blockObject {
			if key != "type" && key != "text" {
				return value
			}
		}
		texts = append(texts, text)
	}
	joined := map[string]any{
		"type":    record["type"],
		"role":    record["role"],
		"content": strings.Join(texts, ""),
	}
	for _, key := range []string{"name"} {
		if extra, ok := record[key]; ok {
			joined[key] = extra
		}
	}
	return joined
}

func isAnthropicMessagesRequest(headers http.Header, upstreamURL string) bool {
	return isAnthropicMessagesRequestHeaders(headers) && isAnthropicMessagesPath(upstreamURL)
}

func isAnthropicMessagesRequestHeaders(headers http.Header) bool {
	if headers.Get("Anthropic-Version") == "" {
		return false
	}
	return headers.Get("X-Api-Key") != "" ||
		headers.Get("Anthropic-Api-Key") != "" ||
		headers.Get("Authorization") != ""
}

func isAnthropicMessagesPath(upstreamURL string) bool {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return false
	}
	return stripV1Prefix(parsed.Path) == "/messages"
}

// stripV1Prefix mirrors `.replace(/^\/v1(?=\/|$)/, '') || '/'`.
func stripV1Prefix(path string) string {
	if strings.HasPrefix(path, "/v1") && (len(path) == 3 || path[3] == '/') {
		rest := path[3:]
		if rest == "" {
			return "/"
		}
		return rest
	}
	if path == "" {
		return "/"
	}
	return path
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
