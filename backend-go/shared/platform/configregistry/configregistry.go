// Package configregistry 是三项目环境变量的声明式登记处（升级兼容与迁移
// 账本设计 §12.1）：启动期未知变量告警、别名回落（变量改名过渡期）、
// `maintenance check-config` 升级前自检与 env 契约表生成、漂移测试双向比对
// 的唯一事实源。
//
// 登记基线 entries_gen.go 由全仓收割生成；新增 env 字面量后重新收割（漂移
// 测试会红着提醒）。Default/Kind/Range 等强元数据按变量逐步补齐——P3 只对
// 声明了 Kind/Range 的条目做值校验，未声明的条目仅登记名称与归属进程。
package configregistry

import (
	"fmt"
	"sort"
	"strings"
)

// Process 是消费该变量的进程标识。
type Process = string

const (
	ProcessGateway     Process = "gateway"
	ProcessJobs        Process = "jobs"
	ProcessMaintenance Process = "maintenance"
)

// Entry 描述一个环境变量（§12.1）。零值即「仅登记名称」：Required=false、
// 无别名、无值校验——对应存量变量的初始登记形态，强元数据逐步补齐。
type Entry struct {
	Name       string
	Aliases    []string // 旧名，主名未设置时按序回落
	Process    []Process
	Required   bool
	Kind       string // string|int|bool|duration|url|path|enum；空 = 不校验
	Default    string
	Range      string
	Deprecated string // 弃用起始版本
	RemovedIn  string // 计划移除版本
	Sensitive  bool
	Shared     bool // shared/platform 层读取，三进程通用
}

var (
	entries    = map[string]*Entry{}
	aliasIndex = map[string]string{} // alias -> primary
)

func register(entry Entry) {
	if entry.Name == "" {
		panic("configregistry: entry name 为空")
	}
	if _, dup := entries[entry.Name]; dup {
		panic(fmt.Sprintf("configregistry: 重复登记 %s", entry.Name))
	}
	stored := entry
	entries[stored.Name] = &stored
	for _, alias := range stored.Aliases {
		aliasIndex[alias] = stored.Name
	}
}

// Lookup 按主名取条目；别名命中返回主名条目。
func Lookup(name string) (*Entry, bool) {
	if entry, ok := entries[name]; ok {
		return entry, true
	}
	if primary, ok := aliasIndex[name]; ok {
		return entries[primary], true
	}
	return nil, false
}

// Names 返回全部已登记主名（排序稳定）。
func Names() []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Known 判定变量名（主名或别名）是否已登记。
func Known(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// UnknownFromEnviron 从环境清单（os.Environ() 形态）筛出未登记的 JUHE_AI_*
// 变量名（启动诊断 §12.2：单条 WARN 列出，不阻断启动）。
func UnknownFromEnviron(environ []string) []string {
	unknown := []string{}
	for _, item := range environ {
		if !strings.HasPrefix(item, "JUHE_AI_") {
			continue
		}
		name := item
		if idx := strings.IndexByte(item, '='); idx >= 0 {
			name = item[:idx]
		}
		if !Known(name) {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// MarkdownList 生成 env 契约表（check-config --list --format markdown 的实现，
// docs 环境契约表的生成来源，§12.4）。
func MarkdownList() string {
	var builder strings.Builder
	builder.WriteString("| 变量名 | 归属进程 | 必填 | 别名 | 备注 |\n")
	builder.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, name := range Names() {
		entry := entries[name]
		processes := strings.Join(entry.Process, " / ")
		required := ""
		if entry.Required {
			required = "是"
		}
		aliases := strings.Join(entry.Aliases, " / ")
		notes := []string{}
		if entry.Shared {
			notes = append(notes, "shared 层读取")
		}
		if entry.Deprecated != "" {
			notes = append(notes, "自 "+entry.Deprecated+" 弃用")
		}
		if entry.RemovedIn != "" {
			notes = append(notes, "计划 "+entry.RemovedIn+" 移除")
		}
		builder.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s |\n",
			name, processes, required, aliases, strings.Join(notes, "；")))
	}
	return builder.String()
}
