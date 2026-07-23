package gatewayattemptloop

import (
	"testing"
	"time"
)

func TestDecidePolicyMatchesPriorityAndAllConfiguredDimensions(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 30, 0, 0, time.UTC)
	rules := []any{
		rule(map[string]any{"name": "later", "priority": float64(20), "status_codes": []any{float64(429)}, "action": "error_disabled"}),
		rule(map[string]any{
			"name": "specific", "priority": float64(10), "status_codes": []any{float64(429)},
			"error_codes": []any{"rate_limit"}, "error_types": []any{"quota"}, "keywords": []any{"retry after"},
			"action": "rate_limited", "reset_strategy": "duration", "duration_hours": float64(2),
		}),
	}
	decision, err := DecidePolicy(rules, FailureFacts{StatusCode: 429, ErrorCode: "RATE_LIMIT", ErrorType: "Quota", BodyText: "Retry after 30 seconds"}, PolicySettings{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != PolicyActionCooldown || decision.RuleName != "specific" || decision.CooldownStatus != CooldownRateLimited || decision.CooldownUntil == nil || !decision.CooldownUntil.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecidePolicyKeepsUnconfiguredAndSuccessResponsesOpaque(t *testing.T) {
	for _, failure := range []FailureFacts{{StatusCode: 200, BodyText: "failed"}, {StatusCode: 503, BodyText: "failed"}} {
		decision, err := DecidePolicy(nil, failure, PolicySettings{}, time.Now())
		if err != nil || decision.Action != PolicyActionNone {
			t.Fatalf("decision = %+v err=%v", decision, err)
		}
	}
}

func TestDecidePolicyComputesTemporaryDailyAndWeeklyCooldowns(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 7, 23, 10, 30, 0, 0, location)
	tests := []struct {
		name string
		rule map[string]any
		want time.Time
	}{
		{name: "temporary", rule: rule(map[string]any{"action": "temp_unschedulable"}), want: now.Add(7 * time.Minute)},
		{name: "daily", rule: rule(map[string]any{"action": "rate_limited", "reset_strategy": "daily", "daily_reset_hour": float64(8)}), want: time.Date(2026, 7, 24, 8, 0, 0, 0, location)},
		{name: "weekly", rule: rule(map[string]any{"action": "rate_limited", "reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(6)}), want: time.Date(2026, 7, 27, 6, 0, 0, 0, location)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := DecidePolicy([]any{testCase.rule}, FailureFacts{StatusCode: 429}, PolicySettings{DefaultTemporaryCooldown: 7 * time.Minute}, now)
			if err != nil || decision.CooldownUntil == nil || !decision.CooldownUntil.Equal(testCase.want) {
				t.Fatalf("decision = %+v err=%v want=%s", decision, err, testCase.want)
			}
		})
	}
}

func TestDecidePolicyRejectsInvalidRuntimeRules(t *testing.T) {
	tests := []any{
		map[string]any{"enabled": true},
		rule(map[string]any{"status_codes": []any{float64(200)}}),
		rule(map[string]any{"status_codes": "429"}),
		rule(map[string]any{"action": "future"}),
		rule(map[string]any{"action": "rate_limited", "reset_strategy": "weekly", "weekly_reset_day": float64(8), "weekly_reset_hour": float64(0)}),
	}
	for index, value := range tests {
		if _, err := DecidePolicy([]any{value}, FailureFacts{StatusCode: 429}, PolicySettings{}, time.Now()); err == nil {
			t.Fatalf("case %d error = nil", index)
		}
	}
}

func TestNormalizePolicyDecisionRejectsNonCanonicalExternalMutations(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	tests := []PolicyDecision{
		{Action: PolicyAction("future"), RuleName: "rule"},
		{Action: PolicyActionCooldown, RuleName: "rule", CooldownStatus: CooldownRateLimited},
		{Action: PolicyActionCooldown, RuleName: "rule", CooldownStatus: CooldownStatus("future"), CooldownUntil: &future},
		{Action: PolicyActionDisable, RuleName: "rule", CooldownUntil: &future},
		{Action: PolicyActionRetryNext},
		{Action: PolicyActionNone, RuleName: "stale"},
	}
	for index, decision := range tests {
		if _, err := normalizePolicyDecision(decision, now); err == nil {
			t.Fatalf("case %d error = nil", index)
		}
	}
}

func TestNormalizePolicyDecisionClonesAndNormalizesCooldown(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	location := time.FixedZone("UTC+8", 8*60*60)
	until := now.Add(time.Hour).In(location)
	decision, err := normalizePolicyDecision(PolicyDecision{
		Action: PolicyActionCooldown, RuleName: " rule ", CooldownStatus: CooldownRateLimited, CooldownUntil: &until,
	}, now)
	if err != nil || decision.RuleName != "rule" || decision.CooldownUntil == nil || decision.CooldownUntil.Location() != time.UTC || !decision.CooldownUntil.Equal(until) {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if decision.CooldownUntil == &until {
		t.Fatal("cooldown pointer was not detached")
	}
}

func rule(overrides map[string]any) map[string]any {
	value := map[string]any{
		"enabled": true, "name": "rule", "priority": float64(10),
		"status_codes": []any{float64(429)}, "action": "retry_next",
	}
	for key, item := range overrides {
		value[key] = item
	}
	return value
}
