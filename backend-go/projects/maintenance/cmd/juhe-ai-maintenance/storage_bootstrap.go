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
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
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
	// PendingBackfillTables 是 ensure 后 backfill_progress 中 status <> 'done'
	// 的表名清单（成功成本历史回填治理：ensure 不自动迁移，仅在结果摘要
	// 提示待回填表；marker 表不存在或全部 done 时缺省不出现，见计划 §3.2）。
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
			// stats 库的回填待办并在结果摘要提示；marker 表不存在（未跑过
			// ensure 的库）或刚建表为空均静默，探测失败保留原始错误。
			pending, _, pendingErr := pendingBackfillTablesSQLite(context.Background(), parsed.Stats)
			if pendingErr != nil {
				fmt.Fprintf(os.Stderr, "sqlite ensure-schema 后探测回填待办失败：%v\n", pendingErr)
				return 1
			}
			report.PendingBackfillTables = pending
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
			// juhe_stats 的回填待办并在结果摘要提示；marker 表不存在（新建
			// 库刚建表为空）或全部 done 均静默，探测失败保留原始错误。
			pending, _, pendingErr := pendingBackfillTables(context.Background(), db, backfillDialect{postgres: true})
			if pendingErr != nil {
				fmt.Fprintf(os.Stderr, "postgres ensure-schema 后探测回填待办失败：%v\n", pendingErr)
				return 1
			}
			report.PendingBackfillTables = pending
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
	// 结果摘要提示（计划 §3.2 原文口径）：仅在确有非 done marker 时输出。
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
