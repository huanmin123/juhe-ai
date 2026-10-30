// The juhe-ai-maintenance storage bootstrap commands (--ensure-schema /
// --seed): the one-shot, operator-invoked counterpart of the Node PostgreSQL
// init script (backend/src/scripts/maintenance/init-postgres-schema.ts:
// applyPostgresSchema + seedPostgresDefaults).
//
// Storage shape (2026-10-24 SQLite 退役, PLAN-20261024T051925945Z B4):
// PostgreSQL over one --dsn is the only form; the six-database SQLite
// ensure/seed arm and the --driver/--paths flag family are deleted.
//
// Exit codes follow the maintenance command contract: 0 success, 2 usage
// errors (missing/invalid flags), 1 runtime failure.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// storageBootstrapReport is the JSON result printed by both commands.
type storageBootstrapReport struct {
	Driver    string `json:"driver"`
	EnsureRan bool   `json:"ensureRan"`
	SeedRan   bool   `json:"seedRan"`
	// PendingBackfillTables 是固定六目标中 marker 缺失或 status 非 done
	// 的表名清单。ensure 只提示、不扫描事实表或自动迁移；六表均 done
	// 时省略该字段，见成功成本历史回填治理计划 §3.2。
	PendingBackfillTables []string                  `json:"pendingBackfillTables,omitempty"`
	Postgres              *storageBootstrapPostgres `json:"postgres,omitempty"`
}

type storageBootstrapPostgres struct {
	Schema *schema.PGResult     `json:"schema,omitempty"`
	Seed   *schema.PGSeedResult `json:"seed,omitempty"`
}

// seedSecretValue resolves the encryption secret: flag, then JUHE_AI_SECRET,
// then the Node dev default handled inside schema.SeedOptions.
func seedSecretValue(flagValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return strings.TrimSpace(flagValue)
	}
	return strings.TrimSpace(os.Getenv("JUHE_AI_SECRET"))
}

// runStorageBootstrap implements --ensure-schema / --seed. exitCode follows
// the maintenance contract (0 ok / 1 runtime failure); usage errors are
// rejected by the caller before this runs.
func runStorageBootstrap(ensure, seed bool, dsn, secret string) int {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--ensure-schema/--seed 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	db, openErr := openPostgresBootstrap(parsedDSN)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", openErr)
		return 2
	}
	defer db.Close()
	// 升级编排互斥锁（升级兼容与迁移账本设计 §9.5）：与 maintenance upgrade
	// 共用 advisorylock.UpgradeOrchestration，防止结构收敛与升级编排并发；
	// 2s 快失败（BUG-0313 同口径），拿不到即退出由操作者重试。
	unlock, lockErr := upgradepipeline.AcquireOrchestrationLock(context.Background(), db)
	if lockErr != nil {
		fmt.Fprintf(os.Stderr, "ensure-schema/seed 取升级编排锁失败: %v\n", lockErr)
		return 1
	}
	defer unlock()
	report := storageBootstrapReport{Driver: "postgres", EnsureRan: ensure, SeedRan: seed}
	report.Postgres = &storageBootstrapPostgres{}
	if ensure {
		schemaResult, ensureErr := schema.EnsurePostgres(context.Background(), db)
		if ensureErr != nil {
			fmt.Fprintf(os.Stderr, "postgres ensure-schema 失败：%v\n", ensureErr)
			return 1
		}
		report.Postgres.Schema = &schemaResult
		// 成功成本历史回填治理（计划 §3.2）：ensure 不自动迁移，仅探测
		// juhe_stats 的回填待办并在结果摘要提示；缺失或非 done 的目标
		// marker 均列为待办，全部 done 才静默；探测失败保留原始错误。
		pending, _, pendingErr := pendingBackfillTables(context.Background(), db, backfillDialect{})
		if pendingErr != nil {
			fmt.Fprintf(os.Stderr, "postgres ensure-schema 后探测回填待办失败：%v\n", pendingErr)
			return 1
		}
		report.PendingBackfillTables = pending
		// 全新空库自动封存（AI 账户能力域拆分 M2）：阶段记录不存在且
		// accounts 无行时按序执行空库 M1→M2 并封存 m2_finalized；记录已存在
		// 或 accounts 非空一律跳过（真实切换必须显式跑迁移命令）。
		if sealErr := sealFreshInstallCapabilityDomain(context.Background(), db); sealErr != nil {
			fmt.Fprintf(os.Stderr, "能力域空库自动封存失败：%v\n", sealErr)
			return 1
		}
		// 调度偏好独立实体（PLAN-20261012T033000000Z）：ensure 臂恢复式
		// 补种内置调度偏好行，保证存量库「升级 = 跑 ensure-schema 即得
		// 内置行」（ensure 臂不执行完整 seed）；与 --seed 链共用同一权威值，
		// 幂等可重入，重复执行不推进 updated_at。
		if seedErr := schema.SeedBuiltinSchedulingPreferencePostgres(context.Background(), db); seedErr != nil {
			fmt.Fprintf(os.Stderr, "postgres ensure-schema 内置调度偏好补种失败：%v\n", seedErr)
			return 1
		}
	}
	if seed {
		seedResult, seedErr := schema.SeedPostgresDefaults(context.Background(), db, schema.SeedOptions{Secret: seedSecretValue(secret)})
		if seedErr != nil {
			fmt.Fprintf(os.Stderr, "postgres seed 失败：%v\n", seedErr)
			return 1
		}
		report.Postgres.Seed = &seedResult
	}
	// 结果摘要提示：缺失或非 done 的目标 marker 均为待办。
	if len(report.PendingBackfillTables) > 0 {
		fmt.Fprintf(os.Stderr, "成功成本历史回填提示：存在待回填表（%s），请在维护窗口执行 --apply-success-cost-backfill。\n",
			strings.Join(report.PendingBackfillTables, "、"))
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode storage bootstrap report: %v\n", err)
		return 1
	}
	return 0
}

// openPostgresBootstrap mirrors the maintenance PostgreSQL connection contract
// (explicit postgres URL with host, database and role).
func openPostgresBootstrap(rawURL string) (*sql.DB, error) {
	if !strings.HasPrefix(rawURL, "postgres://") && !strings.HasPrefix(rawURL, "postgresql://") {
		return nil, errors.New("--dsn 必须是 postgres:// 或 postgresql:// URL")
	}
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres bootstrap connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// sealFreshInstallCapabilityDomain 在 schema 确保后执行全新空库自动封存
// （AI 账户能力域拆分 M2）：阶段记录不存在且 accounts 无任何行时按序执行
// 空库 M1→M2 并写 m2_finalized；记录已存在或 accounts 非空一律跳过（真实
// 切换必须显式跑迁移命令）。封存成功时向 stderr 输出一行提示，便于操作者
// 在 ensure 输出中确认封存发生。
func sealFreshInstallCapabilityDomain(ctx context.Context, db *sql.DB) error {
	sealed, err := capabilitydomain.SealFreshInstall(ctx, db)
	if err != nil {
		return err
	}
	if sealed {
		fmt.Fprintln(os.Stderr, "全新空库已自动封存 AI 账户能力域阶段记录（m2_finalized）。")
	}
	return nil
}
