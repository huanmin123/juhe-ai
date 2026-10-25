package auditlog

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

const (
	defaultOwnerLease               = 30 * time.Second
	defaultRetentionInterval        = time.Minute
	defaultRetentionBatchSize       = 512
	defaultSuccessHotRetentionHours = 1
	// 2026-09-28 默认开启整改：成功正文默认全量（1.0；原 0.1 采样是"未配置
	// 即降级"的反模式），窗口仍默认 1h 热窗 / 3d 长期保留；失败面本就全量。
	// 显式配置 0..1 仍生效（0 须与 SUCCESS_RETENTION_DAYS=0 联动）。
	defaultSuccessSampleRate    = 1.0
	defaultSuccessRetentionDays = 3
	defaultProblemRetentionDays = 7
	// 2026-10-17 取值收敛 5096→20（BUG-0313 治理批次 3）：5096 对 PG
	// max_connections=100（与 jobs/maintenance/exporter/超管预留共享）是
	// 连接风暴隐患——饱和时审计池可尝试数千连接致全库 FATAL too many
	// clients；审计存储负载形态为异步批量写 + 保留任务 + 管理页查询，
	// 十级并发规模下 20（=2× 平台 idle 上限 10）已 3 倍以上冗余。
	defaultPostgresPoolSize     = 20
	defaultPostgresMaxIdleConns = sqlpool.MaxIdleConns
)

// Config 承载 F3 审计存储与保留策略配置；2026-10-24 存储形态收口后仅有
// PostgreSQL + 普通 blob/hot 文件目录形态（sqlite 专库路径族字段已随
// sqlite 存储形态物理删除）。
type Config struct {
	InstanceID               string
	OwnerLease               time.Duration
	RetentionInterval        time.Duration
	RetentionBatchSize       int
	SuccessHotRetentionHours int
	SuccessSampleRate        float64
	SuccessRetentionDays     int
	ProblemRetentionDays     int
	PayloadBlobDirectory     string
	HotSearchDirectory       string
	PostgresURL              string
	PostgresMaxOpenConns     int
	PostgresMaxIdleConns     int
	PostgresPool             *pgpool.Handle
	BusinessSettingsURL      string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	// 2026-10-24 存储形态收口：JUHE_AI_AUDIT_LOG_STORE 仅剩单一合法形态
	// postgres——整个 env 读取删除（不留解析不留告警）；Mode 恒 ModePostgres。
	postgresURL := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_POSTGRES_URL"))
	// F3 PG 连接池上限为内部常量（2026-09-30 C4 收编：原
	// JUHE_AI_AUDIT_LOG_POSTGRES_MAX_OPEN_CONNS / MAX_IDLE_CONNS 全库五源
	// 零引用，pool 覆盖族旋钮从未被配置，取值固定为下方默认值）。
	postgresMaxOpen := defaultPostgresPoolSize
	postgresMaxIdle := defaultPostgresMaxIdleConns
	// 路径类 env 派生（普通文件目录面；internal/datadir 固定名表）：
	// audit-blob 落 <JUHE_AI_DATA_DIR=./data>/<固定名>，显式配置优先。
	dataDir := datadir.Dir(getenv)
	cfg := Config{
		InstanceID:           strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INSTANCE_ID")),
		PayloadBlobDirectory: datadir.Path(getenv, dataDir, "JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY", datadir.AuditBlobDirectory),
		HotSearchDirectory:   strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY")),
		PostgresURL:          postgresURL,
		PostgresMaxOpenConns: postgresMaxOpen,
		PostgresMaxIdleConns: postgresMaxIdle,
		BusinessSettingsURL:  strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL")),
	}
	if err := sqlpool.ValidatePoolLimits(cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns); err != nil {
		return Config{}, fmt.Errorf("F3 PostgreSQL 连接池配置无效: %w", err)
	}
	if cfg.InstanceID == "" {
		// 2026-09-19 零配置默认：os.Hostname()，空/错退化 "juhe-ai-gateway"。
		cfg.InstanceID = datadir.DefaultInstanceID()
	}
	lease, err := parseDuration("JUHE_AI_AUDIT_LOG_OWNER_LEASE", getenv("JUHE_AI_AUDIT_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || lease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_OWNER_LEASE 必须是不少于 5s 的正 duration")
	}
	cfg.OwnerLease = lease
	retentionInterval, err := parseDuration("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL", getenv("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL"), defaultRetentionInterval)
	if err != nil || retentionInterval < time.Second || retentionInterval > 24*time.Hour {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL 必须为 1s 到 24h 的正 duration")
	}
	cfg.RetentionInterval = retentionInterval
	retentionBatchSize, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE", getenv("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE"), defaultRetentionBatchSize, 1, 5096)
	if err != nil {
		return Config{}, err
	}
	cfg.RetentionBatchSize = retentionBatchSize
	successHotRetentionHours, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS"), defaultSuccessHotRetentionHours, 0, 168)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessHotRetentionHours = successHotRetentionHours
	successSampleRate, err := parseBoundedDecimal("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE"), defaultSuccessSampleRate, 0, 1, 4)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessSampleRate = successSampleRate
	successRetentionDays, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS"), defaultSuccessRetentionDays, 0, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessRetentionDays = successRetentionDays
	problemRetentionDays, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS", getenv("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS"), defaultProblemRetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.ProblemRetentionDays = problemRetentionDays
	if err := cfg.validateRetentionPolicy(); err != nil {
		return Config{}, err
	}
	// F3 只读业务设置连接串：未显式配置时回落主 JUHE_AI_POSTGRES_URL；
	// 仍为空时保持原 fail-fast。
	if cfg.BusinessSettingsURL == "" {
		cfg.BusinessSettingsURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	}
	if cfg.BusinessSettingsURL == "" {
		return Config{}, fmt.Errorf("必须设置 JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL；F3 只能只读业务设置")
	}
	if cfg.PostgresURL == "" {
		// PG 模式 F3 store URL 缺省回退主 JUHE_AI_POSTGRES_URL，
		// 仍为空时按原契约报错。
		cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	}
	if cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("缺少 JUHE_AI_AUDIT_LOG_POSTGRES_URL")
	}
	if cfg.PayloadBlobDirectory == "" {
		return Config{}, fmt.Errorf("缺少 JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY")
	}
	return cfg, nil
}

func parseDuration(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正 duration", name)
	}
	return parsed, nil
}

func parseBoundedInteger(name, value string, fallback, min, max int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) != parsed || parsed < float64(min) || parsed > float64(max) {
		return 0, fmt.Errorf("%s 必须是 %d 到 %d 之间的整数", name, min, max)
	}
	return int(parsed), nil
}

func parseBoundedDecimal(name, value string, fallback, min, max float64, maxDecimals int) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	message := fmt.Sprintf("%s 必须是 %g 到 %g 之间且最多 %d 位小数的数字", name, min, max, maxDecimals)
	if !regexp.MustCompile(`^(?:\d+|\d*\.\d+)$`).MatchString(value) {
		return 0, fmt.Errorf("%s", message)
	}
	if point := strings.IndexByte(value, '.'); point >= 0 && len(value)-point-1 > maxDecimals {
		return 0, fmt.Errorf("%s", message)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s", message)
	}
	return parsed, nil
}

func (cfg Config) validateRetentionPolicy() error {
	if cfg.RetentionInterval < time.Second || cfg.RetentionInterval > 24*time.Hour {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL 必须为 1s 到 24h 的正 duration")
	}
	if cfg.RetentionBatchSize < 1 || cfg.RetentionBatchSize > 5096 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE 必须是 1 到 5096 之间的整数")
	}
	if cfg.SuccessHotRetentionHours < 0 || cfg.SuccessHotRetentionHours > 168 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS 必须是 0 到 168 之间的整数")
	}
	if math.IsNaN(cfg.SuccessSampleRate) || math.IsInf(cfg.SuccessSampleRate, 0) || cfg.SuccessSampleRate < 0 || cfg.SuccessSampleRate > 1 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 必须是 0 到 1 之间且最多 4 位小数的数字")
	}
	if cfg.SuccessRetentionDays < 0 || cfg.SuccessRetentionDays > 3650 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须是 0 到 3650 之间的整数")
	}
	if cfg.ProblemRetentionDays < 1 || cfg.ProblemRetentionDays > 3650 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS 必须是 1 到 3650 之间的整数")
	}
	if (cfg.SuccessSampleRate == 0) != (cfg.SuccessRetentionDays == 0) {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 和 JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须同时为 0 或同时大于 0")
	}
	if cfg.SuccessRetentionDays > 0 && cfg.SuccessRetentionDays*24 < cfg.SuccessHotRetentionHours {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须覆盖 JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS")
	}
	return nil
}

// RetentionConfigAt derives F3 cleanup cutoffs from retention settings the Go
// audit-log owner reads directly. The zero-success-retention mode
// intentionally falls back to the hot window.
func (cfg Config) RetentionConfigAt(now time.Time) RetentionConfig {
	now = now.UTC()
	successCutoff := now.Add(-time.Duration(cfg.SuccessHotRetentionHours) * time.Hour)
	if cfg.SuccessRetentionDays > 0 {
		successCutoff = now.Add(-time.Duration(cfg.SuccessRetentionDays) * 24 * time.Hour)
	}
	problemCutoff := now.Add(-time.Duration(cfg.ProblemRetentionDays) * 24 * time.Hour)
	return RetentionConfig{
		SuccessHotCutoff:             now.Add(-time.Duration(cfg.SuccessHotRetentionHours) * time.Hour),
		SuccessCutoff:                successCutoff,
		FailureCutoff:                problemCutoff,
		ErrorGroupCutoff:             problemCutoff,
		SuccessSampleBucketThreshold: int(math.Round(cfg.SuccessSampleRate * 10000)),
		BatchSize:                    cfg.RetentionBatchSize,
	}
}
