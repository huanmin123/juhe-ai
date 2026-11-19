// 媒体统一价格一次性迁移命令薄壳（非对话媒体统一计费设计 §9.2/§9.3）：
//   - migrate-media-pricing --dry-run --manifest-out <path>：只读盘点
//     juhe_business.models 的 manual-override 候选行，把可换算行与
//     manual_review 清单写入 manifest（dry-run 唯一写入）；
//   - migrate-media-pricing：要求 --stop-write-confirmed --backup-confirmed，
//     单业务库事务内重新盘点并写入可换算行；无法换算的行保持 NULL，不阻断；
//   - 重复执行幂等；已写入的行不再进入候选集。
//
// 退出码契约：0 成功（含 dry-run）、1 运行失败、2 用法错误。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mediamigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

func runMigrateMediaPricingCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance migrate-media-pricing", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only inventory: write the reviewable manifest to --manifest-out and change nothing")
	manifestOut := fs.String("manifest-out", "", "manifest JSON output path (required with --dry-run; optional for apply archiving)")
	stopWriteConfirmed := fs.Bool("stop-write-confirmed", false, "confirm gateway/jobs writers are stopped for the migration (required for apply)")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified (required for apply)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	parsedDSN := strings.TrimSpace(*dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "migrate-media-pricing 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	if *dryRun {
		outPath := strings.TrimSpace(*manifestOut)
		if outPath == "" {
			fmt.Fprintln(os.Stderr, "--dry-run 需要 --manifest-out 指向 manifest 输出文件")
			return 2
		}
		return migrateMediaPricingDryRun(parsedDSN, outPath)
	}
	if !*stopWriteConfirmed || !*backupConfirmed {
		fmt.Fprintln(os.Stderr, "正式执行要求 --stop-write-confirmed --backup-confirmed（停写并验证可恢复备份后执行；命令本身不负责停进程）")
		return 2
	}
	return migrateMediaPricingApply(parsedDSN, strings.TrimSpace(*manifestOut))
}

func mediaPricingMigrationOptions(dryRun bool) mediamigration.Options {
	return mediamigration.Options{
		DryRun:              dryRun,
		CatalogMediaPricing: schema.SeedMediaPricingJSON,
	}
}

func migrateMediaPricingDryRun(dsn, outPath string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := mediamigration.BuildPlan(context.Background(), db, mediaPricingMigrationOptions(true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "媒体价格迁移盘点失败: %v\n", err)
		return 1
	}
	if err := writeMediaPricingManifest(outPath, plan); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("dry-run manifest 已写入 %s (scanned=%d, convertible=%d, manualReview=%d, skipped=%d)\n",
		outPath, plan.Scanned, plan.Convertible, plan.ManualReview, plan.Skipped)
	logMediaPricingSummary(plan)
	return 0
}

func migrateMediaPricingApply(dsn, manifestOut string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	// 与 upgrade/ensure-schema/seed 共用升级编排锁：媒体价格迁移必须在
	// seed 前单独完成，禁止与结构变更或 seed 并发。
	unlock, lockErr := upgradepipeline.AcquireOrchestrationLock(context.Background(), db)
	if lockErr != nil {
		fmt.Fprintf(os.Stderr, "媒体价格迁移取升级编排锁失败: %v\n", lockErr)
		return 1
	}
	defer unlock()
	plan, err := mediamigration.Apply(context.Background(), db, mediaPricingMigrationOptions(false))
	if err != nil {
		fmt.Fprintf(os.Stderr, "媒体价格迁移失败: %v\n", err)
		return 1
	}
	if manifestOut != "" {
		if writeErr := writeMediaPricingManifest(manifestOut, plan); writeErr != nil {
			fmt.Fprintf(os.Stderr, "迁移已提交，但写 manifest 失败: %v\n", writeErr)
			return 1
		}
	}
	fmt.Printf("媒体价格迁移完成 (scanned=%d, convertible=%d, applied=%d, manualReview=%d, skipped=%d)\n",
		plan.Scanned, plan.Convertible, plan.Applied, plan.ManualReview, plan.Skipped)
	logMediaPricingSummary(plan)
	if plan.ManualReview > 0 {
		fmt.Fprintf(os.Stderr, "[migrate-media-pricing] 有 %d 行无法自动换算，已保持 media_pricing_json=NULL；请按 manifest 明细人工补价（设计 §9.2）。\n", plan.ManualReview)
	}
	return 0
}

func writeMediaPricingManifest(outPath string, plan mediamigration.Plan) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		return fmt.Errorf("encode media pricing manifest: %w", err)
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写 manifest %s 失败: %w", outPath, err)
	}
	return nil
}

func logMediaPricingSummary(plan mediamigration.Plan) {
	fmt.Fprintf(os.Stderr, "[migrate-media-pricing] 库 %s mode=%s dryRun=%t scanned=%d convertible=%d applied=%d manualReview=%d skipped=%d\n",
		plan.Database, plan.Mode, plan.DryRun, plan.Scanned, plan.Convertible, plan.Applied, plan.ManualReview, plan.Skipped)
	for _, row := range plan.Rows {
		if row.Action != mediamigration.ActionManualReview {
			continue
		}
		fmt.Fprintf(os.Stderr, "manual_review: %s %s/%s kind=%s reason=%s\n",
			row.ID, row.ProviderCode, row.Model, row.Kind, row.Reason)
	}
}
