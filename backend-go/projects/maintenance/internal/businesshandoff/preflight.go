package businesshandoff

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Report is the machine-readable evidence produced by the Business SQLite
// handoff preflight. The user databases are only inspected with stat; the
// query_only write probe always runs against a disposable temporary SQLite
// file so this command cannot mutate an application database.
type Report struct {
	BusinessPath          string `json:"businessPath"`
	J3BPath               string `json:"j3bPath"`
	BusinessExists        bool   `json:"businessExists"`
	J3BExists             bool   `json:"j3bExists"`
	BusinessRegularFile   bool   `json:"businessRegularFile"`
	J3BRegularFile        bool   `json:"j3bRegularFile"`
	PathsDistinct         bool   `json:"pathsDistinct"`
	SameFile              bool   `json:"sameFile"`
	QueryOnlyEnabled      bool   `json:"queryOnlyEnabled"`
	WriteRejected         bool   `json:"writeRejected"`
	IsolatedRowsUnchanged bool   `json:"isolatedRowsUnchanged"`
	UserDatabaseTouched   bool   `json:"userDatabaseTouched"`
	// PathIsolationReady is the actual scope of this local preflight. It does
	// not establish a writer handoff, epoch, drain, rollback, or freshness.
	PathIsolationReady bool `json:"pathIsolationReady"`
	// HandoffReady intentionally remains false here: it can only be proven by
	// external cutover evidence from the real writer-drain window.
	HandoffReady bool     `json:"handoffReady"`
	Ready        bool     `json:"ready"`
	Errors       []string `json:"errors,omitempty"`
}

// Verify runs the non-mutating handoff preflight. Both configured files must
// already exist and be different regular files. It deliberately does not open
// either file through database/sql: path checks are sufficient here, and the
// write-rejection assertion is performed only on an isolated temporary file.
func Verify(ctx context.Context, businessPath, j3bPath string) (Report, error) {
	businessPath = canonicalPath(businessPath)
	j3bPath = canonicalPath(j3bPath)
	report := Report{BusinessPath: businessPath, J3BPath: j3bPath}
	if businessPath == "" {
		report.Errors = append(report.Errors, "Business SQLite path is empty")
	}
	if j3bPath == "" {
		report.Errors = append(report.Errors, "J3b SQLite path is empty")
	}
	if businessPath != "" && j3bPath != "" {
		report.PathsDistinct = !strings.EqualFold(businessPath, j3bPath)
		if !report.PathsDistinct {
			report.Errors = append(report.Errors, "Business SQLite and J3b SQLite paths are identical")
		}
	}

	businessInfo, businessErr := inspectPath(businessPath)
	if businessErr != nil {
		report.Errors = append(report.Errors, "Business SQLite: "+businessErr.Error())
	} else {
		report.BusinessExists = true
		report.BusinessRegularFile = businessInfo.Mode().IsRegular()
		if !report.BusinessRegularFile {
			report.Errors = append(report.Errors, "Business SQLite path is not a regular file")
		}
	}
	j3bInfo, j3bErr := inspectPath(j3bPath)
	if j3bErr != nil {
		report.Errors = append(report.Errors, "J3b SQLite: "+j3bErr.Error())
	} else {
		report.J3BExists = true
		report.J3BRegularFile = j3bInfo.Mode().IsRegular()
		if !report.J3BRegularFile {
			report.Errors = append(report.Errors, "J3b SQLite path is not a regular file")
		}
	}
	if businessErr == nil && j3bErr == nil {
		report.SameFile = os.SameFile(businessInfo, j3bInfo)
		if report.SameFile {
			report.PathsDistinct = false
			report.Errors = append(report.Errors, "Business SQLite and J3b SQLite resolve to the same file")
		}
	}

	if err := verifyQueryOnlyWriteRejection(ctx, &report); err != nil {
		return Report{}, err
	}
	report.UserDatabaseTouched = false
	report.PathIsolationReady = report.BusinessExists && report.J3BExists && report.BusinessRegularFile && report.J3BRegularFile && report.PathsDistinct && !report.SameFile && report.QueryOnlyEnabled && report.WriteRejected && report.IsolatedRowsUnchanged && len(report.Errors) == 0
	// Keep Ready as the compatibility alias consumed by the existing path
	// isolation CLI. Consumers that need an owner handoff must require the
	// separate cutover-evidence gate instead.
	report.Ready = report.PathIsolationReady
	sort.Strings(report.Errors)
	return report, nil
}

func canonicalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func inspectPath(path string) (os.FileInfo, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %w", err)
	}
	return info, nil
}

func verifyQueryOnlyWriteRejection(ctx context.Context, report *Report) error {
	tempDir, err := os.MkdirTemp("", "juhe-business-handoff-")
	if err != nil {
		return fmt.Errorf("create isolated SQLite directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	tempPath := filepath.Join(tempDir, "probe.sqlite3")
	seed, err := sql.Open("sqlite", "file:"+tempPath+"?mode=rwc")
	if err != nil {
		return fmt.Errorf("open isolated SQLite seed: %w", err)
	}
	seed.SetMaxOpenConns(1)
	if _, err := seed.ExecContext(ctx, "CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
		seed.Close()
		return fmt.Errorf("seed isolated SQLite: %w", err)
	}
	if err := seed.Close(); err != nil {
		return fmt.Errorf("close isolated SQLite seed: %w", err)
	}

	readOnly, err := sql.Open("sqlite", "file:"+tempPath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return fmt.Errorf("open isolated query_only SQLite: %w", err)
	}
	defer readOnly.Close()
	readOnly.SetMaxOpenConns(1)
	var queryOnly int
	if err := readOnly.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return fmt.Errorf("read isolated SQLite query_only pragma: %w", err)
	}
	report.QueryOnlyEnabled = queryOnly == 1
	if !report.QueryOnlyEnabled {
		report.Errors = append(report.Errors, "isolated SQLite query_only pragma is not enabled")
	}

	_, writeErr := readOnly.ExecContext(ctx, "INSERT INTO probe(id) VALUES (1)")
	report.WriteRejected = writeErr != nil
	if !report.WriteRejected {
		report.Errors = append(report.Errors, "isolated query_only SQLite unexpectedly accepted a write")
	}
	var rows int
	if err := readOnly.QueryRowContext(ctx, "SELECT COUNT(*) FROM probe").Scan(&rows); err != nil {
		return fmt.Errorf("read isolated SQLite probe row count: %w", err)
	}
	report.IsolatedRowsUnchanged = rows == 0
	if !report.IsolatedRowsUnchanged {
		report.Errors = append(report.Errors, "isolated SQLite probe rows changed")
	}
	return nil
}
