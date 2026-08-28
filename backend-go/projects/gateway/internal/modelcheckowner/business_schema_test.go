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

func newBusinessSQLiteSchemaFixture(t *testing.T, omitTable, omitRefTable string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
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
		if _, err := db.Exec(`CREATE TABLE ` + quoteSQLiteIdentifier(table) + ` (` + strings.Join(defs, ",") + `)`); err != nil {
			t.Fatalf("create table %s: %v", table, err)
		}
		for _, index := range spec.Indexes {
			if _, err := db.Exec(`CREATE INDEX ` + quoteSQLiteIdentifier(index) + ` ON ` + quoteSQLiteIdentifier(table) + ` (` + quoteSQLiteIdentifier(spec.Columns[0]) + `)`); err != nil {
				t.Fatalf("create index %s: %v", index, err)
			}
		}
	}
	return db
}

func quoteSQLiteColumns(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteSQLiteIdentifier(value))
	}
	return strings.Join(quoted, ",")
}
