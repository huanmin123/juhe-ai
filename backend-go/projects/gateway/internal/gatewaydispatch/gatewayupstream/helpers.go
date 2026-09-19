package gatewayupstream

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 跨族叶子纯工具（REFACTOR-0006 阶段 A 收敛落位）：以下函数原散落在
// gatewaydispatch 根包（util.go / errorhelpers.go / bodypreparation.go）与
// oauth/codex 适配族（oauthnormalizer.go / oauthadapter.go），被传输族与
// dispatch 两侧共同消费。阶段 A 随传输族落位 gatewayupstream 并导出；
// dispatch 门面经桥转发保持根包消费点零改动，gatewayoauthcodex 经
// gatewayoauthcodex → gatewayupstream 单向边调用。

// DecodeJSONObject parses a JSON object payload into a map; ok=false when
// the payload is not a JSON object (mirrors the isPlainObject guard).
// Migrated from gatewaydispatch util.go decodeJSONObject.
func DecodeJSONObject(raw []byte) (map[string]any, bool) {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}
	object, ok := parsed.(map[string]any)
	return object, ok
}

// TrimString mirrors the Node `.trim()`-based normalization used throughout
// the dispatch pipeline (empty string stays empty).
// Migrated from gatewaydispatch util.go trimString.
func TrimString(value string) string {
	return strings.TrimSpace(value)
}

// FirstNonEmpty returns the first value that is non-blank after trimming.
// Migrated from gatewaydispatch errorhelpers.go firstNonEmpty.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// HeaderValueOf returns the first non-empty trimmed value of a header.
// Migrated from gatewaydispatch oauthnormalizer.go headerValueOf.
func HeaderValueOf(inputHeaders http.Header, name string) string {
	if inputHeaders == nil {
		return ""
	}
	values := inputHeaders.Values(name)
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// StripV1Prefix mirrors `.replace(/^\/v1(?=\/|$)/, ”) || '/'`.
// Migrated from gatewaydispatch bodypreparation.go stripV1Prefix.
func StripV1Prefix(path string) string {
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

// IsOpenAIOAuthCodexCompactRequest mirrors isOpenAIOAuthCodexCompactRequest:
// the /responses/compact endpoint (with the /v1 prefix stripped).
// Migrated from gatewaydispatch oauthadapter.go（传输族流式判定与 oauth codex
// 适配器共同消费）.
func IsOpenAIOAuthCodexCompactRequest(req *gatewaypreauth.GatewayRequest) bool {
	path := req.Path()
	if path == "" {
		path = "/"
	}
	return StripV1Prefix(path) == "/responses/compact"
}

// JSONCloneValue deep-clones through a JSON round trip (mirrors structured
// clone usage in the codex normalizer).
// Migrated from gatewaydispatch util.go jsonCloneValue.
func JSONCloneValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil
	}
	return cloned
}
