package postgres

import (
	"errors"
	"fmt"
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
		{
			name: "missing provider takes priority over missing system account",
			want: port.ErrManagementGroupProviderNotFound,
		},
		{
			name:            "disabled provider takes priority over missing system account",
			providerExists:  true,
			providerEnabled: false,
			want:            port.ErrManagementGroupProviderDisabled,
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

func TestManagementGroupUpdateSQLLocksAndSeparatesOwnerAuthorization(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_update.sql")
	if err != nil {
		t.Fatalf("read management group update query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"-- name: FindManagementGroupUpdateProvider :one",
		"FOR SHARE",
		"-- name: LockManagementGroupUpdateTarget :one",
		"'owner'::text",
		"'authorized'::text",
		"resource_authorizations.resource_owner_system_account_id = groups.system_account_id",
		"FOR UPDATE OF groups",
		"-- name: LockManagementGroupUpdateAuthorization :one",
		"FOR UPDATE OF resource_authorizations",
		"-- name: LockManagementGroupUpdateAuthorizationSettings :one",
		"FROM juhe_business.group_authorization_settings",
		"FOR UPDATE;",
		"-- name: CountManagementGroupUpdateAccounts :one",
		"FROM juhe_business.group_accounts AS group_accounts",
		"AND group_accounts.enabled = true",
		"AND accounts.deleted_at IS NULL",
		"account_authorizations.id = group_accounts.account_authorization_id",
		"account_authorizations.id = accounts.authorization_instance_authorization_id",
		"OR account_authorizations.id IS NOT NULL",
		"-- name: LockManagementGroupUpdateRouteStrategies :many",
		"sqlc.arg(all_scopes)::boolean",
		"OR target_bindings.system_account_id = sqlc.arg(effective_system_account_id)::text",
		"LIMIT 101",
		"FOR UPDATE OF route_strategies",
		"-- name: CountManagementGroupUpdateRouteStrategyLoss :one",
		"route_strategies.id = ANY(sqlc.arg(route_strategy_ids)::text[])",
		"other_authorization.status = 'active'",
		"coalesce(other_settings.enabled, true) = true",
		"-- name: UpdateManagementGroupOwner :one",
		"-- name: UpsertManagementGroupAuthorizationSettings :one",
		"ON CONFLICT (authorization_id) DO UPDATE",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management group update SQL missing %q", want)
		}
	}
	if got := strings.Count(sql, "sqlc.arg(all_scopes)::boolean"); got != 2 {
		t.Fatalf("management group update SQL all-scopes guards = %d, want 2", got)
	}
	if got := strings.Count(sql, "other_bindings.system_account_id = target_bindings.system_account_id"); got != 1 {
		t.Fatalf("management group update SQL binding-scope alternatives = %d, want 1", got)
	}
}

func TestManagementGroupUpdateProviderValidationPrecedesTargetLookup(t *testing.T) {
	source, err := os.ReadFile("managementgroups.go")
	if err != nil {
		t.Fatalf("read management groups store: %v", err)
	}
	goSource := string(source)
	providerIndex := strings.Index(goSource, "q.FindManagementGroupUpdateProvider")
	targetIndex := strings.Index(goSource, "q.LockManagementGroupUpdateTarget")
	if providerIndex < 0 || targetIndex < 0 {
		t.Fatalf("management group update store is missing provider or target query")
	}
	if providerIndex >= targetIndex {
		t.Fatalf("provider validation index = %d, target lookup index = %d", providerIndex, targetIndex)
	}
}

func TestManagementGroupUpdateKeepsPathGroupIDExact(t *testing.T) {
	source, err := os.ReadFile("managementgroups.go")
	if err != nil {
		t.Fatalf("read management groups store: %v", err)
	}
	goSource := string(source)
	if !strings.Contains(goSource, "GroupID:                  input.GroupID") {
		t.Fatal("management group update must pass the path group ID unchanged")
	}
	if strings.Contains(goSource, "strings.TrimSpace(input.GroupID)") {
		t.Fatal("management group update must not trim the path group ID")
	}
}

func TestManagementGroupUpdateStoreMapsRequiredGuards(t *testing.T) {
	source, err := os.ReadFile("managementgroups.go")
	if err != nil {
		t.Fatalf("read management groups store: %v", err)
	}
	goSource := string(source)
	for _, want := range []string{
		"port.ErrManagementGroupNotFound",
		"port.ErrManagementGroupDefaultReadonly",
		"q.CountManagementGroupUpdateAccounts",
		"port.ErrManagementGroupProviderHasAccounts",
		"managementGroupDuplicateNameError(err)",
		"port.ErrManagementGroupNameExists",
		"port.ErrManagementGroupAuthorizedFields",
		"q.LockManagementGroupUpdateAuthorization",
		"q.LockManagementGroupUpdateAuthorizationSettings",
		"guardManagementGroupUpdateRouteStrategies",
		"port.ErrManagementGroupRouteStrategyWouldLose",
	} {
		if !strings.Contains(goSource, want) {
			t.Fatalf("management group update store missing %q", want)
		}
	}
}

func TestManagementGroupUpdateEffectiveScope(t *testing.T) {
	tests := []struct {
		name  string
		input port.ManagementGroupUpdateInput
		want  string
	}{
		{
			name: "resolved target scope",
			input: port.ManagementGroupUpdateInput{
				ActorSystemAccountID:     " sys_user ",
				EffectiveSystemAccountID: "sys_other",
			},
			want: "sys_other",
		},
		{
			name: "self scope falls back to actor",
			input: port.ManagementGroupUpdateInput{
				ActorSystemAccountID: " sys_user ",
			},
			want: "sys_user",
		},
		{
			name: "admin global scope",
			input: port.ManagementGroupUpdateInput{
				ActorSystemAccountID: "sys_admin",
				CanAccessAll:         true,
			},
		},
		{
			name: "admin target scope",
			input: port.ManagementGroupUpdateInput{
				ActorSystemAccountID:     "sys_admin",
				CanAccessAll:             true,
				EffectiveSystemAccountID: " sys_target ",
			},
			want: "sys_target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managementGroupUpdateEffectiveSystemAccountID(tt.input); got != tt.want {
				t.Fatalf("effective scope = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagementGroupAuthorizedFieldsError(t *testing.T) {
	if err := managementGroupAuthorizedFieldsError(port.ManagementGroupUpdateInput{
		HasEnabled:          true,
		HasGroupType:        true,
		HasSchedulingPolicy: true,
	}); err != nil {
		t.Fatalf("allowed authorized fields error = %v", err)
	}
	err := managementGroupAuthorizedFieldsError(port.ManagementGroupUpdateInput{
		HasName:         true,
		HasProviderCode: true,
		HasDescription:  true,
	})
	if !errors.Is(err, port.ErrManagementGroupAuthorizedFields) {
		t.Fatalf("authorized fields error = %v", err)
	}
	const want = "management group authorized fields: 授权分组使用配置包含未知字段：name、providerCode、description"
	if err.Error() != want {
		t.Fatalf("authorized fields error = %q, want %q", err.Error(), want)
	}
}

func TestManagementGroupUpdateNameExistsErrorIncludesNextName(t *testing.T) {
	err := managementGroupUpdateNameExistsError("重名分组")
	if !errors.Is(err, port.ErrManagementGroupNameExists) {
		t.Fatalf("name exists error = %v", err)
	}
	if err.Error() != "management group name exists: 重名分组" {
		t.Fatalf("name exists error = %q", err.Error())
	}
}

func TestManagementGroupUpdateRouteStrategyLimitError(t *testing.T) {
	if err := managementGroupUpdateRouteStrategyLimitError(100, "停用分组"); err != nil {
		t.Fatalf("100 route strategies error = %v", err)
	}
	err := managementGroupUpdateRouteStrategyLimitError(101, "停用分组")
	if !errors.Is(err, port.ErrManagementGroupRouteStrategyWouldLose) {
		t.Fatalf("route strategy limit error = %v", err)
	}
	const want = "management group route strategy would lose: 该分组关联的策略路由超过 100 个，请先分批解除绑定后再停用分组"
	if err.Error() != want {
		t.Fatalf("route strategy limit error = %q, want %q", err.Error(), want)
	}
}

func TestManagementGroupUpdateRouteStrategyScopes(t *testing.T) {
	owner := managementGroupUpdateOwnerRouteStrategyScope()
	if !owner.allScopes || owner.effectiveSystemAccountID != "" {
		t.Fatalf("owner route strategy scope = %#v, want all scopes", owner)
	}

	authorized := managementGroupUpdateAuthorizedRouteStrategyScope("sys_grantee")
	if authorized.allScopes || authorized.effectiveSystemAccountID != "sys_grantee" {
		t.Fatalf("authorized route strategy scope = %#v, want grantee scope", authorized)
	}
}

func TestManagementGroupUpdateSchedulingPolicyMerge(t *testing.T) {
	current := `{"mode":"balanced_fast","defaultSoftConcurrency":7}`
	explicit := `{"mode":"balanced_fast","defaultSoftConcurrency":9}`
	defaultPolicy := fullHighConcurrencyPolicyJSON()
	tests := []struct {
		name        string
		currentType string
		currentJSON *string
		nextType    string
		input       port.ManagementGroupUpdateInput
		want        *string
	}{
		{
			name:        "high concurrency preserves full current policy",
			currentType: "high_concurrency",
			currentJSON: &current,
			nextType:    "high_concurrency",
			input:       port.ManagementGroupUpdateInput{DefaultSchedulingPolicyJSON: defaultPolicy},
			want:        &current,
		},
		{
			name:        "personal to high concurrency uses service default",
			currentType: "personal",
			nextType:    "high_concurrency",
			input:       port.ManagementGroupUpdateInput{DefaultSchedulingPolicyJSON: defaultPolicy},
			want:        &defaultPolicy,
		},
		{
			name:        "explicit policy wins",
			currentType: "high_concurrency",
			currentJSON: &current,
			nextType:    "high_concurrency",
			input: port.ManagementGroupUpdateInput{
				HasSchedulingPolicy:  true,
				SchedulingPolicyJSON: &explicit,
			},
			want: &explicit,
		},
		{
			name:        "personal clears policy",
			currentType: "high_concurrency",
			currentJSON: &current,
			nextType:    "personal",
			input:       port.ManagementGroupUpdateInput{DefaultSchedulingPolicyJSON: defaultPolicy},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := managementGroupUpdateSchedulingPolicy(
				tt.currentType,
				tt.currentJSON,
				tt.nextType,
				tt.input,
			)
			switch {
			case got == nil && tt.want == nil:
			case got == nil || tt.want == nil:
				t.Fatalf("policy = %#v, want %#v", got, tt.want)
			case *got != *tt.want:
				t.Fatalf("policy = %q, want %q", *got, *tt.want)
			}
		})
	}
}

func TestManagementGroupUpdateSentinelsSupportErrorsIs(t *testing.T) {
	sentinels := []error{
		port.ErrManagementGroupNotFound,
		port.ErrManagementGroupDefaultReadonly,
		port.ErrManagementGroupProviderHasAccounts,
		port.ErrManagementGroupAuthorizedFields,
		port.ErrManagementGroupRouteStrategyWouldLose,
	}
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("wrapped: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("errors.Is(%v) = false", sentinel)
		}
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
