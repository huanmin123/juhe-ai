package ownermanifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRepositoryBusinessOwnerManifest(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	report, err := Verify(
		filepath.Join(root, "docs", "migration", "BusinessSQLite-owner-manifest.json"),
		filepath.Join(root, "backend", "src", "modules", "db-service", "db-service-types.ts"),
		filepath.Join(root, "backend", "src", "modules", "db-service", "db-service-operation-access-mode.ts"),
		filepath.Join(root, "backend", "src", "modules", "db-service", "db-service-handlers.ts"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Operations == 0 || report.Operations != report.HandlerMatches || report.Writes == 0 || report.Reads == 0 {
		t.Fatalf("unexpected owner manifest report=%+v", report)
	}
}

func TestVerifyRejectsMissingOperation(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"manifest_version":1,"operations":[{"operation":"one","access":"read","tables":"t","transaction_group":"g","current_owner":"node","target_owner":"read-consumer","rollback":"r","verification":"v"}]}`
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	_, err := Verify(
		write("manifest.json", manifest),
		write("types.ts", "export type DbServiceOperation =\n | { type: 'one' }\n | { type: 'two' }\n"),
		write("access.ts", "one: 'read',\ntwo: 'read',\n"),
		write("handlers.ts", "case 'one':\ncase 'two':\n"),
	)
	if err == nil {
		t.Fatal("missing operation must fail closed")
	}
}
