package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementResponseInspectionPolicyStoreImplementsSharedSchemaContract(t *testing.T) {
	var _ port.ResponseInspectionPolicyStore = (*Store)(nil)

	source, err := os.ReadFile("managementresponseinspectionpolicies.go")
	if err != nil {
		t.Fatalf("read store source: %v", err)
	}
	text := string(source)
	required := []string{
		"juhe_business.response_inspection_policies",
		"ORDER BY priority ASC, updated_at DESC, id ASC",
		"LIMIT $1",
		"pg_advisory_xact_lock(hashtext('response_inspection_policies.capacity'))",
		"FOR UPDATE",
		"juhe_business.provider_protocol_profiles",
		"juhe_business.providers",
		"providers.enabled::text IN ('true', '1')",
		"profiles.enabled::text IN ('true', '1')",
		"profiles.protocol_code = $2",
		"ResponseInspectionPolicyInTx",
		"tx.Commit(ctx)",
	}
	for _, fragment := range required {
		if !strings.Contains(text, fragment) {
			t.Errorf("store source missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"COUNT(*)", "CREATE TABLE", "sqlite", "database/sql"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("store source contains forbidden %q", forbidden)
		}
	}
}

func TestManagementResponseInspectionPolicyDoesNotAddGoMigration(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "..", "db", "migrations", "*response*inspection*"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("unexpected response inspection policy Go migrations: %#v", matches)
	}
}
