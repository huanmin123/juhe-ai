package managementaccountstatussnapshot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestParseAccountIDsBoundsAndOrder(t *testing.T) {
	got, err := ParseAccountIDs(" account_b, account_a,account_b ")
	if err != nil || len(got) != 2 || got[0] != "account_b" || got[1] != "account_a" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := ParseAccountIDs(""); err != ErrInvalidAccountIDs {
		t.Fatalf("empty err=%v", err)
	}
	if _, err := ParseAccountIDs("a," + strings.Repeat("x", MaxQueryLength)); err != ErrQueryTooLong {
		t.Fatalf("long err=%v", err)
	}
}

func TestServiceSelfScopeAndEffectiveAvailability(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{ID: "a1", SystemAccountID: "u1", Name: "A", Status: "active", Schedulable: true}}}
	s := NewService(reader)
	s.now = func() time.Time { return time.Unix(0, 0) }
	result, err := s.Get(context.Background(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"a1"}})
	if err != nil || reader.input.SystemAccountID != "u1" || !result.Items[0].EffectiveAvailability.Available {
		t.Fatalf("result=%+v err=%v input=%+v", result, err, reader.input)
	}
}

func TestServiceUsesUsageStatsTimezoneForTodayProjection(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{ID: "a1", SystemAccountID: "u1", Name: "A", Status: "active", Schedulable: true}}}
	service := NewServiceWithOptions(ServiceOptions{
		Reader:             reader,
		UsageStatsTimezone: &statusTimezoneReaderStub{timezone: "Asia/Shanghai", found: true},
		Now:                func() time.Time { return time.Date(2026, 7, 21, 18, 30, 0, 0, time.UTC) },
	})
	if _, err := service.Get(t.Context(), Input{ActorSystemAccountID: "u1", SelfOnly: true, AccountIDs: []string{"a1"}}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reader.input.StatDate != "2026-07-22" {
		t.Fatalf("stat date = %q, want 2026-07-22", reader.input.StatDate)
	}
}

func TestServiceLoadsCurrentConcurrencyWhenReaderIsAvailable(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{ID: "a1", SystemAccountID: "u1", Name: "A", Status: "active", Schedulable: true}}}
	concurrency := &statusConcurrencyReaderStub{values: map[string]int{"a1": 3}}
	s := NewServiceWithOptions(ServiceOptions{Reader: reader, AccountConcurrency: concurrency})

	result, err := s.Get(context.Background(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"a1"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !result.RuntimeSnapshot.AccountConcurrencyAvailable || result.Items[0].CurrentConcurrency != 3 || len(concurrency.ids) != 1 || concurrency.ids[0] != "a1" {
		t.Fatalf("runtime=%+v item=%+v ids=%v", result.RuntimeSnapshot, result.Items[0], concurrency.ids)
	}
}

func TestServiceLoadsAuthorizedInstanceConcurrencyFromSourceAccount(t *testing.T) {
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{
		ID: "instance_1", SystemAccountID: "u1", Name: "授权实例", Status: "active", Schedulable: true,
		AuthorizationInstanceSourceAccountID: "source_1",
	}}}
	concurrency := &statusConcurrencyReaderStub{values: map[string]int{"source_1": 4}}
	s := NewServiceWithOptions(ServiceOptions{Reader: reader, AccountConcurrency: concurrency})

	result, err := s.Get(context.Background(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"instance_1"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(concurrency.ids) != 1 || concurrency.ids[0] != "source_1" || result.Items[0].CurrentConcurrency != 4 {
		t.Fatalf("ids=%v item=%+v", concurrency.ids, result.Items[0])
	}
}

func TestServiceAddsAPIKeyRuntimeSummaryFromSourceAccount(t *testing.T) {
	secret := "runtime-secret"
	firstFingerprint := statusTestFingerprint(secret, "sk-first")
	secondFingerprint := statusTestFingerprint(secret, "sk-second")
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{
		ID: "instance_1", SystemAccountID: "u1", Name: "授权实例", Status: "active", Schedulable: true,
		AuthorizationInstanceSourceAccountID: "source_1",
	}}}
	sources := &statusAPIKeyRuntimeSourceReaderStub{values: map[string]port.ManagementAccountAPIKeyRuntimeSource{
		"instance_1": {ViewAccountID: "instance_1", SourceAccountID: "source_1", ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", CredentialsEncrypted: "source-cipher"},
	}}
	runtime := &statusAPIKeyRuntimeReaderStub{values: map[string][]port.ManagementAccountAPIKeyRuntimeState{
		"source_1": {
			{KeyFingerprint: firstFingerprint, KeyIndex: 0, Status: "rate_limited", NextProbeAt: "2026-07-21T10:00:00Z", LastFailureAt: "2026-07-21T09:00:00Z", LastErrorCode: "rate_limit", LastErrorMessage: "raw upstream failure", LastTraceID: "trace-first"},
			{KeyFingerprint: secondFingerprint, KeyIndex: 1, Status: "disabled", NextProbeAt: "2026-07-21T08:00:00Z", LastFailureAt: "2026-07-21T09:30:00Z", LastErrorCode: "disabled", LastTraceID: "trace-second"},
		},
	}}
	service := NewServiceWithOptions(ServiceOptions{
		Reader: reader, APIKeyRuntime: runtime, APIKeySources: sources,
		CredentialCodec: &statusCredentialCodecStub{values: map[string]any{
			"api_keys": []any{"sk-first", "sk-second", "sk-third"},
		}},
		FingerprintSecret: secret,
	})

	result, err := service.Get(t.Context(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"instance_1"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if result.RuntimeSnapshot.AccountRuntimeAvailabilityAvailable || len(runtime.requests["source_1"]) != 3 {
		t.Fatalf("runtime snapshot=%+v requests=%v", result.RuntimeSnapshot, runtime.requests)
	}
	summary := result.Items[0].APIKeyRuntime
	if summary == nil {
		t.Fatal("APIKeyRuntime = nil")
	}
	if summary.Total != 3 || summary.Active != 1 || summary.Unavailable != 2 || summary.RateLimited != 1 || summary.Disabled != 1 || summary.AllUnavailable {
		t.Fatalf("APIKeyRuntime = %+v", summary)
	}
	if summary.NextProbeAt != "2026-07-21T10:00:00Z" || summary.LastFailureAt != "2026-07-21T09:30:00Z" || summary.LastErrorCode != "disabled" || summary.LastTraceID != "trace-second" {
		t.Fatalf("APIKeyRuntime timing/error = %+v", summary)
	}
}

func TestServiceBlocksAvailabilityWhenAllAPIKeysAreUnavailable(t *testing.T) {
	secret := "runtime-secret"
	firstFingerprint := statusTestFingerprint(secret, "sk-first")
	secondFingerprint := statusTestFingerprint(secret, "sk-second")
	reader := &statusReaderStub{rows: []port.ManagementAccountStatusProjection{{
		ID: "account_1", SystemAccountID: "u1", Name: "账户", Status: "active", Schedulable: true,
	}}}
	sources := &statusAPIKeyRuntimeSourceReaderStub{values: map[string]port.ManagementAccountAPIKeyRuntimeSource{
		"account_1": {ViewAccountID: "account_1", SourceAccountID: "account_1", ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", CredentialsEncrypted: "cipher"},
	}}
	runtime := &statusAPIKeyRuntimeReaderStub{values: map[string][]port.ManagementAccountAPIKeyRuntimeState{
		"account_1": {
			{KeyFingerprint: firstFingerprint, Status: "rate_limited", NextProbeAt: "2026-07-21T10:00:00Z"},
			{KeyFingerprint: secondFingerprint, Status: "disabled"},
		},
	}}
	service := NewServiceWithOptions(ServiceOptions{
		Reader: reader, APIKeyRuntime: runtime, APIKeySources: sources,
		CredentialCodec:   &statusCredentialCodecStub{values: map[string]any{"api_keys": []any{"sk-first", "sk-second"}}},
		FingerprintSecret: secret,
	})

	result, err := service.Get(t.Context(), Input{ActorSystemAccountID: "u1", ActorRole: "user", SelfOnly: true, AccountIDs: []string{"account_1"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	item := result.Items[0]
	if item.APIKeyRuntime == nil || !item.APIKeyRuntime.AllUnavailable {
		t.Fatalf("APIKeyRuntime = %+v", item.APIKeyRuntime)
	}
	if item.EffectiveAvailability.Available || item.EffectiveAvailability.Status != "api_key_pool_unavailable" || item.EffectiveAvailability.BlockerScope != "api_key_pool" || item.EffectiveAvailability.RetryAt != "2026-07-21T10:00:00Z" {
		t.Fatalf("EffectiveAvailability = %+v", item.EffectiveAvailability)
	}
	if item.AvailabilityPresentation.Status != "key_pool_unavailable" || item.AvailabilityPresentation.Action != "retry_check" {
		t.Fatalf("AvailabilityPresentation = %+v", item.AvailabilityPresentation)
	}
}

func TestEffectiveAvailabilityUsesNodeStaticBlockerPriority(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		row        port.ManagementAccountStatusProjection
		wantStatus string
		wantScope  string
	}{
		{
			name: "authorization expired before source and instance",
			row: port.ManagementAccountStatusProjection{
				Status: "disabled", Schedulable: false, AuthorizationID: "auth_1", AuthorizationStatus: "expired",
				AuthorizationInstanceSourceAccountID: "source_1", AuthorizationInstanceSourceAccountStatus: "disabled", BoundGroupID: "group_1", GroupBindStatus: "bound",
			},
			wantStatus: "authorization_expired", wantScope: "authorization",
		},
		{
			name: "source cooldown before instance",
			row: port.ManagementAccountStatusProjection{
				Status: "active", Schedulable: true, AuthorizationID: "auth_1", AuthorizationStatus: "active",
				AuthorizationInstanceSourceAccountID: "source_1", AuthorizationInstanceSourceAccountStatus: "active", AuthorizationInstanceSourceSchedulable: true,
				AuthorizationInstanceSourceCooldownUntil: now.Add(time.Hour).Format(time.RFC3339), BoundGroupID: "group_1", GroupBindStatus: "bound",
			},
			wantStatus: "source_cooldown", wantScope: "source_account",
		},
		{
			name: "pending health check failure",
			row: port.ManagementAccountStatusProjection{
				Status: "pending_test", Schedulable: true, LastHealthCheckAt: now.Add(-time.Minute).Format(time.RFC3339), LastHealthCheckErrorCode: "probe_failed",
			},
			wantStatus: "instance_pending_test", wantScope: "account",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := effective(test.row, now)
			if got.Status != test.wantStatus || got.BlockerScope != test.wantScope || got.Available || got.Reason == "" {
				t.Fatalf("effective = %+v", got)
			}
			presentation := availabilityPresentation(got, test.row)
			if presentation.Status == "" || presentation.Label == "" || presentation.Action == "" {
				t.Fatalf("presentation = %+v", presentation)
			}
		})
	}
}

func TestEffectiveAvailabilityPresentsQualityIsolationForSourceAndInstance(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name                   string
		row                    port.ManagementAccountStatusProjection
		wantStatus             string
		wantLabel              string
		wantScope              string
		wantReason             string
		wantPresentation       string
		wantPresentationAction string
	}{
		{
			name: "authorized instance is blocked when source is quality isolated",
			row: port.ManagementAccountStatusProjection{
				Status: "active", Schedulable: true, AuthorizationID: "auth_1", AuthorizationStatus: "active",
				AuthorizationInstanceSourceAccountID: "source_1", AuthorizationInstanceSourceAccountStatus: "quality_isolated",
				AuthorizationInstanceSourceSchedulable: true, AuthorizationInstanceSourceLastErrorMessage: "质量分数低于策略阈值",
				BoundGroupID: "group_1", GroupBindStatus: "bound",
			},
			wantStatus: "source_quality_isolated", wantLabel: "来源质量隔离", wantScope: "source_account", wantReason: "质量分数低于策略阈值",
			wantPresentation: "source_blocked", wantPresentationAction: "contact_authorizer",
		},
		{
			name: "account is blocked when its own quality is isolated",
			row: port.ManagementAccountStatusProjection{
				Status: "quality_isolated", Schedulable: true, LastErrorMessage: "模型质量恢复检查尚未通过",
			},
			wantStatus: "instance_quality_isolated", wantLabel: "账户质量隔离", wantScope: "account", wantReason: "模型质量恢复检查尚未通过",
			wantPresentation: "error", wantPresentationAction: "retry_check",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effectiveAvailability := effective(test.row, now)
			if effectiveAvailability.Available || effectiveAvailability.Status != test.wantStatus || effectiveAvailability.Label != test.wantLabel || effectiveAvailability.Color != "red" || effectiveAvailability.BlockerScope != test.wantScope || effectiveAvailability.Reason != test.wantReason {
				t.Fatalf("effective availability = %+v", effectiveAvailability)
			}
			presentation := availabilityPresentation(effectiveAvailability, test.row)
			if presentation.Status != test.wantPresentation || presentation.Action != test.wantPresentationAction || presentation.Label != test.wantLabel || presentation.Reason != test.wantReason {
				t.Fatalf("presentation = %+v", presentation)
			}
		})
	}
}

type statusReaderStub struct {
	input port.ManagementAccountStatusSnapshotInput
	rows  []port.ManagementAccountStatusProjection
}

type statusConcurrencyReaderStub struct {
	ids    []string
	values map[string]int
}

type statusAPIKeyRuntimeReaderStub struct {
	requests map[string][]string
	values   map[string][]port.ManagementAccountAPIKeyRuntimeState
}

type statusAPIKeyRuntimeSourceReaderStub struct {
	ids    []string
	values map[string]port.ManagementAccountAPIKeyRuntimeSource
}

func (s *statusAPIKeyRuntimeSourceReaderStub) ListManagementAccountAPIKeyRuntimeSourcesByAccountIDs(_ context.Context, ids []string) (map[string]port.ManagementAccountAPIKeyRuntimeSource, error) {
	s.ids = append([]string(nil), ids...)
	return s.values, nil
}

func (s *statusAPIKeyRuntimeReaderStub) ListManagementAccountAPIKeyRuntimeStatesByFingerprints(_ context.Context, requests map[string][]string) (map[string][]port.ManagementAccountAPIKeyRuntimeState, error) {
	s.requests = requests
	return s.values, nil
}

type statusCredentialCodecStub struct {
	values map[string]any
}

type statusTimezoneReaderStub struct {
	timezone string
	found    bool
	err      error
}

func (s *statusTimezoneReaderStub) GetManagementUsageStatsTimezone(context.Context) (string, bool, error) {
	return s.timezone, s.found, s.err
}

func (s *statusCredentialCodecStub) DecryptJSON(_ string) (map[string]any, error) {
	return s.values, nil
}

func statusTestFingerprint(secret string, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *statusConcurrencyReaderStub) LoadAccountCurrentConcurrencyByIDs(_ context.Context, ids []string, _ time.Time) (map[string]int, error) {
	s.ids = append([]string(nil), ids...)
	return s.values, nil
}

func (s *statusReaderStub) ListManagementAccountStatusProjections(_ context.Context, input port.ManagementAccountStatusSnapshotInput) ([]port.ManagementAccountStatusProjection, error) {
	s.input = input
	return s.rows, nil
}
