package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 本文件覆盖 --export-business-dataset / --import-business-dataset 的 CLI 面：
// 互斥分支、runner 预检（exit 2）、manifest 输入预检、出口映射（0/1/3）与
// allow-missing 重复 flag 的解析规则。数据库链路的完整行为由
// internal/businessdataset 包内的内存 PG fake 全链路覆盖；cmd 层只验证接线、
// 预检与出口码（测试单进程纪律：不 exec 子进程、不依赖真实 PostgreSQL）。

// bdCaptureOutput 捕获 fn 期间写入 os.Stdout/os.Stderr 的内容（进程内替换，
// 本包测试不使用 t.Parallel()）。
func bdCaptureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedStdout, savedStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW
	stdoutBuf, stderrBuf := &strings.Builder{}, &strings.Builder{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(stdoutBuf, stdoutR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(stderrBuf, stderrR) }()
	fn()
	os.Stdout, os.Stderr = savedStdout, savedStderr
	_ = stdoutW.Close()
	_ = stderrW.Close()
	wg.Wait()
	_ = stdoutR.Close()
	_ = stderrR.Close()
	return stdoutBuf.String(), stderrBuf.String()
}

func TestBDBusinessDatasetMutexBranches(t *testing.T) {
	branches := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"export and import", []string{"-export-business-dataset", "-import-business-dataset"}, "business dataset export/import flags are mutually exclusive"},
		{"export with check", []string{"-export-business-dataset", "-check-boundary"}, "business dataset export/import flags are mutually exclusive"},
		{"export with version", []string{"-export-business-dataset", "-version"}, "business dataset export/import flags are mutually exclusive"},
		{"export with ensure-schema", []string{"-export-business-dataset", "-ensure-schema"}, "business dataset export/import flags are mutually exclusive"},
		{"import with snapshot", []string{"-import-business-dataset", "-postgres-schema-snapshot"}, "PostgreSQL schema snapshot flag is mutually exclusive"},
		{"import with metrics", []string{"-import-business-dataset", "-check-go-runtime-metrics"}, "business dataset export/import flags are mutually exclusive"},
		{"import with cutover evidence", []string{"-import-business-dataset", "-verify-j3b-cutover-evidence=x"}, "business dataset export/import flags are mutually exclusive"},
		{"import with inventory", []string{"-import-business-dataset", "-verify-j3b-model-check-inventory"}, "business dataset export/import flags are mutually exclusive"},
		{"import with j3a check", []string{"-import-business-dataset", "-check-j3a-proxy-latency-postgres"}, "business dataset export/import flags are mutually exclusive"},
		{"replace-existing with seed", []string{"-business-dataset-replace-existing", "-seed"}, "storage bootstrap flags are mutually exclusive"},
	}
	for _, item := range branches {
		item := item
		t.Run(item.name, func(t *testing.T) {
			code, _, stderr := wmRunMaintenanceCapture(t, item.args...)
			if code != 2 {
				t.Fatalf("exit=%d want 2\n%s", code, stderr)
			}
			if !strings.Contains(stderr, item.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", item.wantErr, stderr)
			}
		})
	}
}

func TestBDBusinessDatasetExportPreflight(t *testing.T) {
	t.Run("missing url", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetExportResult("  ", t.TempDir(), "READ_ONLY"); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --business-dataset-url") {
			t.Fatalf("stderr 缺少 URL 要求:\n%s", stderr)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetExportResult("postgres://127.0.0.1:5432/db", "  ", "READ_ONLY"); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --business-dataset-dir") {
			t.Fatalf("stderr 缺少目录要求:\n%s", stderr)
		}
	})
	t.Run("missing readonly confirm", func(t *testing.T) {
		for _, confirm := range []string{"", "read_only", "YES"} {
			_, stderr := bdCaptureOutput(t, func() {
				if code := businessDatasetExportResult("postgres://127.0.0.1:5432/db", t.TempDir(), confirm); code != 2 {
					t.Fatalf("confirm=%q exit=%d want 2", confirm, code)
				}
			})
			if !strings.Contains(stderr, "READ_ONLY") {
				t.Fatalf("stderr 缺少只读确认要求:\n%s", stderr)
			}
		}
	})
	t.Run("rejects sqlite url", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetExportResult("sqlite:///x.db", t.TempDir(), "READ_ONLY"); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "open business dataset export connection") {
			t.Fatalf("stderr 缺少打开连接错误:\n%s", stderr)
		}
	})
}

func TestBDBusinessDatasetImportPreflight(t *testing.T) {
	t.Run("missing url", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetImportResult("", t.TempDir(), nil); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --business-dataset-url") {
			t.Fatalf("stderr 缺少 URL 要求:\n%s", stderr)
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetImportResult("postgres://127.0.0.1:5432/db", "", nil); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "requires --business-dataset-dir") {
			t.Fatalf("stderr 缺少目录要求:\n%s", stderr)
		}
	})
	t.Run("missing manifest", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetImportResult("postgres://127.0.0.1:5432/db", filepath.Join(t.TempDir(), "missing"), nil); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "manifest input failed") {
			t.Fatalf("stderr 缺少 manifest 输入错误:\n%s", stderr)
		}
	})
	t.Run("malformed manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "business-dataset-manifest.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetImportResult("postgres://127.0.0.1:5432/db", dir, nil); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "manifest input failed") {
			t.Fatalf("stderr 缺少 manifest 输入错误:\n%s", stderr)
		}
	})
}

func TestBDBusinessDatasetOutcomeExitCode(t *testing.T) {
	t.Run("success exits zero and prints report", func(t *testing.T) {
		stdout, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetOutcomeExitCode("bd", map[string]any{"mode": "export"}, true, nil); code != 0 {
				t.Fatalf("exit=%d want 0", code)
			}
		})
		if stderr != "" {
			t.Fatalf("stderr 必须为空: %s", stderr)
		}
		if !strings.Contains(stdout, `"mode":"export"`) {
			t.Fatalf("stdout 必须包含 JSON 报告: %s", stdout)
		}
	})
	t.Run("unready exits three", func(t *testing.T) {
		stdout, _ := bdCaptureOutput(t, func() {
			if code := businessDatasetOutcomeExitCode("bd", map[string]any{"blockers": []string{"x"}}, false, nil); code != 3 {
				t.Fatalf("exit=%d want 3", code)
			}
		})
		if !strings.Contains(stdout, "blockers") {
			t.Fatalf("stdout 必须包含 blocker 明细: %s", stdout)
		}
	})
	t.Run("runtime error exits one", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetOutcomeExitCode("bd", nil, false, context.DeadlineExceeded); code != 1 {
				t.Fatalf("exit=%d want 1", code)
			}
		})
		if !strings.Contains(stderr, "bd failed") {
			t.Fatalf("stderr 必须包含失败原因:\n%s", stderr)
		}
	})
}

func TestBDBusinessDatasetAllowMissingFlag(t *testing.T) {
	var values []string
	flag := &businessDatasetAllowMissingFlag{values: &values}
	if flag.String() != "" {
		t.Fatal("空值 String 必须为空")
	}
	if err := flag.Set("accounts.provider_code"); err != nil {
		t.Fatalf("合法登记必须接受: %v", err)
	}
	if err := flag.Set("  system_accounts.extra "); err != nil {
		t.Fatalf("带空白登记必须去空白接受: %v", err)
	}
	for _, bad := range []string{"", "   ", "nodot"} {
		if err := flag.Set(bad); err == nil {
			t.Fatalf("非法登记 %q 必须拒绝", bad)
		}
	}
	if flag.String() != "accounts.provider_code,system_accounts.extra" {
		t.Fatalf("String 必须拼接登记值: %q", flag.String())
	}
	var nilFlag *businessDatasetAllowMissingFlag
	if nilFlag.String() != "" {
		t.Fatal("nil flag String 必须为空")
	}
}

func TestBDBusinessDatasetRunMaintenanceDispatch(t *testing.T) {
	// allow-missing 解析失败属于 flag 用法错误（exit 2，FlagSet ContinueOnError
	// 打印用法后返回）。
	t.Run("bad allow-missing value", func(t *testing.T) {
		code, _, _ := wmRunMaintenanceCapture(t, "-import-business-dataset", "-business-dataset-allow-missing-column", "nodot")
		if code != 2 {
			t.Fatalf("exit=%d want 2", code)
		}
	})
	// 分发到 export runner：预检通过后进入数据库链路，不可达地址暴露为
	// 运行时失败（exit 1）。
	t.Run("export dispatch runtime failure", func(t *testing.T) {
		dir := t.TempDir()
		code, _, stderr := wmRunMaintenanceCapture(t,
			"-export-business-dataset",
			"-business-dataset-url", "postgres://bd@127.0.0.1:1/none",
			"-business-dataset-dir", dir,
			"-business-dataset-confirm-readonly", "READ_ONLY")
		if code != 1 {
			t.Fatalf("exit=%d want 1\nstderr=%s", code, stderr)
		}
		if !strings.Contains(stderr, "business dataset export failed") {
			t.Fatalf("stderr 缺少导出失败原因:\n%s", stderr)
		}
	})
	// 分发到 import runner：自洽性失败的 manifest 在触碰数据库前就被阻断
	// （exit 3，blocker 明细走 stdout JSON）。
	t.Run("import dispatch manifest blockers", func(t *testing.T) {
		dir := t.TempDir()
		manifest := map[string]any{"formatVersion": "business-dataset-manifest/v1"}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "business-dataset-manifest.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := wmRunMaintenanceCapture(t,
			"-import-business-dataset",
			"-business-dataset-url", "postgres://bd@127.0.0.1:1/none",
			"-business-dataset-dir", dir)
		if code != 3 {
			t.Fatalf("exit=%d want 3\nstderr=%s", code, stderr)
		}
		if !strings.Contains(stdout, `"blockers"`) {
			t.Fatalf("stdout 必须输出 blocker 明细:\n%s", stdout)
		}
	})
}

func TestBDBusinessDatasetTailBranches(t *testing.T) {
	t.Run("import rejects sqlite url after manifest preflight", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "business-dataset-manifest.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetImportResult("sqlite:///x.db", dir, nil); code != 2 {
				t.Fatalf("exit=%d want 2", code)
			}
		})
		if !strings.Contains(stderr, "open business dataset import connection") {
			t.Fatalf("stderr 缺少打开连接错误:\n%s", stderr)
		}
	})
	t.Run("report encode failure exits one", func(t *testing.T) {
		_, stderr := bdCaptureOutput(t, func() {
			if code := businessDatasetOutcomeExitCode("bd", map[string]any{"bad": make(chan int)}, true, nil); code != 1 {
				t.Fatalf("exit=%d want 1", code)
			}
		})
		if !strings.Contains(stderr, "encode bd report") {
			t.Fatalf("stderr 缺少编码失败原因:\n%s", stderr)
		}
	})
}
