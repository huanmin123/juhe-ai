package gometricsstore

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

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	defaultInterval      = 15 * time.Second
	defaultRetentionDays = 30
)

type Config struct {
	Enabled       bool
	Store         gometrics.SQLDialect
	DatabasePath  string
	PostgresURL   string
	Interval      time.Duration
	RetentionDays int
	Service       string
	Role          string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	store := strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_STORE")))
	cfg := Config{Interval: defaultInterval, RetentionDays: defaultRetentionDays, Service: "juhe-ai", Role: "jobs"}
	if store == "" || store == "disabled" || strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_ENABLED")), "false") {
		return cfg, nil
	}
	if store != string(gometrics.DialectSQLite) && store != string(gometrics.DialectPostgres) {
		return Config{}, fmt.Errorf("JUHE_AI_GO_RUNTIME_METRICS_STORE 必须为 sqlite 或 postgres")
	}
	cfg.Enabled = true
	cfg.Store = gometrics.SQLDialect(store)
	cfg.DatabasePath = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH"))
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL"))
	if cfg.Store == gometrics.DialectSQLite && cfg.DatabasePath == "" {
		return Config{}, errors.New("sqlite 模式缺少 JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH")
	}
	if cfg.Store == gometrics.DialectPostgres && cfg.PostgresURL == "" {
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

func OpenStore(cfg Config) (*gometrics.Store, *sql.DB, error) {
	if !cfg.Enabled {
		return nil, nil, nil
	}
	driver, dsn := "sqlite", cfg.DatabasePath
	if cfg.Store == gometrics.DialectPostgres {
		driver, dsn = "pgx", cfg.PostgresURL
	}
	if cfg.Store == gometrics.DialectSQLite {
		if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o755); err != nil {
			return nil, nil, err
		}
		dsn = "file:" + cfg.DatabasePath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open Go metrics database: %w", err)
	}
	store, err := gometrics.NewStore(db, cfg.Store)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return store, db, nil
}

func ParseUnixOrRFC3339(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(seconds, 0).UTC(), nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func EnsureReady(ctx context.Context, store *gometrics.Store) error {
	if store == nil {
		return errors.New("Go metrics store 未启用")
	}
	return store.CheckSchema(ctx)
}
