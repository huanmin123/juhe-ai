package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUpstreamBaseURLPrivateAllowlistRequiresExactIPOrigins(t *testing.T) {
	valid := Config{UpstreamBaseURLPrivateAllowlist: []string{
		"http://192.168.40.199:8317",
		"https://[fd00::1]",
	}}
	if err := validateUpstreamBaseURLPrivateAllowlist(valid); err != nil {
		t.Fatalf("valid exact IP origins: %v", err)
	}

	for _, value := range []string{
		"192.168.40.199",
		"http://private-upstream.example:8317",
		"http://192.168.40.199:8317/v1",
		"http://user:pass@192.168.40.199:8317",
		"ftp://192.168.40.199:8317",
		"http://192.168.40.199:70000",
	} {
		err := validateUpstreamBaseURLPrivateAllowlist(Config{UpstreamBaseURLPrivateAllowlist: []string{value}})
		if err == nil {
			t.Fatalf("invalid allowlist %q accepted", value)
		}
	}
}

func TestLoadReadsUpstreamBaseURLPrivateAllowlistFromEnvironment(t *testing.T) {
	t.Setenv("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST", "http://192.168.40.199:8317,https://[fd00::1]")
	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if len(cfg.UpstreamBaseURLPrivateAllowlist) != 2 {
		t.Fatalf("UpstreamBaseURLPrivateAllowlist = %v, want two origins", cfg.UpstreamBaseURLPrivateAllowlist)
	}
}

func TestAllowPrivateUpstreamBaseURLsIsExplicitAndForbiddenInProduction(t *testing.T) {
	t.Setenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS", "true")
	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !cfg.AllowPrivateUpstreamBaseURLs {
		t.Fatal("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS was not loaded")
	}
	cfg.Env = "production"
	if err := validateUpstreamBaseURLPrivateAllowlist(cfg); err == nil || !strings.Contains(err.Error(), "只能用于") {
		t.Fatalf("production private upstream override error = %v", err)
	}
}

func TestLoadReadsBoundedRuntimeLogGrepConfiguration(t *testing.T) {
	t.Setenv("JUHE_AI_LOG_DIR", "D:/juhe/logs")
	t.Setenv("JUHE_AI_LOG_FILE_ENABLED", "false")
	t.Setenv("JUHE_AI_LOG_RETENTION_DAYS", "21")
	t.Setenv("JUHE_AI_LOG_MAX_FILES", "123")
	t.Setenv("JUHE_AI_RG_PATH", "D:/tools/rg.exe")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.RuntimeLogDirectory != "D:/juhe/logs" || cfg.RuntimeLogFileEnabled || cfg.RuntimeLogRetentionDays != 21 || cfg.RuntimeLogMaxFiles != 123 || cfg.RGPath != "D:/tools/rg.exe" {
		t.Fatalf("runtime log grep config = %+v", cfg)
	}

	cfg.RuntimeLogMaxFiles = 501
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_MAX_FILES") {
		t.Fatalf("Validate() error = %v", err)
	}
	cfg.RuntimeLogMaxFiles = 123
	cfg.RuntimeLogRetentionDays = 31
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_RETENTION_DAYS") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadReadsOptionalAuditHotSearchRoot(t *testing.T) {
	t.Setenv("JUHE_AI_AUDIT_HOT_SEARCH_ROOT", "D:/juhe/audit/search-hot")
	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.AuditHotSearchRoot != "D:/juhe/audit/search-hot" {
		t.Fatalf("AuditHotSearchRoot=%q", cfg.AuditHotSearchRoot)
	}

	t.Setenv("JUHE_AI_AUDIT_HOT_SEARCH_ROOT", "")
	cfg, err = Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() empty root: %v", err)
	}
	if cfg.AuditHotSearchRoot != "" {
		t.Fatalf("AuditHotSearchRoot=%q, want empty by default", cfg.AuditHotSearchRoot)
	}
}

func TestLoadUsesGoDevelopmentRuntimeLogDirectoryDefault(t *testing.T) {
	if _, configured := os.LookupEnv("JUHE_AI_LOG_DIR"); configured {
		t.Skip("JUHE_AI_LOG_DIR is configured by the test environment")
	}
	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeLogDirectory != "../backend/logs" {
		t.Fatalf("RuntimeLogDirectory=%q", cfg.RuntimeLogDirectory)
	}
}

func TestConfigDefaultsValidate(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		Env:                        "test",
		LogLevel:                   "info",
		RedisNamespace:             "juhe-ai",
		TrustProxy:                 "false",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            15 * time.Second,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if got := cfg.Address(); got != "127.0.0.1:3000" {
		t.Fatalf("Address() = %q", got)
	}
}

func TestTrustProxyConfigDefaultsToDisabled(t *testing.T) {
	cfg := Config{}
	got, err := cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if got.Enabled || got.TrustAll || got.Hops != 0 {
		t.Fatalf("TrustProxyConfig() = %+v, want disabled", got)
	}
}

func TestTrustProxyConfigSupportsBooleanValues(t *testing.T) {
	cfg := Config{TrustProxy: "yes"}
	got, err := cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if !got.Enabled || !got.TrustAll {
		t.Fatalf("TrustProxyConfig() = %+v, want trust all", got)
	}

	cfg.TrustProxy = "off"
	got, err = cfg.TrustProxyConfig()
	if err != nil {
		t.Fatalf("TrustProxyConfig() error = %v", err)
	}
	if got.Enabled {
		t.Fatalf("TrustProxyConfig() = %+v, want disabled", got)
	}
}

func TestTrustProxyConfigSupportsHopCount(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
		hops    int
	}{
		{value: "0", enabled: false, hops: 0},
		{value: "1", enabled: true, hops: 1},
		{value: "2", enabled: true, hops: 2},
		{value: "16", enabled: true, hops: 16},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := Config{TrustProxy: tc.value}
			got, err := cfg.TrustProxyConfig()
			if err != nil {
				t.Fatalf("TrustProxyConfig() error = %v", err)
			}
			if got.Enabled != tc.enabled || got.TrustAll || got.Hops != tc.hops {
				t.Fatalf("TrustProxyConfig() = %+v, want enabled=%v hops=%d", got, tc.enabled, tc.hops)
			}
		})
	}
}

func TestTrustProxyConfigRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"maybe", "-1", "17", "1.5"} {
		t.Run(value, func(t *testing.T) {
			cfg := Config{
				Host:                       "127.0.0.1",
				Port:                       3000,
				RedisNamespace:             "juhe-ai",
				TrustProxy:                 value,
				NodeInternalRequestTimeout: 2 * time.Second,
				ShutdownTimeout:            time.Second,
			}
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid trust proxy error")
			}
		})
	}
}

func TestCookieSameSiteConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{value: "", want: "lax"},
		{value: "lax", want: "lax"},
		{value: "strict", want: "strict"},
		{value: "none", want: "none"},
		{value: " LAX ", want: "lax"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cfg := Config{CookieSameSite: tc.value}
			got, err := cfg.CookieSameSiteMode()
			if err != nil {
				t.Fatalf("CookieSameSiteMode() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("CookieSameSiteMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigRejectsInvalidCookieSameSite(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		CookieSameSite:             "invalid",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid cookie same-site error")
	}
}

func TestConfigRequiresSecureCookieForSameSiteNone(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		Env:                        "development",
		RedisNamespace:             "juhe-ai",
		CookieSameSite:             "none",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development Validate() error = %v", err)
	}

	cfg.Env = "production"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SECURE") {
		t.Fatalf("Validate() error = %v, want secure cookie dependency error", err)
	}

	cfg.CookieSecure = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadDefaultsSecureCookieInProduction(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JUHE_AI_ENV", "development")
	t.Setenv("JUHE_AI_COOKIE_SECURE", "")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.CookieSecure {
		t.Fatal("CookieSecure = false, want production default true")
	}
}

func TestLoadRejectsProductionOnlyDeclaredInDotEnv(t *testing.T) {
	t.Setenv("NODE_ENV", "")
	t.Setenv("JUHE_AI_ENV", "")
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("NODE_ENV=production\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	_, err := Load(LoadOptions{LoadDotEnv: true})
	if err == nil || !strings.Contains(err.Error(), "NODE_ENV=production") {
		t.Fatalf("Load() error = %v, want explicit process environment error", err)
	}
}

func TestLoadUsesNodeEnvAsRuntimeEnvironment(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JUHE_AI_ENV", "development")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !strings.EqualFold(cfg.Env, "production") {
		t.Fatalf("Env = %q, want production from NODE_ENV", cfg.Env)
	}
}

func TestLoadAllowsCaptchaDisableInProduction(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JUHE_AI_AUTH_CAPTCHA_DISABLED", "true")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AuthCaptchaDisabled {
		t.Fatal("AuthCaptchaDisabled = false, want true")
	}
}

func TestLoadAllowsExplicitCookieSecureOverrideInProduction(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JUHE_AI_COOKIE_SECURE", "false")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CookieSecure {
		t.Fatal("CookieSecure = true, want explicit false override")
	}
}

func TestLoadDefaultsNodeInternalRequestTimeout(t *testing.T) {
	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NodeInternalRequestTimeout != 2*time.Second {
		t.Fatalf(
			"NodeInternalRequestTimeout = %s, want %s",
			cfg.NodeInternalRequestTimeout,
			2*time.Second,
		)
	}
}

func TestLoadUsesExplicitRedisNamespace(t *testing.T) {
	t.Setenv("JUHE_AI_REDIS_NAMESPACE", " prod west/1 ")
	t.Setenv("JUHE_AI_SECRET", "namespace-secret-for-w5-tests-123456789")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RedisNamespace != "prod_west_1" {
		t.Fatalf("RedisNamespace = %q, want prod_west_1", cfg.RedisNamespace)
	}
}

func TestLoadDerivesRedisNamespaceFromSecret(t *testing.T) {
	t.Setenv("JUHE_AI_REDIS_NAMESPACE", "")
	t.Setenv("JUHE_AI_SECRET", "namespace-secret-for-w5-tests-123456789")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RedisNamespace != "env-2de48e5b74e9" {
		t.Fatalf("RedisNamespace = %q, want env-2de48e5b74e9", cfg.RedisNamespace)
	}
}

func TestConfigRejectsInvalidPort(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       70000,
		RedisNamespace:             "juhe-ai",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("Validate() expected error")
	}
}

func TestConfigRejectsSameRedisDB(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6379/0",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate Redis DB error")
	}
}

func TestConfigRejectsSameRedisDBWithDefaultPort(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1/0",
		RedisStateURL:              "redis://127.0.0.1:6379/0",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate Redis DB error")
	}
}

func TestConfigAllowsDifferentRedisProcesses(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigRejectsDifferentRedisDBsOnSameRedisProcess(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6379/1",
		RedisQueueURL:              "redis://127.0.0.1:6379/2",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want same Redis process error")
	}
}

func TestConfigRejectsLoopbackRedisAlias(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://localhost:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want loopback Redis alias error")
	}
}

func TestConfigRejectsIPv6LoopbackRedisAlias(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://[::1]:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want IPv6 loopback Redis alias error")
	}
}

func TestConfigRejectsRedissSameRedisProcess(t *testing.T) {
	cfg := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		RedisCacheURL:              "rediss://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6379/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want same Redis process error across redis schemes")
	}
}

func TestConfigPublicAPIEnabledRequiresRuntimeDependencies(t *testing.T) {
	base := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		TrustProxy:                 "false",
		PublicAPIEnabled:           true,
		PostgresURL:                "postgres://127.0.0.1:5432/juhe_ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalBaseURL:        "http://127.0.0.1:3001",
		Secret:                     "12345678901234567890123456789012",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}

	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "postgres",
			edit: func(cfg *Config) { cfg.PostgresURL = "" },
			want: "JUHE_AI_POSTGRES_URL",
		},
		{
			name: "state redis",
			edit: func(cfg *Config) { cfg.RedisStateURL = "" },
			want: "JUHE_AI_REDIS_STATE_URL",
		},
		{
			name: "cache redis",
			edit: func(cfg *Config) { cfg.RedisCacheURL = "" },
			want: "JUHE_AI_REDIS_CACHE_URL",
		},
		{
			name: "queue redis",
			edit: func(cfg *Config) { cfg.RedisQueueURL = "" },
			want: "JUHE_AI_REDIS_QUEUE_URL",
		},
		{
			name: "secret",
			edit: func(cfg *Config) { cfg.Secret = "" },
			want: "JUHE_AI_SECRET",
		},
		{
			name: "short secret",
			edit: func(cfg *Config) { cfg.Secret = "short-secret" },
			want: "JUHE_AI_SECRET",
		},
		{
			name: "node internal base URL",
			edit: func(cfg *Config) { cfg.NodeInternalBaseURL = "" },
			want: "JUHE_AI_NODE_INTERNAL_BASE_URL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want public API dependency error")
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("Validate() error = %q, want contains %q", got, tc.want)
			}
		})
	}

	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigManagementAPIEnabledRequiresRuntimeDependencies(t *testing.T) {
	base := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		TrustProxy:                 "false",
		ManagementAPIEnabled:       true,
		PostgresURL:                "postgres://127.0.0.1:5432/juhe_ai",
		RedisCacheURL:              "redis://127.0.0.1:6379/0",
		RedisStateURL:              "redis://127.0.0.1:6380/1",
		RedisQueueURL:              "redis://127.0.0.1:6381/2",
		NodeInternalBaseURL:        "http://127.0.0.1:3001",
		Secret:                     "12345678901234567890123456789012",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
	}

	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "postgres",
			edit: func(cfg *Config) { cfg.PostgresURL = "" },
			want: "JUHE_AI_POSTGRES_URL",
		},
		{
			name: "state redis",
			edit: func(cfg *Config) { cfg.RedisStateURL = "" },
			want: "JUHE_AI_REDIS_STATE_URL",
		},
		{
			name: "cache redis",
			edit: func(cfg *Config) { cfg.RedisCacheURL = "" },
			want: "JUHE_AI_REDIS_CACHE_URL",
		},
		{
			name: "queue redis",
			edit: func(cfg *Config) { cfg.RedisQueueURL = "" },
			want: "JUHE_AI_REDIS_QUEUE_URL",
		},
		{
			name: "secret",
			edit: func(cfg *Config) { cfg.Secret = "" },
			want: "JUHE_AI_SECRET",
		},
		{
			name: "short secret",
			edit: func(cfg *Config) { cfg.Secret = "short-secret" },
			want: "JUHE_AI_SECRET",
		},
		{
			name: "node internal base URL",
			edit: func(cfg *Config) { cfg.NodeInternalBaseURL = "" },
			want: "JUHE_AI_NODE_INTERNAL_BASE_URL",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want management API dependency error")
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("Validate() error = %q, want contains %q", got, tc.want)
			}
		})
	}

	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigGatewayModelsEnabledRequiresPostgres(t *testing.T) {
	cfg := Config{
		Host: "127.0.0.1", Port: 3000, Env: "test", LogLevel: "info",
		RedisNamespace: "juhe-ai", TrustProxy: "false",
		NodeInternalRequestTimeout: 2 * time.Second, ShutdownTimeout: 15 * time.Second,
		GatewayModelsEnabled: true,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_GATEWAY_MODELS_ENABLED") {
		t.Fatalf("Validate() error = %v, want gateway models postgres requirement", err)
	}
}

func TestConfigDoesNotExposeManagementAuthSessionsSwitch(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("ManagementAuthSessionsEnabled"); ok {
		t.Fatal("Config still exposes ManagementAuthSessionsEnabled")
	}
}

func TestConfigOwnerLockRequiresExplicitRuntimeFields(t *testing.T) {
	base := Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "juhe-ai",
		TrustProxy:                 "false",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
		OwnerLockEnabled:           true,
		OwnerLockPath:              "runtime/owner.lock",
		OwnerLockDeploymentEpoch:   "epoch-1",
		OwnerLockRole:              "server",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "path", edit: func(cfg *Config) { cfg.OwnerLockPath = "" }, want: "JUHE_AI_OWNER_LOCK_PATH"},
		{name: "epoch", edit: func(cfg *Config) { cfg.OwnerLockDeploymentEpoch = "" }, want: "JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH"},
		{name: "role", edit: func(cfg *Config) { cfg.OwnerLockRole = "management" }, want: "JUHE_AI_OWNER_LOCK_ROLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestLoadParsesPublicAPIEnv(t *testing.T) {
	t.Setenv("JUHE_AI_PUBLIC_API_ENABLED", "true")
	t.Setenv("JUHE_AI_POSTGRES_URL", "postgres://127.0.0.1:5432/juhe_ai")
	t.Setenv("JUHE_AI_REDIS_CACHE_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JUHE_AI_REDIS_STATE_URL", "redis://127.0.0.1:6380/1")
	t.Setenv("JUHE_AI_REDIS_QUEUE_URL", "redis://127.0.0.1:6381/2")
	t.Setenv("JUHE_AI_SECRET", "12345678901234567890123456789012")
	t.Setenv("JUHE_AI_NODE_INTERNAL_BASE_URL", "http://127.0.0.1:3001")
	t.Setenv("JUHE_AI_NODE_INTERNAL_REQUEST_TIMEOUT", "750ms")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.PublicAPIEnabled {
		t.Fatal("PublicAPIEnabled = false, want true")
	}
	if cfg.Secret != "12345678901234567890123456789012" {
		t.Fatalf("Secret = %q, want configured secret", cfg.Secret)
	}
	if cfg.NodeInternalBaseURL != "http://127.0.0.1:3001" {
		t.Fatalf("NodeInternalBaseURL = %q, want configured URL", cfg.NodeInternalBaseURL)
	}
	if cfg.NodeInternalRequestTimeout != 750*time.Millisecond {
		t.Fatalf(
			"NodeInternalRequestTimeout = %s, want %s",
			cfg.NodeInternalRequestTimeout,
			750*time.Millisecond,
		)
	}
}

func TestLoadParsesManagementAPIEnv(t *testing.T) {
	t.Setenv("JUHE_AI_MANAGEMENT_API_ENABLED", "true")
	t.Setenv("JUHE_AI_POSTGRES_URL", "postgres://127.0.0.1:5432/juhe_ai")
	t.Setenv("JUHE_AI_REDIS_CACHE_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("JUHE_AI_REDIS_STATE_URL", "redis://127.0.0.1:6380/1")
	t.Setenv("JUHE_AI_REDIS_QUEUE_URL", "redis://127.0.0.1:6381/2")
	t.Setenv("JUHE_AI_NODE_INTERNAL_BASE_URL", "http://127.0.0.1:3001")
	t.Setenv("JUHE_AI_SECRET", "12345678901234567890123456789012")

	cfg, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ManagementAPIEnabled {
		t.Fatal("ManagementAPIEnabled = false, want true")
	}
}

func TestLoadIgnoresRemovedManagementAuthSessionsEnv(t *testing.T) {
	t.Setenv("JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED", "true")

	_, err := Load(LoadOptions{LoadDotEnv: false})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadParsesRuntimeLogIndexEnabledEnv(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "false", value: "false", want: false},
		{name: "off", value: "OFF", want: false},
		{name: "no", value: " no ", want: false},
		{name: "zero", value: "0", want: false},
		{name: "true", value: "true", want: true},
		{name: "yes", value: "YES", want: true},
		{name: "on", value: "on", want: true},
		{name: "one", value: "1", want: true},
		{name: "ECMAScript byte order mark", value: "\uFEFF true \uFEFF", want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED", testCase.value)
			cfg, err := Load(LoadOptions{LoadDotEnv: false})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.RuntimeLogIndexEnabled != testCase.want {
				t.Fatalf("RuntimeLogIndexEnabled = %v, want %v", cfg.RuntimeLogIndexEnabled, testCase.want)
			}
		})
	}

	t.Run("unset defaults true", func(t *testing.T) {
		t.Setenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED", "")
		_ = os.Unsetenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED")
		cfg, err := Load(LoadOptions{LoadDotEnv: false})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.RuntimeLogIndexEnabled {
			t.Fatal("RuntimeLogIndexEnabled = false, want true")
		}
	})

	t.Run("rejects bare t", func(t *testing.T) {
		t.Setenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED", "t")
		if _, err := Load(LoadOptions{LoadDotEnv: false}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_INDEX_ENABLED") {
			t.Fatalf("Load() error = %v, want deployment bool error", err)
		}
	})

	t.Run("rejects explicitly empty value", func(t *testing.T) {
		t.Setenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED", "")
		if _, err := Load(LoadOptions{LoadDotEnv: false}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_INDEX_ENABLED") {
			t.Fatalf("Load() error = %v, want deployment bool error", err)
		}
	})

	t.Run("preserves non ECMAScript next line", func(t *testing.T) {
		t.Setenv("JUHE_AI_RUNTIME_LOG_INDEX_ENABLED", "\u0085true\u0085")
		if _, err := Load(LoadOptions{LoadDotEnv: false}); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_INDEX_ENABLED") {
			t.Fatalf("Load() error = %v, want deployment bool error", err)
		}
	})
}

func TestConfigValidatesNodeInternalRequestTimeoutRangeRegardlessOfPublicAPI(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{name: "below minimum", timeout: 100*time.Millisecond - time.Nanosecond, wantErr: true},
		{name: "minimum", timeout: 100 * time.Millisecond},
		{name: "maximum", timeout: 10 * time.Second},
		{name: "above maximum", timeout: 10*time.Second + time.Nanosecond, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Host:                       "127.0.0.1",
				Port:                       3000,
				RedisNamespace:             "juhe-ai",
				NodeInternalRequestTimeout: test.timeout,
				ShutdownTimeout:            time.Second,
			}
			err := cfg.Validate()
			if test.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want timeout range error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "JUHE_AI_NODE_INTERNAL_REQUEST_TIMEOUT") {
				t.Fatalf("Validate() error = %q, want timeout env name", err)
			}
		})
	}
}
