package businesshandoff

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

func TestVerifySQLiteSchemaFixtureReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns))
		for _, col := range spec.Columns {
			defs = append(defs, fmt.Sprintf(`"%s" TEXT`, col))
		}
		for _, foreignKey := range spec.ForeignKeys {
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteColumns(foreignKey.Columns), foreignKey.RefTable, quoteColumns(foreignKey.RefColumns))
			if foreignKey.OnDelete != "" {
				clause += " ON DELETE " + foreignKey.OnDelete
			}
			if foreignKey.OnUpdate != "" {
				clause += " ON UPDATE " + foreignKey.OnUpdate
			}
			defs = append(defs, clause)
		}
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (` + join(defs) + `)`); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		for _, idx := range spec.Indexes {
			if _, err := db.Exec(`CREATE INDEX "` + idx + `" ON "` + table + `" ("` + spec.Columns[0] + `")`); err != nil {
				t.Fatalf("index %s: %v", idx, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.PresentTables != report.RequiredTables || len(report.MissingTables) != 0 {
		t.Fatalf("fixture should satisfy schema contract: %+v", report)
	}
}

func TestAnnouncementContractMatchesBusinessSQLiteShape(t *testing.T) {
	announcements, ok := contracts.BusinessSQLiteSchema["announcements"]
	if !ok {
		t.Fatal("announcements contract is missing")
	}
	if got, want := join(announcements.Columns), "id,title,content,level,status,created_by,updated_by,published_at,created_at,updated_at"; got != want {
		t.Fatalf("announcement columns=%q, want %q", got, want)
	}
	if got, want := join(announcements.Indexes), "idx_announcements_public,idx_announcements_admin,idx_announcements_admin_page"; got != want {
		t.Fatalf("announcement indexes=%q, want %q", got, want)
	}
	announcementReads, ok := contracts.BusinessSQLiteSchema["announcement_reads"]
	if !ok {
		t.Fatal("announcement_reads contract is missing")
	}
	if got, want := join(announcementReads.Columns), "announcement_id,system_account_id,read_at"; got != want {
		t.Fatalf("announcement_reads columns=%q, want %q", got, want)
	}
	if got, want := join(announcementReads.Indexes), "idx_announcement_reads_account"; got != want {
		t.Fatalf("announcement_reads indexes=%q, want %q", got, want)
	}
	if len(announcements.ForeignKeys) != 2 || len(announcementReads.ForeignKeys) != 2 {
		t.Fatalf("announcement foreign key count announcements=%d reads=%d", len(announcements.ForeignKeys), len(announcementReads.ForeignKeys))
	}
	for _, foreignKey := range announcements.ForeignKeys {
		if foreignKey.RefTable != "system_accounts" || join(foreignKey.RefColumns) != "id" || foreignKey.OnDelete != "" || foreignKey.OnUpdate != "" {
			t.Fatalf("unexpected announcements foreign key=%+v", foreignKey)
		}
	}
	for _, foreignKey := range announcementReads.ForeignKeys {
		if foreignKey.RefTable != "system_accounts" && foreignKey.RefTable != "announcements" {
			t.Fatalf("unexpected announcement_reads foreign key=%+v", foreignKey)
		}
		if foreignKey.OnDelete != "CASCADE" || foreignKey.OnUpdate != "" {
			t.Fatalf("announcement_reads foreign key must cascade delete=%+v", foreignKey)
		}
	}
}

func quoteColumns(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+value+`"`)
	}
	return join(quoted)
}

func TestVerifySQLiteSchemaFailsClosedMissingColumnAndIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.MissingTables) == 0 || len(report.MissingColumns["system_accounts"]) == 0 {
		t.Fatalf("missing schema must fail closed: %+v", report)
	}
}

func TestVerifySQLiteSchemaFailsClosedMissingAnnouncementForeignKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns)+len(spec.ForeignKeys))
		for _, col := range spec.Columns {
			defs = append(defs, fmt.Sprintf(`"%s" TEXT`, col))
		}
		for _, foreignKey := range spec.ForeignKeys {
			if table == "announcement_reads" && foreignKey.RefTable == "announcements" {
				continue
			}
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteColumns(foreignKey.Columns), foreignKey.RefTable, quoteColumns(foreignKey.RefColumns))
			if foreignKey.OnDelete != "" {
				clause += " ON DELETE " + foreignKey.OnDelete
			}
			defs = append(defs, clause)
		}
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (` + join(defs) + `)`); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		for _, idx := range spec.Indexes {
			if _, err := db.Exec(`CREATE INDEX "` + idx + `" ON "` + table + `" ("` + spec.Columns[0] + `")`); err != nil {
				t.Fatalf("index %s: %v", idx, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.MissingForeignKeys["announcement_reads"]) == 0 {
		t.Fatalf("missing announcement foreign key must fail closed: %+v", report)
	}
}

func TestVerifySQLiteSchemaFailsClosedAnnouncementForeignKeyActionDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns)+len(spec.ForeignKeys))
		for _, col := range spec.Columns {
			defs = append(defs, fmt.Sprintf(`"%s" TEXT`, col))
		}
		for _, foreignKey := range spec.ForeignKeys {
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteColumns(foreignKey.Columns), foreignKey.RefTable, quoteColumns(foreignKey.RefColumns))
			if foreignKey.OnDelete != "" {
				clause += " ON DELETE " + foreignKey.OnDelete
			}
			if table == "announcement_reads" && foreignKey.RefTable == "announcements" {
				clause = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE NO ACTION", quoteColumns(foreignKey.Columns), foreignKey.RefTable, quoteColumns(foreignKey.RefColumns))
			}
			defs = append(defs, clause)
		}
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (` + join(defs) + `)`); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		for _, idx := range spec.Indexes {
			if _, err := db.Exec(`CREATE INDEX "` + idx + `" ON "` + table + `" ("` + spec.Columns[0] + `")`); err != nil {
				t.Fatalf("index %s: %v", idx, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.MissingForeignKeys["announcement_reads"]) == 0 {
		t.Fatalf("foreign key action drift must fail closed: %+v", report)
	}
}

func join(values []string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += ","
		}
		result += value
	}
	return result
}
