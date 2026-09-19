package gatewaydispatch

import (
	"encoding/json"
)

// Serialized JSON body helpers, migrated from
// request/serialized-json-body.ts. The Node WeakMap keyed by Buffer identity
// becomes a bounded registry keyed by body content; the sanitized flag only
// needs to live for the duration of one dispatch.
//
// REFACTOR-0006 阶段 A：codex 清洗标记（MarkGatewayCodexHistorySanitized /
// IsGatewayCodexHistorySanitized 与 registry）随 oauth/codex 族迁入
// gatewayoauthcodex，本包经 gatewayoauthcodex_bridge.go 转发保持消费点零改动。

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
