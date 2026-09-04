package gatewaydispatch

import (
	"encoding/json"
	"sync"
)

// Serialized JSON body helpers, migrated from
// request/serialized-json-body.ts. The Node WeakMap keyed by Buffer identity
// becomes a bounded registry keyed by body content; the sanitized flag only
// needs to live for the duration of one dispatch.

const gatewaySerializedFlagCapacity = 8192

var (
	gatewaySerializedFlagsMu    sync.Mutex
	gatewayCodexSanitizedBodies = make(map[string]struct{})
)

// MarkGatewayCodexHistorySanitized mirrors markGatewayCodexHistorySanitized.
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

// GatewaySerializedJSONObject mirrors gatewaySerializedJsonObject: the
// parsed object associated with the raw body bytes, when already parsed.
func GatewaySerializedJSONObject(body []byte) map[string]any {
	object, ok := decodeJSONObject(body)
	if !ok {
		return nil
	}
	return object
}

// SerializeGatewayJSONObject mirrors serializeGatewayJsonObject.
func SerializeGatewayJSONObject(body map[string]any) []byte {
	serialized, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return serialized
}
