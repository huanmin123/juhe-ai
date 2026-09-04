package gatewaycircuit

import (
	"strings"
	"testing"
)

func TestNextAccountPrecheckProbeAtMs(t *testing.T) {
	random := func() float64 { return 0.5 }
	// More attempts remain: schedule one probe interval ahead with jitter.
	at, ok := NextAccountPrecheckProbeAtMs(PrecheckProbeInput{
		AttemptCount: 1, MaxAttempts: 3, StartedAtMs: 0, NowMs: 10_000,
	}, random)
	if !ok || at <= 10_000+AccountPrecheckProbeIntervalMs-30_000+1 {
		t.Fatalf("probe at = (%d, %v)", at, ok)
	}
	// The deterministic random 0.5 yields the exact offset 1ms.
	if at != 10_000+AccountPrecheckProbeIntervalMs+1 {
		t.Fatalf("jittered probe at = %d, want %d", at, 10_000+AccountPrecheckProbeIntervalMs+1)
	}
	// Max attempts reached before the observation floor: wait until the
	// confirmation point.
	at, ok = NextAccountPrecheckProbeAtMs(PrecheckProbeInput{
		AttemptCount: 3, MaxAttempts: 3, StartedAtMs: 0, NowMs: 60_000,
	}, random)
	if !ok || at != 60_000+AccountPrecheckMinimumObservationMs-60_000+1 {
		t.Fatalf("confirmation wait = (%d, %v)", at, ok)
	}
	// Past the observation floor no further probe is scheduled.
	if at, ok := NextAccountPrecheckProbeAtMs(PrecheckProbeInput{
		AttemptCount: 3, MaxAttempts: 3, StartedAtMs: 0, NowMs: 300_000,
	}, random); ok {
		t.Fatalf("past the floor a probe must not be scheduled, got %d", at)
	}
}

func TestPrecheckSummaryMapper(t *testing.T) {
	account := testAccount()
	account.Name = "acct"
	account.Status = "active"
	account.SystemAccountID = "sys-root"
	account.AccountAccessType = "owner"
	account.SupportedModels = []string{"gpt-4o"}
	account.CooldownUntil = strPtr("2026-01-01T00:00:00Z")
	account.LastErrorMessage = strPtr("boom")
	account.StreamFailureCount = 2
	account.StreamFailureWindowStartedAt = strPtr("2026-01-01T01:00:00Z")
	account.Credentials = map[string]any{"api_key": "sk"}

	mapper := &PrecheckSummaryMapper{}
	summary, err := mapper.MapFromGatewayPrecheckAccount(account, PrecheckSummaryContext{GroupID: "grp"})
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if summary.SystemAccountID != "sys-root" || summary.BoundGroupID != "grp" || summary.AccessType != "owner" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ProtocolCode != "openai" || summary.ProtocolVersion != "v1" {
		t.Fatalf("protocol = (%s, %s)", summary.ProtocolCode, summary.ProtocolVersion)
	}
	if !summary.Permissions.CanUse || summary.Permissions.CanEdit {
		t.Fatalf("permissions = %+v", summary.Permissions)
	}
	if summary.Schedulable != true || summary.CooldownUntil != "2026-01-01T00:00:00Z" {
		t.Fatalf("summary fields = %+v", summary)
	}

	// Authorized accounts resolve the binding system account and bound group.
	authorized := account
	authorized.AccountAccessType = "account_authorized"
	authorized.BindingSystemAccountID = strPtr("sys-binding")
	authorized.BoundGroupID = strPtr("grp-bound")
	authorized.AccountAuthorizationID = strPtr("auth-1")
	authorized.ProtocolCode = ""
	authorized.ProtocolVersion = ""
	authorized.ProviderProtocolProfileID = "default_anthropic_profile"
	authorized.SystemAccountID = ""
	summary, err = mapper.MapFromGatewayPrecheckAccount(authorized, PrecheckSummaryContext{GroupID: "grp", SystemAccountID: "sys-ctx"})
	if err != nil {
		t.Fatalf("authorized map: %v", err)
	}
	if summary.SystemAccountID != "sys-binding" || summary.BoundGroupID != "grp-bound" || summary.BindingSystemAccountID != "sys-binding" {
		t.Fatalf("authorized summary = %+v", summary)
	}
	if summary.AccessType != "authorized" {
		t.Fatalf("access type = %s", summary.AccessType)
	}
	if summary.ProtocolCode != AnthropicProtocolCode || summary.ProtocolVersion != AnthropicProtocolVersion {
		t.Fatalf("profile fallback protocol = (%s, %s)", summary.ProtocolCode, summary.ProtocolVersion)
	}

	// Missing binding surfaces the Node error copy.
	broken := authorized
	broken.BindingSystemAccountID = strPtr(" ")
	if _, err := mapper.MapFromGatewayPrecheckAccount(broken, PrecheckSummaryContext{GroupID: "grp"}); err == nil ||
		err.Error() != "授权账户缺少绑定系统账户，无法构造测试摘要" {
		t.Fatalf("missing binding error = %v", err)
	}
	// Missing bound group surfaces the Node error copy.
	broken2 := authorized
	broken2.BindingSystemAccountID = strPtr("sys-binding")
	broken2.BoundGroupID = strPtr("")
	if _, err := mapper.MapFromGatewayPrecheckAccount(broken2, PrecheckSummaryContext{GroupID: "grp"}); err == nil ||
		err.Error() != "授权账户缺少绑定分组，无法构造测试摘要" {
		t.Fatalf("missing group error = %v", err)
	}
	// Missing system account on a plain account falls back to the context.
	plain := account
	plain.SystemAccountID = ""
	summary, err = mapper.MapFromGatewayPrecheckAccount(plain, PrecheckSummaryContext{GroupID: "grp", SystemAccountID: "sys-ctx"})
	if err != nil || summary.SystemAccountID != "sys-ctx" {
		t.Fatalf("context fallback = (%+v, %v)", summary, err)
	}
	// ...and errors when the context is empty too.
	if _, err := mapper.MapFromGatewayPrecheckAccount(plain, PrecheckSummaryContext{GroupID: "grp"}); err == nil ||
		err.Error() != "账户缺少系统账户，无法构造测试摘要" {
		t.Fatalf("missing system account error = %v", err)
	}

	// The effective availability hook receives the assembled summary.
	decorated := &PrecheckSummaryMapper{
		WithEffectiveAvailability: func(summary PrecheckAccountSummary, nowMs int64) (PrecheckAccountSummary, error) {
			if summary.ID != account.ID || nowMs != 42 {
				t.Fatalf("hook input = (%+v, %d)", summary, nowMs)
			}
			summary.Schedulable = false
			return summary, nil
		},
		Now: func() int64 { return 42 },
	}
	hooked, err := decorated.MapFromGatewayPrecheckAccount(account, PrecheckSummaryContext{GroupID: "grp"})
	if err != nil || hooked.Schedulable {
		t.Fatalf("hooked summary = (%+v, %v)", hooked, err)
	}
	// Gemini profile fallbacks resolve through substring matching.
	gemini := account
	gemini.ProtocolCode = " "
	gemini.ProtocolVersion = " "
	gemini.ProviderProtocolProfileID = "default_gemini_native"
	summary, err = mapper.MapFromGatewayPrecheckAccount(gemini, PrecheckSummaryContext{GroupID: "g"})
	if err != nil || summary.ProtocolCode != GeminiProtocolCode || !strings.HasPrefix(summary.ProtocolVersion, "v1") {
		t.Fatalf("gemini fallback = (%s, %s, %v)", summary.ProtocolCode, summary.ProtocolVersion, err)
	}
}
