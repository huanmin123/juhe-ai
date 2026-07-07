//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/publicapikeys"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicAPIKeysPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	groupService := publicgroups.NewService(publicgroups.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID:      sequenceID("w1b_api_key_group"),
	})
	routeService := publicroutestrategies.NewService(publicroutestrategies.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID:      sequenceID("w1b_api_key_route"),
	})
	apiKeyService := publicapikeys.NewService(publicapikeys.Options{
		Store:      store,
		Transactor: store,
		Now:        func() time.Time { return now },
		NewID:      sequenceID("w1b_api_key"),
		NewSecret: sequentialIntegrationSecrets(
			"sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"sk-fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		),
	})

	if _, err := apiKeyService.Add(ctx, publicapikeys.AddInput{TargetUsername: "missing", Name: "缺失", RouteStrategyID: "rts_missing"}); !errors.Is(err, publicapikeys.ErrTargetNotFound) {
		t.Fatalf("add missing target err = %v, want ErrTargetNotFound", err)
	}

	group, err := groupService.Add(ctx, publicgroups.AddInput{
		TargetUsername:    "admin",
		TargetDisplayName: "管理员",
		Name:              "API Key 分组",
		ProviderCode:      "gpt",
	})
	if err != nil {
		t.Fatalf("add group: %v", err)
	}
	route, err := routeService.Add(ctx, publicroutestrategies.AddInput{
		TargetUsername: "admin",
		Name:           "API Key 策略",
		GroupBindings:  []publicroutestrategies.GroupBindingInput{{GroupID: group.Group.ID}},
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	created, err := apiKeyService.Add(ctx, publicapikeys.AddInput{
		TargetUsername:  "admin",
		Name:            "公开 API Key",
		RouteStrategyID: route.RouteStrategy.ID,
		QuotaLimits: publicapikeys.NewJSONValue(map[string]any{
			"daily": map[string]any{"enabled": true, "limit": json.Number("100")},
		}, true),
		AvailabilitySchedule: publicapikeys.NewJSONValue(map[string]any{
			"enabled": true,
			"mode":    "allow_windows",
			"windows": []any{map[string]any{"daysOfWeek": []any{json.Number("2")}, "start": "09:00", "end": "18:00"}},
		}, true),
	})
	if err != nil {
		t.Fatalf("add api key: %v", err)
	}
	if created.Action != "created" || created.APIKey == nil || created.APIKey.Key == "" || created.APIKey.KeySuffix == "" {
		t.Fatalf("created api key = %+v", created)
	}
	apiKeyID := created.APIKey.ID
	assertW1bPublicAPIKeyStored(t, ctx, db, apiKeyID, created.APIKey.Key)

	if _, err := apiKeyService.Add(ctx, publicapikeys.AddInput{TargetUsername: "admin", Name: "公开 API Key", RouteStrategyID: route.RouteStrategy.ID}); !errors.Is(err, publicapikeys.ErrDuplicateAPIKeyName) {
		t.Fatalf("duplicate add err = %v, want ErrDuplicateAPIKeyName", err)
	}

	listed, err := apiKeyService.List(ctx, publicapikeys.ListInput{
		TargetUsername:  "admin",
		RouteStrategyID: route.RouteStrategy.ID,
		Keyword:         "公开",
		Status:          "all",
		Page:            1,
		PageSize:        10,
	})
	if err != nil {
		t.Fatalf("list api keys: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != apiKeyID || listed.Items[0].Key != "" || listed.Items[0].QuotaLimits == nil {
		t.Fatalf("listed api keys = %+v", listed.Items)
	}

	expiresAt := "2026-08-01T00:00:00Z"
	updated, err := apiKeyService.Update(ctx, publicapikeys.UpdateInput{
		APIKeyID:  apiKeyID,
		Name:      ptrIntegrationString("公开 API Key 更新"),
		ExpiresAt: publicapikeys.NewOptionalString(&expiresAt, true),
	})
	if err != nil {
		t.Fatalf("update api key: %v", err)
	}
	if updated.APIKey == nil || updated.APIKey.Key != "" || updated.APIKey.ExpiresAt != expiresAt {
		t.Fatalf("updated api key = %+v", updated.APIKey)
	}

	insertW1bPublicAPIDefaultKey(t, ctx, db, created.Target.SystemAccountID, route.RouteStrategy.ID, now)
	if _, err := apiKeyService.Delete(ctx, publicapikeys.DeleteInput{APIKeyID: "key_w1b_api_default"}); !errors.Is(err, publicapikeys.ErrDefaultAPIKeyDelete) {
		t.Fatalf("delete default api key err = %v, want ErrDefaultAPIKeyDelete", err)
	}

	deleted, err := apiKeyService.Delete(ctx, publicapikeys.DeleteInput{APIKeyID: apiKeyID})
	if err != nil {
		t.Fatalf("delete api key: %v", err)
	}
	if deleted.Action != "deleted" || deleted.APIKey == nil || deleted.APIKey.Key != "" {
		t.Fatalf("deleted api key = %+v", deleted)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*)::int FROM juhe_business.api_keys WHERE id = $1", apiKeyID).Scan(&count); err != nil {
		t.Fatalf("count deleted api key: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted api key count = %d, want 0", count)
	}
}

func assertW1bPublicAPIKeyStored(t *testing.T, ctx context.Context, db *sql.DB, id string, secret string) {
	t.Helper()

	var keyHash, keyPrefix, keySuffix string
	var quotaValid, scheduleValid bool
	err := db.QueryRowContext(ctx, `
		SELECT key_hash, key_prefix, key_suffix, quota_limits_json IS NOT NULL, availability_schedule_json IS NOT NULL
		FROM juhe_business.api_keys
		WHERE id = $1
	`, id).Scan(&keyHash, &keyPrefix, &keySuffix, &quotaValid, &scheduleValid)
	if err != nil {
		t.Fatalf("read api key: %v", err)
	}
	sum := sha256.Sum256([]byte(secret))
	if keyHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("key_hash = %q, want hash of secret", keyHash)
	}
	if keyPrefix != secret[:8] || keySuffix != secret[len(secret)-8:] {
		t.Fatalf("prefix/suffix = %q/%q", keyPrefix, keySuffix)
	}
	if !quotaValid || !scheduleValid {
		t.Fatalf("quota/schedule valid = %v/%v", quotaValid, scheduleValid)
	}
}

func insertW1bPublicAPIDefaultKey(t *testing.T, ctx context.Context, db *sql.DB, ownerID string, routeID string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.api_keys (
			id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, status, is_default, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', true, $8, $9)
	`, "key_w1b_api_default", ownerID, routeID, "默认 API Key", "hash_w1b_api_default", "sk-defau", "default", now, now)
	if err != nil {
		t.Fatalf("insert default api key: %v", err)
	}
}

func sequenceID(scope string) func(string) string {
	seq := 0
	return func(prefix string) string {
		seq++
		return prefix + "_" + scope + "_" + strconv.Itoa(seq)
	}
}

func fixedIntegrationSecret(secret string) func() (string, error) {
	return func() (string, error) {
		return secret, nil
	}
}

func sequentialIntegrationSecrets(secrets ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index >= len(secrets) {
			return secrets[len(secrets)-1], nil
		}
		secret := secrets[index]
		index++
		return secret, nil
	}
}

func ptrIntegrationString(value string) *string {
	return &value
}
