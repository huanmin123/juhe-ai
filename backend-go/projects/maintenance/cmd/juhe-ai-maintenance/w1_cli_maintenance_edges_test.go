package main

// w1 波次 CLI 边缘测试：J3a/J3b outcome 出口映射、flag 解析契约、PostgreSQL
// URL/sslmode 本地错误保留、usage rebuild 用法门、snapshot 会话提交失败可见。
// SQLite 载体测试（J3b SQLite bootstrap、能力域 --paths 解析与 SQLite 端到端、
// 成本回填 --paths 开库错误、chat 绑定 SQLite 文件流）随 SQLite 存储形态退役
// 删除（PLAN-20261024T051925945Z B4）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3aproxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
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
		code = runStorageBootstrap(true, false, w1InvalidPostgresSchemeURL, "")
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
			code = accountCapabilityDomainVerifyResult(w1InvalidPostgresSchemeURL, capabilityDomainTestSecret, "m1", "preflight")
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
			code = rebuildUsageStatsResult(w1InvalidPostgresSchemeURL, 1, 1, true)
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
			code = runStorageBootstrap(true, false, w1InvalidPGSSLModeURL, "")
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

	t.Run("business opener remains lazy", func(t *testing.T) {
		db, code := openBusinessPostgres(w1InvalidPGSSLModeURL)
		if code != 0 || db == nil {
			t.Fatalf("business PG open = db:%v code:%d, want lazy db and code 0", db != nil, code)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("usage stats rebuild", func(t *testing.T) {
		var code int
		stdout, stderr := bdCaptureOutput(t, func() {
			code = rebuildUsageStatsResult(w1InvalidPGSSLModeURL, 1, 1, true)
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
		{name: "missing dsn", call: func() int { return rebuildUsageStatsResult("", 1, 1, true) }, want: "统计缓存重建需要 --dsn 指向显式的 PostgreSQL URL"},
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
}

func TestW1RemainingLocalHelperErrorEdges(t *testing.T) {
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
	cases := []struct {
		name     string
		dsn      string
		dryRun   bool
		wantCode int
		wantErr  string
	}{
		{name: "missing dsn", wantCode: 2, wantErr: "需要 --dsn 指向显式的 PostgreSQL URL"},
		{name: "invalid scheme", dsn: "sqlite:///w1-invalid", wantCode: 2, wantErr: "必须是 postgres:// 或 postgresql://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			stdout, stderr := bdCaptureOutput(t, func() {
				code = applySuccessCostBackfillResult(tc.dsn, tc.dryRun)
			})
			if code != tc.wantCode || stdout != "" || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("success-cost input/open error = %d/%q/%q, want code %d and %q", code, stdout, stderr, tc.wantCode, tc.wantErr)
			}
		})
	}
}
