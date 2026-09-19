package gatewayoauthcodex

import (
	"sync"
)

// Serialized codex history sanitization flag, migrated from
// request/serialized-json-body.ts (REFACTOR-0006 阶段 A 自 gatewaydispatch
// serialized.go 随 oauth/codex 族迁入). The Node WeakMap keyed by Buffer
// identity becomes a bounded registry keyed by body content; the sanitized
// flag only needs to live for the duration of one dispatch.

const gatewaySerializedFlagCapacity = 8192

var (
	gatewaySerializedFlagsMu    sync.Mutex
	gatewayCodexSanitizedBodies = make(map[string]struct{})
)

// markCodexHistorySanitizedLocked registers a sanitized body key.
func markCodexHistorySanitizedLocked(key string) {
	if len(gatewayCodexSanitizedBodies) >= gatewaySerializedFlagCapacity {
		// Drop arbitrary entries (the flag is request-scoped; capacity
		// pressure only appears under pathological fan-out).
		for existing := range gatewayCodexSanitizedBodies {
			delete(gatewayCodexSanitizedBodies, existing)
			break
		}
	}
	gatewayCodexSanitizedBodies[key] = struct{}{}
}

// MarkGatewayCodexHistorySanitized flags the serialized body so a later
// dispatch attempt of the same bytes skips re-sanitization.
func MarkGatewayCodexHistorySanitized(body []byte) []byte {
	key := serializedBodyKey(body)
	gatewaySerializedFlagsMu.Lock()
	markCodexHistorySanitizedLocked(key)
	gatewaySerializedFlagsMu.Unlock()
	return body
}

// IsGatewayCodexHistorySanitized mirrors isGatewayCodexHistorySanitized.
func IsGatewayCodexHistorySanitized(body []byte) bool {
	key := serializedBodyKey(body)
	gatewaySerializedFlagsMu.Lock()
	defer gatewaySerializedFlagsMu.Unlock()
	_, ok := gatewayCodexSanitizedBodies[key]
	return ok
}

func serializedBodyKey(body []byte) string {
	return string(body)
}
