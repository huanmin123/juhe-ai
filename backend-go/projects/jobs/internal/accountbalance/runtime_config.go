package accountbalance

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

const (
	defaultAccountBalanceConcurrency       = 1000
	defaultAccountBalanceBatchSize         = 1000
	defaultAccountBalanceRecoveryBatchSize = 100
	maxAccountBalanceWorkItems             = 1_000_000
)

// RuntimeConfig is deliberately opt-in.  The J2 package never claims an
// owner merely because a newer jobs binary is deployed.
type RuntimeConfig struct {
	Enabled                   bool
	OwnerID                   string
	Store                     StoreConfig
	BusinessPostgresURL       string
	CredentialSecret          string
	ManualHTTPSecret          string
	ScanInterval              time.Duration
	OwnerLease                time.Duration
	AccountLease              time.Duration
	InputTTL                  time.Duration
	ProbeTimeout              time.Duration
	CycleBudget               time.Duration
	MaxResponseBytes          int64
	MaxConcurrency            int
	BatchSize                 int
	RecoveryBatchSize         int
	PostgresMaxOpenConns      int
	PostgresMaxIdleConns      int
	InputPostgresMaxOpenConns int
	InputPostgresMaxIdleConns int
	PostgresPool              *pgpool.Handle
	InputPostgresPool         *pgpool.Handle
	Now                       func() time.Time
}

func LoadRuntimeConfig(getenv func(string) string) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := RuntimeConfig{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_ENABLED")), "true"), Now: time.Now}
	if !cfg.Enabled {
		return cfg, nil
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER")), "go") {
		return RuntimeConfig{}, errors.New("J2 只有显式 JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER=go 才能启动")
	}
	cfg.OwnerID = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_OWNER_ID"))
	if cfg.OwnerID == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_ACCOUNT_BALANCE_OWNER_ID 是必填配置")
	}
	mode := StoreMode(strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_STORE"))))
	if mode != StorePostgres {
		return RuntimeConfig{}, errors.New("J2 Go owner 只允许 JUHE_AI_ACCOUNT_BALANCE_STORE=postgres；SQLite outcome 不能由 Node projector 接管")
	}
	cfg.Store = StoreConfig{Mode: mode, DatabasePath: strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_DATABASE_PATH")), PostgresURL: strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL"))}
	if cfg.Store.PostgresURL == "" {
		return RuntimeConfig{}, errors.New("postgres 模式缺少 JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL")
	}
	var err error
	if cfg.PostgresMaxOpenConns, err = runtimePositiveInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_OPEN_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.PostgresMaxIdleConns, err = runtimePositiveInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_IDLE_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputPostgresMaxOpenConns, err = runtimePositiveInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_OPEN_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputPostgresMaxIdleConns, err = runtimePositiveInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL"))
	if cfg.BusinessPostgresURL == "" {
		return RuntimeConfig{}, errors.New("J2 direct input 缺少 JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL")
	}
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET 是必填配置")
	}
	cfg.ManualHTTPSecret = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET"))
	if len(cfg.ManualHTTPSecret) < 32 {
		return RuntimeConfig{}, errors.New("JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET 至少需要 32 个字符")
	}
	if cfg.ScanInterval, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_SCAN_INTERVAL", time.Minute, 5*time.Second); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.OwnerLease, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE", 5*time.Minute, 15*time.Second); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.AccountLease, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE", 30*time.Second, time.Second); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputTTL, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_INPUT_TTL", 15*time.Minute, time.Minute); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.ProbeTimeout, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT", 15*time.Second, time.Second); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.CycleBudget, err = runtimeDuration(getenv, "JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET", 45*time.Second, cfg.ProbeTimeout); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.OwnerLease <= cfg.ProbeTimeout {
		return RuntimeConfig{}, errors.New("J2 owner lease 必须大于 probe timeout")
	}
	if cfg.OwnerLease <= cfg.CycleBudget {
		return RuntimeConfig{}, errors.New("J2 owner lease 必须大于 cycle budget")
	}
	if cfg.AccountLease <= cfg.ProbeTimeout {
		return RuntimeConfig{}, errors.New("J2 account lease 必须大于 probe timeout")
	}
	if cfg.MaxResponseBytes, err = runtimeInt64(getenv, "JUHE_AI_ACCOUNT_BALANCE_MAX_RESPONSE_BYTES", defaultMaxBodyBytes, 1, defaultMaxBodyBytes); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.MaxConcurrency, err = runtimeInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY", defaultAccountBalanceConcurrency, 1, maxAccountBalanceWorkItems); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.BatchSize, err = runtimeInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE", defaultAccountBalanceBatchSize, 1, maxAccountBalanceWorkItems); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.RecoveryBatchSize, err = runtimeInt(getenv, "JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE", defaultAccountBalanceRecoveryBatchSize, 1, cfg.BatchSize); err != nil {
		return RuntimeConfig{}, err
	}
	waves := (cfg.BatchSize + cfg.MaxConcurrency - 1) / cfg.MaxConcurrency
	if cfg.OwnerLease < time.Duration(waves)*cfg.ProbeTimeout+10*time.Second {
		return RuntimeConfig{}, errors.New("J2 owner lease 必须覆盖一轮 batch 的最坏 probe 时间")
	}
	return cfg, nil
}

func runtimeDuration(getenv func(string) string, name string, fallback, minimum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s 必须是不小于 %s 的 duration", name, minimum)
	}
	return parsed, nil
}
func runtimeInt(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
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
func runtimeInt64(getenv func(string) string, name string, fallback, minimum, maximum int64) (int64, error) {
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

func runtimePositiveInt(getenv func(string) string, name string, fallback int) (int, error) {
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
