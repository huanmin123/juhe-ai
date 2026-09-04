package gatewaycircuit

import (
	"math"
	"strings"
	"testing"
)

func TestScopeKeyEncoding(t *testing.T) {
	tests := []struct {
		name    string
		scope   Scope
		want    string
		wantErr string
	}{
		{
			name:  "account scope",
			scope: Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "acc1"},
			want:  "7:account|4:acc1",
		},
		{
			name:  "key scope",
			scope: Scope{Kind: ScopeKindKey, AccountRuntimeKey: "acc1", KeyFingerprint: "fp"},
			want:  "3:key|4:acc1|2:fp",
		},
		{
			name: "protocol_model scope",
			scope: Scope{
				Kind: ScopeKindProtocolModel, AccountRuntimeKey: "a", ProtocolProfile: "p",
				RequestLane: LaneText, ModelBucket: "gpt-4o",
			},
			want: "14:protocol_model|1:a|1:p|4:text|6:gpt-4o",
		},
		{
			name:    "missing accountRuntimeKey",
			scope:   Scope{Kind: ScopeKindAccount},
			wantErr: "账户电路作用域缺少 accountRuntimeKey",
		},
		{
			name:    "invalid lane",
			scope:   Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "audio", ModelBucket: "m"},
			wantErr: "账户电路作用域 requestLane 必须是 text 或 image",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScopeKey(tt.scope)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("ScopeKey() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ScopeKey() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ScopeKey() = %q, want %q", got, tt.want)
			}
			if AssertStateScopeKey(ClosedState(tt.scope, "", 0, "", 0)) != nil {
				t.Fatalf("AssertStateScopeKey should accept generated states")
			}
		})
	}
}

func TestHierarchyTransitionIDDeterministic(t *testing.T) {
	first, err := HierarchyTransitionID("shadow", "pt", "pi", "ck", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, _ := HierarchyTransitionID("shadow", "pt", "pi", "ck", 3)
	if first != second {
		t.Fatalf("hierarchy transition id not deterministic: %s vs %s", first, second)
	}
	if !strings.HasPrefix(first, "hierarchy:shadow:") {
		t.Fatalf("unexpected prefix: %s", first)
	}
	if _, err := HierarchyTransitionID("shadow", "", "pi", "ck", 3); err == nil || !strings.Contains(err.Error(), "parentTransitionId") {
		t.Fatalf("expected missing parentTransitionId error, got %v", err)
	}
	if _, err := HierarchyTransitionID("shadow", "pt", "pi", "ck", -1); err == nil || err.Error() != "账户电路 hierarchy childGeneration 无效" {
		t.Fatalf("expected childGeneration error, got %v", err)
	}
}

func TestNormalizeConfirmationFailuresRequired(t *testing.T) {
	tests := []struct {
		name    string
		value   *int64
		fallback int64
		want    int64
		wantErr string
	}{
		{name: "nil uses fallback", value: nil, fallback: DefaultConfirmationFailuresRequired, want: 2},
		{name: "legacy fallback clamped", value: int64Ptr(1), fallback: 1, want: 1},
		{name: "in range", value: int64Ptr(5), fallback: 1, want: 5},
		{name: "too low", value: int64Ptr(0), fallback: 1, wantErr: "账户电路 confirmationFailuresRequired 必须是 1..5 的整数"},
		{name: "too high", value: int64Ptr(6), fallback: 1, wantErr: "账户电路 confirmationFailuresRequired 必须是 1..5 的整数"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeConfirmationFailuresRequired(tt.value, tt.fallback)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got (%d, %v), want %d", got, err, tt.want)
			}
		})
	}
}

func TestNormalizeFailureEvidenceKey(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	got, err := NormalizeFailureEvidenceKey(strPtr(strings.ToUpper(sha + " ")), "")
	if err != nil || got != sha {
		t.Fatalf("NormalizeFailureEvidenceKey uppercase = (%q, %v)", got, err)
	}
	hashed, err := NormalizeFailureEvidenceKey(nil, "suspect:t1")
	if err != nil || len(hashed) != 64 {
		t.Fatalf("fallback hash = (%q, %v)", hashed, err)
	}
	if hashed != sha256Hex("suspect:t1") {
		t.Fatalf("fallback hash mismatch")
	}
	if _, err := NormalizeFailureEvidenceKey(strPtr("nothex"), ""); err == nil || err.Error() != "账户电路 failure evidence 缺少 fallbackSeed" {
		t.Fatalf("expected fallbackSeed error, got %v", err)
	}
}

func TestFailureEvidenceKeysOfDedupeAndTrim(t *testing.T) {
	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	required := int64(2)
	state := State{
		Phase:                        PhaseSuspect,
		ConfirmationFailuresRequired: &required,
		FailureEvidenceKeys:          stringList{strings.ToUpper(shaA + " "), shaB, shaA, "zz"},
	}
	keys, err := FailureEvidenceKeysOf(state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 || keys[0] != shaA || keys[1] != shaB {
		t.Fatalf("keys = %v", keys)
	}
}

func TestOlderNumericDispatchRevision(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"3", "5", true},
		{"5", "3", false},
		{"5", "5", false},
		{"v1:abc", "9", false},
		{"9", "v1:abc", false},
		{"3.5", "9", false},
		{"-2", "5", false},
		{"", "5", false},
	}
	for _, tt := range tests {
		if got := olderNumericDispatchRevision(tt.candidate, tt.current); got != tt.want {
			t.Fatalf("olderNumericDispatchRevision(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
		}
	}
}

func TestCapacityExhaustedStateShape(t *testing.T) {
	scope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "a"}
	state := CapacityExhaustedState(scope, "7", 1000)
	if state.Phase != PhaseSuspect || state.FailureReason == nil || *state.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs != 2000 {
		t.Fatalf("retryAtMs = %v", state.RetryAtMs)
	}
	if state.TransitionID != "runtime-capacity-exhausted" {
		t.Fatalf("transitionId = %q", state.TransitionID)
	}
}

func TestPassiveScheduleJitterWindow(t *testing.T) {
	tests := []struct {
		interval int64
		want     int64
	}{
		{3000, 1500},
		{60000, 30000},
		{120000, 30000},
		{3 * 60 * 60_000, 30 * 60_000},
		{25 * 60 * 60_000, 60 * 60_000},
		{8 * 24 * 60 * 60_000, 8 * 60 * 60_000},
	}
	for _, tt := range tests {
		if got := passiveScheduleJitterWindowMs(tt.interval); got != tt.want {
			t.Fatalf("window(%d) = %d, want %d", tt.interval, got, tt.want)
		}
	}
}

func TestAccountCircuitBackoffDelayMsGolden(t *testing.T) {
	settings := DefaultSettings()
	// Early attempts are exact.
	if got := settings.accountCircuitBackoffDelayMs(1, "", nil); got != 3000 {
		t.Fatalf("attempt 1 = %d", got)
	}
	if got := settings.accountCircuitBackoffDelayMs(4, "seed", nil); got != 30000 {
		t.Fatalf("attempt 4 = %d", got)
	}
	// attempt >= 5 with a seed derives the offset from sha1(seed).
	// sha1("abc") = a9993e36... => sample 0xa9993e36.
	digest := sha1Hex("abc")
	if !strings.HasPrefix(digest, "a9993e36") {
		t.Fatalf("sha1 sanity failed: %s", digest)
	}
	window := int64(30000) // base 120000 -> minute window
	want := int64(120000 + (int64(0xa9993e36)%int64(window*2+1)) - window)
	if want == 120000 {
		want = 120001 // Node flips a zero offset to 1
	}
	if got := settings.accountCircuitBackoffDelayMs(6, "abc", nil); got != want {
		t.Fatalf("attempt 6 seeded = %d, want %d", got, want)
	}
	// Random path stays within the symmetric window and is strictly positive.
	base := int64(60000)
	for i := 0; i < 200; i++ {
		got := settings.accountCircuitBackoffDelayMs(5, "", defaultRandom)
		if got < base-30000 || got > base+30000 || got < 1 {
			t.Fatalf("random delay out of window: %d", got)
		}
	}
}

func TestPassiveScheduleDelayAlwaysPositive(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := passiveScheduleDelayMs(1, defaultRandom); got < 1 {
			t.Fatalf("delay = %d", got)
		}
		if got := passiveScheduleNotBeforeDelayMs(1, defaultRandom); got < 1 {
			t.Fatalf("notBefore delay = %d", got)
		}
	}
}

func TestStableSerializeSortedKeys(t *testing.T) {
	value := map[string]any{
		"b": "x\"y",
		"a": []any{int64(1), true, nil},
	}
	got := stableSerialize(value)
	want := `{"a":[1,true,null],"b":"x\"y"}`
	if got != want {
		t.Fatalf("stableSerialize = %s, want %s", got, want)
	}
	if got := stableSerialize(nil); got != "null" {
		t.Fatalf("null = %s", got)
	}
}

func TestStateJSONRoundTripThroughLuaShapes(t *testing.T) {
	// Lua cjson encodes empty arrays as `{}`; the tolerant decoders must
	// accept that shape.
	encoded := `{"scopeKey":"k","scope":{"kind":"account"},"phase":"SUSPECT","generation":1,
		"failureEvidenceKeys":{},"childScopeKeys":{},"relatedStates":{}}`
	var payload struct {
		State         State     `json:"state"`
		RelatedStates stateList `json:"relatedStates"`
	}
	wrapped := `{"state":` + strings.ReplaceAll(encoded, "\n", "") + `,"relatedStates":{}}`
	if err := jsonUnmarshalHelper(wrapped, &payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(payload.State.FailureEvidenceKeys) != 0 || len(payload.RelatedStates) != 0 {
		t.Fatalf("empty Lua tables should decode to empty lists")
	}
}

func TestAccountCircuitDueAtMs(t *testing.T) {
	if got := accountCircuitDueAtMs(State{Phase: PhaseClosed}); got != math.MaxInt64 {
		t.Fatalf("closed due = %d", got)
	}
	retry := int64(55)
	if got := accountCircuitDueAtMs(State{Phase: PhaseOpen, RetryAtMs: &retry}); got != 55 {
		t.Fatalf("open due = %d", got)
	}
	if got := accountCircuitDueAtMs(State{Phase: PhaseHalfOpen}); got != math.MaxInt64 {
		t.Fatalf("half-open without lease due = %d", got)
	}
	lease := &Lease{LeaseUntilMs: 99}
	if got := accountCircuitDueAtMs(State{Phase: PhaseHalfOpen, Lease: lease}); got != 99 {
		t.Fatalf("lease due = %d", got)
	}
}

func jsonUnmarshalHelper(raw string, dst any) error {
	return jsonUnmarshal([]byte(raw), dst)
}
