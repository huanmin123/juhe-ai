package postgres

import (
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestGatewayAccountPolicySQLKeepsMutationRevisionOutboxAndDirtyAtomic(t *testing.T) {
	for name, sql := range map[string]string{
		"lock":          lockGatewayAccountPolicyRowsSQL,
		"binding":       lockGatewayAccountPolicyBindingSQL,
		"authorization": lockGatewayAccountPolicyAuthorizationSQL,
		"cooldown":      applyGatewayAccountPolicyCooldownSQL,
		"disable":       applyGatewayAccountPolicyDisableSQL,
		"outbox":        insertGatewayAccountPolicyOutboxSQL,
		"family":        advanceGatewayAccountPolicyFamilyRevisionSQL,
		"dirty":         markGatewayAccountPolicyStatsDirtySQL,
	} {
		if strings.Contains(sql, "account_api_key_runtime_states") {
			t.Fatalf("%s SQL must not mutate API-key runtime", name)
		}
	}
	for _, fragment := range []string{"dispatch_revision = dispatch_revision + 1", "dispatch_revision = $2::bigint", "RETURNING dispatch_revision"} {
		if !strings.Contains(advanceGatewayAccountPolicyFamilyRevisionSQL, fragment) {
			t.Fatalf("family revision SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"ORDER BY id ASC", "FOR UPDATE", "config_revision", "dispatch_revision", "authorization_instance_source_account_id = $2::text"} {
		if !strings.Contains(lockGatewayAccountPolicyRowsSQL, fragment) {
			t.Fatalf("lock SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"group_accounts.enabled = true", "account_authorization_id IS NOT DISTINCT FROM", "FOR UPDATE OF group_accounts"} {
		if !strings.Contains(lockGatewayAccountPolicyBindingSQL, fragment) {
			t.Fatalf("binding SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{"resource_authorizations.status = 'active'", "resource_authorizations.expires_at >", "FOR UPDATE OF resource_authorizations"} {
		if !strings.Contains(lockGatewayAccountPolicyAuthorizationSQL, fragment) {
			t.Fatalf("authorization SQL missing %q", fragment)
		}
	}
	for name, sql := range map[string]string{"cooldown": applyGatewayAccountPolicyCooldownSQL, "disable": applyGatewayAccountPolicyDisableSQL} {
		for _, fragment := range []string{"dispatch_revision = dispatch_revision + 1", "status = $", "schedulable = true", "config_revision = $", "dispatch_revision = $", "deleted_at IS NULL", "RETURNING dispatch_revision"} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("%s SQL missing %q", name, fragment)
			}
		}
	}
	for _, fragment := range []string{"'dispatch_revision_changed'", "'pending'", "available_at_ms", "attempt_count"} {
		if !strings.Contains(insertGatewayAccountPolicyOutboxSQL, fragment) {
			t.Fatalf("outbox SQL missing %q", fragment)
		}
	}
	if !strings.Contains(markGatewayAccountPolicyStatsDirtySQL, "SELECT DISTINCT group_accounts.group_id") || !strings.Contains(markGatewayAccountPolicyStatsDirtySQL, "authorization_instance_source_account_id = $1::text") || !strings.Contains(markGatewayAccountPolicyStatsDirtySQL, "ON CONFLICT (group_id) DO UPDATE") {
		t.Fatal("dirty SQL must mark direct and source-authorized groups idempotently")
	}
}

func TestGatewayAccountPolicyFamilyTransitionIsStableAndInstanceSpecific(t *testing.T) {
	first := gatewayAccountPolicyFamilyTransitionID("gateway-policy:v1:root", "instance_1")
	repeated := gatewayAccountPolicyFamilyTransitionID("gateway-policy:v1:root", "instance_1")
	other := gatewayAccountPolicyFamilyTransitionID("gateway-policy:v1:root", "instance_2")
	if first != repeated || first == other || !strings.HasPrefix(first, "gateway-policy-family:v1:") || len(first) > 247 {
		t.Fatalf("family transitions = %q / %q / %q", first, repeated, other)
	}
}

func TestValidateGatewayAccountPolicyWriteInputRequiresBoundedCASFacts(t *testing.T) {
	now := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	input := validGatewayAccountPolicyWriteInput(now)
	if err := validateGatewayAccountPolicyWriteInput(input); err != nil {
		t.Fatal(err)
	}

	invalid := input
	invalid.Target.ExpectedDispatchRevision = 0
	if err := validateGatewayAccountPolicyWriteInput(invalid); err == nil {
		t.Fatal("expected invalid target revision")
	}
	invalid = input
	invalid.CooldownUntil = timePointer(now)
	if err := validateGatewayAccountPolicyWriteInput(invalid); err == nil {
		t.Fatal("expected non-future cooldown rejection")
	}
	invalid = input
	invalid.Action = port.GatewayAccountPolicyDisable
	if err := validateGatewayAccountPolicyWriteInput(invalid); err == nil {
		t.Fatal("expected disable with cooldown rejection")
	}
	invalid = input
	invalid.Target.AccountID = " account_1"
	if err := validateGatewayAccountPolicyWriteInput(invalid); err == nil {
		t.Fatal("expected non-canonical identity rejection")
	}
}

func TestGatewayAccountPolicyEligibilityKeepsAuthorizedMutationLocal(t *testing.T) {
	now := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	input := validGatewayAccountPolicyWriteInput(now)
	input.Target.AccountID = "instance_1"
	input.Target.SystemAccountID = "grantee_1"
	input.Target.AccountAuthorizationID = "authorization_1"
	input.Target.AuthorizationSourceAccountID = "source_1"
	input.Target.AuthorizationOwnerSystemAccountID = "owner_1"
	input.Target.AccountRuntimeKey = "instance_1"
	input.Source.AccountID = "source_1"
	target := gatewayAccountPolicyLockedRow{
		id: "instance_1", systemAccountID: "grantee_1", status: "active", schedulable: true,
		configRevision: 3, dispatchRevision: 4, authorizationSource: "source_1", authorizationID: "authorization_1", authorizationOwnerID: "owner_1",
	}
	source := gatewayAccountPolicyLockedRow{id: "source_1", systemAccountID: "owner_1", status: "active", schedulable: true, configRevision: 8, dispatchRevision: 9}
	if !gatewayAccountPolicyTargetEligible(target, source, input) {
		t.Fatal("valid authorized target/source should remain eligible")
	}
	source.systemAccountID = "other_owner"
	if gatewayAccountPolicyTargetEligible(target, source, input) {
		t.Fatal("source owner drift must make mutation ineligible")
	}
	source.systemAccountID = "owner_1"
	source.status = "rate_limited"
	if gatewayAccountPolicyTargetEligible(target, source, input) {
		t.Fatal("authorized mutation must not apply after the physical source became unavailable")
	}
}

func validGatewayAccountPolicyWriteInput(now time.Time) port.GatewayAccountPolicyWriteInput {
	until := now.Add(5 * time.Minute)
	return port.GatewayAccountPolicyWriteInput{
		TransitionID: "gateway-policy:req_1:attempt_1:cooldown",
		Target: port.GatewayAccountPolicyTarget{
			GatewayAccountPolicyRevisionFence: port.GatewayAccountPolicyRevisionFence{AccountID: "account_1", ExpectedConfigRevision: 3, ExpectedDispatchRevision: 4},
			SystemAccountID:                   "owner_1", GroupID: "group_1", AccountRuntimeKey: "account_1", ExpectedStatus: "active",
		},
		Source:         port.GatewayAccountPolicyRevisionFence{AccountID: "account_1", ExpectedConfigRevision: 3, ExpectedDispatchRevision: 4},
		Action:         port.GatewayAccountPolicyCooldown,
		CooldownStatus: port.GatewayAccountPolicyRateLimited,
		CooldownUntil:  &until,
		Reason:         "account policy matched",
		AppliedAt:      now,
	}
}

func timePointer(value time.Time) *time.Time { return &value }
