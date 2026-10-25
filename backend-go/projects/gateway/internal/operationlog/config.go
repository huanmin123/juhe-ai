package operationlog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

// Config 承载 F4 操作日志存储配置；2026-10-24 存储形态收口后仅有
// PostgreSQL 形态（sqlite 专库路径族字段已随 sqlite 存储形态物理删除）。
type Config struct {
	Enabled              bool
	InstanceID           string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
	OwnerLease           time.Duration
	RetentionInterval    time.Duration
	RetentionDays        int
	RetentionBatchSize   int
}

const (
	defaultOwnerLease           = 30 * time.Second
	defaultRetentionInterval    = time.Minute
	defaultRetentionBatch       = 512
	// 2026-10-17 取值收敛 5096→20（BUG-0313 治理批次 3，与审计池同口径）：
	// F4 负载形态为异步追加+保留任务+管理页查询，5096 对 PG
	// max_connections=100 是连接风暴隐患。
	defaultPostgresPoolSize     = 20
	defaultPostgresMaxIdleConns = sqlpool.MaxIdleConns
)

func LoadConfig(getenv func(string) string) (Config, error) {
	// 2026-10-24 存储形态收口：JUHE_AI_OPERATION_LOG_STORE 仅剩单一合法形态
	// postgres——整个 env 读取删除（不留解析不留告警）；Mode 恒 ModePostgres。
	postgresURL := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_POSTGRES_URL"))
	ownerLease, err := durationOrDefault("JUHE_AI_OPERATION_LOG_OWNER_LEASE", getenv("JUHE_AI_OPERATION_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || ownerLease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_OWNER_LEASE must be a duration of at least 5s")
	}
	retentionInterval, err := durationOrDefault("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL", getenv("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL"), defaultRetentionInterval)
	if err != nil || retentionInterval < time.Second || retentionInterval > 24*time.Hour {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL must be a duration from 1s to 24h")
	}
	retentionBatch, err := intOrDefault("JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE", getenv("JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE"), defaultRetentionBatch, 1, 5096)
	if err != nil {
		return Config{}, err
	}
	// F4 PG 连接池上限为内部常量（2026-09-30 C4 收编：原
	// JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS / MAX_IDLE_CONNS 全库五源
	// 零引用，pool 覆盖族旋钮从未被配置，取值固定为下方默认值）。
	postgresMaxOpen := defaultPostgresPoolSize
	postgresMaxIdle := defaultPostgresMaxIdleConns
	cfg := Config{
		Enabled:              true,
		InstanceID:           strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INSTANCE_ID")),
		PostgresURL:          postgresURL,
		PostgresMaxOpenConns: postgresMaxOpen,
		PostgresMaxIdleConns: postgresMaxIdle,
		OwnerLease:           ownerLease,
		RetentionInterval:    retentionInterval,
		RetentionDays:        365,
		RetentionBatchSize:   retentionBatch,
	}
	if err := sqlpool.ValidatePoolLimits(cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns); err != nil {
		return Config{}, fmt.Errorf("F4 PostgreSQL 连接池配置无效: %w", err)
	}
	if cfg.InstanceID == "" {
		// 2026-09-19 零配置默认：os.Hostname()，空/错退化 "juhe-ai-gateway"。
		cfg.InstanceID = datadir.DefaultInstanceID()
	}
	if cfg.PostgresURL == "" {
		// PG 模式 F4 store URL 缺省回退主 JUHE_AI_POSTGRES_URL，
		// 仍为空时按原契约报错。
		cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	}
	if cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("缺少 JUHE_AI_OPERATION_LOG_POSTGRES_URL")
	}
	return cfg, nil
}

func durationOrDefault(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func intOrDefault(name, value string, fallback, min, max int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, min, max)
	}
	return parsed, nil
}
