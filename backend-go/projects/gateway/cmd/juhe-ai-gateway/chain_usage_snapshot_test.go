package main

// finalize 侧完成尝试快照记账回归（2026-10-03 BUG-0269 残余缺口收口）：
//
//   - finalization 失败终态行（流失败/下游中断/非流式失败/inspection 失败）
//     的 request_snapshot_json / response_snapshot_json 必须落库：响应管线
//     7 个生产调用点已构造 CompletedAttemptInput.RequestSnapshot /
//     ResponseSnapshot 并送达 sink，chainFinalizationUsage 必须消费；
//   - 快照为失败记录专属契约（docs/functions/核心功能设计.md:568-569）：
//     成功行输入恒 nil（响应管线三态门控已保证），记账侧不得自造成功行快照；
//   - probe 流量置 nil（Node records.ts:375-376 usageRecordSnapshot 门控；
//     gatewayusage 同名辅助未导出，chain_usage.go 走同语义私有 helper）；
//   - 64KB 截断不本地做：直投 recorder 入口第一步 NormalizeUsageRecordInput
//     → BoundUsageRecordSnapshot（chain_usage.go:123 →
//     internal/gatewayusage/records.go:143-144）。
//
// 断言走 gatewayusage.MemoryUsageRecorder（与生产 spooledUsageRecorder 同一
// NormalizeUsageRecordInput 入口），normalize 后快照为 *OrderedObject。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// snapshotFailureInput 构造失败终态尝试输入：双快照齐备，request 侧带
// bodyOmission omission 形态（usageRequestSnapshotWithOmission 输出形状）。
func snapshotFailureInput() gatewayresponse.CompletedAttemptInput {
	return gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID:       "trace-snapshot-failure",
			TrafficSource: gatewaypreauth.TrafficSourceGateway,
		},
		Account: gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{
			ID: "acc-snapshot", ProviderCode: "openai",
		}},
		Success:        false,
		StatusCode:     502,
		Stream:         true,
		RequestedModel: "glm-5.3-flash",
		ErrorCode:      "upstream_protocol_failure",
		ErrorMessage:   "上游响应违反请求协议终态",
		RequestSnapshot: &gatewayresponse.UsageRequestSnapshotView{
			Method:      "POST",
			Path:        "/v1/chat/completions",
			TraceID:     "trace-snapshot-failure",
			OmittedBody: true,
			BodyOmission: &gatewayresponse.StreamBodyOmissionSummary{
				Reason:             "image_stream_payload",
				Message:            "图片流正文不落库",
				TotalUpstreamBytes: 1024,
			},
		},
		ResponseSnapshot: &gatewayresponse.UsageResponseSnapshotView{
			UpstreamURL:  "https://upstream.example/v1/chat/completions",
			StatusCode:   502,
			BodyText:     "data: [DONE]",
			ErrorMessage: "stream interrupted",
		},
	}
}

// snapshotObject 断言 normalize 后快照为 *OrderedObject（BoundUsageRecordSnapshot
// 对结构体快照的输出形状）。
func snapshotObject(t *testing.T, value any) *gatewayusage.OrderedObject {
	t.Helper()
	object, ok := value.(*gatewayusage.OrderedObject)
	if !ok {
		t.Fatalf("快照类型 = %T, want *gatewayusage.OrderedObject", value)
	}
	return object
}

// TestChainFinalizationUsageFailureSnapshotPassThrough 是字段不消费时必红的
// 守卫：失败终态行双快照透传，request 侧保留 bodyOmission omission 形态，
// response 侧保留上游观察事实。
func TestChainFinalizationUsageFailureSnapshotPassThrough(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder}
	usage.RecordCompletedUpstreamAttempt(snapshotFailureInput())

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.RequestSnapshot == nil {
		t.Fatalf("失败终态行 RequestSnapshot = nil, want 快照落库")
	}
	if record.ResponseSnapshot == nil {
		t.Fatalf("失败终态行 ResponseSnapshot = nil, want 快照落库")
	}
	request := snapshotObject(t, record.RequestSnapshot)
	if request.Get("Method") != "POST" {
		t.Errorf("request.Method = %v, want POST", request.Get("Method"))
	}
	if request.Get("Path") != "/v1/chat/completions" {
		t.Errorf("request.Path = %v, want /v1/chat/completions", request.Get("Path"))
	}
	if request.Get("OmittedBody") != true {
		t.Errorf("request.OmittedBody = %v, want true", request.Get("OmittedBody"))
	}
	omission := snapshotObject(t, request.Get("BodyOmission"))
	if omission.Get("Reason") != "image_stream_payload" {
		t.Errorf("bodyOmission.Reason = %v, want image_stream_payload", omission.Get("Reason"))
	}
	if omission.Get("TotalUpstreamBytes") != int64(1024) {
		t.Errorf("bodyOmission.TotalUpstreamBytes = %v, want 1024", omission.Get("TotalUpstreamBytes"))
	}
	response := snapshotObject(t, record.ResponseSnapshot)
	if response.Get("statusCode") != 502 {
		t.Errorf("response.statusCode = %v, want 502", response.Get("statusCode"))
	}
	if response.Get("bodyText") != "data: [DONE]" {
		t.Errorf("response.bodyText = %v, want data: [DONE]", response.Get("bodyText"))
	}
	if response.Get("errorMessage") != "stream interrupted" {
		t.Errorf("response.errorMessage = %v, want stream interrupted", response.Get("errorMessage"))
	}
}

// TestChainFinalizationUsageSuccessSnapshotAlwaysEmpty 钉住"快照为失败记录
// 专属"契约：生产构造点三态门控（nonstream.go:1038-1053 与 Node 同构）保证
// 成功行输入恒 nil，记账侧不得为成功行自造快照。
func TestChainFinalizationUsageSuccessSnapshotAlwaysEmpty(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder}
	input := snapshotFailureInput()
	input.Success = true
	input.StatusCode = 200
	input.ErrorCode = ""
	input.ErrorMessage = ""
	input.RequestSnapshot = nil
	input.ResponseSnapshot = nil
	usage.RecordCompletedUpstreamAttempt(input)

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].RequestSnapshot != nil {
		t.Errorf("成功行 RequestSnapshot = %T, want nil（成功行不写快照）", records[0].RequestSnapshot)
	}
	if records[0].ResponseSnapshot != nil {
		t.Errorf("成功行 ResponseSnapshot = %T, want nil（成功行不写快照）", records[0].ResponseSnapshot)
	}
}

// TestChainFinalizationUsageProbeTrafficSnapshotDropped 钉住 probe 门控边界：
// 三个账号探针来源双快照置 nil；manual_account_test 属诊断而非探针，快照保留
// （对齐 gatewayusage.IsAccountProbeTrafficSource / Node isAccountProbeTrafficSource）。
func TestChainFinalizationUsageProbeTrafficSnapshotDropped(t *testing.T) {
	cases := []struct {
		name          string
		trafficSource string
		wantSnapshots bool
	}{
		{"account_health_check 探针置 nil", gatewayusage.TrafficSourceAccountHealthCheck, false},
		{"runtime_recovery_probe 探针置 nil", gatewayusage.TrafficSourceRuntimeRecoveryProbe, false},
		{"cooldown_retest 探针置 nil", gatewayusage.TrafficSourceCooldownRetest, false},
		{"manual_account_test 非探针保留", gatewayusage.TrafficSourceManualAccountTest, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
			usage := chainFinalizationUsage{recorder: recorder}
			input := snapshotFailureInput()
			input.UsageContext.TrafficSource = tc.trafficSource
			usage.RecordCompletedUpstreamAttempt(input)

			records := recorder.Records()
			if len(records) != 1 {
				t.Fatalf("records = %d, want 1", len(records))
			}
			got := records[0].RequestSnapshot != nil || records[0].ResponseSnapshot != nil
			if got != tc.wantSnapshots {
				t.Fatalf("trafficSource=%s 快照保留 = %v, want %v", tc.trafficSource, got, tc.wantSnapshots)
			}
		})
	}
}

// TestChainFinalizationUsageFailureSnapshotBoundedThroughRecorder 验证直投路径
// 的 64KB 截断链：记账侧不本地截断，超限快照经 recorder 入口
// NormalizeUsageRecordInput → BoundUsageRecordSnapshot 收紧（与生产
// spooledUsageRecorder 同一 normalize 函数）。
func TestChainFinalizationUsageFailureSnapshotBoundedThroughRecorder(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	usage := chainFinalizationUsage{recorder: recorder}
	input := snapshotFailureInput()
	input.ResponseSnapshot.BodyText = strings.Repeat("x", 100*1024)
	usage.RecordCompletedUpstreamAttempt(input)

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	response := snapshotObject(t, records[0].ResponseSnapshot)
	bodyText, ok := response.Get("bodyText").(string)
	if !ok {
		t.Fatalf("response.bodyText 类型 = %T, want string", response.Get("bodyText"))
	}
	if !strings.Contains(bodyText, "...[truncated ") {
		t.Errorf("超限 BodyText 未带截断标记: %q", bodyText[len(bodyText)-40:])
	}
	// 单字符串预算 16KB（usageSnapshotMaxStringBytes）+ 截断后缀。
	if len(bodyText) > 17*1024 {
		t.Errorf("截断后 BodyText = %d bytes, want ≤ 16KB 预算", len(bodyText))
	}
}
