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

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	defaultInterval      = 15 * time.Second
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
// family. defaultRole is the calling process' role (gateway / jobs) applied
// when JUHE_AI_GO_RUNTIME_METRICS_ROLE is unset; every env name, default and
// bound is identical across processes. The store is disabled unless
// JUHE_AI_GO_RUNTIME_METRICS_STORE selects sqlite or postgres (or
// JUHE_AI_GO_RUNTIME_METRICS_ENABLED=false), so an unconfigured deployment
// neither samples nor errors.
func LoadConfig(getenv func(string) string, defaultRole string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	store := strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_STORE")))
	cfg := Config{Interval: defaultInterval, RetentionDays: defaultRetentionDays, Service: "juhe-ai", Role: defaultRole}
	if store == "" || store == "disabled" || strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_ENABLED")), "false") {
		return cfg, nil
	}
	if store != string(DialectSQLite) && store != string(DialectPostgres) {
		return Config{}, fmt.Errorf("JUHE_AI_GO_RUNTIME_METRICS_STORE 必须为 sqlite 或 postgres")
	}
	cfg.Enabled = true
	cfg.Store = SQLDialect(store)
	cfg.DatabasePath = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH"))
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL"))
	if cfg.Store == DialectSQLite && cfg.DatabasePath == "" {
		return Config{}, errors.New("sqlite 模式缺少 JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH")
	}
	if cfg.Store == DialectPostgres && cfg.PostgresURL == "" {
		return Config{}, errors.New("postgres 模式缺少 JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL")
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
	if value := strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_ROLE")); value != "" {
		cfg.Role = value
	}
	return cfg, nil
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
	db, err := sql.Open(driver, dsn)
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
// EnsureSchema; the samplers only check).
func EnsureReady(ctx context.Context, store *Store) error {
	if store == nil {
		return errors.New("Go metrics store 未启用")
	}
	return store.CheckSchema(ctx)
}
