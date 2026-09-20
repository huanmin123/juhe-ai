package auditlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// w16l：legacy 迁移路径 helper 与 appendHotSearchFile 的定向补测
//（全模块扫描发现 94.8% < 95% 硬门槛，补齐边际）。
func TestW16LEqualLegacyPath(t *testing.T) {
	same := `internal/auditlog/./legacy.db`
	if runtime.GOOS == "windows" {
		same = `internal\auditlog\.\legacy.db`
	}
	if !equalLegacyPath(same, filepath.Join("internal", "auditlog", "legacy.db")) {
		t.Fatal("Clean 归一后应相等")
	}
	if equalLegacyPath("a.db", "b.db") {
		t.Fatal("不同路径不应相等")
	}
}

func TestW16LDSNHelpers(t *testing.T) {
	ro := readOnlySQLiteDSN("legacy.db")
	if !strings.Contains(ro, "mode=ro") {
		t.Fatalf("只读 DSN 应含 mode=ro: %q", ro)
	}
	target := targetSQLiteDSN("target.db")
	if !strings.Contains(target, "busy_timeout(5000)") {
		t.Fatalf("目标 DSN 应含 busy_timeout: %q", target)
	}
}

func TestW16LAppendHotSearchFileRoundTripAndErrorArm(t *testing.T) {
	t.Run("写读回", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "2026-09-20.jsonl")
		lines := []hotSearchLine{{AuditLogID: "a1", Text: "gpt-5"}}
		if err := appendHotSearchFile(path, lines); err != nil {
			t.Fatalf("append: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "gpt-5") {
			t.Fatalf("写读回失败: %v %s", err, data)
		}
	})
	t.Run("目录路径触发打开失败臂", func(t *testing.T) {
		dir := t.TempDir()
		if err := appendHotSearchFile(dir, []hotSearchLine{{Text: "x"}}); err == nil {
			t.Fatal("对目录路径 append 必须报错")
		}
	})
}
