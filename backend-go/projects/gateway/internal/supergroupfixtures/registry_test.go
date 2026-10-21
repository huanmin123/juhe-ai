// 超级分组设计 §7 验收集合守卫：从设计文档读取 44 个 fixture_id，断言集合
// 唯一，并确认 gateway 测试代码中存在同名 fixture 引用（子测试名/注册表/
// 注释）。逐行行为断言由各 fixture 自身承担。
package supergroupfixtures

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var fixtureRow = regexp.MustCompile("^[|] .([a-z0-9-]+). [|]")

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(dir, "..", "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "docs", "functions", "超级分组设计.md")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func designFixtureIDs(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "docs", "functions", "超级分组设计.md"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		if match := fixtureRow.FindStringSubmatch(strings.TrimSpace(line)); match != nil && match[1] != "-" {
			ids = append(ids, match[1])
		}
	}
	return ids
}

func TestSuperGroupFixtureCatalog(t *testing.T) {
	root := repoRoot(t)
	ids := designFixtureIDs(t, root)
	if len(ids) != 44 {
		t.Fatalf("design fixture count = %d, want 44", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate fixture id %q", id)
		}
		seen[id] = true
	}
	gatewayRoot := filepath.Join(root, "backend-go", "projects", "gateway")
	var corpus strings.Builder
	err := filepath.WalkDir(gatewayRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		corpus.Write(data)
		corpus.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	content := corpus.String()
	missing := []string{}
	for _, id := range ids {
		if !strings.Contains(content, `"`+id+`"`) && !strings.Contains(content, "fixture:"+id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("fixtures without gateway test reference: %v", missing)
	}
}
