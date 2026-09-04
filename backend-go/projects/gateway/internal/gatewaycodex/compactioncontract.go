package gatewaycodex

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Port of response/codex-compaction-contract.ts: the request-side compaction
// expectation predicate the codex preflight and client strategy consume. The
// response-side mismatch frame helpers belong to the response inspection
// slice and are not ported here.

const codexCompactionRawBodyScanEdgeBytes = 64 * 1024

var codexCompactionRequestSearchPattern = regexp.MustCompile(`"type"\s*:\s*"compaction_trigger"`)

// CodexCompactionExpectedForRequest mirrors codexCompactionExpectedForRequest.
func CodexCompactionExpectedForRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	normalizedPath := NormalizedOpenAIRequestPath(req)
	if normalizedPath == "/responses/compact" {
		return true
	}
	return normalizedPath == "/responses" && requestBodyHasCompactionTrigger(req)
}

// NormalizedOpenAIRequestPath mirrors normalizedOpenAIRequestPath: the query
// is dropped, a leading slash is guaranteed and the `/v1` prefix is stripped
// only when followed by `/` or the end of the path (RE2 has no lookahead).
func NormalizedOpenAIRequestPath(req *gatewaypreauth.GatewayRequest) string {
	return normalizeOpenAIRequestPath(req.PathAndQuery())
}

func normalizeOpenAIRequestPath(pathAndQuery string) string {
	rawPath := pathAndQuery
	if index := strings.IndexByte(pathAndQuery, '?'); index >= 0 {
		rawPath = pathAndQuery[:index]
	}
	if rawPath == "" {
		rawPath = "/"
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	path := stripV1Prefix(rawPath)
	if path == "" {
		return "/"
	}
	return path
}

// stripV1Prefix mirrors path.replace(/^\/v1(?=\/|$)/, ”).
func stripV1Prefix(path string) string {
	if !strings.HasPrefix(path, "/v1") {
		return path
	}
	if len(path) == 3 || path[3] == '/' {
		return path[3:]
	}
	return path
}

// isOpenAIResponsesPostRequest mirrors the same-named Node helper.
func isOpenAIResponsesPostRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	path := normalizeOpenAIRequestPath(req.PathAndQuery())
	if path == "" {
		path = "/"
	}
	return path == "/responses"
}

// isOpenAIResponsesCompactPostRequest mirrors the same-named Node helper.
func isOpenAIResponsesCompactPostRequest(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil || req.MethodUpper() != "POST" {
		return false
	}
	path := normalizeOpenAIRequestPath(req.PathAndQuery())
	if path == "" {
		path = "/"
	}
	return path == "/responses/compact"
}

func requestBodyHasCompactionTrigger(req *gatewaypreauth.GatewayRequest) bool {
	parsedBody := req.ParsedJSONObjectBody()
	if parsedBody != nil && jsonValueHasCompactionTrigger(parsedBody, 0, map[uintptr]struct{}{}) {
		return true
	}
	bodyState := req.BodyState()
	if bodyState != nil && bodyState.CodexCompactionTrigger {
		return true
	}
	if bodyState != nil && bodyState.JSONParseStatus == gatewaybody.JSONParseStatusScannedJSON {
		return false
	}
	var rawBody []byte
	if req.Body != nil {
		rawBody = req.Body.RawBody
	}
	if len(rawBody) == 0 || (bodyState != nil && !bodyState.IsJSON) {
		return false
	}
	if len(rawBody) <= codexCompactionRawBodyScanEdgeBytes*2 {
		return codexCompactionRequestSearchPattern.Match(rawBody)
	}
	prefix := rawBody[:codexCompactionRawBodyScanEdgeBytes]
	if codexCompactionRequestSearchPattern.Match(prefix) {
		return true
	}
	tailStart := len(rawBody) - codexCompactionRawBodyScanEdgeBytes
	if tailStart < 0 {
		tailStart = 0
	}
	suffix := rawBody[tailStart:]
	return codexCompactionRequestSearchPattern.Match(suffix)
}

func jsonValueHasCompactionTrigger(value any, depth int, seen map[uintptr]struct{}) bool {
	if depth > 8 {
		return false
	}
	switch typed := value.(type) {
	case []any:
		if cycleKey := sliceCycleKey(typed); cycleKey != 0 {
			if _, ok := seen[cycleKey]; ok {
				return false
			}
			seen[cycleKey] = struct{}{}
			defer delete(seen, cycleKey)
		}
		length := len(typed)
		if length > 500 {
			length = 500
		}
		for index := 0; index < length; index++ {
			if jsonValueHasCompactionTrigger(typed[index], depth+1, seen) {
				return true
			}
		}
		return false
	case map[string]any:
		if cycleKey := mapCycleKey(typed); cycleKey != 0 {
			if _, ok := seen[cycleKey]; ok {
				return false
			}
			seen[cycleKey] = struct{}{}
			defer delete(seen, cycleKey)
		}
		if typeField, hasType := typed["type"]; hasType {
			if text, isString := typeField.(string); isString && text == "compaction_trigger" {
				return true
			}
		}
		visited := 0
		for key, child := range typed {
			if visited >= 200 {
				break
			}
			visited++
			_ = key
			if jsonValueHasCompactionTrigger(child, depth+1, seen) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Node guards cycles with a WeakSet over object identity; JSON-decoded trees
// are acyclic but hand-built maps may cycle, so the Go port keys the visited
// set on the runtime pointer.
func mapCycleKey(value map[string]any) uintptr {
	return reflect.ValueOf(value).Pointer()
}

func sliceCycleKey(value []any) uintptr {
	if len(value) == 0 {
		return 0
	}
	return reflect.ValueOf(value).Pointer()
}
