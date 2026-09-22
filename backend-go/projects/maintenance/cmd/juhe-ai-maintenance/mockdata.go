// The juhe-ai-maintenance local mockdata commands (--mockdata /
// --verify-mockdata-coverage): the Go port of the Node maintenance script
// (migration-backup-1/node/final-archive/backend/src/scripts/maintenance/mockdata/
// cli.ts), scoped to the local SQLite layout.
//
// Driver matrix: SQLite only. The development runtime is go-only + SQLite
// (scripts/dev.mjs sets JUHE_AI_DATA_DIR / JUHE_AI_LOG_DIR); PostgreSQL mockdata
// would need the Node high-performance path's write fencing, so an explicit
// --driver postgres or --dsn is rejected as a usage error instead of silently
// seeding the wrong database.
//
// Exit codes follow the maintenance command contract: 0 success, 2 usage errors
// (missing/invalid flags, unsupported driver), 1 runtime failure, and 3 for the
// coverage command when the report is not ready.
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
	DataDir        string
	LogDir         string
	Days           int
	DailyRequests  int
	Driver         string
	DSN            string
	Secret         string
}

// runMockdataCommand 是 --mockdata / --verify-mockdata-coverage 的统一入口。
func runMockdataCommand(flags mockdataCommandFlags) int {
	if code := validateMockdataUsage(flags); code != 0 {
		return code
	}
	paths, err := resolveMockdataPaths(flags.DataDir, flags.LogDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	if flags.VerifyCoverage {
		return runMockdataCoverage(paths)
	}
	return runMockdataSeed(paths, flags)
}

// validateMockdataUsage 做全部「不碰存储」的参数校验：驱动、DSN、参数边界。
// 未通过时给出中文原因并返回 2；0 表示可以继续。
func validateMockdataUsage(flags mockdataCommandFlags) int {
	driver := strings.ToLower(strings.TrimSpace(flags.Driver))
	switch {
	case driver != "" && driver != "sqlite":
		fmt.Fprintf(os.Stderr, "juhe-ai-maintenance mockdata 只支持 SQLite：--driver 必须为空或 sqlite（当前 %q）\n", flags.Driver)
		return 2
	case strings.TrimSpace(flags.DSN) != "":
		fmt.Fprintln(os.Stderr, "juhe-ai-maintenance mockdata 只支持 SQLite：不接受 --dsn，请用 --mockdata-data-dir 指定数据根")
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

// resolveMockdataPaths 解析数据根与日志目录。数据根必须是显式的（flag 或
// JUHE_AI_DATA_DIR）：一次误执行的造数命令不得在仓库工作区里凭空建目录。
func resolveMockdataPaths(dataDir, logDir string) (mockdata.Paths, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("JUHE_AI_DATA_DIR"))
	}
	paths, err := mockdata.ResolvePaths(dataDir, logDir, os.Getenv)
	if err != nil {
		return mockdata.Paths{}, err
	}
	return paths, nil
}

// runMockdataSeed 执行一次造数并把 Report 以 JSON 写 stdout。
func runMockdataSeed(paths mockdata.Paths, flags mockdataCommandFlags) int {
	// 进度写 stderr：stdout 必须只有一条 JSON，调用方（脚本 / 页面工具）会直接
	// 解析它。
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	report, err := mockdata.RunWithLogger(context.Background(), mockdata.Options{
		Paths:         paths,
		Days:          flags.Days,
		DailyRequests: flags.DailyRequests,
		Secret:        seedSecretValue(flags.Secret),
	}, logger)
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
// （与既有 read-only 门禁命令的 fail-closed 约定一致）。
func runMockdataCoverage(paths mockdata.Paths) int {
	report, exitCode, err := mockdataCoverageReport(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockdata 覆盖校验失败：%v\n", err)
		return exitCode
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

// mockdataCoverageReport 是覆盖命令的可测内核：返回报告与「出错时的退出码」。
func mockdataCoverageReport(paths mockdata.Paths) (mockdata.CoverageReport, int, error) {
	options := mockdata.Options{
		Paths:         paths,
		Days:          mockdata.DefaultDays,
		DailyRequests: mockdata.DefaultDailyRequests,
	}
	report, err := mockdata.VerifyPathsCoverage(context.Background(), options)
	if err != nil {
		return mockdata.CoverageReport{}, 1, err
	}
	return report, 0, nil
}
