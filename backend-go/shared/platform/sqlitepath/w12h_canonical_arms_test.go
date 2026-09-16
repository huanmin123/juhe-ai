package sqlitepath

// w12h 补充 arms：CanonicalPath 的三类错误收口（非法字符、无物理父目录、
// 断链子路径）。不可达语句登记（windows/amd64 实测无法触达）：
//   - physical.go ListUsageShardFiles 回调内 filepath.Rel 错误分支：WalkDir
//     产出的 path 恒由 root 连接而成，Rel(root, path) 无失败路径；
//   - physical.go SameFile 非 windows 分支：runtime.GOOS 常量分流，windows
//     构建下恒走 EqualFold 分支；
//   - physical.go CanonicalPath 循环内父目录 Lstat 的非 NotExist 错误分支：
//     本机不存在盘符返回 PATH_NOT_FOUND（归类 NotExist），带非法字符的路径
//     在初始 Lstat 即被拒，循环中无法再造出非 NotExist 的父目录错误。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW12HCanonicalPathRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	// Windows 文件名禁止 `<`，Lstat 报 ERROR_INVALID_NAME（非 ErrNotExist），
	// 走初始 Lstat 的非 NotExist 错误收口。
	path := filepath.Join(dir, "w12h-bad<name")
	_, err := CanonicalPath(path)
	if err == nil {
		t.Fatal("非法文件名必须报错")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("非法文件名不应归类为 NotExist: %v", err)
	}
	if _, err := CanonicalPath(""); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("空路径必须报 path is required: %v", err)
	}
}

func TestW12HCanonicalPathNonExistentDrive(t *testing.T) {
	// 选中一个不存在的盘符，让父目录链一路 NotExist 走到盘符根。
	letter := freeDriveLetter(t)
	if letter == 0 {
		t.Skip("未找到不存在的盘符（跳过）")
	}
	root := string(letter) + `:\`
	if _, err := os.Lstat(root); err == nil {
		t.Skipf("盘符 %s 实际存在（跳过）", root)
	}
	path := root + `w12h-missing\nested`
	resolved, err := CanonicalPath(path)
	if err == nil {
		t.Fatalf("不存在盘符的路径必须报错, got %q", resolved)
	}
	t.Logf("drive arm error: %v", err)
}

func TestW12HCanonicalPathBrokenSymlinkChild(t *testing.T) {
	dir := t.TempDir()
	requireSymlink(t, filepath.Join(dir, "w12h-nowhere"), filepath.Join(dir, "w12h-link"))
	// 穿过断裂符号链接的子路径：初始 Lstat 为 NotExist，循环里父目录是
	// 断链，EvalSymlinks 失败进入循环错误收口。
	resolved, err := CanonicalPath(filepath.Join(dir, "w12h-link", "w12h-child"))
	if err == nil {
		t.Fatalf("断链子路径必须报错, got %q", resolved)
	}
	t.Logf("symlink arm error: %v", err)
}

// freeDriveLetter 返回一个当前不存在的盘符（找不到返回 0）。
func freeDriveLetter(t *testing.T) rune {
	t.Helper()
	for letter := 'Z'; letter >= 'D'; letter-- {
		if _, err := os.Lstat(string(letter) + `:\`); err != nil {
			return letter
		}
	}
	return 0
}
