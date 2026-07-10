package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

type W1aPublicSettingsSmokeResult struct {
	Success        bool                                     `json:"success"`
	Checks         map[string]W1aPublicSettingsSmokeCheck   `json:"checks"`
	PublicSettings *publicsettings.Response                 `json:"publicSettings,omitempty"`
	RateLimit      *W1aPublicSettingsSmokeRateLimitSettings `json:"rateLimit,omitempty"`
}

type W1aPublicSettingsSmokeCheck struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type W1aPublicSettingsSmokeRateLimitSettings struct {
	PerMinute         int `json:"perMinute,omitempty"`
	BurstPer10Seconds int `json:"burstPer10Seconds,omitempty"`
}

func RunW1aPublicSettingsSmoke(ctx context.Context, cfg config.Config, out io.Writer) error {
	missing := missingW1aPublicSettingsURLs(cfg)
	if len(missing) > 0 {
		return fmt.Errorf("W1a public settings smoke 缺少必要配置: %v", missing)
	}

	checks := map[string]W1aPublicSettingsSmokeCheck{}
	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		checks["postgres"] = failedW1aSmokeCheck("PostgreSQL 连接失败")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{Checks: checks})
	}
	defer store.Close()
	if err := store.Ping(ctx); err != nil {
		checks["postgres"] = failedW1aSmokeCheck("PostgreSQL ping 失败")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{Checks: checks})
	}
	checks["postgres"] = okW1aSmokeCheck()

	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		checks["redisState"] = failedW1aSmokeCheck("Redis state client 初始化失败")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{Checks: checks})
	}
	defer func() { _ = stateRedis.Close() }()
	if err := stateRedis.Ping(ctx); err != nil {
		checks["redisState"] = failedW1aSmokeCheck("Redis state ping 失败")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{Checks: checks})
	}
	checks["redisState"] = okW1aSmokeCheck()

	publicSettings, err := store.PublicGlobalSettings(ctx)
	if err != nil {
		checks["publicSettingsStore"] = failedW1aSmokeCheck("公开设置读取失败，请确认 000002_w1_public_settings.sql 已执行且 global_settings 包含 appName/appIcon")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{Checks: checks})
	}
	publicResponse := publicsettings.Response{
		AppName: publicSettings.AppName,
		AppIcon: publicSettings.AppIcon,
	}
	checks["publicSettingsStore"] = okW1aSmokeCheck()

	rateLimit, err := store.SystemAPIRateLimitSettings(ctx)
	if err != nil {
		checks["rateLimitSettingsStore"] = failedW1aSmokeCheck("系统 API 读限流设置读取失败，请确认 system_settings 包含 W1a 限流键")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{
			Checks:         checks,
			PublicSettings: &publicResponse,
		})
	}
	rateLimitResponse := W1aPublicSettingsSmokeRateLimitSettings{
		PerMinute:         rateLimit.IPReadPerMinute,
		BurstPer10Seconds: rateLimit.IPReadBurstPer10Seconds,
	}
	checks["rateLimitSettingsStore"] = okW1aSmokeCheck()

	if err := smokeW1aPublicSettingsRoute(ctx, cfg, store, stateRedis, publicResponse); err != nil {
		checks["publicSettingsRoute"] = failedW1aSmokeCheck(err.Error())
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{
			Checks:         checks,
			PublicSettings: &publicResponse,
			RateLimit:      &rateLimitResponse,
		})
	}
	checks["publicSettingsRoute"] = okW1aSmokeCheck()

	if err := smokeW1aRedisRateLimit(ctx, cfg, stateRedis); err != nil {
		checks["redisRateLimitSharedState"] = failedW1aSmokeCheck("Redis state 限流探测失败")
		return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{
			Checks:         checks,
			PublicSettings: &publicResponse,
			RateLimit:      &rateLimitResponse,
		})
	}
	checks["redisRateLimitSharedState"] = okW1aSmokeCheck()

	return writeW1aPublicSettingsSmokeResult(out, W1aPublicSettingsSmokeResult{
		Success:        true,
		Checks:         checks,
		PublicSettings: &publicResponse,
		RateLimit:      &rateLimitResponse,
	})
}

func smokeW1aPublicSettingsRoute(
	ctx context.Context,
	cfg config.Config,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	want publicsettings.Response,
) error {
	service := publicsettings.NewService(store)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                   cfg,
		Logger:                   slog.New(slog.NewTextHandler(io.Discard, nil)),
		PublicSettingsService:    &service,
		SystemAPIRateLimitReader: store,
		SystemAPIIPRateLimiter:   httpapi.NewRedisSystemAPIIPRateLimiter(stateRedis),
	})

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/__aisys__/api/settings/public", nil)
	clientIP := uniqueSmokeIPv4()
	if trustProxy, err := cfg.TrustProxyConfig(); err == nil && trustProxy.Enabled {
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Forwarded-For", clientIP)
	} else {
		req.RemoteAddr = clientIP + ":12345"
	}
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return fmt.Errorf("public settings route status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		return fmt.Errorf("public settings route Cache-Control = %q", got)
	}

	var envelope map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode public settings route response: %w", err)
	}
	if len(envelope) != 1 {
		return fmt.Errorf("public settings route response top-level field count = %d, want 1", len(envelope))
	}
	rawData, ok := envelope["data"]
	if !ok {
		return fmt.Errorf("public settings route response missing data")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &data); err != nil {
		return fmt.Errorf("decode public settings route data: %w", err)
	}
	if len(data) != 2 {
		return fmt.Errorf("public settings route data field count = %d, want 2", len(data))
	}
	var got publicsettings.Response
	if err := json.Unmarshal(data["appName"], &got.AppName); err != nil {
		return fmt.Errorf("decode public settings route appName: %w", err)
	}
	if err := json.Unmarshal(data["appIcon"], &got.AppIcon); err != nil {
		return fmt.Errorf("decode public settings route appIcon: %w", err)
	}
	if got.AppName == "" || got.AppIcon == "" {
		return fmt.Errorf("public settings route response contains empty public setting")
	}
	if got != want {
		return fmt.Errorf("public settings route response = %+v, want %+v", got, want)
	}
	return nil
}

func smokeW1aRedisRateLimit(ctx context.Context, cfg config.Config, stateRedis *redisplatform.Client) error {
	peer, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		return err
	}
	defer func() { _ = peer.Close() }()

	limiterA := httpapi.NewRedisSystemAPIIPRateLimiter(stateRedis)
	limiterB := httpapi.NewRedisSystemAPIIPRateLimiter(peer)
	settings := httpapi.SystemAPIIPRateLimitSettings{
		PerMinute:         1,
		BurstPer10Seconds: 1,
	}
	key := "w1a-public-settings-smoke:" + uuid.NewString()

	first, err := limiterA.AllowSystemAPIIP(ctx, key, settings)
	if err != nil {
		return err
	}
	if !first.Allowed {
		return fmt.Errorf("first redis rate-limit decision denied")
	}

	second, err := limiterB.AllowSystemAPIIP(ctx, key, settings)
	if err != nil {
		return err
	}
	if second.Allowed {
		return fmt.Errorf("second redis rate-limit decision allowed; want shared state denial")
	}
	if second.RetryAfterSeconds <= 0 {
		return fmt.Errorf("second redis rate-limit retry-after = %d", second.RetryAfterSeconds)
	}
	return nil
}

func writeW1aPublicSettingsSmokeResult(out io.Writer, result W1aPublicSettingsSmokeResult) error {
	if result.Checks == nil {
		result.Checks = map[string]W1aPublicSettingsSmokeCheck{}
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("W1a public settings smoke 未通过")
	}
	return nil
}

func okW1aSmokeCheck() W1aPublicSettingsSmokeCheck {
	return W1aPublicSettingsSmokeCheck{Status: "ok"}
}

func failedW1aSmokeCheck(message string) W1aPublicSettingsSmokeCheck {
	return W1aPublicSettingsSmokeCheck{Status: "error", Error: message}
}

func missingW1aPublicSettingsURLs(cfg config.Config) []string {
	var missing []string
	if cfg.PostgresURL == "" {
		missing = append(missing, "JUHE_AI_POSTGRES_URL")
	}
	if cfg.RedisStateURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_STATE_URL")
	}
	return missing
}

func uniqueSmokeIPv4() string {
	now := time.Now().UnixNano()
	return fmt.Sprintf("198.18.%d.%d", 1+now%200, 1+(now/200)%200)
}
