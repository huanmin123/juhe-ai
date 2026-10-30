// `maintenance upgrade` 子命令入口（升级兼容与迁移账本设计 §9.1）。
// 分发策略：runMaintenance 在旗标解析前识别 argv[0]=="upgrade" 剥离后进入
// 本文件的独立 FlagSet——与既有单层 FlagSet 互不干扰（Go flag 在首个非 flag
// 参数处停止解析，不能沿用单层形态承载子命令）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/version"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// runUpgradeCommand 是 upgrade 子命令入口，返回进程退出码（§9.2）。
func runUpgradeCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance upgrade", flag.ContinueOnError)
	bootstrapDSN := fs.String("dsn", "", "primary PostgreSQL URL (required)")
	dryRun := fs.Bool("dry-run", false, "read-only: print the plan (pending/adopt/destructive disclosure/rollback level/estimate) and exit")
	stopWrite := fs.Bool("stop-write-confirmed", false, "confirm gateway and jobs are stopped")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup exists")
	secret := fs.String("secret", "", "seed encryption secret (or JUHE_AI_SECRET)")
	reportOut := fs.String("report-out", "", "additionally write the JSON report to this file (write failure is a WARN, never flips the exit code)")
	stepTimeout := fs.Duration("step-timeout", 30*time.Minute, "per-step apply/verify timeout, clamped to 1m..4h")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		// 旗标解析失败：无 JSON 报告（字段无从产生），stderr 单行 + exit 2（§9.3）。
		return ExitUsageCode
	}
	if strings.TrimSpace(*bootstrapDSN) == "" {
		fmt.Fprintln(os.Stderr, "upgrade 需要 --dsn 指向主库 PostgreSQL URL")
		return ExitUsageCode
	}
	// --step-timeout 钳制 1m..4h（§9.1）。
	timeout := *stepTimeout
	if timeout < time.Minute {
		timeout = time.Minute
	}
	if timeout > 4*time.Hour {
		timeout = 4 * time.Hour
	}

	// 构建身份：dev 直建降级为 dev-<commit>（§6.2 dev 构建披露）。
	appVersion := version.Version
	commit := version.Commit
	if appVersion == "dev" && commit != "none" {
		appVersion = "dev-" + commit
	}
	opts := upgradepipeline.Options{
		PrimaryDSN:         strings.TrimSpace(*bootstrapDSN),
		Secret:             strings.TrimSpace(*secret),
		DryRun:             *dryRun,
		StopWriteConfirmed: *stopWrite,
		BackupConfirmed:    *backupConfirmed,
		StepTimeout:        timeout,
		AppVersion:         appVersion,
	}
	if opts.Secret == "" {
		opts.Secret = strings.TrimSpace(os.Getenv("JUHE_AI_SECRET"))
	}

	out := upgradepipeline.Run(context.Background(), opts, upgradeSteps(opts.PrimaryDSN, opts.Secret))
	if out.Report != nil {
		encoded, encodeErr := json.Marshal(out.Report)
		if encodeErr == nil {
			fmt.Println(string(encoded))
		} else {
			// 报告编码失败属于实现缺陷而非运行失败：降级为 stderr 提示，
			// 退出码以运行结果为准。
			fmt.Fprintf(os.Stderr, "报告 JSON 编码失败: %v\n", encodeErr)
		}
		if strings.TrimSpace(*reportOut) != "" {
			// 落盘失败仅 WARN，不翻转退出码（stdout 报告是权威载体，§9.1）。
			if writeErr := os.WriteFile(*reportOut, encoded, 0o600); writeErr != nil {
				fmt.Fprintf(os.Stderr, "WARN: --report-out 写入失败: %v\n", writeErr)
			}
		}
	}
	if out.Stderr != "" {
		fmt.Fprintf(os.Stderr, "%s\n", out.Stderr)
	}
	return out.ExitCode
}

// ExitUsageCode 是 upgrade 子命令的用法错误退出码（与包级 maintenance 契约
// 同值，避免与 flag 包惯例冲突而独立命名）。
const ExitUsageCode = 2
