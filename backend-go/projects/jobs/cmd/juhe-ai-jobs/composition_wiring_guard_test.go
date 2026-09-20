// 组合根接线守卫（composition wiring guard）。
//
// 守卫语义：这是「注释宣称组合根已接线、但组合根源码中从未出现该调用」
// 这一类缺陷的 CI 闸门。2026-09-20 专项审计发现 jobs 组合根存在 retention
// 快照上送退化为 stub、OAuth keepalive 任务零生产构造且调度注册表无条目、
// usage 定价目录未注入等「生产静默空转」缺口，本测试把「组合根源码必须
// 出现对应接线标识符」固化为可执行断言：遍历本目录（cmd/juhe-ai-jobs）
// 下所有非 _test.go 的 .go 文件拼成源码池，下方清单中每个标识符都必须在
// 源码池中出现，缺失即测试失败。
//
// keepalive 的调度注册条目（oauth-keepalive-token-refresh）除 cmd 源码池外
// 还必须出现在 internal/jobregistry（相对本包目录为
// ../../internal/jobregistry，即 jobs 项目的 cmd 兄弟目录）：注册表条目与
// worker 装配两者缺一，任务都不会被调度，/health 也不可见。
//
// 有意移除或更名某个端口时，必须同步增删 jobsCompositionWiringGuardEntries
// 清单，否则测试失败；失败信息中的「缺失影响」即该端口的用户可见语义，
// 移除前请先确认业务上确实不再需要。
//
// 边界：本测试是纯源码文本断言，不编译、不运行被测代码；它只堵「端口
// 零生产调用」这一类缺口。装配的具体行为语义由 wg_* / w*_cmd_* 等行为级
// 测试承担，二者互补。

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jobsCompositionWiringGuardEntry 是一条 jobs 组合根接线守卫清单项。
type jobsCompositionWiringGuardEntry struct {
	// Identifier 是必须出现在对应源码池中的字面标识符片段。
	Identifier string
	// Name 是守卫条目名。
	Name string
	// Purpose 是该接线端口的作用。
	Purpose string
	// MissingImpact 是缺失时的用户可见影响，失败信息直接引用。
	MissingImpact string
	// RegistryScope 为 true 时，标识符除 cmd 源码池外还必须出现在
	// ../../internal/jobregistry 的非测试源码池中。
	RegistryScope bool
}

// jobsStubSnapshotErrorText 是 retention 快照上送退化为 stub 时的错误文案
// 关键片段；它不得再出现在生产源码池中（与「真实实现接线」断言互补，
// 防止换一种方式退回 stub）。
const jobsStubSnapshotErrorText = "未接线（归 J2/J3 探针域"

// jobsCompositionWiringGuardEntries 是 jobs 组合根（cmd/juhe-ai-jobs）必须
// 存在的接线清单。新增受审计约束的组合根端口时，请在这里同步加一行。
var jobsCompositionWiringGuardEntries = []jobsCompositionWiringGuardEntry{
	{
		Identifier:    "UpsertAccountUsageSnapshots(",
		Name:          "retention 快照上送真实实现",
		Purpose:       "familyStatsWriter.UpsertAccountUsageSnapshots 必须接 cleanuprepo.RecordCleanupStore 真实实现（worker_retention.go），而非 stub",
		MissingImpact: "retention 账户用量快照上送退化为空实现，用量快照不再落库，管理面的用量/余额投影缺数据且无任何报错",
	},
	{
		Identifier:    "NewKeepaliveJob(",
		Name:          "OAuth keepalive 生产构造",
		Purpose:       "worker 装配必须构造 oauthrefresh.NewKeepaliveJob 并调度（worker_assembly.go）",
		MissingImpact: "OAuth 账户的 keepalive 刷新任务无生产构造，长期运行的 OAuth 账户 access token 过期后批量失效，用户请求开始 401/403",
	},
	{
		Identifier:    "oauth-keepalive-token-refresh",
		Name:          "keepalive 调度注册条目",
		Purpose:       "调度注册条目必须同时存在于 worker 装配（scheduleWiredJob）与 internal/jobregistry 注册表，二者缺一则任务不被调度、/health 不可见",
		MissingImpact: "keepalive 任务看似已实现但从未被调度执行，效果等同上一条缺口且监控面无法发现",
		RegistryScope: true,
	},
	{
		Identifier:    "WithCatalog(",
		Name:          "usage 定价目录注入",
		Purpose:       "usage 写入器必须注入定价目录（usagewriter.WithCatalog，worker_assembly.go / worker_usage_pricing_catalog.go）",
		MissingImpact: "用量记录缺少定价目录，计费统计退化为无价格或零价格数据，账单与成本报表失真",
	},
}

// jobsReadGoSourcePool 拼接目录 dir 下所有非 _test.go 的 .go 文件为源码池。
func jobsReadGoSourcePool(t *testing.T, dir string) (string, int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", dir, err)
	}
	var pool strings.Builder
	fileCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取源码文件 %s 失败: %v", filepath.Join(dir, name), err)
		}
		pool.Write(source)
		fileCount++
	}
	return pool.String(), fileCount
}

// TestCompositionWiringGuard 断言 jobs 组合根源码池（及 jobregistry 源码池）
// 包含全部接线清单项。测试名是外部验证入口，请勿改名。
func TestCompositionWiringGuard(t *testing.T) {
	// jobregistry 相对本包目录是 ../../internal/jobregistry（jobs 项目 cmd 的
	// 兄弟目录），目录缺失属于装配结构被破坏，必须 Fatal 而不是跳过。
	registryPool, registryFiles := jobsReadGoSourcePool(t, filepath.Join("..", "..", "internal", "jobregistry"))
	if registryFiles == 0 {
		t.Fatalf("../../internal/jobregistry 源码池为空：调度注册表结构异常，无法验证 keepalive 注册条目")
	}
	cmdPool, cmdFiles := jobsReadGoSourcePool(t, ".")
	// 防御性自检：cmd 源码池必须像 jobs 组合根（package main 且非空），否则
	// 下面循环会在错误路径上静默全绿。
	if cmdFiles == 0 || !strings.Contains(cmdPool, "package main") {
		t.Fatalf("组合根源码池异常（文件数=%d，缺失 package main）：请确认测试运行目录是 cmd/juhe-ai-jobs", cmdFiles)
	}
	for _, guard := range jobsCompositionWiringGuardEntries {
		if !strings.Contains(cmdPool, guard.Identifier) {
			t.Errorf("组合根接线守卫失败 [%s]：标识符 %q 未出现在 cmd/juhe-ai-jobs 的任何非测试 .go 源码中。端口作用：%s。缺失时的用户可见影响：%s。若属有意移除该端口，必须同步更新本测试的 jobsCompositionWiringGuardEntries 清单；否则请在组合根补上对应接线。",
				guard.Name, guard.Identifier, guard.Purpose, guard.MissingImpact)
			continue
		}
		if guard.RegistryScope && !strings.Contains(registryPool, guard.Identifier) {
			t.Errorf("组合根接线守卫失败 [%s]：标识符 %q 已在 cmd 组合根接线，但未出现在 internal/jobregistry 的任何非测试 .go 源码中，任务不会被调度注册。端口作用：%s。缺失时的用户可见影响：%s。",
				guard.Name, guard.Identifier, guard.Purpose, guard.MissingImpact)
		}
	}
	// stub 回退守卫：retention 快照上送不允许以任何形式退回 stub 文案。
	if strings.Contains(cmdPool, jobsStubSnapshotErrorText) {
		t.Errorf("组合根接线守卫失败 [retention 快照上送真实实现]：生产源码池中出现 stub 错误文案 %q，说明快照上送已退回未接线 stub，须恢复 UpsertAccountUsageSnapshots 真实实现接线。", jobsStubSnapshotErrorText)
	}
}
