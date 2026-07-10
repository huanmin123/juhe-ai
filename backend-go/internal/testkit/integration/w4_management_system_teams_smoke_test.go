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
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const w4SystemTeamsRedisNamespace = "w4-management-system-teams"

func TestW4ManagementSystemTeamsPostgresRedisAsynqSmoke(t *testing.T) {
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
	defer terminateContainer(t, ctx, postgresContainer)

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	redisContainer, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer terminateContainer(t, ctx, redisContainer)

	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}
	redisQueueURL := w3RedisURLWithDB(t, redisURL, 0)
	redisStateURL := w3RedisURLWithDB(t, redisURL, 1)
	redisCacheURL := w3RedisURLWithDB(t, redisURL, 2)
	redisOpts, err := queue.ParseRedisURL(redisQueueURL)
	if err != nil {
		t.Fatalf("parse redis queue url: %v", err)
	}
	stateRedis, err := redisplatform.NewClient(redisStateURL, w4SystemTeamsRedisNamespace+":state")
	if err != nil {
		t.Fatalf("open state redis: %v", err)
	}
	defer closeRedisClient(t, stateRedis)
	cacheRedis, err := redisplatform.NewClient(redisCacheURL, w4SystemTeamsRedisNamespace+":cache")
	if err != nil {
		t.Fatalf("open cache redis: %v", err)
	}
	defer closeRedisClient(t, cacheRedis)

	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	insertW4SystemTeamAccountFixtures(t, ctx, db, now)
	sessionToken := "w4-management-system-teams-admin-session"
	userSessionToken := "w4-management-system-teams-user-session"
	sessionCreatedAt := now.Add(-2 * time.Minute)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		"sess_w4_management_system_teams_admin",
		"sys_w4_team_admin",
		sessionToken,
		sessionCreatedAt,
	)
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		"sess_w4_management_system_teams_user",
		"sys_w4_team_member",
		userSessionToken,
		sessionCreatedAt,
	)

	var invalidationVersion int
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: w4SystemTeamsRedisNamespace,
		Now:       func() time.Time { return now },
		NewVersion: func(time.Time) (string, error) {
			invalidationVersion++
			return fmt.Sprintf("w4-system-teams-version-%d", invalidationVersion), nil
		},
	})
	if err != nil {
		t.Fatalf("create gateway cache invalidator: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	var workerErrMu sync.Mutex
	var workerRunErr error
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
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("ingest worker shutdown timed out")
		}
		workerErrMu.Lock()
		err := workerRunErr
		workerErrMu.Unlock()
		if err != nil {
			t.Fatalf("ingest worker run: %v", err)
		}
	}()

	logClient := queue.NewClient(redisOpts)
	defer closeClient(t, logClient)
	inspector := queue.NewInspector(redisOpts)
	defer closeInspector(t, inspector)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementsystemteams.NewServiceWithOptions(managementsystemteams.ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})
	cfg := config.Config{
		Host:                 "127.0.0.1",
		Port:                 3000,
		ManagementAPIEnabled: true,
		TrustProxy:           "false",
	}
	logID := 0
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         logClient,
		SettingsReader: store,
		Now:            func() time.Time { return now },
		NewLogID: func() string {
			logID++
			return fmt.Sprintf("oplog_w4_system_teams_%d", logID)
		},
	}
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           cfg,
		Logger:                           logger,
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementSystemTeamCreateHandler: httpapi.NewManagementSystemTeamCreateHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementSystemTeamPatchHandler: httpapi.NewManagementSystemTeamPatchHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementSystemTeamMembersAddHandler: httpapi.NewManagementSystemTeamMembersAddHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
		ManagementSystemTeamMemberDeleteHandler: httpapi.NewManagementSystemTeamMemberDeleteHandlerWithOperationLog(
			service,
			operationLogOptions,
		),
	})

	forbiddenRec := serveW4SystemTeamRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/system-teams",
		userSessionToken,
		`{"name":"W4 Forbidden Team"}`,
		"req_w4_system_teams_forbidden",
	)
	if forbiddenRec.Code != http.StatusForbidden ||
		!strings.Contains(forbiddenRec.Body.String(), "需要管理员权限") {
		t.Fatalf("non-admin create status = %d, body = %s", forbiddenRec.Code, forbiddenRec.Body.String())
	}
	assertW4SystemTeamMissing(t, ctx, db, "W4 Forbidden Team")

	createRec := serveW4SystemTeamRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/system-teams",
		sessionToken,
		`{"name":"W4 Smoke Team","description":"W4 integration team","status":"active"}`,
		"req_w4_system_teams_create",
	)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create team status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createBody struct {
		Data managementsystemteams.Summary `json:"data"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create team response: %v", err)
	}
	team := createBody.Data
	if team.ID == "" ||
		team.Name != "W4 Smoke Team" ||
		team.Description != "W4 integration team" ||
		team.Status != "active" ||
		team.MemberCount != 0 ||
		team.ActiveMemberCount != 0 ||
		team.CreatedBy != "sys_w4_team_admin" {
		t.Fatalf("create team response = %+v", team)
	}
	assertW4ManagementSessionTouched(t, ctx, db, "sess_w4_management_system_teams_admin", now)
	assertW4SystemTeamRow(t, ctx, db, team.ID, "active")
	assertW4GroupAccountStatsDirtyMissing(t, ctx, db)

	addRec := serveW4SystemTeamRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/system-teams/"+team.ID+"/members",
		sessionToken,
		`{"systemAccountIds":["sys_w4_team_member"]}`,
		"req_w4_system_teams_add_members",
	)
	if addRec.Code != http.StatusOK {
		t.Fatalf("add team member status = %d, body = %s", addRec.Code, addRec.Body.String())
	}
	var addBody struct {
		Data managementsystemteams.Detail `json:"data"`
	}
	if err := json.NewDecoder(addRec.Body).Decode(&addBody); err != nil {
		t.Fatalf("decode add team member response: %v", err)
	}
	if addBody.Data.ID != team.ID ||
		addBody.Data.Status != "active" ||
		addBody.Data.MemberCount != 1 ||
		addBody.Data.ActiveMemberCount != 1 ||
		len(addBody.Data.Members) != 1 {
		t.Fatalf("add team member response = %+v", addBody.Data)
	}
	member := addBody.Data.Members[0]
	if member.ID == "" ||
		member.TeamID != team.ID ||
		member.SystemAccountID != "sys_w4_team_member" ||
		member.SystemAccountName != "W4 Team Member" ||
		member.Username != "w4-team-member" ||
		member.MemberRole != "member" ||
		member.Status != "active" {
		t.Fatalf("added team member = %+v", member)
	}
	assertW4SystemTeamMemberRow(t, ctx, db, member.ID, team.ID, "active", false, now)
	assertW4GroupAccountStatsDirty(t, ctx, db, managementsystemteams.TeamMembersChangedReason, now)
	addInvalidation := assertW4AuthorizationInvalidation(
		t,
		ctx,
		stateRedis,
		managementsystemteams.TeamMembersChangedReason,
		now,
		w4AuthorizationInvalidationVersions{},
	)

	patchRec := serveW4SystemTeamRequest(
		router,
		http.MethodPatch,
		"/__aisys__/api/system-teams/"+team.ID,
		sessionToken,
		`{"status":"disabled"}`,
		"req_w4_system_teams_update",
	)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("disable team status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	var patchBody struct {
		Data managementsystemteams.Detail `json:"data"`
	}
	if err := json.NewDecoder(patchRec.Body).Decode(&patchBody); err != nil {
		t.Fatalf("decode disable team response: %v", err)
	}
	if patchBody.Data.ID != team.ID ||
		patchBody.Data.Status != "disabled" ||
		patchBody.Data.MemberCount != 1 ||
		patchBody.Data.ActiveMemberCount != 1 ||
		len(patchBody.Data.Members) != 1 ||
		patchBody.Data.Members[0].ID != member.ID {
		t.Fatalf("disable team response = %+v", patchBody.Data)
	}
	assertW4SystemTeamRow(t, ctx, db, team.ID, "disabled")
	assertW4SystemTeamMemberRow(t, ctx, db, member.ID, team.ID, "active", false, now)
	assertW4GroupAccountStatsDirty(t, ctx, db, managementsystemteams.TeamAuthorizationChangedReason, now)
	patchInvalidation := assertW4AuthorizationInvalidation(
		t,
		ctx,
		stateRedis,
		managementsystemteams.TeamAuthorizationChangedReason,
		now,
		addInvalidation,
	)

	removeRec := serveW4SystemTeamRequest(
		router,
		http.MethodDelete,
		"/__aisys__/api/system-teams/"+team.ID+"/members/"+member.ID,
		sessionToken,
		"",
		"req_w4_system_teams_remove_member",
	)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("remove team member status = %d, body = %s", removeRec.Code, removeRec.Body.String())
	}
	var removeBody struct {
		Data managementsystemteams.Detail `json:"data"`
	}
	if err := json.NewDecoder(removeRec.Body).Decode(&removeBody); err != nil {
		t.Fatalf("decode remove team member response: %v", err)
	}
	if removeBody.Data.ID != team.ID ||
		removeBody.Data.Status != "disabled" ||
		removeBody.Data.MemberCount != 0 ||
		removeBody.Data.ActiveMemberCount != 0 ||
		len(removeBody.Data.Members) != 0 {
		t.Fatalf("remove team member response = %+v", removeBody.Data)
	}
	assertW4SystemTeamRow(t, ctx, db, team.ID, "disabled")
	assertW4SystemTeamMemberRow(t, ctx, db, member.ID, team.ID, "removed", true, now)
	assertW4GroupAccountStatsDirty(t, ctx, db, managementsystemteams.TeamMembersChangedReason, now)
	assertW4AuthorizationInvalidation(
		t,
		ctx,
		stateRedis,
		managementsystemteams.TeamMembersChangedReason,
		now,
		patchInvalidation,
	)

	if err := waitForOperationLogQueueDrained(ctx, inspector, workerDone, func() error {
		workerErrMu.Lock()
		defer workerErrMu.Unlock()
		return workerRunErr
	}); err != nil {
		t.Fatal(err)
	}
	queueInfo, err := inspector.QueueInfo(operationlogjob.QueueName)
	if err != nil {
		t.Fatalf("read operation log queue info: %v", err)
	}
	if queueInfo.Archived != 0 || queueInfo.Completed < 4 {
		t.Fatalf("operation log queue info = %+v, want at least 4 completed and 0 archived", queueInfo)
	}
	assertW4SystemTeamOperationLogs(t, ctx, db, team.ID, member.ID)
}

func serveW4SystemTeamRequest(
	router http.Handler,
	method string,
	target string,
	sessionToken string,
	body string,
	requestID string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	req.Header.Set("User-Agent", "w4-management-system-teams-smoke")
	req.Header.Set("X-Request-Id", requestID)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func insertW4SystemTeamAccountFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES
			(
				'sys_w4_team_admin', 'w4-team-admin', 'W4 Team Admin', NULL, 'admin', 'active', 'hash',
				false, false, $1, $2
			),
			(
				'sys_w4_team_member', 'w4-team-member', 'W4 Team Member', NULL, 'user', 'active', 'hash',
				false, false, $1, $2
			)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W4 system team account fixtures: %v", err)
	}
}

func assertW4ManagementSessionTouched(t *testing.T, ctx context.Context, db *sql.DB, sessionID string, want time.Time) {
	t.Helper()
	var got time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT last_seen_at
		FROM juhe_business.system_sessions
		WHERE id = $1
	`, sessionID).Scan(&got); err != nil {
		t.Fatalf("read W4 management session touch: %v", err)
	}
	if !got.UTC().Equal(want.UTC()) {
		t.Fatalf("W4 management session last_seen_at = %s, want %s",
			got.UTC().Format(time.RFC3339Nano),
			want.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW4SystemTeamRow(t *testing.T, ctx context.Context, db *sql.DB, teamID string, wantStatus string) {
	t.Helper()
	var name string
	var description sql.NullString
	var status string
	var createdBy string
	if err := db.QueryRowContext(ctx, `
		SELECT name, description, status, created_by
		FROM juhe_business.system_teams
		WHERE id = $1
	`, teamID).Scan(&name, &description, &status, &createdBy); err != nil {
		t.Fatalf("read W4 system team row: %v", err)
	}
	if name != "W4 Smoke Team" ||
		!description.Valid ||
		description.String != "W4 integration team" ||
		status != wantStatus ||
		createdBy != "sys_w4_team_admin" {
		t.Fatalf("W4 system team row = name:%q description:%+v status:%q createdBy:%q",
			name,
			description,
			status,
			createdBy,
		)
	}
}

func assertW4SystemTeamMissing(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.system_teams
		WHERE name = $1
	`, name).Scan(&count); err != nil {
		t.Fatalf("count W4 system team %q: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("W4 system team %q count = %d, want 0", name, count)
	}
}

func assertW4SystemTeamMemberRow(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	memberID string,
	teamID string,
	wantStatus string,
	wantRemoved bool,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var gotTeamID string
	var systemAccountID string
	var memberRole string
	var status string
	var removedAt sql.NullTime
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT team_id, system_account_id, member_role, status, removed_at, updated_at
		FROM juhe_business.system_team_members
		WHERE id = $1
	`, memberID).Scan(&gotTeamID, &systemAccountID, &memberRole, &status, &removedAt, &updatedAt); err != nil {
		t.Fatalf("read W4 system team member row: %v", err)
	}
	if gotTeamID != teamID ||
		systemAccountID != "sys_w4_team_member" ||
		memberRole != "member" ||
		status != wantStatus ||
		removedAt.Valid != wantRemoved ||
		!updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf("W4 system team member row = team:%q account:%q role:%q status:%q removed:%+v updated:%s",
			gotTeamID,
			systemAccountID,
			memberRole,
			status,
			removedAt,
			updatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	if wantRemoved && !removedAt.Time.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf("W4 removed_at = %s, want %s",
			removedAt.Time.UTC().Format(time.RFC3339Nano),
			wantUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

func assertW4GroupAccountStatsDirtyMissing(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_business.group_account_stats_dirty
		WHERE group_id = '__all__'
	`).Scan(&count); err != nil {
		t.Fatalf("count W4 group account stats dirty marker: %v", err)
	}
	if count != 0 {
		t.Fatalf("W4 group account stats dirty marker count = %d, want 0 after team create", count)
	}
}

func assertW4GroupAccountStatsDirty(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	wantReason string,
	wantUpdatedAt time.Time,
) {
	t.Helper()
	var reason sql.NullString
	var updatedAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT reason, updated_at
		FROM juhe_business.group_account_stats_dirty
		WHERE group_id = '__all__'
	`).Scan(&reason, &updatedAt); err != nil {
		t.Fatalf("read W4 group account stats dirty marker: %v", err)
	}
	if !reason.Valid || reason.String != wantReason || !updatedAt.UTC().Equal(wantUpdatedAt.UTC()) {
		t.Fatalf("W4 group account stats dirty marker = reason:%+v updated:%s, want %q/%s",
			reason,
			updatedAt.UTC().Format(time.RFC3339Nano),
			wantReason,
			wantUpdatedAt.UTC().Format(time.RFC3339Nano),
		)
	}
}

type w4AuthorizationInvalidationVersions struct {
	GatewayRuntime     string
	AuthorizationQuota string
}

func assertW4AuthorizationInvalidation(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	wantReason string,
	wantPublishedAt time.Time,
	previous w4AuthorizationInvalidationVersions,
) w4AuthorizationInvalidationVersions {
	t.Helper()
	gatewayVersion := assertW4AuthorizationInvalidationTopic(
		t,
		ctx,
		stateRedis,
		gatewaycache.GatewayRuntimeCacheTopic,
		wantReason,
		wantPublishedAt,
		previous.GatewayRuntime,
	)
	authorizationVersion := assertW4AuthorizationInvalidationTopic(
		t,
		ctx,
		stateRedis,
		gatewaycache.AuthorizationQuotaCacheTopic,
		wantReason,
		wantPublishedAt,
		previous.AuthorizationQuota,
	)
	if gatewayVersion == authorizationVersion {
		t.Fatalf("W4 gateway and authorization invalidation versions are equal: %q", gatewayVersion)
	}
	return w4AuthorizationInvalidationVersions{
		GatewayRuntime:     gatewayVersion,
		AuthorizationQuota: authorizationVersion,
	}
}

func assertW4AuthorizationInvalidationTopic(
	t *testing.T,
	ctx context.Context,
	stateRedis *redisplatform.Client,
	topic string,
	wantReason string,
	wantPublishedAt time.Time,
	previousVersion string,
) string {
	t.Helper()
	key, err := gatewaycache.RuntimeStateKey(
		w4SystemTeamsRedisNamespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+topic,
	)
	if err != nil {
		t.Fatalf("build W4 invalidation key for %s: %v", topic, err)
	}
	raw, err := stateRedis.GetRaw(ctx, key)
	if err != nil {
		t.Fatalf("read W4 invalidation topic %s key %s: %v", topic, key, err)
	}
	var state struct {
		Version     string `json:"version"`
		Reason      string `json:"reason"`
		PublishedAt string `json:"publishedAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode W4 invalidation topic %s payload %s: %v", topic, raw, err)
	}
	wantPublished := wantPublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if state.Version == "" || state.Reason != wantReason || state.PublishedAt != wantPublished {
		t.Fatalf("W4 invalidation topic %s state = %+v, want reason %q publishedAt %q",
			topic,
			state,
			wantReason,
			wantPublished,
		)
	}
	if previousVersion != "" && state.Version == previousVersion {
		t.Fatalf("W4 invalidation topic %s version did not change from %q", topic, previousVersion)
	}
	return state.Version
}

type w4SystemTeamOperationLogRow struct {
	ID                            string
	TraceID                       string
	ActorSystemAccountID          string
	OperationScopeSystemAccountID string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	ChangesJSON                   string
	MetadataJSON                  string
	Method                        string
	Path                          string
	StatusCode                    int
}

func assertW4SystemTeamOperationLogs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	teamID string,
	memberID string,
) {
	t.Helper()
	create := readW4SystemTeamOperationLog(t, ctx, db, "oplog_w4_system_teams_1")
	assertW4SystemTeamOperationLogIdentity(
		t,
		create,
		"req_w4_system_teams_create",
		"create",
		"system_teams.create",
		teamID,
		http.MethodPost,
		"/__aisys__/api/system-teams",
		http.StatusCreated,
	)
	createChanges := decodeW4SystemTeamOperationLogChanges(t, create.ChangesJSON)
	if len(createChanges) != 3 ||
		!w4SystemTeamHasChange(createChanges, "name", nil, "W4 Smoke Team") ||
		!w4SystemTeamHasChange(createChanges, "description", nil, "W4 integration team") ||
		!w4SystemTeamHasChange(createChanges, "status", nil, "active") {
		t.Fatalf("W4 create team operation log changes = %+v", createChanges)
	}
	assertW4SystemTeamOperationLogMetadataEmpty(t, create.MetadataJSON)

	add := readW4SystemTeamOperationLog(t, ctx, db, "oplog_w4_system_teams_2")
	assertW4SystemTeamOperationLogIdentity(
		t,
		add,
		"req_w4_system_teams_add_members",
		"add_members",
		"system_teams.add_members",
		teamID,
		http.MethodPost,
		"/__aisys__/api/system-teams/"+teamID+"/members",
		http.StatusOK,
	)
	addChanges := decodeW4SystemTeamOperationLogChanges(t, add.ChangesJSON)
	if len(addChanges) != 1 || !w4SystemTeamHasChange(addChanges, "members", nil, "W4 Team Member") {
		t.Fatalf("W4 add member operation log changes = %+v", addChanges)
	}
	assertW4SystemTeamOperationLogMetadataEmpty(t, add.MetadataJSON)

	update := readW4SystemTeamOperationLog(t, ctx, db, "oplog_w4_system_teams_3")
	assertW4SystemTeamOperationLogIdentity(
		t,
		update,
		"req_w4_system_teams_update",
		"update",
		"system_teams.update",
		teamID,
		http.MethodPatch,
		"/__aisys__/api/system-teams/"+teamID,
		http.StatusOK,
	)
	updateChanges := decodeW4SystemTeamOperationLogChanges(t, update.ChangesJSON)
	if len(updateChanges) != 1 || !w4SystemTeamHasChange(updateChanges, "status", "active", "disabled") {
		t.Fatalf("W4 update team operation log changes = %+v", updateChanges)
	}
	updateMetadata := decodeW4SystemTeamOperationLogMetadata(t, update.MetadataJSON)
	if len(updateMetadata) != 1 || updateMetadata["authorizationChanged"] != true {
		t.Fatalf("W4 update team operation log metadata = %+v", updateMetadata)
	}

	remove := readW4SystemTeamOperationLog(t, ctx, db, "oplog_w4_system_teams_4")
	assertW4SystemTeamOperationLogIdentity(
		t,
		remove,
		"req_w4_system_teams_remove_member",
		"remove_member",
		"system_teams.remove_member",
		teamID,
		http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+memberID,
		http.StatusOK,
	)
	removeChanges := decodeW4SystemTeamOperationLogChanges(t, remove.ChangesJSON)
	if len(removeChanges) != 1 || !w4SystemTeamHasChange(removeChanges, "member", "W4 Team Member", nil) {
		t.Fatalf("W4 remove member operation log changes = %+v", removeChanges)
	}
	assertW4SystemTeamOperationLogMetadataEmpty(t, remove.MetadataJSON)

	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM juhe_dataset.operation_logs
		WHERE id LIKE 'oplog_w4_system_teams_%'
	`).Scan(&total); err != nil {
		t.Fatalf("count W4 system team operation logs: %v", err)
	}
	if total != 4 {
		t.Fatalf("W4 system team operation log count = %d, want 4", total)
	}
}

func readW4SystemTeamOperationLog(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	id string,
) w4SystemTeamOperationLogRow {
	t.Helper()
	var row w4SystemTeamOperationLogRow
	if err := db.QueryRowContext(ctx, `
		SELECT
			id,
			trace_id,
			actor_system_account_id,
			operation_scope_system_account_id,
			module,
			action,
			operation_key,
			resource_type,
			resource_id,
			resource_name,
			changes_json,
			metadata_json,
			method,
			path,
			status_code
		FROM juhe_dataset.operation_logs
		WHERE id = $1
	`, id).Scan(
		&row.ID,
		&row.TraceID,
		&row.ActorSystemAccountID,
		&row.OperationScopeSystemAccountID,
		&row.Module,
		&row.Action,
		&row.OperationKey,
		&row.ResourceType,
		&row.ResourceID,
		&row.ResourceName,
		&row.ChangesJSON,
		&row.MetadataJSON,
		&row.Method,
		&row.Path,
		&row.StatusCode,
	); err != nil {
		t.Fatalf("read W4 system team operation log %s: %v", id, err)
	}
	return row
}

func assertW4SystemTeamOperationLogIdentity(
	t *testing.T,
	row w4SystemTeamOperationLogRow,
	wantTraceID string,
	wantAction string,
	wantOperationKey string,
	wantResourceID string,
	wantMethod string,
	wantPath string,
	wantStatusCode int,
) {
	t.Helper()
	if row.TraceID != wantTraceID ||
		row.ActorSystemAccountID != "sys_w4_team_admin" ||
		row.OperationScopeSystemAccountID != "sys_w4_team_admin" ||
		row.Module != "system_teams" ||
		row.Action != wantAction ||
		row.OperationKey != wantOperationKey ||
		row.ResourceType != "system_team" ||
		row.ResourceID != wantResourceID ||
		row.ResourceName != "W4 Smoke Team" ||
		row.Method != wantMethod ||
		!strings.HasPrefix(row.Path, wantPath) ||
		row.StatusCode != wantStatusCode {
		t.Fatalf("W4 system team operation log identity = %+v", row)
	}
}

func decodeW4SystemTeamOperationLogChanges(t *testing.T, raw string) []port.OperationLogChange {
	t.Helper()
	var changes []port.OperationLogChange
	if err := json.Unmarshal([]byte(raw), &changes); err != nil {
		t.Fatalf("decode W4 system team operation log changes %s: %v", raw, err)
	}
	return changes
}

func decodeW4SystemTeamOperationLogMetadata(t *testing.T, raw string) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("decode W4 system team operation log metadata %s: %v", raw, err)
	}
	return metadata
}

func assertW4SystemTeamOperationLogMetadataEmpty(t *testing.T, raw string) {
	t.Helper()
	if metadata := decodeW4SystemTeamOperationLogMetadata(t, raw); len(metadata) != 0 {
		t.Fatalf("W4 system team operation log metadata = %+v, want empty", metadata)
	}
}

func w4SystemTeamHasChange(changes []port.OperationLogChange, field string, before any, after any) bool {
	for _, change := range changes {
		if change.Field == field && change.Before == before && change.After == after {
			return true
		}
	}
	return false
}
