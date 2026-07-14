//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2ExternalSourceListActiveID   = "extsrc_w2_list_active"
	w2ExternalSourceListDisabledID = "extsrc_w2_list_disabled"
	w2ExternalSourceListNearID     = "extsrc_w2_list_near"

	w2ExternalSourceListActiveTokenID   = "exttok_w2_list_active_expired"
	w2ExternalSourceListDisabledTokenID = "exttok_w2_list_disabled_newer"
	w2ExternalSourceListRevokedTokenID  = "exttok_w2_list_revoked_newest"
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

	defer cleanupW2ExternalSourceListFixtures(t, ctx, db)
	fixture := insertW2ExternalSourceListFixtures(t, ctx, db)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	service := managementexternalintegrationsources.NewService(store)

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
	if item.Status != publicapi.SourceStatusActive || item.TokenCount != 3 || item.ActiveTokenCount != 1 {
		t.Fatalf("active source token stats = %+v", item)
	}
	if item.PrimaryToken == nil {
		t.Fatalf("active source primary token is nil: %+v", item)
	}
	primary := item.PrimaryToken
	if primary.ID != w2ExternalSourceListActiveTokenID || primary.Status != publicapi.TokenStatusActive {
		t.Fatalf("primary token did not prefer expired active token over newer non-active tokens: %+v", primary)
	}
	if primary.ExpiresAt == nil || *primary.ExpiresAt != fixture.activeTokenExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("expired active primary token expiry = %#v", primary.ExpiresAt)
	}
	if item.CreatedAt != fixture.activeSourceCreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") ||
		item.UpdatedAt != fixture.activeSourceUpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z") ||
		primary.CreatedAt != fixture.activeTokenCreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") ||
		primary.UpdatedAt != fixture.activeTokenUpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("UTC millisecond DTO times = source:%s/%s token:%s/%s",
			item.CreatedAt,
			item.UpdatedAt,
			primary.CreatedAt,
			primary.UpdatedAt,
		)
	}
	if !reflect.DeepEqual(item.Scopes, []string{publicapi.ScopeAccountListRead, publicapi.ScopeGroupListRead}) {
		t.Fatalf("source scopes = %#v", item.Scopes)
	}
	if len(item.RateLimits) != 2 ||
		item.RateLimits[0].WindowSeconds != 1 ||
		item.RateLimits[1].WindowSeconds != 60 {
		t.Fatalf("source rate limits = %#v", item.RateLimits)
	}
	if item.IsBuiltIn || primary.IsBuiltIn {
		t.Fatalf("fixture must not be marked built-in: source=%t token=%t", item.IsBuiltIn, primary.IsBuiltIn)
	}
	assertW2ExternalSourceListSafeDTO(t, all)
}

type w2ExternalSourceListFixtureTimes struct {
	activeSourceCreatedAt time.Time
	activeSourceUpdatedAt time.Time
	activeTokenExpiresAt  time.Time
	activeTokenCreatedAt  time.Time
	activeTokenUpdatedAt  time.Time
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
	tokens := []struct {
		id         string
		name       string
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
			status:     publicapi.TokenStatusActive,
			expiresAt:  &activeTokenExpiresAt,
			lastUsedAt: &activeTokenLastUsedAt,
			createdAt:  activeTokenCreatedAt,
			updatedAt:  activeTokenUpdatedAt,
		},
		{
			id:        w2ExternalSourceListDisabledTokenID,
			name:      "Newer Disabled",
			status:    publicapi.TokenStatusDisabled,
			createdAt: activeTokenCreatedAt.Add(time.Hour),
			updatedAt: activeTokenUpdatedAt.Add(time.Hour),
		},
		{
			id:        w2ExternalSourceListRevokedTokenID,
			name:      "Newest Revoked",
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
			"w2-list-encrypted-"+token.id,
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
	items []managementexternalintegrationsources.Source,
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
	items []managementexternalintegrationsources.Source,
	id string,
) *managementexternalintegrationsources.Source {
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
) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal external integration source list DTO: %v", err)
	}
	body := string(encoded)
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
			t.Fatalf("external integration source list DTO leaked %q: %s", forbidden, body)
		}
	}
}

func w2ExternalSourceListStringPointer(value string) *string {
	return &value
}
