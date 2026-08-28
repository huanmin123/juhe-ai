package businesshandoff

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestVerifyUsesIsolatedQueryOnlyProbe(t *testing.T) {
	dir := t.TempDir()
	business := filepath.Join(dir, "business.sqlite3")
	j3b := filepath.Join(dir, "j3b.sqlite3")
	for _, path := range []string{business, j3b} {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE marker (id INTEGER)"); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Verify(context.Background(), business, j3b)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || !report.PathsDistinct || !report.QueryOnlyEnabled || !report.WriteRejected || !report.IsolatedRowsUnchanged || report.UserDatabaseTouched {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.BusinessPath != canonicalPath(business) || report.J3BPath != canonicalPath(j3b) {
		t.Fatalf("paths not canonicalized: %+v", report)
	}
}

func TestVerifyRejectsSharedOrMissingPath(t *testing.T) {
	dir := t.TempDir()
	business := filepath.Join(dir, "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+business+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE marker (id INTEGER)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Verify(context.Background(), business, business)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || !report.SameFile || report.PathsDistinct {
		t.Fatalf("shared path must fail closed: %+v", report)
	}
	report, err = Verify(context.Background(), business, filepath.Join(dir, "missing.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.J3BExists {
		t.Fatalf("missing J3b path must fail closed: %+v", report)
	}
}

func TestVerifyDoesNotRequireWritableUserFiles(t *testing.T) {
	dir := t.TempDir()
	business := filepath.Join(dir, "business.sqlite3")
	j3b := filepath.Join(dir, "j3b.sqlite3")
	for _, path := range []string{business, j3b} {
		if err := os.WriteFile(path, []byte("not inspected as SQLite"), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Verify(context.Background(), business, j3b)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.UserDatabaseTouched {
		t.Fatalf("preflight should only stat user files: %+v", report)
	}
}
