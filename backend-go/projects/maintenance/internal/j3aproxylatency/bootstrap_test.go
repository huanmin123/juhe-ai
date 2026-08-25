package j3aproxylatency

import (
	"strings"
	"testing"
)

func TestOpenRejectsIncompleteOrUnsupportedURLs(t *testing.T) {
	for _, rawURL := range []string{
		"",
		"https://example.test/database",
		"postgres://example.test/database",
		"postgres://user@example.test",
		"postgres:///database",
	} {
		db, err := Open(rawURL)
		if err == nil {
			if db != nil {
				_ = db.Close()
			}
			t.Fatalf("Open(%q) unexpectedly succeeded", rawURL)
		}
	}
}

func TestReportRequiresCurrentRoleToOwnJ3aSchema(t *testing.T) {
	report := Report{Schema: SchemaName, CurrentRole: "jobs", SchemaOwner: "other", OwnerMismatch: true}
	if report.Ready() {
		t.Fatal("owner mismatch must fail the J3a schema contract")
	}
}

func TestReportRejectsInvalidTableShape(t *testing.T) {
	report := Report{Schema: SchemaName, CurrentRole: "jobs", SchemaOwner: "jobs", InvalidTables: []string{"proxy_latency_owner_leases.fence_token:type=text/text"}}
	if report.Ready() {
		t.Fatal("invalid table shape must fail the J3a schema contract")
	}
}

func TestBootstrapDDLIsScopedToJ3aJobsObjects(t *testing.T) {
	for _, table := range requiredTables {
		if !strings.Contains(postgresSchema, "juhe_jobs."+table) {
			t.Fatalf("bootstrap DDL missing table %q", table)
		}
	}
	for index := range requiredIndexes {
		if !strings.Contains(postgresSchema, index) {
			t.Fatalf("bootstrap DDL missing index %q", index)
		}
	}
	normalizedDDL := strings.ToLower(strings.Join(strings.Fields(postgresSchema), " "))
	normalizedDDL = strings.ReplaceAll(normalizedDDL, "unique(", "unique (")
	if primaryKeys := strings.Count(normalizedDDL, "primary key"); primaryKeys != len(requiredTables) {
		t.Fatalf("bootstrap DDL primary key count = %d, want %d", primaryKeys, len(requiredTables))
	}
	uniqueConstraints := 0
	for _, constraints := range requiredConstraints {
		for _, constraint := range constraints {
			if strings.HasPrefix(constraint, "unique") {
				uniqueConstraints++
			}
		}
	}
	if actual := strings.Count(normalizedDDL, "unique"); actual != uniqueConstraints {
		t.Fatalf("bootstrap DDL unique constraint count = %d, want %d", actual, uniqueConstraints)
	}
	for _, forbidden := range []string{"juhe_business", "goose_db_version", "DROP ", "ALTER TABLE"} {
		if strings.Contains(strings.ToUpper(postgresSchema), strings.ToUpper(forbidden)) {
			t.Fatalf("bootstrap DDL must not touch %q", forbidden)
		}
	}
	if names := requiredIndexNames(); len(names) != 2 || names[0] != "idx_proxy_latency_outcomes_cursor" || names[1] != "idx_proxy_latency_outcomes_proxy" {
		t.Fatalf("required index names are not stable/sorted: %v", names)
	}
}
