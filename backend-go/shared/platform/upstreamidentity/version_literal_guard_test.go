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

// TestVersionLiteralGuardPatternDiscrimination 锁定 guard 正则的判别力：
// 六种客户端标识的「标识/x.y.z」字面量必须命中（防正则失效回潮），
// 无版本段或纯 getter 引用不得命中（防误伤合法代码）。
func TestVersionLiteralGuardPatternDiscrimination(t *testing.T) {
	mustMatch := []string{
		`Codex Desktop/0.159.3 (Windows 10.0.22621; x86_64) unknown`,
		`codex_exec/0.161.0 (Windows 10.0.22621; x86_64) unknown`,
		`claude-cli/2.1.292 (external, cli)`,
		`GeminiCLI/0.63.0 (Windows; AMD64)`,
		`ZCode/3.14.3`,
		`xai-grok-workspace/1.0.13`,
	}
	for _, sample := range mustMatch {
		if !versionLiteralGuardPattern.MatchString(sample) {
			t.Errorf("guard 正则应命中版本字面量: %q", sample)
		}
	}
	noMatch := []string{
		`claude-cli (external, cli)`,
		`codex_exec getter 转发 upstreamidentity.EffectiveCodexUserAgent()`,
		`xai-grok-workspace/ + upstreamidentity.EffectiveGrokCLIVersion()`,
	}
	for _, sample := range noMatch {
		if versionLiteralGuardPattern.MatchString(sample) {
			t.Errorf("guard 正则不应命中无版本段的合法引用: %q", sample)
		}
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
