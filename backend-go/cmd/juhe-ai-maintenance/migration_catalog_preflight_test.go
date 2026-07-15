package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type commandPreflightResult struct {
	Success bool `json:"success"`
}

func TestMigrationCatalogPreflightCommandReportsArgumentErrors(t *testing.T) {
	var combined bytes.Buffer
	cmd := newMigrationCatalogPreflightCommand()
	cmd.SetOut(&combined)
	cmd.SetErr(&combined)
	cmd.SetArgs([]string{"--unknown-flag"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("ExecuteContext() error = nil, want unknown flag error")
	}
	if !strings.Contains(combined.String(), "unknown flag") {
		t.Fatalf("combined output = %q, want unknown flag error", combined.String())
	}
}

func TestMigrationCatalogPreflightCommandRunsWithoutDependencyConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001_baseline.sql"), []byte("-- +goose Up\nSELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newMigrationCatalogPreflightCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--dir", dir})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v, output = %s", err, out.String())
	}
}

func TestMigrationCatalogPreflightCommandFailureOutputRemainsJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.go"), []byte("package notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	var combined bytes.Buffer
	cmd := newMigrationCatalogPreflightCommand()
	cmd.SetOut(&combined)
	cmd.SetErr(&combined)
	cmd.SetArgs([]string{"--dir", dir})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("ExecuteContext() error = nil, want invalid catalog error")
	}

	var result commandPreflightResult
	if err := json.Unmarshal(combined.Bytes(), &result); err != nil {
		t.Fatalf("combined output is not JSON: %v; output = %q", err, combined.String())
	}
	if result.Success {
		t.Fatalf("result = %+v", result)
	}
}
