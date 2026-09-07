package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The production security gates mirror the Node baselines pinned by
// http-security-production-guard-regression.ts (secret strength, cookie
// secure default, CORS allowlist) and development.ts (dev auto-login), all
// keyed off the single production signal NODE_ENV (Node isProductionRuntime,
// runtime.ts:979-980).

func strongProductionSecret(length int) string {
	return strings.Repeat("a", length)
}

func productionSecurityEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"NODE_ENV":                "production",
		"JUHE_AI_DATABASE_PATH":   filepath.Join(t.TempDir(), "juhe-ai.sqlite3"),
		"JUHE_AI_SECRET":          strongProductionSecret(32),
		"JUHE_AI_ALLOWED_ORIGINS": "https://admin.example.com",
	}
}

func developmentSecurityEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(t.TempDir(), "juhe-ai.sqlite3"),
	}
}

func loadRuntimeConfigEnv(t *testing.T, env map[string]string) (runtimeConfig, error) {
	t.Helper()
	return loadRuntimeConfig(func(key string) string { return env[key] })
}

func TestLoadRuntimeConfigProductionSecretGate(t *testing.T) {
	// 生产 + 默认开发密钥 → 启动失败且文案匹配（Node runtime.ts:958）。
	env := productionSecurityEnv(t)
	env["JUHE_AI_SECRET"] = "juhe-ai-dev-secret-change-me"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET 在生产环境必须配置为至少 32 位的稳定随机密钥") {
		t.Fatalf("production default secret must fail fast, got %v", err)
	}

	// 生产 + 31 位密钥 → 启动失败（长度边界）。
	env = productionSecurityEnv(t)
	env["JUHE_AI_SECRET"] = strongProductionSecret(31)
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET 在生产环境必须配置为至少 32 位") {
		t.Fatalf("production 31-char secret must fail fast, got %v", err)
	}

	// 生产 + 32 位密钥 → 通过（长度边界）。
	env = productionSecurityEnv(t)
	env["JUHE_AI_SECRET"] = strongProductionSecret(32)
	if _, err := loadRuntimeConfigEnv(t, env); err != nil {
		t.Fatalf("production 32-char secret must pass: %v", err)
	}

	// 生产判定大小写不敏感（Node toLowerCase 语义）。
	env = productionSecurityEnv(t)
	env["NODE_ENV"] = "Production"
	env["JUHE_AI_SECRET"] = "short"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET 在生产环境必须配置为至少 32 位") {
		t.Fatalf("case-insensitive production signal must gate the secret, got %v", err)
	}

	// 非生产 + 弱/默认密钥 → 通过（本地开发不受影响）。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_SECRET"] = "juhe-ai-dev-secret-change-me"
	if _, err := loadRuntimeConfigEnv(t, env); err != nil {
		t.Fatalf("non-production weak secret must pass: %v", err)
	}
}

// TestLoadRuntimeConfigAccountHealthProbeDeadline：健康检查派发 outbox 行的
// 探针 deadline 窗口（去跨进程战役第二刀迁到 gateway 的同名 env）——默认
// 65000、合法值投影、非法值按 0 处理保持写侧 input_unavailable 降级。
func TestLoadRuntimeConfigAccountHealthProbeDeadline(t *testing.T) {
	env := developmentSecurityEnv(t)
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("base config: %v", err)
	}
	if cfg.AccountHealthProbeDeadlineMS != 65_000 {
		t.Fatalf("default deadline = %d want 65000", cfg.AccountHealthProbeDeadlineMS)
	}
	env["JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS"] = "30000"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil || cfg.AccountHealthProbeDeadlineMS != 30_000 {
		t.Fatalf("valid override = %d, %v", cfg.AccountHealthProbeDeadlineMS, err)
	}
	for _, raw := range []string{"abc", "999", "600001"} {
		env["JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS"] = raw
		cfg, err = loadRuntimeConfigEnv(t, env)
		if err != nil {
			t.Fatalf("invalid value %q must not fail startup: %v", raw, err)
		}
		if cfg.AccountHealthProbeDeadlineMS != 0 {
			t.Fatalf("invalid value %q must keep the writer inert, got %d", raw, cfg.AccountHealthProbeDeadlineMS)
		}
	}
}

func TestLoadRuntimeConfigProductionSignalReadsNodeEnv(t *testing.T) {
	// JUHE_AI_NODE_ENV 未设 + NODE_ENV=production → 全部生产门禁生效
	// （Node isProductionRuntime 只读 NODE_ENV，runtime.ts:979-980）。
	env := productionSecurityEnv(t)
	if _, ok := env["JUHE_AI_NODE_ENV"]; ok {
		t.Fatal("production base env must not set JUHE_AI_NODE_ENV")
	}
	env["JUHE_AI_SECRET"] = "short"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET 在生产环境必须配置为至少 32 位") {
		t.Fatalf("NODE_ENV=production alone must gate the secret, got %v", err)
	}
	env = productionSecurityEnv(t)
	delete(env, "JUHE_AI_ALLOWED_ORIGINS")
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ALLOWED_ORIGINS 在生产环境必须显式配置后台前端 Origin") {
		t.Fatalf("NODE_ENV=production alone must gate the CORS allowlist, got %v", err)
	}
	env = productionSecurityEnv(t)
	env["JUHE_AI_DEV_AUTO_LOGIN_USERNAME"] = "admin"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用") {
		t.Fatalf("NODE_ENV=production alone must disable dev auto-login, got %v", err)
	}
	env = productionSecurityEnv(t)
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("NODE_ENV=production base config: %v", err)
	}
	if !cfg.CookieSecure {
		t.Fatal("NODE_ENV=production alone must default cookie secure on")
	}
	if cfg.ChatToolEnvironment != "production" {
		t.Fatalf("chat tool environment must fall back to NODE_ENV, got %q", cfg.ChatToolEnvironment)
	}

	// 显式 JUHE_AI_NODE_ENV 保持覆盖 ChatToolEnvironment 的能力，但不再携带
	// 生产语义：JUHE_AI_NODE_ENV=development + NODE_ENV=production 时生产门禁
	// 仍然生效。
	env = productionSecurityEnv(t)
	env["JUHE_AI_NODE_ENV"] = "development"
	env["JUHE_AI_SECRET"] = "short"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET 在生产环境必须配置为至少 32 位") {
		t.Fatalf("explicit JUHE_AI_NODE_ENV must not weaken the production gates, got %v", err)
	}
	env = productionSecurityEnv(t)
	env["JUHE_AI_NODE_ENV"] = "development"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("explicit JUHE_AI_NODE_ENV=development under production: %v", err)
	}
	if cfg.ChatToolEnvironment != "development" {
		t.Fatalf("explicit JUHE_AI_NODE_ENV must override the chat environment, got %q", cfg.ChatToolEnvironment)
	}

	// 仅设 JUHE_AI_NODE_ENV=production（NODE_ENV 未设）→ 非生产契约，门禁不生效。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = "juhe-ai-dev-secret-change-me"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("JUHE_AI_NODE_ENV alone must not arm the production gates, got %v", err)
	}
	if cfg.CookieSecure {
		t.Fatal("JUHE_AI_NODE_ENV alone must not default cookie secure on")
	}
	if cfg.ChatToolEnvironment != "production" {
		t.Fatalf("chat tool environment override lost: %q", cfg.ChatToolEnvironment)
	}
}

func TestLoadRuntimeConfigCookieSecureDefaultAndOverride(t *testing.T) {
	// 生产默认 Secure=true（Node strictBooleanConfig fallback=production）。
	cfg, err := loadRuntimeConfigEnv(t, productionSecurityEnv(t))
	if err != nil {
		t.Fatalf("production base config: %v", err)
	}
	if !cfg.CookieSecure || cfg.CookieSameSite != "lax" {
		t.Fatalf("production cookie defaults wrong: secure=%v sameSite=%q", cfg.CookieSecure, cfg.CookieSameSite)
	}

	// 显式 env 覆盖优先（生产显式关闭保持既有覆盖语义）。
	env := productionSecurityEnv(t)
	env["JUHE_AI_COOKIE_SECURE"] = "false"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("production explicit secure=false: %v", err)
	}
	if cfg.CookieSecure {
		t.Fatal("explicit JUHE_AI_COOKIE_SECURE=false must override the production default")
	}

	// 生产 SameSite=none 借助 Secure 默认值通过（Node 同一判定顺序）。
	env = productionSecurityEnv(t)
	env["JUHE_AI_COOKIE_SAME_SITE"] = "none"
	if _, err := loadRuntimeConfigEnv(t, env); err != nil {
		t.Fatalf("production same-site=none with secure default true must pass: %v", err)
	}

	// 非生产默认 Secure=false；显式 true 覆盖。
	cfg, err = loadRuntimeConfigEnv(t, developmentSecurityEnv(t))
	if err != nil {
		t.Fatalf("development base config: %v", err)
	}
	if cfg.CookieSecure {
		t.Fatal("development cookie secure must default to false")
	}
	env = developmentSecurityEnv(t)
	env["JUHE_AI_COOKIE_SECURE"] = "true"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("development explicit secure=true: %v", err)
	}
	if !cfg.CookieSecure {
		t.Fatal("explicit JUHE_AI_COOKIE_SECURE=true must override the development default")
	}

	// 非生产 SameSite=none 且未开 Secure 仍失败（既有契约）。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_COOKIE_SAME_SITE"] = "none"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SAME_SITE=none 时必须启用 JUHE_AI_COOKIE_SECURE=true") {
		t.Fatalf("non-production same-site=none without secure must fail, got %v", err)
	}
}

func TestLoadRuntimeConfigCookieSecureStrictBooleanMatrix(t *testing.T) {
	// Node strictBooleanConfig（runtime.ts:1575-1582）解析矩阵：
	// true/1/yes/on → true；false/0/no/off → false（大小写不敏感）；
	// 其他非空值启动报错；空/未设 → 生产默认 true、非生产默认 false。
	trueValues := []string{"true", "TRUE", "True", "1", "yes", "YES", "on", "On"}
	falseValues := []string{"false", "FALSE", "0", "no", "No", "off", "OFF"}
	for _, value := range trueValues {
		env := productionSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || !cfg.CookieSecure {
			t.Fatalf("production JUHE_AI_COOKIE_SECURE=%q must parse true (%v)", value, err)
		}
		env = developmentSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err = loadRuntimeConfigEnv(t, env)
		if err != nil || !cfg.CookieSecure {
			t.Fatalf("development JUHE_AI_COOKIE_SECURE=%q must parse true (%v)", value, err)
		}
	}
	for _, value := range falseValues {
		env := productionSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || cfg.CookieSecure {
			t.Fatalf("production JUHE_AI_COOKIE_SECURE=%q must parse false (%v)", value, err)
		}
		env = developmentSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err = loadRuntimeConfigEnv(t, env)
		if err != nil || cfg.CookieSecure {
			t.Fatalf("development JUHE_AI_COOKIE_SECURE=%q must parse false (%v)", value, err)
		}
	}

	// 其他非空值（含带空白）→ fail-fast，文案对齐 Node。
	for _, value := range []string{"bogus", "2", "enabled", " true x", "y"} {
		env := productionSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SECURE 只能配置为 true/false/1/0/yes/no/on/off") {
			t.Fatalf("production JUHE_AI_COOKIE_SECURE=%q must fail fast, got %v", value, err)
		}
		env = developmentSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SECURE 只能配置为 true/false/1/0/yes/no/on/off") {
			t.Fatalf("development JUHE_AI_COOKIE_SECURE=%q must fail fast, got %v", value, err)
		}
	}

	// 空白字符串与未设等价 → 走生产/非生产默认。
	for _, value := range []string{"", "   "} {
		env := productionSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil || !cfg.CookieSecure {
			t.Fatalf("production blank JUHE_AI_COOKIE_SECURE must keep the default true (%v)", err)
		}
		env = developmentSecurityEnv(t)
		env["JUHE_AI_COOKIE_SECURE"] = value
		cfg, err = loadRuntimeConfigEnv(t, env)
		if err != nil || cfg.CookieSecure {
			t.Fatalf("development blank JUHE_AI_COOKIE_SECURE must keep the default false (%v)", err)
		}
	}
}

func TestLoadRuntimeConfigDevAutoLoginGate(t *testing.T) {
	// 生产启用 dev auto-login → 拒绝启动（Node development.ts:7-9）。
	env := productionSecurityEnv(t)
	env["JUHE_AI_DEV_AUTO_LOGIN_USERNAME"] = "admin"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用") {
		t.Fatalf("production dev auto-login must fail fast, got %v", err)
	}

	// 生产未配置 → 通过。
	if _, err := loadRuntimeConfigEnv(t, productionSecurityEnv(t)); err != nil {
		t.Fatalf("production without dev auto-login must pass: %v", err)
	}

	// 非生产配置 → 放行（本地隔离开发实例依赖）。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_DEV_AUTO_LOGIN_USERNAME"] = "admin"
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("non-production dev auto-login must pass: %v", err)
	}
	if cfg.DevAutoLoginUsername != "admin" {
		t.Fatalf("dev auto-login username lost: %q", cfg.DevAutoLoginUsername)
	}
}

func TestLoadRuntimeConfigCORSAllowlistGate(t *testing.T) {
	// 生产未配置 → 启动失败（Node runtime.ts:1551-1553）。
	env := productionSecurityEnv(t)
	delete(env, "JUHE_AI_ALLOWED_ORIGINS")
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ALLOWED_ORIGINS 在生产环境必须显式配置后台前端 Origin") {
		t.Fatalf("production without allowed origins must fail fast, got %v", err)
	}

	// 生产配置 * → 启动失败。
	env = productionSecurityEnv(t)
	env["JUHE_AI_ALLOWED_ORIGINS"] = "*"
	if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_ALLOWED_ORIGINS 不允许配置 *") {
		t.Fatalf("production wildcard origin must fail fast, got %v", err)
	}

	// 生产白名单：规范化（去尾斜杠、默认端口剥离、去重、保序）。
	env = productionSecurityEnv(t)
	env["JUHE_AI_ALLOWED_ORIGINS"] = "https://admin.example.com, https://admin.example.com/ ,https://ops.example.com:8443/,https://admin.example.com:443"
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("production allowlist config: %v", err)
	}
	want := []string{"https://admin.example.com", "https://ops.example.com:8443"}
	if len(cfg.CORSAllowedOrigins) != len(want) {
		t.Fatalf("normalized origins wrong: %#v", cfg.CORSAllowedOrigins)
	}
	for i, origin := range want {
		if cfg.CORSAllowedOrigins[i] != origin {
			t.Fatalf("normalized origins wrong: %#v", cfg.CORSAllowedOrigins)
		}
	}
	if cfg.CORSAllowAnyOrigin {
		t.Fatal("production must not reflect any origin")
	}

	// 生产非法 Origin → 启动失败（Node normalizeAllowedOrigin）。
	for raw, wantMessage := range map[string]string{
		"ftp://admin.example.com":        "只允许 http 或 https Origin",
		"https://admin.example.com/app":  "只能填写 Origin",
		"https://admin.example.com/?x=1": "只能填写 Origin",
		"not-an-origin":                  "包含无效 Origin",
	} {
		env = productionSecurityEnv(t)
		env["JUHE_AI_ALLOWED_ORIGINS"] = raw
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), wantMessage) {
			t.Fatalf("production invalid origin %q must fail fast, got %v", raw, err)
		}
	}

	// 非生产未配置 → allow-any（等价 *，本地跨域联调）。
	cfg, err = loadRuntimeConfigEnv(t, developmentSecurityEnv(t))
	if err != nil {
		t.Fatalf("development config: %v", err)
	}
	if !cfg.CORSAllowAnyOrigin || len(cfg.CORSAllowedOrigins) != 0 {
		t.Fatalf("development default CORS wrong: any=%v origins=%#v", cfg.CORSAllowAnyOrigin, cfg.CORSAllowedOrigins)
	}

	// 非生产显式 * → allow-any；显式列表 → 精确匹配。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_ALLOWED_ORIGINS"] = "*"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("development wildcard config: %v", err)
	}
	if !cfg.CORSAllowAnyOrigin {
		t.Fatal("development wildcard must enable allow-any")
	}
	env = developmentSecurityEnv(t)
	env["JUHE_AI_ALLOWED_ORIGINS"] = "http://127.0.0.1:5173/"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("development list config: %v", err)
	}
	if cfg.CORSAllowAnyOrigin || len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != "http://127.0.0.1:5173" {
		t.Fatalf("development list wrong: any=%v origins=%#v", cfg.CORSAllowAnyOrigin, cfg.CORSAllowedOrigins)
	}
}

func TestLoadRuntimeConfigLogBoundsFailFast(t *testing.T) {
	// Node numberConfig（runtime.ts:891-892,1384-1395）：非数字或越界启动报错，
	// 不再静默 clamp；范围检查前先 Math.trunc，所以 "10.5" 按 10 通过。
	nonNumbers := []string{"abc", "1e999", "NaN", "true"}
	for _, value := range nonNumbers {
		env := productionSecurityEnv(t)
		env["JUHE_AI_LOG_MAX_FILES"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_MAX_FILES 必须配置为数字") {
			t.Fatalf("LOG_MAX_FILES=%q must fail as a non-number, got %v", value, err)
		}
		env = productionSecurityEnv(t)
		env["JUHE_AI_LOG_RETENTION_DAYS"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_RETENTION_DAYS 必须配置为数字") {
			t.Fatalf("LOG_RETENTION_DAYS=%q must fail as a non-number, got %v", value, err)
		}
	}

	// 越界仍报错（含截断后越界：0.9 → 0）。
	outOfRangeFiles := []string{"501", "0", "-1", "0.9", "501.5"}
	for _, value := range outOfRangeFiles {
		env := productionSecurityEnv(t)
		env["JUHE_AI_LOG_MAX_FILES"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_MAX_FILES 必须在 1 到 500 之间") {
			t.Fatalf("LOG_MAX_FILES=%q must fail fast, got %v", value, err)
		}
	}
	outOfRangeDays := []string{"31", "0", "31.0", "0.5"}
	for _, value := range outOfRangeDays {
		env := productionSecurityEnv(t)
		env["JUHE_AI_LOG_RETENTION_DAYS"] = value
		if _, err := loadRuntimeConfigEnv(t, env); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_RETENTION_DAYS 必须在 1 到 30 之间") {
			t.Fatalf("LOG_RETENTION_DAYS=%q must fail fast, got %v", value, err)
		}
	}

	// Math.trunc 语义：截断后落界内即合法。
	truncatedPass := []struct {
		value string
		files int
		days  int
	}{
		{"10.5", 10, 10},
		{"1.9", 1, 1},
		{"30.5", 30, 30},
	}
	for _, item := range truncatedPass {
		env := productionSecurityEnv(t)
		env["JUHE_AI_LOG_MAX_FILES"] = item.value
		env["JUHE_AI_LOG_RETENTION_DAYS"] = item.value
		cfg, err := loadRuntimeConfigEnv(t, env)
		if err != nil {
			t.Fatalf("truncated value %q must pass: %v", item.value, err)
		}
		if cfg.LogMaxFiles != item.files || cfg.LogRetentionDays != item.days {
			t.Fatalf("truncated value %q wrong: files=%d want %d, days=%d want %d", item.value, cfg.LogMaxFiles, item.files, cfg.LogRetentionDays, item.days)
		}
	}
	// 上界截断：500.9 → 500 恰好落界内。
	env := productionSecurityEnv(t)
	env["JUHE_AI_LOG_MAX_FILES"] = "500.9"
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("LOG_MAX_FILES=500.9 must pass after truncation: %v", err)
	}
	if cfg.LogMaxFiles != 500 {
		t.Fatalf("LOG_MAX_FILES=500.9 must truncate to 500, got %d", cfg.LogMaxFiles)
	}

	// 边界值与默认值。
	env = productionSecurityEnv(t)
	env["JUHE_AI_LOG_MAX_FILES"] = "1"
	env["JUHE_AI_LOG_RETENTION_DAYS"] = "30"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("log bounds edge values: %v", err)
	}
	if cfg.LogMaxFiles != 1 || cfg.LogRetentionDays != 30 {
		t.Fatalf("log bounds edge values wrong: files=%d days=%d", cfg.LogMaxFiles, cfg.LogRetentionDays)
	}
}

func TestLoadRuntimeConfigQueueDriverConfigRemoved(t *testing.T) {
	// JUHE_AI_QUEUE_DRIVER / JUHE_AI_REDIS_QUEUE_URL 无消费者：
	// 旧实现会对非法值启动报错、performance 模式强制要求 URL；现在必须完全惰性。
	env := developmentSecurityEnv(t)
	env["JUHE_AI_QUEUE_DRIVER"] = "bogus-driver"
	env["JUHE_AI_REDIS_QUEUE_URL"] = "redis://127.0.0.1:6379/2"
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatalf("dead queue config must be inert, got %v", err)
	}
	if cfg.RuntimeMode != "standalone" {
		t.Fatalf("dead queue URL must not flip the runtime mode: %q", cfg.RuntimeMode)
	}

	// 仅设置 JUHE_AI_REDIS_QUEUE_URL 不再触发 performance 提示。
	env = developmentSecurityEnv(t)
	delete(env, "JUHE_AI_DATABASE_PATH")
	env["JUHE_AI_REDIS_QUEUE_URL"] = "redis://127.0.0.1:6379/2"
	cfg, err = loadRuntimeConfigEnv(t, env)
	if err == nil {
		t.Fatalf("sqlite without database path must still fail, got %#v", cfg)
	}
	if !strings.Contains(err.Error(), "JUHE_AI_DATABASE_PATH") {
		t.Fatalf("unexpected error: %v", err)
	}
}
