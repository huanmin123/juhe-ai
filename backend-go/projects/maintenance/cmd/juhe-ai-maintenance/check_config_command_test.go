package main

// check-config 子命令测试（升级兼容与迁移账本设计 §12.3）：env-file 解析、
// 别名/必填/未知变量结论与 --list 输出形态。进程内直调（测试单进程纪律）。
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckConfigEnvFileBrokenLineFails(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "broken.env")
	if err := os.WriteFile(envFile, []byte("BROKEN_LINE_WITHOUT_EQUALS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := captureCheckConfig([]string{
		"check-config", "--process", "gateway", "--env-file", envFile,
	})
	if code != ExitCheckFailed {
		t.Fatalf("坏行应 exit %d, got %d", ExitCheckFailed, code)
	}
	if !strings.Contains(stderr, "不是 KEY=VALUE 形态") {
		t.Fatalf("坏行应报错: stderr=%q", stderr)
	}
}

func TestCheckConfigEnvFileUnknownVariableFails(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "stray.env")
	if err := os.WriteFile(envFile, []byte("JUHE_AI_NOT_IN_REGISTRY=stray\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := captureCheckConfig([]string{
		"check-config", "--process", "gateway", "--env-file", envFile,
	})
	if code != ExitCheckFailed {
		t.Fatalf("含未登记变量应 exit %d, got %d", ExitCheckFailed, code)
	}
	if !strings.Contains(stdout, "JUHE_AI_NOT_IN_REGISTRY") {
		t.Fatalf("未登记变量应被列出: %q", stdout)
	}
}

func TestCheckConfigCleanEnvFilePasses(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "clean.env")
	// 主库连接串是登记变量；全干净内容应 exit 0。
	if err := os.WriteFile(envFile, []byte("JUHE_AI_POSTGRES_URL=postgres://u@h/db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := captureCheckConfig([]string{
		"check-config", "--process", "gateway", "--env-file", envFile,
	})
	if code != 0 {
		t.Fatalf("干净 env 应 exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "全部通过") {
		t.Fatalf("干净 env 应输出「全部通过」: %q", stdout)
	}
	if !strings.Contains(stdout, "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("输出应包含已登记变量: %q", stdout)
	}
}

func TestCheckConfigListOutputsMarkdown(t *testing.T) {
	stdout, _, code := captureCheckConfig([]string{"check-config", "--list"})
	if code != 0 {
		t.Fatalf("--list 应 exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "| 变量名 |") || !strings.Contains(stdout, "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("--list 应输出 markdown 契约表: %q", stdout[:min(300, len(stdout))])
	}
}

func TestCheckConfigRejectsBadProcess(t *testing.T) {
	_, stderr, code := captureCheckConfig([]string{"check-config", "--process", "nonsense"})
	if code != ExitCheckFailed {
		t.Fatalf("非法 process 应 exit %d, got %d", ExitCheckFailed, code)
	}
	if !strings.Contains(stderr, "gateway|jobs|maintenance|all") {
		t.Fatalf("应给出合法取值: %q", stderr)
	}
}

// captureCheckOutput 进程内直调 runCheckConfigCommand 并捕获 stdout/stderr。
func captureCheckConfig(argv []string) (stdout, stderr string, code int) {
	savedOut, savedErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = savedOut, savedErr
	}()
	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() { buf := make([]byte, 0, 64<<10); tmp := make([]byte, 4096); for { n, err := outR.Read(tmp); buf = append(buf, tmp[:n]...); if err != nil { doneOut <- string(buf); return } } }()
	go func() { buf := make([]byte, 0, 64<<10); tmp := make([]byte, 4096); for { n, err := errR.Read(tmp); buf = append(buf, tmp[:n]...); if err != nil { doneErr <- string(buf); return } } }()
	code = runCheckConfigCommand(stripCommand(argv))
	outW.Close()
	errW.Close()
	return <-doneOut, <-doneErr, code
}

func stripCommand(argv []string) []string {
	if len(argv) > 0 && argv[0] == "check-config" {
		return argv[1:]
	}
	return argv
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
