package auditlog

import "testing"

// TestRequestFingerprintMatchesAuditDerivation：导出的请求指纹构造与审计
// derivedErrorGroup 的 requestFingerprint 分量逐字节同源（同一输入同键），
// L2 负面缓存与审计错误分组的 request_fingerprint 列可对照排障。
func TestRequestFingerprintMatchesAuditDerivation(t *testing.T) {
	code := 400
	input := AuditLogInput{
		CreatedAt:       "2026-10-25T00:00:00Z",
		Method:          "POST",
		Path:            "/v1/chat/completions",
		Model:           "gpt-test",
		Stream:          &[]bool{true}[0],
		SystemAccountID: "sys_1",
		APIKeyID:        "key_1",
		TrafficSource:   TrafficSourceGateway,
		FinalStatusCode: &code,
	}
	derived, err := derivedErrorGroup(input, []preparedPayload{})
	if err != nil {
		t.Fatalf("derive error group: %v", err)
	}
	fingerprint := RequestFingerprint(RequestFingerprintInput{
		Method: input.Method,
		Path:   input.Path,
		Model:  input.Model,
		Stream: *input.Stream,
	})
	if derived.requestFingerprint != fingerprint {
		t.Fatalf("audit-derived fingerprint = %q, exported constructor = %q", derived.requestFingerprint, fingerprint)
	}
	if len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q want 64-hex sha256", fingerprint)
	}
	// 空流（nil Stream）与 false 同值聚合；bodySha256 缺省为空串参与聚合。
	input.Stream = nil
	derivedNilStream, err := derivedErrorGroup(input, []preparedPayload{})
	if err != nil {
		t.Fatalf("derive with nil stream: %v", err)
	}
	if derivedNilStream.requestFingerprint != RequestFingerprint(RequestFingerprintInput{Method: input.Method, Path: input.Path, Model: input.Model, Stream: false}) {
		t.Fatal("nil stream must aggregate identically to false")
	}
}
