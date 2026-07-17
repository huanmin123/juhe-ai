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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w4AuthorizationWriteNamespace = "w4-management-authorization-write"

	w4AuthorizationWriteAdminID      = "sys_w4_authorization_write_admin"
	w4AuthorizationWriteOwnerID      = "sys_w4_authorization_write_owner"
	w4AuthorizationWriteGranteeID    = "sys_w4_authorization_write_grantee"
	w4AuthorizationWriteAdminSession = "sess_w4_authorization_write_admin"
	w4AuthorizationWriteAdminToken   = "w4-authorization-write-admin-session-token"
	w4AuthorizationWriteGroupID      = "grp_w4_authorization_write_owner"
	w4AuthorizationWriteGroupName    = "W4 Authorization Owner Group"
	w4AuthorizationWriteLogID        = "oplog_w4_authorization_write_create"
	w4AuthorizationWriteCanary       = "w4-authorization-write-duplicate-canary"
)

func TestW4ManagementAuthorizationWritePostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		postgresContainer *tcpostgres.PostgresContainer
		redisContainer    *tcredis.RedisContainer
		db                *sql.DB
		store             *postgresstore.Store
		stateRedis        *redisplatform.Client
		keyspaceRedis     *goredis.Client
		logClient         *queue.Client
		inspector         *queue.Inspector
		httpServer        *httptest.Server
		stopWorker        context.CancelFunc
		workerDone        chan struct{}
		workerErrMu       sync.Mutex
		workerRunErr      error
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()

		if stopWorker != nil {
			stopWorker()
			select {
			case <-workerDone:
			case <-cleanupCtx.Done():
				t.Errorf("ingest worker shutdown: %v", cleanupCtx.Err())
			}
			workerErrMu.Lock()
			err := workerRunErr
			workerErrMu.Unlock()
			if err != nil {
				t.Errorf("ingest worker run: %v", err)
			}
		}
		if httpServer != nil {
			httpServer.Close()
		}
		if inspector != nil {
			if err := inspector.Close(); err != nil {
				t.Errorf("close queue inspector: %v", err)
			}
		}
		if logClient != nil {
			if err := logClient.Close(); err != nil {
				t.Errorf("close queue client: %v", err)
			}
		}
		if stateRedis != nil {
			if err := stateRedis.Close(); err != nil {
				t.Errorf("close state redis: %v", err)
			}
		}
		if keyspaceRedis != nil {
			if err := keyspaceRedis.Close(); err != nil {
				t.Errorf("close keyspace redis: %v", err)
			}
		}
		if store != nil {
			store.Close()
		}
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close postgres db: %v", err)
			}
		}
		if redisContainer != nil {
			if err := redisContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate redis container: %v", err)
			}
		}
		if postgresContainer != nil {
			if err := postgresContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate postgres container: %v", err)
			}
		}
	})

	var err error
	postgresContainer, err = tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db = openSQLDB(t, postgresURL)
	runGooseMigrations(t, db)
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read Goose version: %v", err)
	}
	if version != 55 {
		t.Fatalf("Goose version = %d, want 55", version)
	}

	redisContainer, err = tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}
	stateRedis, err = redisplatform.NewClient(redisStateURL, w4AuthorizationWriteNamespace+":state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	keyspaceRedisOptions, err := goredis.ParseURL(redisStateURL)
	if err != nil {
		t.Fatalf("parse state redis url for keyspace snapshot: %v", err)
	}
	keyspaceRedis = goredis.NewClient(keyspaceRedisOptions)

	now := time.Date(2026, 7, 17, 10, 30, 0, 0, time.UTC)
	insertW4AuthorizationWriteFixtures(t, ctx, db, now)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w4AuthorizationWriteAdminSession,
		w4AuthorizationWriteAdminID,
		w4AuthorizationWriteAdminToken,
		now.Add(-time.Minute),
	)

	var invalidationCalls int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		State:     stateRedis,
		Namespace: w4AuthorizationWriteNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationCalls++
			return fmt.Sprintf("w4-authorization-write-version-%d", invalidationCalls), nil
		},
	})
	if err != nil {
		t.Fatalf("create authorization invalidator: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	workerCtx, workerCancel := context.WithCancel(ctx)
	stopWorker = workerCancel
	workerDone = make(chan struct{})
	go func() {
		err := app.RunIngestWorker(workerCtx, config.Config{
			PostgresURL:     postgresURL,
			RedisQueueURL:   redisQueueURL,
			RedisNamespace:  "juhe-ai",
			LogLevel:        "error",
			ShutdownTimeout: time.Second,
		}, logger)
		workerErrMu.Lock()
		workerRunErr = err
		workerErrMu.Unlock()
		close(workerDone)
	}()

	logClient = queue.NewClient(redisOpts)
	inspector = queue.NewInspector(redisOpts)
	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		Secret:                   "w4-authorization-write-secret",
		AuthorizationInvalidator: invalidator,
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logIDCalls := 0
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAuthorizationCreateHandler: httpapi.NewManagementAuthorizationCreateHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{
				Config:         cfg,
				Logger:         logger,
				Client:         logClient,
				SettingsReader: store,
				Now:            func() time.Time { return now },
				NewLogID: func() string {
					logIDCalls++
					return w4AuthorizationWriteLogID
				},
			},
		),
	})
	httpServer = httptest.NewServer(router)

	createBody := `{
		"resourceType":"group",
		"resourceId":"` + w4AuthorizationWriteGroupID + `",
		"granteeType":"system_account",
		"granteeId":"` + w4AuthorizationWriteGranteeID + `",
		"remark":"W4 authorization write smoke",
		"limits":{"daily":{"enabled":true,"limit":17}}
	}`
	createResponse := doW4AuthorizationWriteRequest(t, ctx, httpServer.URL, createBody, "req_w4_authorization_write_create")
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("authorization create status = %d, body = %s", createResponse.StatusCode, createResponse.Body)
	}
	created := decodeW4AuthorizationWriteCreated(t, createResponse.Body)
	if created.ID == "" ||
		created.ResourceType != "group" ||
		created.ResourceID != w4AuthorizationWriteGroupID ||
		created.ResourceName != w4AuthorizationWriteGroupName ||
		created.ResourceOwnerSystemAccountID != w4AuthorizationWriteOwnerID ||
		created.GranteeType != "system_account" ||
		created.GranteeSystemAccountID != w4AuthorizationWriteGranteeID ||
		created.Status != "active" ||
		created.CreatedBy != w4AuthorizationWriteAdminID {
		t.Fatalf("created authorization response = %+v", created)
	}

	authFacts := readW4AuthorizationWriteFacts(t, ctx, db)
	assertW4AuthorizationWriteFacts(t, authFacts, created.ID, now)
	assertW4AuthorizationWriteInvalidations(t, ctx, stateRedis, now)
	if invalidationCalls != 2 {
		t.Fatalf("authorization invalidation calls = %d, want 2", invalidationCalls)
	}
	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueBeforeDuplicate := readW4AuthorizationWriteQueueInfo(t, inspector)
	if queueBeforeDuplicate.Completed != 1 {
		t.Fatalf("operation log queue completed = %d, want 1", queueBeforeDuplicate.Completed)
	}
	assertW4AuthorizationWriteOperationLog(t, ctx, db, created, now)
	countsBeforeDuplicate := readW4AuthorizationWriteCounts(t, ctx, db)
	if countsBeforeDuplicate != (w4AuthorizationWriteCounts{
		Grants:              1,
		Runtime:             1,
		Sources:             1,
		Dirty:               1,
		OperationLogs:       1,
		OperationLogTargets: 3,
		OperationLogViewers: 3,
		OperationLogTerms:   countsBeforeDuplicate.OperationLogTerms,
	}) || countsBeforeDuplicate.OperationLogTerms == 0 {
		t.Fatalf("authorization write counts = %+v", countsBeforeDuplicate)
	}
	businessRowsBeforeDuplicate := readW4AuthorizationWriteBusinessRows(t, ctx, db)
	redisKeyspaceBeforeDuplicate := readW4AuthorizationWriteRedisKeyspace(t, ctx, keyspaceRedis)
	if len(redisKeyspaceBeforeDuplicate) != 2 {
		t.Fatalf("authorization Redis namespace keyspace = %+v, want exactly 2 invalidation keys", redisKeyspaceBeforeDuplicate)
	}
	assertW4AuthorizationWriteSessionStorage(t, ctx, db)

	duplicateBody := strings.Replace(createBody, "W4 authorization write smoke", w4AuthorizationWriteCanary, 1)
	duplicateResponse := doW4AuthorizationWriteRequest(t, ctx, httpServer.URL, duplicateBody, "req_w4_authorization_write_duplicate")
	if duplicateResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate authorization status = %d, want 400; body = %s", duplicateResponse.StatusCode, duplicateResponse.Body)
	}
	var duplicateEnvelope map[string]string
	if err := json.Unmarshal([]byte(duplicateResponse.Body), &duplicateEnvelope); err != nil {
		t.Fatalf("decode duplicate authorization response: %v", err)
	}
	const duplicateMessage = "该资源已授权给该用户，请勿重复授权"
	if len(duplicateEnvelope) != 1 || duplicateEnvelope["message"] != duplicateMessage {
		t.Fatalf("duplicate authorization response = %+v, want exact message %q", duplicateEnvelope, duplicateMessage)
	}

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueAfterDuplicate := readW4AuthorizationWriteQueueInfo(t, inspector)
	if queueAfterDuplicate != queueBeforeDuplicate {
		t.Fatalf("operation log queue changed after duplicate: before=%+v after=%+v", queueBeforeDuplicate, queueAfterDuplicate)
	}
	if got := readW4AuthorizationWriteCounts(t, ctx, db); got != countsBeforeDuplicate {
		t.Fatalf("authorization business/log counts changed after duplicate: before=%+v after=%+v", countsBeforeDuplicate, got)
	}
	if got := readW4AuthorizationWriteBusinessRows(t, ctx, db); !reflect.DeepEqual(got, businessRowsBeforeDuplicate) {
		t.Fatalf("authorization business rows changed after duplicate:\nbefore=%+v\nafter=%+v", businessRowsBeforeDuplicate, got)
	}
	if got := readW4AuthorizationWriteRedisKeyspace(t, ctx, keyspaceRedis); !reflect.DeepEqual(got, redisKeyspaceBeforeDuplicate) {
		t.Fatalf("authorization Redis namespace changed after duplicate:\nbefore=%+v\nafter=%+v", redisKeyspaceBeforeDuplicate, got)
	}
	if invalidationCalls != 2 || logIDCalls != 1 {
		t.Fatalf("duplicate side-effect calls: invalidations=%d logIDs=%d, want 2 and 1", invalidationCalls, logIDCalls)
	}
	assertW4AuthorizationWriteSensitiveValuesAbsent(t, ctx, db, w4AuthorizationWriteCanary, w4AuthorizationWriteAdminToken)
	assertW4AuthorizationWriteSessionStorage(t, ctx, db)
}

type w4AuthorizationWriteHTTPResponse struct {
	StatusCode int
	Body       string
}

func doW4AuthorizationWriteRequest(
	t *testing.T,
	ctx context.Context,
	serverURL string,
	body string,
	requestID string,
) w4AuthorizationWriteHTTPResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		serverURL+"/__aisys__/api/authorizations?systemAccountId="+w4AuthorizationWriteOwnerID,
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("create authorization HTTP request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: w4AuthorizationWriteAdminToken})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w4-management-authorization-write-smoke")
	req.Header.Set("X-Request-Id", requestID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute authorization HTTP request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read authorization HTTP response: %v", err)
	}
	return w4AuthorizationWriteHTTPResponse{StatusCode: resp.StatusCode, Body: string(raw)}
}

func decodeW4AuthorizationWriteCreated(t *testing.T, body string) managementauthorizations.Summary {
	t.Helper()
	var envelope struct {
		Data managementauthorizations.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode authorization create response: %v", err)
	}
	return envelope.Data
}

func insertW4AuthorizationWriteFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			($1, 'w4-authorization-admin', 'W4 Authorization Admin', NULL, 'admin', 'active', 'hash', false, false, $4, $4),
			($2, 'w4-authorization-owner', 'W4 Authorization Owner', NULL, 'user', 'active', 'hash', false, false, $4, $4),
			($3, 'w4-authorization-grantee', 'W4 Authorization Grantee', NULL, 'user', 'active', 'hash', false, false, $4, $4)
	`, w4AuthorizationWriteAdminID, w4AuthorizationWriteOwnerID, w4AuthorizationWriteGranteeID, now); err != nil {
		t.Fatalf("insert W4 authorization write system accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.groups (
			id, system_account_id, name, provider_code, description, enabled, is_default,
			group_type, scheduling_policy_json, created_at, updated_at
		) VALUES ($1, $2, $3, 'openai', NULL, true, false, 'personal', NULL, $4, $4)
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteOwnerID, w4AuthorizationWriteGroupName, now); err != nil {
		t.Fatalf("insert W4 authorization write group: %v", err)
	}
}

type w4AuthorizationWriteFacts struct {
	GrantID              string
	GrantStatus          string
	GrantCreatedBy       string
	RuntimeID            string
	RuntimeStatus        string
	RuntimeEffectiveType string
	RuntimeCreatedBy     string
	SourceType           string
	SourceStatus         string
	SourceCreatedBy      string
	DirtyGroupID         string
	DirtyReason          string
	GrantCreatedAt       time.Time
	RuntimeCreatedAt     time.Time
	SourceCreatedAt      time.Time
	DirtyUpdatedAt       time.Time
}

func readW4AuthorizationWriteFacts(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationWriteFacts {
	t.Helper()
	var facts w4AuthorizationWriteFacts
	if err := db.QueryRowContext(ctx, `
		SELECT id, status, created_by, created_at
		FROM juhe_business.resource_authorization_grants
		WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID).Scan(
		&facts.GrantID,
		&facts.GrantStatus,
		&facts.GrantCreatedBy,
		&facts.GrantCreatedAt,
	); err != nil {
		t.Fatalf("read W4 authorization grant fact: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT id, status, effective_source_type, created_by, created_at
		FROM juhe_business.resource_authorizations
		WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID).Scan(
		&facts.RuntimeID,
		&facts.RuntimeStatus,
		&facts.RuntimeEffectiveType,
		&facts.RuntimeCreatedBy,
		&facts.RuntimeCreatedAt,
	); err != nil {
		t.Fatalf("read W4 runtime authorization fact: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT source_type, status, created_by, created_at
		FROM juhe_business.resource_authorization_sources
		WHERE authorization_id = $1
		ORDER BY id
		LIMIT 1
	`, facts.RuntimeID).Scan(
		&facts.SourceType,
		&facts.SourceStatus,
		&facts.SourceCreatedBy,
		&facts.SourceCreatedAt,
	); err != nil {
		t.Fatalf("read W4 authorization source fact: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT group_id, reason, updated_at
		FROM juhe_business.group_account_stats_dirty
		WHERE group_id = '__all__'
	`).Scan(
		&facts.DirtyGroupID,
		&facts.DirtyReason,
		&facts.DirtyUpdatedAt,
	); err != nil {
		t.Fatalf("read W4 authorization dirty fact: %v", err)
	}
	return facts
}

func assertW4AuthorizationWriteFacts(t *testing.T, facts w4AuthorizationWriteFacts, wantGrantID string, now time.Time) {
	t.Helper()
	if facts.GrantID != wantGrantID ||
		facts.GrantStatus != "active" ||
		facts.GrantCreatedBy != w4AuthorizationWriteAdminID ||
		facts.RuntimeID == "" ||
		facts.RuntimeID == facts.GrantID ||
		facts.RuntimeStatus != "active" ||
		facts.RuntimeEffectiveType != "manual" ||
		facts.RuntimeCreatedBy != w4AuthorizationWriteAdminID ||
		facts.SourceType != "manual" ||
		facts.SourceStatus != "active" ||
		facts.SourceCreatedBy != w4AuthorizationWriteAdminID ||
		facts.DirtyGroupID != "__all__" ||
		facts.DirtyReason != managementauthorizations.ResourceAuthorizationCreatedReason ||
		!facts.GrantCreatedAt.UTC().Equal(now) ||
		!facts.RuntimeCreatedAt.UTC().Equal(now) ||
		!facts.SourceCreatedAt.UTC().Equal(now) ||
		!facts.DirtyUpdatedAt.UTC().Equal(now) {
		t.Fatalf("authorization write facts = %+v", facts)
	}
}

type w4AuthorizationWriteGrantRow struct {
	ID                           string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeType                  string
	GranteeSystemAccountID       sql.NullString
	GranteeTeamID                sql.NullString
	Scope                        string
	Status                       string
	Remark                       sql.NullString
	ExpiresAt                    sql.NullTime
	LimitsJSON                   sql.NullString
	CreatedBy                    string
	CreatedAt                    time.Time
	RevokedBy                    sql.NullString
	RevokedAt                    sql.NullTime
	UpdatedAt                    time.Time
}

type w4AuthorizationWriteRuntimeRow struct {
	ID                           string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	Scope                        string
	Status                       string
	EffectiveSourceType          sql.NullString
	EffectiveSourceTeamID        sql.NullString
	ActivatedAt                  sql.NullTime
	LastSourceChangedAt          sql.NullTime
	Remark                       sql.NullString
	ExpiresAt                    sql.NullTime
	LimitsJSON                   sql.NullString
	CreatedBy                    string
	CreatedAt                    time.Time
	RevokedBy                    sql.NullString
	RevokedAt                    sql.NullTime
	RevokedReason                sql.NullString
	UpdatedAt                    time.Time
}

type w4AuthorizationWriteSourceRow struct {
	ID              string
	AuthorizationID string
	SourceType      string
	SourceTeamID    sql.NullString
	Status          string
	ActivatedAt     sql.NullTime
	EndedAt         sql.NullTime
	EndedReason     sql.NullString
	CreatedBy       string
	CreatedAt       time.Time
	RevokedBy       sql.NullString
	RevokedAt       sql.NullTime
	UpdatedAt       time.Time
}

type w4AuthorizationWriteDirtyRow struct {
	GroupID   string
	Reason    sql.NullString
	UpdatedAt time.Time
}

type w4AuthorizationWriteBusinessRows struct {
	Grants  []w4AuthorizationWriteGrantRow
	Runtime []w4AuthorizationWriteRuntimeRow
	Sources []w4AuthorizationWriteSourceRow
	Dirty   []w4AuthorizationWriteDirtyRow
}

func readW4AuthorizationWriteBusinessRows(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationWriteBusinessRows {
	t.Helper()
	return w4AuthorizationWriteBusinessRows{
		Grants:  readW4AuthorizationWriteGrantRows(t, ctx, db),
		Runtime: readW4AuthorizationWriteRuntimeRows(t, ctx, db),
		Sources: readW4AuthorizationWriteSourceRows(t, ctx, db),
		Dirty:   readW4AuthorizationWriteDirtyRows(t, ctx, db),
	}
}

func readW4AuthorizationWriteGrantRows(t *testing.T, ctx context.Context, db *sql.DB) []w4AuthorizationWriteGrantRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		       grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
		       limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
		FROM juhe_business.resource_authorization_grants
		WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2
		ORDER BY id
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID)
	if err != nil {
		t.Fatalf("query authorization grant snapshot: %v", err)
	}
	defer rows.Close()
	result := []w4AuthorizationWriteGrantRow{}
	for rows.Next() {
		var row w4AuthorizationWriteGrantRow
		if err := rows.Scan(
			&row.ID, &row.ResourceType, &row.ResourceID, &row.ResourceOwnerSystemAccountID, &row.GranteeType,
			&row.GranteeSystemAccountID, &row.GranteeTeamID, &row.Scope, &row.Status, &row.Remark, &row.ExpiresAt,
			&row.LimitsJSON, &row.CreatedBy, &row.CreatedAt, &row.RevokedBy, &row.RevokedAt, &row.UpdatedAt,
		); err != nil {
			t.Fatalf("scan authorization grant snapshot: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization grant snapshot: %v", err)
	}
	return result
}

func readW4AuthorizationWriteRuntimeRows(t *testing.T, ctx context.Context, db *sql.DB) []w4AuthorizationWriteRuntimeRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		       scope, status, effective_source_type, effective_source_team_id, activated_at,
		       last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
		       revoked_by, revoked_at, revoked_reason, updated_at
		FROM juhe_business.resource_authorizations
		WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2
		ORDER BY id
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID)
	if err != nil {
		t.Fatalf("query runtime authorization snapshot: %v", err)
	}
	defer rows.Close()
	result := []w4AuthorizationWriteRuntimeRow{}
	for rows.Next() {
		var row w4AuthorizationWriteRuntimeRow
		if err := rows.Scan(
			&row.ID, &row.ResourceType, &row.ResourceID, &row.ResourceOwnerSystemAccountID, &row.GranteeSystemAccountID,
			&row.Scope, &row.Status, &row.EffectiveSourceType, &row.EffectiveSourceTeamID, &row.ActivatedAt,
			&row.LastSourceChangedAt, &row.Remark, &row.ExpiresAt, &row.LimitsJSON, &row.CreatedBy, &row.CreatedAt,
			&row.RevokedBy, &row.RevokedAt, &row.RevokedReason, &row.UpdatedAt,
		); err != nil {
			t.Fatalf("scan runtime authorization snapshot: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate runtime authorization snapshot: %v", err)
	}
	return result
}

func readW4AuthorizationWriteSourceRows(t *testing.T, ctx context.Context, db *sql.DB) []w4AuthorizationWriteSourceRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.authorization_id, s.source_type, s.source_team_id, s.status,
		       s.activated_at, s.ended_at, s.ended_reason, s.created_by, s.created_at,
		       s.revoked_by, s.revoked_at, s.updated_at
		FROM juhe_business.resource_authorization_sources AS s
		JOIN juhe_business.resource_authorizations AS r ON r.id = s.authorization_id
		WHERE r.resource_type = 'group' AND r.resource_id = $1 AND r.grantee_system_account_id = $2
		ORDER BY s.id
	`, w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID)
	if err != nil {
		t.Fatalf("query authorization source snapshot: %v", err)
	}
	defer rows.Close()
	result := []w4AuthorizationWriteSourceRow{}
	for rows.Next() {
		var row w4AuthorizationWriteSourceRow
		if err := rows.Scan(
			&row.ID, &row.AuthorizationID, &row.SourceType, &row.SourceTeamID, &row.Status,
			&row.ActivatedAt, &row.EndedAt, &row.EndedReason, &row.CreatedBy, &row.CreatedAt,
			&row.RevokedBy, &row.RevokedAt, &row.UpdatedAt,
		); err != nil {
			t.Fatalf("scan authorization source snapshot: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization source snapshot: %v", err)
	}
	return result
}

func readW4AuthorizationWriteDirtyRows(t *testing.T, ctx context.Context, db *sql.DB) []w4AuthorizationWriteDirtyRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT group_id, reason, updated_at
		FROM juhe_business.group_account_stats_dirty
		WHERE group_id = '__all__'
		ORDER BY group_id
	`)
	if err != nil {
		t.Fatalf("query authorization dirty snapshot: %v", err)
	}
	defer rows.Close()
	result := []w4AuthorizationWriteDirtyRow{}
	for rows.Next() {
		var row w4AuthorizationWriteDirtyRow
		if err := rows.Scan(&row.GroupID, &row.Reason, &row.UpdatedAt); err != nil {
			t.Fatalf("scan authorization dirty snapshot: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization dirty snapshot: %v", err)
	}
	return result
}

type w4AuthorizationWriteRedisEntry struct {
	Key   string
	Value string
}

func readW4AuthorizationWriteRedisKeyspace(
	t *testing.T,
	ctx context.Context,
	client *goredis.Client,
) []w4AuthorizationWriteRedisEntry {
	t.Helper()
	pattern := "juhe-ai:" + w4AuthorizationWriteNamespace + ":*"
	keySet := make(map[string]struct{}, 2)
	var cursor uint64
	for {
		page, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			t.Fatalf("scan authorization Redis namespace: %v", err)
		}
		for _, key := range page {
			keySet[key] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]w4AuthorizationWriteRedisEntry, 0, len(keys))
	for _, key := range keys {
		value, err := client.Get(ctx, key).Result()
		if err != nil {
			t.Fatalf("read authorization Redis key %s: %v", key, err)
		}
		entries = append(entries, w4AuthorizationWriteRedisEntry{Key: key, Value: value})
	}
	return entries
}

func assertW4AuthorizationWriteInvalidations(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	now time.Time,
) {
	t.Helper()
	assertTopic := func(topic string, wantVersion string) {
		t.Helper()
		raw := readW4AuthorizationWriteInvalidationRaw(t, ctx, stateRedis, topic)
		var state struct {
			Version     string `json:"version"`
			Reason      string `json:"reason"`
			PublishedAt string `json:"publishedAt"`
		}
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatalf("decode authorization invalidation %s: %v", topic, err)
		}
		if state.Version != wantVersion ||
			state.Reason != managementauthorizations.ResourceAuthorizationCreatedReason ||
			state.PublishedAt != now.UTC().Format("2006-01-02T15:04:05.000Z") {
			t.Fatalf("authorization invalidation %s = %+v", topic, state)
		}
	}
	assertTopic(gatewaycache.GatewayRuntimeCacheTopic, "w4-authorization-write-version-1")
	assertTopic(gatewaycache.AuthorizationQuotaCacheTopic, "w4-authorization-write-version-2")
}

func readW4AuthorizationWriteInvalidationRaw(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	topic string,
) string {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w4AuthorizationWriteNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+topic,
	)
	if err != nil {
		t.Fatalf("build authorization invalidation key %s: %v", topic, err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read authorization invalidation %s: %v", topic, err)
	}
	return string(raw)
}

func readW4AuthorizationWriteQueueInfo(t *testing.T, inspector *queue.Inspector) queue.QueueInfo {
	t.Helper()
	info, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read authorization operation-log queue: %v", err)
	}
	if info.Pending != 0 || info.Active != 0 || info.Retry != 0 || info.Archived != 0 {
		t.Fatalf("authorization operation-log queue is not drained: %+v", info)
	}
	return info
}

func assertW4AuthorizationWriteOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	created managementauthorizations.Summary,
	now time.Time,
) {
	t.Helper()
	var (
		traceID       string
		actorID       string
		actorUsername string
		actorName     string
		actorRole     string
		scopeID       string
		mode          string
		module        string
		action        string
		operationKey  string
		resourceType  string
		resourceID    string
		resourceName  string
		summary       string
		detailLevel   string
		visibility    string
		changesJSON   string
		metadataJSON  string
		method        string
		path          string
		statusCode    int
		clientIP      string
		userAgent     string
		createdAt     time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
		       operation_scope_system_account_id, mode, module, action, operation_key,
		       resource_type, resource_id, resource_name, summary, detail_level, visibility_scope,
		       changes_json, metadata_json, method, path, status_code, client_ip, user_agent, created_at
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, w4AuthorizationWriteLogID).Scan(
		&traceID, &actorID, &actorUsername, &actorName, &actorRole,
		&scopeID, &mode, &module, &action, &operationKey,
		&resourceType, &resourceID, &resourceName, &summary, &detailLevel, &visibility,
		&changesJSON, &metadataJSON, &method, &path, &statusCode, &clientIP, &userAgent, &createdAt,
	); err != nil {
		t.Fatalf("read authorization operation log: %v", err)
	}
	wantSummary := "创建资源授权：" + w4AuthorizationWriteGroupName + " -> W4 Authorization Grantee"
	if traceID != "req_w4_authorization_write_create" ||
		actorID != w4AuthorizationWriteAdminID ||
		actorUsername != "w4-authorization-admin" ||
		actorName != "W4 Authorization Admin" ||
		actorRole != "admin" ||
		scopeID != w4AuthorizationWriteOwnerID ||
		mode != "admin" ||
		module != "authorizations" ||
		action != "create" ||
		operationKey != "authorizations.create" ||
		resourceType != "authorization" ||
		resourceID != created.ID ||
		resourceName != w4AuthorizationWriteGroupName ||
		summary != wantSummary ||
		detailLevel != "full" ||
		visibility != "targeted" ||
		metadataJSON != "{}" ||
		method != http.MethodPost ||
		path != "/__aisys__/api/authorizations" ||
		statusCode != http.StatusCreated ||
		clientIP != "127.0.0.1" ||
		userAgent != "w4-management-authorization-write-smoke" ||
		!createdAt.UTC().Equal(now) {
		t.Fatalf("authorization operation log mismatch: key=%q resource=%q summary=%q trace=%q actor=%q scope=%q", operationKey, resourceID, summary, traceID, actorID, scopeID)
	}
	assertW4AuthorizationWriteChanges(t, changesJSON)
	assertW4AuthorizationWriteTargets(t, ctx, db, created.ID)
	assertW4AuthorizationWriteViewers(t, ctx, db)
	assertW4AuthorizationWriteSearchTerms(t, ctx, db)
}

func assertW4AuthorizationWriteChanges(t *testing.T, raw string) {
	t.Helper()
	var changes []struct {
		Field     string          `json:"field"`
		Label     string          `json:"label"`
		Before    json.RawMessage `json:"before"`
		After     json.RawMessage `json:"after"`
		Sensitive bool            `json:"sensitive"`
	}
	if err := json.Unmarshal([]byte(raw), &changes); err != nil {
		t.Fatalf("decode authorization operation-log changes: %v", err)
	}
	wantAfter := []any{
		"group",
		w4AuthorizationWriteGroupName,
		"W4 Authorization Grantee",
		"",
		"active",
		"",
		"{}",
	}
	wantFields := []struct {
		field string
		label string
	}{
		{field: "resourceType", label: "资源类型"},
		{field: "resourceId", label: "授权资源"},
		{field: "grantee", label: "被授权目标"},
		{field: "targetGroupId", label: "目标分组"},
		{field: "status", label: "状态"},
		{field: "expiresAt", label: "过期时间"},
		{field: "limits", label: "额度限制"},
	}
	if len(changes) != len(wantFields) {
		t.Fatalf("authorization operation-log changes count=%d fields=%s", len(changes), w4AuthorizationWriteChangeDiagnostics(changes))
	}
	for index, change := range changes {
		wantAfterJSON, err := json.Marshal(wantAfter[index])
		if err != nil {
			t.Fatalf("marshal expected authorization change %s: %v", wantFields[index].field, err)
		}
		var wantValue any
		var gotValue any
		if err := json.Unmarshal(wantAfterJSON, &wantValue); err != nil {
			t.Fatalf("decode expected authorization change %s: %v", wantFields[index].field, err)
		}
		if len(change.After) == 0 {
			t.Fatalf("authorization operation-log change[%d] field=%q label=%q after=<missing> wantType=%s wantValue=%s", index, change.Field, change.Label, jsonType(wantAfterJSON), safeJSONDiagnostic(wantAfterJSON))
		}
		if err := json.Unmarshal(change.After, &gotValue); err != nil {
			t.Fatalf("decode authorization change[%d] field=%q after type=%s value=%s: %v", index, change.Field, jsonType(change.After), safeJSONDiagnostic(change.After), err)
		}
		if change.Field != wantFields[index].field ||
			change.Label != wantFields[index].label ||
			len(change.Before) != 0 ||
			change.Sensitive ||
			!reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf(
				"unsafe or incorrect authorization operation-log change[%d]: got field=%q label=%q before=%s afterType=%s after=%s sensitive=%t; want field=%q label=%q before=<missing> afterType=%s after=%s sensitive=false",
				index,
				change.Field,
				change.Label,
				jsonPresence(change.Before),
				jsonType(change.After),
				safeJSONDiagnostic(change.After),
				change.Sensitive,
				wantFields[index].field,
				wantFields[index].label,
				jsonType(wantAfterJSON),
				safeJSONDiagnostic(wantAfterJSON),
			)
		}
	}
	if strings.Contains(raw, w4AuthorizationWriteAdminToken) || strings.Contains(raw, "cipher") || strings.Contains(raw, "secret") {
		t.Fatalf("authorization operation-log changes contain forbidden sensitive material; fields=%s", w4AuthorizationWriteChangeDiagnostics(changes))
	}
}

func w4AuthorizationWriteChangeDiagnostics(changes []struct {
	Field     string          `json:"field"`
	Label     string          `json:"label"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
	Sensitive bool            `json:"sensitive"`
}) string {
	items := make([]string, 0, len(changes))
	for index, change := range changes {
		items = append(items, fmt.Sprintf(
			"%d:%s/%s before=%s afterType=%s after=%s sensitive=%t",
			index,
			change.Field,
			change.Label,
			jsonPresence(change.Before),
			jsonType(change.After),
			safeJSONDiagnostic(change.After),
			change.Sensitive,
		))
	}
	return strings.Join(items, "; ")
}

func jsonPresence(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "missing"
	}
	return "present"
}

func jsonType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "missing"
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return "null"
	}
	if strings.HasPrefix(trimmed, `"`) {
		return "string"
	}
	if strings.HasPrefix(trimmed, "{") {
		return "object"
	}
	if strings.HasPrefix(trimmed, "[") {
		return "array"
	}
	if trimmed == "true" || trimmed == "false" {
		return "bool"
	}
	return "number"
}

func safeJSONDiagnostic(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "<missing>"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "<invalid-json>"
	}
	switch typed := value.(type) {
	case string:
		return fmt.Sprintf("%q", typed)
	case nil:
		return "null"
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "<unprintable>"
		}
		return string(encoded)
	}
}

func assertW4AuthorizationWriteTargets(t *testing.T, ctx context.Context, db *sql.DB, grantID string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT target_type, target_id, target_name, target_owner_system_account_id, relation
		FROM juhe_dataset.operation_log_targets
		WHERE operation_log_id = $1
		ORDER BY relation, target_type, target_id
	`, w4AuthorizationWriteLogID)
	if err != nil {
		t.Fatalf("query authorization operation-log targets: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var targetType string
		var targetID sql.NullString
		var targetName sql.NullString
		var ownerID sql.NullString
		var relation string
		if err := rows.Scan(&targetType, &targetID, &targetName, &ownerID, &relation); err != nil {
			t.Fatalf("scan authorization operation-log target: %v", err)
		}
		got[relation] = strings.Join([]string{targetType, targetID.String, targetName.String, ownerID.String}, "|")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization operation-log targets: %v", err)
	}
	want := map[string]string{
		"owner":   "group|" + w4AuthorizationWriteGroupID + "|" + w4AuthorizationWriteGroupName + "|" + w4AuthorizationWriteOwnerID,
		"grantee": "system_account|" + w4AuthorizationWriteGranteeID + "|W4 Authorization Grantee|" + w4AuthorizationWriteGranteeID,
		"primary": "authorization|" + grantID + "|" + w4AuthorizationWriteGroupName + "|" + w4AuthorizationWriteOwnerID,
	}
	if len(got) != len(want) {
		t.Fatalf("authorization operation-log targets = %+v, want %+v", got, want)
	}
	for relation, value := range want {
		if got[relation] != value {
			t.Fatalf("authorization operation-log target %s = %q, want %q", relation, got[relation], value)
		}
	}
}

func assertW4AuthorizationWriteViewers(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT system_account_id, visibility_reason, detail_level
		FROM juhe_dataset.operation_log_viewers
		WHERE operation_log_id = $1
	`, w4AuthorizationWriteLogID)
	if err != nil {
		t.Fatalf("query authorization operation-log viewers: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var accountID string
		var reason string
		var detail string
		if err := rows.Scan(&accountID, &reason, &detail); err != nil {
			t.Fatalf("scan authorization operation-log viewer: %v", err)
		}
		got[accountID+"|"+reason] = detail
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization operation-log viewers: %v", err)
	}
	want := map[string]string{
		w4AuthorizationWriteOwnerID + "|authorization_owner":     "full",
		w4AuthorizationWriteGranteeID + "|authorization_grantee": "full",
		w4AuthorizationWriteAdminID + "|actor_self":              "full",
	}
	if len(got) != len(want) {
		t.Fatalf("authorization operation-log viewers = %+v, want %+v", got, want)
	}
	for key, detail := range want {
		if got[key] != detail {
			t.Fatalf("authorization operation-log viewer %q = %q, want %q", key, got[key], detail)
		}
	}
}

func assertW4AuthorizationWriteSearchTerms(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT term
		FROM juhe_dataset.operation_log_summary_search_terms
		WHERE operation_log_id = $1
	`, w4AuthorizationWriteLogID)
	if err != nil {
		t.Fatalf("query authorization operation-log search terms: %v", err)
	}
	defer rows.Close()
	terms := map[string]bool{}
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			t.Fatalf("scan authorization operation-log search term: %v", err)
		}
		terms[term] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization operation-log search terms: %v", err)
	}
	for _, want := range []string{"w4", "authorization", "owner", "group", "grantee"} {
		if !terms[want] {
			t.Fatalf("authorization operation-log search terms missing %q: %+v", want, terms)
		}
	}
}

type w4AuthorizationWriteCounts struct {
	Grants              int
	Runtime             int
	Sources             int
	Dirty               int
	OperationLogs       int
	OperationLogTargets int
	OperationLogViewers int
	OperationLogTerms   int
}

func readW4AuthorizationWriteCounts(t *testing.T, ctx context.Context, db *sql.DB) w4AuthorizationWriteCounts {
	t.Helper()
	var counts w4AuthorizationWriteCounts
	queries := []struct {
		destination *int
		query       string
		args        []any
	}{
		{&counts.Grants, `SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2`, []any{w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID}},
		{&counts.Runtime, `SELECT count(*) FROM juhe_business.resource_authorizations WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2`, []any{w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID}},
		{&counts.Sources, `SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE authorization_id IN (SELECT id FROM juhe_business.resource_authorizations WHERE resource_type = 'group' AND resource_id = $1 AND grantee_system_account_id = $2)`, []any{w4AuthorizationWriteGroupID, w4AuthorizationWriteGranteeID}},
		{&counts.Dirty, `SELECT count(*) FROM juhe_business.group_account_stats_dirty WHERE group_id = '__all__'`, nil},
		{&counts.OperationLogs, `SELECT count(*) FROM juhe_dataset.operation_logs WHERE id = $1`, []any{w4AuthorizationWriteLogID}},
		{&counts.OperationLogTargets, `SELECT count(*) FROM juhe_dataset.operation_log_targets WHERE operation_log_id = $1`, []any{w4AuthorizationWriteLogID}},
		{&counts.OperationLogViewers, `SELECT count(*) FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = $1`, []any{w4AuthorizationWriteLogID}},
		{&counts.OperationLogTerms, `SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = $1`, []any{w4AuthorizationWriteLogID}},
	}
	for index, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.destination); err != nil {
			t.Fatalf("read authorization write count %d: %v", index, err)
		}
	}
	return counts
}

func assertW4AuthorizationWriteSensitiveValuesAbsent(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	values ...string,
) {
	t.Helper()
	queries := []struct {
		name  string
		query string
		args  func(string) []any
	}{
		{
			name: "authorization grants",
			query: `SELECT count(*) FROM juhe_business.resource_authorization_grants
				WHERE resource_id = $2 AND (
					strpos(id, $1) > 0 OR strpos(resource_type, $1) > 0 OR strpos(resource_id, $1) > 0 OR
					strpos(resource_owner_system_account_id, $1) > 0 OR strpos(grantee_type, $1) > 0 OR
					strpos(COALESCE(grantee_system_account_id, ''), $1) > 0 OR strpos(COALESCE(grantee_team_id, ''), $1) > 0 OR
					strpos(scope, $1) > 0 OR strpos(status, $1) > 0 OR strpos(COALESCE(remark, ''), $1) > 0 OR
					strpos(COALESCE(limits_json, ''), $1) > 0 OR strpos(created_by, $1) > 0 OR
					strpos(COALESCE(revoked_by, ''), $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteGroupID} },
		},
		{
			name: "runtime authorizations",
			query: `SELECT count(*) FROM juhe_business.resource_authorizations
				WHERE resource_id = $2 AND (
					strpos(id, $1) > 0 OR strpos(resource_type, $1) > 0 OR strpos(resource_id, $1) > 0 OR
					strpos(resource_owner_system_account_id, $1) > 0 OR strpos(grantee_system_account_id, $1) > 0 OR
					strpos(scope, $1) > 0 OR strpos(status, $1) > 0 OR strpos(COALESCE(effective_source_type, ''), $1) > 0 OR
					strpos(COALESCE(effective_source_team_id, ''), $1) > 0 OR strpos(COALESCE(remark, ''), $1) > 0 OR
					strpos(COALESCE(limits_json, ''), $1) > 0 OR strpos(created_by, $1) > 0 OR
					strpos(COALESCE(revoked_by, ''), $1) > 0 OR strpos(COALESCE(revoked_reason, ''), $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteGroupID} },
		},
		{
			name: "authorization sources",
			query: `SELECT count(*)
				FROM juhe_business.resource_authorization_sources AS s
				JOIN juhe_business.resource_authorizations AS r ON r.id = s.authorization_id
				WHERE r.resource_id = $2 AND (
					strpos(s.id, $1) > 0 OR strpos(s.authorization_id, $1) > 0 OR strpos(s.source_type, $1) > 0 OR
					strpos(COALESCE(s.source_team_id, ''), $1) > 0 OR strpos(s.status, $1) > 0 OR
					strpos(COALESCE(s.ended_reason, ''), $1) > 0 OR strpos(s.created_by, $1) > 0 OR
					strpos(COALESCE(s.revoked_by, ''), $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteGroupID} },
		},
		{
			name: "operation logs",
			query: `SELECT count(*) FROM juhe_dataset.operation_logs
				WHERE id = $2 AND (
					strpos(id, $1) > 0 OR strpos(COALESCE(trace_id, ''), $1) > 0 OR strpos(actor_system_account_id, $1) > 0 OR
					strpos(COALESCE(actor_username, ''), $1) > 0 OR strpos(COALESCE(actor_display_name, ''), $1) > 0 OR
					strpos(actor_role, $1) > 0 OR strpos(COALESCE(operation_scope_system_account_id, ''), $1) > 0 OR
					strpos(mode, $1) > 0 OR strpos(module, $1) > 0 OR strpos(action, $1) > 0 OR strpos(operation_key, $1) > 0 OR
					strpos(resource_type, $1) > 0 OR strpos(COALESCE(resource_id, ''), $1) > 0 OR
					strpos(COALESCE(resource_name, ''), $1) > 0 OR strpos(summary, $1) > 0 OR
					strpos(changes_json, $1) > 0 OR strpos(metadata_json, $1) > 0 OR
					strpos(COALESCE(method, ''), $1) > 0 OR strpos(COALESCE(path, ''), $1) > 0 OR
					strpos(COALESCE(client_ip, ''), $1) > 0 OR strpos(COALESCE(user_agent, ''), $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteLogID} },
		},
		{
			name: "operation log targets",
			query: `SELECT count(*) FROM juhe_dataset.operation_log_targets
				WHERE operation_log_id = $2 AND (
					strpos(id, $1) > 0 OR strpos(operation_log_id, $1) > 0 OR strpos(target_type, $1) > 0 OR
					strpos(COALESCE(target_id, ''), $1) > 0 OR strpos(COALESCE(target_name, ''), $1) > 0 OR
					strpos(COALESCE(target_owner_system_account_id, ''), $1) > 0 OR strpos(relation, $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteLogID} },
		},
		{
			name: "operation log viewers",
			query: `SELECT count(*) FROM juhe_dataset.operation_log_viewers
				WHERE operation_log_id = $2 AND (
					strpos(operation_log_id, $1) > 0 OR strpos(system_account_id, $1) > 0 OR
					strpos(visibility_reason, $1) > 0 OR strpos(detail_level, $1) > 0
				)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteLogID} },
		},
		{
			name: "operation log search terms",
			query: `SELECT count(*) FROM juhe_dataset.operation_log_summary_search_terms
				WHERE operation_log_id = $2 AND (strpos(operation_log_id, $1) > 0 OR strpos(term, $1) > 0)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteLogID} },
		},
		{
			name: "system sessions",
			query: `SELECT count(*) FROM juhe_business.system_sessions
				WHERE id = $2 AND (strpos(id, $1) > 0 OR strpos(system_account_id, $1) > 0 OR strpos(token_hash, $1) > 0)`,
			args: func(value string) []any { return []any{value, w4AuthorizationWriteAdminSession} },
		},
	}
	for _, value := range values {
		for _, query := range queries {
			var count int
			if err := db.QueryRowContext(ctx, query.query, query.args(value)...).Scan(&count); err != nil {
				t.Fatalf("scan forbidden value %q in %s: %v", value, query.name, err)
			}
			if count != 0 {
				t.Fatalf("forbidden value %q found in %s", value, query.name)
			}
		}
	}
}

func assertW4AuthorizationWriteSessionStorage(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var (
		id              string
		systemAccountID string
		tokenHash       string
		expiresAt       time.Time
		createdAt       time.Time
		lastSeenAt      time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT id, system_account_id, token_hash, expires_at, created_at, last_seen_at
		FROM juhe_business.system_sessions
		WHERE id = $1
	`, w4AuthorizationWriteAdminSession).Scan(
		&id,
		&systemAccountID,
		&tokenHash,
		&expiresAt,
		&createdAt,
		&lastSeenAt,
	); err != nil {
		t.Fatalf("read authorization admin session storage: %v", err)
	}
	wantHash := managementauth.HashSessionToken(w4AuthorizationWriteAdminToken)
	if id != w4AuthorizationWriteAdminSession ||
		systemAccountID != w4AuthorizationWriteAdminID ||
		tokenHash != wantHash ||
		tokenHash == w4AuthorizationWriteAdminToken ||
		!expiresAt.After(createdAt) ||
		lastSeenAt.Before(createdAt) {
		t.Fatalf(
			"authorization admin session storage id=%q account=%q hashMatches=%t plaintext=%t expires=%s created=%s lastSeen=%s",
			id,
			systemAccountID,
			tokenHash == wantHash,
			tokenHash == w4AuthorizationWriteAdminToken,
			expiresAt.UTC().Format(time.RFC3339Nano),
			createdAt.UTC().Format(time.RFC3339Nano),
			lastSeenAt.UTC().Format(time.RFC3339Nano),
		)
	}
}
