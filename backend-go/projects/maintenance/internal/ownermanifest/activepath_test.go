package ownermanifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanNodeJ3bActivePathsReportsKnownEntrypoints(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "backend", "src", "modules")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "model-checks.ts"), []byte("const modelChecksRouter = Router()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedFiles != 1 || len(report.Findings) != 1 || report.Findings[0].Pattern != "model-check-route" {
		t.Fatalf("report=%+v", report)
	}
}

func TestScanNodeJ3bActivePathsIgnoresGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"backend/src/node_modules", "backend/src/dist"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "generated.ts"), []byte("modelChecksRouter"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "backend", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedFiles != 0 || len(report.Findings) != 0 {
		t.Fatalf("generated files must be ignored: %+v", report)
	}
}
