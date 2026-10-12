package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

const w1InvalidPostgresSchemeURL = "sqlite:///w1-invalid"
const w1InvalidPGSSLModeURL = "postgres://w1@127.0.0.1:5432/db?sslmode=w1_invalid"

// w14kFailConn is shared by the storage snapshot gap tests.  Its catalog
// routes include PostgreSQL ANY($1::text[]) calls, so this local test seam
// must advertise the same []string conversion that the snapshot fake uses.
func (c *w14kFailConn) CheckNamedValue(nv *driver.NamedValue) error {
	if values, ok := nv.Value.([]string); ok {
		nv.Value = strings.Join(values, ",")
		return nil
	}
	if driver.IsValue(nv.Value) {
		return nil
	}
	return errors.New("w14k fail fake: unsupported argument")
}

func TestW1CLIJ3BootstrapOutcomeExitCodes(t *testing.T) {
	tests := []struct {
		name        string
		failed      func(error) int
		unready     func() int
		ready       func() int
		failedText  string
		unreadyMark string
	}{
		{
			name: "j3a",
			failed: func(err error) int {
				return j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{}, err)
			},
			unready: func() int {
				return j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{MissingTables: []string{"j3a_missing"}}, nil)
			},
			ready:       func() int { return j3aBootstrapOutcomeExitCode(j3aproxylatency.Report{}, nil) },
			failedText:  "J3a PostgreSQL bootstrap failed: sentinel failure\n",
			unreadyMark: `"missingTables":["j3a_missing"]`,
		},
		{
			name: "j3b postgres",
			failed: func(err error) int {
				return j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{}, err)
			},
			unready: func() int {
				return j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{MissingIndexes: []string{"j3b_missing"}}, nil)
			},
			ready:       func() int { return j3bBootstrapOutcomeExitCode(j3bmodelcheck.Report{}, nil) },
			failedText:  "J3b PostgreSQL bootstrap failed: sentinel failure\n",
			unreadyMark: `"missingIndexes":["j3b_missing"]`,
		},
		{
			name: "j3b sqlite",
			failed: func(err error) int {
				return j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{}, err)
			},
			unready: func() int {
				return j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{MissingTables: []string{"j3b_sqlite_missing"}}, nil)
			},
			ready: func() int {
				return j3bSQLiteBootstrapOutcomeExitCode(j3bmodelcheck.SQLiteReport{}, nil)
			},
			failedText:  "J3b SQLite bootstrap failed: sentinel failure\n",
			unreadyMark: `"missingTables":["j3b_sqlite_missing"]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var code int
			stdout, stderr := bdCaptureOutput(t, func() {
				code = test.failed(errors.New("sentinel failure"))
			})
			if code != 1 || stdout != "" || stderr != test.failedText {
				t.Fatalf("error exit/output = %d/%q/%q, want 1/empty/%q", code, stdout, stderr, test.failedText)
			}

			stdout, stderr = bdCaptureOutput(t, func() { code = test.unready() })
			if code != 3 || stderr != "" || !strings.Contains(stdout, test.unreadyMark) {
				t.Fatalf("unready exit/output = %d/%q/%q, want exit 3 and report %q", code, stdout, stderr, test.unreadyMark)
			}

			stdout, stderr = bdCaptureOutput(t, func() { code = test.ready() })
			var report map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("ready path must emit JSON: %v; stdout=%q", err, stdout)
			}
			if code != 0 || stderr != "" || len(report) == 0 {
				t.Fatalf("ready exit/output = %d/%q/%q, want exit 0 with JSON", code, stdout, stderr)
			}

			stderr = w1CaptureStderrWithClosedStdout(t, func() { code = test.ready() })
			if code != 1 || !strings.Contains(stderr, "encode ") {
				t.Fatalf("encode failure exit/stderr = %d/%q, want exit 1 with encode error", code, stderr)
			}
		})
	}
}

func w1CaptureStderrWithClosedStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedStderr := os.Stderr
	os.Stderr = writer
	var stderr strings.Builder
	var copyWG sync.WaitGroup
	copyWG.Add(1)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(&stderr, reader)
	}()
	defer func() { os.Stderr = savedStderr }()
	wm14kWithClosedStdout(t, fn)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	copyWG.Wait()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return stderr.String()
}

func TestW1CLIMaintenanceFlagParseContract(t *testing.T) {
	code, stdout, stderr := wmRunMaintenanceCapture(t, "-h")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "Usage of ") {
		t.Fatalf("help exit/output = %d/%q/%q, want exit 0 and usage on stderr", code, stdout, stderr)
	}

	code, stdout, stderr = wmRunMaintenanceCapture(t, "-w1-unknown-flag")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "flag provided but not defined: -w1-unknown-flag") {
		t.Fatalf("unknown flag exit/output = %d/%q/%q, want exit 2 and original flag error", code, stdout, stderr)
	}
}

func TestW1CLIPostgresURLValidationErrorsAreLocalAndPreserved(t *testing.T) {
	storageErr := "--dsn 必须是 postgres:// 或 postgresql:// URL"
	if _, err := openPostgresBootstrap(w1InvalidPostgresSchemeURL); err == nil || err.Error() != storageErr {
		t.Fatalf("storage bootstrap URL validation error = %v, want %q", err, storageErr)
	}
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = runStorageBootstrap(true, false, "postgres", "", w1InvalidPostgresSchemeURL, "")
	})
	if code != 2 || stdout != "" || stderr != storageErr+"\n" {
		t.Fatalf("storage bootstrap URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, storageErr+"\n")
	}

	t.Run("j3a", func(t *testing.T) {
		t.Setenv(j3aproxylatency.BootstrapEnv, w1InvalidPostgresSchemeURL)
		var code int
		stdout, stderr := bdCaptureOutput(t, func() { code = j3aProxyLatencyBootstrapResult(false) })
		want := "open J3a PostgreSQL bootstrap connection: J3a bootstrap 必须提供 postgres/postgresql URL\n"
		if code != 2 || stdout != "" || stderr != want {
			t.Fatalf("J3a PostgreSQL URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, want)
		}
	})

	t.Run("j3b", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.BootstrapEnv, w1InvalidPostgresSchemeURL)
		var code int
		stdout, stderr := bdCaptureOutput(t, func() { code = j3bModelCheckBootstrapResult(false) })
		want := "open J3b PostgreSQL bootstrap connection: J3b bootstrap 必须提供包含主机、数据库和显式角色的 postgres URL\n"
		if code != 2 || stdout != "" || stderr != want {
			t.Fatalf("J3b PostgreSQL URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, want)
		}
	})

	t.Run("account capability domain", func(t *testing.T) {
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = accountCapabilityDomainVerifyResult("postgres", "", w1InvalidPostgresSchemeURL, capabilityDomainTestSecret, "m1", "preflight")
		})
		want := "capability domain 迁移必须提供 postgres:// 或 postgresql:// URL\n"
		if code != 2 || stdout != "" || stderr != want {
			t.Fatalf("capability-domain PostgreSQL URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, want)
		}
	})

	t.Run("account balance", func(t *testing.T) {
		t.Setenv(accountBalanceBootstrapEnv, w1InvalidPostgresSchemeURL)
		var code int
		stdout, stderr := bdCaptureOutput(t, func() { code = accountBalanceBootstrapResult() })
		want := "open J2 account-balance PostgreSQL bootstrap connection: J2 account-balance bootstrap 必须提供 postgres/postgresql URL\n"
		if code != 2 || stdout != "" || stderr != want {
			t.Fatalf("account-balance PostgreSQL URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, want)
		}
	})

	t.Run("usage stats rebuild", func(t *testing.T) {
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult("postgres", "", w1InvalidPostgresSchemeURL, 1, 1, true)
		})
		want := storageErr + "\n"
		if code != 2 || stdout != "" || stderr != want {
			t.Fatalf("usage rebuild PostgreSQL URL error = %d/%q/%q, want exit 2 with original %q", code, stdout, stderr, want)
		}
	})
}

func TestW1CLIRuntimePostgresConfigErrorsStayLocal(t *testing.T) {
	t.Run("storage ensure", func(t *testing.T) {
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = runStorageBootstrap(true, false, "postgres", "", w1InvalidPGSSLModeURL, "")
		})
		if code != 1 || stdout != "" || !strings.Contains(stderr, "postgres ensure-schema 失败：") || !strings.Contains(stderr, "sslmode is invalid") {
			t.Fatalf("storage local PG config error = %d/%q/%q, want exit 1 with sslmode error", code, stdout, stderr)
		}
	})

	t.Run("j3a", func(t *testing.T) {
		t.Setenv(j3aproxylatency.BootstrapEnv, w1InvalidPGSSLModeURL)
		var code int
		stdout, stderr := bdCaptureOutput(t, func() { code = j3aProxyLatencyBootstrapResult(false) })
		if code != 1 || stdout != "" || !strings.Contains(stderr, "J3a PostgreSQL bootstrap failed:") || !strings.Contains(stderr, "sslmode is invalid") {
			t.Fatalf("J3a local PG config error = %d/%q/%q, want exit 1 with sslmode error", code, stdout, stderr)
		}
	})

	t.Run("j3b", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.BootstrapEnv, w1InvalidPGSSLModeURL)
		var code int
		stdout, stderr := bdCaptureOutput(t, func() { code = j3bModelCheckBootstrapResult(false) })
		if code != 1 || stdout != "" || !strings.Contains(stderr, "J3b PostgreSQL bootstrap failed:") || !strings.Contains(stderr, "sslmode is invalid") {
			t.Fatalf("J3b local PG config error = %d/%q/%q, want exit 1 with sslmode error", code, stdout, stderr)
		}
	})

	t.Run("capability-domain opener remains lazy", func(t *testing.T) {
		db, dialect, code := openCapabilityDomainDB("postgres", "", w1InvalidPGSSLModeURL)
		if code != 0 || db == nil || dialect != capdomain.DialectPostgres {
			t.Fatalf("capability-domain PG open = db:%v dialect:%v code:%d, want lazy db and code 0", db != nil, dialect, code)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("usage stats rebuild", func(t *testing.T) {
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult("postgres", "", w1InvalidPGSSLModeURL, 1, 1, true)
		})
		if code != 1 || stdout != "" || !strings.Contains(stderr, "统计缓存离线重建失败：") || !strings.Contains(stderr, "sslmode is invalid") {
			t.Fatalf("usage rebuild local PG config error = %d/%q/%q, want exit 1 with sslmode error", code, stdout, stderr)
		}
	})
}

func TestW1RebuildUsageStatsFlagErrorsAndReportEncoding(t *testing.T) {
	cases := []struct {
		name string
		call func() int
		want string
	}{
		{name: "sqlite dsn", call: func() int {
			return rebuildUsageStatsResult("sqlite", "business=b,stats=s", "postgres://ignored", 1, 1, true)
		}, want: "--dsn 只适用于 --driver postgres"},
		{name: "postgres paths", call: func() int { return rebuildUsageStatsResult("postgres", "business=b", "postgres://ignored", 1, 1, true) }, want: "--paths 只适用于 --driver sqlite"},
		{name: "postgres missing dsn", call: func() int { return rebuildUsageStatsResult("postgres", "", "", 1, 1, true) }, want: "--driver postgres 需要 --dsn"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr := bdCaptureOutput(t, func() {
				if code := test.call(); code != 2 {
					t.Fatalf("usage exit = %d, want 2", code)
				}
			})
			if stdout != "" || !strings.Contains(stderr, test.want) {
				t.Fatalf("usage output = %q/%q, want stderr %q", stdout, stderr, test.want)
			}
		})
	}

	t.Run("successful empty source report encode failure", func(t *testing.T) {
		_, statsPath, _, paths := rebuildFixturePaths(t)
		statsDB := seedRebuildUsageMirror(t, statsPath, nil)
		if err := statsDB.Close(); err != nil {
			t.Fatal(err)
		}
		var code int
		stderr := w1CaptureStderrWithClosedStdout(t, func() {
			code = rebuildUsageStatsResult("sqlite", paths, "", 1, 1, true)
		})
		if code != 1 || !strings.Contains(stderr, "encode rebuild-usage-stats report:") {
			t.Fatalf("report encode failure = %d/%q, want exit 1 with encode error", code, stderr)
		}
	})
}

func TestW1CapabilityDomainM2FailureReport(t *testing.T) {
	paths := capabilityDomainPaths(t)
	seedCapabilityDomainAccounts(t, paths, map[string][]string{"acct-bad-m2": {"chat_json"}})
	var setupCode int
	setupStdout, setupStderr := bdCaptureOutput(t, func() {
		setupCode = accountCapabilityDomainMigrateResult("sqlite", paths, "", capabilityDomainTestSecret, "m1", true, true, true)
	})
	if setupCode != 0 || setupStderr != "" || !strings.Contains(setupStdout, `"completedStage":"m1_complete"`) {
		t.Fatalf("prepare completed M1 stage = %d/%q/%q, want successful report", setupCode, setupStdout, setupStderr)
	}
	db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(paths, "business="))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`, "invalid-v1-envelope", "acct-bad-m2"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = accountCapabilityDomainMigrateResult("sqlite", paths, "", capabilityDomainTestSecret, "m2-cutover", true, true, true)
	})
	if code != 3 || stdout == "" || !strings.Contains(stderr, "能力域迁移未完成：") || !strings.Contains(stderr, "保留 m2_running 阶段") {
		t.Fatalf("M2 failure report = %d/%q/%q, want exit 3 with m2_running report", code, stdout, stderr)
	}
}

func TestW1RemainingLocalHelperErrorEdges(t *testing.T) {
	t.Run("capability paths ignore empty entries", func(t *testing.T) {
		business, err := parseCapabilityDomainBusinessPath(",, business=" + filepath.Join(t.TempDir(), "business.sqlite3") + ",,")
		if err != nil || business == "" {
			t.Fatalf("empty path entries = %q/%v, want trimmed business path", business, err)
		}
	})

	t.Run("fresh-install seal preserves missing-schema error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.sqlite3")
		db, err := bootstrap.OpenSQLiteFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		err = sealFreshInstallCapabilityDomain(context.Background(), db, capdomain.DialectSQLite)
		if err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Fatalf("fresh-install seal error = %v, want missing-schema error", err)
		}
	})

	t.Run("snapshot commit failure remains observable", func(t *testing.T) {
		conn := &w14kFailConn{routes: wmSnapshotRoutes(), failCommit: true}
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest)
		if err == nil || !strings.Contains(err.Error(), "commit 失败") {
			t.Fatalf("snapshot commit error = %v, want original commit failure", err)
		}
	})
}

func TestW1SuccessCostBackfillCLIInputAndOpenErrors(t *testing.T) {
	// An existing directory reaches the SQLite open/PRAGMA error path without
	// letting OpenSQLiteFile create a new database file first.
	missingStats := t.TempDir()
	cases := []struct {
		name     string
		driver   string
		paths    string
		dsn      string
		dryRun   bool
		wantCode int
		wantErr  string
	}{
		{name: "unknown driver", driver: "mysql", wantCode: 2, wantErr: "--driver 必须为 sqlite 或 postgres"},
		{name: "sqlite missing database apply", driver: "sqlite", paths: "stats=" + missingStats, wantCode: 2, wantErr: "unable to open database file"},
		{name: "sqlite missing database dry run", driver: "sqlite", paths: "stats=" + missingStats, dryRun: true, wantCode: 1, wantErr: "open read-only sqlite stats"},
		{name: "postgres missing dsn", driver: "postgres", wantCode: 2, wantErr: "--driver postgres 需要 --dsn"},
		{name: "postgres invalid scheme", driver: "postgres", dsn: "sqlite:///w1-invalid", wantCode: 2, wantErr: "必须是 postgres:// 或 postgresql://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stdout, stderr := bdCaptureOutput(t, func() {
				code = applySuccessCostBackfillResult(tc.driver, tc.paths, tc.dsn, tc.dryRun)
			})
			if code != tc.wantCode || stdout != "" || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("success-cost input/open error = %d/%q/%q, want code %d and %q", code, stdout, stderr, tc.wantCode, tc.wantErr)
			}
		})
	}
}

func TestW1J3bSQLiteBootstrapOpenError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, dir)
	_, openErr := j3bmodelcheck.OpenSQLite(dir)
	if openErr == nil {
		t.Fatal("a directory cannot be opened as the dedicated SQLite database file")
	}
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = j3bModelCheckSQLiteBootstrapResult(false, false, false, false)
	})
	want := "open J3b SQLite bootstrap connection: " + openErr.Error() + "\n"
	if code != 2 || stdout != "" || stderr != want {
		t.Fatalf("J3b SQLite open error = %d/%q/%q, want exit 2 and original %q", code, stdout, stderr, want)
	}
}

func TestW1CapabilityDomainPathAndOpenErrors(t *testing.T) {
	parseCases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing business", raw: "chat=ignored", want: "--paths 缺少必填 key：business（能力域迁移/核验只读写业务库）"},
		{name: "malformed entry", raw: "business", want: "--paths 条目必须是 key=value 形式：\"business\""},
		{name: "empty key", raw: "=path", want: "--paths 条目必须是 key=value 形式：\"=path\""},
		{name: "empty value", raw: "business= ", want: "--paths 条目必须是 key=value 形式：\"business=\""},
		{name: "duplicate key", raw: "business=a,business=b", want: "--paths 条目重复：\"business\""},
	}
	for _, test := range parseCases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCapabilityDomainBusinessPath(test.raw)
			if err == nil || err.Error() != test.want {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}

	openCases := []struct {
		name   string
		driver string
		paths  string
		dsn    string
		want   string
	}{
		{name: "invalid driver", driver: "mysql", want: "--driver 必须为 sqlite 或 postgres: \"mysql\""},
		{name: "sqlite with dsn", driver: "sqlite", paths: "business=x", dsn: "postgres://u@h/db", want: "--dsn 只适用于 --driver postgres；sqlite 模式使用 --paths"},
		{name: "postgres with paths", driver: "postgres", paths: "business=x", dsn: "postgres://u@h/db", want: "--paths 只适用于 --driver sqlite；postgres 模式使用 --dsn"},
		{name: "postgres missing dsn", driver: "postgres", want: "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL"},
		{name: "sqlite parse failure", driver: "sqlite", paths: "business", want: "--paths 条目必须是 key=value 形式：\"business\""},
		{name: "postgres wrong scheme", driver: "postgres", dsn: "sqlite:///w1-invalid", want: "capability domain 迁移必须提供 postgres:// 或 postgresql:// URL"},
	}
	for _, test := range openCases {
		t.Run(test.name, func(t *testing.T) {
			var dbClosed bool
			var code int
			stdout, stderr := bdCaptureOutput(t, func() {
				db, _, got := openCapabilityDomainDB(test.driver, test.paths, test.dsn)
				code = got
				if db != nil {
					dbClosed = db.Close() == nil
				}
			})
			if code != 2 || stdout != "" || !strings.Contains(stderr, test.want) {
				t.Fatalf("open result = %d/%q/%q, want exit 2 and %q", code, stdout, stderr, test.want)
			}
			if dbClosed {
				t.Fatal("usage errors must return before opening a database")
			}
		})
	}

	dir := t.TempDir()
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = openCapabilityDomainSQLiteDirectory(dir)
	})
	if code != 2 || stdout != "" || !strings.Contains(stderr, "初始化业务 SQLite PRAGMA 失败") || !strings.Contains(stderr, "unable to open database file") {
		t.Fatalf("SQLite directory failure = %d/%q/%q, want exit 2 with original PRAGMA error", code, stdout, stderr)
	}
}

func openCapabilityDomainSQLiteDirectory(dir string) int {
	db, _, code := openCapabilityDomainDB("sqlite", "business="+dir, "")
	if db != nil {
		_ = db.Close()
	}
	return code
}

func TestW1CapabilityDomainVerifyErrorAndFailureReport(t *testing.T) {
	t.Run("runtime error preserves sqlite reason with no schema writes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-business.sqlite3")
		paths := "business=" + path
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-verify-account-capability-domain",
			"-account-capability-domain-stage=m1",
			"-account-capability-domain-check=preflight",
			"-driver", "sqlite", "-paths", paths, "-secret", capabilityDomainTestSecret)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "能力域核验失败：") || !strings.Contains(stderr, "no such table: global_settings") {
			t.Fatalf("verify runtime error = %d/%q/%q, want exit 1 with original missing-table error", code, stdout, stderr)
		}
		db, err := capabilitydomain.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var tables int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
			t.Fatal(err)
		}
		if tables != 0 {
			t.Fatalf("read-only verify error created %d tables", tables)
		}
	})

	t.Run("postflight failure report sorts account ids", func(t *testing.T) {
		paths := capabilityDomainPaths(t)
		seedCapabilityDomainAccounts(t, paths, map[string][]string{
			"acct-z": {},
			"acct-a": {},
		})
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-verify-account-capability-domain",
			"-account-capability-domain-stage=m1",
			"-account-capability-domain-check=postflight",
			"-driver", "sqlite", "-paths", paths, "-secret", capabilityDomainTestSecret)
		var report capabilitydomain.VerifyReport
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("failure report JSON: %v; stdout=%q", err, stdout)
		}
		if code != 3 || len(report.Failures) != 2 || report.Failures[0].AccountID != "acct-a" || report.Failures[1].AccountID != "acct-z" {
			t.Fatalf("verify failures = exit %d, report %+v; want exit 3 sorted [acct-a acct-z]", code, report.Failures)
		}
		if !strings.Contains(stderr, "能力域核验未通过：2 项失败") {
			t.Fatalf("failure summary missing: %q", stderr)
		}
	})
}

func TestW1RebuildSQLitePathErrorsAndIgnoredKeys(t *testing.T) {
	if business, stats, err := parseRebuildSQLitePaths("business=b,,chat=c,dataset=d,usage-catalog=u,stats=s,codex-context-shard-root=r,codex-context-shard-count=2,"); err != nil || business != "b" || stats != "s" {
		t.Fatalf("valid full storage paths = %q/%q/%v", business, stats, err)
	}
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "business", want: "--paths 条目必须是 key=value 形式：\"business\""},
		{raw: "business=,stats=s", want: "--paths 条目必须是 key=value 形式：\"business=\""},
		{raw: "business=a,business=b,stats=s", want: "--paths 条目重复：\"business\""},
		{raw: "business=a,stats=s,unknown=x", want: "--paths 未知 key \"unknown\"（有效 key：business、chat、dataset、usage-catalog、stats、codex-context-shard-root、codex-context-shard-count）"},
		{raw: "business=a", want: "--paths 缺少必填 key：stats（统计重建读写 stats 库，并以只读方式打开 business 库）"},
		{raw: "stats=s", want: "--paths 缺少必填 key：business（统计重建读写 stats 库，并以只读方式打开 business 库）"},
		{raw: "", want: "--paths 缺少必填 key：business、stats（统计重建读写 stats 库，并以只读方式打开 business 库）"},
	}
	for _, test := range cases {
		t.Run(test.raw, func(t *testing.T) {
			_, _, err := parseRebuildSQLitePaths(test.raw)
			if err == nil || err.Error() != test.want {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestW1RebuildSQLiteOpenErrorsPreserveOriginalError(t *testing.T) {
	t.Run("stats path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		_, openErr := bootstrap.OpenSQLiteFile(dir)
		if openErr == nil {
			t.Fatal("directory path must fail SQLite open")
		}
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult("sqlite", "business=unused,stats="+dir, "", 1, 1, true)
		})
		if code != 2 || stdout != "" || stderr != openErr.Error()+"\n" {
			t.Fatalf("stats open error = %d/%q/%q, want exit 2 and original %q", code, stdout, stderr, openErr.Error()+"\n")
		}
	})

	t.Run("business path is a directory after stats open", func(t *testing.T) {
		dir := t.TempDir()
		statsPath := filepath.Join(dir, "stats.sqlite3")
		_, openErr := bootstrap.OpenSQLiteFile(dir)
		if openErr == nil {
			t.Fatal("directory path must fail SQLite open")
		}
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult("sqlite", "business="+dir+",stats="+statsPath, "", 1, 1, true)
		})
		if code != 2 || stdout != "" || stderr != openErr.Error()+"\n" {
			t.Fatalf("business open error = %d/%q/%q, want exit 2 and original %q", code, stdout, stderr, openErr.Error()+"\n")
		}
		if _, err := os.Stat(statsPath); err != nil {
			t.Fatalf("stats handle is opened before the business handle; expected its temp file to exist: %v", err)
		}
	})

	t.Run("offline database schema error exits one without data changes", func(t *testing.T) {
		dir := t.TempDir()
		statsPath := filepath.Join(dir, "stats.sqlite3")
		businessPath := filepath.Join(dir, "business.sqlite3")
		for _, path := range []string{statsPath, businessPath} {
			db, err := bootstrap.OpenSQLiteFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult("sqlite", "business="+businessPath+",stats="+statsPath, "", 1, 1, true)
		})
		if code != 1 || stdout != "" || !strings.Contains(stderr, "统计缓存离线重建失败：") || !strings.Contains(stderr, "no such table: background_job_leases") {
			t.Fatalf("rebuild schema error = %d/%q/%q, want exit 1 with original offline-check error", code, stdout, stderr)
		}
		db, err := bootstrap.OpenSQLiteFile(statsPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var tables int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
			t.Fatal(err)
		}
		if tables != 0 {
			t.Fatalf("failed offline check must happen before schema or data changes; found %d tables", tables)
		}
	})
}

func TestW1CapabilityDomainMigrationOpenFailurePreservesError(t *testing.T) {
	dir := t.TempDir()
	_, openErr := capabilitydomain.OpenSQLite(dir)
	if openErr == nil {
		t.Fatal("directory path must fail capability-domain SQLite open")
	}
	var code int
	stdout, stderr := bdCaptureOutput(t, func() {
		code = accountCapabilityDomainMigrateResult("sqlite", "business="+dir, "", capabilityDomainTestSecret, "m1", true, true, true)
	})
	if code != 2 || stdout != "" || stderr != openErr.Error()+"\n" {
		t.Fatalf("capability-domain migration open error = %d/%q/%q, want exit 2 and original %q", code, stdout, stderr, openErr.Error()+"\n")
	}
}

func TestW1CapabilityDomainReportEncodingFailures(t *testing.T) {
	t.Run("migration report failure follows committed migration", func(t *testing.T) {
		paths := capabilityDomainPaths(t)
		seedCapabilityDomainAccounts(t, paths, map[string][]string{"acct-chat": {"chat_json"}})
		var code int
		stderr := w1CaptureStderrWithClosedStdout(t, func() {
			code = accountCapabilityDomainMigrateResult("sqlite", paths, "", capabilityDomainTestSecret, "m1", true, true, true)
		})
		if code != 1 || !strings.Contains(stderr, "encode capability domain migration report:") {
			t.Fatalf("migration encode failure = %d/%q, want exit 1 with encode error", code, stderr)
		}
		db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(paths, "business="))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var raw string
		if err := db.QueryRow(`SELECT value_json FROM global_settings WHERE key = ?`, capdomain.MigrationSettingKey).Scan(&raw); err != nil {
			t.Fatalf("migration must commit before report encoding: %v", err)
		}
		record, err := capdomain.ParseRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
		if record.Stage != capdomain.StageM1Complete {
			t.Fatalf("committed migration stage = %q, want %q", record.Stage, capdomain.StageM1Complete)
		}
	})

	t.Run("verify report failure leaves migration state absent", func(t *testing.T) {
		paths := capabilityDomainPaths(t)
		seedCapabilityDomainAccounts(t, paths, map[string][]string{"acct-chat": {"chat_json"}})
		var code int
		stderr := w1CaptureStderrWithClosedStdout(t, func() {
			code = accountCapabilityDomainVerifyResult("sqlite", paths, "", capabilityDomainTestSecret, "m1", "preflight")
		})
		if code != 1 || !strings.Contains(stderr, "encode capability domain verify report:") {
			t.Fatalf("verify encode failure = %d/%q, want exit 1 with encode error", code, stderr)
		}
		db, err := capabilitydomain.OpenSQLite(strings.TrimPrefix(paths, "business="))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var records int
		if err := db.QueryRow(`SELECT count(*) FROM global_settings WHERE key = ?`, capdomain.MigrationSettingKey).Scan(&records); err != nil {
			t.Fatal(err)
		}
		if records != 0 {
			t.Fatal("read-only verify must not create migration state")
		}
	})
}

func TestW1ChatBindingMigrationRunnerOutputAndErrors(t *testing.T) {
	t.Run("runtime error preserves missing table", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-chat.sqlite3")
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-migrate-chat-account-only-binding", "-driver", "sqlite", "-chat-sqlite-path", path)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "chat account-only binding migration failed:") || !strings.Contains(stderr, "chat_conversations 表不存在") {
			t.Fatalf("chat migration error = %d/%q/%q, want exit 1 with original missing-table error", code, stdout, stderr)
		}
	})

	t.Run("success emits JSON and rollback SQL", func(t *testing.T) {
		path := w1ChatBindingLegacySQLite(t)
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-migrate-chat-account-only-binding", "-driver", "sqlite", "-chat-sqlite-path", path)
		if code != 0 || stderr != "" || !strings.Contains(stdout, `"driver":"sqlite"`) || !strings.Contains(stdout, "ALTER TABLE chat_conversations ADD COLUMN bind_mode") {
			t.Fatalf("chat migration success = %d/%q/%q, want JSON report and rollback SQL", code, stdout, stderr)
		}
	})

	t.Run("encode error is reported after migration", func(t *testing.T) {
		path := w1ChatBindingLegacySQLite(t)
		var code int
		stderr := w1CaptureStderrWithClosedStdout(t, func() {
			code = chatAccountOnlyBindingMigrationResult("sqlite", path, "")
		})
		if code != 1 || !strings.Contains(stderr, "encode chat account-only binding migration report:") {
			t.Fatalf("chat migration encode failure = %d/%q, want exit 1 with encode error", code, stderr)
		}
		db := openChatBindingTestSQLite(t, path)
		defer db.Close()
		rows, err := db.Query("PRAGMA table_info(chat_conversations)")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notNull, pk int
			var name, declaredType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			if name == "bind_mode" {
				t.Fatal("migration must have committed the column removal before report encoding")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	})
}

func w1ChatBindingLegacySQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.sqlite3")
	db := openChatBindingTestSQLite(t, path)
	if _, err := schema.EnsureSQLiteChat(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("ensure chat schema: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE chat_conversations ADD COLUMN bind_mode TEXT NOT NULL DEFAULT 'api_key'`,
		`ALTER TABLE chat_conversations ADD COLUMN bind_group_id TEXT`,
		`ALTER TABLE chat_conversations ADD COLUMN bind_group_name_snapshot TEXT`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("add legacy binding column: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
