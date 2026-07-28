package postgres

import (
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/store/port"
)

func TestCooldownAccountRetestOutcomeSQLKeepsFenceMutationOutboxAndStatsAtomic(t *testing.T) {
	for name, sql := range map[string]string{
		"lock accounts": lockCooldownAccountRetestOutcomeAccountsSQL,
		"find binding":  findCooldownAccountRetestOutcomeBindingSQL,
		"find auth":     findCooldownAccountRetestOutcomeAuthorizationSQL,
		"success":       restoreCooldownAccountRetestOutcomeSQL,
		"defer":         deferCooldownAccountRetestOutcomeSQL,
		"failure":       failCooldownAccountRetestOutcomeSQL,
		"family":        advanceCooldownAccountRetestFamilySQL,
		"outbox":        insertCooldownAccountRetestOutcomeOutboxSQL,
		"dirty":         markCooldownAccountRetestOutcomeStatsDirtySQL,
	} {
		if strings.Contains(sql, "account_api_key_runtime_states") {
			t.Fatalf("%s SQL must not mutate API-key sibling runtime", name)
		}
	}
	for name, test := range map[string]struct {
		sql         string
		lastBinding string
	}{
		"success": {sql: restoreCooldownAccountRetestOutcomeSQL, lastBinding: "$10::text"},
		"defer":   {sql: deferCooldownAccountRetestOutcomeSQL, lastBinding: "$11::text"},
		"failure": {sql: failCooldownAccountRetestOutcomeSQL, lastBinding: "$19::text"},
	} {
		if !strings.Contains(test.sql, test.lastBinding) {
			t.Fatalf("%s SQL missing final mutation fence placeholder %q", name, test.lastBinding)
		}
	}
	for _, fragment := range []string{"ORDER BY accounts.id ASC", "LIMIT $3::integer", "FOR UPDATE OF accounts", "authorization_instance_source_account_id", "cooldown_retest_generation", "dispatch_revision"} {
		if !strings.Contains(lockCooldownAccountRetestOutcomeAccountsSQL, fragment) {
			t.Fatalf("account lock SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"jsonb_to_recordset", "UPDATE juhe_business.accounts", "INSERT INTO juhe_business.account_circuit_outbox", "accounts.dispatch_revision = batch.expected_dispatch_revision", "SELECT count(*) FROM updated", "SELECT count(*) FROM inserted"} {
		if !strings.Contains(advanceCooldownAccountRetestFamilySQL, fragment) {
			t.Fatalf("family batch SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"enabled = true", "account_authorization_id IS NOT DISTINCT FROM", "LIMIT 1"} {
		if !strings.Contains(findCooldownAccountRetestOutcomeBindingSQL, fragment) {
			t.Fatalf("binding lookup SQL missing %q", fragment)
		}
	}
	if strings.Contains(findCooldownAccountRetestOutcomeBindingSQL, "FOR UPDATE") {
		t.Fatal("binding lookup must not invert the authorization-maintenance auth-to-account lock order")
	}
	for _, fragment := range []string{"resource_type = 'account'", "status = 'active'", "expires_at >"} {
		if !strings.Contains(findCooldownAccountRetestOutcomeAuthorizationSQL, fragment) {
			t.Fatalf("authorization lookup SQL missing %q", fragment)
		}
	}
	if strings.Contains(findCooldownAccountRetestOutcomeAuthorizationSQL, "FOR UPDATE") {
		t.Fatal("outcome must not hold an authorization row lock after locking accounts")
	}
	if strings.Contains(findCooldownAccountRetestOutcomeAuthorizationSQL, "LOCK TABLE") || strings.Contains(findCooldownAccountRetestOutcomeBindingSQL, "LOCK TABLE") {
		t.Fatal("outcome must not add a unilateral table lock without all competing writers sharing the protocol")
	}
	if selectCooldownAccountRetestOutcomeNowSQL != "SELECT clock_timestamp()" {
		t.Fatalf("outcome time must be sampled from PostgreSQL after locks, got %q", selectCooldownAccountRetestOutcomeNowSQL)
	}
	for name, sql := range map[string]string{
		"success": restoreCooldownAccountRetestOutcomeSQL,
		"defer":   deferCooldownAccountRetestOutcomeSQL,
		"failure": failCooldownAccountRetestOutcomeSQL,
	} {
		for _, fragment := range []string{
			"config_revision = $", "dispatch_revision = $", "cooldown_retest_observation_started_at = $",
			"cooldown_retest_generation = $", "dispatch_revision = dispatch_revision + 1", "deleted_at IS NULL",
			"RETURNING dispatch_revision",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("%s SQL missing five-fence/control-plane fragment %q", name, fragment)
			}
		}
		for _, fragment := range []string{"EXISTS (", "FROM juhe_business.group_accounts", "FROM juhe_business.resource_authorizations", "status = 'active'", "authorization_instance_source_account_id"} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("%s SQL missing mutation-time authorization/binding fence %q", name, fragment)
			}
		}
	}
	for _, fragment := range []string{
		"status = 'active'", "cooldown_until = NULL", "last_error_code = NULL",
		"cooldown_retest_failure_count = 0", "cooldown_retest_observation_started_at = NULL",
		"cooldown_retest_generation = NULL", "cooldown_retest_last_at = NULL",
	} {
		if !strings.Contains(restoreCooldownAccountRetestOutcomeSQL, fragment) {
			t.Fatalf("success SQL missing Node field semantic %q", fragment)
		}
	}
	if strings.Contains(restoreCooldownAccountRetestOutcomeSQL, "stream_failure_count") {
		t.Fatal("success must not clear stream failure diagnostics; Node only clears them on failure")
	}
	for _, fragment := range []string{
		"status = CASE", "schedulable = CASE", "cooldown_retest_failure_count = $7",
		"cooldown_retest_last_at = $9", "cooldown_retest_last_status_code = $10",
		"stream_failure_count = 0", "stream_failure_window_started_at = NULL",
	} {
		if !strings.Contains(failCooldownAccountRetestOutcomeSQL, fragment) {
			t.Fatalf("failure SQL missing Node field semantic %q", fragment)
		}
	}
	if !strings.Contains(deferCooldownAccountRetestOutcomeSQL, "cooldown_until < $2") {
		t.Fatal("neutral defer must never shorten an existing cooldown")
	}
	for _, fragment := range []string{"'dispatch_revision_changed'", "'pending'", "attempt_count", "available_at_ms"} {
		if !strings.Contains(insertCooldownAccountRetestOutcomeOutboxSQL, fragment) {
			t.Fatalf("outbox SQL missing %q", fragment)
		}
	}
	if !strings.Contains(markCooldownAccountRetestOutcomeStatsDirtySQL, "authorization_instance_source_account_id = $1::text") ||
		!strings.Contains(markCooldownAccountRetestOutcomeStatsDirtySQL, "ORDER BY group_accounts.group_id ASC") ||
		!strings.Contains(markCooldownAccountRetestOutcomeStatsDirtySQL, "ON CONFLICT (group_id) DO UPDATE") {
		t.Fatal("stats dirty SQL must include direct/source-authorized groups and lock dirty rows in stable order")
	}
}

func TestCooldownAccountRetestOutcomeTransitionIDsAreStableAndFenceSpecific(t *testing.T) {
	task := validCooldownOutcomeTask()
	first := cooldownAccountRetestOutcomeTransitionID(task, cooldownOutcomeFailure)
	if first != cooldownAccountRetestOutcomeTransitionID(task, cooldownOutcomeFailure) || !strings.HasPrefix(first, "cooldown-retest:v1:") || len("dispatch:"+first) > 256 {
		t.Fatalf("unstable or oversized transition %q", first)
	}
	variants := []struct {
		name string
		task port.CooldownAccountRetestTask
		kind cooldownOutcomeKind
	}{
		{name: "kind", task: task, kind: cooldownOutcomeSuccess},
		{name: "config", task: mutateCooldownOutcomeTask(task, func(value *port.CooldownAccountRetestTask) { value.ConfigRevision++ }), kind: cooldownOutcomeFailure},
		{name: "dispatch", task: mutateCooldownOutcomeTask(task, func(value *port.CooldownAccountRetestTask) { value.DispatchRevision++ }), kind: cooldownOutcomeFailure},
		{name: "observation", task: mutateCooldownOutcomeTask(task, func(value *port.CooldownAccountRetestTask) {
			later := value.ObservationStartedAt.Add(time.Second)
			value.ObservationStartedAt = &later
		}), kind: cooldownOutcomeFailure},
		{name: "generation", task: mutateCooldownOutcomeTask(task, func(value *port.CooldownAccountRetestTask) { value.Generation = "generation-2" }), kind: cooldownOutcomeFailure},
		{name: "source", task: mutateCooldownOutcomeTask(task, func(value *port.CooldownAccountRetestTask) { revision := 12; value.SourceConfigRevision = &revision }), kind: cooldownOutcomeFailure},
	}
	for _, variant := range variants {
		if got := cooldownAccountRetestOutcomeTransitionID(variant.task, variant.kind); got == first {
			t.Fatalf("%s did not change transition ID", variant.name)
		}
	}
	family := cooldownAccountRetestFamilyTransitionID(first, "authorized-1")
	if family != cooldownAccountRetestFamilyTransitionID(first, "authorized-1") || family == cooldownAccountRetestFamilyTransitionID(first, "authorized-2") || len("dispatch:"+family) > 256 {
		t.Fatalf("invalid family transition %q", family)
	}
}

func TestCooldownAccountRetestFamilyBatchHasHardLimit(t *testing.T) {
	if cooldownOutcomeMaxAuthorizedFamilySize != 1000 {
		t.Fatalf("authorized family limit = %d, want 1000", cooldownOutcomeMaxAuthorizedFamilySize)
	}
	if err := validateCooldownOutcomeFamilySize(cooldownOutcomeMaxAuthorizedFamilySize); err != nil {
		t.Fatalf("family at limit rejected: %v", err)
	}
	if err := validateCooldownOutcomeFamilySize(cooldownOutcomeMaxAuthorizedFamilySize + 1); err == nil {
		t.Fatal("family above hard limit accepted")
	}
}

func TestCooldownAccountRetestRecoveryPlanMatchesNodeBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	started := now.Add(-30 * time.Second)
	task := validCooldownOutcomeTask()
	task.MaxPauseMinutes = 1
	task.MaxRecoveryHours = 12
	target := cooldownOutcomeAccountRow{status: "temporary_unavailable", observationStartedAt: &started, continuousProbeEnabled: 1}
	wantBackoff := []int{3, 6, 12, 24, 48, 60, 60}
	for failureCount, want := range wantBackoff {
		target.failureCount = failureCount
		plan := cooldownAccountRetestRecoveryPlan(target, task, now)
		if plan.backoffSeconds != want {
			t.Fatalf("failure %d backoff = %d, want %d", failureCount+1, plan.backoffSeconds, want)
		}
	}
	target.failureCount = -10
	if got := boundedCooldownOutcomeBackoff(max(target.failureCount, 0)+1, task.MaxPauseMinutes*60); got != 3 {
		t.Fatalf("legacy negative count first backoff = %d, want 3", got)
	}

	longTermStarted := now.Add(-12 * time.Hour)
	target.observationStartedAt = &longTermStarted
	plan := cooldownAccountRetestRecoveryPlan(target, task, now)
	if plan.stage != "long_term" || plan.backoffSeconds != 60*60 {
		t.Fatalf("long-term plan = %+v", plan)
	}

	terminalStarted := now.Add(-7 * 24 * time.Hour)
	target.observationStartedAt = &terminalStarted
	plan = cooldownAccountRetestRecoveryPlan(target, task, now)
	if plan.stage != "terminal" || plan.backoffSeconds != 0 || plan.observationTimeoutSeconds != 7*24*60*60 {
		t.Fatalf("terminal plan = %+v", plan)
	}

	limitedStarted := now.Add(-10 * time.Minute)
	target.observationStartedAt = &limitedStarted
	target.continuousProbeEnabled = 0
	plan = cooldownAccountRetestRecoveryPlan(target, task, now)
	if plan.stage != "terminal" || plan.observationTimeoutSeconds != 10*60 {
		t.Fatalf("limited plan = %+v", plan)
	}
}

func TestCooldownAccountRetestFailureNormalizationAndProvenanceMatchNode(t *testing.T) {
	probe := port.CooldownAccountRetestProbeResult{
		StatusCode: 429, ErrorCode: "insufficient_user_quota", Message: "quota exhausted", TraceID: "trace-1",
	}
	code, message, traceID := normalizeCooldownAccountRetestFailure(probe)
	if code != "insufficient_user_quota" || traceID != "trace-1" || message != "traceId trace-1；HTTP 429；insufficient_user_quota；quota exhausted" {
		t.Fatalf("normalized failure = %q / %q / %q", code, message, traceID)
	}
	code, message, _ = normalizeCooldownAccountRetestFailure(port.CooldownAccountRetestProbeResult{StatusCode: 503})
	if code != "http_503" || message != "HTTP 503；后台冷却复测失败" {
		t.Fatalf("HTTP fallback = %q / %q", code, message)
	}

	target := cooldownOutcomeAccountRow{lastErrorMessage: cooldownOutcomeLegacyExplicitPolicyMessageLead + "429」"}
	if got := cooldownAccountRetestPersistedErrorCode(target, false, "fast", "http_429"); got != cooldownOutcomeExplicitPolicyCode {
		t.Fatalf("legacy explicit policy code = %q", got)
	}
	if got := cooldownAccountRetestPersistedErrorCode(target, false, "long_term", "http_429"); got != cooldownOutcomeExplicitPolicyCode {
		t.Fatalf("explicit policy must outrank long-term code, got %q", got)
	}

	long := strings.Repeat("😀", 600)
	truncated := truncateCooldownOutcomeUTF16(long, 1000)
	if got := len(utf16.Encode([]rune(truncated))); got != 1000 {
		t.Fatalf("UTF-16 truncation units = %d, want 1000", got)
	}
	boundary := truncateCooldownOutcomeUTF16(strings.Repeat("a", 999)+"😀", 1000)
	if boundary != strings.Repeat("a", 999)+"�" {
		t.Fatalf("dangling UTF-16 surrogate PostgreSQL encoding boundary = %q", boundary[len(boundary)-4:])
	}
	longTrace := strings.Repeat("t", 250)
	_, message, storedTrace := normalizeCooldownAccountRetestFailure(port.CooldownAccountRetestProbeResult{Message: "failed", TraceID: longTrace})
	if len(storedTrace) != 200 || !strings.Contains(message, longTrace) {
		t.Fatalf("trace normalization did not keep full message evidence and bounded storage: message=%q stored=%d", message, len(storedTrace))
	}
}

func TestCooldownOutcomeTargetCurrentUsesAllFiveFenceParts(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	task := validCooldownOutcomeTask()
	target := cooldownOutcomeAccountRow{
		status: "temporary_unavailable", schedulable: true, configRevision: 7, dispatchRevision: 9,
		observationStartedAt: task.ObservationStartedAt, generation: task.Generation,
	}
	if !cooldownOutcomeTargetCurrent(target, task, now) {
		t.Fatal("current five-part fence rejected")
	}
	tests := map[string]func(*cooldownOutcomeAccountRow){
		"config":   func(value *cooldownOutcomeAccountRow) { value.configRevision++ },
		"dispatch": func(value *cooldownOutcomeAccountRow) { value.dispatchRevision++ },
		"observation": func(value *cooldownOutcomeAccountRow) {
			later := value.observationStartedAt.Add(time.Second)
			value.observationStartedAt = &later
		},
		"generation": func(value *cooldownOutcomeAccountRow) { value.generation = "generation-2" },
		"status":     func(value *cooldownOutcomeAccountRow) { value.status = "active" },
	}
	for name, mutate := range tests {
		changed := target
		mutate(&changed)
		if cooldownOutcomeTargetCurrent(changed, task, now) {
			t.Fatalf("stale %s accepted", name)
		}
	}
}

func TestNormalizeCooldownOutcomeDeferDelayMatchesNodeBounds(t *testing.T) {
	tests := map[time.Duration]time.Duration{
		-time.Second:            3 * time.Second,
		0:                       3 * time.Second,
		3500 * time.Millisecond: 3 * time.Second,
		30 * time.Second:        30 * time.Second,
		20 * time.Minute:        15 * time.Minute,
	}
	for input, want := range tests {
		if got := normalizeCooldownOutcomeDeferDelay(input); got != want {
			t.Fatalf("normalize(%s) = %s, want %s", input, got, want)
		}
	}
}

func validCooldownOutcomeTask() port.CooldownAccountRetestTask {
	started := time.Date(2026, 7, 28, 11, 59, 30, 0, time.UTC)
	return port.CooldownAccountRetestTask{
		AccountID: "account-1", ConfigRevision: 7, DispatchRevision: 9,
		ObservationStartedAt: &started, Generation: "generation-1",
		MaxPauseMinutes: 2, MaxRecoveryHours: 12,
	}
}

func mutateCooldownOutcomeTask(task port.CooldownAccountRetestTask, mutate func(*port.CooldownAccountRetestTask)) port.CooldownAccountRetestTask {
	mutate(&task)
	return task
}
