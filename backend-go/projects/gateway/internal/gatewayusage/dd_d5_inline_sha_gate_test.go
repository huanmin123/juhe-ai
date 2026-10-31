package gatewayusage

// DD-D5 波次测试（2026-10-31）：捕获路径的内联 SHA-256 与 Node 镜像语义
// 背离——Node sha256BufferIfSmall（audit-capture.service.ts:820-821）超过
// auditInlineSha256MaxBytes（1MB）不内联计算，Go 端此前对上游请求体与客户
// 端请求体无上限全量计算。收口契约：>1MB body 跳过内联摘要（BodySha256
// 留空，保留到落库的由 blob 持久化侧补算同一摘要），≤1MB 照旧内联。
//
// 与 L2 负面缓存指纹的交互（钉住）：≤1MB 时 ClientRequestBodySha256 与
// L2 侧自算 fallback（BodySha256Hex）同值——L2 键对合格 body 形状不变；
// >1MB 时 ClientRequestBodySha256 返回空串，而 L2 调用方
// （cmd/juhe-ai-gateway requestNegativeCacheKey）本就以同一 1MB 上限把超限
// body 排除在缓存之外，不会以空串哈希互撞。

import (
	"bytes"
	"testing"
)

func dd5Settings() AuditLogSettings {
	settings := auditSettings(true, 1) // 采样率 1：成功捕获恒选中
	settings.SuccessFullBodyLimitBytes = 4 * 1024 * 1024
	settings.ProblemFullBodyLimitBytes = 8 * 1024 * 1024
	return settings
}

func dd5Capture(rawBody []byte) *AuditCaptureContext {
	return captureInput(nil, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: dd5Settings()}
		input.RawBody = rawBody
		input.RequestHeaders = map[string]any{"content-type": "application/json"}
	})
}

// TestDD5ClientRequestBodySha256Gate 锁定客户端请求体门：>1MB 不内联
// （BodySha256 留空、RawBodySizeBytes 保留），≤1MB 内联且与 L2 自算同值。
func TestDD5ClientRequestBodySha256Gate(t *testing.T) {
	oversize := bytes.Repeat([]byte("a"), AuditInlineSha256MaxBytes+1)
	oversizeCapture := dd5Capture(oversize)
	if got := oversizeCapture.ClientRequestBodySha256(); got != "" {
		t.Fatalf("超 1MB 客户端请求体不得内联计算 SHA-256，实际 %q", got)
	}
	payload, ok := dd5FindPayload(oversizeCapture, AuditPartClientRequest)
	if !ok {
		t.Fatal("超限 body 仍须保留 client_request payload（仅跳过内联摘要）")
	}
	if payload.BodySha256 != "" {
		t.Fatalf("超限 payload BodySha256 必须为空，实际 %q", payload.BodySha256)
	}
	if payload.RawBodySizeBytes == nil || *payload.RawBodySizeBytes != len(oversize) {
		t.Fatalf("超限 payload 必须保留原始大小: %+v", payload.RawBodySizeBytes)
	}
	if !payload.HasBody || len(payload.Body) != len(oversize) {
		t.Fatalf("超限 body 本体必须原样保留: hasBody=%v len=%d", payload.HasBody, len(payload.Body))
	}

	inline := bytes.Repeat([]byte("b"), 1024)
	inlineCapture := dd5Capture(inline)
	want := BodySha256Hex(inline)
	if got := inlineCapture.ClientRequestBodySha256(); got != want {
		t.Fatalf("≤1MB 客户端请求体必须内联计算且与 L2 自算同值: got=%q want=%q", got, want)
	}
}

// TestDD5UpstreamAttemptBodyShaGate 锁定上游请求体门：>1MB 跳过内联，
// ≤1MB 内联摘要与 sha256Hex 一致。
func TestDD5UpstreamAttemptBodyShaGate(t *testing.T) {
	oversize := bytes.Repeat([]byte("u"), AuditInlineSha256MaxBytes+1)
	oversizeCapture := dd5Capture(nil)
	oversizeCapture.StartAttempt(StartAttemptInput{
		Account:      UsageModelAccount{ID: "acc-dd5", ProviderCode: OpenAIProtocolCode},
		AttemptIndex: 0,
		UpstreamURL:  "http://upstream.invalid/v1/chat/completions",
		Method:       "POST",
		Body:         oversize,
		HasBody:      true,
	})
	payload, ok := dd5FindPayload(oversizeCapture, AuditPartUpstreamRequest)
	if !ok {
		t.Fatal("超限上游请求体仍须保留 upstream_request payload")
	}
	if payload.BodySha256 != "" {
		t.Fatalf("超 1MB 上游请求体不得内联计算 SHA-256，实际 %q", payload.BodySha256)
	}
	if payload.RawBodySizeBytes == nil || *payload.RawBodySizeBytes != len(oversize) {
		t.Fatalf("超限 attempt payload 必须保留原始大小: %+v", payload.RawBodySizeBytes)
	}

	inline := bytes.Repeat([]byte("v"), 2048)
	inlineCapture := dd5Capture(nil)
	inlineCapture.StartAttempt(StartAttemptInput{
		Account:      UsageModelAccount{ID: "acc-dd5", ProviderCode: OpenAIProtocolCode},
		AttemptIndex: 0,
		UpstreamURL:  "http://upstream.invalid/v1/chat/completions",
		Method:       "POST",
		Body:         inline,
		HasBody:      true,
	})
	payload, ok = dd5FindPayload(inlineCapture, AuditPartUpstreamRequest)
	if !ok {
		t.Fatal("≤1MB 上游请求体必须保留 payload")
	}
	if want := BodySha256Hex(inline); payload.BodySha256 != want {
		t.Fatalf("≤1MB 上游请求体必须内联计算: got=%q want=%q", payload.BodySha256, want)
	}
}

// dd5FindPayload 取第一个指定 partType 的 payload（测试辅助，锁内读取）。
func dd5FindPayload(capture *AuditCaptureContext, partType AuditPayloadPartType) (AuditLogPayloadInput, bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for index := range capture.payloads {
		if capture.payloads[index].PartType == partType {
			return capture.payloads[index], true
		}
	}
	return AuditLogPayloadInput{}, false
}
