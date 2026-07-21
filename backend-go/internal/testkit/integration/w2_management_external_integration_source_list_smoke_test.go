//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2ExternalSourceListActiveID   = "extsrc_w2_list_active"
	w2ExternalSourceListDisabledID = "extsrc_w2_list_disabled"
	w2ExternalSourceListNearID     = "extsrc_w2_list_near"

	w2ExternalSourceListActiveTokenID   = "exttok_w2_list_active_expired"
	w2ExternalSourceListDisabledTokenID = "exttok_w2_list_disabled_newer"
	w2ExternalSourceListRevokedTokenID  = "exttok_w2_list_revoked_newest"

	w2ExternalSourceListFixtureSecret        = "w2-external-source-list-fixture-secret-key"
	w2ExternalSourceListActiveTokenPlaintext = "w2-expired-active-token-plaintext-fixture"
)

func TestW2ManagementExternalIntegrationSourceListPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)
	assertW2ExternalSourceListPrefixIndex(t, ctx, db)
	assertW2ExternalSourceDetailTokenOrderIndex(t, ctx, db)

	defer cleanupW2ExternalSourceListFixtures(t, ctx, db)
	fixture := insertW2ExternalSourceListFixtures(t, ctx, db)
	authNow := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, authNow)
	sessionCreatedAt := authNow.Add(-5 * time.Minute)
	sessionToken := "w2-external-source-list-session-token"
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		"sess_w2_external_source_list",
		"sys_w2_proxy_options",
		sessionToken,
		sessionCreatedAt,
	)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	service := managementexternalintegrationsources.NewServiceWithOptions(
		managementexternalintegrationsources.ServiceOptions{
			ListReader:   store,
			DetailReader: store,
			SecretReader: store,
			Secret:       w2ExternalSourceListFixtureSecret,
		},
	)

	all, err := service.List(ctx, managementexternalintegrationsources.ListInput{
		Keyword: "  mIxEd%_PrEfIx  ",
		Status:  "all",
	})
	if err != nil {
		t.Fatalf("list all matching external integration sources: %v", err)
	}
	assertW2ExternalSourceListIDs(t, all.Items, []string{
		w2ExternalSourceListDisabledID,
		w2ExternalSourceListActiveID,
	})
	if all.Page != 1 || all.PageSize != 20 || all.PageUpperBound != 2 || all.HasMore {
		t.Fatalf("all list pagination = %+v", all)
	}
	if findW2ExternalSourceListItem(all.Items, w2ExternalSourceListNearID) != nil {
		t.Fatalf("literal %%_ prefix matched wildcard-like decoy: %+v", all.Items)
	}

	active, err := service.List(ctx, managementexternalintegrationsources.ListInput{
		Keyword: "MIXED%_PREFIX",
		Status:  "active",
	})
	if err != nil {
		t.Fatalf("list active external integration sources: %v", err)
	}
	assertW2ExternalSourceListIDs(t, active.Items, []string{w2ExternalSourceListActiveID})

	disabled, err := service.List(ctx, managementexternalintegrationsources.ListInput{
		Keyword: "mixed%_prefix",
		Status:  "disabled",
	})
	if err != nil {
		t.Fatalf("list disabled external integration sources: %v", err)
	}
	assertW2ExternalSourceListIDs(t, disabled.Items, []string{w2ExternalSourceListDisabledID})

	item := findW2ExternalSourceListItem(active.Items, w2ExternalSourceListActiveID)
	if item == nil {
		t.Fatal("active source is missing from active status result")
	}
	if item.Status != publicapi.SourceStatusActive {
		t.Fatalf("active source status = %+v", item)
	}
	if item.PrimaryToken == nil {
		t.Fatalf("active source primary token is nil: %+v", item)
	}
	primary := item.PrimaryToken
	if primary.ID != w2ExternalSourceListActiveTokenID || primary.TokenPrefix != "juis_w2_a" || primary.TokenSuffix != "suffix01" {
		t.Fatalf("primary token did not prefer expired active token over newer non-active tokens: %+v", primary)
	}
	if !reflect.DeepEqual(item.Scopes, []string{publicapi.ScopeAccountListRead, publicapi.ScopeGroupListRead}) {
		t.Fatalf("source scopes = %#v", item.Scopes)
	}
	if len(item.RateLimits) != 2 ||
		item.RateLimits[0].WindowSeconds != 1 ||
		item.RateLimits[1].WindowSeconds != 60 {
		t.Fatalf("source rate limits = %#v", item.RateLimits)
	}
	if item.IsBuiltIn {
		t.Fatalf("fixture must not be marked built-in: source=%t", item.IsBuiltIn)
	}
	assertW2ExternalSourceListSafeDTO(t, all, fixture.activeTokenCiphertext)

	detail, err := service.Get(ctx, w2ExternalSourceListActiveID)
	if err != nil {
		t.Fatalf("get external integration source detail: %v", err)
	}
	assertW2ExternalSourceDetail(t, detail, item, fixture)

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return authNow },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		ManagementAPIAuthMiddleware:                      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementExternalIntegrationSourceListHandler:   httpapi.NewManagementExternalIntegrationSourceListHandler(service),
		ManagementExternalIntegrationSourceDetailHandler: httpapi.NewManagementExternalIntegrationSourceDetailHandler(service),
		ManagementExternalSourceTokenSecretHandler:       httpapi.NewManagementExternalIntegrationSourceTokenSecretHandler(service),
	})
	req := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/external-integration-sources?page=1.0&pageSize=1e1&keyword=%20mIxEd%25_PrEfIx%20&status=active",
		nil,
	)
	req.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP list status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("HTTP list Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("HTTP list Content-Type = %q", got)
	}
	rawBody := rec.Body.String()
	var response struct {
		Data managementexternalintegrationsources.ListResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawBody), &response); err != nil {
		t.Fatalf("decode HTTP list response: %v", err)
	}
	assertW2ExternalSourceListIDs(t, response.Data.Items, []string{w2ExternalSourceListActiveID})
	if response.Data.Page != 1 || response.Data.PageSize != 10 || response.Data.PageUpperBound != 1 || response.Data.HasMore {
		t.Fatalf("HTTP list pagination = %+v", response.Data)
	}
	assertW2ExternalSourceTokenSecretAbsent(t, rawBody, fixture.activeTokenCiphertext)
	assertW2ExternalSourceListSafeJSON(t, rawBody)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_external_source_list", sessionCreatedAt)

	detailReq := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/external-integration-sources/"+url.PathEscape(w2ExternalSourceListActiveID),
		nil,
	)
	detailReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	detailRec := httptest.NewRecorder()
	router.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("HTTP detail status = %d, want 200", detailRec.Code)
	}
	if got := detailRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("HTTP detail Cache-Control = %q, want no-store", got)
	}
	if got := detailRec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("HTTP detail Content-Type = %q", got)
	}
	var detailResponse struct {
		Data managementexternalintegrationsources.Detail `json:"data"`
	}
	detailBody := detailRec.Body.String()
	if err := json.Unmarshal([]byte(detailBody), &detailResponse); err != nil {
		t.Fatalf("decode HTTP detail response: %v", err)
	}
	assertW2ExternalSourceDetail(t, &detailResponse.Data, item, fixture)
	assertW2ExternalSourceTokenSecretAbsent(t, detailBody, fixture.activeTokenCiphertext)
	assertW2ExternalSourceDetailSafeJSON(t, detailBody)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_external_source_list", sessionCreatedAt)

	secretReq := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/external-integration-sources/"+url.PathEscape(w2ExternalSourceListActiveID)+
			"/tokens/"+url.PathEscape(w2ExternalSourceListActiveTokenID)+"/secret",
		nil,
	)
	secretReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	secretRec := httptest.NewRecorder()
	router.ServeHTTP(secretRec, secretReq)
	if secretRec.Code != http.StatusOK {
		t.Fatalf("HTTP token secret status = %d, want 200", secretRec.Code)
	}
	if got := secretRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("HTTP token secret Cache-Control = %q, want no-store", got)
	}
	if got := secretRec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("HTTP token secret Pragma = %q, want no-cache", got)
	}
	assertW2ExternalSourceTokenSecretResponse(t, secretRec.Body.Bytes())
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_external_source_list", sessionCreatedAt)

	mismatchReq := httptest.NewRequest(
		http.MethodGet,
		"/__aisys__/api/external-integration-sources/"+url.PathEscape(w2ExternalSourceListDisabledID)+
			"/tokens/"+url.PathEscape(w2ExternalSourceListActiveTokenID)+"/secret",
		nil,
	)
	mismatchReq.Header.Set("Cookie", "juhe_ai_session="+sessionToken)
	mismatchRec := httptest.NewRecorder()
	router.ServeHTTP(mismatchRec, mismatchReq)
	assertW2ExternalSourceTokenSecretNotFound(t, mismatchRec)
	assertW2ManagementSessionLastSeenAt(t, ctx, db, "sess_w2_external_source_list", sessionCreatedAt)

	assertW2ExternalSourceUpdateStore(t, ctx, db, store)
}

func assertW2ExternalSourceUpdateStore(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
) {
	t.Helper()
	updateService := managementexternalintegrationsources.NewUpdateService(store)
	result, err := updateService.Update(ctx, managementexternalintegrationsources.UpdateInput{
		SourceID:      w2ExternalSourceListActiveID,
		HasName:       true,
		Name:          " W2 PATCH Updated ",
		HasStatus:     true,
		Status:        publicapi.SourceStatusDisabled,
		HasScopes:     true,
		Scopes:        []any{publicapi.ScopeAPIKeyListRead},
		HasRateLimits: true,
		RateLimits: []any{
			map[string]any{"windowSeconds": 30, "maxRequests": 7},
		},
		HasExpiresAt: true,
		ExpiresAt:    nil,
		HasNotes:     true,
		Notes:        nil,
	})
	if err != nil {
		t.Fatalf("update external integration source: %v", err)
	}
	if !result.Committed || result.Before.Name != "MiXeD%_Prefix Active" ||
		result.After.Name != "W2 PATCH Updated" || result.After.Status != publicapi.SourceStatusDisabled ||
		result.After.ExpiresAt != nil || result.After.Notes != nil ||
		!reflect.DeepEqual(result.After.Scopes, []string{publicapi.ScopeAPIKeyListRead}) ||
		len(result.After.RateLimits) != 1 || result.After.RateLimits[0].WindowSeconds != 30 ||
		len(result.After.Tokens) != 3 {
		t.Fatalf("external integration source update result = %#v", result)
	}
	for _, token := range result.After.Tokens {
		wantStatus := publicapi.TokenStatusDisabled
		if token.ID == w2ExternalSourceListRevokedTokenID {
			wantStatus = publicapi.TokenStatusRevoked
		}
		if token.Name != "W2 PATCH Updated 生产 Token" || token.Status != wantStatus ||
			!reflect.DeepEqual(token.Scopes, []string{publicapi.ScopeAPIKeyListRead}) || token.ExpiresAt != nil {
			t.Fatalf("synchronized external integration source token = %#v", token)
		}
	}

	validationFailure := errors.New("forced transaction validation failure")
	_, err = store.UpdateManagementExternalIntegrationSource(
		ctx,
		port.ManagementExternalIntegrationSourceUpdateInput{
			SourceID:  w2ExternalSourceListActiveID,
			HasName:   true,
			Name:      "W2 PATCH Must Roll Back",
			UpdatedAt: time.Now().UTC(),
		},
		func(port.ManagementExternalIntegrationSourceUpdateResult) error {
			return validationFailure
		},
	)
	if !errors.Is(err, validationFailure) {
		t.Fatalf("transaction validation error = %v", err)
	}
	var sourceName string
	if err := db.QueryRowContext(ctx, `
		SELECT name
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, w2ExternalSourceListActiveID).Scan(&sourceName); err != nil {
		t.Fatalf("read source after forced rollback: %v", err)
	}
	if sourceName != "W2 PATCH Updated" {
		t.Fatalf("source name after forced rollback = %q", sourceName)
	}
	var mismatchedTokens int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
		  AND name <> $2
	`, w2ExternalSourceListActiveID, "W2 PATCH Updated 生产 Token").Scan(&mismatchedTokens); err != nil {
		t.Fatalf("read tokens after forced rollback: %v", err)
	}
	if mismatchedTokens != 0 {
		t.Fatalf("tokens changed despite forced rollback: %d", mismatchedTokens)
	}

	_, err = updateService.Update(ctx, managementexternalintegrationsources.UpdateInput{
		SourceID: w2ExternalSourceListDisabledID,
		HasName:  true,
		Name:     "w2 patch updated",
	})
	if !errors.Is(err, managementexternalintegrationsources.ErrNameExists) {
		t.Fatalf("duplicate external integration source name error = %v", err)
	}
	_, err = updateService.Update(ctx, managementexternalintegrationsources.UpdateInput{SourceID: "missing_source"})
	if !errors.Is(err, managementexternalintegrationsources.ErrNotFound) {
		t.Fatalf("missing external integration source error = %v", err)
	}

	assertW2ExternalSourceUpdateRowLock(t, ctx, db, store)
}

func assertW2ExternalSourceUpdateRowLock(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	store *postgresstore.Store,
) {
	t.Helper()
	testCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type updateOutcome struct {
		result port.ManagementExternalIntegrationSourceUpdateResult
		err    error
	}

	firstValidated := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	release := func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) }
	var updates sync.WaitGroup
	defer func() {
		release()
		cancel()
		done := make(chan struct{})
		go func() {
			updates.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("external source row lock update goroutines did not stop")
		}
	}()
	firstDone := make(chan updateOutcome, 1)
	updates.Add(1)
	go func() {
		defer updates.Done()
		result, err := store.UpdateManagementExternalIntegrationSource(
			testCtx,
			port.ManagementExternalIntegrationSourceUpdateInput{
				SourceID:  w2ExternalSourceListActiveID,
				HasName:   true,
				Name:      "W2 PATCH Lock First",
				UpdatedAt: time.Date(2026, 7, 16, 5, 0, 0, 0, time.UTC),
			},
			func(port.ManagementExternalIntegrationSourceUpdateResult) error {
				close(firstValidated)
				select {
				case <-releaseFirst:
					return nil
				case <-testCtx.Done():
					return testCtx.Err()
				}
			},
		)
		firstDone <- updateOutcome{result: result, err: err}
	}()

	select {
	case <-firstValidated:
	case <-time.After(5 * time.Second):
		t.Fatal("first external source update did not reach the transaction validation barrier")
	}

	secondNotes := "row lock merged notes"
	secondDone := make(chan updateOutcome, 1)
	updates.Add(1)
	go func() {
		defer updates.Done()
		result, err := store.UpdateManagementExternalIntegrationSource(
			testCtx,
			port.ManagementExternalIntegrationSourceUpdateInput{
				SourceID:  w2ExternalSourceListActiveID,
				HasNotes:  true,
				Notes:     &secondNotes,
				UpdatedAt: time.Date(2026, 7, 16, 5, 0, 1, 0, time.UTC),
			},
			nil,
		)
		secondDone <- updateOutcome{result: result, err: err}
	}()

	assertW2ExternalSourceUpdateWaitingOnRowLock(t, testCtx, db)
	select {
	case outcome := <-secondDone:
		t.Fatalf("second external source update completed before the row lock was released: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}
	release()

	var first updateOutcome
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first external source update did not commit after releasing validation barrier")
	}
	if first.err != nil {
		t.Fatalf("first external source row lock update: %v", first.err)
	}

	var second updateOutcome
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second external source update did not resume after the first commit")
	}
	if second.err != nil {
		t.Fatalf("second external source row lock update: %v", second.err)
	}
	if second.result.BeforeSource.Name != "W2 PATCH Lock First" ||
		second.result.AfterSource.Name != "W2 PATCH Lock First" ||
		second.result.AfterSource.Notes == nil || *second.result.AfterSource.Notes != secondNotes {
		t.Fatalf("second update did not merge from the latest committed source snapshot: %#v", second.result)
	}

	var persistedName string
	var persistedNotes sql.NullString
	if err := db.QueryRowContext(testCtx, `
		SELECT name, notes
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, w2ExternalSourceListActiveID).Scan(&persistedName, &persistedNotes); err != nil {
		t.Fatalf("read persisted external source row lock result: %v", err)
	}
	if persistedName != "W2 PATCH Lock First" || !persistedNotes.Valid || persistedNotes.String != secondNotes {
		t.Fatalf("persisted external source row lock result = name %q notes %#v", persistedName, persistedNotes)
	}
	var mismatchedTokenNames int
	if err := db.QueryRowContext(testCtx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
		  AND name <> $2
	`, w2ExternalSourceListActiveID, "W2 PATCH Lock First 生产 Token").Scan(&mismatchedTokenNames); err != nil {
		t.Fatalf("read persisted external source token names after row lock updates: %v", err)
	}
	if mismatchedTokenNames != 0 {
		t.Fatalf("external source row lock updates lost synchronized token name: mismatches=%d", mismatchedTokenNames)
	}
}

func assertW2ExternalSourceUpdateWaitingOnRowLock(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(waitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND cardinality(pg_blocking_pids(pid)) > 0
				  AND query LIKE '-- name: FindManagementExternalIntegrationSourceForUpdate :one%'
			)
		`).Scan(&waiting)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				t.Fatal("second external source update never entered a PostgreSQL row lock wait")
			}
			t.Fatalf("inspect external source row lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatal("second external source update never entered a PostgreSQL row lock wait")
		}
	}
}

type w2ExternalSourceListFixtureTimes struct {
	activeSourceCreatedAt time.Time
	activeSourceUpdatedAt time.Time
	activeTokenExpiresAt  time.Time
	activeTokenCreatedAt  time.Time
	activeTokenUpdatedAt  time.Time
	activeTokenCiphertext string
}

func insertW2ExternalSourceListFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) w2ExternalSourceListFixtureTimes {
	t.Helper()
	location := time.FixedZone("UTC+8", 8*60*60)
	activeSourceCreatedAt := time.Date(2026, 7, 15, 8, 9, 10, 345_000_000, location)
	activeSourceUpdatedAt := activeSourceCreatedAt.Add(10 * time.Minute)
	activeSourceExpiresAt := activeSourceCreatedAt.Add(24 * time.Hour)
	activeSourceLastUsedAt := activeSourceCreatedAt.Add(5 * time.Minute)

	sources := []struct {
		id             string
		name           string
		status         string
		scopesJSON     string
		rateLimitsJSON string
		expiresAt      *time.Time
		notes          *string
		lastUsedAt     *time.Time
		createdAt      time.Time
		updatedAt      time.Time
	}{
		{
			id:             w2ExternalSourceListActiveID,
			name:           "MiXeD%_Prefix Active",
			status:         publicapi.SourceStatusActive,
			scopesJSON:     `["juhe_ai_public:group_list:read","unknown:scope","juhe_ai_public:account_list:read","juhe_ai_public:group_list:read"]`,
			rateLimitsJSON: `[{"windowSeconds":60,"maxRequests":100},{"windowSeconds":1,"maxRequests":2}]`,
			expiresAt:      &activeSourceExpiresAt,
			notes:          w2ExternalSourceListStringPointer("integration source"),
			lastUsedAt:     &activeSourceLastUsedAt,
			createdAt:      activeSourceCreatedAt,
			updatedAt:      activeSourceUpdatedAt,
		},
		{
			id:             w2ExternalSourceListDisabledID,
			name:           "mixed%_prefix Disabled",
			status:         publicapi.SourceStatusDisabled,
			scopesJSON:     `[]`,
			rateLimitsJSON: `[]`,
			createdAt:      activeSourceCreatedAt.Add(time.Minute),
			updatedAt:      activeSourceUpdatedAt.Add(time.Minute),
		},
		{
			id:             w2ExternalSourceListNearID,
			name:           "MiXeDXYPrEfIx Wildcard Decoy",
			status:         publicapi.SourceStatusActive,
			scopesJSON:     `[]`,
			rateLimitsJSON: `[]`,
			createdAt:      activeSourceCreatedAt.Add(2 * time.Minute),
			updatedAt:      activeSourceUpdatedAt.Add(2 * time.Minute),
		},
	}
	for _, source := range sources {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_sources (
				id, name, status, scopes_json, rate_limits_json,
				expires_at, notes, last_used_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10
			)
		`,
			source.id,
			source.name,
			source.status,
			source.scopesJSON,
			source.rateLimitsJSON,
			source.expiresAt,
			source.notes,
			source.lastUsedAt,
			source.createdAt,
			source.updatedAt,
		); err != nil {
			t.Fatalf("insert external integration source fixture %s: %v", source.id, err)
		}
	}

	activeTokenCreatedAt := activeSourceCreatedAt.Add(-2 * time.Hour)
	activeTokenUpdatedAt := activeTokenCreatedAt.Add(5 * time.Minute)
	activeTokenExpiresAt := activeSourceCreatedAt.Add(-3 * time.Hour)
	activeTokenLastUsedAt := activeSourceCreatedAt.Add(-30 * time.Minute)
	revokedAt := activeSourceCreatedAt.Add(3 * time.Hour)
	activeTokenCiphertext, err := secretcrypto.NewJSONCodec(w2ExternalSourceListFixtureSecret).EncryptJSON(
		map[string]any{"token": w2ExternalSourceListActiveTokenPlaintext},
	)
	if err != nil {
		t.Fatal("encrypt external integration source token fixture")
	}
	tokens := []struct {
		id         string
		name       string
		encrypted  string
		status     string
		expiresAt  *time.Time
		lastUsedAt *time.Time
		createdAt  time.Time
		updatedAt  time.Time
		revokedAt  *time.Time
	}{
		{
			id:         w2ExternalSourceListActiveTokenID,
			name:       "Expired Active",
			encrypted:  activeTokenCiphertext,
			status:     publicapi.TokenStatusActive,
			expiresAt:  &activeTokenExpiresAt,
			lastUsedAt: &activeTokenLastUsedAt,
			createdAt:  activeTokenCreatedAt,
			updatedAt:  activeTokenUpdatedAt,
		},
		{
			id:        w2ExternalSourceListDisabledTokenID,
			name:      "Newer Disabled",
			encrypted: "w2-list-encrypted-" + w2ExternalSourceListDisabledTokenID,
			status:    publicapi.TokenStatusDisabled,
			createdAt: activeTokenCreatedAt.Add(time.Hour),
			updatedAt: activeTokenUpdatedAt.Add(time.Hour),
		},
		{
			id:        w2ExternalSourceListRevokedTokenID,
			name:      "Newest Revoked",
			encrypted: "w2-list-encrypted-" + w2ExternalSourceListRevokedTokenID,
			status:    publicapi.TokenStatusRevoked,
			createdAt: activeTokenCreatedAt.Add(2 * time.Hour),
			updatedAt: activeTokenUpdatedAt.Add(2 * time.Hour),
			revokedAt: &revokedAt,
		},
	}
	for index, token := range tokens {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_source_tokens (
				id, source_ref_id, name, token_hash, token_secret_encrypted,
				token_prefix, token_suffix, status, scopes_json,
				expires_at, last_used_at, created_at, updated_at, revoked_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10, $11, $12, $13, $14
			)
		`,
			token.id,
			w2ExternalSourceListActiveID,
			token.name,
			"w2-list-hash-"+token.id,
			token.encrypted,
			"juis_w2_"+string(rune('a'+index)),
			"suffix0"+string(rune('1'+index)),
			token.status,
			`["juhe_ai_public:group_list:read"]`,
			token.expiresAt,
			token.lastUsedAt,
			token.createdAt,
			token.updatedAt,
			token.revokedAt,
		); err != nil {
			t.Fatalf("insert external integration source token fixture %s: %v", token.id, err)
		}
	}

	return w2ExternalSourceListFixtureTimes{
		activeSourceCreatedAt: activeSourceCreatedAt,
		activeSourceUpdatedAt: activeSourceUpdatedAt,
		activeTokenExpiresAt:  activeTokenExpiresAt,
		activeTokenCreatedAt:  activeTokenCreatedAt,
		activeTokenUpdatedAt:  activeTokenUpdatedAt,
		activeTokenCiphertext: activeTokenCiphertext,
	}
}

func assertW2ExternalSourceListPrefixIndex(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'juhe_business'
			  AND tablename = 'external_integration_sources'
			  AND indexname = 'idx_external_integration_sources_name_lower_c_prefix'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("query migration 000049 prefix index: %v", err)
	}
	if !exists {
		t.Fatal("migration 000049 prefix index is missing")
	}
}

func assertW2ExternalSourceDetailTokenOrderIndex(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var definition string
	if err := db.QueryRowContext(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'juhe_business'
		  AND tablename = 'external_integration_source_tokens'
		  AND indexname = 'idx_external_integration_source_tokens_source_created'
	`).Scan(&definition); err != nil {
		t.Fatalf("query migration 000050 token order index: %v", err)
	}
	normalized := strings.Join(strings.Fields(definition), " ")
	if !strings.Contains(normalized, "(source_ref_id, created_at DESC, id DESC)") {
		t.Fatalf("migration 000050 token order index definition = %q", definition)
	}
}

func cleanupW2ExternalSourceListFixtures(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.external_integration_sources
		WHERE id IN ($1, $2, $3)
	`,
		w2ExternalSourceListActiveID,
		w2ExternalSourceListDisabledID,
		w2ExternalSourceListNearID,
	); err != nil {
		t.Errorf("cleanup external integration source list fixtures: %v", err)
	}
}

func assertW2ExternalSourceListIDs(
	t *testing.T,
	items []managementexternalintegrationsources.ListItem,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("external integration source IDs = %#v, want %#v", got, want)
	}
}

func findW2ExternalSourceListItem(
	items []managementexternalintegrationsources.ListItem,
	id string,
) *managementexternalintegrationsources.ListItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
	}
	return nil
}

func assertW2ExternalSourceListSafeDTO(
	t *testing.T,
	result managementexternalintegrationsources.ListResult,
	activeTokenCiphertext string,
) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal external integration source list DTO: %v", err)
	}
	assertW2ExternalSourceTokenSecretAbsent(t, string(encoded), activeTokenCiphertext)
	assertW2ExternalSourceListSafeJSON(t, string(encoded))
}

func assertW2ExternalSourceDetail(
	t *testing.T,
	detail *managementexternalintegrationsources.Detail,
	listItem *managementexternalintegrationsources.ListItem,
	fixture w2ExternalSourceListFixtureTimes,
) {
	t.Helper()
	if detail == nil {
		t.Fatal("external integration source detail is nil")
	}
	if detail.ID != w2ExternalSourceListActiveID || detail.Name != listItem.Name || detail.Status != listItem.Status {
		t.Fatalf("external integration source detail identity = %+v, list = %+v", detail.Source, listItem)
	}
	if detail.TokenCount != 3 || detail.ActiveTokenCount != 1 || detail.PrimaryToken != nil {
		t.Fatalf("external integration source detail token summary = %+v", detail.Source)
	}
	if !reflect.DeepEqual(detail.Scopes, listItem.Scopes) || !reflect.DeepEqual(detail.RateLimits, listItem.RateLimits) {
		t.Fatalf("external integration source detail fields differ from list: detail=%+v list=%+v", detail.Source, listItem)
	}
	wantTokenIDs := []string{
		w2ExternalSourceListRevokedTokenID,
		w2ExternalSourceListDisabledTokenID,
		w2ExternalSourceListActiveTokenID,
	}
	gotTokenIDs := make([]string, 0, len(detail.Tokens))
	for _, token := range detail.Tokens {
		gotTokenIDs = append(gotTokenIDs, token.ID)
	}
	if !reflect.DeepEqual(gotTokenIDs, wantTokenIDs) {
		t.Fatalf("external integration source detail token order = %#v, want %#v", gotTokenIDs, wantTokenIDs)
	}
	if detail.Tokens[0].Status != publicapi.TokenStatusRevoked || detail.Tokens[0].RevokedAt == nil ||
		detail.Tokens[1].Status != publicapi.TokenStatusDisabled ||
		detail.Tokens[2].Status != publicapi.TokenStatusActive {
		t.Fatalf("external integration source detail token statuses = %+v", detail.Tokens)
	}
	if detail.Tokens[2].ExpiresAt == nil ||
		*detail.Tokens[2].ExpiresAt != fixture.activeTokenExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("external integration source detail active token expiry = %#v", detail.Tokens[2].ExpiresAt)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal external integration source detail DTO: %v", err)
	}
	assertW2ExternalSourceTokenSecretAbsent(t, string(encoded), fixture.activeTokenCiphertext)
	assertW2ExternalSourceDetailSafeJSON(t, string(encoded))
}

func assertW2ExternalSourceTokenSecretResponse(t *testing.T, body []byte) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode external integration source token secret response: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatal("external integration source token secret response must contain only data")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &data); err != nil {
		t.Fatalf("decode external integration source token secret data: %v", err)
	}
	if len(data) != 1 {
		t.Fatal("external integration source token secret data must contain only token")
	}
	var token string
	if err := json.Unmarshal(data["token"], &token); err != nil {
		t.Fatalf("decode external integration source token secret token: %v", err)
	}
	if token != w2ExternalSourceListActiveTokenPlaintext {
		t.Fatal("external integration source token secret plaintext differs from fixture")
	}
}

func assertW2ExternalSourceTokenSecretNotFound(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("HTTP mismatched token secret status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode mismatched token secret response: %v", err)
	}
	if len(body) != 1 || body["message"] != "Token 不存在" {
		t.Fatal("mismatched token secret response must be exact Token 不存在 error")
	}
}

func assertW2ExternalSourceTokenSecretAbsent(t *testing.T, body string, ciphertext string) {
	t.Helper()
	if strings.Contains(body, w2ExternalSourceListActiveTokenPlaintext) || strings.Contains(body, ciphertext) {
		t.Fatal("external integration source list/detail leaked token secret material")
	}
}

func assertW2ExternalSourceListSafeJSON(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{
		"token_hash",
		"tokenHash",
		"token_secret_encrypted",
		"tokenSecretEncrypted",
		"w2-list-hash-",
		"w2-list-encrypted-",
		`"token":`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("external integration source list DTO leaked forbidden field or fixture marker %q", forbidden)
		}
	}
}

func assertW2ExternalSourceDetailSafeJSON(t *testing.T, body string) {
	t.Helper()
	assertW2ExternalSourceListSafeJSON(t, body)
	if strings.Contains(body, `"primaryToken"`) {
		t.Fatal("external integration source detail DTO must not contain primaryToken")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode external integration source detail safety envelope: %v", err)
	}
	rawDetail := json.RawMessage(body)
	if data, ok := envelope["data"]; ok {
		rawDetail = data
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(rawDetail, &source); err != nil {
		t.Fatalf("decode external integration source detail safety object: %v", err)
	}
	allowedSourceFields := map[string]struct{}{
		"id": {}, "name": {}, "status": {}, "scopes": {}, "rateLimits": {},
		"expiresAt": {}, "notes": {}, "lastUsedAt": {}, "createdAt": {}, "updatedAt": {},
		"tokenCount": {}, "activeTokenCount": {}, "tokens": {}, "isBuiltIn": {},
	}
	for field := range source {
		if _, allowed := allowedSourceFields[field]; !allowed {
			t.Fatalf("external integration source detail contains unexpected field %q", field)
		}
	}
	var tokens []map[string]json.RawMessage
	if err := json.Unmarshal(source["tokens"], &tokens); err != nil {
		t.Fatalf("decode external integration source detail token safety objects: %v", err)
	}
	allowedTokenFields := map[string]struct{}{
		"id": {}, "name": {}, "tokenPrefix": {}, "tokenSuffix": {}, "status": {}, "scopes": {},
		"expiresAt": {}, "lastUsedAt": {}, "createdAt": {}, "updatedAt": {}, "revokedAt": {}, "isBuiltIn": {},
	}
	for _, token := range tokens {
		for field := range token {
			if _, allowed := allowedTokenFields[field]; !allowed {
				t.Fatalf("external integration source detail token contains unexpected field %q", field)
			}
		}
	}
}

func w2ExternalSourceListStringPointer(value string) *string {
	return &value
}
