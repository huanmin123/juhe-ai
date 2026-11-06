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
	// MinCount 是源码池中必须出现的最小次数；0 等价于 1。用于同一端口在
	// 多个装配点复用时防止删掉其中一处仍被单次命中掩盖。
	MinCount int
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
	{
		Identifier:    "WithFailureStateStore(",
		Name:          "OAuth 刷新失败状态 Redis 装配",
		Purpose:       "redis-state 部署必须经 WithFailureStateStore 注入 Redis 版失败状态存储（worker_oauth_failurestate.go），否则保持内存版",
		MissingImpact: "OAuth 刷新退避状态只在进程内存：重启清零、多副本不共享，故障账号被重复刷新打上游",
	},
	{
		Identifier:    "WithKeepaliveFailureStateStore(",
		Name:          "OAuth keepalive 失败状态注入",
		Purpose:       "keepalive 三族必须与 refresh 同分支接入失败记账（WithKeepaliveFailureStateStore，worker_assembly.go / worker_oauth_failurestate.go，key 前缀族隔离），本地 3 次终态、300s 退避与成功恢复口径对齐 OpenAI 族",
		MissingImpact: "keepalive 失败退避只在进程内存：重启清零、多副本不共享，故障账号被重复保活打上游；本地配置终态与恢复通道缺失，三族口径与 OpenAI 族脱节",
	},
	{
		Identifier:    "WithCrossProcessLock(",
		Name:          "OAuth 刷新跨进程锁接入",
		Purpose:       "redis-state 部署必须为 OpenAI 刷新族注入共享 Redis 账户锁（WithCrossProcessLock，worker_assembly.go / worker_oauth_crossprocess.go），与 gateway 401 自动刷新互斥",
		MissingImpact: "gateway 与 jobs 可并发刷新同一账户的 rotation 型 refresh token，后到请求用已作废 token 打上游，账户被迫重新授权",
	},
	{
		Identifier:    "WithKeepaliveCrossProcessLock(",
		Name:          "OAuth keepalive 跨进程锁接入",
		Purpose:       "anthropic/gemini/grok keepalive 必须与 OpenAI 族共用同一 oauth 域账户锁（WithKeepaliveCrossProcessLock），与 gateway 自动刷新互斥",
		MissingImpact: "keepalive 与 gateway 401 自动刷新并发换发同一账户令牌，rotation 型 refresh token 单次作废导致账户异常",
	},
	{
		Identifier:    "SetCredentialVersionBumper(",
		Name:          "OAuth 凭据版本投影接入",
		Purpose:       "jobs 凭据写事务提交后必须 bump 共享版本（SetCredentialVersionBumper，worker_assembly.go / worker_oauth_crossprocess.go），gateway 运行态账号缓存据此提前重载",
		MissingImpact: "jobs 刷新的新 token 最长 60s 才被 gateway 看到，期间网关继续用旧 token 打上游触发 401 与无谓重试",
	},
	{
		Identifier:    "SetStateVersionBumpers(",
		Name:          "账户/API Key 运行态版本栅栏接入",
		Purpose:       "jobs 闸门同步事务提交后必须按 ChangedIDs bump 账户/Key 状态序号（SetStateVersionBumpers，worker_assembly.go / worker_oauth_crossprocess.go），gateway runtime 缓存下一次读重载",
		MissingImpact: "jobs 翻转时间计划闸门/Key 状态后，gateway 最长 60s 仍按旧快照派发，时段外账户继续被请求",
	},
	{
		Identifier:    "accountStateVersionBumperOrNil(stateClient)",
		Name:          "账户健康投影状态版本栅栏接入",
		Purpose:       "J1 outcome 投影提交后必须 bump 账户状态序号（OutcomeProjectorConfig.StateVersion = accountStateVersionBumperOrNil(stateClient)，worker_health_projection.go）",
		MissingImpact: "jobs 将账户标记临时不可用/冷却/恢复后，gateway runtime 缓存最长 60s 不感知，继续使用旧状态候选",
	},
	{
		Identifier:    "deletedAccounts.StateVersion = oauthRuntimeStateBumper{",
		Name:          "孤儿账户删除状态版本栅栏接入",
		Purpose:       "孤儿授权实例账户逻辑删除提交后必须 bump 账户状态序号（worker_retention.go），gateway runtime 缓存下一次读重载",
		MissingImpact: "已删除孤儿账户在 gateway 最长 60s 仍可能被派发",
	},
	{
		Identifier:    "Version: a.settingsVersionReader()",
		MinCount:      2,
		Name:          "jobs settings 跨进程版本读取",
		Purpose:       "statsagg 与 probe 的 jobssettings Source 必须读取 version:settings，gateway 写入后下一次读重载 60s 快照（B5）",
		MissingImpact: "gateway 改设置后 jobs 最长 60s 继续使用旧值（统计批量/冷却复测）",
	},
	{
		Identifier:    "newRetentionSettingsRuntime(business.DB, a.logger, a.settingsVersionReader())",
		Name:          "retention settings 跨进程版本读取",
		Purpose:       "retention 家族的 jobssettings Source 必须注入 version:settings 读取端口（worker_retention.go），与 statsagg/probe 同步重载",
		MissingImpact: "gateway 改 retention 保留策略后 jobs 最长 60s 继续使用旧值",
	},
	{
		Identifier:    "rediscfg.DomainSettings",
		Name:          "settings 版本键独立域",
		Purpose:       "jobs settings 版本客户端派生 `<RedisNamespace>:settings` 域（worker_settings_crossprocess.go），与 gateway 写入侧一致",
		MissingImpact: "jobs 读到的版本键与 gateway 不在同一域，B5 跨进程失效静默不生效",
	},
	{
		Identifier:    "a.bumpSettingsVersion(ctx)",
		Name:          "jobs settings 自动覆盖写后版本 bump",
		Purpose:       "upstream-client-version-refresh 成功写入自动覆盖后必须 bump settings 版本（worker_version_tracking.go）",
		MissingImpact: "jobs 自动版本写后其它读方继续使用旧 settings 快照",
	},
	{
		Identifier:    "StateVersion: a.accountStateVersionBumper()",
		Name:          "探针 Key 运行态状态版本栅栏接入",
		Purpose:       "jobs 冷却复测 RecordKeySuccess/Failure 提交后必须按来源+授权实例 bump 账户状态序号（worker_probe_jobs.go），gateway runtime 下一次读重载 Key 运行态",
		MissingImpact: "jobs 恢复/冷却 Key 后 gateway 最长 60s 仍按旧 Key 状态过滤",
	},
	{
		Identifier:    "newAccountScheduleActivationHook(",
		Name:          "排期激活 dispatch revision 推进 hook",
		Purpose:       "account-availability-schedule-status-sync 必须注入激活 hook（worker_oauth_activation.go），在激活翻转时推进 circuit dispatch revision 家族",
		MissingImpact: "定时窗口启用的账号只翻状态、不解除网关 dispatch revision 门控，账号启用后请求持续被拒",
	},
	{
		Identifier:    "newAPIKeyScheduleActivationHook(",
		Name:          "API Key 排期激活小时额度绑定 hook",
		Purpose:       "api-key-availability-schedule-status-sync 必须注入激活 hook（worker_oauth_activation.go），在激活翻转时重建 request_quota_hourly_window_scope_bindings 并提交后打脏",
		MissingImpact: "计划启用的 API Key 永久缺失小时额度窗口绑定，statsagg 不生成 usage_quota_hourly_windows，小时额度静默失效（B3-H11）",
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
		minCount := guard.MinCount
		if minCount < 1 {
			minCount = 1
		}
		if count := strings.Count(cmdPool, guard.Identifier); count < minCount {
			t.Errorf("组合根接线守卫失败 [%s]：标识符 %q 在 cmd/juhe-ai-jobs 的任何非测试 .go 源码中出现 %d 次，要求至少 %d 次。端口作用：%s。缺失时的用户可见影响：%s。若属有意移除该端口，必须同步更新本测试的 jobsCompositionWiringGuardEntries 清单；否则请在组合根补上对应接线。",
				guard.Name, guard.Identifier, count, minCount, guard.Purpose, guard.MissingImpact)
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
