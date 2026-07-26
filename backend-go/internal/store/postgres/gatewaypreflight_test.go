package postgres

import (
	"strings"
	"testing"
)

func TestGatewayPreflightSQLContractIsParameterizedBoundedAndCandidateFree(t *testing.T) {
	apiKeySQL := strings.ToLower(gatewayPreflightAPIKeySQL)
	for _, required := range []string{"api_keys.key_hash = $1", "limit 1", "system_accounts.status", "route_strategies.status", "route_dispatch_generation.generation", "cross join juhe_business.gateway_route_dispatch_generations"} {
		if !strings.Contains(apiKeySQL, required) {
			t.Fatalf("API key SQL missing %q:\n%s", required, gatewayPreflightAPIKeySQL)
		}
	}

	bindingsSQL := strings.ToLower(gatewayPreflightBindingsSQL)
	for _, required := range []string{
		"$1::text as api_key_id", "group_authorization.expires_at > $2", "route_strategies.id = $3",
		"group_authorization.resource_owner_system_account_id = groups.system_account_id",
		"else group_authorization.expires_at", "end as access_expires_at",
		"route_strategies.system_account_id = $4", "order by route_strategy_groups.priority asc, route_strategy_groups.created_at asc, route_strategy_groups.id asc",
		"limit $5",
	} {
		if !strings.Contains(bindingsSQL, required) {
			t.Fatalf("bindings SQL missing %q:\n%s", required, gatewayPreflightBindingsSQL)
		}
	}

	settingsSQL := strings.ToLower(gatewayPreflightSettingsSQL)
	for _, required := range []string{"system_account_id = 'sys_admin'", "key = any($1::text[])", "order by key asc", "limit $2"} {
		if !strings.Contains(settingsSQL, required) {
			t.Fatalf("settings SQL missing %q:\n%s", required, gatewayPreflightSettingsSQL)
		}
	}

	combined := apiKeySQL + "\n" + bindingsSQL + "\n" + settingsSQL
	for _, forbidden := range []string{
		"juhe_business.accounts", "juhe_business.group_accounts", "usage_records", "usage_stats_", "for update",
		"insert ", "update ", "delete ",
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("gateway preflight SQL must not contain %q", forbidden)
		}
	}
}

func TestGatewayPreflightSettingsRequireFixedKeySet(t *testing.T) {
	values := map[string]string{
		"gatewayTextRawBodyLimitMegabytes":           "16",
		"defaultTemporaryUnschedulableMinutes":       "2",
		"temporaryUnschedulableRetryIntervalSeconds": "3",
		"temporaryUnschedulableRetryAttempts":        "3",
		"textFirstResponseTimeoutSeconds":            "120",
		"textStreamIdleTimeoutSeconds":               "30",
		"textUncommittedAttemptMaxLifetimeSeconds":   "1800",
		"imageFirstResponseTimeoutSeconds":           "600",
		"imageStreamIdleTimeoutSeconds":              "120",
		"imageUncommittedAttemptMaxLifetimeSeconds":  "3600",
		"noAvailableAccountWaitTimeoutSeconds":       "270",
		"streamFailureThresholdCount":                "3",
		"streamFailureThresholdWindowMinutes":        "5",
	}
	settings, err := gatewayPreflightSettingsFromValues(values)
	if err != nil {
		t.Fatalf("gatewayPreflightSettingsFromValues() error = %v", err)
	}
	if settings.TextFirstResponseTimeoutSeconds != 120 || settings.StreamFailureThresholdWindowMinutes != 5 {
		t.Fatalf("settings = %+v", settings)
	}
	delete(values, "textFirstResponseTimeoutSeconds")
	if _, err := gatewayPreflightSettingsFromValues(values); err == nil || !strings.Contains(err.Error(), "textFirstResponseTimeoutSeconds") {
		t.Fatalf("missing setting error = %v", err)
	}
}
