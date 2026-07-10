package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	env "github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	Host                          string        `env:"JUHE_AI_HOST" envDefault:"127.0.0.1"`
	Port                          int           `env:"JUHE_AI_PORT" envDefault:"3000"`
	Env                           string        `env:"JUHE_AI_ENV" envDefault:"development"`
	LogLevel                      string        `env:"JUHE_AI_LOG_LEVEL" envDefault:"info"`
	PostgresURL                   string        `env:"JUHE_AI_POSTGRES_URL"`
	RedisCacheURL                 string        `env:"JUHE_AI_REDIS_CACHE_URL"`
	RedisStateURL                 string        `env:"JUHE_AI_REDIS_STATE_URL"`
	RedisQueueURL                 string        `env:"JUHE_AI_REDIS_QUEUE_URL"`
	RedisNamespace                string        `env:"JUHE_AI_REDIS_NAMESPACE" envDefault:"juhe-ai"`
	Secret                        string        `env:"JUHE_AI_SECRET"`
	PublicAPIEnabled              bool          `env:"JUHE_AI_PUBLIC_API_ENABLED" envDefault:"false"`
	ManagementAPIEnabled          bool          `env:"JUHE_AI_MANAGEMENT_API_ENABLED" envDefault:"false"`
	ManagementAuthSessionsEnabled bool          `env:"JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED" envDefault:"false"`
	TrustProxy                    string        `env:"JUHE_AI_TRUST_PROXY" envDefault:"false"`
	CookieSecure                  bool          `env:"JUHE_AI_COOKIE_SECURE" envDefault:"false"`
	CookieSameSite                string        `env:"JUHE_AI_COOKIE_SAME_SITE" envDefault:"lax"`
	MetricsEnabled                bool          `env:"JUHE_AI_METRICS_ENABLED" envDefault:"false"`
	PprofEnabled                  bool          `env:"JUHE_AI_PPROF_ENABLED" envDefault:"false"`
	ShutdownTimeout               time.Duration `env:"JUHE_AI_SHUTDOWN_TIMEOUT" envDefault:"15s"`
}

type TrustProxyConfig struct {
	Enabled  bool
	TrustAll bool
	Hops     int
}

type LoadOptions struct {
	LoadDotEnv bool
}

func Load(opts LoadOptions) (Config, error) {
	if opts.LoadDotEnv && os.Getenv("JUHE_AI_ENV") != "production" {
		_ = godotenv.Load()
	}

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("读取 Go 后端环境变量失败: %w", err)
	}
	applyProductionCookieDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyProductionCookieDefaults(cfg *Config) {
	if strings.EqualFold(strings.TrimSpace(cfg.Env), "production") && os.Getenv("JUHE_AI_COOKIE_SECURE") == "" {
		cfg.CookieSecure = true
	}
}

func (cfg Config) Validate() error {
	if cfg.Host == "" {
		return fmt.Errorf("JUHE_AI_HOST 不能为空")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("JUHE_AI_PORT 必须在 1 到 65535 之间")
	}
	if cfg.ShutdownTimeout <= 0 {
		return fmt.Errorf("JUHE_AI_SHUTDOWN_TIMEOUT 必须大于 0")
	}
	if strings.TrimSpace(cfg.RedisNamespace) == "" {
		return fmt.Errorf("JUHE_AI_REDIS_NAMESPACE 不能为空")
	}
	if _, err := cfg.TrustProxyConfig(); err != nil {
		return err
	}
	if _, err := cfg.CookieSameSiteMode(); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(cfg.CookieSameSite), "none") && !cfg.CookieSecure {
		return fmt.Errorf("JUHE_AI_COOKIE_SAME_SITE=none 时必须启用 JUHE_AI_COOKIE_SECURE=true")
	}
	if err := validatePublicAPIConfig(cfg); err != nil {
		return err
	}
	if err := validateManagementAPIConfig(cfg); err != nil {
		return err
	}
	if err := validateDistinctRedisURLs(cfg); err != nil {
		return err
	}
	return nil
}

func (cfg Config) Address() string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

func (cfg Config) TrustProxyConfig() (TrustProxyConfig, error) {
	value := strings.ToLower(strings.TrimSpace(cfg.TrustProxy))
	if value == "" {
		value = "false"
	}
	switch value {
	case "true", "yes", "on":
		return TrustProxyConfig{Enabled: true, TrustAll: true}, nil
	case "false", "no", "off":
		return TrustProxyConfig{}, nil
	}

	hops, err := strconv.Atoi(value)
	if err != nil || hops < 0 || hops > 16 {
		return TrustProxyConfig{}, fmt.Errorf("JUHE_AI_TRUST_PROXY 只能配置为 true/false 或 0-16 的反向代理跳数")
	}
	if hops == 0 {
		return TrustProxyConfig{}, nil
	}
	return TrustProxyConfig{Enabled: true, Hops: hops}, nil
}

func (cfg Config) CookieSameSiteMode() (string, error) {
	value := strings.ToLower(strings.TrimSpace(cfg.CookieSameSite))
	if value == "" {
		value = "lax"
	}
	switch value {
	case "lax", "strict", "none":
		return value, nil
	default:
		return "", fmt.Errorf("JUHE_AI_COOKIE_SAME_SITE 只能配置为 lax、strict 或 none")
	}
}

func validateDistinctRedisURLs(cfg Config) error {
	type namedURL struct {
		name string
		raw  string
	}

	seen := map[string]string{}
	for _, item := range []namedURL{
		{name: "JUHE_AI_REDIS_CACHE_URL", raw: cfg.RedisCacheURL},
		{name: "JUHE_AI_REDIS_STATE_URL", raw: cfg.RedisStateURL},
		{name: "JUHE_AI_REDIS_QUEUE_URL", raw: cfg.RedisQueueURL},
	} {
		if item.raw == "" {
			continue
		}
		identity, err := redisIdentity(item.raw)
		if err != nil {
			return fmt.Errorf("%s 无效: %w", item.name, err)
		}
		if prev, ok := seen[identity]; ok {
			return fmt.Errorf("%s 不能与 %s 指向同一个 Redis DB", item.name, prev)
		}
		seen[identity] = item.name
	}
	return nil
}

func validatePublicAPIConfig(cfg Config) error {
	if !cfg.PublicAPIEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		return fmt.Errorf("启用 JUHE_AI_PUBLIC_API_ENABLED 时 JUHE_AI_REDIS_STATE_URL 不能为空")
	}
	if strings.TrimSpace(cfg.RedisCacheURL) == "" {
		return fmt.Errorf("启用 JUHE_AI_PUBLIC_API_ENABLED 时 JUHE_AI_REDIS_CACHE_URL 不能为空")
	}
	if strings.TrimSpace(cfg.RedisQueueURL) == "" {
		return fmt.Errorf("启用 JUHE_AI_PUBLIC_API_ENABLED 时 JUHE_AI_REDIS_QUEUE_URL 不能为空")
	}
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return fmt.Errorf("启用 JUHE_AI_PUBLIC_API_ENABLED 时 JUHE_AI_SECRET 不能为空")
	}
	if len([]rune(secret)) < 32 {
		return fmt.Errorf("JUHE_AI_SECRET 至少需要 32 个字符")
	}
	return nil
}

func validateManagementAPIConfig(cfg Config) error {
	if !cfg.ManagementAPIEnabled && !cfg.ManagementAuthSessionsEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		return fmt.Errorf("启用 Go 管理端接口时 JUHE_AI_REDIS_STATE_URL 不能为空")
	}
	if !cfg.ManagementAPIEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.RedisQueueURL) == "" {
		return fmt.Errorf("启用 JUHE_AI_MANAGEMENT_API_ENABLED 时 JUHE_AI_REDIS_QUEUE_URL 不能为空")
	}
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return fmt.Errorf("启用 JUHE_AI_MANAGEMENT_API_ENABLED 时 JUHE_AI_SECRET 不能为空")
	}
	if len([]rune(secret)) < 32 {
		return fmt.Errorf("JUHE_AI_SECRET 至少需要 32 个字符")
	}
	return nil
}

func redisIdentity(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return "", fmt.Errorf("unsupported redis scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("redis host is required")
	}

	db := strings.Trim(parsed.Path, "/")
	if db == "" {
		db = "0"
	}
	if _, err := strconv.Atoi(db); err != nil {
		return "", fmt.Errorf("invalid redis db: %w", err)
	}

	port := parsed.Port()
	if port == "" {
		port = "6379"
	}

	host := strings.ToLower(parsed.Hostname())
	return net.JoinHostPort(host, port) + "/" + db, nil
}
