package proxylatency

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

const (
	defaultProxyLatencyInterval     = time.Minute
	defaultProxyLatencyOwnerLease   = 90 * time.Second
	defaultProxyLatencyProxyLease   = 75 * time.Second
	defaultProxyLatencyBatchSize    = 20
	defaultProxyLatencyPoolFactor   = 4
	defaultProxyLatencyConcurrency  = 4
	defaultProxyLatencyProbeLimit   = defaultProxyLatencyBatchSize * defaultProxyLatencyPoolFactor
	defaultProxyLatencyProbeTimeout = 30 * time.Second
)

// RuntimeConfig is opt-in. Disabled configuration deliberately does not
// validate or open any database, so upgrading the jobs binary cannot claim
// J3a ownership accidentally.
type RuntimeConfig struct {
	Enabled                   bool
	InstanceID                string
	Store                     StoreConfig
	BusinessPostgresURL       string
	PostgresMaxOpenConns      int
	PostgresMaxIdleConns      int
	InputPostgresMaxOpenConns int
	InputPostgresMaxIdleConns int
	PostgresPool              *pgpool.Handle
	InputPostgresPool         *pgpool.Handle
	InputLimit                int
	BatchSize                 int
	CandidatePoolFactor       int
	WorkerConcurrency         int
	InputTTL                  time.Duration
	Interval                  time.Duration
	OwnerLease                time.Duration
	ProxyLease                time.Duration
	ProbeTimeout              time.Duration
	CredentialSecret          string
	ManualEnabled             bool
	ManualHTTPSecret          string
	ManualDeadline            time.Duration
	Now                       func() time.Time
}

func LoadRuntimeConfig(getenv func(string) string) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := RuntimeConfig{Now: time.Now}
	cfg.Enabled = strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_ENABLED")), "true")
	if !cfg.Enabled {
		return cfg, nil
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_JOBS_OWNER")), "go") {
		return RuntimeConfig{}, errors.New("启用 J3a 时 JUHE_AI_PROXY_LATENCY_JOBS_OWNER 必须明确为 go")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_PROXY_LATENCY_INSTANCE_ID 是必填配置")
	}
	mode := StoreMode(strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_STORE"))))
	if mode != StorePostgres {
		return RuntimeConfig{}, errors.New("J3a runtime 只允许 postgres jobs store")
	}
	cfg.Store = StoreConfig{Mode: mode, PostgresURL: strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_POSTGRES_URL"))}
	if cfg.Store.PostgresURL == "" {
		return RuntimeConfig{}, errors.New("启用 J3a 时缺少 JUHE_AI_PROXY_LATENCY_POSTGRES_URL")
	}
	var err error
	if cfg.PostgresMaxOpenConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_OPEN_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.PostgresMaxIdleConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_IDLE_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputPostgresMaxOpenConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_MAX_OPEN_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputPostgresMaxIdleConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_MAX_IDLE_CONNS", 1000); err != nil {
		return RuntimeConfig{}, err
	}
	cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL"))
	if cfg.BusinessPostgresURL == "" {
		return RuntimeConfig{}, errors.New("启用 J3a 时缺少 JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL")
	}
	if cfg.InputLimit, err = runtimeInt(getenv, "JUHE_AI_PROXY_LATENCY_INPUT_LIMIT", defaultProxyLatencyProbeLimit, 1, 1024); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.BatchSize, err = runtimeInt(getenv, "JUHE_AI_PROXY_LATENCY_BATCH_SIZE", defaultProxyLatencyBatchSize, 1, 1024); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.CandidatePoolFactor, err = runtimeInt(getenv, "JUHE_AI_PROXY_LATENCY_CANDIDATE_POOL_FACTOR", defaultProxyLatencyPoolFactor, 1, 100); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.WorkerConcurrency, err = runtimeInt(getenv, "JUHE_AI_PROXY_LATENCY_WORKER_CONCURRENCY", defaultProxyLatencyConcurrency, 1, 128); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.InputTTL, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_INPUT_TTL", 5*time.Minute, time.Minute, 15*time.Minute); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.Interval, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_INTERVAL", defaultProxyLatencyInterval, time.Second, 24*time.Hour); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.OwnerLease, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_OWNER_LEASE", defaultProxyLatencyOwnerLease, 5*time.Second, 24*time.Hour); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.ProxyLease, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_PROXY_LEASE", defaultProxyLatencyProxyLease, time.Second, 24*time.Hour); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.ProbeTimeout, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_PROBE_TIMEOUT", defaultProxyLatencyProbeTimeout, time.Second, 15*time.Minute); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.OwnerLease <= cfg.ProbeTimeout || cfg.OwnerLease <= cfg.ProxyLease {
		return RuntimeConfig{}, errors.New("J3a owner lease 必须大于 probe/proxy lease")
	}
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		return RuntimeConfig{}, errors.New("启用 J3a 时缺少 JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET")
	}
	cfg.ManualEnabled = strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_MANUAL_ENABLED")), "true")
	if cfg.ManualEnabled {
		cfg.ManualHTTPSecret = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_MANUAL_HTTP_SECRET"))
		if len(cfg.ManualHTTPSecret) < 32 {
			return RuntimeConfig{}, errors.New("启用 J3a manual bridge 时 JUHE_AI_PROXY_LATENCY_MANUAL_HTTP_SECRET 至少 32 字符")
		}
		if cfg.ManualDeadline, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_MANUAL_DEADLINE", 25*time.Second, time.Second, 25*time.Second); err != nil {
			return RuntimeConfig{}, err
		}
	}
	return cfg, nil
}

func runtimeDuration(getenv func(string) string, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New(name + " 必须是合法 duration")
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
		return 0, errors.New(name + " 必须在有效范围内")
	}
	return parsed, nil
}

func positiveInt(getenv func(string) string, name string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, errors.New(name + " 必须是正整数")
	}
	return parsed, nil
}
