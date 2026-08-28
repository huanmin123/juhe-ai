package ownermanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRepositoryCapabilityManifest(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	report, err := VerifyCapabilityManifest(
		filepath.Join(root, "docs", "migration", "GoBusinessCapabilityManifest.json"),
		filepath.Join(root, "docs", "migration", "BusinessSQLite-owner-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Capabilities != 15 || report.Groups != 15 || report.Operations != 92 {
		t.Fatalf("unexpected capability report=%+v", report)
	}
	if report.StatusCoverage["partial"] != 4 || report.StatusCoverage["missing"] != 9 || report.StatusCoverage["excluded"] != 2 {
		t.Fatalf("capability status coverage=%+v", report.StatusCoverage)
	}
}

func TestVerifyCapabilityManifestRejectsImplementedWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	operationPath := filepath.Join(dir, "operations.json")
	capabilityPath := filepath.Join(dir, "capabilities.json")
	writeJSON := func(path string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(operationPath, map[string]any{
		"manifest_version": 1,
		"operations": []map[string]any{{
			"operation": "one", "transaction_group": "g", "current_owner": "node-db-service", "target_owner": "go-gateway",
		}},
	})
	writeJSON(capabilityPath, map[string]any{
		"manifest_version": 1,
		"capabilities": []map[string]any{{
			"id": "cap", "node_writer_operation_group": "g", "node_operations": []string{"one"}, "operation_count": 1,
			"current_owner": "node-db-service", "target_owner": "go-gateway", "gateway_target_module": "gateway/business",
			"status": "implemented", "migration_method": "rewrite", "acceptance_gates": []string{"gate"}, "rollback": "drain",
		}},
	})
	if _, err := VerifyCapabilityManifest(capabilityPath, operationPath); err == nil {
		t.Fatal("implemented capability without evidence must fail closed")
	}
}

func TestVerifyCapabilityManifestRejectsOmittedGroup(t *testing.T) {
	dir := t.TempDir()
	operationPath := filepath.Join(dir, "operations.json")
	capabilityPath := filepath.Join(dir, "capabilities.json")
	write := func(path string, data string) {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(operationPath, `{"manifest_version":1,"operations":[{"operation":"one","transaction_group":"g","current_owner":"node","target_owner":"go"},{"operation":"two","transaction_group":"h","current_owner":"node","target_owner":"go"}]}`)
	write(capabilityPath, `{"manifest_version":1,"capabilities":[{"id":"cap","node_writer_operation_group":"g","node_operations":["one"],"operation_count":1,"current_owner":"node","target_owner":"go","gateway_target_module":"gateway","status":"missing","migration_method":"rewrite","acceptance_gates":["gate"],"rollback":"drain"}]}`)
	if _, err := VerifyCapabilityManifest(capabilityPath, operationPath); err == nil {
		t.Fatal("omitted transaction group must fail closed")
	}
}
