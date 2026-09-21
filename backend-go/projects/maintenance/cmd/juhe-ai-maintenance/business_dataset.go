package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businessdataset"
)

// businessDatasetReadOnlyConfirmValue is the only accepted value of
// --business-dataset-confirm-readonly: it mirrors the schema snapshot
// JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY gate so a production
// export never runs without an explicit read-only assertion.
const businessDatasetReadOnlyConfirmValue = "READ_ONLY"

// businessDatasetAllowMissingFlag accumulates repeatable
// --business-dataset-allow-missing-column values ("table.column").
type businessDatasetAllowMissingFlag struct {
	values *[]string
}

func (f *businessDatasetAllowMissingFlag) String() string {
	if f == nil || f.values == nil {
		return ""
	}
	return strings.Join(*f.values, ",")
}

func (f *businessDatasetAllowMissingFlag) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.Contains(trimmed, ".") {
		return fmt.Errorf("必须以 table.column 形式登记，例如 system_accounts.extra_col")
	}
	*f.values = append(*f.values, trimmed)
	return nil
}

// businessDatasetExportResult returns the CLI exit code for
// --export-business-dataset; runMaintenance dispatches it and tests call it
// in-process. Preflight failures are usage errors (exit 2), runtime errors
// return 1, and an unready report (blockers) exits 3.
func businessDatasetExportResult(rawURL, dir, readonlyConfirm string) int {
	if strings.TrimSpace(rawURL) == "" {
		fmt.Fprintln(os.Stderr, "business dataset export requires --business-dataset-url with an explicit maintenance-scoped PostgreSQL URL")
		return 2
	}
	if strings.TrimSpace(dir) == "" {
		fmt.Fprintln(os.Stderr, "business dataset export requires --business-dataset-dir with an explicit output directory")
		return 2
	}
	if readonlyConfirm != businessDatasetReadOnlyConfirmValue {
		fmt.Fprintf(os.Stderr, "business dataset export requires --business-dataset-confirm-readonly=%s；该工具只允许只读导出\n", businessDatasetReadOnlyConfirmValue)
		return 2
	}
	db, err := businessdataset.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open business dataset export connection: %v\n", err)
		return 2
	}
	defer db.Close()
	report, runErr := businessdataset.ExportBusinessDataset(context.Background(), db, dir, businessdataset.ExportOptions{})
	return businessDatasetOutcomeExitCode("business dataset export", report, report.Ready(), runErr)
}

// businessDatasetImportResult keeps the original three-argument runner
// signature for existing callers and tests; runMaintenance dispatches the
// expected-db aware variant below. Behavior without the two expected-db flags
// is unchanged.
func businessDatasetImportResult(rawURL, dir string, allowedMissing []string) int {
	return businessDatasetImportResultWithExpectedDB(rawURL, dir, allowedMissing, "", "")
}

// businessDatasetImportResultWithExpectedDB returns the CLI exit code for
// --import-business-dataset. The manifest file is decoded up front so a
// missing or malformed dataset is a usage error instead of a runtime failure.
//
// expectedTargetDB / expectedSourceDB wire --business-dataset-expected-target-db
// and --business-dataset-expected-source-db. Both are optional, but each
// enables its own fail-closed identity gate: the target flag is compared
// strictly against the target database's actual current_database() and the
// source flag against the manifest's recorded sourceIdentity (which must not
// be a placeholder). A mismatch — or an identity that cannot be verified — is
// a usage error (exit 2) before the import transaction starts, and both
// expectations are re-enforced inside ImportBusinessDataset as blockers so the
// library gate stays reachable on the production path.
//
// 生产切流时两个 flag 都是必填项：runbook 必须同时提供
// --business-dataset-expected-target-db 与 --business-dataset-expected-source-db
// 并全部匹配；缺任何一项都不得执行切流导入。
func businessDatasetImportResultWithExpectedDB(rawURL, dir string, allowedMissing []string, expectedTargetDB, expectedSourceDB string) int {
	return businessDatasetImportResultWithReplace(rawURL, dir, allowedMissing, expectedTargetDB, expectedSourceDB, false)
}

// businessDatasetImportResultWithReplace keeps the full import runner in one
// place; replaceExisting wires --business-dataset-replace-existing: when true
// the import empties every whitelist data table in reverse foreign-key order
// inside the import transaction before inserting, so an already-seeded target
// database accepts the authoritative production rows instead of failing on
// primary-key conflicts. This is REQUIRED for the production flow of
// "authoritative schema + seed, then import the 40 production tables"
// (production cutover runbook section 6 step 3 imports after --seed). False
// keeps the insert-only fail-closed behavior unchanged.
func businessDatasetImportResultWithReplace(rawURL, dir string, allowedMissing []string, expectedTargetDB, expectedSourceDB string, replaceExisting bool) int {
	if strings.TrimSpace(rawURL) == "" {
		fmt.Fprintln(os.Stderr, "business dataset import requires --business-dataset-url with an explicit maintenance-scoped PostgreSQL URL")
		return 2
	}
	if strings.TrimSpace(dir) == "" {
		fmt.Fprintln(os.Stderr, "business dataset import requires --business-dataset-dir with an explicit dataset directory")
		return 2
	}
	manifest, _, err := businessdataset.LoadBusinessDatasetManifest(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "business dataset manifest input failed: %v\n", err)
		return 2
	}
	// 源库恒等门：--business-dataset-expected-source-db 传入时，manifest 记录
	// 的源库标识不得是占位值，且必须与期望严格一致。
	if expectedSource := strings.TrimSpace(expectedSourceDB); expectedSource != "" {
		if businessdataset.IsPlaceholderIdentity(manifest.SourceIdentity) {
			fmt.Fprintf(os.Stderr, "business dataset manifest sourceIdentity is the placeholder %q; --business-dataset-expected-source-db requires a manifest exported from a real source database\n", manifest.SourceIdentity)
			return 2
		}
		if strings.TrimSpace(manifest.SourceIdentity) != expectedSource {
			fmt.Fprintf(os.Stderr, "business dataset manifest sourceIdentity mismatch (manifest=%q expected=%q)\n", strings.TrimSpace(manifest.SourceIdentity), expectedSource)
			return 2
		}
	}
	db, err := businessdataset.Open(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open business dataset import connection: %v\n", err)
		return 2
	}
	defer db.Close()
	// 目标库恒等门：--business-dataset-expected-target-db 传入时，必须能在
	// 导入事务开始前核实目标 current_database() 并严格一致；核实失败同样
	// fail-closed（exit 2）。
	if expectedTarget := strings.TrimSpace(expectedTargetDB); expectedTarget != "" {
		var actual string
		if err := db.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&actual); err != nil {
			fmt.Fprintf(os.Stderr, "business dataset import could not verify the target database identity: %v\n", err)
			return 2
		}
		if strings.TrimSpace(actual) != expectedTarget {
			fmt.Fprintf(os.Stderr, "business dataset import target database identity mismatch (actual=%q expected=%q)\n", strings.TrimSpace(actual), expectedTarget)
			return 2
		}
	}
	report, runErr := businessdataset.ImportBusinessDataset(context.Background(), db, dir, businessdataset.ImportOptions{
		ExpectedTargetIdentity: strings.TrimSpace(expectedTargetDB),
		ExpectedSourceIdentity: strings.TrimSpace(expectedSourceDB),
		AllowedMissingColumns:  allowedMissing,
		ReplaceExisting:        replaceExisting,
	})
	return businessDatasetOutcomeExitCode("business dataset import", report, report.Ready(), runErr)
}

// businessDatasetOutcomeExitCode renders the JSON report on stdout and maps
// the outcome to the maintenance exit-code contract.
func businessDatasetOutcomeExitCode(stage string, report any, ready bool, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", stage, runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode %s report: %v\n", stage, err)
		return 1
	}
	if !ready {
		return 3
	}
	return 0
}
