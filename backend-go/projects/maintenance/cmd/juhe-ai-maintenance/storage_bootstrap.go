// The juhe-ai-maintenance storage bootstrap commands (--ensure-schema /
// --seed): the one-shot, operator-invoked counterpart of the Node storage
// startup (backend/src/storage/database.ts getBusinessDatabase:
// applyBusinessSchema + seedDefaults) and the Node PostgreSQL init script
// (backend/src/scripts/maintenance/init-postgres-schema.ts:
// applyPostgresSchema + seedPostgresDefaults).
//
// Driver matrix:
//   - sqlite: six-database ensure (business/stats/chat/codex-context/
//     dataset/usage-catalog) plus the business seed; paths come as
//     --paths key=value pairs, mirroring the Node per-file storage layout.
//   - postgres: schema + seed over one --dsn, mirroring the Node init script
//     (the Node runtime itself never auto-ensures PostgreSQL —
//     database.ts refuses JUHE_AI_DATABASE_DRIVER=postgres — so the gateway
//     does not auto-ensure in PG mode either; this command is the external
//     execution path).
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
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// sqliteStoragePaths mirrors the Node six-database storage layout.
type sqliteStoragePaths struct {
	Business               string
	Chat                   string
	Dataset                string
	UsageCatalog           string
	Stats                  string
	CodexContextShardRoot  string
	CodexContextShardCount int
}

// codexContextShardFilename mirrors Node codexContextStateShardPath.
func codexContextShardFilename(shardIndex int) string {
	return fmt.Sprintf("state-%03d.sqlite3", shardIndex)
}

// parseSQLiteStoragePaths parses --paths "business=...,chat=...,...".
func parseSQLiteStoragePaths(raw string) (sqliteStoragePaths, error) {
	paths := sqliteStoragePaths{CodexContextShardCount: 16}
	seen := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, value, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			return sqliteStoragePaths{}, fmt.Errorf("--paths 条目必须是 key=value 形式：%q", entry)
		}
		if seen[key] {
			return sqliteStoragePaths{}, fmt.Errorf("--paths 条目重复：%q", key)
		}
		seen[key] = true
		switch key {
		case "business":
			paths.Business = value
		case "chat":
			paths.Chat = value
		case "dataset":
			paths.Dataset = value
		case "usage-catalog":
			paths.UsageCatalog = value
		case "stats":
			paths.Stats = value
		case "codex-context-shard-root":
			paths.CodexContextShardRoot = value
		case "codex-context-shard-count":
			count, err := strconv.Atoi(value)
			if err != nil || count < 1 || count > 256 {
				return sqliteStoragePaths{}, fmt.Errorf("--paths codex-context-shard-count 必须在 1 到 256 之间：%q", value)
			}
			paths.CodexContextShardCount = count
		default:
			return sqliteStoragePaths{}, fmt.Errorf("--paths 未知 key %q（有效 key：business、chat、dataset、usage-catalog、stats、codex-context-shard-root、codex-context-shard-count）", key)
		}
	}
	var missing []string
	for key, value := range map[string]string{
		"business":                 paths.Business,
		"chat":                     paths.Chat,
		"dataset":                  paths.Dataset,
		"usage-catalog":            paths.UsageCatalog,
		"stats":                    paths.Stats,
		"codex-context-shard-root": paths.CodexContextShardRoot,
	} {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return sqliteStoragePaths{}, fmt.Errorf("--paths 缺少必填 key：%s", strings.Join(missing, "、"))
	}
	return paths, nil
}

// storageBootstrapReport is the JSON result printed by both commands.
type storageBootstrapReport struct {
	Driver    string `json:"driver"`
	EnsureRan bool   `json:"ensureRan"`
	SeedRan   bool   `json:"seedRan"`
	// PendingBackfillTables 是固定六目标中 marker 缺失或 status 非 done
	// 的表名清单。ensure 只提示、不扫描事实表或自动迁移；六表均 done
	// 时省略该字段，见成功成本历史回填治理计划 §3.2。
	PendingBackfillTables []string                  `json:"pendingBackfillTables,omitempty"`
	SQLite                *storageBootstrapSQLite   `json:"sqlite,omitempty"`
	Postgres              *storageBootstrapPostgres `json:"postgres,omitempty"`
}

type storageBootstrapSQLite struct {
	Ensure map[string]schema.SchemaCounts `json:"ensure,omitempty"`
	Seed   *schema.SQLiteSeedResult       `json:"seed,omitempty"`
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
func runStorageBootstrap(ensure, seed bool, driver, paths, dsn, secret string) int {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "sqlite" && driver != "postgres" {
		fmt.Fprintf(os.Stderr, "--driver 必须为 sqlite 或 postgres: %q\n", driver)
		return 2
	}
	if driver == "sqlite" && strings.TrimSpace(dsn) != "" {
		fmt.Fprintln(os.Stderr, "--dsn 只适用于 --driver postgres；sqlite 模式使用 --paths")
		return 2
	}
	if driver == "postgres" && strings.TrimSpace(paths) != "" {
		fmt.Fprintln(os.Stderr, "--paths 只适用于 --driver sqlite；postgres 模式使用 --dsn")
		return 2
	}
	report := storageBootstrapReport{Driver: driver, EnsureRan: ensure, SeedRan: seed}
	var err error
	if driver == "sqlite" {
		parsed, parseErr := parseSQLiteStoragePaths(paths)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", parseErr)
			return 2
		}
		report.SQLite = &storageBootstrapSQLite{}
		if ensure {
			report.SQLite.Ensure, err = ensureSQLiteStorage(context.Background(), parsed)
			if err != nil {
				fmt.Fprintf(os.Stderr, "sqlite ensure-schema 失败：%v\n", err)
				return 1
			}
			// 成功成本历史回填治理（计划 §3.2）：ensure 不自动迁移，仅探测
			// stats 库的回填待办并在结果摘要提示；缺失或非 done 的目标
			// marker 均列为待办，全部 done 才静默；探测失败保留原始错误。
			pending, _, pendingErr := pendingBackfillTablesSQLite(context.Background(), parsed.Stats)
			if pendingErr != nil {
				fmt.Fprintf(os.Stderr, "sqlite ensure-schema 后探测回填待办失败：%v\n", pendingErr)
				return 1
			}
			report.PendingBackfillTables = pending
			// 全新空库自动封存（AI 账户能力域拆分 M2）：schema 确保后，阶段
			// 记录不存在且 accounts 无行时按序执行空库 M1→M2 并封存
			// m2_finalized；记录已存在或 accounts 非空一律跳过（真实切换必须
			// 显式跑迁移命令，见 capabilitydomain.SealFreshInstall；gateway
			// SQLite preflight 经 bootstrap.SeedSQLiteBusiness 走同一封存语义）。
			businessDB, openErr := bootstrap.OpenSQLiteFile(parsed.Business)
			if openErr != nil {
				fmt.Fprintf(os.Stderr, "sqlite ensure-schema 后打开业务库失败：%v\n", openErr)
				return 1
			}
			// 调度偏好独立实体（PLAN-20261012T033000000Z）：ensure 臂恢复式
			// 补种内置调度偏好行，保证存量库「升级 = 跑 ensure-schema 即得
			// 内置行」（ensure 臂不执行完整 seed）；与 --seed 链共用同一
			// upsert 语句，幂等可重入，重复执行不推进 updated_at。
			if seedErr := schema.SeedBuiltinSchedulingPreferenceSQLite(context.Background(), businessDB); seedErr != nil {
				_ = businessDB.Close()
				fmt.Fprintf(os.Stderr, "sqlite ensure-schema 内置调度偏好补种失败：%v\n", seedErr)
				return 1
			}
			sealErr := sealFreshInstallCapabilityDomain(context.Background(), businessDB, capdomain.DialectSQLite)
			if closeErr := businessDB.Close(); sealErr == nil {
				sealErr = closeErr
			}
			if sealErr != nil {
				fmt.Fprintf(os.Stderr, "能力域空库自动封存失败：%v\n", sealErr)
				return 1
			}
		}
		if seed {
			seedResult, seedErr := seedSQLiteBusiness(context.Background(), parsed, secret)
			if seedErr != nil {
				fmt.Fprintf(os.Stderr, "sqlite seed 失败：%v\n", seedErr)
				return 1
			}
			report.SQLite.Seed = &seedResult
		}
	} else {
		parsedDSN := strings.TrimSpace(dsn)
		if parsedDSN == "" {
			fmt.Fprintln(os.Stderr, "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL")
			return 2
		}
		db, openErr := openPostgresBootstrap(parsedDSN)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", openErr)
			return 2
		}
		defer db.Close()
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
			pending, _, pendingErr := pendingBackfillTables(context.Background(), db, backfillDialect{postgres: true})
			if pendingErr != nil {
				fmt.Fprintf(os.Stderr, "postgres ensure-schema 后探测回填待办失败：%v\n", pendingErr)
				return 1
			}
			report.PendingBackfillTables = pending
			// 全新空库自动封存（AI 账户能力域拆分 M2）：与 sqlite ensure 臂
			// 同语义（PG 方言），非空库一律不自动写阶段记录。
			if sealErr := sealFreshInstallCapabilityDomain(context.Background(), db, capdomain.DialectPostgres); sealErr != nil {
				fmt.Fprintf(os.Stderr, "能力域空库自动封存失败：%v\n", sealErr)
				return 1
			}
			// 调度偏好独立实体（PLAN-20261012T033000000Z）：ensure 臂恢复式
			// 补种内置调度偏好行，语义与 sqlite 臂一致（同源权威值、同恢复式
			// 幂等；PG 模式 gateway 启动不自动 ensure/seed，本臂是存量库升级
			// 的触达点）。
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

// ensureSQLiteStorage applies the matching schema to every SQLite file in the
// Node dependency order (business, stats, chat, codex-context shards,
// dataset, usage-catalog).
func ensureSQLiteStorage(ctx context.Context, paths sqliteStoragePaths) (map[string]schema.SchemaCounts, error) {
	ensure := map[string]schema.SchemaCounts{}
	ensureOne := func(name, path string, apply func(context.Context, *sql.DB) (schema.SchemaCounts, error)) error {
		db, err := bootstrap.OpenSQLiteFile(path)
		if err != nil {
			return err
		}
		defer db.Close()
		counts, err := apply(ctx, db)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		ensure[name] = counts
		return nil
	}
	if err := ensureOne("business", paths.Business, schema.EnsureSQLiteBusiness); err != nil {
		return nil, err
	}
	if err := ensureOne("stats", paths.Stats, schema.EnsureSQLiteStats); err != nil {
		return nil, err
	}
	if err := ensureOne("chat", paths.Chat, schema.EnsureSQLiteChat); err != nil {
		return nil, err
	}
	for shardIndex := 0; shardIndex < paths.CodexContextShardCount; shardIndex++ {
		shardPath := filepath.Join(paths.CodexContextShardRoot, codexContextShardFilename(shardIndex))
		if err := ensureOne(fmt.Sprintf("codex-context[%d]", shardIndex), shardPath, schema.EnsureSQLiteCodexContext); err != nil {
			return nil, err
		}
	}
	if err := ensureOne("dataset", paths.Dataset, schema.EnsureSQLiteDataset); err != nil {
		return nil, err
	}
	if err := ensureOne("usage-catalog", paths.UsageCatalog, schema.EnsureSQLiteUsageCatalog); err != nil {
		return nil, err
	}
	return ensure, nil
}

// seedSQLiteBusiness seeds the business file (the only seeded database in
// Node).
func seedSQLiteBusiness(ctx context.Context, paths sqliteStoragePaths, secret string) (schema.SQLiteSeedResult, error) {
	db, err := bootstrap.OpenSQLiteFile(paths.Business)
	if err != nil {
		return schema.SQLiteSeedResult{}, err
	}
	defer db.Close()
	return schema.SeedSQLiteDefaults(ctx, db, schema.SeedOptions{Secret: secret})
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
func sealFreshInstallCapabilityDomain(ctx context.Context, db *sql.DB, dialect capdomain.Dialect) error {
	sealed, err := capabilitydomain.SealFreshInstall(ctx, db, dialect)
	if err != nil {
		return err
	}
	if sealed {
		fmt.Fprintln(os.Stderr, "全新空库已自动封存 AI 账户能力域阶段记录（m2_finalized）。")
	}
	return nil
}
