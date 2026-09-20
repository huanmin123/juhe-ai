package accounthealth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

const (
	defaultScanInterval     = 5 * time.Second
	defaultOwnerLease       = 90 * time.Second
	defaultProbeTimeout     = 65 * time.Second
	defaultMaxResponseBytes = int64(256 * 1024)
	// defaultInputTTL 对齐 Node runtime.ts:1345（D-208，BUG-0175）：
	// integerConfig('JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS', 24*60*60_000,
	// 60_000, 7*24*60*60_000)。Go 曾误取 15 分钟——未显式配置 env 的部署
	// 过期窗口比 Node 缩短 96%，签名 input 会提前失效。
	defaultInputTTL               = 24 * time.Hour
	defaultDirectInputLimit       = 512
	defaultPostgresPoolSize       = 16
	defaultPostgresMaxIdleConns   = sqlpool.MaxIdleConns
	defaultSQLiteConcurrency      = 512
	defaultPerformanceConcurrency = 512
	defaultPostgresConcurrency    = 512
	defaultDBConcurrency          = 16
	defaultDBQueueSize            = 512
	minJ1Concurrency              = 1
	maxJ1Concurrency              = 5096
	maxJ1Capacity                 = 5096
)

// Config 是 J1 账户健康机制的终态配置：机制强制常开（2026-09-19 产品决策），
// LoadConfig 恒走完整校验路径、缺失必填项即 fail-fast。原「deliberately
// opt-in」开关（JUHE_AI_ACCOUNT_HEALTH_ENABLED）的防双 owner 理由已随 Node
// 后端归档（migration-backup/node/final-archive）失效，开关与 Config.Enabled
// 字段一并移除。
type Config struct {
	InstanceID                      string
	Store                           StoreConfig
	InputDirectory                  string
	InputKeys                       map[string][]byte
	InputSource                     string
	BusinessPostgresURL             string
	BusinessSQLitePath              string
	StatsSQLitePath                 string
	DirectInputLimit                int
	DirectInputPostgresMaxOpenConns int
	DirectInputPostgresMaxIdleConns int
	CredentialSecret                string
	InputTTL                        time.Duration
	ScanInterval                    time.Duration
	OwnerLease                      time.Duration
	ProbeTimeout                    time.Duration
	MaxResponseBytes                int64
	MaxConcurrency                  int
	IOConcurrency                   int
	DBConcurrency                   int
	DBQueueSize                     int
	Now                             func() time.Time
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{Now: time.Now}
	// OWNER 缺省视为 go；显式设置非 go 值（trim + 大小写不敏感）拒绝。
	if owner := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER")); owner != "" && !strings.EqualFold(owner, "go") {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER 仅支持 go")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		hostname, hostErr := os.Hostname()
		if hostErr != nil || strings.TrimSpace(hostname) == "" {
			hostname = "juhe-ai-jobs"
		}
		cfg.InstanceID = hostname
	}
	var err error
	// STORE 缺省跟随 JUHE_AI_DATABASE_DRIVER（与 jobs 组合根 loadWorkerConfig
	// 同一依据，2026-09-19 零配置决策）：postgres → StorePostgres，否则
	// StoreSQLite；显式配置仍必须为 sqlite|postgres。
	mode := StoreMode(strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_STORE"))))
	if mode == "" {
		if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")), "postgres") {
			mode = StorePostgres
		} else {
			mode = StoreSQLite
		}
	}
	if mode != StoreSQLite && mode != StorePostgres {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_STORE 必须为 sqlite 或 postgres")
	}
	// sqlite 库路径按 DATA_DIR 约定派生（account-health.sqlite3）；PG 连接串
	// 无法凭空默认：显式 JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL 优先，缺省回退
	// worker/gateway 共用的 JUHE_AI_POSTGRES_URL，PG 模式仍空则 fail-fast。
	storePath := datadir.Path(getenv, "JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH", "account-health.sqlite3")
	postgresURL := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"))
	if postgresURL == "" {
		postgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	}
	cfg.Store = StoreConfig{Mode: mode, DatabasePath: storePath, PostgresURL: postgresURL}
	if mode == StoreSQLite && strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH")) == "" {
		// 派生路径的父目录由配置装载负责创建（显式配置保持既有语义，不代建）。
		if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
			return Config{}, fmt.Errorf("创建 J1 SQLite store 目录失败: %w", err)
		}
	}
	if mode == StorePostgres && cfg.Store.PostgresURL == "" {
		return Config{}, errors.New("postgres 模式缺少 JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL（或 JUHE_AI_POSTGRES_URL）")
	}
	if mode == StorePostgres {
		if cfg.Store.PostgresMaxOpenConns, err = configPositiveInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS", defaultPostgresPoolSize); err != nil {
			return Config{}, err
		}
		if cfg.Store.PostgresMaxIdleConns, err = configPositiveInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS", defaultPostgresMaxIdleConns); err != nil {
			return Config{}, err
		}
		if err := sqlpool.ValidatePoolLimits(cfg.Store.PostgresMaxOpenConns, cfg.Store.PostgresMaxIdleConns); err != nil {
			return Config{}, fmt.Errorf("J1 jobs PostgreSQL 连接池配置无效: %w", err)
		}
	}
	cfg.InputDirectory = datadir.Path(getenv, "JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", "account-health-input")
	if strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY")) == "" {
		// 派生 input 目录在使用前创建；显式配置保持既有语义，不代建。
		if err := os.MkdirAll(cfg.InputDirectory, 0o755); err != nil {
			return Config{}, fmt.Errorf("创建 J1 input 目录失败: %w", err)
		}
	}
	if mode == StoreSQLite {
		if err := validateSQLiteIsolation(cfg.Store.DatabasePath, cfg.InputDirectory, getenv); err != nil {
			return Config{}, err
		}
	}
	cfg.InputSource = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE")))
	if cfg.InputSource == "" {
		// 缺省跟随 store 模式：PG store 直读业务库（2026-09-19 零配置决策）；
		// sqlite store 自 2026-09 起改为直读业务/统计 SQLite 库
		// （SQLiteDirectInputReader）——原签名文件通道的发布方已随 Node 归档、
		// 目录恒空导致导入账户永停 pending_test，故降级为显式 files 后备。
		if mode == StorePostgres {
			cfg.InputSource = "postgres"
		} else {
			cfg.InputSource = "sqlite"
		}
	}
	if cfg.InputSource != "files" && cfg.InputSource != "postgres" && cfg.InputSource != "sqlite" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE 必须为 files、postgres 或 sqlite")
	}
	// SQLite 直读路径按 DATA_DIR 约定派生（与 worker/gateway 同名 env 同固定
	// 名），不新增 env 名；不做文件存在性校验——业务库缺失由组合根打开/预检
	// 时 fail-fast。
	cfg.BusinessSQLitePath = datadir.Path(getenv, "JUHE_AI_DATABASE_PATH", "business.sqlite3")
	cfg.StatsSQLitePath = datadir.Path(getenv, "JUHE_AI_STATS_DATABASE_PATH", "stats.sqlite3")
	if cfg.InputSource == "sqlite" && mode != StoreSQLite {
		return Config{}, errors.New("sqlite direct input 只允许与 sqlite jobs store 一起启用")
	}
	if cfg.DirectInputLimit, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT", defaultDirectInputLimit, 1, maxJ1Capacity); err != nil {
		return Config{}, err
	}
	if cfg.InputSource == "postgres" {
		if mode != StorePostgres {
			return Config{}, errors.New("PG direct input 只允许与 postgres jobs store 一起启用")
		}
		cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"))
		if cfg.BusinessPostgresURL == "" {
			return Config{}, errors.New("postgres direct input 缺少 JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL")
		}
		if cfg.DirectInputPostgresMaxOpenConns, err = configPositiveInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS", defaultPostgresPoolSize); err != nil {
			return Config{}, err
		}
		if cfg.DirectInputPostgresMaxIdleConns, err = configPositiveInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS", defaultPostgresMaxIdleConns); err != nil {
			return Config{}, err
		}
		if err := sqlpool.ValidatePoolLimits(cfg.DirectInputPostgresMaxOpenConns, cfg.DirectInputPostgresMaxIdleConns); err != nil {
			return Config{}, fmt.Errorf("J1 业务读取 PostgreSQL 连接池配置无效: %w", err)
		}
	}
	var key []byte
	keyText := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"))
	if keyText == "" {
		// 签名密钥缺省落 <DATA_DIR>/account-health-input.key（2026-09-19 零配置
		// 决策）：不存在则生成 48 字节随机密钥、base64rawurl 编码写入（权限
		// 0600，Windows 下尽最大努力）；存在则复用，进程重启后密钥保持稳定。
		// 显式 env 始终优先；文件读写失败 fail-fast，不静默换 key。
		loaded, keyErr := loadOrCreateInputSigningKey(filepath.Join(datadir.Root(getenv), "account-health-input.key"))
		if keyErr != nil {
			return Config{}, keyErr
		}
		key = loaded
	} else {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(keyText)
		if decodeErr != nil || len(decoded) < 32 {
			return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY 必须是至少 32 字节的 base64url")
		}
		key = decoded
	}
	keyID := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY_ID"))
	if keyID == "" {
		keyID = "runtime-v1"
	}
	cfg.InputKeys = map[string][]byte{keyID: key}
	// 凭据封套密钥：显式 CREDENTIAL_SECRET 优先；缺省回退 JUHE_AI_SECRET；
	// 两者均空时非生产回退与 gateway defaultRuntimeSecret 同值的开发密钥
	// （探针解密凭据要求与 gateway 加密侧同值），production 仍 fail-fast。
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
	}
	if cfg.CredentialSecret == "" {
		if strings.EqualFold(strings.TrimSpace(getenv("NODE_ENV")), "production") {
			return Config{}, errors.New("production 模式必须配置 JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET 或 JUHE_AI_SECRET（凭据封套密钥）")
		}
		cfg.CredentialSecret = "juhe-ai-dev-secret-change-me"
	}
	if cfg.InputTTL, err = configMilliseconds(getenv, "JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS", defaultInputTTL, time.Minute, 7*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.ScanInterval, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL", defaultScanInterval, 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OwnerLease, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE", defaultOwnerLease, 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ProbeTimeout, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT", defaultProbeTimeout, time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OwnerLease <= cfg.ProbeTimeout {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE 必须大于 JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT")
	}
	if cfg.MaxResponseBytes, err = configInt64(getenv, "JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES", defaultMaxResponseBytes, 1, 16*1024*1024); err != nil {
		return Config{}, err
	}
	concurrencyDefault := defaultSQLiteConcurrency
	if mode == StorePostgres {
		concurrencyDefault = defaultPostgresConcurrency
	} else if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_RUNTIME_MODE")), "performance") {
		concurrencyDefault = defaultPerformanceConcurrency
	}
	if cfg.MaxConcurrency, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY", concurrencyDefault, minJ1Concurrency, maxJ1Concurrency); err != nil {
		return Config{}, err
	}
	if cfg.IOConcurrency, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_IO_CONCURRENCY", cfg.MaxConcurrency, minJ1Concurrency, maxJ1Concurrency); err != nil {
		return Config{}, err
	}
	if cfg.DBConcurrency, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_DB_CONCURRENCY", defaultDBConcurrency, minJ1Concurrency, maxJ1Concurrency); err != nil {
		return Config{}, err
	}
	if cfg.DBQueueSize, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_DB_QUEUE_SIZE", defaultDBQueueSize, minJ1Concurrency, maxJ1Capacity); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadOrCreateInputSigningKey 读取派生签名密钥文件；首次使用时生成 48 字节
// 随机密钥并以 base64rawurl 写入（0600，Windows 权限尽力而为），随后读取
// 复用。文件存在但内容无效、读写失败均返回错误（fail-fast，不静默换 key）。
func loadOrCreateInputSigningKey(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		key, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decodeErr != nil || len(key) < 32 {
			return nil, fmt.Errorf("J1 input 签名密钥文件 %s 内容无效（须为至少 32 字节的 base64rawurl）", path)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 J1 input 签名密钥文件 %s 失败: %w", path, err)
	}
	buffer := make([]byte, 48)
	if _, err := rand.Read(buffer); err != nil {
		return nil, fmt.Errorf("生成 J1 input 签名密钥失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建 J1 input 签名密钥目录失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(buffer)), 0o600); err != nil {
		return nil, fmt.Errorf("写入 J1 input 签名密钥文件 %s 失败: %w", path, err)
	}
	return buffer, nil
}

func validateSQLiteIsolation(storePath, inputDirectory string, getenv func(string) string) error {
	store, err := canonicalPath(storePath)
	if err != nil {
		return fmt.Errorf("解析 J1 SQLite store 路径失败: %w", err)
	}
	for _, name := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH"} {
		other := strings.TrimSpace(getenv(name))
		if other == "" {
			continue
		}
		candidate, err := canonicalPath(other)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", name, err)
		}
		if equalPath(store, candidate) {
			return fmt.Errorf("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH 不得与 %s 共用 SQLite 文件", name)
		}
	}
	input, err := canonicalPath(inputDirectory)
	if err != nil {
		return fmt.Errorf("解析 J1 input 目录失败: %w", err)
	}
	relative, err := filepath.Rel(input, store)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH 不得放入 input 目录")
	}
	return nil
}

func canonicalPath(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func equalPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func configDuration(getenv func(string) string, name string, fallback, minimum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s 必须是不少于 %s 的 duration", name, minimum)
	}
	return parsed, nil
}

func configInt(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s 必须在 %d..%d", name, minimum, maximum)
	}
	return parsed, nil
}

func configPositiveInt(getenv func(string) string, name string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return parsed, nil
}

func configInt64(getenv func(string) string, name string, fallback, minimum, maximum int64) (int64, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s 必须在 %d..%d", name, minimum, maximum)
	}
	return parsed, nil
}

func configMilliseconds(getenv func(string) string, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds < 1 {
		return 0, fmt.Errorf("%s 必须是有效毫秒数", name)
	}
	if milliseconds > int64(maximum/time.Millisecond) {
		return 0, fmt.Errorf("%s 必须在 %s..%s", name, minimum, maximum)
	}
	result := time.Duration(milliseconds) * time.Millisecond
	if result < minimum || result > maximum {
		return 0, fmt.Errorf("%s 必须在 %s..%s", name, minimum, maximum)
	}
	return result, nil
}
