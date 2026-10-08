package gometrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	stdlib "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	// 缺省 1m（2026-10-08 粒度根治，原 15s）：明细表 go_runtime_metrics_samples
	// 无任何 SELECT 消费方（系统指标页只读 hourly/trend_windows 聚合表），
	// 15s×双进程×30 天保留约 34.6 万行纯写入浪费；1m 密度对小时/天级窗口
	// 聚合语义无损。裁决与量化见
	// docs/plans/计划-20261008T203741000Z-表监控采样粒度根治.md 第 7 节。
	defaultInterval      = time.Minute
	defaultRetentionDays = 30
)

type Config struct {
	Enabled       bool
	Store         SQLDialect
	DatabasePath  string
	PostgresURL   string
	Interval      time.Duration
	RetentionDays int
	Service       string
	Role          string
}

// LoadConfig parses the shared JUHE_AI_GO_RUNTIME_METRICS_* environment
// family. defaultRole is the calling process' role (gateway / jobs) and is
// authoritative: the former JUHE_AI_GO_RUNTIME_METRICS_ROLE override was
// removed on 2026-09-27 because the shared env forwarding made the gateway
// sample under the jobs role, skewing the read side's fixed two-role query.
// The postgres URL falls back to JUHE_AI_POSTGRES_URL when the dedicated
// JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL is unset. Since 2026-09-27 the
// store is factory-on: an unset JUHE_AI_GO_RUNTIME_METRICS_STORE follows
// JUHE_AI_DATABASE_DRIVER (postgres → postgres; sqlite / empty / unknown →
// sqlite, the zero-config standalone default) so a fresh deployment starts
// sampling instead of showing a permanently empty metrics page; only the
// explicit "disabled" value turns sampling off (the former
// JUHE_AI_GO_RUNTIME_METRICS_ENABLED switch was removed on 2026-09-21).
func LoadConfig(getenv func(string) string, defaultRole string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{Interval: defaultInterval, RetentionDays: defaultRetentionDays, Service: "juhe-ai", Role: defaultRole}
	store := strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_STORE")))
	if store == "disabled" {
		// 2026-09-27 起默认开启（跟随主存储），显式 disabled 是唯一关闭路径；
		// 关闭后读接口返回 samplingEnabled=false。
		return cfg, nil
	}
	if store == "" {
		// 未配置时跟随 JUHE_AI_DATABASE_DRIVER（与 jobs datadir 的零配置约定
		// 同方向）：postgres → postgres；sqlite、空或未知值 → sqlite（零配置
		// standalone 默认）。跟随派生与显式取值同样 Enabled=true。
		store = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")))
		if store != string(DialectPostgres) {
			store = string(DialectSQLite)
		}
	} else if store != string(DialectSQLite) && store != string(DialectPostgres) {
		return Config{}, fmt.Errorf("JUHE_AI_GO_RUNTIME_METRICS_STORE 必须为 sqlite、postgres 或 disabled")
	}
	cfg.Enabled = true
	cfg.Store = SQLDialect(store)
	cfg.DatabasePath = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH"))
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL"))
	if cfg.Store == DialectSQLite && cfg.DatabasePath == "" {
		// 未配置时按数据根派生固定文件名（与 jobs datadir 约定对齐）。
		cfg.DatabasePath = filepath.Join(metricsDataRoot(getenv), "go-runtime-metrics.sqlite3")
	}
	if cfg.Store == DialectPostgres && cfg.PostgresURL == "" {
		// 2026-09-27 起专用 URL 缺省时回退共享 JUHE_AI_POSTGRES_URL（两进程
		// 共享 env 转发的部署形态无需重复配置连接串）；两者都空才报错。
		cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
		if cfg.PostgresURL == "" {
			return Config{}, errors.New("postgres 模式缺少 JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL 和 JUHE_AI_POSTGRES_URL")
		}
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_INTERVAL")); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval < time.Second {
			return Config{}, fmt.Errorf("JUHE_AI_GO_RUNTIME_METRICS_INTERVAL 必须是不少于 1s 的 duration")
		}
		cfg.Interval = interval
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS")); value != "" {
		retentionDays, err := strconv.Atoi(value)
		if err != nil || retentionDays < 1 || retentionDays > 3650 {
			return Config{}, fmt.Errorf("JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS 必须是 1..3650 的整数")
		}
		cfg.RetentionDays = retentionDays
	}
	if value := strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_SERVICE")); value != "" {
		cfg.Service = value
	}
	// ROLE 不再从环境读取：role 一律取调用方 defaultRole（gateway 进程 =
	// gateway、jobs 进程 = jobs），残留 JUHE_AI_GO_RUNTIME_METRICS_ROLE 值
	// 必须被忽略（该 env 被两进程共享转发，覆盖会导致角色采样失真）。
	return cfg, nil
}

// metricsDataRoot 返回 Go runtime 指标 SQLite 文件的数据根目录：
// JUHE_AI_DATA_DIR（TrimSpace 非空）优先，否则 ./data。该派生与 jobs 模块
// internal/datadir 的 Root 语义保持一致（同一零配置约定：相对进程 cwd、
// TrimSpace 后为空视为未配置、固定名派生）；不在 shared 包 import jobs 的
// datadir——那是 jobs 模块内部包，平台共享包不得反向依赖具体项目模块，
// 因此按同一语义在本地实现（2026-09-27 起 sqlite 路径缺省派生也遵循它）。
func metricsDataRoot(getenv func(string) string) string {
	if dir := strings.TrimSpace(getenv("JUHE_AI_DATA_DIR")); dir != "" {
		return dir
	}
	return "./data"
}

// OpenStore opens the configured SQL handle and wraps it in a Store. A
// disabled config returns nil handles; the caller skips sampling and the
// read routes degrade to empty items instead of erroring.
func OpenStore(cfg Config) (*Store, *sql.DB, error) {
	if !cfg.Enabled {
		return nil, nil, nil
	}
	driver, dsn := "sqlite", cfg.DatabasePath
	if cfg.Store == DialectPostgres {
		driver, dsn = "pgx", cfg.PostgresURL
	}
	if cfg.Store == DialectSQLite {
		if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o755); err != nil {
			return nil, nil, err
		}
		dsn = "file:" + cfg.DatabasePath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	var db *sql.DB
	var err error
	if driver == "pgx" {
		// pgx 池统一经 sqldialect 改写 driver 打开（清理批次 C5 统一裸开
		// 路径）：本包 PG 方言 SQL 由 placeholder() 生成 `$n`，改写层幂等
		// 透传，并对未来遗漏改写的 `?` SQL 提供驱动层兜底；与
		// sql.Open("pgx", dsn) 同一 driver 实例、同一惰性语义。SQLite 路径
		// 原样打开。
		db, err = sqldialect.OpenDB(stdlib.GetDefaultDriver(), dsn)
	} else {
		db, err = sql.Open(driver, dsn)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open Go metrics database: %w", err)
	}
	store, err := NewStore(db, cfg.Store)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return store, db, nil
}

// EnsureReady verifies the pre-provisioned schema (maintenance owns
// EnsureSchema; the samplers only check). Dialect semantics since 2026-09-27:
// postgres callers keep this maintenance-owned check (missing tables fail
// fast); sqlite callers bootstrap via Store.EnsureSchema at startup instead
// (idempotent CREATE TABLE IF NOT EXISTS, safe for the gateway/jobs double
// process).
func EnsureReady(ctx context.Context, store *Store) error {
	if store == nil {
		return errors.New("Go metrics store 未启用")
	}
	return store.CheckSchema(ctx)
}
