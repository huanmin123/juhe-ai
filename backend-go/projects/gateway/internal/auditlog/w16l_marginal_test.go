package auditlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w16l：legacy 迁移路径 helper 与 appendHotSearchFile 的定向补测
// （全模块扫描发现 94.8% < 95% 硬门槛，补齐边际）。

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
