package j3bmodelcheck

import (
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestJ3bBootstrapDDLIsScopedAndComplete(t *testing.T) {
	for _, table := range contracts.J3BModelCheckTables {
		if !strings.Contains(postgresSchema, "juhe_jobs."+table) {
			t.Fatalf("DDL missing table %q", table)
		}
	}
	for index := range contracts.J3BModelCheckIndexes {
		if !strings.Contains(postgresSchema, index) {
			t.Fatalf("DDL missing index %q", index)
		}
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(postgresSchema), " "))
	for _, forbidden := range []string{"juhe_business", "goose_db_version", "drop ", "alter table"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("DDL must not touch %q", forbidden)
		}
	}
	if got := len(requiredIndexNames()); got != 2 {
		t.Fatalf("index count=%d", got)
	}
}

func TestJ3bReportReadinessRejectsMissingOrMalformedObjects(t *testing.T) {
	if (Report{Schema: SchemaName, MissingSchema: true}).Ready() {
		t.Fatal("missing schema must fail readiness")
	}
	if (Report{Schema: SchemaName, InvalidTables: []string{"model_check_inputs.input_digest"}}).Ready() {
		t.Fatal("invalid table must fail readiness")
	}
	if !(Report{Schema: SchemaName, CurrentRole: "jobs", SchemaOwner: "jobs"}).Ready() {
		t.Fatal("empty valid report should be ready")
	}
}
