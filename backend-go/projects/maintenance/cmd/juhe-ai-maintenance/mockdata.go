// The juhe-ai-maintenance local mockdata commands (--mockdata /
// --verify-mockdata-coverage): PG 造数重写（T66 第一波）的 CLI 契约面。
//
// 存储形态（2026-10-24 SQLite 退役, PLAN-20261024T051925945Z B4；T66 第一波
// 交付 PG 运行）：目标为 --dsn 指向的 PostgreSQL 单库多 schema 存储。
// --mockdata-summary-file 是可选的摘要路径（无隐式回退）：--mockdata 把摘要
// JSON 写到该路径；--verify-mockdata-coverage 从同一路径读域接线快照，路径为
// 空时全部域按未接线评估。
//
// Exit codes follow the maintenance command contract: 0 success, 1 runtime
// failure, 2 usage errors (missing/invalid flags), and 3 for the coverage
// command when the report is not ready.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mockdata"
)

// mockdataCommandFlags 汇总 CLI 层的造数参数，避免 runMaintenance 里散落参数
// 拼装（也方便测试直接构造边界组合）。
type mockdataCommandFlags struct {
	VerifyCoverage bool
	Days           int
	DailyRequests  int
	DSN            string
	Secret         string
	SummaryFile    string
}

// runMockdataCommand 是 --mockdata / --verify-mockdata-coverage 的统一入口。
func runMockdataCommand(flags mockdataCommandFlags) int {
	if code := validateMockdataUsage(flags); code != 0 {
		return code
	}
	options := mockdata.Options{
		DSN:           strings.TrimSpace(flags.DSN),
		Days:          flags.Days,
		DailyRequests: flags.DailyRequests,
		Secret:        seedSecretValue(flags.Secret),
		SummaryFile:   strings.TrimSpace(flags.SummaryFile),
	}
	if flags.VerifyCoverage {
		return runMockdataCoverage(options)
	}
	return runMockdataSeed(options)
}

// validateMockdataUsage 做全部「不碰存储」的参数校验：DSN、参数边界。
// 未通过时给出中文原因并返回 2；0 表示可以继续。
func validateMockdataUsage(flags mockdataCommandFlags) int {
	switch {
	case strings.TrimSpace(flags.DSN) == "":
		fmt.Fprintln(os.Stderr, "--mockdata/--verify-mockdata-coverage 需要 --dsn 指向显式的 PostgreSQL URL（开发库或测试库）")
		return 2
	case flags.Days < 1 || flags.Days > mockdata.MaxDays:
		fmt.Fprintf(os.Stderr, "--mockdata-days 必须在 1 到 %d 之间: %d\n", mockdata.MaxDays, flags.Days)
		return 2
	case flags.DailyRequests < 1 || flags.DailyRequests > mockdata.MaxDailyRequests:
		fmt.Fprintf(os.Stderr, "--mockdata-daily-requests 必须在 1 到 %d 之间: %d\n", mockdata.MaxDailyRequests, flags.DailyRequests)
		return 2
	}
	return 0
}

// runMockdataSeed 执行一次造数并把 Report 以 JSON 写 stdout。
func runMockdataSeed(options mockdata.Options) int {
	// 进度写 stderr：stdout 必须只有一条 JSON，调用方（脚本 / 页面工具）会直接
	// 解析它。
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	report, err := mockdata.RunWithLogger(context.Background(), options, logger)
	if err != nil {
		// 失败也要打印已经完成的报告：清理计数与域结果是排障的第一手证据。
		_ = json.NewEncoder(os.Stdout).Encode(report)
		fmt.Fprintf(os.Stderr, "mockdata 造数失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode mockdata report: %v\n", err)
		return 1
	}
	return 0
}

// runMockdataCoverage 执行覆盖校验：Ready 时 exit 0，未 Ready 时 exit 3
// （与既有 read-only 门禁命令的 fail-closed 约定一致）；校验运行失败 exit 1。
func runMockdataCoverage(options mockdata.Options) int {
	report, err := mockdata.VerifyCoverage(context.Background(), options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockdata 覆盖校验失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode mockdata coverage report: %v\n", err)
		return 1
	}
	if !report.Ready {
		return 3
	}
	return 0
}
