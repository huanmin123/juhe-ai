//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	w6ManagementClientIPStatsNamespace = "w6-management-client-ip-stats-list"
	w6ManagementClientIPStatsAdminID   = "sys_w6_client_ip_stats_admin"
	w6ManagementClientIPStatsSessionID = "sess_w6_client_ip_stats_admin"
	w6ManagementClientIPStatsToken     = "w6-client-ip-stats-admin-session"
	w6ManagementClientIPStatsRemoteIP  = "198.51.100.250"
	w6ManagementClientIPStatsStartDate = "2026-07-14"
	w6ManagementClientIPStatsEndDate   = "2026-07-14"

	w6ManagementClientIPStatsNormalHash      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	w6ManagementClientIPStatsBlacklistHash   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	w6ManagementClientIPStatsAllowlistHash   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	w6ManagementClientIPStatsExpiredHash     = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	w6ManagementClientIPStatsFallbackHash    = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	w6ManagementClientIPStatsEmptyMetricHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func TestW6ManagementClientIPStatsListPostgresRedisSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, postgresContainer)
	}()

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)
	assertW6ManagementClientIPStatsMigration(t, ctx, db)

	redisContainer, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, redisContainer)
	}()
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisURL, w6ManagementClientIPStatsNamespace+":state")
	if err != nil {
		t.Fatalf("open redis state client: %v", err)
	}
	defer closeRedisClient(t, stateRedis)

	now := time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC)
	sessionLastSeenAt := now.Add(-10 * time.Minute)
	insertW6ManagementClientIPStatsFixtures(t, ctx, db, now, sessionLastSeenAt)
	for _, table := range []string{
		"client_ip_registry",
		"client_ip_usage_range_windows",
		"client_ip_policies",
	} {
		if _, err := db.ExecContext(ctx, "ANALYZE juhe_stats."+table); err != nil {
			t.Fatalf("analyze client IP stats fixture table %s: %v", table, err)
		}
	}

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementclientipstats.NewServiceWithOptions(
		managementclientipstats.ServiceOptions{
			ListReader:               store,
			UsageStatsTimezoneReader: store,
			Now:                      func() time.Time { return now },
		},
	)
	ipLimiter := &w6ManagementClientIPStatsIPRateLimiter{
		delegate: httpapi.NewRedisSystemAPIIPRateLimiter(
			stateRedis,
			w6ManagementClientIPStatsNamespace,
		),
	}
	authenticatedLimiter := &w6ManagementClientIPStatsAuthenticatedRateLimiter{
		delegate: httpapi.NewRedisSystemAPIAuthenticatedRateLimiter(
			stateRedis,
			w6ManagementClientIPStatsNamespace,
		),
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		Logger:                            slog.New(slog.NewTextHandler(io.Discard, nil)),
		SystemAPIRateLimitReader:          store,
		SystemAPIClientIPAllowlistReader:  store,
		SystemAPIIPRateLimiter:            ipLimiter,
		SystemAPIAuthenticatedRateLimiter: authenticatedLimiter,
		ManagementAPIAuthMiddleware:       httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:  httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementClientIPStatsHandler:    httpapi.NewManagementClientIPStatsHandler(service),
	})

	beforeLastSeenAt := w6ManagementClientIPStatsSessionLastSeenAt(t, ctx, db)
	if !beforeLastSeenAt.Equal(sessionLastSeenAt) {
		t.Fatalf("session last_seen_at before reads = %s, want %s", beforeLastSeenAt, sessionLastSeenAt)
	}

	requestCount := int64(0)
	request := func(rawQuery string) w6ManagementClientIPStatsResponse {
		requestCount++
		return requestW6ManagementClientIPStats(t, ctx, router, rawQuery)
	}

	deleteW6ManagementClientIPStatsRangeState(t, ctx, db)
	assertW6ManagementClientIPStatsAvailable(
		t,
		request("page=1&pageSize=2&keyword=203.0.113.1"),
	)

	setW6ManagementClientIPStatsRangeState(t, ctx, db, nil)
	assertW6ManagementClientIPStatsUnavailable(t, request("page=1&pageSize=2"))
	emptySuccess := ""
	setW6ManagementClientIPStatsRangeState(t, ctx, db, &emptySuccess)
	assertW6ManagementClientIPStatsUnavailable(t, request("page=1&pageSize=2"))

	successAt := formatW6ManagementClientIPStatsTime(now.Add(-time.Minute))
	setW6ManagementClientIPStatsRangeState(t, ctx, db, &successAt)
	insertW6ManagementClientIPStatsDirtyMarker(
		t,
		ctx,
		db,
		"client_ip_range_window_dirty_ips",
		w6ManagementClientIPStatsNormalHash,
		now,
	)
	assertW6ManagementClientIPStatsUnavailable(t, request("page=1&pageSize=2"))
	deleteW6ManagementClientIPStatsDirtyMarkers(t, ctx, db)
	insertW6ManagementClientIPStatsDirtyMarker(
		t,
		ctx,
		db,
		"client_ip_account_range_window_dirty_ips",
		w6ManagementClientIPStatsAllowlistHash,
		now,
	)
	assertW6ManagementClientIPStatsUnavailable(t, request("page=1&pageSize=2"))
	deleteW6ManagementClientIPStatsDirtyMarkers(t, ctx, db)

	pageOne := request("page=1&pageSize=2")
	assertW6ManagementClientIPStatsIDs(t, pageOne.Result.Items, []string{
		w6ManagementClientIPStatsNormalHash,
		w6ManagementClientIPStatsBlacklistHash,
	})
	if !pageOne.Result.RangeReady || !pageOne.Result.HasMore ||
		pageOne.Result.PageUpperBound != 3 || pageOne.Result.Page != 1 ||
		pageOne.Result.PageSize != 2 {
		t.Fatalf("default first page = %+v", pageOne.Result)
	}
	assertW6ManagementClientIPStatsStoredMetrics(t, pageOne.Result.Items[0])
	explicitRequestCountDesc := request(
		"page=1&pageSize=2&sortField=requestCount&sortOrder=desc",
	)
	assertW6ManagementClientIPStatsIDs(t, explicitRequestCountDesc.Result.Items, []string{
		w6ManagementClientIPStatsNormalHash,
		w6ManagementClientIPStatsBlacklistHash,
	})
	pageTwo := request("page=2&pageSize=2")
	assertW6ManagementClientIPStatsIDs(t, pageTwo.Result.Items, []string{
		w6ManagementClientIPStatsAllowlistHash,
		w6ManagementClientIPStatsExpiredHash,
	})
	if !pageTwo.Result.HasMore || pageTwo.Result.PageUpperBound != 5 {
		t.Fatalf("default second page = %+v", pageTwo.Result)
	}

	lastUsed := request("page=1&pageSize=3&sortField=lastUsedAt&sortOrder=desc")
	assertW6ManagementClientIPStatsIDs(t, lastUsed.Result.Items, []string{
		w6ManagementClientIPStatsBlacklistHash,
		w6ManagementClientIPStatsAllowlistHash,
		w6ManagementClientIPStatsExpiredHash,
	})
	lastUsedAscendingTie := request(
		"page=1&pageSize=20&keyword=198.51.100.2&sortField=lastUsedAt&sortOrder=asc",
	)
	assertW6ManagementClientIPStatsIDs(t, lastUsedAscendingTie.Result.Items, []string{
		w6ManagementClientIPStatsExpiredHash,
		w6ManagementClientIPStatsAllowlistHash,
	})

	prefix := request("page=1&pageSize=20&keyword=203.0.113.1")
	assertW6ManagementClientIPStatsIDs(t, prefix.Result.Items, []string{
		w6ManagementClientIPStatsNormalHash,
		w6ManagementClientIPStatsBlacklistHash,
	})
	notSubstring := request("page=1&pageSize=20&keyword=0.113")
	assertW6ManagementClientIPStatsIDs(t, notSubstring.Result.Items, nil)
	blacklisted := request("page=1&pageSize=20&keyword=203.0.113.&status=blacklisted")
	assertW6ManagementClientIPStatsIDs(t, blacklisted.Result.Items, []string{
		w6ManagementClientIPStatsBlacklistHash,
	})
	if blacklisted.Result.Items[0].Status != "blacklisted" {
		t.Fatalf("blacklisted row status = %q", blacklisted.Result.Items[0].Status)
	}
	allowlisted := request("page=1&pageSize=20&keyword=198.51.100.2&status=allowlisted")
	assertW6ManagementClientIPStatsIDs(t, allowlisted.Result.Items, []string{
		w6ManagementClientIPStatsAllowlistHash,
	})
	if allowlisted.Result.Items[0].Status != "allowlisted" {
		t.Fatalf("allowlisted row status = %q", allowlisted.Result.Items[0].Status)
	}
	normal := request("page=1&pageSize=20&keyword=198.51.100.2&status=normal")
	assertW6ManagementClientIPStatsIDs(t, normal.Result.Items, []string{
		w6ManagementClientIPStatsExpiredHash,
	})
	if normal.Result.Items[0].Status != "normal" {
		t.Fatalf("expired-at-now row status = %q, want normal", normal.Result.Items[0].Status)
	}

	metrics := request("page=1&pageSize=20&keyword=192.0.2.")
	assertW6ManagementClientIPStatsIDs(t, metrics.Result.Items, []string{
		w6ManagementClientIPStatsFallbackHash,
		w6ManagementClientIPStatsEmptyMetricHash,
	})
	assertW6ManagementClientIPStatsOptionalMetrics(t, metrics)

	afterLastSeenAt := w6ManagementClientIPStatsSessionLastSeenAt(t, ctx, db)
	if !afterLastSeenAt.Equal(beforeLastSeenAt) {
		t.Fatalf("read route touched session: before=%s after=%s", beforeLastSeenAt, afterLastSeenAt)
	}
	assertW6ManagementClientIPStatsRateLimiters(
		t,
		ipLimiter,
		authenticatedLimiter,
		requestCount,
	)

	assertW6ManagementClientIPStatsProductionPlans(t, ctx, db, postgresURL, now)
}

type w6ManagementClientIPStatsFixture struct {
	ipHash             string
	aggregateIPKey     string
	lastSeenAt         string
	requestCount       int64
	successCount       int64
	errorCount         int64
	inputTokens        int64
	outputTokens       int64
	cacheReadTokens    int64
	cacheReadCost      float64
	cacheWriteTokens   int64
	cacheWrite1hTokens int64
	cacheWriteCost     float64
	thinkingTokens     int64
	inputImageTokens   int64
	outputImageTokens  int64
	totalCost          float64
	durationSum        int64
	durationCount      int64
	durationMax        int64
	averageDuration    *float64
	firstTokenSum      int64
	firstTokenCount    int64
	averageFirstToken  *float64
	activeDays         int
	lastUsedAt         *string
	lastErrorAt        *string
}

func insertW6ManagementClientIPStatsFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	sessionLastSeenAt time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			$1, 'w6-client-ip-stats-admin', 'W6 Client IP Stats Admin', NULL,
			'admin', 'active', 'hash', false, false, $2, $2
		)
	`, w6ManagementClientIPStatsAdminID, now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert client IP stats admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w6ManagementClientIPStatsSessionID,
		w6ManagementClientIPStatsAdminID,
		w6ManagementClientIPStatsToken,
		sessionLastSeenAt,
	)

	float64Pointer := func(value float64) *float64 { return &value }
	stringPointer := func(value string) *string { return &value }
	fixtures := []w6ManagementClientIPStatsFixture{
		{
			ipHash:             w6ManagementClientIPStatsNormalHash,
			aggregateIPKey:     "203.0.113.10",
			lastSeenAt:         "2026-07-14T08:00:00.000Z",
			requestCount:       90,
			successCount:       80,
			errorCount:         10,
			inputTokens:        100,
			outputTokens:       50,
			cacheReadTokens:    11,
			cacheReadCost:      0.11,
			cacheWriteTokens:   12,
			cacheWrite1hTokens: 4,
			cacheWriteCost:     0.12,
			thinkingTokens:     13,
			inputImageTokens:   14,
			outputImageTokens:  15,
			totalCost:          1.25,
			durationSum:        500,
			durationCount:      2,
			durationMax:        300,
			averageDuration:    float64Pointer(222.25),
			firstTokenSum:      100,
			firstTokenCount:    2,
			averageFirstToken:  float64Pointer(44.5),
			activeDays:         2,
			lastUsedAt:         stringPointer("2026-07-14T10:00:00.000Z"),
			lastErrorAt:        stringPointer("2026-07-14T07:00:00.000Z"),
		},
		{
			ipHash:          w6ManagementClientIPStatsBlacklistHash,
			aggregateIPKey:  "203.0.113.11",
			lastSeenAt:      "2026-07-14T09:00:00.000Z",
			requestCount:    80,
			successCount:    79,
			errorCount:      1,
			inputTokens:     90,
			outputTokens:    40,
			durationSum:     100,
			durationCount:   1,
			durationMax:     100,
			firstTokenSum:   20,
			firstTokenCount: 1,
			activeDays:      1,
			lastUsedAt:      stringPointer("2026-07-14T07:30:00.000Z"),
		},
		{
			ipHash:          w6ManagementClientIPStatsAllowlistHash,
			aggregateIPKey:  "198.51.100.20",
			lastSeenAt:      "2026-07-14T09:00:00.000Z",
			requestCount:    80,
			successCount:    80,
			inputTokens:     80,
			outputTokens:    30,
			durationSum:     90,
			durationCount:   1,
			durationMax:     90,
			firstTokenSum:   15,
			firstTokenCount: 1,
			activeDays:      1,
			lastUsedAt:      stringPointer("2026-07-14T07:00:00.000Z"),
		},
		{
			ipHash:          w6ManagementClientIPStatsExpiredHash,
			aggregateIPKey:  "198.51.100.21",
			lastSeenAt:      "2026-07-14T09:00:00.000Z",
			requestCount:    70,
			successCount:    70,
			inputTokens:     70,
			outputTokens:    20,
			durationSum:     80,
			durationCount:   1,
			durationMax:     80,
			firstTokenSum:   10,
			firstTokenCount: 1,
			activeDays:      1,
			lastUsedAt:      stringPointer("2026-07-14T06:30:00.000Z"),
		},
		{
			ipHash:          w6ManagementClientIPStatsFallbackHash,
			aggregateIPKey:  "192.0.2.50",
			lastSeenAt:      "2026-07-14T07:00:00.000Z",
			requestCount:    60,
			successCount:    55,
			errorCount:      5,
			inputTokens:     60,
			outputTokens:    10,
			durationSum:     90,
			durationCount:   2,
			durationMax:     70,
			firstTokenSum:   15,
			firstTokenCount: 3,
			activeDays:      3,
			lastUsedAt:      stringPointer(""),
			lastErrorAt:     stringPointer(""),
		},
		{
			ipHash:         w6ManagementClientIPStatsEmptyMetricHash,
			aggregateIPKey: "192.0.2.51",
			lastSeenAt:     "2026-07-14T06:00:00.000Z",
			requestCount:   50,
			successCount:   50,
			inputTokens:    50,
			outputTokens:   5,
			durationMax:    99,
			activeDays:     1,
		},
	}

	createdAt := formatW6ManagementClientIPStatsTime(now.Add(-2 * time.Hour))
	updatedAt := formatW6ManagementClientIPStatsTime(now.Add(-time.Minute))
	for index, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_stats.client_ip_registry (
				ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
				first_seen_at, last_seen_at, created_at, updated_at
			) VALUES ($1, $2, $3, $3, 4, $4, $5, $4, $6)
		`,
			fixture.ipHash,
			index+1,
			fixture.aggregateIPKey,
			createdAt,
			fixture.lastSeenAt,
			updatedAt,
		); err != nil {
			t.Fatalf("insert client IP registry fixture %s: %v", fixture.ipHash, err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_stats.client_ip_usage_range_windows (
				ip_hash, start_date, end_date,
				request_count, success_count, error_count,
				input_tokens, output_tokens,
				cache_read_tokens, cache_read_cost_usd,
				cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
				thinking_tokens, input_image_tokens, output_image_tokens,
				total_cost_usd,
				duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
				first_token_ms_sum, first_token_ms_count, average_first_token_ms,
				active_days, last_used_at, last_error_at, updated_at
			) VALUES (
				$1, $2, $3,
				$4, $5, $6,
				$7, $8,
				$9, $10,
				$11, $12, $13,
				$14, $15, $16,
				$17,
				$18, $19, $20, $21,
				$22, $23, $24,
				$25, $26, $27, $28
			)
		`,
			fixture.ipHash,
			w6ManagementClientIPStatsStartDate,
			w6ManagementClientIPStatsEndDate,
			fixture.requestCount,
			fixture.successCount,
			fixture.errorCount,
			fixture.inputTokens,
			fixture.outputTokens,
			fixture.cacheReadTokens,
			fixture.cacheReadCost,
			fixture.cacheWriteTokens,
			fixture.cacheWrite1hTokens,
			fixture.cacheWriteCost,
			fixture.thinkingTokens,
			fixture.inputImageTokens,
			fixture.outputImageTokens,
			fixture.totalCost,
			fixture.durationSum,
			fixture.durationCount,
			fixture.durationMax,
			fixture.averageDuration,
			fixture.firstTokenSum,
			fixture.firstTokenCount,
			fixture.averageFirstToken,
			fixture.activeDays,
			fixture.lastUsedAt,
			fixture.lastErrorAt,
			updatedAt,
		); err != nil {
			t.Fatalf("insert client IP range fixture %s: %v", fixture.ipHash, err)
		}
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.client_ip_registry (
			ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
			first_seen_at, last_seen_at, created_at, updated_at
		)
		SELECT
			lpad(to_hex(value), 64, '0'),
			value % 256,
			'198.18.' || ((value - 1) / 250)::text || '.' || (((value - 1) % 250) + 1)::text,
			'198.18.' || ((value - 1) / 250)::text || '.' || (((value - 1) % 250) + 1)::text,
			4,
			'2026-07-13T00:00:00.000Z',
			'2026-07-13T00:00:00.000Z',
			'2026-07-13T00:00:00.000Z',
			'2026-07-14T09:29:00.000Z'
		FROM generate_series(1, 1500) AS value
	`); err != nil {
		t.Fatalf("insert bulk client IP registry fixtures: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.client_ip_usage_range_windows (
			ip_hash, start_date, end_date,
			request_count, success_count, error_count,
			input_tokens, output_tokens, active_days, updated_at
		)
		SELECT
			lpad(to_hex(value), 64, '0'),
			$1,
			$2,
			1 + (value % 40),
			1 + (value % 40),
			0,
			value,
			value % 10,
			1,
			'2026-07-14T09:29:00.000Z'
		FROM generate_series(1, 1500) AS value
	`, w6ManagementClientIPStatsStartDate, w6ManagementClientIPStatsEndDate); err != nil {
		t.Fatalf("insert bulk client IP range fixtures: %v", err)
	}

	policyNow := formatW6ManagementClientIPStatsTime(now)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.client_ip_policies (
			id, ip_hash, policy_type, status, reason, expires_at,
			created_by_system_account_id, created_at, updated_at,
			disabled_at, disabled_by_system_account_id, disabled_reason
		) VALUES
			(
				'ip_policy_w6_stats_blacklist', $1, 'blacklist', 'active',
				'active blacklist fixture', NULL, $4, $5, $5, NULL, NULL, NULL
			),
			(
				'ip_policy_w6_stats_allowlist', $2, 'allowlist', 'active',
				'active allowlist fixture', $6, $4, $5, $5, NULL, NULL, NULL
			),
			(
				'ip_policy_w6_stats_expired_at_now', $3, 'blacklist', 'active',
				'expiry equals policy now', $7, $4, $5, $5, NULL, NULL, NULL
			)
	`,
		w6ManagementClientIPStatsBlacklistHash,
		w6ManagementClientIPStatsAllowlistHash,
		w6ManagementClientIPStatsExpiredHash,
		w6ManagementClientIPStatsAdminID,
		createdAt,
		formatW6ManagementClientIPStatsTime(now.Add(time.Hour)),
		policyNow,
	); err != nil {
		t.Fatalf("insert client IP policy fixtures: %v", err)
	}

	successAt := formatW6ManagementClientIPStatsTime(now.Add(-time.Minute))
	setW6ManagementClientIPStatsRangeState(t, ctx, db, &successAt)
}

func assertW6ManagementClientIPStatsMigration(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var migrationApplied bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM goose_db_version
			WHERE version_id = 40
			  AND is_applied
		)
	`).Scan(&migrationApplied); err != nil {
		t.Fatalf("inspect goose migration 000040: %v", err)
	}
	if !migrationApplied {
		t.Fatal("fresh migrations did not apply 000040")
	}

	objects := []string{
		"juhe_stats.client_ip_usage_range_windows",
		"juhe_stats.client_ip_range_window_dirty_ips",
		"juhe_stats.client_ip_account_range_window_dirty_ips",
		"juhe_stats.idx_client_ip_range_requests",
		"juhe_stats.idx_client_ip_registry_aggregate_ip_key_c",
		"juhe_stats.idx_client_ip_registry_client_ip_c",
	}
	for _, object := range objects {
		var regclass sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, object).Scan(&regclass); err != nil {
			t.Fatalf("inspect migrated object %s: %v", object, err)
		}
		if !regclass.Valid || regclass.String == "" {
			t.Fatalf("fresh migrations are missing %s", object)
		}
	}
}

func setW6ManagementClientIPStatsRangeState(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	lastSuccessAt *string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_stats.stats_job_state (
			scope_type, scope_id, job_name,
			cursor_created_at, cursor_id, last_success_at,
			last_error_message, lag_seconds, updated_at
		) VALUES (
			'client_ip_range_window', $1, 'client_ip_range_window_refresh',
			NULL, NULL, $2, NULL, 0, '2026-07-14T09:29:00.000Z'
		)
		ON CONFLICT (scope_type, scope_id, job_name) DO UPDATE SET
			last_success_at = EXCLUDED.last_success_at,
			last_error_message = EXCLUDED.last_error_message,
			lag_seconds = EXCLUDED.lag_seconds,
			updated_at = EXCLUDED.updated_at
	`,
		w6ManagementClientIPStatsStartDate+":"+w6ManagementClientIPStatsEndDate,
		lastSuccessAt,
	); err != nil {
		t.Fatalf("upsert client IP range state: %v", err)
	}
}

func deleteW6ManagementClientIPStatsRangeState(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_stats.stats_job_state
		WHERE scope_type = 'client_ip_range_window'
		  AND scope_id = $1
		  AND job_name = 'client_ip_range_window_refresh'
	`, w6ManagementClientIPStatsStartDate+":"+w6ManagementClientIPStatsEndDate); err != nil {
		t.Fatalf("delete client IP range state: %v", err)
	}
}

func insertW6ManagementClientIPStatsDirtyMarker(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	table string,
	ipHash string,
	now time.Time,
) {
	t.Helper()
	if table != "client_ip_range_window_dirty_ips" &&
		table != "client_ip_account_range_window_dirty_ips" {
		t.Fatalf("unsupported dirty marker table %q", table)
	}
	query := fmt.Sprintf(
		"INSERT INTO juhe_stats.%s (ip_hash, updated_at) VALUES ($1, $2)",
		table,
	)
	if _, err := db.ExecContext(ctx, query, ipHash, formatW6ManagementClientIPStatsTime(now)); err != nil {
		t.Fatalf("insert %s marker: %v", table, err)
	}
}

func deleteW6ManagementClientIPStatsDirtyMarkers(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range []string{
		"client_ip_range_window_dirty_ips",
		"client_ip_account_range_window_dirty_ips",
	} {
		if _, err := db.ExecContext(ctx, "DELETE FROM juhe_stats."+table); err != nil {
			t.Fatalf("delete client IP dirty markers from %s: %v", table, err)
		}
	}
}

type w6ManagementClientIPStatsResponse struct {
	Result managementclientipstats.ListResult
	Raw    map[string]any
}

func requestW6ManagementClientIPStats(
	t *testing.T,
	ctx context.Context,
	router http.Handler,
	rawQuery string,
) w6ManagementClientIPStatsResponse {
	t.Helper()
	path := "/__aisys__/api/ip-stats"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+w6ManagementClientIPStatsToken)
	req.Header.Set("User-Agent", "w6-management-client-ip-stats-list-smoke")
	req.RemoteAddr = w6ManagementClientIPStatsRemoteIP + ":12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", path, got)
	}

	body := rec.Body.Bytes()
	var typed struct {
		Data managementclientipstats.ListResult `json:"data"`
	}
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("decode typed GET %s response: %v", path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw GET %s response: %v", path, err)
	}
	return w6ManagementClientIPStatsResponse{Result: typed.Data, Raw: raw}
}

func assertW6ManagementClientIPStatsAvailable(
	t *testing.T,
	response w6ManagementClientIPStatsResponse,
) {
	t.Helper()
	if !response.Result.RangeReady || len(response.Result.Items) == 0 {
		t.Fatalf("range fallback response = %+v, want ready rows", response.Result)
	}
}

func assertW6ManagementClientIPStatsUnavailable(
	t *testing.T,
	response w6ManagementClientIPStatsResponse,
) {
	t.Helper()
	if response.Result.RangeReady || len(response.Result.Items) != 0 ||
		response.Result.PageUpperBound != 0 || response.Result.HasMore {
		t.Fatalf("unready range response = %+v", response.Result)
	}
	if response.Result.Page != 1 || response.Result.PageSize != 2 ||
		response.Result.Range.StartDate != w6ManagementClientIPStatsStartDate ||
		response.Result.Range.EndDate != w6ManagementClientIPStatsEndDate {
		t.Fatalf("unready range metadata = %+v", response.Result)
	}
}

func assertW6ManagementClientIPStatsIDs(
	t *testing.T,
	items []managementclientipstats.ListItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.IPHash)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("client IP stats IDs = %v, want %v", got, want)
	}
}

func assertW6ManagementClientIPStatsOptionalMetrics(
	t *testing.T,
	response w6ManagementClientIPStatsResponse,
) {
	t.Helper()
	if len(response.Result.Items) != 2 {
		t.Fatalf("optional metric rows = %d, want 2", len(response.Result.Items))
	}
	fallback := response.Result.Items[0].RangeUsage
	if fallback.AverageDurationMs == nil || *fallback.AverageDurationMs != 45 ||
		fallback.AverageFirstTokenMs == nil || *fallback.AverageFirstTokenMs != 5 ||
		fallback.MaxDurationMs == nil || *fallback.MaxDurationMs != 70 ||
		fallback.LastUsedAt == nil || *fallback.LastUsedAt != "" ||
		fallback.LastErrorAt == nil || *fallback.LastErrorAt != "" {
		t.Fatalf("fallback optional metrics = %+v", fallback)
	}
	empty := response.Result.Items[1].RangeUsage
	if empty.AverageDurationMs != nil || empty.AverageFirstTokenMs != nil ||
		empty.MaxDurationMs != nil || empty.LastUsedAt != nil || empty.LastErrorAt != nil {
		t.Fatalf("empty optional metrics = %+v", empty)
	}

	data, ok := response.Raw["data"].(map[string]any)
	if !ok {
		t.Fatalf("raw data = %#v", response.Raw["data"])
	}
	rawItems, ok := data["items"].([]any)
	if !ok || len(rawItems) != 2 {
		t.Fatalf("raw items = %#v", data["items"])
	}
	emptyItem, ok := rawItems[1].(map[string]any)
	if !ok {
		t.Fatalf("raw empty item = %#v", rawItems[1])
	}
	rangeUsage, ok := emptyItem["rangeUsage"].(map[string]any)
	if !ok {
		t.Fatalf("raw empty range usage = %#v", emptyItem["rangeUsage"])
	}
	for _, field := range []string{
		"averageDurationMs",
		"averageFirstTokenMs",
		"maxDurationMs",
		"lastUsedAt",
		"lastErrorAt",
	} {
		if _, exists := rangeUsage[field]; exists {
			t.Fatalf("raw empty range usage unexpectedly contains %s: %#v", field, rangeUsage)
		}
	}
}

func assertW6ManagementClientIPStatsStoredMetrics(
	t *testing.T,
	item managementclientipstats.ListItem,
) {
	t.Helper()
	usage := item.RangeUsage
	if item.IPHash != w6ManagementClientIPStatsNormalHash ||
		item.LastSeenAt == nil || *item.LastSeenAt != "2026-07-14T08:00:00.000Z" ||
		usage.ErrorRate != float64(10)/float64(90) ||
		usage.TotalTokens != 150 || usage.TotalCost != 1.25 ||
		usage.AverageDurationMs == nil || *usage.AverageDurationMs != 222.25 ||
		usage.AverageFirstTokenMs == nil || *usage.AverageFirstTokenMs != 44.5 ||
		usage.MaxDurationMs == nil || *usage.MaxDurationMs != 300 ||
		usage.LastUsedAt == nil || *usage.LastUsedAt != "2026-07-14T10:00:00.000Z" ||
		usage.LastErrorAt == nil || *usage.LastErrorAt != "2026-07-14T07:00:00.000Z" {
		t.Fatalf("stored client IP usage metrics = %+v", item)
	}
}

func w6ManagementClientIPStatsSessionLastSeenAt(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) time.Time {
	t.Helper()
	var value time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT last_seen_at
		FROM juhe_business.system_sessions
		WHERE id = $1
	`, w6ManagementClientIPStatsSessionID).Scan(&value); err != nil {
		t.Fatalf("read client IP stats session last_seen_at: %v", err)
	}
	return value
}

type w6ManagementClientIPStatsIPRateLimiter struct {
	delegate httpapi.SystemAPIIPRateLimiter
	calls    atomic.Int64
	mu       sync.Mutex
	lastKey  string
	settings httpapi.SystemAPIIPRateLimitSettings
}

func (l *w6ManagementClientIPStatsIPRateLimiter) AllowSystemAPIIP(
	ctx context.Context,
	key string,
	settings httpapi.SystemAPIIPRateLimitSettings,
) (httpapi.SystemAPIRateLimitDecision, error) {
	l.calls.Add(1)
	l.mu.Lock()
	l.lastKey = key
	l.settings = settings
	l.mu.Unlock()
	return l.delegate.AllowSystemAPIIP(ctx, key, settings)
}

type w6ManagementClientIPStatsAuthenticatedRateLimiter struct {
	delegate httpapi.SystemAPIAuthenticatedRateLimiter
	calls    atomic.Int64
	mu       sync.Mutex
	lastKey  string
	limit    int
}

func (l *w6ManagementClientIPStatsAuthenticatedRateLimiter) AllowSystemAPIAuthenticated(
	ctx context.Context,
	key string,
	limit int,
) (httpapi.SystemAPIRateLimitDecision, error) {
	l.calls.Add(1)
	l.mu.Lock()
	l.lastKey = key
	l.limit = limit
	l.mu.Unlock()
	return l.delegate.AllowSystemAPIAuthenticated(ctx, key, limit)
}

func assertW6ManagementClientIPStatsRateLimiters(
	t *testing.T,
	ipLimiter *w6ManagementClientIPStatsIPRateLimiter,
	authenticatedLimiter *w6ManagementClientIPStatsAuthenticatedRateLimiter,
	wantCalls int64,
) {
	t.Helper()
	if got := ipLimiter.calls.Load(); got != wantCalls {
		t.Fatalf("system API IP limiter calls = %d, want %d", got, wantCalls)
	}
	if got := authenticatedLimiter.calls.Load(); got != wantCalls {
		t.Fatalf("system API authenticated limiter calls = %d, want %d", got, wantCalls)
	}
	ipLimiter.mu.Lock()
	ipKey := ipLimiter.lastKey
	ipSettings := ipLimiter.settings
	ipLimiter.mu.Unlock()
	if len(ipKey) != 43 || ipSettings.PerMinute != 600 || ipSettings.BurstPer10Seconds != 120 {
		t.Fatalf("IP limiter call key=%q settings=%+v", ipKey, ipSettings)
	}
	authenticatedLimiter.mu.Lock()
	authenticatedKey := authenticatedLimiter.lastKey
	authenticatedLimit := authenticatedLimiter.limit
	authenticatedLimiter.mu.Unlock()
	if len(authenticatedKey) != 43 || authenticatedLimit != 300 {
		t.Fatalf("authenticated limiter call key=%q limit=%d", authenticatedKey, authenticatedLimit)
	}
}

type w6ManagementClientIPStatsCaptureDB struct {
	delegate *pgxpool.Pool
	mu       sync.Mutex
	query    string
	args     []any
}

func (db *w6ManagementClientIPStatsCaptureDB) Exec(
	ctx context.Context,
	query string,
	args ...any,
) (pgconn.CommandTag, error) {
	return db.delegate.Exec(ctx, query, args...)
}

func (db *w6ManagementClientIPStatsCaptureDB) Query(
	ctx context.Context,
	query string,
	args ...any,
) (pgx.Rows, error) {
	db.mu.Lock()
	db.query = query
	db.args = append([]any(nil), args...)
	db.mu.Unlock()
	return db.delegate.Query(ctx, query, args...)
}

func (db *w6ManagementClientIPStatsCaptureDB) QueryRow(
	ctx context.Context,
	query string,
	args ...any,
) pgx.Row {
	return db.delegate.QueryRow(ctx, query, args...)
}

func (db *w6ManagementClientIPStatsCaptureDB) snapshot() (string, []any) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.query, append([]any(nil), db.args...)
}

func assertW6ManagementClientIPStatsProductionPlans(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	postgresURL string,
	now time.Time,
) {
	t.Helper()
	pool, err := pgxpool.New(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open plan capture pool: %v", err)
	}
	defer pool.Close()
	capture := &w6ManagementClientIPStatsCaptureDB{delegate: pool}
	queries := postgresqueries.New(capture)
	requestCountDescParams := postgresqueries.ListManagementClientIPStatsRequestCountDescParams{
		StatusFilter:     "all",
		RowOffset:        0,
		RowLimit:         21,
		PolicyNow:        formatW6ManagementClientIPStatsTime(now),
		StartDate:        w6ManagementClientIPStatsStartDate,
		EndDate:          w6ManagementClientIPStatsEndDate,
		HasLastUsedRange: false,
		Keyword:          "",
		KeywordUpper:     "",
	}
	var productionQuery string
	for _, scenario := range []string{"default", "explicit requestCount desc"} {
		if _, err := queries.ListManagementClientIPStatsRequestCountDesc(
			ctx,
			requestCountDescParams,
		); err != nil {
			t.Fatalf("execute production sqlc %s query: %v", scenario, err)
		}
		query, args := capture.snapshot()
		assertW6ManagementClientIPStatsStaticRequestCountQuery(t, query)
		if productionQuery == "" {
			productionQuery = query
		} else if query != productionQuery {
			t.Fatalf("%s query did not use the production static SQL", scenario)
		}
		for _, planCacheMode := range []string{"force_custom_plan", "force_generic_plan"} {
			plan := explainW6ManagementClientIPStatsPreparedPlan(
				t,
				ctx,
				db,
				query,
				args,
				planCacheMode,
			)
			if !strings.Contains(plan, "idx_client_ip_range_requests") {
				t.Fatalf(
					"production %s query cannot use idx_client_ip_range_requests under %s: %s",
					scenario,
					planCacheMode,
					plan,
				)
			}
			if strings.Contains(plan, `"Node Type": "Sort"`) {
				t.Fatalf(
					"production %s query adds Sort under %s: %s",
					scenario,
					planCacheMode,
					plan,
				)
			}
		}
	}

	keywordParams := requestCountDescParams
	keywordParams.Keyword = "203.0.113.1"
	keywordParams.KeywordUpper = "203.0.113.2"
	if _, err := queries.ListManagementClientIPStatsRequestCountDesc(ctx, keywordParams); err != nil {
		t.Fatalf("execute production sqlc keyword query: %v", err)
	}
	keywordQuery, keywordArgs := capture.snapshot()
	if keywordQuery != productionQuery {
		t.Fatal("sqlc emitted different SQL text for the keyword query")
	}
	keywordPlan := explainW6ManagementClientIPStatsQuery(
		t,
		ctx,
		db,
		keywordQuery,
		keywordArgs,
		true,
	)
	for _, index := range []string{
		"idx_client_ip_registry_aggregate_ip_key_c",
		"idx_client_ip_registry_client_ip_c",
	} {
		if !strings.Contains(keywordPlan, index) {
			t.Fatalf("production keyword query cannot use %s: %s", index, keywordPlan)
		}
	}
}

func assertW6ManagementClientIPStatsStaticRequestCountQuery(t *testing.T, query string) {
	t.Helper()
	assertW6ManagementClientIPStatsPreaggregatedQuery(t, query)
	for _, required := range []string{
		"-- name: ListManagementClientIPStatsRequestCountDesc :many",
		"ORDER BY request_count DESC, ip_hash ASC",
		"LIMIT $3::int",
		"OFFSET $2::int",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("production static request-count query is missing %q: %s", required, query)
		}
	}
	if strings.Contains(query, "CASE WHEN $") {
		t.Fatalf("production request-count query still has parameterized CASE ordering: %s", query)
	}
}

func assertW6ManagementClientIPStatsPreaggregatedQuery(t *testing.T, query string) {
	t.Helper()
	lower := strings.ToLower(query)
	for _, forbidden := range []string{
		"sum(",
		"count(",
		"group by",
		"usage_records",
		"client_ip_stats_daily",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("production list query scans or aggregates detail via %q: %s", forbidden, query)
		}
	}
	for _, required := range []string{
		"juhe_stats.client_ip_usage_range_windows",
		"juhe_stats.client_ip_registry",
	} {
		if !strings.Contains(lower, strings.ToLower(required)) {
			t.Fatalf("production list query is missing %q: %s", required, query)
		}
	}
}

func explainW6ManagementClientIPStatsQuery(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	query string,
	args []any,
	disableSequentialScan bool,
) string {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin client IP stats explain transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if disableSequentialScan {
		if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			t.Fatalf("disable sequential scan for client IP stats explain: %v", err)
		}
	}
	var plan string
	if err := tx.QueryRowContext(
		ctx,
		"EXPLAIN (FORMAT JSON, COSTS false)\n"+query,
		args...,
	).Scan(&plan); err != nil {
		t.Fatalf("explain production client IP stats query: %v", err)
	}
	return plan
}

func explainW6ManagementClientIPStatsPreparedPlan(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	query string,
	args []any,
	planCacheMode string,
) string {
	t.Helper()
	if len(args) != 11 {
		t.Fatalf("production static client IP stats args = %d, want 11", len(args))
	}
	if planCacheMode != "force_custom_plan" && planCacheMode != "force_generic_plan" {
		t.Fatalf("unsupported client IP stats plan cache mode %q", planCacheMode)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin %s transaction: %v", planCacheMode, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable sequential scan for %s: %v", planCacheMode, err)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL plan_cache_mode = "+planCacheMode); err != nil {
		t.Fatalf("set client IP stats plan cache mode %s: %v", planCacheMode, err)
	}
	prepare := `PREPARE w6_client_ip_stats_plan (
		text, integer, integer, text, text, text,
		boolean, text, text, text, text
	) AS
` + query
	if _, err := tx.ExecContext(ctx, prepare); err != nil {
		t.Fatalf("prepare production client IP stats query under %s: %v", planCacheMode, err)
	}
	literals := make([]string, 0, len(args))
	for _, arg := range args {
		literals = append(literals, w6ManagementClientIPStatsSQLLiteral(t, arg))
	}
	var plan string
	explain := "EXPLAIN (FORMAT JSON, COSTS false) EXECUTE w6_client_ip_stats_plan(" +
		strings.Join(literals, ", ") + ")"
	if err := tx.QueryRowContext(ctx, explain).Scan(&plan); err != nil {
		t.Fatalf("explain production client IP stats query under %s: %v", planCacheMode, err)
	}
	return plan
}

func w6ManagementClientIPStatsSQLLiteral(t *testing.T, value any) string {
	t.Helper()
	switch typed := value.(type) {
	case string:
		return "'" + strings.ReplaceAll(typed, "'", "''") + "'"
	case bool:
		return strconv.FormatBool(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	default:
		t.Fatalf("unsupported client IP stats SQL literal type %T", value)
		return "NULL"
	}
}

func formatW6ManagementClientIPStatsTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

var _ postgresqueries.DBTX = (*w6ManagementClientIPStatsCaptureDB)(nil)
