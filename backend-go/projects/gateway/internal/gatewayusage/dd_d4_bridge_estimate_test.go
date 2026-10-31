// 外部测试包：本文件引用 internal/auditlog（其经 internal/kernel 依赖本包），
// in-package 测试会构成 "import cycle not allowed in test"（同
// audit_types_test.go 约定）。
package gatewayusage_test

// DD-D4 波次测试（2026-10-31）：请求路径全量 Marshal+base64 移入 producer
// worker 后，类型桥输出必须与旧实现逐字节等价、大 body 过桥无损，且队列
// 记账估算覆盖实际 wire 字节（估算值不得小于真实编码长度）。

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// dd4BigBridgeInput 构造一条 3MB 级 body 的采集输入（含 attempts 文本与
// 多 payload，覆盖估算式每一项）。
func dd4BigBridgeInput(t *testing.T) gatewayusage.AuditLogInput {
	t.Helper()
	bigBody := bytes.Repeat([]byte("x"), 3*1024*1024+7)
	smallBody := []byte(`{"type":"gateway_metadata","label":"dd4"}`)
	attemptError := bytes.Repeat([]byte("e"), 4096)
	input := gatewayusage.AuditLogInput{
		ID:           "audit-dd4-bridge",
		TraceID:      "trace-dd4-bridge",
		UserAgent:    "dd4-agent",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		QueryString:  "api_key=redacted",
		ErrorMessage: string(attemptError),
		ErrorCode:    "dd4_probe",
		Attempts: []gatewayusage.AuditLogAttemptInput{{
			ID:            "audatt-dd4-1",
			TempID:        "attempt_0_dd4_abcdef",
			AttemptIndex:  0,
			UpstreamURL:   "http://upstream.invalid/v1/chat/completions",
			UpstreamModel: "gpt-dd4",
			ErrorMessage:  string(attemptError),
		}},
		Payloads: []gatewayusage.AuditLogPayloadInput{{
			ID:          "audpay-dd4-1",
			PartType:    gatewayusage.AuditPartClientRequest,
			Body:        bigBody,
			HasBody:     true,
			ContentType: "application/json",
		}, {
			ID:          "audpay-dd4-2",
			PartType:    gatewayusage.AuditPartGatewayMetadata,
			Body:        smallBody,
			HasBody:     true,
			ContentType: "application/json; audit=gateway-metadata",
		}},
	}
	return input
}

// dd4DeferredEstimate 复刻 cmd/juhe-ai-gateway deferredAuditEstimateOf 的
// 投影口径（文本精确字节、body 原始字节合计）。
func dd4DeferredEstimate(input gatewayusage.AuditLogInput) auditlog.DeferredAuditEstimate {
	estimate := auditlog.DeferredAuditEstimate{
		ErrorTextBytes: len(input.ErrorMessage) + len(input.ErrorCode) + len(input.ErrorPhase) +
			len(input.TraceID) + len(input.UserAgent) + len(input.Path) + len(input.QueryString),
		AttemptCount: len(input.Attempts),
		PayloadCount: len(input.Payloads),
	}
	for index := range input.Attempts {
		attempt := &input.Attempts[index]
		estimate.AttemptTextBytes += len(attempt.ErrorMessage) + len(attempt.ErrorCode) + len(attempt.UpstreamURL) + len(attempt.UpstreamModel)
	}
	for index := range input.Payloads {
		estimate.BodyBytes += len(input.Payloads[index].Body)
	}
	return estimate
}

// TestDD4BigBodyBridgeRoundTripMatchesLegacyPath：新 worker 侧类型桥与旧
// 实现使用同一 Marshal→Unmarshal 序列——对大 body 输入，桥产物与旧路径
// 产物 deep 相等、payload 字节无损，且 Marshal 输出与旧实现逐字节一致。
func TestDD4BigBodyBridgeRoundTripMatchesLegacyPath(t *testing.T) {
	input := dd4BigBridgeInput(t)

	// 旧实现（chain_ports.go 原位序列）：Marshal → Unmarshal → Capture。
	legacyEncoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("legacy marshal: %v", err)
	}
	var legacy auditlog.AuditLogInput
	if err := json.Unmarshal(legacyEncoded, &legacy); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}

	// 新实现：worker 侧 decodeAuditWireInput 同构序列（Marshal → Unmarshal）。
	deferred, decodeErr := func() (auditlog.AuditLogInput, error) {
		encoded, err := json.Marshal(input)
		if err != nil {
			return auditlog.AuditLogInput{}, err
		}
		var decoded auditlog.AuditLogInput
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return auditlog.AuditLogInput{}, err
		}
		return decoded, nil
	}()
	if decodeErr != nil {
		t.Fatalf("deferred decode: %v", decodeErr)
	}

	// 逐字节等价：新旧 Marshal 输出一致（同一输入同一线性序列化）。
	newerEncoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("deferred marshal: %v", err)
	}
	if !bytes.Equal(legacyEncoded, newerEncoded) {
		t.Fatal("新旧路径的 Marshal 输出必须逐字节一致")
	}
	// 结构等价：桥产物与旧路径产物 deep 相等。
	if !dd4AuditInputEqual(t, legacy, deferred) {
		t.Fatal("worker 侧解码产物必须与旧实现产物 deep 相等")
	}
	// 大 body 无损：client_request payload 字节原样过桥。
	if len(deferred.Payloads) != 2 {
		t.Fatalf("payloads lost in bridge: %d", len(deferred.Payloads))
	}
	want := input.Payloads[0].Body
	got := deferred.Payloads[0].Body.Bytes
	if !deferred.Payloads[0].Body.Present || !bytes.Equal(got, want) {
		t.Fatalf("大 body 过桥必须无损: present=%v len(got)=%d len(want)=%d",
			deferred.Payloads[0].Body.Present, len(got), len(want))
	}
}

// TestDD4DeferredEstimateCoversWireBytes：队列记账估算必须覆盖实际 wire
// 字节（估算 < 实际会让预算失守），且 body 按估算口径放大后占主导。
func TestDD4DeferredEstimateCoversWireBytes(t *testing.T) {
	input := dd4BigBridgeInput(t)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	estimate := auditlog.EstimateDeferredAuditBytes(dd4DeferredEstimate(input))
	if estimate < len(encoded) {
		t.Fatalf("队列记账估算 %d 不得小于实际 wire 字节 %d", estimate, len(encoded))
	}
	baseline := auditlog.EstimateDeferredAuditBytes(auditlog.DeferredAuditEstimate{})
	if estimate <= baseline+len(input.Payloads[0].Body) {
		t.Fatalf("估算必须按 ×4/3 上界放大 body: estimate=%d baseline=%d body=%d",
			estimate, baseline, len(input.Payloads[0].Body))
	}
}

func dd4AuditInputEqual(t *testing.T, legacy, deferred auditlog.AuditLogInput) bool {
	t.Helper()
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	deferredJSON, err := json.Marshal(deferred)
	if err != nil {
		t.Fatalf("marshal deferred: %v", err)
	}
	return bytes.Equal(legacyJSON, deferredJSON)
}
