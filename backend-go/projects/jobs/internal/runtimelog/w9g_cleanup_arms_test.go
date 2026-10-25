package runtimelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// w9g（runtimelog 批次）：Windows 文件身份、清理配额/保护臂、config
// shard-root 校验臂。

func TestW9GFileIdentityWindowsArms(t *testing.T) {
	if _, err := FileIdentity("bad\x00path", nil); err == nil {
		t.Fatal("非法路径必须报错")
	}
	if _, err := FileIdentity(filepath.Join(t.TempDir(), "missing.sqlite3"), nil); err == nil {
		t.Fatal("不存在文件必须报错")
	}
	existing := filepath.Join(t.TempDir(), "ok.sqlite3")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FileIdentity(existing, info)
	if err != nil || !strings.Contains(identity, ":") {
		t.Fatalf("identity = %q err = %v", identity, err)
	}
	_ = windows.CloseHandle // 保持依赖可见
}

func runtimeWindowsRL() bool { return os.PathSeparator == '\\' }
