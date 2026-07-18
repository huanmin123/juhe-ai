package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrationCatalogPreflightReportsValidCatalog(t *testing.T) {
	dir := t.TempDir()
	writeMigrationTestFile(t, dir, "000002_second.sql")
	writeMigrationTestFile(t, dir, "000001_first.sql")

	var out bytes.Buffer
	if err := RunMigrationCatalogPreflight(context.Background(), dir, &out); err != nil {
		t.Fatalf("RunMigrationCatalogPreflight() error = %v", err)
	}

	var result MigrationCatalogPreflightResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Success || result.Directory != filepath.Clean(dir) || result.MigrationCount != 2 || result.MinVersion != 1 || result.MaxVersion != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("issues = %v, want empty", result.Issues)
	}
}

func TestRunMigrationCatalogPreflightReportsSafeFailure(t *testing.T) {
	dir := t.TempDir()
	const secret = "do-not-expose-this-sql"
	if err := os.WriteFile(filepath.Join(dir, "notes.go"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := RunMigrationCatalogPreflight(context.Background(), dir, &out)
	if err == nil || err.Error() != "migration catalog preflight 未通过" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("output exposed file contents: %s", out.String())
	}

	var result MigrationCatalogPreflightResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode result: %v", decodeErr)
	}
	if result.Success || result.MigrationCount != 0 || len(result.Issues) != 1 || !strings.Contains(result.Issues[0], "notes.go") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunMigrationCatalogPreflightRejectsEmptyDirectory(t *testing.T) {
	var out bytes.Buffer
	err := RunMigrationCatalogPreflight(context.Background(), t.TempDir(), &out)
	if err == nil {
		t.Fatal("RunMigrationCatalogPreflight() error = nil, want empty catalog error")
	}

	var result MigrationCatalogPreflightResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode result: %v", decodeErr)
	}
	if result.Success || len(result.Issues) != 1 || result.Issues[0] != "migration catalog is empty" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunMigrationCatalogPreflightRejectsEmptyDirectoryArgument(t *testing.T) {
	var out bytes.Buffer
	err := RunMigrationCatalogPreflight(context.Background(), "", &out)
	if err == nil {
		t.Fatal("RunMigrationCatalogPreflight() error = nil, want missing directory error")
	}

	var result MigrationCatalogPreflightResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode result: %v", decodeErr)
	}
	if result.Success || len(result.Issues) != 1 || result.Issues[0] != "migration catalog directory is required" {
		t.Fatalf("result = %+v", result)
	}
}

func writeMigrationTestFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("-- +goose Up\nSELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
