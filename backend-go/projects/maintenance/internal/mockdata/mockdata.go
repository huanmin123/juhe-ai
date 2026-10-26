// Package mockdata 是本地开发造数能力（docs/functions/Mockdata造数设计.md）
// 的维护入口。
//
// 2026-10 存储形态（T66 PG 造数重写）：目标为 --dsn 指向的 PostgreSQL 单库多
// schema 存储（juhe_business / juhe_usage / juhe_stats / juhe_dataset /
// juhe_chat / juhe_codex_context + 属主 schema juhe_j3b / juhe_jobs）。主流程：
// EnsurePostgres 建骨架 → SeedPostgresDefaults 落最小种子 → 属主存储探测
// （juhe_j3b 受控 bootstrap，其余只探测）→ 幂等清理 → 逐域 seed → autofill →
// 摘要。五个域（business / usage / stats / observability /
// chat_codex_model_check）均在同一注册表接线；属主存储缺失时域内跳过并记
// OwnerStoresPending，不阻塞 Ready。
package mockdata

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// 造数域名。域名同时是 domainRegistry 的注册键、schema 归属键与覆盖报告里
// DomainResult.Name 的取值，三处必须一致。
const (
	// DomainBusiness 拥有 juhe_business（系统账户、AI 账户、分组、路由策略、
	// API Key、授权、公告、外部来源、响应检查策略、账户测试与题库）。
	DomainBusiness = "business"
	// DomainUsage 拥有 juhe_usage（usage_records 日分区写入面与逻辑 shard
	// 四表登记）。
	DomainUsage = "usage"
	// DomainStats 拥有 juhe_stats 明细写入面（含 background_task_runs；派生
	// 聚合由既有聚合器重建，不手工伪造）。
	DomainStats = "stats"
	// DomainObservability 拥有 juhe_dataset 维护面（公开接口日志、记录清理
	// 目标）与审计 / 操作 / 运行日志属主表（缺失时域内跳过）。
	DomainObservability = "observability"
	// DomainChatCodexModelCheck 拥有 juhe_chat 日分区面、juhe_codex_context、
	// J3b 模型检测专库与账户健康库（后两者属主缺失时域内跳过）。
	DomainChatCodexModelCheck = "chat_codex_model_check"
)

// 参数边界（docs/functions/Mockdata造数设计.md「命令」小节的表格）：
// --mockdata-days 1..90、--mockdata-daily-requests 1..500。
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
// 计数（例如 accounts=31）。Counts 为空表示该域尚未接线（或域内整体跳过），
// 覆盖报告会据此把该域的关键状态断言标成 not-covered。
type DomainResult struct {
	Name   string         `json:"name"`
	Counts map[string]int `json:"counts"`
}

// domainEntry 是域注册项。Wired=false 的域保持注册但不执行（报告与覆盖口径
// 仍列出它）；域实现交付时把 Run 指到实现函数并把 Wired 置 true。
type domainEntry struct {
	Name  string
	Wired bool
	Run   func(context.Context, *env) (DomainResult, error)
}

// domainRegistry 是域注册表：报告顺序、覆盖域词表与执行的唯一清单。
var domainRegistry = []domainEntry{
	{Name: DomainBusiness, Wired: true, Run: seedBusiness},
	{Name: DomainUsage, Wired: true, Run: seedUsage},
	{Name: DomainStats, Wired: true, Run: seedStats},
	{Name: DomainObservability, Wired: true, Run: seedObservability},
	{Name: DomainChatCodexModelCheck, Wired: true, Run: seedChatCodexModelCheck},
}

// Options 是一次造数运行的输入。
type Options struct {
	// DSN 是目标 PostgreSQL URL（开发库或测试库；一次误执行的造数命令不得
	// 落到未知库，必填）。
	DSN string
	// Days 是明细与监控样本的天数跨度，1..MaxDays。
	Days int
	// DailyRequests 是每天生成的使用记录条数，1..MaxDailyRequests。
	DailyRequests int
	// Secret 是账户凭据信封使用的运行密钥（JUHE_AI_SECRET）；空值交给
	// schema 的既有默认值处理。
	Secret string
	// SummaryFile 是 mockdata 摘要 JSON 的写出路径；空值不写摘要（无隐式
	// 回退）。--verify-mockdata-coverage 用同一路径读域接线快照。
	SummaryFile string
	// Now 是本次运行的时间基准；零值取 time.Now()。固定它可让造数结果
	// 在测试与回放中可复现。
	Now time.Time
}

// validate 校验参数边界。越界即错而不是钳制：造数结果会被当作页面验收的
// 证据，静默钳制会让「--mockdata-days 500」看起来成功但只生成 90 天。
func (o Options) validate() error {
	if o.Days < 1 || o.Days > MaxDays {
		return fmt.Errorf("mockdata days 必须在 1 到 %d 之间: %d", MaxDays, o.Days)
	}
	if o.DailyRequests < 1 || o.DailyRequests > MaxDailyRequests {
		return fmt.Errorf("mockdata dailyRequests 必须在 1 到 %d 之间: %d", MaxDailyRequests, o.DailyRequests)
	}
	if o.DSN == "" {
		return fmt.Errorf("mockdata 需要 --dsn 指向显式的 PostgreSQL URL")
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

// Report 是 --mockdata 的 stdout JSON，也是库调用方的返回值。
type Report struct {
	Driver        string `json:"driver"`
	DSN           string `json:"dsn"`
	Days          int    `json:"days"`
	DailyRequests int    `json:"dailyRequests"`
	StartedAt     string `json:"startedAt"`
	FinishedAt    string `json:"finishedAt"`
	DurationMs    int64  `json:"durationMs"`
	// Cleanup 是「schema.table → 删除行数」；CleanupSkipped 记录因表不存在
	// 而跳过的清理目标——不是失败（首次运行本来就什么都没有），但必须显式
	// 列出，避免「清理了 0 行」被误读成「上一批数据已清空」。
	Cleanup        map[string]int `json:"cleanup"`
	CleanupSkipped []string       `json:"cleanupSkipped,omitempty"`
	Domains        []DomainResult `json:"domains"`
	// Counts 是各域计数的合并视图（同名键相加），给只关心总数的调用方。
	Counts map[string]int `json:"counts"`
	// Autofill 是自动补全阶段的行数记账（schema.table → 插入行数，只补空
	// 表）；AutofillSkipped 记录跳过的表与原因（运行态/队列/游标与域写入面
	// 按清单跳过，外键父表缺失、结构不可满足、单表插入失败携带原始错误）。
	Autofill        map[string]int    `json:"autofill"`
	AutofillSkipped map[string]string `json:"autofillSkipped,omitempty"`
	// OwnerStoresPending 列出缺失的属主存储（gateway/jobs 运行时建表或 J3b
	// 受控 bootstrap 尚未发生）。属主缺失的域按域内跳过处理，不构成运行
	// 失败，也不阻塞覆盖 Ready（对应断言进 NotCovered）。
	OwnerStoresPending []OwnerStorePending `json:"ownerStoresPending,omitempty"`
	// SummaryPath 是摘要文件路径；Options.SummaryFile 为空时为空串。
	SummaryPath string   `json:"summaryPath"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Run 执行一次完整造数：打开目标库 → EnsurePostgres → SeedPostgresDefaults →
// 属主探测（juhe_j3b 受控 bootstrap）→ 清理上一批 → 逐域 seed → autofill →
// 摘要。任何一步失败即整体失败（返回已完成部分的报告与错误），不做部分成功
// 提交：半批数据比没有数据更难排查。
func Run(ctx context.Context, options Options) (Report, error) {
	return RunWithLogger(ctx, options, nil)
}

// RunWithLogger 与 Run 相同，但接受注入的日志器（CLI 用它把进度写到 stderr，
// 保持 stdout 只有 JSON）。
func RunWithLogger(ctx context.Context, options Options, logger *slog.Logger) (report Report, err error) {
	if err := options.validate(); err != nil {
		return Report{}, err
	}
	started := options.clock()
	// 耗时用真实墙钟测量：options.Now 是数据时间基准（可被测试固定），
	// 拿它算耗时会恒为 0。
	wallStart := time.Now()
	report = Report{
		Driver:        "postgres",
		DSN:           options.DSN,
		Days:          options.Days,
		DailyRequests: options.DailyRequests,
		StartedAt:     started.UTC().Format(time.RFC3339),
	}
	e, err := newEnv(options, logger)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if closeErr := e.Close(); closeErr != nil {
			report.Warnings = append(report.Warnings, closeErr.Error())
		}
	}()

	// 1. schema 骨架 + 最小种子（幂等）：造数行引用的 vendors / models /
	//    admin 都来自 seed，先建后种是域 guard 的前置。
	if _, err := schema.EnsurePostgres(ctx, e.db); err != nil {
		return finishReport(report, options, wallStart), fmt.Errorf("mockdata EnsurePostgres: %w", err)
	}
	if _, err := schema.SeedPostgresDefaults(ctx, e.db, schema.SeedOptions{
		Secret: options.Secret,
		Now:    options.nowFunc(),
	}); err != nil {
		return finishReport(report, options, wallStart), fmt.Errorf("mockdata SeedPostgresDefaults: %w", err)
	}

	// 2. 属主存储：juhe_j3b 走受控 bootstrap（schema 存在且属主匹配时建表；
	//    schema 缺失时按 OwnerStoresPending 记录，不在 maintenance 造
	//    schema）；其余属主只探测。探测在 bootstrap 之后统一执行，bootstrap
	//    成功的属主自然不进 pending。
	if err := ensureJ3BOwnerStore(ctx, e.db); err != nil {
		return finishReport(report, options, wallStart), err
	}
	pending, err := probeOwnerStores(ctx, e.db)
	if err != nil {
		return finishReport(report, options, wallStart), err
	}
	report.OwnerStoresPending = pending

	// 3. 清理上一批（幂等收敛），再逐域 seed。
	cleanup, skipped, err := cleanupAll(ctx, e, logger)
	report.Cleanup = cleanup
	report.CleanupSkipped = skipped
	if err != nil {
		return finishReport(report, options, wallStart), err
	}
	for _, domain := range domainRegistry {
		if !domain.Wired {
			e.logger.Info("mockdata 域未接线，跳过", "domain", domain.Name)
			e.recordDomainResult(DomainResult{Name: domain.Name})
			continue
		}
		result, seedErr := domain.Run(ctx, e)
		if seedErr != nil {
			return finishReport(report, options, wallStart), fmt.Errorf("mockdata 域 %s 失败: %w", domain.Name, seedErr)
		}
		if result.Name == "" {
			result.Name = domain.Name
		}
		e.recordDomainResult(result)
	}

	// 4. autofill：为「maintenance schema 内、域没有写入且当前为空」的表补
	//    1..3 行结构合法占位（运行态/队列/游标与域写入面按清单跳过）；跳过
	//    原因显式进报告，单表失败不阻塞整体（样例数据不是硬数据）。
	autofill, autofillSkipped, err := autofillTables(ctx, e)
	if err != nil {
		return finishReport(report, options, wallStart), err
	}
	report.Autofill = autofill
	report.AutofillSkipped = autofillSkipped

	report.Domains = e.domainResults()
	report.Counts = mergedDomainCounts(report.Domains)
	summaryPath, err := writeSummary(e, report, options.SummaryFile)
	report.SummaryPath = summaryPath
	if err != nil {
		return finishReport(report, options, wallStart), err
	}
	return finishReport(report, options, wallStart), nil
}

// nowFunc 把 Options.Now 转成 SeedOptions 需要的时钟函数。
func (o Options) nowFunc() func() time.Time {
	if o.Now.IsZero() {
		return nil
	}
	return func() time.Time { return o.Now }
}

// ensureJ3BOwnerStore 对 juhe_j3b 做受控 bootstrap：先只读巡检，schema 缺失
// 时跳过（J3b bootstrap 拒绝创建 schema——必须由受控数据库流程预置，
// maintenance 不越权，缺失由 probeOwnerStores 记入 pending）；schema 存在时
// 执行 apply 建表，apply 失败即错（不静默降级）。
func ensureJ3BOwnerStore(ctx context.Context, db *sql.DB) error {
	inspection, err := j3bmodelcheck.Run(ctx, db, false)
	if err != nil {
		return fmt.Errorf("mockdata 巡检 juhe_j3b 属主存储失败: %w", err)
	}
	if inspection.MissingSchema {
		return nil
	}
	if _, err := j3bmodelcheck.Run(ctx, db, true); err != nil {
		return fmt.Errorf("mockdata 受控 bootstrap juhe_j3b 失败: %w", err)
	}
	return nil
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
