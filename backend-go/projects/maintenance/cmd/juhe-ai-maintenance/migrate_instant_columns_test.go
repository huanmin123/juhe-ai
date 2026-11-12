package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/timeinventory"
)

func TestMigrateInstantColumnsRequiresInput(t *testing.T) {
	if code := runMaintenance([]string{"migrate-instant-columns"}); code != 2 {
		t.Fatalf("缺 --dsn/--manifest 应 exit 2，实际 %d", code)
	}
	if code := runMaintenance([]string{"migrate-instant-columns", "--dsn", "sqlite:///tmp/x.db"}); code != 2 {
		t.Fatalf("非 PostgreSQL DSN 应 exit 2，实际 %d", code)
	}
}

func TestMigrateInstantColumnsReadsManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "inventory.json")
	report := timeinventory.Report{Columns: []timeinventory.Column{{
		Schema: "juhe_business", Table: "accounts", Column: "created_at", DataType: "text",
		Target: timeinventory.Target{Type: "timestamptz(3)", Kind: "instant"},
	}}}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "plan.json")
	if code := runMigrateInstantColumnsCommand([]string{"--manifest", manifest, "--out", out}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var plan instantmigration.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Column != "created_at" {
		t.Fatalf("plan = %+v", plan)
	}
}
