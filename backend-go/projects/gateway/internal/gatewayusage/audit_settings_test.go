package gatewayusage

import (
	"encoding/json"
	"strings"
	"testing"
)

// 审计捕获上限的设置回落契约（docs/functions/原始审计日志设计.md
// fixedAuditLogSettings 表）：设置源未提供 limit（<=0）时回落默认，
// 不能把「设置缺失」当成「显式 0」把全部载荷判 overflow。
func TestResolveAuditCaptureLimitsFallback(t *testing.T) {
	测试用例 := []struct {
		名称       string
		设置       AuditLogSettings
		期望活跃上限   int
		期望成功正文上限 int
		期望问题正文上限 int
	}{
		{
			名称:       "全部缺失时三个上限都回落设计文档默认",
			设置:       AuditLogSettings{Enabled: true},
			期望活跃上限:   DefaultAuditCaptureHardLimitBytes,
			期望成功正文上限: DefaultAuditSuccessFullBodyLimitBytes,
			期望问题正文上限: DefaultAuditProblemFullBodyLimitBytes,
		},
		{
			名称:       "负值视同缺失回落默认",
			设置:       AuditLogSettings{Enabled: true, ActiveCaptureMaxBytes: -1, SuccessFullBodyLimitBytes: -1, ProblemFullBodyLimitBytes: -1},
			期望活跃上限:   DefaultAuditCaptureHardLimitBytes,
			期望成功正文上限: DefaultAuditSuccessFullBodyLimitBytes,
			期望问题正文上限: DefaultAuditProblemFullBodyLimitBytes,
		},
		{
			名称:       "显式提供的合法上限原样保留",
			设置:       AuditLogSettings{Enabled: true, ActiveCaptureMaxBytes: 1024, SuccessFullBodyLimitBytes: 4096, ProblemFullBodyLimitBytes: 8192},
			期望活跃上限:   1024,
			期望成功正文上限: 4096,
			期望问题正文上限: 8192,
		},
		{
			名称:       "超过硬上限的活跃捕获被钳制到 64MB",
			设置:       AuditLogSettings{Enabled: true, ActiveCaptureMaxBytes: DefaultAuditCaptureHardLimitBytes * 2},
			期望活跃上限:   DefaultAuditCaptureHardLimitBytes,
			期望成功正文上限: DefaultAuditSuccessFullBodyLimitBytes,
			期望问题正文上限: DefaultAuditProblemFullBodyLimitBytes,
		},
	}
	for _, 用例 := range 测试用例 {
		t.Run(用例.名称, func(t *testing.T) {
			if got := ResolveAuditCaptureLimits(用例.设置); got != 用例.期望活跃上限 {
				t.Fatalf("活跃捕获上限 = %d，期望 %d", got, 用例.期望活跃上限)
			}
			if got := ResolveAuditSuccessFullBodyLimitBytes(用例.设置); got != 用例.期望成功正文上限 {
				t.Fatalf("成功正文保留上限 = %d，期望 %d", got, 用例.期望成功正文上限)
			}
			if got := ResolveAuditProblemFullBodyLimitBytes(用例.设置); got != 用例.期望问题正文上限 {
				t.Fatalf("问题正文保留上限 = %d，期望 %d", got, 用例.期望问题正文上限)
			}
		})
	}
}

// 生产设置源适配器只传递 Enabled 位、其余字段全零（组合根现状）。捕获上下文
// 在该设置下必须仍按默认上限保留载荷：请求级 gateway_metadata 事件（如
// same_account_retry_dispatch）不得被 active_capture_overflow 清空。
func TestAuditCaptureKeepsMetadataWhenSettingsMissing(t *testing.T) {
	ResetActiveAuditCaptureCountForTest()
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		// 复刻 auditSettingsSourceAdapter 现状：只有 Enabled=true。
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true, SuccessSampleRate: 1,
		}}
	})
	capture.AddGatewayMetadata("same_account_retry_dispatch", map[string]any{
		"accountId":   "acc_1",
		"retryNumber": 1,
	})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(500)}
	})
	dispatched := dispatcher.all()
	if len(dispatched) != 1 {
		t.Fatalf("派发审计数 = %d，期望 1", len(dispatched))
	}
	auditLog := dispatched[0]
	if auditLog.CaptureStatus == string(AuditCaptureOverflow) {
		t.Fatalf("捕获状态 = overflow，设置缺失不应把请求判为溢出：%+v", auditLog.Payloads)
	}
	var metadataLabels []string
	for _, payload := range auditLog.Payloads {
		if payload.PartType != AuditPartGatewayMetadata {
			continue
		}
		var decoded struct {
			Label string `json:"label"`
		}
		if err := json.Unmarshal(payload.Body, &decoded); err != nil {
			t.Fatalf("gateway_metadata 载荷解析失败：%v", err)
		}
		if strings.TrimSpace(decoded.Label) != "" {
			metadataLabels = append(metadataLabels, decoded.Label)
		}
	}
	if len(metadataLabels) != 1 || metadataLabels[0] != "same_account_retry_dispatch" {
		t.Fatalf("gateway_metadata label = %v，期望 [same_account_retry_dispatch]", metadataLabels)
	}
}
