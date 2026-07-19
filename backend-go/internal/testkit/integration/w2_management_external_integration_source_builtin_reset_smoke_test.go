//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2BuiltInResetAdminID        = "sys_w2_builtin_reset_admin"
	w2BuiltInResetAdminSessionID = "sess_w2_builtin_reset_admin"
	w2BuiltInResetAdminToken     = "w2-builtin-reset-admin-session-token"
	w2BuiltInResetSecret         = "w2-builtin-reset-secret-key"
	w2BuiltInResetLogID          = "oplog_w2_builtin_reset"
	w2BuiltInResetRequestID      = "req_w2_builtin_reset"
	w2BuiltInResetNewToken       = "juis_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"
	w2BuiltInResetOldToken       = "juis_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	w2BuiltInResetCollisionToken = "juis_ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210abcdefg"
	w2BuiltInResetHolderSourceID = "extsrc_w2_builtin_reset_collision"
	w2BuiltInResetHolderTokenID  = "exttok_w2_builtin_reset_collision"
)

func TestW2ManagementExternalIntegrationSourceBuiltInResetPostgresRedisAsynqSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		postgresContainer *tcpostgres.PostgresContainer
		redisContainer    *tcredis.RedisContainer
		db                *sql.DB
		store             *postgresstore.Store
		logClient         *queue.Client
		inspector         *queue.Inspector
		resetServer       *httptest.Server
		collisionServer   *httptest.Server
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
		if collisionServer != nil {
			collisionServer.Close()
		}
		if resetServer != nil {
			resetServer.Close()
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

	redisContainer, err = tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}

	now := time.Date(2026, 7, 19, 8, 30, 0, 0, time.UTC)
	fixture := insertW2BuiltInResetFixtures(t, ctx, db, now)

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
	if err := logClient.Ping(); err != nil {
		t.Fatalf("ping operation log queue: %v", err)
	}
	inspector = queue.NewInspector(redisOpts)
	recordingClient := &w2BuiltInResetRecordingQueue{delegate: logClient}
	store, err = postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementexternalintegrationsources.NewBuiltInResetServiceWithOptions(
		managementexternalintegrationsources.BuiltInResetServiceOptions{
			Store:    store,
			Secret:   w2BuiltInResetSecret,
			Now:      func() time.Time { return now },
			NewToken: func() (string, error) { return w2BuiltInResetNewToken, nil },
		},
	)
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logIDCalls := 0
	resetServer = httptest.NewServer(w2BuiltInResetRouter(
		authenticator,
		service,
		httpapi.ManagementOperationLogOptions{
			Config:         cfg,
			Logger:         logger,
			Client:         recordingClient,
			SettingsReader: store,
			Now:            func() time.Time { return now },
			NewLogID: func() string {
				logIDCalls++
				return w2BuiltInResetLogID
			},
		},
	))

	response := doW2BuiltInResetRequest(t, ctx, resetServer.URL, w2BuiltInResetRequestID)
	if response.StatusCode != http.StatusOK || response.CacheControl != "no-store" || response.Pragma != "no-cache" {
		t.Fatalf("reset response status=%d Cache-Control=%q Pragma=%q body=%s", response.StatusCode, response.CacheControl, response.Pragma, response.Body)
	}
	var envelope struct {
		Data managementexternalintegrationsources.TokenCreateResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
		t.Fatalf("decode built-in reset response: %v", err)
	}
	assertW2BuiltInResetResponse(t, envelope.Data, response.Body)
	stored := assertW2BuiltInResetStorage(t, ctx, db, fixture, now)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueInfo, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read operation log queue: %v", err)
	}
	if queueInfo.Pending != 0 || queueInfo.Active != 0 || queueInfo.Retry != 0 || queueInfo.Archived != 0 || queueInfo.Completed != 1 {
		t.Fatalf("operation log queue info = %+v, want one completed task", queueInfo)
	}
	assertW2BuiltInResetOperationLog(t, ctx, db, recordingClient, fixture, stored, now)

	beforeCollision := readW2BuiltInResetBusinessSnapshot(t, ctx, db)
	collisionCalls := 0
	collisionService := managementexternalintegrationsources.NewBuiltInResetServiceWithOptions(
		managementexternalintegrationsources.BuiltInResetServiceOptions{
			Store:  store,
			Secret: w2BuiltInResetSecret,
			Now:    func() time.Time { return now.Add(time.Minute) },
			NewToken: func() (string, error) {
				collisionCalls++
				return w2BuiltInResetCollisionToken, nil
			},
		},
	)
	collisionServer = httptest.NewServer(w2BuiltInResetRouter(
		authenticator,
		collisionService,
		httpapi.ManagementOperationLogOptions{
			Config:         cfg,
			Logger:         logger,
			Client:         recordingClient,
			SettingsReader: store,
			Now:            func() time.Time { return now.Add(time.Minute) },
			NewLogID: func() string {
				logIDCalls++
				return "oplog_w2_builtin_reset_collision"
			},
		},
	))
	collisionResponse := doW2BuiltInResetRequest(t, ctx, collisionServer.URL, "req_w2_builtin_reset_collision")
	if collisionResponse.StatusCode != http.StatusBadRequest || !strings.Contains(collisionResponse.Body, managementexternalintegrationsources.ErrTokenExists.Error()) {
		t.Fatalf("collision response status=%d body=%s", collisionResponse.StatusCode, collisionResponse.Body)
	}
	if collisionCalls != 3 {
		t.Fatalf("collision token generation calls=%d, want 3", collisionCalls)
	}
	afterCollision := readW2BuiltInResetBusinessSnapshot(t, ctx, db)
	if !reflect.DeepEqual(afterCollision, beforeCollision) {
		t.Fatalf("collision changed source/token rows\nbefore=%s\nafter=%s", beforeCollision, afterCollision)
	}
	if logIDCalls != 1 || recordingClient.PayloadCount() != 1 {
		t.Fatalf("collision emitted operation log: log IDs=%d payloads=%d", logIDCalls, recordingClient.PayloadCount())
	}
}

type w2BuiltInResetFixture struct {
	sourceName       string
	sourceScopes     string
	sourceExpiresAt  time.Time
	sourceLastUsedAt time.Time
	sourceCreatedAt  time.Time
	tokenName        string
	tokenScopes      string
	tokenExpiresAt   time.Time
	tokenLastUsedAt  time.Time
	tokenCreatedAt   time.Time
	oldHash          string
	oldCipher        string
}

type w2BuiltInResetStored struct {
	newHash   string
	newCipher string
	prefix    string
	suffix    string
}

type w2BuiltInResetHTTPResponse struct {
	StatusCode   int
	CacheControl string
	Pragma       string
	Body         string
}

type w2BuiltInResetRecordingQueue struct {
	delegate *queue.Client
	mu       sync.Mutex
	payloads [][]byte
}

func (q *w2BuiltInResetRecordingQueue) Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	copyPayload := append([]byte(nil), payload...)
	q.mu.Lock()
	q.payloads = append(q.payloads, copyPayload)
	q.mu.Unlock()
	return q.delegate.Enqueue(ctx, taskType, payload, opts)
}

func (q *w2BuiltInResetRecordingQueue) Payloads() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([][]byte, len(q.payloads))
	for index := range q.payloads {
		result[index] = append([]byte(nil), q.payloads[index]...)
	}
	return result
}

func (q *w2BuiltInResetRecordingQueue) PayloadCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.payloads)
}

func w2BuiltInResetRouter(
	authenticator *managementauth.Authenticator,
	service *managementexternalintegrationsources.BuiltInResetService,
	logOptions httpapi.ManagementOperationLogOptions,
) http.Handler {
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           logOptions.Config,
		Logger:                           logOptions.Logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementExternalSourceBuiltInResetHandler: httpapi.NewManagementExternalIntegrationSourceBuiltInResetHandlerWithOperationLog(
			service,
			logOptions,
		),
	})
}

func doW2BuiltInResetRequest(t *testing.T, ctx context.Context, serverURL string, requestID string) w2BuiltInResetHTTPResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		serverURL+"/__aisys__/api/external-integration-sources/built-in-test-token/reset",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatalf("build built-in reset request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: managementauth.SessionCookieName, Value: w2BuiltInResetAdminToken})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "w2-built-in-reset-smoke")
	req.Header.Set("X-Request-Id", requestID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute built-in reset request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read built-in reset response: %v", err)
	}
	return w2BuiltInResetHTTPResponse{
		StatusCode:   resp.StatusCode,
		CacheControl: resp.Header.Get("Cache-Control"),
		Pragma:       resp.Header.Get("Pragma"),
		Body:         string(body),
	}
}

func assertW2BuiltInResetResponse(t *testing.T, result managementexternalintegrationsources.TokenCreateResult, raw string) {
	t.Helper()
	prefix := w2BuiltInResetNewToken[:8]
	suffix := w2BuiltInResetNewToken[len(w2BuiltInResetNewToken)-8:]
	if result.Source.ID != publicapi.BuiltInTestSourceID || result.Source.Name != "W2 Built-in Reset Source" ||
		len(result.Source.Tokens) != 1 || result.Source.Tokens[0].ID != publicapi.BuiltInTestTokenID ||
		result.Token.ID != publicapi.BuiltInTestTokenID || result.Token.Token != w2BuiltInResetNewToken ||
		result.Token.TokenPrefix != prefix || result.Token.TokenSuffix != suffix ||
		len(result.Token.Scopes) != 2 {
		t.Fatalf("built-in reset response is incomplete: source=%+v token=%+v", result.Source, result.Token)
	}
	if len(result.Token.Token) != 48 || !strings.HasPrefix(result.Token.Token, "juis_") ||
		strings.ContainsAny(strings.TrimPrefix(result.Token.Token, "juis_"), "+/=") {
		t.Fatalf("built-in reset plaintext token format is invalid")
	}
	if strings.Count(raw, w2BuiltInResetNewToken) != 1 {
		t.Fatalf("plaintext token occurrence count=%d, want 1", strings.Count(raw, w2BuiltInResetNewToken))
	}
}

func insertW2BuiltInResetFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) w2BuiltInResetFixture {
	t.Helper()
	fixture := w2BuiltInResetFixture{
		sourceName:       "W2 Built-in Reset Source",
		sourceScopes:     `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]`,
		sourceExpiresAt:  now.Add(30 * 24 * time.Hour),
		sourceLastUsedAt: now.Add(-2 * time.Hour),
		sourceCreatedAt:  now.Add(-72 * time.Hour),
		tokenName:        "W2 Built-in Reset Token",
		tokenScopes:      `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]`,
		tokenExpiresAt:   now.Add(14 * 24 * time.Hour),
		tokenLastUsedAt:  now.Add(-90 * time.Minute),
		tokenCreatedAt:   now.Add(-48 * time.Hour),
		oldHash:          publicapiauth.HashExternalSourceToken(w2BuiltInResetOldToken),
	}
	codec := secretcrypto.NewJSONCodec(w2BuiltInResetSecret)
	var err error
	fixture.oldCipher, err = codec.EncryptJSON(map[string]any{"token": w2BuiltInResetOldToken})
	if err != nil {
		t.Fatalf("encrypt old built-in token fixture: %v", err)
	}
	collisionCipher, err := codec.EncryptJSON(map[string]any{"token": w2BuiltInResetCollisionToken})
	if err != nil {
		t.Fatalf("encrypt collision token fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES ($1, 'w2-builtin-reset-admin', 'W2 Built-in Reset Admin', NULL, 'admin', 'active', 'hash', false, false, $2, $2)
	`, w2BuiltInResetAdminID, fixture.sourceCreatedAt); err != nil {
		t.Fatalf("insert built-in reset admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w2BuiltInResetAdminSessionID, w2BuiltInResetAdminID, w2BuiltInResetAdminToken, now.Add(-time.Minute))
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, expires_at, notes,
			last_used_at, created_at, updated_at
		) VALUES
			($1, $2, 'active', $3, '[{"windowSeconds":60,"maxRequests":20}]', $4, 'built-in reset fixture', $5, $6, $7),
			($8, 'W2 Built-in Reset Collision Holder', 'active', '[]', '[]', NULL, NULL, NULL, $6, $7)
	`, publicapi.BuiltInTestSourceID, fixture.sourceName, fixture.sourceScopes, fixture.sourceExpiresAt,
		fixture.sourceLastUsedAt, fixture.sourceCreatedAt, now.Add(-24*time.Hour), w2BuiltInResetHolderSourceID); err != nil {
		t.Fatalf("insert built-in reset source fixtures: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix,
			status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at
		) VALUES
			($1, $2, $3, $4, $5, $6, $7, 'revoked', $8, $9, $10, $11, $12, $13),
			($14, $15, 'W2 Collision Holder Token', $16, $17, $18, $19, 'active', '[]', NULL, NULL, $11, $12, NULL)
	`, publicapi.BuiltInTestTokenID, publicapi.BuiltInTestSourceID, fixture.tokenName, fixture.oldHash, fixture.oldCipher,
		w2BuiltInResetOldToken[:8], w2BuiltInResetOldToken[len(w2BuiltInResetOldToken)-8:], fixture.tokenScopes,
		fixture.tokenExpiresAt, fixture.tokenLastUsedAt, fixture.tokenCreatedAt, now.Add(-12*time.Hour), now.Add(-6*time.Hour),
		w2BuiltInResetHolderTokenID, w2BuiltInResetHolderSourceID, publicapiauth.HashExternalSourceToken(w2BuiltInResetCollisionToken),
		collisionCipher, w2BuiltInResetCollisionToken[:8], w2BuiltInResetCollisionToken[len(w2BuiltInResetCollisionToken)-8:]); err != nil {
		t.Fatalf("insert built-in reset token fixtures: %v", err)
	}
	return fixture
}

func assertW2BuiltInResetStorage(t *testing.T, ctx context.Context, db *sql.DB, fixture w2BuiltInResetFixture, now time.Time) w2BuiltInResetStored {
	t.Helper()
	var (
		sourceName, sourceScopes                                            string
		sourceExpiresAt, sourceLastUsedAt, sourceCreatedAt, sourceUpdatedAt time.Time
	)
	if err := db.QueryRowContext(ctx, `
		SELECT name, scopes_json, expires_at, last_used_at, created_at, updated_at
		FROM juhe_business.external_integration_sources WHERE id = $1
	`, publicapi.BuiltInTestSourceID).Scan(&sourceName, &sourceScopes, &sourceExpiresAt, &sourceLastUsedAt, &sourceCreatedAt, &sourceUpdatedAt); err != nil {
		t.Fatalf("read reset built-in source: %v", err)
	}
	if sourceName != fixture.sourceName || sourceScopes != fixture.sourceScopes ||
		!sourceExpiresAt.Equal(fixture.sourceExpiresAt) || !sourceLastUsedAt.Equal(fixture.sourceLastUsedAt) ||
		!sourceCreatedAt.Equal(fixture.sourceCreatedAt) || !sourceUpdatedAt.Equal(now) {
		t.Fatal("built-in source preserved fields or updated_at mismatch")
	}

	var (
		stored                                      w2BuiltInResetStored
		name, status, scopes                        string
		expiresAt, lastUsedAt, createdAt, updatedAt time.Time
		revokedAt                                   sql.NullTime
	)
	if err := db.QueryRowContext(ctx, `
		SELECT name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status,
		       scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at
		FROM juhe_business.external_integration_source_tokens WHERE id = $1
	`, publicapi.BuiltInTestTokenID).Scan(
		&name, &stored.newHash, &stored.newCipher, &stored.prefix, &stored.suffix, &status,
		&scopes, &expiresAt, &lastUsedAt, &createdAt, &updatedAt, &revokedAt,
	); err != nil {
		t.Fatalf("read reset built-in token: %v", err)
	}
	wantHash := publicapiauth.HashExternalSourceToken(w2BuiltInResetNewToken)
	wantPrefix := w2BuiltInResetNewToken[:8]
	wantSuffix := w2BuiltInResetNewToken[len(w2BuiltInResetNewToken)-8:]
	if name != fixture.tokenName || scopes != fixture.tokenScopes || !expiresAt.Equal(fixture.tokenExpiresAt) ||
		!lastUsedAt.Equal(fixture.tokenLastUsedAt) || !createdAt.Equal(fixture.tokenCreatedAt) || !updatedAt.Equal(now) ||
		stored.newHash != wantHash || stored.newHash == fixture.oldHash || stored.newCipher == fixture.oldCipher ||
		stored.prefix != wantPrefix || stored.suffix != wantSuffix ||
		status != publicapi.TokenStatusActive || revokedAt.Valid {
		t.Fatal("built-in token reset fields or preserved fields mismatch")
	}
	if fixture.oldHash == wantHash {
		t.Fatal("old hash unexpectedly equals new token hash")
	}
	payload, err := secretcrypto.NewJSONCodec(w2BuiltInResetSecret).DecryptJSON(stored.newCipher)
	if err != nil || len(payload) != 1 || payload["token"] != w2BuiltInResetNewToken {
		t.Fatalf("decrypt reset token matches plaintext=%t err=%v", payload["token"] == w2BuiltInResetNewToken, err)
	}
	return stored
}

func assertW2BuiltInResetOperationLog(t *testing.T, ctx context.Context, db *sql.DB, recorder *w2BuiltInResetRecordingQueue, fixture w2BuiltInResetFixture, stored w2BuiltInResetStored, now time.Time) {
	t.Helper()
	payloads := recorder.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("operation log payload count=%d, want 1", len(payloads))
	}
	input, err := operationlogjob.DecodeWriteTaskPayload(payloads[0])
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	wantPreview := stored.prefix + "..." + stored.suffix
	wantChanges := []port.OperationLogChange{{Field: "tokenPreview", Label: "Token 标识", Before: nil, After: wantPreview}}
	if input.ID != w2BuiltInResetLogID || input.Module != "external_integration_sources" ||
		input.Action != "reset_builtin_test_token" || input.OperationKey != "external_integration_sources.reset_builtin_test_token" ||
		input.ResourceType != "external_integration_source" || input.ResourceID != publicapi.BuiltInTestSourceID ||
		input.ResourceName != "W2 Built-in Reset Source" || input.Summary != "重置内置测试 Token" ||
		input.VisibilityScope != "admin_only" || input.DetailLevel != "full" || !reflect.DeepEqual(input.Changes, wantChanges) {
		t.Fatalf("operation log payload mismatch: %+v", input)
	}

	var module, action, operationKey, resourceType, resourceID, resourceName, summary, visibility, detail, changesJSON, metadataJSON string
	if err := db.QueryRowContext(ctx, `
		SELECT module, action, operation_key, resource_type, resource_id, resource_name, summary,
		       visibility_scope, detail_level, changes_json, metadata_json
		FROM juhe_dataset.operation_logs WHERE id = $1
	`, w2BuiltInResetLogID).Scan(
		&module, &action, &operationKey, &resourceType, &resourceID, &resourceName, &summary,
		&visibility, &detail, &changesJSON, &metadataJSON,
	); err != nil {
		t.Fatalf("read built-in reset operation log: %v", err)
	}
	if module != "external_integration_sources" || action != "reset_builtin_test_token" ||
		operationKey != "external_integration_sources.reset_builtin_test_token" || resourceType != "external_integration_source" ||
		resourceID != publicapi.BuiltInTestSourceID || resourceName != "W2 Built-in Reset Source" ||
		summary != "重置内置测试 Token" || visibility != "admin_only" || detail != "full" || metadataJSON != "{}" {
		t.Fatal("persisted built-in reset operation log metadata mismatch")
	}
	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(changesJSON), &changes); err != nil || !reflect.DeepEqual(changes, wantChanges) {
		t.Fatalf("persisted operation log changes=%s err=%v", changesJSON, err)
	}
	for label, raw := range map[string]string{"payload": string(payloads[0]), "changes": changesJSON} {
		for _, forbidden := range []string{
			w2BuiltInResetNewToken, w2BuiltInResetOldToken, w2BuiltInResetCollisionToken,
			fixture.oldHash, fixture.oldCipher, stored.newHash, stored.newCipher,
			"tokenHash", "token_hash", "tokenSecretEncrypted", "ciphertext", "plaintext",
		} {
			if forbidden != "" && strings.Contains(raw, forbidden) {
				t.Fatalf("operation log %s contains forbidden token material", label)
			}
		}
	}
	if !input.CreatedAt.Equal(now) {
		t.Fatalf("operation log created_at=%s want=%s", input.CreatedAt, now)
	}
}

func readW2BuiltInResetBusinessSnapshot(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	queries := []string{
		`SELECT COALESCE(jsonb_agg(to_jsonb(rows) ORDER BY rows.id)::text, '[]') FROM juhe_business.external_integration_sources AS rows`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(rows) ORDER BY rows.id)::text, '[]') FROM juhe_business.external_integration_source_tokens AS rows`,
	}
	result := make([]string, len(queries))
	for index, query := range queries {
		if err := db.QueryRowContext(ctx, query).Scan(&result[index]); err != nil {
			t.Fatalf("read built-in reset business snapshot %d: %v", index, err)
		}
	}
	return result
}
