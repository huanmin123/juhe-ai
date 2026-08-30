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
		appendSQLiteConstraints(&defs, spec)
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (` + join(defs) + `)`); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		for _, idx := range spec.Indexes {
			if hasIndexDefinition(spec.IndexDefinitions, idx) {
				continue
			}
			if _, err := db.Exec(`CREATE INDEX "` + idx + `" ON "` + table + `" ("` + spec.Columns[0] + `")`); err != nil {
				t.Fatalf("index %s: %v", idx, err)
			}
		}
		for _, idx := range spec.IndexDefinitions {
			columns := make([]string, 0, len(idx.Columns))
			for _, column := range idx.Columns {
				columns = append(columns, `"`+column+`"`)
			}
			kind := "INDEX"
			if idx.Unique {
				kind = "UNIQUE INDEX"
			}
			ddl := `CREATE ` + kind + ` "` + idx.Name + `" ON "` + table + `" (` + join(columns) + `)`
			if idx.Predicate != "" {
				ddl += " WHERE " + idx.Predicate
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatalf("index %s: %v", idx.Name, err)
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

func hasIndexDefinition(definitions []contracts.SQLiteIndexDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func TestVerifySQLiteSchemaRejectsMalformedDefinedIndex(t *testing.T) {
	tests := []struct{ name, ddl string }{
		{"wrong columns", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(capability_hash,scope_kind) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"not unique", `CREATE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash) WHERE scope_kind='key_model' AND capability_hash IS NOT NULL`},
		{"missing predicate", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind,capability_hash)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "business.sqlite3")
			buildBusinessSQLiteFixture(t, path)
			db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`DROP INDEX idx_account_circuit_incidents_key_model_capability`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			report, err := VerifySQLiteSchema(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if report.Ready || len(report.MissingIndexes["account_circuit_incidents"]) == 0 || len(report.Errors) == 0 {
				t.Fatalf("malformed index accepted: %+v", report)
			}
		})
	}
}

func TestVerifySQLiteSchemaRejectsMissingSystemSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	buildBusinessSQLiteFixture(t, path)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE system_settings`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !contains(report.MissingTables, "system_settings") {
		t.Fatalf("missing system_settings must fail closed: %+v", report)
	}
}

func TestVerifySQLiteSchemaRejectsConstraintDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	buildBusinessSQLiteFixture(t, path)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE system_accounts`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE system_accounts (id TEXT, username TEXT, display_name TEXT, status TEXT, role TEXT, must_change_password TEXT, password_hash TEXT, last_login_at TEXT, updated_at TEXT)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !contains(report.MissingConstraints["system_accounts"], "UNIQUE (username)") {
		t.Fatalf("missing username uniqueness must fail closed: %+v", report)
	}
}

func buildBusinessSQLiteFixture(t *testing.T, path string) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, spec := range contracts.BusinessSQLiteSchema {
		defs := make([]string, 0, len(spec.Columns))
		for _, col := range spec.Columns {
			defs = append(defs, fmt.Sprintf(`"%s" TEXT`, col))
		}
		for _, fk := range spec.ForeignKeys {
			clause := fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteColumns(fk.Columns), fk.RefTable, quoteColumns(fk.RefColumns))
			if fk.OnDelete != "" {
				clause += " ON DELETE " + fk.OnDelete
			}
			if fk.OnUpdate != "" {
				clause += " ON UPDATE " + fk.OnUpdate
			}
			defs = append(defs, clause)
		}
		appendSQLiteConstraints(&defs, spec)
		if _, err := db.Exec(`CREATE TABLE "` + table + `" (` + join(defs) + `)`); err != nil {
			t.Fatal(err)
		}
		for _, idx := range spec.Indexes {
			if hasIndexDefinition(spec.IndexDefinitions, idx) {
				continue
			}
			if _, err := db.Exec(`CREATE INDEX "` + idx + `" ON "` + table + `" ("` + spec.Columns[0] + `")`); err != nil {
				t.Fatal(err)
			}
		}
		for _, idx := range spec.IndexDefinitions {
			cols := make([]string, len(idx.Columns))
			for i, c := range idx.Columns {
				cols[i] = `"` + c + `"`
			}
			kind := "INDEX"
			if idx.Unique {
				kind = "UNIQUE INDEX"
			}
			ddl := `CREATE ` + kind + ` "` + idx.Name + `" ON "` + table + `" (` + join(cols) + `)`
			if idx.Predicate != "" {
				ddl += " WHERE " + idx.Predicate
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
		}
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

func TestSystemSessionContractIncludesCascadeOwnerRelation(t *testing.T) {
	sessions, ok := contracts.BusinessSQLiteSchema["system_sessions"]
	if !ok {
		t.Fatal("system_sessions contract is missing")
	}
	if got, want := join(sessions.Columns), "id,system_account_id,token_hash,expires_at,created_at,last_seen_at"; got != want {
		t.Fatalf("system_sessions columns=%q, want %q", got, want)
	}
	if len(sessions.ForeignKeys) != 1 {
		t.Fatalf("system_sessions foreign key count=%d, want 1", len(sessions.ForeignKeys))
	}
	relation := sessions.ForeignKeys[0]
	if relation.RefTable != "system_accounts" || join(relation.Columns) != "system_account_id" || join(relation.RefColumns) != "id" || relation.OnDelete != "CASCADE" || relation.OnUpdate != "" {
		t.Fatalf("unexpected system_sessions foreign key=%+v", relation)
	}
}

func TestBusinessSQLiteContractIncludesGatewayTargetDependencies(t *testing.T) {
	if got, want := contracts.BusinessSQLiteSchemaVersion, "business-sqlite-gateway-v10"; got != want {
		t.Fatalf("schema version=%q, want %q", got, want)
	}
	for table, required := range map[string][]string{
		"accounts":                     {"dispatch_revision", "circuit_projection_revision", "type", "proxy_profile_id", "account_expires_at", "cooldown_until", "authorization_instance_source_account_id"},
		"system_accounts":              {"display_name", "role", "must_change_password"},
		"group_accounts":               {"account_authorization_id"},
		"proxy_profiles":               {"id", "enabled", "type", "host", "port", "username", "password_encrypted"},
		"resource_authorizations":      {"id", "resource_type", "resource_id", "resource_owner_system_account_id", "grantee_system_account_id", "scope", "status", "expires_at"},
		"provider_model_catalog":       {"catalog_visible"},
		"model_quality_policies":       {"created_at", "updated_at"},
		"model_quality_schedules":      {"created_at"},
		"account_quality_enforcements": {"fallback_was_enabled", "super_priority_was_enabled", "started_at", "created_at"},
		"account_circuit_incidents":    {"circuit_scope_key", "capability_hash", "confirmation_failure_evidence_keys_json", "updated_at_ms"},
		"account_circuit_outbox":       {"event_id", "claim_token", "acknowledged_at_ms", "updated_at_ms"},
	} {
		spec, ok := contracts.BusinessSQLiteSchema[table]
		if !ok {
			t.Fatalf("gateway target dependency table %s is missing", table)
		}
		columns := map[string]bool{}
		for _, column := range spec.Columns {
			columns[column] = true
		}
		for _, column := range required {
			if !columns[column] {
				t.Fatalf("gateway target dependency column %s.%s is missing from contract", table, column)
			}
		}
	}
}

func TestVerifySQLiteSchemaFailsClosedForGatewayRuntimeColumns(t *testing.T) {
	for table, columns := range map[string][]string{
		"accounts":                     {"circuit_projection_revision"},
		"system_accounts":              {"display_name", "role", "must_change_password"},
		"provider_model_catalog":       {"catalog_visible"},
		"model_quality_policies":       {"created_at", "updated_at"},
		"model_quality_schedules":      {"created_at"},
		"account_quality_enforcements": {"fallback_was_enabled", "super_priority_was_enabled", "started_at", "created_at"},
		"account_circuit_incidents":    {"capability_hash"},
		"account_circuit_outbox":       {"claim_token"},
	} {
		for _, omitted := range columns {
			t.Run(table+"_"+omitted, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "business.sqlite3")
				db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
				if err != nil {
					t.Fatal(err)
				}
				for fixtureTable, spec := range contracts.BusinessSQLiteSchema {
					defs := make([]string, 0, len(spec.Columns)+len(spec.ForeignKeys))
					for _, column := range spec.Columns {
						if fixtureTable == table && column == omitted {
							continue
						}
						defs = append(defs, fmt.Sprintf(`"%s" TEXT`, column))
					}
					for _, foreignKey := range spec.ForeignKeys {
						defs = append(defs, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", quoteColumns(foreignKey.Columns), foreignKey.RefTable, quoteColumns(foreignKey.RefColumns)))
					}
					appendSQLiteConstraints(&defs, spec)
					if _, err := db.Exec(`CREATE TABLE "` + fixtureTable + `" (` + join(defs) + `)`); err != nil {
						t.Fatalf("table %s: %v", fixtureTable, err)
					}
					for _, index := range spec.Indexes {
						if _, err := db.Exec(`CREATE INDEX "` + index + `" ON "` + fixtureTable + `" ("` + spec.Columns[0] + `")`); err != nil {
							t.Fatalf("index %s: %v", index, err)
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
				if report.Ready || !contains(report.MissingColumns[table], omitted) {
					t.Fatalf("missing runtime column %s.%s must fail closed: %+v", table, omitted, report)
				}
			})
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

func appendSQLiteConstraints(defs *[]string, spec contracts.SQLiteTableSpec) {
	if len(spec.PrimaryKey) > 0 {
		*defs = append(*defs, "PRIMARY KEY ("+quoteColumns(spec.PrimaryKey)+")")
	}
	for _, unique := range spec.UniqueConstraints {
		*defs = append(*defs, "UNIQUE ("+quoteColumns(unique)+")")
	}
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
		appendSQLiteConstraints(&defs, spec)
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
		appendSQLiteConstraints(&defs, spec)
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

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
