package postgres

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementGroupOptionLimit(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: -1, want: 50},
		{input: 1, want: 1},
		{input: 50, want: 50},
		{input: 51, want: 50},
	}
	for _, tt := range tests {
		if got := managementGroupOptionLimit(tt.input); got != tt.want {
			t.Fatalf("managementGroupOptionLimit(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestManagementGroupSchedulingPolicy(t *testing.T) {
	personal, err := managementGroupSchedulingPolicy("group_personal", "personal", pgtype.Text{})
	if err != nil {
		t.Fatalf("personal policy error = %v", err)
	}
	if personal != nil {
		t.Fatalf("personal policy = %#v, want nil", personal)
	}

	policy, err := managementGroupSchedulingPolicy("group_high", "high_concurrency", pgtype.Text{String: fullHighConcurrencyPolicyJSON(), Valid: true})
	if err != nil {
		t.Fatalf("high concurrency policy error = %v", err)
	}
	if policy["mode"] != "balanced_fast" || policy["clientIpConcurrencyOverflowMode"] != "reject" {
		t.Fatalf("policy = %#v", policy)
	}

	if _, err := managementGroupSchedulingPolicy("group_missing", "high_concurrency", pgtype.Text{}); err == nil {
		t.Fatal("missing high concurrency policy error = nil")
	}
	if _, err := managementGroupSchedulingPolicy("group_invalid", "high_concurrency", pgtype.Text{String: `{"mode":`, Valid: true}); err == nil {
		t.Fatal("invalid high concurrency policy error = nil")
	}
	if _, err := managementGroupSchedulingPolicy("group_partial", "high_concurrency", pgtype.Text{String: `{"mode":"balanced_fast"}`, Valid: true}); err == nil {
		t.Fatal("partial high concurrency policy error = nil")
	}
}

func TestManagementGroupAuthorizationLimits(t *testing.T) {
	empty, err := managementGroupAuthorizationLimits("group_authorized", pgtype.Text{})
	if err != nil {
		t.Fatalf("empty limits error = %v", err)
	}
	if empty != nil {
		t.Fatalf("empty limits = %#v, want nil", empty)
	}

	limits, err := managementGroupAuthorizationLimits("group_authorized", pgtype.Text{String: `{"daily":{"limit":100}}`, Valid: true})
	if err != nil {
		t.Fatalf("limits error = %v", err)
	}
	if limits["daily"] == nil {
		t.Fatalf("limits = %#v", limits)
	}

	if _, err := managementGroupAuthorizationLimits("group_authorized", pgtype.Text{String: `{"daily":`, Valid: true}); err == nil {
		t.Fatal("invalid limits error = nil")
	}
}

func TestManagementGroupOptionsSQLMarksReturnableManualAuthorization(t *testing.T) {
	for _, path := range []string{
		"queries/w2_management_group_options.sql",
		"queries/w2_management_group_account_options.sql",
	} {
		t.Run(path, func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read group options query: %v", err)
			}
			sql := string(source)
			for _, want := range []string{
				"false AS has_active_manual_authorization_source",
				"FROM juhe_business.resource_authorization_sources AS returnable_sources",
				"returnable_sources.authorization_id = resource_authorizations.id",
				"returnable_sources.source_type = 'manual'",
				"returnable_sources.status = 'active'",
				"group_rows.has_active_manual_authorization_source",
			} {
				if !strings.Contains(sql, want) {
					t.Fatalf("group options query missing %q", want)
				}
			}
		})
	}
}

func TestManagementGroupCreateSQLIsBoundedAndUsesSingleInsert(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_create.sql")
	if err != nil {
		t.Fatalf("read management group create query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"WHERE id = sqlc.arg(system_account_id)::text",
		"FOR KEY SHARE",
		"WHERE code = sqlc.arg(provider_code)::text",
		"FOR SHARE",
		"WHERE target_provider.enabled",
		"false,",
		"sqlc.narg(description)::text",
		"sqlc.narg(scheduling_policy_json)::text",
		"LEFT JOIN inserted ON true",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management group create SQL missing %q", want)
		}
	}
	if count := strings.Count(sql, "INSERT INTO juhe_business.groups"); count != 1 {
		t.Fatalf("management group create INSERT count = %d, want 1", count)
	}
	for _, forbidden := range []string{
		"COUNT(",
		"juhe_business.group_accounts",
		"juhe_stats.",
		"juhe_dataset.",
		"usage_records",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("management group create SQL should not contain %q", forbidden)
		}
	}
}

func TestManagementGroupCreateDependencyError(t *testing.T) {
	tests := []struct {
		name                string
		systemAccountExists bool
		providerExists      bool
		providerEnabled     bool
		want                error
	}{
		{
			name:                "ready",
			systemAccountExists: true,
			providerExists:      true,
			providerEnabled:     true,
		},
		{
			name:            "missing system account",
			providerExists:  true,
			providerEnabled: true,
			want:            port.ErrManagementGroupSystemAccountNotFound,
		},
		{
			name:                "missing provider",
			systemAccountExists: true,
			want:                port.ErrManagementGroupProviderNotFound,
		},
		{
			name:                "disabled provider",
			systemAccountExists: true,
			providerExists:      true,
			want:                port.ErrManagementGroupProviderDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := managementGroupCreateDependencyError(
				tt.systemAccountExists,
				tt.providerExists,
				tt.providerEnabled,
			)
			if tt.want == nil && err != nil {
				t.Fatalf("dependency error = %v, want nil", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("dependency error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestManagementGroupCreateDatabaseErrorMapping(t *testing.T) {
	for _, constraint := range []string{
		"idx_groups_owner_provider_name_unique",
		"idx_groups_owner_provider_name_unique_lower",
	} {
		if !managementGroupDuplicateNameError(&pgconn.PgError{Code: "23505", ConstraintName: constraint}) {
			t.Fatalf("duplicate constraint %q was not recognized", constraint)
		}
	}
	if managementGroupDuplicateNameError(&pgconn.PgError{Code: "23505", ConstraintName: "other_unique"}) {
		t.Fatal("unrelated unique violation should not be recognized")
	}
	if !managementGroupSystemAccountForeignKeyError(&pgconn.PgError{
		Code:           "23503",
		ConstraintName: "groups_system_account_id_fkey",
	}) {
		t.Fatal("system account foreign key violation was not recognized")
	}
	if !managementGroupProviderForeignKeyError(&pgconn.PgError{
		Code:           "23503",
		ConstraintName: "groups_provider_code_fkey",
	}) {
		t.Fatal("provider foreign key violation was not recognized")
	}
	if managementGroupProviderForeignKeyError(&pgconn.PgError{
		Code:           "23503",
		ConstraintName: "other_foreign_key",
	}) {
		t.Fatal("unrelated foreign key violation should not be recognized")
	}
}

func fullHighConcurrencyPolicyJSON() string {
	return `{
		"mode":"balanced_fast",
		"defaultSoftConcurrency":5,
		"fastFirstEnabled":true,
		"fallbackOnQueueEnabled":true,
		"breakAffinityOnSoftLimit":true,
		"breakAffinityOnQueueWaitMs":0,
		"slowRequestThresholdMs":30000,
		"firstOutputSlowThresholdMs":15000,
		"recentTimeoutWindowSeconds":120,
		"recentTimeoutPenaltyThreshold":2,
		"maxQueueWaitMs":60000,
		"maxQueueSize":1000,
		"perApiKeyQueueLimit":1000,
		"clientIpConcurrencyLimit":0,
		"clientIpConcurrencyOverflowMode":"reject",
		"imageLaneMaxConcurrency":0
	}`
}
