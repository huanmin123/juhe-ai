//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/modules/managementproxies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW2ManagementProxyOptionsPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementproxies.NewService(store)
	all, err := service.Options(ctx, managementproxies.OptionListInput{Limit: 50})
	if err != nil {
		t.Fatalf("list all proxy options: %v", err)
	}
	if got, want := len(all), 3; got != want {
		t.Fatalf("all proxy options length = %d, want %d: %+v", got, want, all)
	}
	for _, item := range all {
		if !item.Enabled {
			t.Fatalf("disabled proxy leaked into options: %+v", item)
		}
		if item.ID == "proxy_w2_disabled" {
			t.Fatalf("disabled proxy id leaked into options: %+v", all)
		}
	}

	prefixed, err := service.Options(ctx, managementproxies.OptionListInput{Keyword: "Al", Limit: 1})
	if err != nil {
		t.Fatalf("list prefixed proxy options: %v", err)
	}
	if len(prefixed) != 1 || prefixed[0].ID != "proxy_w2_alpha" || prefixed[0].Name != "Alpha" {
		t.Fatalf("prefixed proxy options = %+v, want only Alpha", prefixed)
	}
}

func insertW2ProxyOptionsFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			'sys_w2_proxy_options', 'w2-proxy-options', 'W2 Proxy Options', NULL, 'admin', 'active', 'hash',
			false, false, $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W2 proxy system account: %v", err)
	}

	fixtures := []struct {
		id      string
		name    string
		enabled bool
	}{
		{id: "proxy_w2_alpha", name: "Alpha", enabled: true},
		{id: "proxy_w2_alpine", name: "Alpine", enabled: true},
		{id: "proxy_w2_beta", name: "Beta", enabled: true},
		{id: "proxy_w2_disabled", name: "Alps Disabled", enabled: false},
	}
	for index, item := range fixtures {
		_, err = db.ExecContext(ctx, `
			INSERT INTO juhe_business.proxy_profiles (
				id, system_account_id, name, description, type, host, port, username, password_encrypted,
				enabled, test_status, created_at, updated_at
			) VALUES (
				$1, 'sys_w2_proxy_options', $2, NULL, 'http', '127.0.0.1', $3, NULL, NULL,
				$4, 'unknown', $5, $6
			)
		`, item.id, item.name, 8000+index, item.enabled, now, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("insert W2 proxy fixture %s: %v", item.id, err)
		}
	}
}
