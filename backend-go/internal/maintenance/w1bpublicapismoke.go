package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/modules/publicapikeys"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w1bPublicAPISmokeScope              = "local_httptest_public_api_smoke"
	w1bPublicAPISmokeFixtureSentinel    = "w1b-public-api-smoke-temporary"
	w1bPublicAPISmokeFixtureTokenName   = "W1b Public API Smoke Token"
	w1bPublicAPISmokeFixtureSourceName  = "W1b Public API Smoke Temporary Source"
	w1bPublicAPILogIngestTimeout        = 10 * time.Second
	w1bPublicAPISmokeFixtureTTL         = 10 * time.Minute
	w1bPublicAPISmokeDependencyTimeout  = 5 * time.Second
	w1bPublicAPISmokeCleanupTimeout     = 5 * time.Second
	w1bPublicAPISmokeTakeoverReason     = "未验证反向代理切流、真实监听端口、Node 入口删除或生产流量"
	w1bPublicAPISmokeGroupListPath      = publicapi.Prefix + "/group/list"
	w1bPublicAPISmokeTargetUsername     = "smoke"
	w1bPublicAPISmokeUserAgent          = "w1b-public-api-smoke"
	w1bPublicAPISmokeTokenIDPrefix      = "exttok_w1b_public_api_smoke_"
	w1bPublicAPISmokeLogIDPrefix        = "publog_w1b_public_api_smoke_"
	w1bPublicAPISmokeTraceIDPrefix      = "w1b_public_api_smoke_"
	w1bPublicAPISmokeTokenValuePrefix   = publicapi.TokenValuePrefix + "w1b_public_api_smoke_"
	w1bPublicAPISmokeTokenPublicPrefix  = publicapi.TokenValuePrefix + "w1b_smoke_"
	w1bPublicAPISmokeNodeDummyBaseURL   = "http://127.0.0.1:1"
	w1bPublicAPISmokeDefaultRouterError = "接口不存在"
)

type W1bPublicAPISmokeResult struct {
	Success            bool                              `json:"success"`
	Scope              string                            `json:"scope"`
	TakeoverEvidence   bool                              `json:"takeoverEvidence"`
	Checks             map[string]W1bPublicAPISmokeCheck `json:"checks"`
	PublicAPI          *W1bPublicAPISmokePublicAPI       `json:"publicApi,omitempty"`
	PublicAPILog       *W1bPublicAPISmokePublicAPILog    `json:"publicApiLog,omitempty"`
	TakeoverAssessment W1bPublicAPISmokeTakeover         `json:"takeoverAssessment"`
}

type W1bPublicAPISmokeCheck struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type W1bPublicAPISmokePublicAPI struct {
	ConfiguredEnabled   bool   `json:"configuredEnabled"`
	LocalSmokeEnabled   bool   `json:"localSmokeEnabled"`
	Prefix              string `json:"prefix"`
	EndpointCount       int    `json:"endpointCount"`
	TestedEndpointCount int    `json:"testedEndpointCount"`
}

type W1bPublicAPISmokePublicAPILog struct {
	ID                    string `json:"id"`
	TraceID               string `json:"traceId"`
	Path                  string `json:"path"`
	QueryString           string `json:"queryString"`
	StatusCode            int    `json:"statusCode"`
	Success               bool   `json:"success"`
	SourceRefID           string `json:"sourceRefId"`
	TokenID               string `json:"tokenId"`
	IsTestToken           bool   `json:"isTestToken"`
	RequestCaptureStatus  string `json:"requestCaptureStatus"`
	ResponseCaptureStatus string `json:"responseCaptureStatus"`
}

type W1bPublicAPISmokeTakeover struct {
	DefaultRouterRegistered        bool   `json:"defaultRouterRegistered"`
	ExplicitOptInMountWorks        bool   `json:"explicitOptInMountWorks"`
	WorkerIngestVerified           bool   `json:"workerIngestVerified"`
	ProductionTakeoverNotEvaluated bool   `json:"productionTakeoverNotEvaluated"`
	Reason                         string `json:"reason"`
}

type w1bPublicAPISmokeFixture struct {
	Token           string
	TokenID         string
	SourceCreated   bool
	cleanupPostgres func(context.Context) error
}

type w1bPublicAPISmokeLogRow struct {
	ID                    string
	TraceID               string
	SourceRefID           string
	TokenID               string
	IsTestToken           bool
	Method                string
	Path                  string
	QueryString           string
	UserAgent             string
	StatusCode            int
	Success               bool
	RequestCaptureStatus  string
	ResponseCaptureStatus string
	RequestDataJSON       string
	ResponseDataJSON      string
	ErrorCode             string
}

func RunW1bPublicAPISmoke(ctx context.Context, cfg config.Config, out io.Writer) error {
	missing := missingW1bPublicAPIConfig(cfg)
	if len(missing) > 0 {
		return fmt.Errorf("W1b public API smoke 缺少必要配置: %v", missing)
	}

	configuredEnabled := cfg.PublicAPIEnabled
	smokeCfg, err := w1bPublicAPISmokeConfig(cfg)
	if err != nil {
		return fmt.Errorf("W1b public API smoke 配置无效: %w", err)
	}

	checks := map[string]W1bPublicAPISmokeCheck{
		"config": okW1bSmokeCheck(),
	}
	publicAPI := W1bPublicAPISmokePublicAPI{
		ConfiguredEnabled:   configuredEnabled,
		LocalSmokeEnabled:   true,
		Prefix:              publicapi.Prefix,
		EndpointCount:       len(publicapi.Endpoints()),
		TestedEndpointCount: 0,
	}
	assessment := defaultW1bPublicAPISmokeTakeover()

	if err := smokeW1bDefaultRouterGuard(ctx, cfg); err != nil {
		checks["defaultRouterGuard"] = failedW1bSmokeCheck(err.Error())
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	checks["defaultRouterGuard"] = okW1bSmokeCheck()

	store, err := openW1bPublicAPISmokeStore(ctx, smokeCfg.PostgresURL)
	if err != nil {
		checks["postgres"] = failedW1bSmokeCheck("PostgreSQL 连接失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	defer store.Close()
	checks["postgres"] = okW1bSmokeCheck()

	pgPool, err := openW1bPublicAPISmokePool(ctx, smokeCfg.PostgresURL)
	if err != nil {
		checks["postgres"] = failedW1bSmokeCheck("PostgreSQL smoke pool 初始化失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	defer pgPool.Close()

	stateRedis, err := openW1bPublicAPISmokeStateRedis(ctx, smokeCfg)
	if err != nil {
		checks["redisState"] = failedW1bSmokeCheck("Redis state 连接失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	defer func() { _ = stateRedis.Close() }()
	checks["redisState"] = okW1bSmokeCheck()

	cacheRedis, err := openW1bPublicAPISmokeCacheRedis(ctx, smokeCfg)
	if err != nil {
		checks["redisCache"] = failedW1bSmokeCheck("Redis cache 连接失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	defer func() { _ = cacheRedis.Close() }()
	checks["redisCache"] = okW1bSmokeCheck()

	apiKeyInvalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: smokeCfg.RedisNamespace,
	})
	if err != nil {
		checks["gatewayCacheInvalidator"] = failedW1bSmokeCheck("网关缓存失效器初始化失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	checks["gatewayCacheInvalidator"] = okW1bSmokeCheck()

	if err := smokeW1bPublicAPIQueue(ctx, smokeCfg.RedisQueueURL); err != nil {
		checks["redisQueue"] = failedW1bSmokeCheck("Redis queue 连接失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	checks["redisQueue"] = okW1bSmokeCheck()

	fixture, err := prepareW1bPublicAPISmokeFixture(ctx, pgPool)
	if err != nil {
		checks["smokeToken"] = failedW1bSmokeCheck(err.Error())
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	cleanupFixture := fixture.cleanupPostgres
	defer func() {
		if cleanupFixture == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), w1bPublicAPISmokeCleanupTimeout)
		defer cancel()
		_ = cleanupFixture(cleanupCtx)
	}()
	checks["smokeToken"] = okW1bSmokeCheck()

	routeResult, err := smokeW1bPublicAPIRoute(ctx, smokeCfg, store, stateRedis, apiKeyInvalidator, fixture)
	if err != nil {
		checks["publicAPIRoute"] = failedW1bSmokeCheck(err.Error())
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	publicAPI.TestedEndpointCount = 1
	assessment.ExplicitOptInMountWorks = true
	checks["publicAPIRoute"] = okW1bSmokeCheck()

	logRow, err := waitW1bPublicAPISmokeLog(ctx, pgPool, routeResult.LogID)
	if err != nil {
		checks["publicAPILogIngest"] = failedW1bSmokeCheck(err.Error())
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	if err := verifyW1bPublicAPISmokeLog(logRow, fixture, routeResult.TraceID); err != nil {
		checks["publicAPILogIngest"] = failedW1bSmokeCheck(err.Error())
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	checks["publicAPILogIngest"] = okW1bSmokeCheck()
	assessment.WorkerIngestVerified = true

	cleanupCtx, cancel := context.WithTimeout(context.Background(), w1bPublicAPISmokeCleanupTimeout)
	if err := cleanupFixture(cleanupCtx); err != nil {
		cancel()
		checks["smokeTokenCleanup"] = failedW1bSmokeCheck("临时 smoke token 清理失败")
		return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
			Checks:             checks,
			PublicAPI:          &publicAPI,
			TakeoverAssessment: assessment,
		})
	}
	cancel()
	cleanupFixture = nil
	checks["smokeTokenCleanup"] = okW1bSmokeCheck()

	return writeW1bPublicAPISmokeResult(out, W1bPublicAPISmokeResult{
		Success:            true,
		Checks:             checks,
		PublicAPI:          &publicAPI,
		PublicAPILog:       publicAPILogSmokeResult(logRow),
		TakeoverAssessment: assessment,
	})
}

type w1bPublicAPIRouteResult struct {
	LogID   string
	TraceID string
}

type w1bPublicAPISmokeHealthCheckDispatcher struct{}

func (w1bPublicAPISmokeHealthCheckDispatcher) Dispatch(context.Context, string, string) error {
	return nil
}

func w1bPublicAPISmokeConfig(cfg config.Config) (config.Config, error) {
	cfg.PublicAPIEnabled = true
	if strings.TrimSpace(cfg.NodeInternalBaseURL) == "" {
		cfg.NodeInternalBaseURL = w1bPublicAPISmokeNodeDummyBaseURL
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func smokeW1bDefaultRouterGuard(ctx context.Context, cfg config.Config) error {
	cfg.PublicAPIEnabled = false
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, w1bPublicAPISmokeGroupListPath, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		return fmt.Errorf("default public API route status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode default public API route response: %w", err)
	}
	if got, _ := body["error"].(string); got != w1bPublicAPISmokeDefaultRouterError {
		return fmt.Errorf("default public API route error = %q", got)
	}
	return nil
}

func smokeW1bPublicAPIRoute(
	ctx context.Context,
	cfg config.Config,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	apiKeyInvalidator publicapikeys.APIKeyGatewayCacheInvalidator,
	fixture w1bPublicAPISmokeFixture,
) (w1bPublicAPIRouteResult, error) {
	logID := w1bPublicAPISmokeLogIDPrefix + uuidNoDash()
	handler, logQueue, err := app.NewPublicAPIHandlerWithOptions(
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		store,
		stateRedis,
		app.PublicAPIHandlerOptions{
			NewLogID:                     func() string { return logID },
			APIKeyInvalidator:            apiKeyInvalidator,
			AccountHealthCheckDispatcher: w1bPublicAPISmokeHealthCheckDispatcher{},
		},
	)
	if err != nil {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("public API handler 初始化失败")
	}
	if logQueue != nil {
		defer func() { _ = logQueue.Close() }()
	}
	if handler == nil {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("public API handler 未启用")
	}

	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:           cfg,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		PublicAPIHandler: handler,
	})
	traceID := w1bPublicAPISmokeTraceIDPrefix + uuidNoDash()
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		w1bPublicAPISmokeGroupListPath+"?targetUsername="+w1bPublicAPISmokeTargetUsername+"&page=1&pageSize=1",
		nil,
	)
	req.RemoteAddr = uniqueSmokeIPv4() + ":12345"
	req.Header.Set("Authorization", "Bearer "+fixture.Token)
	req.Header.Set("X-Request-Id", traceID)
	req.Header.Set("User-Agent", w1bPublicAPISmokeUserAgent)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if bytes.Contains(body, []byte(fixture.Token)) {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("public API route response leaked smoke token")
	}
	if rec.Code != http.StatusOK {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("public API route status = %d", rec.Code)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("decode public API route response: %w", err)
	}
	if _, ok := envelope["data"]; !ok || len(envelope) != 1 {
		return w1bPublicAPIRouteResult{}, fmt.Errorf("public API route response envelope invalid")
	}
	return w1bPublicAPIRouteResult{LogID: logID, TraceID: traceID}, nil
}

func prepareW1bPublicAPISmokeFixture(ctx context.Context, db *pgxpool.Pool) (w1bPublicAPISmokeFixture, error) {
	if err := cleanupStaleW1bPublicAPISmokeFixtures(ctx, db); err != nil {
		return w1bPublicAPISmokeFixture{}, fmt.Errorf("临时 smoke fixture 清理失败")
	}

	sourceExists, err := w1bPublicAPISmokeSourceExists(ctx, db)
	if err != nil {
		return w1bPublicAPISmokeFixture{}, fmt.Errorf("内置测试 source 检查失败")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(w1bPublicAPISmokeFixtureTTL)
	scopesJSON, rateLimitsJSON, err := w1bPublicAPISmokeFixtureJSON()
	if err != nil {
		return w1bPublicAPISmokeFixture{}, err
	}

	sourceCreated := false
	if !sourceExists {
		if _, err := db.Exec(ctx, `
			INSERT INTO juhe_business.external_integration_sources (
				id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
			) VALUES ($1, $2, 'active', $3, $4, $5, $6, $7, $7)
		`, publicapi.BuiltInTestSourceID, w1bPublicAPISmokeFixtureSourceName, scopesJSON, rateLimitsJSON, expiresAt, w1bPublicAPISmokeFixtureSentinel, now); err != nil {
			return w1bPublicAPISmokeFixture{}, fmt.Errorf("临时内置测试 source 创建失败")
		}
		sourceCreated = true
	}

	randomSuffix := uuidNoDash()
	tokenID := w1bPublicAPISmokeTokenIDPrefix + randomSuffix[:12]
	token := w1bPublicAPISmokeTokenValuePrefix + randomSuffix
	tokenPrefix := w1bPublicAPISmokeTokenPublicPrefix + randomSuffix[:8]
	tokenSuffix := randomSuffix[len(randomSuffix)-8:]

	if _, err := db.Exec(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix,
			status, scopes_json, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10, $10)
	`, tokenID, publicapi.BuiltInTestSourceID, w1bPublicAPISmokeFixtureTokenName,
		publicapiauth.HashExternalSourceToken(token), w1bPublicAPISmokeFixtureSentinel,
		tokenPrefix, tokenSuffix, scopesJSON, expiresAt, now); err != nil {
		if sourceCreated {
			_ = cleanupStaleW1bPublicAPISmokeFixtures(context.Background(), db)
		}
		return w1bPublicAPISmokeFixture{}, fmt.Errorf("临时 smoke token 创建失败")
	}

	return w1bPublicAPISmokeFixture{
		Token:         token,
		TokenID:       tokenID,
		SourceCreated: sourceCreated,
		cleanupPostgres: func(cleanupCtx context.Context) error {
			return cleanupW1bPublicAPISmokeFixture(cleanupCtx, db, tokenID)
		},
	}, nil
}

func w1bPublicAPISmokeSourceExists(ctx context.Context, db *pgxpool.Pool) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM juhe_business.external_integration_sources WHERE id = $1
		)
	`, publicapi.BuiltInTestSourceID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func cleanupStaleW1bPublicAPISmokeFixtures(ctx context.Context, db *pgxpool.Pool) error {
	_, tokenErr := db.Exec(ctx, `
		DELETE FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1 AND token_secret_encrypted = $2
	`, publicapi.BuiltInTestSourceID, w1bPublicAPISmokeFixtureSentinel)
	_, sourceErr := db.Exec(ctx, `
		DELETE FROM juhe_business.external_integration_sources AS sources
		WHERE sources.id = $1
		  AND sources.notes = $2
		  AND NOT EXISTS (
		    SELECT 1 FROM juhe_business.external_integration_source_tokens AS tokens
		    WHERE tokens.source_ref_id = sources.id
		  )
	`, publicapi.BuiltInTestSourceID, w1bPublicAPISmokeFixtureSentinel)
	return errors.Join(tokenErr, sourceErr)
}

func cleanupW1bPublicAPISmokeFixture(ctx context.Context, db *pgxpool.Pool, tokenID string) error {
	_, tokenErr := db.Exec(ctx, `
		DELETE FROM juhe_business.external_integration_source_tokens
		WHERE id = $1 AND token_secret_encrypted = $2
	`, tokenID, w1bPublicAPISmokeFixtureSentinel)
	_, sourceErr := db.Exec(ctx, `
		DELETE FROM juhe_business.external_integration_sources AS sources
		WHERE sources.id = $1
		  AND sources.notes = $2
		  AND NOT EXISTS (
		    SELECT 1 FROM juhe_business.external_integration_source_tokens AS tokens
		    WHERE tokens.source_ref_id = sources.id
		  )
	`, publicapi.BuiltInTestSourceID, w1bPublicAPISmokeFixtureSentinel)
	return errors.Join(tokenErr, sourceErr)
}

func w1bPublicAPISmokeFixtureJSON() (string, string, error) {
	scopes, err := json.Marshal([]string{
		publicapi.ScopeGroupListRead,
		publicapi.ScopeAccountListRead,
	})
	if err != nil {
		return "", "", err
	}
	rateLimits, err := json.Marshal([]port.PublicAPIRateLimitRule{{
		WindowSeconds: publicapi.BuiltInTestRateLimitWindowSeconds,
		MaxRequests:   publicapi.BuiltInTestRateLimitMaxRequests,
	}})
	if err != nil {
		return "", "", err
	}
	return string(scopes), string(rateLimits), nil
}

func waitW1bPublicAPISmokeLog(ctx context.Context, db *pgxpool.Pool, logID string) (w1bPublicAPISmokeLogRow, error) {
	pollCtx, cancel := context.WithTimeout(ctx, w1bPublicAPILogIngestTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, found, err := findW1bPublicAPISmokeLog(pollCtx, db, logID)
		if err != nil {
			return w1bPublicAPISmokeLogRow{}, fmt.Errorf("public API log 查询失败")
		}
		if found {
			return row, nil
		}

		select {
		case <-pollCtx.Done():
			return w1bPublicAPISmokeLogRow{}, fmt.Errorf("未在 %s 内看到 public API log，确认 juhe-ai-worker ingest 已启动并连接同一 PostgreSQL 与 Redis queue", w1bPublicAPILogIngestTimeout)
		case <-ticker.C:
		}
	}
}

func findW1bPublicAPISmokeLog(ctx context.Context, db *pgxpool.Pool, logID string) (w1bPublicAPISmokeLogRow, bool, error) {
	var row w1bPublicAPISmokeLogRow
	err := db.QueryRow(ctx, `
		SELECT
		  id,
		  COALESCE(trace_id, ''),
		  COALESCE(source_ref_id, ''),
		  COALESCE(token_id, ''),
		  is_test_token,
		  method,
		  path,
		  COALESCE(query_string, ''),
		  COALESCE(user_agent, ''),
		  COALESCE(status_code, 0),
		  success,
		  request_capture_status,
		  response_capture_status,
		  request_data_json,
		  response_data_json,
		  COALESCE(error_code, '')
		FROM juhe_dataset.public_api_logs
		WHERE id = $1
		LIMIT 1
	`, logID).Scan(
		&row.ID,
		&row.TraceID,
		&row.SourceRefID,
		&row.TokenID,
		&row.IsTestToken,
		&row.Method,
		&row.Path,
		&row.QueryString,
		&row.UserAgent,
		&row.StatusCode,
		&row.Success,
		&row.RequestCaptureStatus,
		&row.ResponseCaptureStatus,
		&row.RequestDataJSON,
		&row.ResponseDataJSON,
		&row.ErrorCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return w1bPublicAPISmokeLogRow{}, false, nil
	}
	if err != nil {
		return w1bPublicAPISmokeLogRow{}, false, err
	}
	return row, true, nil
}

func verifyW1bPublicAPISmokeLog(row w1bPublicAPISmokeLogRow, fixture w1bPublicAPISmokeFixture, traceID string) error {
	switch {
	case row.TraceID != traceID:
		return fmt.Errorf("public API log trace_id 不匹配")
	case row.Method != http.MethodGet:
		return fmt.Errorf("public API log method = %s", row.Method)
	case row.Path != w1bPublicAPISmokeGroupListPath:
		return fmt.Errorf("public API log path = %s", row.Path)
	case !strings.Contains(row.QueryString, "targetUsername="+w1bPublicAPISmokeTargetUsername):
		return fmt.Errorf("public API log query_string 缺少 smoke target")
	case row.UserAgent != w1bPublicAPISmokeUserAgent:
		return fmt.Errorf("public API log user_agent 不匹配")
	case row.StatusCode != http.StatusOK:
		return fmt.Errorf("public API log status_code = %d", row.StatusCode)
	case !row.Success:
		return fmt.Errorf("public API log success = false")
	case row.SourceRefID != publicapi.BuiltInTestSourceID:
		return fmt.Errorf("public API log source_ref_id 不匹配")
	case row.TokenID != fixture.TokenID:
		return fmt.Errorf("public API log token_id 不匹配")
	case !row.IsTestToken:
		return fmt.Errorf("public API log is_test_token = false")
	case strings.Contains(row.RequestDataJSON, fixture.Token) || strings.Contains(row.ResponseDataJSON, fixture.Token):
		return fmt.Errorf("public API log 泄露 smoke token")
	}
	return nil
}

func publicAPILogSmokeResult(row w1bPublicAPISmokeLogRow) *W1bPublicAPISmokePublicAPILog {
	return &W1bPublicAPISmokePublicAPILog{
		ID:                    row.ID,
		TraceID:               row.TraceID,
		Path:                  row.Path,
		QueryString:           row.QueryString,
		StatusCode:            row.StatusCode,
		Success:               row.Success,
		SourceRefID:           row.SourceRefID,
		TokenID:               row.TokenID,
		IsTestToken:           row.IsTestToken,
		RequestCaptureStatus:  row.RequestCaptureStatus,
		ResponseCaptureStatus: row.ResponseCaptureStatus,
	}
}

func openW1bPublicAPISmokeStore(ctx context.Context, rawURL string) (*postgresstore.Store, error) {
	store, err := postgresstore.Open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w1bPublicAPISmokeDependencyTimeout)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func openW1bPublicAPISmokePool(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w1bPublicAPISmokeDependencyTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func openW1bPublicAPISmokeStateRedis(ctx context.Context, cfg config.Config) (*redisplatform.Client, error) {
	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w1bPublicAPISmokeDependencyTimeout)
	defer cancel()
	if err := stateRedis.Ping(pingCtx); err != nil {
		_ = stateRedis.Close()
		return nil, err
	}
	return stateRedis, nil
}

func openW1bPublicAPISmokeCacheRedis(ctx context.Context, cfg config.Config) (*redisplatform.Client, error) {
	cacheRedis, err := redisplatform.NewClient(cfg.RedisCacheURL, cfg.RedisNamespace+":cache")
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w1bPublicAPISmokeDependencyTimeout)
	defer cancel()
	if err := cacheRedis.Ping(pingCtx); err != nil {
		_ = cacheRedis.Close()
		return nil, err
	}
	return cacheRedis, nil
}

func smokeW1bPublicAPIQueue(ctx context.Context, rawURL string) error {
	opts, err := queue.ParseRedisURL(rawURL)
	if err != nil {
		return err
	}
	client := queue.NewClient(opts)
	defer func() { _ = client.Close() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return client.Ping()
}

func writeW1bPublicAPISmokeResult(out io.Writer, result W1bPublicAPISmokeResult) error {
	if result.Scope == "" {
		result.Scope = w1bPublicAPISmokeScope
	}
	result.TakeoverEvidence = false
	if result.Checks == nil {
		result.Checks = map[string]W1bPublicAPISmokeCheck{}
	}
	result.TakeoverAssessment.ProductionTakeoverNotEvaluated = true
	if result.TakeoverAssessment.Reason == "" {
		result.TakeoverAssessment.Reason = w1bPublicAPISmokeTakeoverReason
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("W1b public API smoke 未通过")
	}
	return nil
}

func defaultW1bPublicAPISmokeTakeover() W1bPublicAPISmokeTakeover {
	return W1bPublicAPISmokeTakeover{
		ProductionTakeoverNotEvaluated: true,
		Reason:                         w1bPublicAPISmokeTakeoverReason,
	}
}

func okW1bSmokeCheck() W1bPublicAPISmokeCheck {
	return W1bPublicAPISmokeCheck{Status: "ok"}
}

func failedW1bSmokeCheck(message string) W1bPublicAPISmokeCheck {
	return W1bPublicAPISmokeCheck{Status: "error", Error: message}
}

func missingW1bPublicAPIConfig(cfg config.Config) []string {
	var missing []string
	if cfg.PostgresURL == "" {
		missing = append(missing, "JUHE_AI_POSTGRES_URL")
	}
	if cfg.RedisStateURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_STATE_URL")
	}
	if cfg.RedisCacheURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_CACHE_URL")
	}
	if cfg.RedisQueueURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_QUEUE_URL")
	}
	if len([]rune(strings.TrimSpace(cfg.Secret))) < 32 {
		missing = append(missing, "JUHE_AI_SECRET(>=32)")
	}
	return missing
}

func uuidNoDash() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}
