package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementTableMonitorSQLIsPostgresOnlyBoundedAndStable(t *testing.T) {
	checks := map[string]string{
		"overview databases": tableMonitorOverviewDatabasesSQL,
		"overview tables":    tableMonitorOverviewTablesSQL,
		"table history":      tableMonitorTableHistorySQL,
		"database history":   tableMonitorDatabaseHistorySQL,
	}
	for name, query := range checks {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "juhe_stats.") {
				t.Fatalf("query must read juhe_stats only:\n%s", query)
			}
			if !strings.Contains(query, "LIMIT") {
				t.Fatalf("query must have a hard row bound:\n%s", query)
			}
			if !strings.Contains(query, "ORDER BY") {
				t.Fatalf("query must have stable ordering:\n%s", query)
			}
			if strings.Contains(strings.ToUpper(query), "COUNT(") || strings.Contains(strings.ToUpper(query), "SELECT *") {
				t.Fatalf("query must not aggregate or select unbounded columns:\n%s", query)
			}
		})
	}
	if strings.Contains(tableMonitorOverviewDatabasesSQL+tableMonitorOverviewTablesSQL+tableMonitorTableHistorySQL+tableMonitorDatabaseHistorySQL, "sqlite") {
		t.Fatal("Go table monitor reader must not contain SQLite fallback")
	}
	for name, query := range map[string]string{
		"overview tables": tableMonitorOverviewTablesSQL,
		"table history":   tableMonitorTableHistorySQL,
	} {
		if !strings.Contains(query, "is_partition <> 0") || !strings.Contains(query, "is_archive <> 0") {
			t.Fatalf("%s must convert Node PostgreSQL integer flags to booleans before scanning:\n%s", name, query)
		}
	}
	if strings.Count(tableMonitorOverviewTablesSQL, "LIMIT $1::int") < 2 {
		t.Fatalf("overview tables must cap each role before global sorting:\n%s", tableMonitorOverviewTablesSQL)
	}
	if !strings.Contains(tableMonitorDatabaseHistorySQL, "history.id DESC") {
		t.Fatalf("database history must preserve Node's id-desc tie order:\n%s", tableMonitorDatabaseHistorySQL)
	}
}

func TestManagementTableMonitorSchemaCapabilityIsReadOnlyAndComplete(t *testing.T) {
	for _, required := range []string{
		"information_schema.tables", "BASE TABLE", "has_table_privilege", "to_regclass",
		"information_schema.columns", "data_type = required.data_type",
		"database_storage_snapshots", "database_path", "page_size", "freelist_count", "used_bytes",
		"table_storage_snapshots", "table_kind", "parent_table_name", "is_partition", "is_archive",
		"growth_bytes_1h", "growth_rows_1h", "growth_bytes_24h", "growth_rows_24h",
		"'text'", "'integer'", "'bigint'",
		"('database_storage_snapshots', 'page_size', 'integer')",
		"('database_storage_snapshots', 'page_count', 'bigint')",
		"('database_storage_snapshots', 'freelist_count', 'bigint')",
		"('database_storage_snapshots', 'table_count', 'bigint')",
		"('database_storage_snapshots', 'index_count', 'bigint')",
		"('table_storage_snapshots', 'is_partition', 'integer')",
		"('table_storage_snapshots', 'is_archive', 'integer')",
		"('table_storage_snapshots', 'row_count', 'bigint')",
		"('table_storage_snapshots', 'page_count', 'bigint')",
		"('table_storage_snapshots', 'index_count', 'bigint')",
		"('table_storage_snapshots', 'growth_bytes_1h', 'bigint')",
		"('table_storage_snapshots', 'growth_rows_1h', 'bigint')",
		"('table_storage_snapshots', 'growth_bytes_24h', 'bigint')",
		"('table_storage_snapshots', 'growth_rows_24h', 'bigint')",
	} {
		if !strings.Contains(tableMonitorSchemaCapabilitySQL, required) {
			t.Fatalf("schema capability query missing %q:\n%s", required, tableMonitorSchemaCapabilitySQL)
		}
	}
	upper := strings.ToUpper(tableMonitorSchemaCapabilitySQL)
	for _, mutation := range []string{"CREATE ", "ALTER ", "INSERT ", "UPDATE ", "DELETE ", "DROP ", "TRUNCATE "} {
		if strings.Contains(upper, mutation) {
			t.Fatalf("schema capability query must remain read-only; found %q:\n%s", mutation, tableMonitorSchemaCapabilitySQL)
		}
	}
}

func TestManagementTableMonitorMapsSchemaRaceErrorsToUnavailable(t *testing.T) {
	for _, code := range []string{"42P01", "42703", "42501"} {
		t.Run(code, func(t *testing.T) {
			executor := &managementTableMonitorExecutorStub{databaseErr: &pgconn.PgError{Code: code, Message: "schema changed"}}
			_, err := getManagementTableStorageOverview(context.Background(), executor, 50)
			if !errors.Is(err, ErrManagementTableMonitorSchemaUnavailable) {
				t.Fatalf("overview error = %v, want schema unavailable", err)
			}
		})
	}
}

func TestManagementTableMonitorRejectsMissingNodeOwnedSnapshotSchemaBeforeReading(t *testing.T) {
	executor := &managementTableMonitorExecutorStub{schemaUnavailable: true}

	if _, err := getManagementTableStorageOverview(context.Background(), executor, 50); !errors.Is(err, ErrManagementTableMonitorSchemaUnavailable) {
		t.Fatalf("overview error = %v, want schema unavailable", err)
	}
	if executor.databaseQueryCalls != 0 || executor.tableQueryCalls != 0 {
		t.Fatalf("missing schema must stop before data reads; database=%d table=%d", executor.databaseQueryCalls, executor.tableQueryCalls)
	}

	input := port.ManagementTableStorageHistoryInput{DatabaseRole: port.MonitoredDatabaseRoleBusiness, TableName: "accounts", Limit: 10}
	if _, err := listManagementTableStorageHistory(context.Background(), executor, input); !errors.Is(err, ErrManagementTableMonitorSchemaUnavailable) {
		t.Fatalf("history error = %v, want schema unavailable", err)
	}
	if executor.tableQueryCalls != 0 {
		t.Fatalf("missing schema must not return fabricated empty history; table reads=%d", executor.tableQueryCalls)
	}
}

func TestManagementTableMonitorSchemaCheckPreservesInfrastructureFailure(t *testing.T) {
	want := errors.New("catalog unavailable")
	executor := &managementTableMonitorExecutorStub{schemaErr: want}

	_, err := getManagementTableStorageOverview(context.Background(), executor, 50)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "check table monitor snapshot schema") {
		t.Fatalf("overview error = %v, want wrapped schema-check error", err)
	}
	if errors.Is(err, ErrManagementTableMonitorSchemaUnavailable) {
		t.Fatalf("catalog failure must not be misreported as missing schema: %v", err)
	}
	if executor.databaseQueryCalls != 0 || executor.tableQueryCalls != 0 {
		t.Fatalf("schema-check failure must stop before data reads; database=%d table=%d", executor.databaseQueryCalls, executor.tableQueryCalls)
	}
}

func TestGetManagementTableStorageOverviewMapsNullableRowsAndRatiosInputs(t *testing.T) {
	executor := &managementTableMonitorExecutorStub{
		databaseRows: []managementDatabaseStorageSnapshotRow{{
			ID:           "db_1",
			DatabaseRole: "business",
			DatabasePath: `C:\\legacy\\business.db`,
			SampledAt:    "2026-07-22T10:00:00.000Z",
			FileBytes:    int64Pointer(4096),
		}},
		tableRows: []managementTableStorageSnapshotRow{{
			ID:           "table_1",
			DatabaseRole: "business",
			TableName:    "system_accounts",
			SampledAt:    "2026-07-22T10:00:00.000Z",
			IndexCount:   2,
		}},
	}

	result, err := getManagementTableStorageOverview(context.Background(), executor, 50)
	if err != nil {
		t.Fatalf("get overview: %v", err)
	}
	if executor.overviewLimit != 50 || len(result.Databases) != 1 || len(result.Tables) != 1 {
		t.Fatalf("executor limit=%d result=%+v", executor.overviewLimit, result)
	}
	if result.Databases[0].DatabasePath != "business.db" {
		t.Fatalf("database path = %q", result.Databases[0].DatabasePath)
	}
	if result.Tables[0].IndexCount != 2 || result.Tables[0].TableBytes != nil {
		t.Fatalf("table row = %+v", result.Tables[0])
	}
}

func TestManagementTableMonitorHistoryForwardsBoundedInputsAndWrapsErrors(t *testing.T) {
	executor := &managementTableMonitorExecutorStub{
		tableRows: []managementTableStorageSnapshotRow{{
			ID:           "table_1",
			DatabaseRole: "dataset",
			TableName:    "audit_logs",
			SampledAt:    "2026-07-22T10:00:00.000Z",
			IndexCount:   1,
		}},
	}
	input := port.ManagementTableStorageHistoryInput{
		DatabaseRole: port.MonitoredDatabaseRoleDataset,
		TableName:    "audit_logs",
		StartAt:      "2026-07-01T00:00:00.000Z",
		EndAt:        "2026-07-22T00:00:00.000Z",
		Limit:        720,
	}
	rows, err := listManagementTableStorageHistory(context.Background(), executor, input)
	if err != nil || len(rows) != 1 {
		t.Fatalf("table history rows=%+v err=%v", rows, err)
	}
	if executor.tableHistoryInput != input {
		t.Fatalf("table history input = %+v", executor.tableHistoryInput)
	}

	want := errors.New("query failed")
	executor.tableErr = want
	if _, err := listManagementTableStorageHistory(context.Background(), executor, input); !errors.Is(err, want) || !strings.Contains(err.Error(), "list table monitor table history") {
		t.Fatalf("wrapped error = %v", err)
	}
}

type managementTableMonitorExecutorStub struct {
	schemaUnavailable    bool
	schemaErr            error
	schemaCheckCalls     int
	overviewLimit        int
	databaseQueryCalls   int
	databaseRows         []managementDatabaseStorageSnapshotRow
	databaseErr          error
	tableQueryCalls      int
	tableRows            []managementTableStorageSnapshotRow
	tableErr             error
	tableHistoryInput    port.ManagementTableStorageHistoryInput
	databaseHistoryInput port.ManagementDatabaseStorageHistoryInput
}

func int64Pointer(value int64) *int64 { return &value }

func (s *managementTableMonitorExecutorStub) QueryManagementTableMonitorSchemaCapability(context.Context) (bool, error) {
	s.schemaCheckCalls++
	return !s.schemaUnavailable, s.schemaErr
}

func (s *managementTableMonitorExecutorStub) QueryLatestManagementDatabaseStorageSnapshots(context.Context) ([]managementDatabaseStorageSnapshotRow, error) {
	s.databaseQueryCalls++
	return s.databaseRows, s.databaseErr
}

func (s *managementTableMonitorExecutorStub) QueryLatestManagementTableStorageSnapshots(_ context.Context, limit int) ([]managementTableStorageSnapshotRow, error) {
	s.tableQueryCalls++
	s.overviewLimit = limit
	return s.tableRows, s.tableErr
}

func (s *managementTableMonitorExecutorStub) QueryManagementTableStorageHistory(_ context.Context, input port.ManagementTableStorageHistoryInput) ([]managementTableStorageSnapshotRow, error) {
	s.tableQueryCalls++
	s.tableHistoryInput = input
	return s.tableRows, s.tableErr
}

func (s *managementTableMonitorExecutorStub) QueryManagementDatabaseStorageHistory(_ context.Context, input port.ManagementDatabaseStorageHistoryInput) ([]managementDatabaseStorageSnapshotRow, error) {
	s.databaseQueryCalls++
	s.databaseHistoryInput = input
	return s.databaseRows, s.databaseErr
}
