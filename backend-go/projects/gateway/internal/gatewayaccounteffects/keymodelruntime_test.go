package gatewayaccounteffects

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func testCapability() CapabilityKey {
	return CapabilityKey{
		CredentialSourceAccountID: "source-1",
		KeyFingerprint:            "fp-1",
		ClientModel:               "gpt-test",
		ClientEndpointFamily:      "chat_completions",
		FinalUpstreamModel:        "gpt-upstream",
		UpstreamEndpointMode:      "chat_json",
		DispatchRevision:          3,
	}
}

// TestCapabilityHashNodeCompat 锁定与 Node / jobs keymodelrecovery 一致的
// canonical JSON + sha256 契约。
func TestCapabilityHashNodeCompat(t *testing.T) {
	key := testCapability()
	hash, err := CapabilityHash(key)
	if err != nil {
		t.Fatal(err)
	}
	canonical := `{"clientEndpointFamily":"chat_completions","clientModel":"gpt-test","credentialSourceAccountId":"source-1","dispatchRevision":3,"finalUpstreamModel":"gpt-upstream","keyFingerprint":"fp-1","upstreamEndpointMode":"chat_json"}`
	digest := sha256.Sum256([]byte(canonical))
	if hash != hex.EncodeToString(digest[:]) {
		t.Fatalf("hash = %s, want %s", hash, hex.EncodeToString(digest[:]))
	}
	canonicalJSON, err := CanonicalCapabilityJSON(key)
	if err != nil || canonicalJSON != canonical {
		t.Fatalf("canonical JSON = %s err = %v", canonicalJSON, err)
	}
}

func TestNormalizeCapabilityKeyValidations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CapabilityKey)
		wantErr string
	}{
		{name: "缺少 credentialSourceAccountId", mutate: func(k *CapabilityKey) { k.CredentialSourceAccountID = " " }, wantErr: "CapabilityKey 缺少 credentialSourceAccountId"},
		{name: "缺少 keyFingerprint", mutate: func(k *CapabilityKey) { k.KeyFingerprint = "" }, wantErr: "CapabilityKey 缺少 keyFingerprint"},
		{name: "dispatchRevision 非正数", mutate: func(k *CapabilityKey) { k.DispatchRevision = 0 }, wantErr: "CapabilityKey dispatchRevision 必须是正整数"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := testCapability()
			tt.mutate(&key)
			_, err := NormalizeCapabilityKey(key)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
	normalized, err := NormalizeCapabilityKey(testCapability())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ClientModel != "gpt-test" {
		t.Fatal("normalized fields should be preserved")
	}
}

func TestKeyModelBackoffLadder(t *testing.T) {
	tests := []struct {
		attempt int
		want    int64
	}{
		{1, 5_000},
		{2, 15_000},
		{3, 60_000},
		{4, 300_000},
		{9, 300_000},
		{0, 5_000},
	}
	for _, tt := range tests {
		if got := KeyModelBackoffDelayMs(tt.attempt); got != tt.want {
			t.Fatalf("backoff(%d) = %d, want %d", tt.attempt, got, tt.want)
		}
	}
}

func TestSettleKeyModelRecoveryBranches(t *testing.T) {
	base := func(t *testing.T) KeyModelState {
		t.Helper()
		state, err := CreateKeyModelOpenState(testCapability(), 0)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase = KeyModelPhaseHalfOpen
		state.ProbeLease = &KeyModelProbeLease{LeaseID: "lease-1", LeaseUntilMs: 100_000, PriorSuccessCount: 0}
		return state
	}
	t.Run("generation 不匹配 stale", func(t *testing.T) {
		state := base(t)
		status, next := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 9, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: 50})
		if status != KeyModelMutationStale || next.Phase != KeyModelPhaseHalfOpen {
			t.Fatalf("status=%s phase=%s", status, next.Phase)
		}
	})
	t.Run("lease 不匹配 lease_mismatch", func(t *testing.T) {
		state := base(t)
		status, _ := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "other", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: 50})
		if status != KeyModelMutationLeaseMismatch {
			t.Fatalf("status = %s", status)
		}
	})
	t.Run("租约过期 stale", func(t *testing.T) {
		state := base(t)
		status, _ := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeUnknown, NowMs: 100_001})
		if status != KeyModelMutationStale {
			t.Fatalf("status = %s", status)
		}
	})
	t.Run("unknown 回 OPEN 且 10s 重试", func(t *testing.T) {
		state := base(t)
		status, next := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeUnknown, NowMs: 1_000})
		if status != KeyModelMutationApplied || next.Phase != KeyModelPhaseOpen || *next.RetryAtMs != 11_000 {
			t.Fatalf("status=%s phase=%s retry=%v", status, next.Phase, *next.RetryAtMs)
		}
	})
	t.Run("失败退避推进且清零成功计数", func(t *testing.T) {
		state := base(t)
		state.Phase = KeyModelPhaseHalfOpen
		state.RecoverySuccessCount = 2
		status, next := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeUpstreamNotComplete, NowMs: 1_000})
		if status != KeyModelMutationApplied || next.Phase != KeyModelPhaseOpen || next.BackoffAttempt != 2 || next.RecoverySuccessCount != 0 {
			t.Fatalf("status=%s phase=%s backoff=%d count=%d", status, next.Phase, next.BackoffAttempt, next.RecoverySuccessCount)
		}
		if *next.RetryAtMs != 16_000 {
			t.Fatalf("retryAt = %d, want 16000", *next.RetryAtMs)
		}
	})
	t.Run("三次成功窗口内闭合", func(t *testing.T) {
		state := base(t)
		state.Phase = KeyModelPhaseHalfOpen
		state.RecoverySuccessCount = 2
		state.LastRecoverySuccessAtMs = int64Ptr(61_000)
		status, next := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: 62_000})
		if status != KeyModelMutationApplied || next.Phase != KeyModelPhaseClosed || next.BackoffAttempt != 0 || next.RecoverySuccessCount != 0 {
			t.Fatalf("status=%s phase=%s", status, next.Phase)
		}
		if next.RetryAtMs != nil {
			t.Fatal("CLOSED state must clear retryAtMs")
		}
	})
	t.Run("成功间隔超窗重置计数", func(t *testing.T) {
		state := base(t)
		state.Phase = KeyModelPhaseHalfOpen
		state.RecoverySuccessCount = 2
		state.LastRecoverySuccessAtMs = int64Ptr(0)
		state.ProbeLease.LeaseUntilMs = 500_000
		status, next := SettleKeyModelRecovery(state, SettleKeyModelRecoveryInput{Generation: 1, DispatchRevision: 3, LeaseID: "lease-1", Outcome: KeyModelOutcomeCompleteSuccess, NowMs: 200_000})
		if status != KeyModelMutationApplied || next.Phase != KeyModelPhaseRecovering || next.RecoverySuccessCount != 1 {
			t.Fatalf("status=%s phase=%s count=%d", status, next.Phase, next.RecoverySuccessCount)
		}
	})
}

func TestAcquireKeyModelRecoveryLeaseBranches(t *testing.T) {
	state, err := CreateKeyModelOpenState(testCapability(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// 未到期 not_due。
	early := state.Clone()
	early.Phase = KeyModelPhaseOpen
	early.RetryAtMs = int64Ptr(5_000)
	status, _, err := AcquireKeyModelRecoveryLease(early, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "l1", NowMs: 4_999})
	if err != nil || status != KeyModelMutationNotDue {
		t.Fatalf("status = %s err = %v", status, err)
	}
	// 到期 → HALF_OPEN + 租约。
	status, acquired, err := AcquireKeyModelRecoveryLease(early, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "l1", NowMs: 5_000})
	if err != nil || status != KeyModelMutationApplied || acquired.Phase != KeyModelPhaseHalfOpen {
		t.Fatalf("status=%s phase=%s err=%v", status, acquired.Phase, err)
	}
	if acquired.ProbeLease.LeaseUntilMs != 5_000+KeyModelProbeLeaseMs || acquired.ProbeLease.PriorSuccessCount != 0 {
		t.Fatalf("lease = %+v", acquired.ProbeLease)
	}
	// HALF_OPEN 期间再次抢占 → not_due（Node/jobs 先检查 phase）。
	status, _, err = AcquireKeyModelRecoveryLease(acquired, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "l2", NowMs: 5_001})
	if err != nil || status != KeyModelMutationNotDue {
		t.Fatalf("half-open re-acquire = %s err = %v", status, err)
	}
	// 带活跃租约的 OPEN 态被抢占 → lease_mismatch。
	withLease := early.Clone()
	withLease.ProbeLease = &KeyModelProbeLease{LeaseID: "holder", LeaseUntilMs: 100_000, PriorSuccessCount: 0}
	status, _, err = AcquireKeyModelRecoveryLease(withLease, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "l2", NowMs: 5_001})
	if err != nil || status != KeyModelMutationLeaseMismatch {
		t.Fatalf("status = %s err = %v", status, err)
	}
	// generation 变更 → stale。
	status, _, err = AcquireKeyModelRecoveryLease(acquired, AcquireKeyModelRecoveryLeaseInput{Generation: 2, DispatchRevision: 3, LeaseID: "l3", NowMs: 5_002})
	if err != nil || status != KeyModelMutationStale {
		t.Fatalf("status = %s err = %v", status, err)
	}
	// 空 leaseId 报错。
	if _, _, err := AcquireKeyModelRecoveryLease(early, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "  ", NowMs: 9_000}); err == nil || err.Error() != "CapabilityKey 缺少 leaseId" {
		t.Fatalf("leaseId err = %v", err)
	}
	// CLOSED 不可抢占。
	closed := early.Clone()
	closed.Phase = KeyModelPhaseClosed
	closed.RetryAtMs = nil
	status, _, err = AcquireKeyModelRecoveryLease(closed, AcquireKeyModelRecoveryLeaseInput{Generation: 1, DispatchRevision: 3, LeaseID: "l4", NowMs: 9_000})
	if err != nil || status != KeyModelMutationNotDue {
		t.Fatalf("closed status = %s err = %v", status, err)
	}
}

func TestForegroundAdmissionDecision(t *testing.T) {
	if decision, _ := DecideKeyModelForegroundAdmission(KeyModelPhaseOpen, 0); decision != ForegroundBlocked {
		t.Fatalf("open decision = %s", decision)
	}
	if decision, _ := DecideKeyModelForegroundAdmission(KeyModelPhaseClosed, 1); decision != ForegroundAdmitted {
		t.Fatalf("decision = %s", decision)
	}
	if decision, _ := DecideKeyModelForegroundAdmission(KeyModelPhaseClosed, 2); decision != ForegroundBusy {
		t.Fatalf("decision = %s", decision)
	}
	if _, err := DecideKeyModelForegroundAdmission(KeyModelPhaseClosed, -1); err == nil || err.Error() != "foreground activeUncommitted 无效" {
		t.Fatalf("err = %v", err)
	}
}

func TestMatchesMainProbeRoute(t *testing.T) {
	main := MainProbeRoute{ClientModel: "gpt-test", ClientEndpointFamily: "chat_completions", FinalUpstreamModel: "gpt-upstream", UpstreamEndpointMode: "chat_json"}
	key := testCapability()
	if !MatchesMainProbeRoute(key, main) {
		t.Fatal("route should match")
	}
	key.ClientModel = "other"
	if MatchesMainProbeRoute(key, main) {
		t.Fatal("changed model must not match")
	}
}

func TestEndpointModeMappings(t *testing.T) {
	tests := []struct {
		source string
		family string
		stream bool
	}{
		{"chat_json", "chat_completions", false},
		{"chat_sse", "chat_completions", true},
		{"responses_json", "responses", false},
		{"responses_sse", "responses", true},
		{"messages_json", "messages", false},
		{"messages_sse", "messages", true},
		{"generate_content_json", "generate_content", false},
		{"generate_content_sse", "stream_generate_content", true},
		{"interactions_json", "interactions", false},
		{"interactions_sse", "interactions", true},
	}
	for _, tt := range tests {
		family, stream, ok := SourceEndpointMode(tt.source)
		if !ok || family != tt.family || stream != tt.stream {
			t.Fatalf("SourceEndpointMode(%s) = %s %v %v", tt.source, family, stream, ok)
		}
	}
	if _, _, ok := SourceEndpointMode("unknown"); ok {
		t.Fatal("unknown mode must not resolve")
	}
	if got := EndpointModeForFamily("chat_completions", true); got != "chat_sse" {
		t.Fatalf("endpointMode = %s", got)
	}
	if got := EndpointModeForFamily("stream_generate_content", false); got != "generate_content_sse" {
		t.Fatalf("stream_generate_content must always be SSE, got %s", got)
	}
	if got := EndpointModeForFamily("unknown", false); got != "" {
		t.Fatalf("unknown family = %q", got)
	}
}

func int64Ptr(value int64) *int64 { return &value }
