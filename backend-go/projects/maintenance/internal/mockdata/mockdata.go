package mockdata

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// 造数域名。域名同时是 domainSeeds 的注册键、存储归属键与覆盖报告里
// DomainResult.Name 的取值，三处必须一致。
const (
	// DomainBusiness 拥有 business 库（系统账户、AI 账户、分组、路由策略、
	// API Key、授权、公告、外部来源、响应检查策略、账户测试与题库）。
	DomainBusiness = "business"
	// DomainUsage 拥有 usage-catalog 库与 usage-shards 分片文件。
	DomainUsage = "usage"
	// DomainStats 拥有 stats 库的明细写入面（派生聚合由本域调用既有聚合器
	// 重建，不手工伪造）。
	DomainStats = "stats"
	// DomainObservability 拥有运行日志、表监控、审计日志、操作日志与
	// dataset 库（公开接口日志、记录清理目标）。
	DomainObservability = "observability"
	// DomainChatCodexModelCheck 拥有 chat 库、codex context state 分片、
	// J3b 模型检测专库、后台任务运行库与账户健康库。
	DomainChatCodexModelCheck = "chat_codex_model_check"
)

// 参数边界（docs/functions/Mockdata造数设计.md「命令」小节的表格）：
// --days 1..90、--daily-requests 1..500。
const (
	// DefaultDays 是 --mockdata-days 的默认值（近 31 天）。
	DefaultDays = 31
	// DefaultDailyRequests 是 --mockdata-daily-requests 的默认值。
	DefaultDailyRequests = 120
	// MaxDays 是 --mockdata-days 的上限。
	MaxDays = 90
	// MaxDailyRequests 是 --mockdata-daily-requests 的上限。
	MaxDailyRequests = 500
)

// DomainResult 是一个域的执行结果：Name 是域名，Counts 是「逻辑对象 → 行数」
// 计数（例如 usageRecords=3720）。Counts 为空表示该域尚未接线，覆盖报告会据此
// 把该域的关键状态断言标成 not-covered。
type DomainResult struct {
	Name   string         `json:"name"`
	Counts map[string]int `json:"counts"`
}

// Options 是一次造数运行的输入。
type Options struct {
	// Paths 是解析后的存储位置（必须是 ResolvePaths 的结果）。
	Paths Paths
	// Days 是明细与监控样本的天数跨度，1..MaxDays。
	Days int
	// DailyRequests 是每天生成的使用记录条数，1..MaxDailyRequests。
	DailyRequests int
	// Secret 是账户凭据信封使用的运行密钥（JUHE_AI_SECRET）；空值交给
	// schema 的既有默认值处理。
	Secret string
	// Now 是本次运行的时间基准；零值取 time.Now()。固定它可让造数结果
	// 在测试与回放中可复现。
	Now time.Time
}

// validate 校验参数边界。越界即错而不是钳制：造数结果会被当作页面验收的
// 证据，静默钳制会让「--days 500」看起来成功但只生成 90 天。
func (o Options) validate() error {
	if o.Days < 1 || o.Days > MaxDays {
		return fmt.Errorf("mockdata days 必须在 1 到 %d 之间: %d", MaxDays, o.Days)
	}
	if o.DailyRequests < 1 || o.DailyRequests > MaxDailyRequests {
		return fmt.Errorf("mockdata dailyRequests 必须在 1 到 %d 之间: %d", MaxDailyRequests, o.DailyRequests)
	}
	if len(o.Paths.DataDir) == 0 {
		return fmt.Errorf("mockdata 需要已解析的存储路径（Paths.DataDir 为空）")
	}
	return nil
}

// clock 返回本次运行的时间基准。
func (o Options) clock() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// domainSeed 是域注册项：固定签名 (context.Context, *env) (DomainResult, error)。
//
// 为什么用函数变量表而不是 switch：后续域实现者只改自己域文件里的函数体
// （domain_*.go），不需要回到本文件加分支；覆盖报告与报告输出也按这张表
// 枚举域，新增域自动出现在两个输出里。
var domainSeeds = []struct {
	Name string
	Run  func(context.Context, *env) (DomainResult, error)
}{
	{Name: DomainBusiness, Run: seedBusiness},
	{Name: DomainUsage, Run: seedUsage},
	{Name: DomainStats, Run: seedStatsRaw},
	{Name: DomainObservability, Run: seedObservability},
	{Name: DomainChatCodexModelCheck, Run: seedChatCodexModelCheck},
}

// Report 是 --mockdata 的 stdout JSON，也是库调用方的返回值。
type Report struct {
	Driver        string         `json:"driver"`
	DataDir       string         `json:"dataDir"`
	Days          int            `json:"days"`
	DailyRequests int            `json:"dailyRequests"`
	StartedAt     string         `json:"startedAt"`
	FinishedAt    string         `json:"finishedAt"`
	DurationMs    int64          `json:"durationMs"`
	Cleanup       map[string]int `json:"cleanup"`
	// CleanupSkipped 记录因存储文件或表不存在而跳过的清理目标；这些不是
	// 失败（首次运行本来就什么都没有），但必须显式列出，避免「清理了 0 行」
	// 被误读成「上一批数据已清空」。
	CleanupSkipped []string       `json:"cleanupSkipped,omitempty"`
	Domains        []DomainResult `json:"domains"`
	// Counts 是各域计数的合并视图（同名键相加），给只关心总数的调用方。
	Counts   map[string]int `json:"counts"`
	Autofill map[string]int `json:"autofill"`
	// AutofillSkipped 记录自动补全跳过的表与原因；跳过不影响成功，但要让
	// 「某张表为什么是空的」在报告里可查。
	AutofillSkipped map[string]string `json:"autofillSkipped,omitempty"`
	SummaryPath     string            `json:"summaryPath"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// Run 执行一次完整造数：打开存储 → 清理上一批 → 逐域 seed → 自动补全 →
// 写摘要。任何一步失败即整体失败（返回已完成部分的报告与错误），不做部分
// 成功提交：半批数据比没有数据更难排查。
func Run(ctx context.Context, options Options) (Report, error) {
	if err := options.validate(); err != nil {
		return Report{}, err
	}
	return runWithLogger(ctx, options, nil)
}

// RunWithLogger 与 Run 相同，但接受注入的日志器（CLI 用它把进度写到 stderr，
// 保持 stdout 只有 JSON）。
func RunWithLogger(ctx context.Context, options Options, logger *slog.Logger) (Report, error) {
	if err := options.validate(); err != nil {
		return Report{}, err
	}
	return runWithLogger(ctx, options, logger)
}

func runWithLogger(ctx context.Context, options Options, logger *slog.Logger) (Report, error) {
	started := options.clock()
	// 耗时用真实墙钟测量：options.Now 是数据时间基准（可被测试固定），
	// 拿它算耗时会恒为 0。
	wallStart := time.Now()
	report := Report{
		Driver:        "sqlite",
		DataDir:       options.Paths.DataDir,
		Days:          options.Days,
		DailyRequests: options.DailyRequests,
		StartedAt:     started.UTC().Format(time.RFC3339),
	}
	e := newEnv(options, logger)
	defer func() {
		if closeErr := e.Close(); closeErr != nil {
			report.Warnings = append(report.Warnings, closeErr.Error())
		}
	}()

	cleanup, skipped, err := cleanupAll(ctx, e)
	report.Cleanup = cleanup
	report.CleanupSkipped = skipped
	if err != nil {
		return finishReport(report, options, wallStart), err
	}

	for _, seed := range domainSeeds {
		result, seedErr := seed.Run(ctx, e)
		if seedErr != nil {
			return finishReport(report, options, wallStart), fmt.Errorf("mockdata 域 %s 失败: %w", seed.Name, seedErr)
		}
		if result.Name == "" {
			result.Name = seed.Name
		}
		e.recordDomainResult(result)
	}

	autofill, autofillSkipped, err := autofillTables(ctx, e)
	report.Autofill = autofill
	report.AutofillSkipped = autofillSkipped
	if err != nil {
		return finishReport(report, options, wallStart), err
	}

	report.Domains = e.domainResults()
	report.Counts = mergedDomainCounts(report.Domains)
	summaryPath, err := writeSummary(e, report)
	report.SummaryPath = summaryPath
	if err != nil {
		return finishReport(report, options, wallStart), err
	}
	return finishReport(report, options, wallStart), nil
}

// finishReport 补齐时间字段，保证错误路径也返回结构完整的报告。
func finishReport(report Report, options Options, wallStart time.Time) Report {
	finished := options.clock()
	report.FinishedAt = finished.UTC().Format(time.RFC3339)
	report.DurationMs = time.Since(wallStart).Milliseconds()
	if report.Cleanup == nil {
		report.Cleanup = map[string]int{}
	}
	if report.Counts == nil {
		report.Counts = map[string]int{}
	}
	if report.Autofill == nil {
		report.Autofill = map[string]int{}
	}
	if report.Domains == nil {
		report.Domains = []DomainResult{}
	}
	return report
}

// mergedDomainCounts 把各域计数按 key 求和。
func mergedDomainCounts(results []DomainResult) map[string]int {
	merged := map[string]int{}
	for _, result := range results {
		for key, count := range result.Counts {
			merged[key] += count
		}
	}
	return merged
}

// sortedKeys 返回 map 的稳定顺序键（报告与日志输出用）。
func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
