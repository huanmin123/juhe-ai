package gatewayusage

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
)

// E2E-FINDING #11（F11）：chain_ports.go 类型桥 auditUsageDispatcher 用
// JSON Marshal→Unmarshal 把 gatewayusage.AuditLogInput 转成
// auditlog.AuditLogInput，AuditLogPayloadInput.MarshalJSON 必须让该往返
// 对 payload body 无损——Body 以 Buffer base64 形式过桥，HasBody 留给
// 接收侧 PayloadBody.Present 推导。
func TestAuditLogPayloadInputMarshalBodyBufferForm(t *testing.T) {
	body := []byte(`{"type":"gateway_metadata","label":"canary"}`)
	encoded, err := json.Marshal(AuditLogPayloadInput{
		PartType:    AuditPartGatewayMetadata,
		ContentType: "application/json; audit=gateway-metadata",
		Body:        body,
		HasBody:     true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	wantBase64 := base64.StdEncoding.EncodeToString(body)
	if !strings.Contains(text, `"partType":"gateway_metadata"`) {
		t.Fatalf("partType should keep its json tag serialization: %s", text)
	}
	if !strings.Contains(text, `"body":{"type":"Buffer","base64":`+jsonString(t, wantBase64)+`}`) {
		t.Fatalf("body should marshal to the Node Buffer base64 wire form: %s", text)
	}
	if strings.Contains(text, "hasBody") {
		t.Fatalf("HasBody must stay unserialized (receiver derives Present): %s", text)
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal string %q: %v", value, err)
	}
	return string(encoded)
}

// 接收侧等价：Marshal 输出必须被 auditlog.PayloadBody.UnmarshalJSON 的
// base64 分支无损接收（StdEncoding 解码回原字节并置 Present）。
func TestAuditLogPayloadInputMarshalFeedsAuditlogPayloadBody(t *testing.T) {
	body := []byte("hi")
	encoded, err := json.Marshal(AuditLogPayloadInput{PartType: AuditPartGatewayMetadata, Body: body, HasBody: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	var decoded auditlog.PayloadBody
	if err := json.Unmarshal(raw["body"], &decoded); err != nil {
		t.Fatalf("auditlog.PayloadBody should accept the Buffer base64 form: %v", err)
	}
	if !decoded.Present || string(decoded.Bytes) != string(body) {
		t.Fatalf("round-trip mismatch: present=%v bytes=%q", decoded.Present, decoded.Bytes)
	}
}

// 空 Body：不输出 body 字段，接收侧保持零值（Present=false），
// 与 UnmarshalJSON 的 null/缺失分支形态对齐。
func TestAuditLogPayloadInputMarshalEmptyBodyOmitsField(t *testing.T) {
	encoded, err := json.Marshal(AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "body") {
		t.Fatalf("empty body should not emit a body key: %s", encoded)
	}
}

// 桥语义整体回归：gatewayusage.AuditLogInput → JSON →
// auditlog.AuditLogInput 的 payload body 不再丢失。
func TestAuditLogInputBridgeRoundTripKeepsPayloadBody(t *testing.T) {
	body := gatewayMetadataBodyForTest("canary")
	input := AuditLogInput{
		TraceID: "trace-f11",
		Method:  "POST",
		Path:    "/v1/chat/completions",
		Payloads: []AuditLogPayloadInput{{
			PartType:    AuditPartGatewayMetadata,
			ContentType: "application/json; audit=gateway-metadata",
			Body:        body,
			HasBody:     true,
		}},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded auditlog.AuditLogInput
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal into auditlog.AuditLogInput: %v", err)
	}
	if len(decoded.Payloads) != 1 {
		t.Fatalf("payloads lost in bridge round-trip: %+v", decoded.Payloads)
	}
	bridged := decoded.Payloads[0].Body
	if !bridged.Present || string(bridged.Bytes) != string(body) {
		t.Fatalf("payload body lost in bridge round-trip: present=%v bytes=%q", bridged.Present, bridged.Bytes)
	}
}

func gatewayMetadataBodyForTest(label string) []byte {
	return []byte(`{"type":"gateway_metadata","label":"` + label + `","metadata":{}}`)
}
