package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestGetManagementAccountTestOptionsSourceMapsOwnerGlobalAndNarrowScopes(t *testing.T) {
	want := port.ManagementAccountTestOptionsSource{
		ID:                        "account_owner",
		OwnerSystemAccountID:      "sys_owner",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Type:                      "api_key",
		ClientCompatibility:       "codex_responses",
		HealthCheckModel:          "gpt-5.1-codex-mini",
		HealthCheckEndpointMode:   "responses_sse",
		CredentialsEncrypted:      "v1:owner-ciphertext",
		ModelMappings: []port.ManagementAccountTestModelMapping{
			{
				SourceModel:            "gpt-5-codex",
				SourceEndpointFamily:   "responses",
				UpstreamModel:          "gpt-5.1-codex",
				UpstreamEndpointFamily: "responses",
				Enabled:                true,
			},
			{
				SourceModel:            "gpt-5.1-codex-mini",
				SourceEndpointFamily:   "responses",
				UpstreamModel:          "gpt-5.1-codex-mini",
				UpstreamEndpointFamily: "responses",
				Enabled:                false,
			},
		},
	}

	tests := []struct {
		name            string
		systemAccountID string
		wantScope       string
	}{
		{name: "admin global", systemAccountID: "", wantScope: ""},
		{name: "owner narrow", systemAccountID: " sys_owner ", wantScope: "sys_owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &managementAccountTestOptionsQueriesStub{
				row:         managementAccountTestOptionsRow(want, "account_owner"),
				mappingRows: managementAccountTestOptionsMappingRows(want.ModelMappings),
			}
			got, found, err := getManagementAccountTestOptionsSource(
				context.Background(),
				q,
				port.ManagementAccountTestOptionsInput{
					AccountID:       " account_owner ",
					SystemAccountID: tt.systemAccountID,
				},
			)
			if err != nil {
				t.Fatalf("get source: %v", err)
			}
			if !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("source = %#v, found = %v, want %#v", got, found, want)
			}
			wantParams := postgresqueries.GetManagementAccountTestOptionsSourceParams{
				AccountID:       "account_owner",
				SystemAccountID: tt.wantScope,
			}
			if !reflect.DeepEqual(q.calls, []postgresqueries.GetManagementAccountTestOptionsSourceParams{wantParams}) {
				t.Fatalf("query calls = %#v, want %#v", q.calls, wantParams)
			}
			if !reflect.DeepEqual(q.mappingCalls, []string{"account_owner"}) {
				t.Fatalf("mapping query calls = %#v, want account_owner", q.mappingCalls)
			}
		})
	}
}

func TestGetManagementAccountTestOptionsSourceMapsAuthorizedInstanceForGlobalAndNarrowScopes(t *testing.T) {
	want := port.ManagementAccountTestOptionsSource{
		ID:                        "account_instance",
		OwnerSystemAccountID:      "sys_resource_owner",
		ProviderCode:              "anthropic",
		ProviderProtocolProfileID: "profile_anthropic_messages_v1",
		ProtocolCode:              "anthropic",
		ProtocolVersion:           "v1",
		Type:                      "oauth",
		ClientCompatibility:       "claude_code",
		HealthCheckModel:          "instance-test-model",
		HealthCheckEndpointMode:   "messages_sse",
		CredentialsEncrypted:      "v1:source-ciphertext",
		ModelMappings: []port.ManagementAccountTestModelMapping{
			{
				SourceModel:            "claude-sonnet-4-5",
				SourceEndpointFamily:   "messages",
				UpstreamModel:          "claude-sonnet-4-5-20250929",
				UpstreamEndpointFamily: "messages",
				Enabled:                true,
			},
		},
	}
	for _, tt := range []struct {
		name            string
		systemAccountID string
	}{
		{name: "admin global", systemAccountID: ""},
		{name: "viewer narrow", systemAccountID: "sys_grantee"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q := &managementAccountTestOptionsQueriesStub{
				row:         managementAccountTestOptionsRow(want, "account_source"),
				mappingRows: managementAccountTestOptionsMappingRows(want.ModelMappings),
			}
			got, found, err := getManagementAccountTestOptionsSource(
				context.Background(),
				q,
				port.ManagementAccountTestOptionsInput{
					AccountID:       "account_instance",
					SystemAccountID: tt.systemAccountID,
				},
			)
			if err != nil {
				t.Fatalf("get authorized source: %v", err)
			}
			if !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("authorized source = %#v, found = %v, want %#v", got, found, want)
			}
			if len(q.calls) != 1 || q.calls[0].AccountID != "account_instance" || q.calls[0].SystemAccountID != tt.systemAccountID {
				t.Fatalf("authorized query calls = %#v", q.calls)
			}
			if !reflect.DeepEqual(q.mappingCalls, []string{"account_source"}) {
				t.Fatalf("authorized mapping query calls = %#v, want source account", q.mappingCalls)
			}
		})
	}
}

func TestGetManagementAccountTestOptionsSourceNarrowScopeRejectsOtherViewerAccounts(t *testing.T) {
	for _, accountID := range []string{"account_owner", "account_instance"} {
		t.Run(accountID, func(t *testing.T) {
			q := &managementAccountTestOptionsQueriesStub{err: pgx.ErrNoRows}
			got, found, err := getManagementAccountTestOptionsSource(
				context.Background(),
				q,
				port.ManagementAccountTestOptionsInput{
					AccountID:       accountID,
					SystemAccountID: "sys_other_viewer",
				},
			)
			if err != nil || found || !reflect.DeepEqual(got, port.ManagementAccountTestOptionsSource{}) {
				t.Fatalf("source = %#v, found = %v, err = %v", got, found, err)
			}
			if len(q.calls) != 1 || q.calls[0].AccountID != accountID || q.calls[0].SystemAccountID != "sys_other_viewer" {
				t.Fatalf("narrow query calls = %#v", q.calls)
			}
			if len(q.mappingCalls) != 0 {
				t.Fatalf("mapping query calls = %#v, want none", q.mappingCalls)
			}
		})
	}
}

func TestGetManagementAccountTestOptionsSourceHandlesNotFoundAndWrapsQueryErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		q := &managementAccountTestOptionsQueriesStub{err: pgx.ErrNoRows}
		got, found, err := getManagementAccountTestOptionsSource(
			context.Background(),
			q,
			port.ManagementAccountTestOptionsInput{AccountID: "missing"},
		)
		if err != nil || found || !reflect.DeepEqual(got, port.ManagementAccountTestOptionsSource{}) {
			t.Fatalf("source = %#v, found = %v, err = %v", got, found, err)
		}
		if len(q.mappingCalls) != 0 {
			t.Fatalf("mapping query calls = %#v, want none", q.mappingCalls)
		}
	})

	t.Run("query error", func(t *testing.T) {
		queryErr := errors.New("query failed")
		q := &managementAccountTestOptionsQueriesStub{err: queryErr}
		got, found, err := getManagementAccountTestOptionsSource(
			context.Background(),
			q,
			port.ManagementAccountTestOptionsInput{AccountID: "account_1"},
		)
		if found || !reflect.DeepEqual(got, port.ManagementAccountTestOptionsSource{}) {
			t.Fatalf("source = %#v, found = %v", got, found)
		}
		if !errors.Is(err, queryErr) || !strings.Contains(err.Error(), "get management account test options source") {
			t.Fatalf("wrapped error = %v", err)
		}
		if len(q.mappingCalls) != 0 {
			t.Fatalf("mapping query calls = %#v, want none", q.mappingCalls)
		}
	})

	t.Run("model mapping query error", func(t *testing.T) {
		queryErr := errors.New("mapping query failed")
		source := port.ManagementAccountTestOptionsSource{ID: "account_instance"}
		q := &managementAccountTestOptionsQueriesStub{
			row:        managementAccountTestOptionsRow(source, "account_source"),
			mappingErr: queryErr,
		}
		got, found, err := getManagementAccountTestOptionsSource(
			context.Background(),
			q,
			port.ManagementAccountTestOptionsInput{AccountID: "account_instance"},
		)
		if found || !reflect.DeepEqual(got, port.ManagementAccountTestOptionsSource{}) {
			t.Fatalf("source = %#v, found = %v", got, found)
		}
		if !errors.Is(err, queryErr) || !strings.Contains(err.Error(), "list management account test option model mappings") {
			t.Fatalf("wrapped mapping error = %v", err)
		}
		if !reflect.DeepEqual(q.mappingCalls, []string{"account_source"}) {
			t.Fatalf("mapping query calls = %#v, want account_source", q.mappingCalls)
		}
	})
}

func TestManagementAccountTestOptionsSQLUsesSourceAndInstanceFieldsWithExactScope(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_account_test_options.sql")
	if err != nil {
		t.Fatalf("read management account options SQL: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	marker := "-- name: GetManagementAccountTestOptionsSource :one"
	markerIndex := strings.Index(sql, marker)
	if markerIndex < 0 {
		t.Fatalf("management account test options SQL is missing %q", marker)
	}
	querySQL := sql[markerIndex:]

	for _, required := range []string{
		"accounts.system_account_id AS owner_system_account_id",
		"accounts.authorization_instance_source_account_id IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"accounts.authorization_instance_owner_system_account_id IS NULL",
		"sqlc.arg(system_account_id)::text = ''",
		"OR accounts.system_account_id = sqlc.arg(system_account_id)::text",
		"UNION ALL",
		"COALESCE(\n      accounts.authorization_instance_owner_system_account_id,\n      resource_authorizations.resource_owner_system_account_id\n    ) AS owner_system_account_id",
		"INNER JOIN juhe_business.accounts AS source_accounts",
		"source_accounts.id = accounts.authorization_instance_source_account_id",
		"source_accounts.deleted_at IS NULL",
		"source_accounts.authorization_instance_source_account_id IS NULL",
		"source_accounts.authorization_instance_authorization_id IS NULL",
		"source_accounts.authorization_instance_owner_system_account_id IS NULL",
		"INNER JOIN juhe_business.resource_authorizations AS resource_authorizations",
		"resource_authorizations.id = accounts.authorization_instance_authorization_id",
		"resource_authorizations.resource_type = 'account'",
		"resource_authorizations.resource_id = accounts.authorization_instance_source_account_id",
		"resource_authorizations.resource_owner_system_account_id = source_accounts.system_account_id",
		"OR accounts.authorization_instance_owner_system_account_id = resource_authorizations.resource_owner_system_account_id",
		"resource_authorizations.grantee_system_account_id = accounts.system_account_id",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"accounts.id = sqlc.arg(account_id)::text",
		"accounts.system_account_id = sqlc.arg(system_account_id)::text",
		"accounts.deleted_at IS NULL",
		"test_options_sources.model_mapping_account_id",
	} {
		if !strings.Contains(querySQL, required) {
			t.Fatalf("management account test options SQL missing %q", required)
		}
	}

	parts := strings.SplitN(querySQL, "  UNION ALL\n", 2)
	if len(parts) != 2 {
		t.Fatalf("management account test options SQL must have owner and authorized branches")
	}
	ownerSQL := parts[0]
	if !strings.Contains(ownerSQL, "\n    accounts.id AS model_mapping_account_id,") {
		t.Fatalf("owner test options SQL must use the owner account model mappings")
	}
	authorizedSQL := strings.SplitN(parts[1], ")\nSELECT\n", 2)[0]
	for _, required := range []string{
		"    accounts.id,",
		"    source_accounts.id AS model_mapping_account_id,",
		"    source_accounts.provider_code,",
		"    source_accounts.provider_protocol_profile_id,",
		"    source_accounts.protocol_code,",
		"    source_accounts.protocol_version,",
		"    source_accounts.type,",
		"    source_accounts.client_compatibility,",
		"    accounts.health_check_model,",
		"    accounts.health_check_endpoint_mode,",
		"    source_accounts.credentials_encrypted",
		"sqlc.arg(system_account_id)::text = ''",
		"OR accounts.system_account_id = sqlc.arg(system_account_id)::text",
	} {
		if !strings.Contains(authorizedSQL, required) {
			t.Fatalf("authorized test options SQL missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"\n    accounts.provider_code,",
		"\n    accounts.provider_protocol_profile_id,",
		"\n    accounts.protocol_code,",
		"\n    accounts.protocol_version,",
		"\n    accounts.type,",
		"\n    accounts.client_compatibility,",
		"\n    source_accounts.health_check_model,",
		"\n    source_accounts.health_check_endpoint_mode,",
		"\n    accounts.credentials_encrypted",
		"\n    accounts.id AS model_mapping_account_id,",
		"'revoked'",
		"LEFT JOIN juhe_business.accounts AS source_accounts",
		"sqlc.arg(system_account_id)::text <> ''",
	} {
		if strings.Contains(authorizedSQL, forbidden) {
			t.Fatalf("authorized test options SQL must not contain %q", forbidden)
		}
	}
	if count := strings.Count(querySQL, "sqlc.arg(system_account_id)::text = ''"); count != 2 {
		t.Fatalf("owner and authorized branches must both support global scope, count = %d", count)
	}
	if count := strings.Count(querySQL, "source_accounts.id AS model_mapping_account_id"); count != 1 {
		t.Fatalf("authorized branch must select exactly one source mapping account id, count = %d", count)
	}

	mappingMarker := "-- name: ListManagementAccountTestOptionModelMappings :many"
	mappingMarkerIndex := strings.Index(sql, mappingMarker)
	if mappingMarkerIndex < 0 {
		t.Fatalf("management account test options SQL is missing %q", mappingMarker)
	}
	mappingSQL := sql[mappingMarkerIndex:]
	for _, required := range []string{
		"account_model_mappings.source_model",
		"account_model_mappings.source_endpoint_family",
		"account_model_mappings.upstream_model",
		"account_model_mappings.upstream_endpoint_family",
		"account_model_mappings.enabled",
		"FROM juhe_business.account_model_mappings AS account_model_mappings",
		"WHERE account_model_mappings.account_id = sqlc.arg(account_id)::text",
		"ORDER BY\n  account_model_mappings.source_model ASC,\n  account_model_mappings.source_endpoint_family ASC",
	} {
		if !strings.Contains(mappingSQL, required) {
			t.Fatalf("management account model mappings SQL missing %q", required)
		}
	}
}

func managementAccountTestOptionsRow(
	source port.ManagementAccountTestOptionsSource,
	modelMappingAccountID string,
) postgresqueries.GetManagementAccountTestOptionsSourceRow {
	return postgresqueries.GetManagementAccountTestOptionsSourceRow{
		ID:                        source.ID,
		OwnerSystemAccountID:      source.OwnerSystemAccountID,
		ModelMappingAccountID:     modelMappingAccountID,
		ProviderCode:              source.ProviderCode,
		ProviderProtocolProfileID: source.ProviderProtocolProfileID,
		ProtocolCode:              source.ProtocolCode,
		ProtocolVersion:           source.ProtocolVersion,
		Type:                      source.Type,
		ClientCompatibility:       source.ClientCompatibility,
		HealthCheckModel:          source.HealthCheckModel,
		HealthCheckEndpointMode:   source.HealthCheckEndpointMode,
		CredentialsEncrypted:      source.CredentialsEncrypted,
	}
}

func managementAccountTestOptionsMappingRows(
	mappings []port.ManagementAccountTestModelMapping,
) []postgresqueries.ListManagementAccountTestOptionModelMappingsRow {
	rows := make([]postgresqueries.ListManagementAccountTestOptionModelMappingsRow, 0, len(mappings))
	for _, mapping := range mappings {
		rows = append(rows, postgresqueries.ListManagementAccountTestOptionModelMappingsRow{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                mapping.Enabled,
		})
	}
	return rows
}

type managementAccountTestOptionsQueriesStub struct {
	row          postgresqueries.GetManagementAccountTestOptionsSourceRow
	err          error
	mappingRows  []postgresqueries.ListManagementAccountTestOptionModelMappingsRow
	mappingErr   error
	calls        []postgresqueries.GetManagementAccountTestOptionsSourceParams
	mappingCalls []string
}

func (s *managementAccountTestOptionsQueriesStub) GetManagementAccountTestOptionsSource(
	_ context.Context,
	arg postgresqueries.GetManagementAccountTestOptionsSourceParams,
) (postgresqueries.GetManagementAccountTestOptionsSourceRow, error) {
	s.calls = append(s.calls, arg)
	return s.row, s.err
}

func (s *managementAccountTestOptionsQueriesStub) ListManagementAccountTestOptionModelMappings(
	_ context.Context,
	accountID string,
) ([]postgresqueries.ListManagementAccountTestOptionModelMappingsRow, error) {
	s.mappingCalls = append(s.mappingCalls, accountID)
	return s.mappingRows, s.mappingErr
}

var _ managementAccountTestOptionsQueries = (*managementAccountTestOptionsQueriesStub)(nil)
