package ownermanifest

import (
	"os"
	"path/filepath"
	"strings"
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
	if report.TransactionGroups == 0 || report.TransactionGroups != len(report.TransactionCoverage) {
		t.Fatalf("transaction coverage missing from owner manifest report=%+v", report)
	}
	if report.AccessCoverage["read"] != report.Reads || report.AccessCoverage["write"] != report.Writes {
		t.Fatalf("access coverage does not reconcile with counts: %+v report=%+v", report.AccessCoverage, report)
	}
	if report.WriterCoverage["business-writer"] != report.Writes || report.WriterCoverage["read-consumer"] != report.Reads {
		t.Fatalf("writer coverage does not reconcile with counts: %+v report=%+v", report.WriterCoverage, report)
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

func TestVerifyRejectsIncompleteOperationSourceContract(t *testing.T) {
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
		write("types.ts", "export type DbServiceOperation =\n | { type: 'one' }\n"),
		write("access.ts", "one: 'read',\n"),
		write("handlers.ts", "case 'one':\n"),
	)
	if err == nil || !strings.Contains(err.Error(), "operation_source_contract") {
		t.Fatalf("incomplete operation source contract error=%v", err)
	}
}

func TestVerifyRejectsDisabledOperationSourceContractGate(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"manifest_version":1,"operation_source_contract":{"path":"types.ts","range":"1-2","handler_path":"handlers.ts","access_mode_path":"access.ts","cutover_epoch_required":true,"drain_required":true,"rollback_requires_stop_and_replay":false},"operations":[{"operation":"one","access":"read","tables":"t","transaction_group":"g","current_owner":"node","target_owner":"read-consumer","rollback":"r","verification":"v"}]}`
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	_, err := Verify(
		write("manifest.json", manifest),
		write("types.ts", "export type DbServiceOperation =\n | { type: 'one' }\n"),
		write("access.ts", "one: 'read',\n"),
		write("handlers.ts", "case 'one':\n"),
	)
	if err == nil || !strings.Contains(err.Error(), "rollback_requires_stop_and_replay=true") {
		t.Fatalf("disabled operation source contract gate error=%v", err)
	}
}

func TestVerifyRejectsStaleDeclaredSourceLine(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"manifest_version":1,"operations":[{"operation":"one","access":"read","source":{"type_union":"backend/src/modules/db-service/db-service-types.ts","handler":"backend/src/modules/db-service/db-service-handlers.ts","type_line":1,"access_mode_line":1,"handler_line":1,"entrypoint":"db-service-handler","writer_kind":"read-consumer"},"tables":"t","transaction_group":"g","current_owner":"node","target_owner":"read-consumer","rollback":"r","verification":"v"}]}`
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	_, err := Verify(
		write("manifest.json", manifest),
		write("types.ts", "export type DbServiceOperation =\n | { type: 'one' }\n"),
		write("access.ts", "one: 'read',\n"),
		write("handlers.ts", "case 'one':\n"),
	)
	if err == nil {
		t.Fatal("stale source location must fail closed")
	}
}

func TestVerifyRejectsTransactionOwnerDrift(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"manifest_version":1,"operations":[
{"operation":"one","access":"read","source":{"type_union":"backend/src/modules/db-service/db-service-types.ts","handler":"backend/src/modules/db-service/db-service-handlers.ts","type_line":2,"access_mode_line":1,"handler_line":1,"entrypoint":"db-service-handler","writer_kind":"read-consumer"},"tables":"t","transaction_group":"shared","current_owner":"node","target_owner":"read-consumer","rollback":"r","verification":"v"},
{"operation":"two","access":"read","source":{"type_union":"backend/src/modules/db-service/db-service-types.ts","handler":"backend/src/modules/db-service/db-service-handlers.ts","type_line":3,"access_mode_line":2,"handler_line":2,"entrypoint":"db-service-handler","writer_kind":"read-consumer"},"tables":"t","transaction_group":"shared","current_owner":"other-node","target_owner":"read-consumer","rollback":"r","verification":"v"}
]}`
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
		t.Fatal("transaction group owner drift must fail closed")
	}
}
