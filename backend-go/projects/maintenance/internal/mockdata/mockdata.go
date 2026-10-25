// Package mockdata 是本地开发造数能力（docs/functions/Mockdata造数设计.md）
// 的维护入口。
//
// 2026-10-24 存储形态收口（PLAN-20261024T051925945Z D7，用户裁决「彻底物理
// 删除、无兼容期」）：原「12 个固定 sqlite3 文件 + usage/codex 分片」存储
// 实现整体删除；PG 造数重写（PG DSN 目标 + maintenance schema EnsurePostgres
// 建骨架 + PG INSERT 造数）以本文件的 Options/Report 骨架与 CLI 参数契约为
// 落点，重写完成前 Run / VerifyCoverage 返回明确的未实现错误——命令可解析、
// 可报错、绝不静默假成功。
//
// fake 数据生成逻辑（domain_*.go 的形状与数量语义）随 sqlite 实现进入 git
// 历史（本删除批次之前的提交），PG 重写时按同语义重建。
package mockdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// 造数域名（PG 重写的域切分与报告口径沿用的固定词表）。
const (
	// DomainBusiness 拥有 juhe_business（系统账户、AI 账户、分组、路由策略、
	// API Key、授权、公告、外部来源、响应检查策略、账户测试与题库）。
	DomainBusiness = "business"
	// DomainUsage 拥有 juhe_usage_catalog 与 usage 明细分片面。
	DomainUsage = "usage"
	// DomainStats 拥有 juhe_stats 明细写入面（派生聚合由既有聚合器重建，
	// 不手工伪造）。
	DomainStats = "stats"
	// DomainObservability 拥有运行日志、表监控、审计日志、操作日志与
	// juhe_dataset（公开接口日志、记录清理目标）。
	DomainObservability = "observability"
	// DomainChatCodexModelCheck 拥有 juhe_chat、codex context 面、J3b 模型
	// 检测专库、后台任务运行库与账户健康库。
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

// ErrPGSeedPending 是 PG 造数重写完成前的固定未实现错误：D7 切片只交付
// 骨架与 CLI 契约，不静默半成品。文案同时是 CLI 与库调用方的稳定判定锚。
var ErrPGSeedPending = errors.New("mockdata PG 造数尚未交付：sqlite 实现已按 SQLite 退役计划删除，PG 重写（EnsurePostgres 建骨架 + PG INSERT 造数）完成后本命令可用")

// DomainResult 是一个域的执行结果：Name 是域名，Counts 是「逻辑对象 → 行数」
// 计数。PG 重写保留该报告口径。
type DomainResult struct {
	Name   string         `json:"name"`
	Counts map[string]int `json:"counts"`
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

// Report 是 --mockdata 的 stdout JSON，也是库调用方的返回值。PG 重写保留
// 字段词表；骨架阶段只回填 driver 与参数回显字段。
type Report struct {
	Driver        string `json:"driver"`
	DSN           string `json:"dsn"`
	Days          int    `json:"days"`
	DailyRequests int    `json:"dailyRequests"`
	StartedAt     string `json:"startedAt"`
	FinishedAt    string `json:"finishedAt"`
	DurationMs    int64  `json:"durationMs"`
}

// CoverageReport 是 --verify-mockdata-coverage 的 stdout JSON。PG 重写保留
// 该口径（Ready=false 时 CLI exit 3）。
type CoverageReport struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
	Ready  bool   `json:"ready"`
}

// Run 执行一次完整造数：PG 重写完成前返回 ErrPGSeedPending（参数校验先行，
// 越界参数优先报边界错误）。
func Run(ctx context.Context, options Options) (Report, error) {
	return RunWithLogger(ctx, options, nil)
}

// RunWithLogger 与 Run 相同，但接受注入的日志器（CLI 用它把进度写到 stderr，
// 保持 stdout 只有 JSON）。
func RunWithLogger(ctx context.Context, options Options, logger *slog.Logger) (Report, error) {
	if err := options.validate(); err != nil {
		return Report{}, err
	}
	_ = logger
	started := options.clock().UTC().Format(time.RFC3339)
	return Report{
		Driver:        "postgres",
		DSN:           options.DSN,
		Days:          options.Days,
		DailyRequests: options.DailyRequests,
		StartedAt:     started,
	}, ErrPGSeedPending
}

// VerifyCoverage 执行只读覆盖校验：PG 重写完成前返回 ErrPGSeedPending。
func VerifyCoverage(ctx context.Context, options Options) (CoverageReport, error) {
	if err := options.validate(); err != nil {
		return CoverageReport{}, err
	}
	return CoverageReport{Driver: "postgres", DSN: options.DSN}, ErrPGSeedPending
}
