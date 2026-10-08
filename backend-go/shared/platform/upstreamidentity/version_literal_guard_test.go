package upstreamidentity

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// versionLiteralGuardPattern 匹配客户端版本字面量「客户端标识/x.y.z」。
// 该事实只允许存在于 upstreamidentity 包的内置常量与 Effective* getter；
// 其余非测试代码一律消费 getter（docs/functions/客户端版本自动跟版设计.md §2）。
// `Codex Desktop/` 模式保留防回潮：旧 Desktop 画像已废弃（设计 §8.1），
// 非测试代码不得再出现。
var versionLiteralGuardPattern = regexp.MustCompile(`(Codex Desktop|codex_exec|claude-cli|GeminiCLI|ZCode|xai-grok-workspace)/\d+\.\d+\.\d+`)

// TestVersionLiteralGuard 防回归：backend-go 下全部非测试 .go 文件（排除本包
// 目录）不得再出现版本字面量。测试 fixture（_test.go）合法，故跳过。
func TestVersionLiteralGuard(t *testing.T) {
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取包工作目录失败: %v", err)
	}
	// backend-go/shared/platform/upstreamidentity -> 仓库根（上溯 4 级）。
	backendGoDir := filepath.Join(packageDir, "..", "..", "..", "..", "backend-go")

	var violations []string
	walkErr := filepath.WalkDir(backendGoDir, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "shared/platform/upstreamidentity") {
			return nil
		}
		violations = append(violations, scanVersionLiterals(t, path)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历 %s 失败: %v", backendGoDir, walkErr)
	}
	if len(violations) > 0 {
		t.Fatalf("客户端版本字面量必须收敛为 upstreamidentity getter（设计文档 §2），命中如下:\n%s", strings.Join(violations, "\n"))
	}
}

// scanVersionLiterals 返回文件内全部命中，格式为「文件:行号: 原文」。
func scanVersionLiterals(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("关闭 %s 失败: %v", path, closeErr)
		}
	}()

	var found []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if versionLiteralGuardPattern.MatchString(line) {
			found = append(found, fmt.Sprintf("%s:%d: %s", path, lineNumber, strings.TrimSpace(line)))
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatalf("扫描 %s 失败: %v", path, scanErr)
	}
	return found
}
