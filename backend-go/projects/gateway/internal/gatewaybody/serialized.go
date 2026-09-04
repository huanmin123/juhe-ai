package gatewaybody

import (
	"bytes"
	"encoding/json"
)

// Serialized-body association, mirroring request/serialized-json-body.ts.
//
// Approved adaptation: Node keys the parsed object and the codex-history
// sanitized marker by Buffer identity in a WeakMap; Go has no weak references
// over slices, so the association is carried explicitly on *SerializedBody
// and threaded through Request.Serialized. Consumers obtain the same facts
// (raw bytes <-> parsed object <-> sanitized marker) without GC-identity
// lookups.
type SerializedBody struct {
	Raw    []byte
	Parsed map[string]any

	codexHistorySanitized bool
}

// SerializeGatewayJSONObject mirrors serializeGatewayJsonObject:
// JSON.stringify plus the parsed-object binding.
func SerializeGatewayJSONObject(body map[string]any) *SerializedBody {
	// json.Encoder with SetEscapeHTML(false) matches JSON.stringify for the
	// practical character set (Go still escapes U+2028/U+2029, which remain
	// valid JSON string content; a re-serialized body differs from Node only
	// in those exotic bytes). Key order is Go's sorted map order; Node
	// preserves insertion order — re-serialized rewrite bodies are therefore
	// semantically equal, not byte-equal, which is the documented Go-side
	// serialization adaptation.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		panic(err)
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte{0x0a})
	return &SerializedBody{Raw: raw, Parsed: body}
}

// BindGatewaySerializedJSONObject mirrors bindGatewaySerializedJsonObject.
func BindGatewaySerializedJSONObject(raw []byte, body map[string]any) *SerializedBody {
	return &SerializedBody{Raw: raw, Parsed: body}
}

// MarkCodexHistorySanitized mirrors markGatewayCodexHistorySanitized.
func (s *SerializedBody) MarkCodexHistorySanitized() *SerializedBody {
	s.codexHistorySanitized = true
	return s
}

// IsCodexHistorySanitized mirrors isGatewayCodexHistorySanitized.
func (s *SerializedBody) IsCodexHistorySanitized() bool {
	return s != nil && s.codexHistorySanitized
}
