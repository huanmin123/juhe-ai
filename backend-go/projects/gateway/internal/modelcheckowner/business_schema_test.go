package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

func TestCheckBusinessSQLiteSchemaAcceptsRequiredForeignKeys(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db.Close()

	if err := CheckBusinessSQLiteSchema(context.Background(), db); err != nil {
		t.Fatalf("complete foreign-key contract must be accepted: %v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForMissingForeignKey(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "announcement_reads", "announcements")
	defer db.Close()

	err := CheckBusinessSQLiteSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "announcement_reads(announcement_id)->announcements(id) onDelete=CASCADE onUpdate=NO ACTION") {
		t.Fatalf("missing foreign key must fail closed, err=%v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForForeignKeyDeleteActionDrift(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	if _, err := db.Exec(`DROP TABLE announcement_reads`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE announcement_reads (announcement_id TEXT, system_account_id TEXT, read_at TEXT, FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE NO ACTION, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_announcement_reads_account ON announcement_reads (announcement_id)`); err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err := CheckBusinessSQLiteSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "announcement_reads(announcement_id)->announcements(id) onDelete=CASCADE onUpdate=NO ACTION") {
		t.Fatalf("foreign key delete action drift must fail closed, err=%v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForMissingSessionOwnerForeignKey(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "system_sessions", "system_accounts")
	defer db.Close()

	err := CheckBusinessSQLiteSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "system_sessions(system_account_id)->system_accounts(id) onDelete=CASCADE onUpdate=NO ACTION") {
		t.Fatalf("missing session owner foreign key must fail closed, err=%v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForMissingSystemSettings(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE system_settings`); err != nil {
		t.Fatal(err)
	}
	if err := CheckBusinessSQLiteSchema(context.Background(), db); err == nil || !strings.Contains(err.Error(), "missing table system_settings") {
		t.Fatalf("missing system_settings must fail closed, err=%v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForMissingHealthCheckEndpointMode(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	columns := make([]string, 0, len(contracts.BusinessSQLiteSchema["accounts"].Columns))
	for _, column := range contracts.BusinessSQLiteSchema["accounts"].Columns {
		if column != "health_check_endpoint_mode" {
			columns = append(columns, quoteSQLiteIdentifier(column)+" TEXT")
		}
	}
	if _, err := db.Exec(`CREATE TABLE accounts (` + strings.Join(columns, ",") + `)`); err != nil {
		t.Fatal(err)
	}
	err := CheckBusinessSQLiteSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "missing column accounts.health_check_endpoint_mode") {
		t.Fatalf("missing health_check_endpoint_mode must fail closed, err=%v", err)
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForMalformedDefinedIndex(t *testing.T) {
	tests := []struct {
		name string
		ddl  string
	}{
		{"wrong columns", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(capability_hash,scope_kind) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"not unique", `CREATE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"missing predicate", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newBusinessSQLiteSchemaFixture(t, "", "")
			defer db.Close()
			if _, err := db.Exec(`DROP INDEX idx_account_circuit_incidents_key_model_capability`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			if err := CheckBusinessSQLiteSchema(context.Background(), db); err == nil || !strings.Contains(err.Error(), "incompatible index account_circuit_incidents.idx_account_circuit_incidents_key_model_capability") {
				t.Fatalf("malformed defined index must fail closed, err=%v", err)
			}
		})
	}
}

func TestCheckBusinessSQLiteSchemaFailsClosedForConstraintDrift(t *testing.T) {
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT, username TEXT, display_name TEXT, status TEXT, role TEXT, must_change_password TEXT, password_hash TEXT, last_login_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	err := CheckBusinessSQLiteSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "missing unique constraint system_accounts(username)") {
		t.Fatalf("missing username uniqueness must fail closed, err=%v", err)
	}
}

func TestPostgresConstraintMatchingRequiresExactKindAndOrder(t *testing.T) {
	constraints := map[int64]*postgresSchemaConstraint{
		1: {kind: "p", columns: []string{"system_account_id", "key"}},
		2: {kind: "u", columns: []string{"system_account_id", "account_id"}},
	}
	if !hasPostgresConstraint(constraints, "p", []string{"system_account_id", "key"}) {
		t.Fatal("expected composite primary key to match")
	}
	if hasPostgresConstraint(constraints, "p", []string{"key", "system_account_id"}) {
		t.Fatal("primary key column order drift must fail")
	}
	if hasPostgresConstraint(constraints, "u", []string{"account_id"}) {
		t.Fatal("unique constraint with incomplete columns must fail")
	}
}

func newBusinessSQLiteSchemaFixture(t *testing.T, omitTable, omitRefTable string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns)+len(spec.ForeignKeys))
		for _, column := range spec.Columns {
			defs = append(defs, quoteSQLiteIdentifier(column)+" TEXT")
		}
		for _, foreignKey := range spec.ForeignKeys {
			if table == omitTable && foreignKey.RefTable == omitRefTable {
				continue
			}
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteSQLiteColumns(foreignKey.Columns), quoteSQLiteIdentifier(foreignKey.RefTable), quoteSQLiteColumns(foreignKey.RefColumns))
			if foreignKey.OnDelete != "" {
				clause += " ON DELETE " + foreignKey.OnDelete
			}
			if foreignKey.OnUpdate != "" {
				clause += " ON UPDATE " + foreignKey.OnUpdate
			}
			defs = append(defs, clause)
		}
		if len(spec.PrimaryKey) > 0 {
			defs = append(defs, "PRIMARY KEY ("+quoteSQLiteColumns(spec.PrimaryKey)+")")
		}
		for _, unique := range spec.UniqueConstraints {
			defs = append(defs, "UNIQUE ("+quoteSQLiteColumns(unique)+")")
		}
		if _, err := db.Exec(`CREATE TABLE ` + quoteSQLiteIdentifier(table) + ` (` + strings.Join(defs, ",") + `)`); err != nil {
			t.Fatalf("create table %s: %v", table, err)
		}
		for _, index := range spec.Indexes {
			if hasSchemaIndexDefinition(spec.IndexDefinitions, index) {
				continue
			}
			if _, err := db.Exec(`CREATE INDEX ` + quoteSQLiteIdentifier(index) + ` ON ` + quoteSQLiteIdentifier(table) + ` (` + quoteSQLiteIdentifier(spec.Columns[0]) + `)`); err != nil {
				t.Fatalf("create index %s: %v", index, err)
			}
		}
		for _, index := range spec.IndexDefinitions {
			columns := make([]string, 0, len(index.Columns))
			for _, column := range index.Columns {
				columns = append(columns, quoteSQLiteIdentifier(column))
			}
			kind := "INDEX"
			if index.Unique {
				kind = "UNIQUE INDEX"
			}
			ddl := `CREATE ` + kind + ` ` + quoteSQLiteIdentifier(index.Name) + ` ON ` + quoteSQLiteIdentifier(table) + ` (` + strings.Join(columns, ",") + `)`
			if index.Predicate != "" {
				ddl += " WHERE " + index.Predicate
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatalf("create index %s: %v", index.Name, err)
			}
		}
	}
	return db
}

func hasSchemaIndexDefinition(definitions []contracts.SQLiteIndexDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func quoteSQLiteColumns(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteSQLiteIdentifier(value))
	}
	return strings.Join(quoted, ",")
}
