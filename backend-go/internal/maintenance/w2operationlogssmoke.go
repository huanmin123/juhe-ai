package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2OperationLogsSmokeScope             = "local_httptest_w2_operation_logs_smoke"
	w2OperationLogsSmokeTakeoverReason    = "未验证反向代理 path-split、真实监听端口、Node 入口删除或生产流量"
	w2OperationLogsSmokeDependencyTimeout = 5 * time.Second
	w2OperationLogsSmokeCleanupTimeout    = 5 * time.Second
	w2OperationLogsSmokeFixtureTTL        = 10 * time.Minute
	w2OperationLogsSmokeIDPrefix          = "w2_operation_logs_smoke_"
	w2OperationLogsSmokeLogIDPrefix       = "oplog_w2_operation_logs_smoke_"
	w2OperationLogsSmokeTracePrefix       = "w2_operation_logs_smoke_"
	w2OperationLogsSmokeUserAgent         = "w2-operation-logs-smoke"
	w2OperationLogsSmokeDefaultRouteError = "接口不存在"
)

type W2OperationLogsSmokeResult struct {
	Success            bool                         `json:"success"`
	Scope              string                       `json:"scope"`
	TakeoverEvidence   bool                         `json:"takeoverEvidence"`
	Checks             map[string]W2SmokeCheck      `json:"checks"`
	OperationLogs      *W2OperationLogsSmokeSummary `json:"operationLogs,omitempty"`
	TakeoverAssessment W2SmokeTakeover              `json:"takeoverAssessment"`
}

type W2SmokeCheck struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type W2OperationLogsSmokeSummary struct {
	ConfiguredEnabled      bool   `json:"configuredEnabled"`
	LocalSmokeEnabled      bool   `json:"localSmokeEnabled"`
	AdminListCount         int    `json:"adminListCount"`
	MyListCount            int    `json:"myListCount"`
	AdminDetailVerified    bool   `json:"adminDetailVerified"`
	MyDetailSanitized      bool   `json:"myDetailSanitized"`
	ForbiddenAdminVerified bool   `json:"forbiddenAdminVerified"`
	TracePrefix            string `json:"tracePrefix"`
	TargetedOperationLogID string `json:"targetedOperationLogId"`
	AllUsersOperationLogID string `json:"allUsersOperationLogId"`
}

type W2SmokeTakeover struct {
	DefaultRouterRegistered        bool   `json:"defaultRouterRegistered"`
	ExplicitOptInMountWorks        bool   `json:"explicitOptInMountWorks"`
	ProductionTakeoverNotEvaluated bool   `json:"productionTakeoverNotEvaluated"`
	Reason                         string `json:"reason"`
}

type w2OperationLogsSmokeFixture struct {
	AdminID       string
	UserID        string
	AdminToken    string
	UserToken     string
	TracePrefix   string
	TargetedLogID string
	AllUsersLogID string
	cleanup       func(context.Context) error
}

func RunW2OperationLogsSmoke(ctx context.Context, cfg config.Config, out io.Writer) error {
	missing := missingW2OperationLogsSmokeConfig(cfg)
	if len(missing) > 0 {
		return fmt.Errorf("W2 operation logs smoke 缺少必要配置: %v", missing)
	}

	smokeCfg := cfg
	configuredEnabled := cfg.ManagementAPIEnabled
	smokeCfg.ManagementAPIEnabled = true
	if err := smokeCfg.Validate(); err != nil {
		return fmt.Errorf("W2 operation logs smoke 配置无效: %w", err)
	}

	checks := map[string]W2SmokeCheck{"config": okW2SmokeCheck()}
	assessment := defaultW2SmokeTakeover()

	if err := smokeW2OperationLogsDefaultRouterGuard(ctx, cfg); err != nil {
		checks["defaultRouterGuard"] = failedW2SmokeCheck(err.Error())
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	checks["defaultRouterGuard"] = okW2SmokeCheck()

	store, err := openW2OperationLogsSmokeStore(ctx, smokeCfg.PostgresURL)
	if err != nil {
		checks["postgres"] = failedW2SmokeCheck("PostgreSQL 连接失败")
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	defer store.Close()
	checks["postgres"] = okW2SmokeCheck()

	pgPool, err := openW2OperationLogsSmokePool(ctx, smokeCfg.PostgresURL)
	if err != nil {
		checks["postgres"] = failedW2SmokeCheck("PostgreSQL smoke pool 初始化失败")
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	defer pgPool.Close()

	stateRedis, err := openW2OperationLogsSmokeStateRedis(ctx, smokeCfg)
	if err != nil {
		checks["redisState"] = failedW2SmokeCheck("Redis state 连接失败")
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	defer func() { _ = stateRedis.Close() }()
	checks["redisState"] = okW2SmokeCheck()

	if err := smokeW2OperationLogsQueue(ctx, smokeCfg.RedisQueueURL); err != nil {
		checks["redisQueue"] = failedW2SmokeCheck("Redis queue 连接失败")
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	checks["redisQueue"] = okW2SmokeCheck()

	fixture, err := prepareW2OperationLogsSmokeFixture(ctx, pgPool, store)
	if err != nil {
		checks["fixture"] = failedW2SmokeCheck(err.Error())
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			TakeoverAssessment: assessment,
		})
	}
	cleanupFixture := fixture.cleanup
	defer func() {
		if cleanupFixture == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), w2OperationLogsSmokeCleanupTimeout)
		defer cancel()
		_ = cleanupFixture(cleanupCtx)
	}()
	checks["fixture"] = okW2SmokeCheck()

	summary, err := smokeW2OperationLogsRoutes(ctx, smokeCfg, store, fixture)
	if err != nil {
		checks["operationLogsRoute"] = failedW2SmokeCheck(err.Error())
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			OperationLogs:      &summary,
			TakeoverAssessment: assessment,
		})
	}
	checks["operationLogsRoute"] = okW2SmokeCheck()

	cleanupCtx, cancel := context.WithTimeout(ctx, w2OperationLogsSmokeCleanupTimeout)
	if err := cleanupFixture(cleanupCtx); err != nil {
		cancel()
		checks["fixtureCleanup"] = failedW2SmokeCheck("临时 operation log smoke fixture 清理失败")
		return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
			Checks:             checks,
			OperationLogs:      &summary,
			TakeoverAssessment: assessment,
		})
	}
	cancel()
	cleanupFixture = nil
	checks["fixtureCleanup"] = okW2SmokeCheck()
	assessment.ExplicitOptInMountWorks = true

	summary.ConfiguredEnabled = configuredEnabled
	summary.LocalSmokeEnabled = true
	return writeW2OperationLogsSmokeResult(out, W2OperationLogsSmokeResult{
		Success:            true,
		Checks:             checks,
		OperationLogs:      &summary,
		TakeoverAssessment: assessment,
	})
}

func smokeW2OperationLogsDefaultRouterGuard(ctx context.Context, cfg config.Config) error {
	cfg.ManagementAPIEnabled = false
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware: func(next http.Handler) http.Handler {
			return next
		},
		ManagementOperationLogsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
	})

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/__aisys__/api/operation-logs", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		return fmt.Errorf("default operation logs route status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode default operation logs route response: %w", err)
	}
	if got, _ := body["error"].(string); got != w2OperationLogsSmokeDefaultRouteError {
		return fmt.Errorf("default operation logs route error = %q", got)
	}
	return nil
}

func smokeW2OperationLogsRoutes(
	ctx context.Context,
	cfg config.Config,
	store *postgresstore.Store,
	fixture w2OperationLogsSmokeFixture,
) (W2OperationLogsSmokeSummary, error) {
	router := newW2OperationLogsSmokeRouter(cfg, store)

	adminList, err := smokeW2OperationLogsList(ctx, router, "/__aisys__/api/operation-logs?traceId="+fixture.TracePrefix+"&pageSize=10", fixture.AdminToken)
	if err != nil {
		return W2OperationLogsSmokeSummary{}, err
	}
	if len(adminList.Items) != 2 || adminList.Items[0].ID != fixture.AllUsersLogID || adminList.Items[1].ID != fixture.TargetedLogID {
		return W2OperationLogsSmokeSummary{}, fmt.Errorf("admin operation logs list = %+v", adminList.Items)
	}
	for _, item := range adminList.Items {
		if item.ClientIP == "" {
			return W2OperationLogsSmokeSummary{}, fmt.Errorf("admin operation logs list missing clientIp for %s", item.ID)
		}
	}

	myList, err := smokeW2OperationLogsList(ctx, router, "/__aisys__/api/my-operation-logs?traceId="+fixture.TracePrefix+"&pageSize=10", fixture.UserToken)
	if err != nil {
		return W2OperationLogsSmokeSummary{}, err
	}
	if len(myList.Items) != 2 {
		return W2OperationLogsSmokeSummary{}, fmt.Errorf("my operation logs list count = %d", len(myList.Items))
	}
	for _, item := range myList.Items {
		if item.ClientIP != "" {
			return W2OperationLogsSmokeSummary{}, fmt.Errorf("my operation logs list leaked clientIp for %s", item.ID)
		}
	}

	adminDetail, err := smokeW2OperationLogsDetail(ctx, router, "/__aisys__/api/operation-logs/"+fixture.TargetedLogID, fixture.AdminToken)
	if err != nil {
		return W2OperationLogsSmokeSummary{}, err
	}
	if adminDetail.ClientIP == "" || adminDetail.UserAgent == "" || len(adminDetail.Changes) == 0 || len(adminDetail.Targets) == 0 || len(adminDetail.Viewers) == 0 {
		return W2OperationLogsSmokeSummary{}, fmt.Errorf("admin operation log detail incomplete: %+v", adminDetail)
	}

	myDetail, err := smokeW2OperationLogsDetail(ctx, router, "/__aisys__/api/my-operation-logs/"+fixture.TargetedLogID, fixture.UserToken)
	if err != nil {
		return W2OperationLogsSmokeSummary{}, err
	}
	if myDetail.ClientIP != "" || myDetail.UserAgent != "" || myDetail.Method != "" || myDetail.StatusCode != nil ||
		len(myDetail.Changes) != 0 || len(myDetail.Targets) != 0 || len(myDetail.Viewers) != 0 {
		return W2OperationLogsSmokeSummary{}, fmt.Errorf("my operation log detail not sanitized: %+v", myDetail)
	}

	if err := smokeW2OperationLogsForbiddenAdmin(ctx, router, fixture.UserToken); err != nil {
		return W2OperationLogsSmokeSummary{}, err
	}

	return W2OperationLogsSmokeSummary{
		LocalSmokeEnabled:      true,
		AdminListCount:         len(adminList.Items),
		MyListCount:            len(myList.Items),
		AdminDetailVerified:    true,
		MyDetailSanitized:      true,
		ForbiddenAdminVerified: true,
		TracePrefix:            fixture.TracePrefix,
		TargetedOperationLogID: fixture.TargetedLogID,
		AllUsersOperationLogID: fixture.AllUsersLogID,
	}, nil
}

func newW2OperationLogsSmokeRouter(cfg config.Config, store *postgresstore.Store) http.Handler {
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store})
	service := managementoperationlogs.NewService(store)
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           slog.New(slog.NewTextHandler(io.Discard, nil)),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementOperationLogsHandler:   httpapi.NewManagementOperationLogsHandler(service),
		ManagementMyOperationLogsHandler: httpapi.NewManagementMyOperationLogsHandler(service),
	})
}

func smokeW2OperationLogsList(ctx context.Context, router http.Handler, target string, token string) (managementoperationlogs.ListResult, error) {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+token)
	req.Header.Set("User-Agent", w2OperationLogsSmokeUserAgent)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return managementoperationlogs.ListResult{}, fmt.Errorf("operation logs list %s status = %d body = %s", target, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		return managementoperationlogs.ListResult{}, fmt.Errorf("operation logs list Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(token)) {
		return managementoperationlogs.ListResult{}, fmt.Errorf("operation logs list response leaked session token")
	}
	return decodeW2SmokeData[managementoperationlogs.ListResult](rec)
}

func smokeW2OperationLogsDetail(ctx context.Context, router http.Handler, target string, token string) (managementoperationlogs.Detail, error) {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+token)
	req.Header.Set("User-Agent", w2OperationLogsSmokeUserAgent)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return managementoperationlogs.Detail{}, fmt.Errorf("operation logs detail %s status = %d body = %s", target, rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(token)) {
		return managementoperationlogs.Detail{}, fmt.Errorf("operation logs detail response leaked session token")
	}
	return decodeW2SmokeData[managementoperationlogs.Detail](rec)
}

func smokeW2OperationLogsForbiddenAdmin(ctx context.Context, router http.Handler, userToken string) error {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/__aisys__/api/operation-logs", nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+userToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		return fmt.Errorf("ordinary user admin operation logs status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode forbidden operation logs response: %w", err)
	}
	if body["message"] != "需要管理员权限" {
		return fmt.Errorf("forbidden operation logs message = %q", body["message"])
	}
	return nil
}

func decodeW2SmokeData[T any](rec *httptest.ResponseRecorder) (T, error) {
	var zero T
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		return zero, fmt.Errorf("decode response: %w", err)
	}
	return envelope.Data, nil
}

func prepareW2OperationLogsSmokeFixture(
	ctx context.Context,
	db *pgxpool.Pool,
	store *postgresstore.Store,
) (w2OperationLogsSmokeFixture, error) {
	if err := cleanupStaleW2OperationLogsSmokeFixtures(ctx, db); err != nil {
		return w2OperationLogsSmokeFixture{}, fmt.Errorf("临时 operation log smoke fixture 清理失败")
	}

	suffix := uuidNoDash()[:12]
	now := time.Now().UTC()
	adminID := "sys_" + w2OperationLogsSmokeIDPrefix + "admin_" + suffix
	userID := "sys_" + w2OperationLogsSmokeIDPrefix + "user_" + suffix
	adminToken := w2OperationLogsSmokeIDPrefix + "admin_token_" + suffix
	userToken := w2OperationLogsSmokeIDPrefix + "user_token_" + suffix
	adminSessionID := "sess_" + w2OperationLogsSmokeIDPrefix + "admin_" + suffix
	userSessionID := "sess_" + w2OperationLogsSmokeIDPrefix + "user_" + suffix
	targetedLogID := w2OperationLogsSmokeLogIDPrefix + "targeted_" + suffix
	allUsersLogID := w2OperationLogsSmokeLogIDPrefix + "all_users_" + suffix
	tracePrefix := w2OperationLogsSmokeTracePrefix + suffix

	if err := insertW2OperationLogsSmokeAccount(ctx, db, adminID, "admin_"+w2OperationLogsSmokeIDPrefix+suffix, "W2 Operation Logs Smoke Admin", "admin", now); err != nil {
		return w2OperationLogsSmokeFixture{}, err
	}
	if err := insertW2OperationLogsSmokeAccount(ctx, db, userID, "user_"+w2OperationLogsSmokeIDPrefix+suffix, "W2 Operation Logs Smoke User", "user", now); err != nil {
		return w2OperationLogsSmokeFixture{}, err
	}
	if err := insertW2OperationLogsSmokeSession(ctx, db, adminSessionID, adminID, adminToken, now); err != nil {
		return w2OperationLogsSmokeFixture{}, err
	}
	if err := insertW2OperationLogsSmokeSession(ctx, db, userSessionID, userID, userToken, now); err != nil {
		return w2OperationLogsSmokeFixture{}, err
	}
	if err := insertW2OperationLogsSmokeLogs(ctx, store, w2OperationLogsSmokeFixture{
		AdminID:       adminID,
		UserID:        userID,
		TracePrefix:   tracePrefix,
		TargetedLogID: targetedLogID,
		AllUsersLogID: allUsersLogID,
	}, now); err != nil {
		return w2OperationLogsSmokeFixture{}, err
	}

	return w2OperationLogsSmokeFixture{
		AdminID:       adminID,
		UserID:        userID,
		AdminToken:    adminToken,
		UserToken:     userToken,
		TracePrefix:   tracePrefix,
		TargetedLogID: targetedLogID,
		AllUsersLogID: allUsersLogID,
		cleanup: func(cleanupCtx context.Context) error {
			return cleanupW2OperationLogsSmokeFixture(cleanupCtx, db)
		},
	}, nil
}

func insertW2OperationLogsSmokeAccount(ctx context.Context, db *pgxpool.Pool, id string, username string, displayName string, role string, now time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			$1, $2, $3, NULL, $4, 'active', 'smoke_hash',
			false, false, $5, $6
		)
	`, id, username, displayName, role, now, now)
	if err != nil {
		return fmt.Errorf("insert operation logs smoke account %s: %w", id, err)
	}
	return nil
}

func insertW2OperationLogsSmokeSession(ctx context.Context, db *pgxpool.Pool, id string, accountID string, token string, now time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO juhe_business.system_sessions (
			id, system_account_id, token_hash, expires_at, created_at, last_seen_at
		) VALUES (
			$1, $2, $3, $4, $5, $6
		)
	`, id, accountID, managementauth.HashSessionToken(token), now.Add(w2OperationLogsSmokeFixtureTTL), now, now)
	if err != nil {
		return fmt.Errorf("insert operation logs smoke session %s: %w", id, err)
	}
	return nil
}

func insertW2OperationLogsSmokeLogs(ctx context.Context, store *postgresstore.Store, fixture w2OperationLogsSmokeFixture, now time.Time) error {
	status := 200
	if err := store.InsertOperationLog(ctx, port.OperationLogInput{
		ID:                            fixture.TargetedLogID,
		TraceID:                       fixture.TracePrefix + "_targeted",
		ActorSystemAccountID:          fixture.AdminID,
		ActorUsername:                 "w2-operation-logs-smoke-admin",
		ActorDisplayName:              "W2 Operation Logs Smoke Admin",
		ActorRole:                     "admin",
		OperationScopeSystemAccountID: fixture.UserID,
		Mode:                          "admin",
		Module:                        "accounts",
		Action:                        "update_tags",
		OperationKey:                  "accounts.update_tags",
		ResourceType:                  "account",
		ResourceID:                    "acct_" + w2OperationLogsSmokeIDPrefix + fixture.TargetedLogID[len(w2OperationLogsSmokeLogIDPrefix):],
		ResourceName:                  "W2 Operation Logs Smoke Account",
		Summary:                       "W2 operation logs smoke 更新账户标签",
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       []port.OperationLogChange{{Field: "tags", Label: "标签", Before: []string{"旧"}, After: []string{"新"}}},
		Metadata:                      map[string]any{"source": "w2-operation-logs-smoke"},
		Method:                        "PATCH",
		Path:                          "/__aisys__/api/accounts/smoke/tags",
		StatusCode:                    &status,
		ClientIP:                      "198.18.1.10",
		UserAgent:                     w2OperationLogsSmokeUserAgent,
		Targets: []port.OperationLogTargetInput{{
			TargetType:                 "account",
			TargetID:                   "acct_" + w2OperationLogsSmokeIDPrefix + "targeted",
			TargetName:                 "W2 Operation Logs Smoke Account",
			TargetOwnerSystemAccountID: fixture.UserID,
			Relation:                   "primary",
		}},
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  fixture.UserID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "summary",
		}},
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("insert targeted operation log: %w", err)
	}

	if err := store.InsertOperationLog(ctx, port.OperationLogInput{
		ID:                   fixture.AllUsersLogID,
		TraceID:              fixture.TracePrefix + "_all_users",
		ActorSystemAccountID: fixture.AdminID,
		ActorUsername:        "w2-operation-logs-smoke-admin",
		ActorDisplayName:     "W2 Operation Logs Smoke Admin",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "system",
		Action:               "notice",
		OperationKey:         "system.notice",
		ResourceType:         "system",
		Summary:              "W2 operation logs smoke 全员摘要",
		DetailLevel:          "full",
		VisibilityScope:      "all_users",
		Metadata:             map[string]any{"source": "w2-operation-logs-smoke"},
		Method:               "POST",
		Path:                 "/__aisys__/api/notices",
		StatusCode:           &status,
		ClientIP:             "198.18.1.11",
		UserAgent:            w2OperationLogsSmokeUserAgent,
		CreatedAt:            now.Add(time.Minute),
	}); err != nil {
		return fmt.Errorf("insert all-users operation log: %w", err)
	}
	return nil
}

func cleanupStaleW2OperationLogsSmokeFixtures(ctx context.Context, db *pgxpool.Pool) error {
	return cleanupW2OperationLogsSmokeFixture(ctx, db)
}

func cleanupW2OperationLogsSmokeFixture(ctx context.Context, db *pgxpool.Pool) error {
	for _, item := range []struct {
		statement string
		prefix    string
	}{
		{statement: `DELETE FROM juhe_dataset.operation_logs WHERE id LIKE $1 ESCAPE '\'`, prefix: w2OperationLogsSmokeLogIDPrefix},
		{statement: `DELETE FROM juhe_business.system_sessions WHERE id LIKE $1 ESCAPE '\'`, prefix: "sess_" + w2OperationLogsSmokeIDPrefix},
		{statement: `DELETE FROM juhe_business.system_accounts WHERE id LIKE $1 ESCAPE '\'`, prefix: "sys_" + w2OperationLogsSmokeIDPrefix},
	} {
		if _, err := db.Exec(ctx, item.statement, escapedW2SmokeLikePrefix(item.prefix)); err != nil {
			return err
		}
	}
	return nil
}

func escapedW2SmokeLikePrefix(prefix string) string {
	escaped := strings.ReplaceAll(prefix, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `%`, `\%`)
	escaped = strings.ReplaceAll(escaped, `_`, `\_`)
	return escaped + "%"
}

func openW2OperationLogsSmokeStore(ctx context.Context, rawURL string) (*postgresstore.Store, error) {
	store, err := postgresstore.Open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w2OperationLogsSmokeDependencyTimeout)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func openW2OperationLogsSmokePool(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w2OperationLogsSmokeDependencyTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func openW2OperationLogsSmokeStateRedis(ctx context.Context, cfg config.Config) (*redisplatform.Client, error) {
	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, w2OperationLogsSmokeDependencyTimeout)
	defer cancel()
	if err := stateRedis.Ping(pingCtx); err != nil {
		_ = stateRedis.Close()
		return nil, err
	}
	return stateRedis, nil
}

func smokeW2OperationLogsQueue(ctx context.Context, rawURL string) error {
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

func writeW2OperationLogsSmokeResult(out io.Writer, result W2OperationLogsSmokeResult) error {
	if result.Scope == "" {
		result.Scope = w2OperationLogsSmokeScope
	}
	result.TakeoverEvidence = false
	if result.Checks == nil {
		result.Checks = map[string]W2SmokeCheck{}
	}
	result.TakeoverAssessment.ProductionTakeoverNotEvaluated = true
	if result.TakeoverAssessment.Reason == "" {
		result.TakeoverAssessment.Reason = w2OperationLogsSmokeTakeoverReason
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("W2 operation logs smoke 未通过")
	}
	return nil
}

func defaultW2SmokeTakeover() W2SmokeTakeover {
	return W2SmokeTakeover{
		ProductionTakeoverNotEvaluated: true,
		Reason:                         w2OperationLogsSmokeTakeoverReason,
	}
}

func okW2SmokeCheck() W2SmokeCheck {
	return W2SmokeCheck{Status: "ok"}
}

func failedW2SmokeCheck(message string) W2SmokeCheck {
	return W2SmokeCheck{Status: "error", Error: message}
}

func missingW2OperationLogsSmokeConfig(cfg config.Config) []string {
	var missing []string
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		missing = append(missing, "JUHE_AI_POSTGRES_URL")
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		missing = append(missing, "JUHE_AI_REDIS_STATE_URL")
	}
	if strings.TrimSpace(cfg.RedisQueueURL) == "" {
		missing = append(missing, "JUHE_AI_REDIS_QUEUE_URL")
	}
	return missing
}
