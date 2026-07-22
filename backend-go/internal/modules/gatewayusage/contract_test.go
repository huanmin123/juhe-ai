package gatewayusage

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestFinalizeBuildsProtocolIndependentSuccessRecord(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	completedAt := startedAt.Add(1250 * time.Millisecond)
	statusCode := 200
	inputTokens := int64(12)
	outputTokens := int64(7)
	costUSD := 0.00042

	record, err := Finalize(RequestFacts{
		TraceID:                   " trace_1 ",
		TrafficSource:             TrafficSourceGateway,
		SystemAccountID:           " sys_1 ",
		APIKeyID:                  " key_1 ",
		Endpoint:                  " POST /v1/responses ",
		ProviderCode:              " openai ",
		ProviderProtocolProfileID: " responses ",
		Model:                     " gpt-5.4 ",
		Stream:                    true,
		StartedAt:                 startedAt,
		Usage: UsageFacts{
			RequestedServiceTier:     "priority",
			EffectiveServiceTier:     "priority",
			RequestedReasoningEffort: "high",
		},
		RequestSnapshot: map[string]any{
			"method":      "POST",
			"originalUrl": "/v1/responses?api_key=diagnostic-original",
			"bodyText":    `{"client_secret":"diagnostic-original"}`,
			"headers": map[string]any{
				"authorization":     "Bearer secret",
				"x-oai-attestation": "never-capture",
				"content-type":      "application/json",
			},
		},
	}, TerminalFacts{
		Outcome:     OutcomeSucceeded,
		CompletedAt: completedAt,
		StatusCode:  &statusCode,
		Usage: UsageFacts{
			ReportedServiceTier: "priority",
			BilledServiceTier:   "priority",
			InputTokens:         &inputTokens,
			OutputTokens:        &outputTokens,
			CostUSD:             &costUSD,
		},
		ResponseSnapshot: map[string]any{
			"statusCode": 200,
			"headers": map[string]any{
				"set-cookie":   "session=secret",
				"content-type": "application/json",
			},
		},
	})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if record.TraceID != "trace_1" || record.SystemAccountID != "sys_1" || record.Endpoint != "POST /v1/responses" {
		t.Fatalf("normalized record = %+v", record)
	}
	if record.Duration != 1250*time.Millisecond || record.Outcome != OutcomeSucceeded {
		t.Fatalf("terminal facts = outcome %q duration %s", record.Outcome, record.Duration)
	}
	if record.FailureAttribution != "" || record.ErrorCode != "" || record.ErrorMessage != "" {
		t.Fatalf("successful record contains failure facts: %+v", record)
	}
	if record.Usage.RequestedServiceTier != "priority" || record.Usage.ReportedServiceTier != "priority" || record.Usage.RequestedReasoningEffort != "high" {
		t.Fatalf("request/final usage merge = %+v", record.Usage)
	}
	assertSnapshotContains(t, record.RequestSnapshot, `"authorization":"Bearer secret"`)
	assertSnapshotContains(t, record.RequestSnapshot, `"content-type":"application/json"`)
	assertSnapshotContains(t, record.RequestSnapshot, "never-capture")
	assertSnapshotContains(t, record.RequestSnapshot, "diagnostic-original")
	assertSnapshotContains(t, record.ResponseSnapshot, "session=secret")
	if !json.Valid(record.RequestSnapshot) || !json.Valid(record.ResponseSnapshot) {
		t.Fatal("finalized snapshots must be valid JSON")
	}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(FinalRecord) error = %v", err)
	}
	var encodedFields map[string]any
	if err := json.Unmarshal(encodedRecord, &encodedFields); err != nil {
		t.Fatalf("json.Unmarshal(FinalRecord) error = %v", err)
	}
	if _, ok := encodedFields["RequestSnapshot"].(map[string]any); !ok {
		t.Fatalf("encoded request snapshot = %#v, want embedded JSON object", encodedFields["RequestSnapshot"])
	}
}

func TestFinalizeDetachesMutableFactsAndOmitsProbeSnapshots(t *testing.T) {
	startedAt := time.Now()
	inputTokens := int64(1)
	request := RequestFacts{
		TraceID:         "trace_probe",
		TrafficSource:   TrafficSourceAccountHealthCheck,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
		Usage:           UsageFacts{InputTokens: &inputTokens},
		RequestSnapshot: map[string]any{"secret": "request"},
	}
	record, err := Finalize(request, TerminalFacts{
		Outcome:            OutcomeFailed,
		CompletedAt:        startedAt,
		FailureAttribution: FailureAttributionAccountUpstream,
		ResponseSnapshot:   map[string]any{"secret": "response"},
	})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	inputTokens = 99
	if record.Usage.InputTokens == nil || *record.Usage.InputTokens != 1 {
		t.Fatalf("record usage aliases caller input: %+v", record.Usage)
	}
	if record.RequestSnapshot != nil || record.ResponseSnapshot != nil {
		t.Fatalf("probe snapshots = request %s response %s, want omitted", record.RequestSnapshot, record.ResponseSnapshot)
	}
}

func TestFinalizeRejectsContradictoryOrIncompleteTerminalFacts(t *testing.T) {
	base := RequestFacts{
		TraceID:         "trace_1",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       time.Now(),
	}

	tests := []struct {
		name     string
		terminal TerminalFacts
		want     string
	}{
		{
			name: "success cannot carry failure attribution",
			terminal: TerminalFacts{
				Outcome:            OutcomeSucceeded,
				CompletedAt:        base.StartedAt,
				FailureAttribution: FailureAttributionGatewayPolicy,
			},
			want: "successful terminal facts cannot contain failure attribution",
		},
		{
			name: "outcome required",
			terminal: TerminalFacts{
				CompletedAt: base.StartedAt,
			},
			want: "invalid terminal outcome",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Finalize(base, test.terminal)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Finalize() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestFinalizeRejectsInvalidMeasurementsAndScopes(t *testing.T) {
	startedAt := time.Now()
	negative := int64(-1)
	infinite := math.Inf(1)
	base := RequestFacts{
		TraceID:         "trace_1",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
	}
	failed := TerminalFacts{
		Outcome:            OutcomeFailed,
		CompletedAt:        startedAt,
		FailureAttribution: FailureAttributionAccountUpstream,
	}

	invalidToken := failed
	invalidToken.Usage.InputTokens = &negative
	if _, err := Finalize(base, invalidToken); err == nil || !strings.Contains(err.Error(), "input tokens") {
		t.Fatalf("negative token error = %v", err)
	}

	invalidCost := failed
	invalidCost.Usage.CostUSD = &infinite
	if _, err := Finalize(base, invalidCost); err == nil || !strings.Contains(err.Error(), "cost USD") {
		t.Fatalf("infinite cost error = %v", err)
	}

	invalidCapability := failed
	invalidCapability.Usage.ReportedServiceTier = " priority "
	if _, err := Finalize(base, invalidCapability); err == nil || !strings.Contains(err.Error(), "reported service tier") {
		t.Fatalf("whitespace capability error = %v", err)
	}

	base.Account = &AccountScope{
		ID:                   "account_1",
		OwnerSystemAccountID: "owner_1",
		AccessType:           AccountAccessGroupAuthorized,
	}
	base.Group = &GroupScope{
		ID:                   "group_1",
		OwnerSystemAccountID: "owner_1",
		AccessType:           GroupAccessOwner,
	}
	record, err := Finalize(base, failed)
	if err != nil {
		t.Fatalf("Finalize() incomplete optional scope error = %v", err)
	}
	if record.Account != nil {
		t.Fatalf("incomplete account scope = %+v, want omitted", record.Account)
	}
	if record.Group == nil || record.Group.ID != "group_1" {
		t.Fatalf("independently valid group scope = %+v, want preserved", record.Group)
	}
}

func TestFinalizeDefaultsFailureAttributionWithoutDroppingRecord(t *testing.T) {
	startedAt := time.Now()
	request := RequestFacts{
		TraceID:         "trace_default_failure",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
	}
	record, err := Finalize(request, TerminalFacts{Outcome: OutcomeFailed, CompletedAt: startedAt})
	if err != nil {
		t.Fatalf("Finalize() gateway failure error = %v", err)
	}
	if record.FailureAttribution != FailureAttributionGatewayPolicy {
		t.Fatalf("gateway failure attribution = %q", record.FailureAttribution)
	}

	request.Account = &AccountScope{ID: "account_1", OwnerSystemAccountID: "sys_owner", AccessType: AccountAccessOwner}
	record, err = Finalize(request, TerminalFacts{Outcome: OutcomeFailed, CompletedAt: startedAt})
	if err != nil {
		t.Fatalf("Finalize() account failure error = %v", err)
	}
	if record.FailureAttribution != FailureAttributionAccountUpstream {
		t.Fatalf("account failure attribution = %q", record.FailureAttribution)
	}
}

func TestFinalizeKeepsCompatibleLegacyScopesWithoutStaleAuthorizationFields(t *testing.T) {
	startedAt := time.Now()
	record, err := Finalize(RequestFacts{
		TraceID:         "trace_legacy_scope",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
		Account: &AccountScope{
			ID:                   "account_owner",
			OwnerSystemAccountID: "sys_owner",
			AccessType:           AccountAccessOwner,
			Authorization:        &AuthorizationRef{ID: "stale_auth", Source: AuthorizationSourceTeam, SourceTeamID: "stale_team"},
		},
		Group: &GroupScope{
			ID:                   "group_authorized",
			OwnerSystemAccountID: "sys_owner",
			AccessType:           GroupAccessAuthorized,
			Authorization:        &AuthorizationRef{ID: "legacy_auth"},
		},
	}, TerminalFacts{Outcome: OutcomeSucceeded, CompletedAt: startedAt})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if record.Account == nil || record.Account.Authorization != nil {
		t.Fatalf("owner account scope = %+v, want stale authorization removed", record.Account)
	}
	if record.Group == nil || record.Group.Authorization == nil || record.Group.Authorization.ID != "legacy_auth" {
		t.Fatalf("legacy authorized group scope = %+v, want retained", record.Group)
	}
}

func TestFinalizePreservesFailureDiagnosticsVerbatim(t *testing.T) {
	startedAt := time.Now()
	record, err := Finalize(RequestFacts{
		TraceID:         "trace_failure",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
	}, TerminalFacts{
		Outcome:            OutcomeFailed,
		CompletedAt:        startedAt,
		FailureAttribution: FailureAttributionAccountUpstream,
		ErrorCode:          " upstream_code ",
		ErrorMessage:       " upstream message ",
	})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if record.ErrorCode != " upstream_code " || record.ErrorMessage != " upstream message " {
		t.Fatalf("diagnostics = %q / %q, want verbatim", record.ErrorCode, record.ErrorMessage)
	}
}

func TestFinalizeAllowsSuccessfulDiagnosticsWithoutFailureAttribution(t *testing.T) {
	startedAt := time.Now()
	record, err := Finalize(RequestFacts{
		TraceID:         "trace_success_diagnostic",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
	}, TerminalFacts{
		Outcome:      OutcomeSucceeded,
		CompletedAt:  startedAt,
		ErrorCode:    "provider_warning",
		ErrorMessage: "forwarded response carried diagnostic payload",
	})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if record.ErrorCode != "provider_warning" || record.FailureAttribution != "" {
		t.Fatalf("successful diagnostics = %+v", record)
	}
}

func TestUsageFactsMergeOnlyOverridesObservedValues(t *testing.T) {
	input := int64(10)
	output := int64(4)
	cacheRead := int64(3)
	current := UsageFacts{InputTokens: &input, OutputTokens: &output, ReportedServiceTier: "default"}
	next := UsageFacts{CacheReadTokens: &cacheRead, ReportedServiceTier: "priority"}

	merged := current.Merge(next)
	if merged.InputTokens == nil || *merged.InputTokens != 10 || merged.OutputTokens == nil || *merged.OutputTokens != 4 {
		t.Fatalf("Merge() dropped current facts: %+v", merged)
	}
	if merged.CacheReadTokens == nil || *merged.CacheReadTokens != 3 || merged.ReportedServiceTier != "priority" {
		t.Fatalf("Merge() did not apply observations: %+v", merged)
	}
}

func TestFinalizeResolvesServiceTierFactsOnce(t *testing.T) {
	startedAt := time.Now()
	record, err := Finalize(RequestFacts{
		TraceID:         "trace_tier",
		TrafficSource:   TrafficSourceGateway,
		SystemAccountID: "sys_1",
		Endpoint:        "POST /v1/responses",
		StartedAt:       startedAt,
	}, TerminalFacts{
		Outcome:     OutcomeSucceeded,
		CompletedAt: startedAt,
		Usage:       UsageFacts{ReportedServiceTier: "flex"},
	})
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if record.Usage.RequestedServiceTier != "default" ||
		record.Usage.EffectiveServiceTier != "default" ||
		record.Usage.ReportedServiceTier != "flex" ||
		record.Usage.BilledServiceTier != "flex" {
		t.Fatalf("resolved service tiers = %+v", record.Usage)
	}
}

func TestSanitizeSnapshotAppliesDeterministicStructuralAndByteBounds(t *testing.T) {
	large := map[string]any{}
	for index := 99; index >= 0; index-- {
		large[string(rune('a'+index%26))+strings.Repeat("x", index/26)] = strings.Repeat("界", 7000)
	}
	large["array"] = make([]any, 70)
	for index := range large["array"].([]any) {
		large["array"].([]any)[index] = strings.Repeat("v", 2000)
	}

	first := SanitizeSnapshot(large)
	second := SanitizeSnapshot(large)
	if !bytes.Equal(first, second) {
		t.Fatal("SanitizeSnapshot() must be deterministic for maps")
	}
	if len(first) > MaxSnapshotBytes {
		t.Fatalf("snapshot bytes = %d, max = %d", len(first), MaxSnapshotBytes)
	}
	if !json.Valid(first) {
		t.Fatalf("snapshot is invalid JSON: %q", first)
	}
	if !bytes.Contains(first, []byte(`"_truncated":true`)) {
		t.Fatalf("truncated snapshot lacks marker: %s", first)
	}
}

func TestSanitizeSnapshotHandlesCyclesAndDepth(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	deep := map[string]any{"leaf": "ok"}
	for index := 0; index < MaxSnapshotDepth+2; index++ {
		deep = map[string]any{"next": deep}
	}
	cyclic["deep"] = deep

	snapshot := SanitizeSnapshot(cyclic)
	assertSnapshotContains(t, snapshot, `"self":"[circular]"`)
	assertSnapshotContains(t, snapshot, `"[depth_truncated]"`)
	if len(snapshot) > MaxSnapshotBytes || !json.Valid(snapshot) {
		t.Fatalf("cycle/depth snapshot invalid: %s", snapshot)
	}
}

func assertSnapshotContains(t *testing.T, snapshot Snapshot, value string) {
	t.Helper()
	if !bytes.Contains(snapshot, []byte(value)) {
		t.Fatalf("snapshot %s does not contain %q", snapshot, value)
	}
}
