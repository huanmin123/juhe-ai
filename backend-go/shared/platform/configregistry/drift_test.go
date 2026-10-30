package configregistry

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// 漂移测试（升级兼容与迁移账本设计 §12.4）：重新收割全仓非测试 Go 源中的
// JUHE_AI_* 带引号完整字面量，与注册表双向比对——
// 代码有而注册表缺 → 失败；注册表有而代码无 → 失败（条目已删但登记残留）。
// 动态拼接的 env 名不在收割范围（已知边界，登记后可手工补 Entry 并在收割
// 排除清单登记）。
func TestHarvestedNamesMatchRegistry(t *testing.T) {
	root := repoRoot(t)
	harvested := map[string][]string{} // name -> projects
	for _, project := range []string{"gateway", "jobs", "maintenance"} {
		base := filepath.Join(root, "backend-go", "projects", project)
		filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			harvestFile(t, path, project, harvested)
			return nil
		})
	}
	filepath.WalkDir(filepath.Join(root, "backend-go", "shared"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		harvestFile(t, path, "", harvested) // shared 归属全部三进程
		return nil
	})

	missing := []string{}
	for _, name := range sortedKeys(harvested) {
		if !Known(name) {
			missing = append(missing, name)
		}
	}
	stale := []string{}
	for _, name := range Names() {
		if _, present := harvested[name]; !present {
			stale = append(stale, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("代码中存在但注册表缺失的 env 字面量 %d 个: %v——请在 entries_gen.go 补登记（重新收割）", len(missing), missing)
	}
	if len(stale) > 0 {
		t.Fatalf("注册表存在但代码已无字面量的残留条目 %d 个: %v——请重新收割 entries_gen.go", len(stale), stale)
	}
}

func harvestFile(t *testing.T, path, project string, harvested map[string][]string) {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	for _, match := range envNamePattern.FindAllStringSubmatch(string(text), -1) {
		name := match[1]
		if project == "" {
			// shared 层：三进程通用，已由 entries_gen 归属；只参与集合比对。
			harvested[name] = append(harvested[name], "shared")
		} else {
			harvested[name] = append(harvested[name], project)
		}
	}
}

var envNamePattern = regexp.MustCompile(`"(JUHE_AI_[A-Z0-9_]+)"`)

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// repoRoot 从包目录向上找 .git（工作目录 = 本包目录）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("未找到仓库根（.git）")
	return ""
}
