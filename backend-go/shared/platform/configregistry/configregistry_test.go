package configregistry

import (
	"strings"
	"testing"
)

func TestRegisterLookupAlias(t *testing.T) {
	reset := captureRegistry(t)
	defer reset()
	register(Entry{Name: "JUHE_AI_EXAMPLE_NEW", Aliases: []string{"JUHE_AI_EXAMPLE_OLD"}, Process: []string{ProcessGateway}, Kind: "int"})
	if !Known("JUHE_AI_EXAMPLE_NEW") || !Known("JUHE_AI_EXAMPLE_OLD") {
		t.Fatal("主名与别名都应可命中")
	}
	entry, ok := Lookup("JUHE_AI_EXAMPLE_OLD")
	if !ok || entry.Name != "JUHE_AI_EXAMPLE_NEW" {
		t.Fatalf("别名应解析到主名条目: %+v", entry)
	}
	if Known("JUHE_AI_NOT_REGISTERED_AT_ALL") {
		t.Fatal("未登记名不应命中（该名仅用于本测试）")
	}
}

func TestUnknownFromEnviron(t *testing.T) {
	reset := captureRegistry(t)
	defer reset()
	register(Entry{Name: "JUHE_AI_KNOWN_ONE", Process: []string{ProcessJobs}})
	environ := []string{
		"JUHE_AI_KNOWN_ONE=1",
		"JUHE_AI_STRAY_LEFTOVER=x",
		"PATH=/bin",
		"JUHE_AI_STRAY_OTHER=y",
	}
	unknown := UnknownFromEnviron(environ)
	if len(unknown) != 2 || unknown[0] != "JUHE_AI_STRAY_LEFTOVER" || unknown[1] != "JUHE_AI_STRAY_OTHER" {
		t.Fatalf("未知变量筛选取值不符: %v", unknown)
	}
}

func TestMarkdownListContainsEntries(t *testing.T) {
	reset := captureRegistry(t)
	defer reset()
	register(Entry{Name: "JUHE_AI_MD_EXAMPLE", Process: []string{ProcessGateway, ProcessJobs}, Required: true, Deprecated: "v1.0", RemovedIn: "v2.0"})
	markdown := MarkdownList()
	for _, fragment := range []string{"JUHE_AI_MD_EXAMPLE", "gateway / jobs", "是", "v1.0 弃用", "计划 v2.0 移除"} {
		if !strings.Contains(markdown, fragment) {
			t.Fatalf("markdown 缺片段 %q", fragment)
		}
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	reset := captureRegistry(t)
	defer reset()
	defer func() {
		if recover() == nil {
			t.Fatal("重复登记必须 panic（实现缺陷才会触发）")
		}
	}()
	register(Entry{Name: "JUHE_AI_DUP_TEST_A"})
	register(Entry{Name: "JUHE_AI_DUP_TEST_A"})
}

// captureRegistry 保存并恢复全局登记表（单测互不污染；registry 仅在 init
// 与测试内写，进程内无并发写面）。
func captureRegistry(t *testing.T) func() {
	t.Helper()
	savedEntries := make(map[string]*Entry, len(entries))
	for name, entry := range entries {
		savedEntries[name] = entry
	}
	savedAliases := make(map[string]string, len(aliasIndex))
	for alias, primary := range aliasIndex {
		savedAliases[alias] = primary
	}
	return func() {
		entries = savedEntries
		aliasIndex = savedAliases
	}
}

