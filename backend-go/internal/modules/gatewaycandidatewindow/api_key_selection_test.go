package gatewaycandidatewindow

import (
	"fmt"
	"testing"
	"time"
)

func TestSelectProbeAPIKeyPreservesOriginalIndexAndFingerprint(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	candidate := Candidate{
		Credentials: NewCredentialSet(map[string]any{"api_keys": []any{"", "key-one", "key-one", "key-two"}}),
		APIKeyRuntime: []APIKeyRuntime{
			{KeyIndex: 1, KeyFingerprint: "fp-one", Status: "rate_limited", CooldownUntil: now.Add(time.Minute).Format(time.RFC3339Nano)},
			{KeyIndex: 3, KeyFingerprint: "fp-two", Status: "active"},
		},
	}
	selected, ok, err := SelectProbeAPIKey(candidate, now)
	if err != nil || !ok || selected.Secret() != "key-two" || selected.Index() != 3 || selected.Fingerprint() != "fp-two" {
		t.Fatalf("selected=%#v ok=%v error=%v", selected, ok, err)
	}
	if fmt.Sprintf("%v/%#v", selected, selected) != "[REDACTED]/[REDACTED]" {
		t.Fatalf("selected formatting leaked: %v/%#v", selected, selected)
	}
}

func TestSelectProbeAPIKeyFailsClosedForRuntimeCredentialDrift(t *testing.T) {
	candidate := Candidate{
		Credentials:   NewCredentialSet(map[string]any{"api_key": "key"}),
		APIKeyRuntime: []APIKeyRuntime{{KeyIndex: 9, KeyFingerprint: "fingerprint", Status: "active"}},
	}
	if _, _, err := SelectProbeAPIKey(candidate, time.Now()); err == nil {
		t.Fatal("runtime credential drift error = nil")
	}
}

func TestSelectProbeAPIKeyReturnsUnavailableWhenEveryKeyIsCooling(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	candidate := Candidate{
		Credentials:   NewCredentialSet(map[string]any{"api_key": "key"}),
		APIKeyRuntime: []APIKeyRuntime{{KeyIndex: 0, KeyFingerprint: "fingerprint", Status: "rate_limited", CooldownUntil: now.Add(time.Minute).Format(time.RFC3339Nano)}},
	}
	if _, ok, err := SelectProbeAPIKey(candidate, now); err != nil || ok {
		t.Fatalf("selection ok=%v error=%v", ok, err)
	}
}
