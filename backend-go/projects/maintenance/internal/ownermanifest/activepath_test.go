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
	if report.RuleVersion != "j3b-active-path-v2" || report.ScannedFiles != 1 || len(report.Findings) != 1 || report.BlockedFindings != 1 || report.Findings[0].Pattern != "model-check-route" || report.Findings[0].Category != "management-route" || report.Findings[0].Disposition != "block" {
		t.Fatalf("report=%+v", report)
	}
}

func TestScanNodeJ3bActivePathsTreatsArchivedActiveTreeAsClean(t *testing.T) {
	root := t.TempDir()
	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Root != filepath.Join(root, "backend", "src") || report.ScannedFiles != 0 || report.BlockedFindings != 0 || len(report.Findings) != 0 {
		t.Fatalf("archived active tree must be clean: %+v", report)
	}
}

func TestScanNodeJ3bActivePathsIgnoresGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"backend/src/node_modules", "backend/src/dist"} {
		dir = filepath.Join(root, dir)
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
	if report.ScannedFiles != 0 || len(report.Findings) != 0 || len(report.Skipped) != 2 {
		t.Fatalf("generated files must be ignored: %+v", report)
	}
}

func TestScanNodeJ3bActivePathsReportsRulesAndSkips(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend", "src", "regression"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend", "src", "regression", "fixture.ts"), []byte("modelChecksRouter"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rules) != 12 || len(report.Skipped) != 1 || report.Skipped[0].Disposition != "allow" {
		t.Fatalf("rules/skips=%+v", report)
	}
}

func TestScanNodeJ3bActivePathsIgnoresMaintenanceMockdata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "backend", "src", "scripts", "maintenance", "mockdata")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "fixture.ts"), []byte("modelChecksRouter\nmodel_check_runs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedFiles != 0 || report.BlockedFindings != 0 || len(report.Skipped) != 1 || report.Skipped[0].Rule != "maintenance-fixture" {
		t.Fatalf("maintenance mockdata must be evidence-only: %+v", report)
	}
}

func TestScanNodeJ3bActivePathsBlocksTokenWorkerTrustAggregationAndStatsIPC(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "backend", "src", "modules", "model-checks")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte("import './model-checks-token-worker.service.js'\n" +
		"const legacy = startModelCheckTokenWorker\n" +
		"const schedule = 'model-trust-observation-aggregation'\n" +
		"const aggregate = 'aggregate_model_trust_observations'\n" +
		"const health = 'record_model_quality_health_failure'\n")
	if err := os.WriteFile(filepath.Join(source, "active.ts"), contents, 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := ScanNodeJ3bActivePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedFiles != 1 || len(report.Findings) != 5 || report.BlockedFindings != 5 {
		t.Fatalf("report=%+v", report)
	}
	want := map[string]string{
		"model-check-token-worker":          "token-worker",
		"model-check-token-worker-service":  "token-worker",
		"model-trust-aggregation-scheduler": "trust-aggregation",
		"model-trust-aggregation-stats-ipc": "stats-ipc",
		"model-quality-health-stats-ipc":    "stats-ipc",
	}
	for _, finding := range report.Findings {
		category, ok := want[finding.Pattern]
		if !ok {
			t.Fatalf("unexpected finding=%+v", finding)
		}
		if finding.Category != category || finding.Disposition != "block" {
			t.Fatalf("finding=%+v", finding)
		}
		delete(want, finding.Pattern)
	}
	if len(want) != 0 {
		t.Fatalf("missing findings=%v", want)
	}
}
