package gatewayusage

// 审计收录范围门控（2026-10-16 收窄）测试：零上游尝试的网关拦截面 finalize
// 不投递审计；账户级连接前失败（RecordFailedDispatchAttempt）仍保留。

import "testing"

func TestAuditFinalizeDropsZeroAttemptRejections(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(true, 1)}
	})
	// 网关拦截面形态：无任何上游账户尝试直接失败（认证/权限/限流/无可用账户等）。
	capture.Finalize(FinalizeAuditInput{
		Outcome: AuditOutcomeGatewayFailed, Success: false,
		StatusCode:   intPointer(401),
		ErrorPhase:   "auth",
		ErrorCode:    "invalid_request_error",
		ErrorMessage: "API Key 无效",
	})
	if got := len(dispatcher.all()); got != 0 {
		t.Fatalf("零上游尝试不得落审计，实际投递 %d 条", got)
	}
}

func TestAuditFinalizeKeepsDispatchAttemptFailures(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true,
			SuccessSampleRate: 1, ActiveCaptureMaxBytes: 64 * 1024,
			SuccessFullBodyLimitBytes: 64 * 1024, ProblemFullBodyLimitBytes: 64 * 1024,
		}}
	})
	// 账户级连接前失败（代理不可用）：计入 attempts，必须保留。
	capture.RecordFailedDispatchAttempt(RecordFailedDispatchAttemptInput{
		Account:      testAccount(),
		AttemptIndex: 0,
		UpstreamURL:  "proxy:configured",
		Method:       "POST",
		ErrorPhase:   "dispatch",
		ErrorCode:    "proxy_unavailable",
		ErrorMessage: "账户绑定的代理不可用",
	})
	capture.Finalize(FinalizeAuditInput{
		Outcome: AuditOutcomeGatewayFailed, Success: false,
		StatusCode:   intPointer(502),
		ErrorPhase:   "dispatch",
		ErrorCode:    "proxy_unavailable",
		ErrorMessage: "账户绑定的代理不可用",
	})
	final := dispatcher.all()
	if len(final) != 1 {
		t.Fatalf("账户级失败必须落审计，实际投递 %d 条", len(final))
	}
	if len(final[0].Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(final[0].Attempts))
	}
}
