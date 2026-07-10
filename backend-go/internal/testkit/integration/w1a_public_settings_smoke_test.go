//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/maintenance"
)

func TestW1aPublicSettingsSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, filepath.Join(repoRoot(t), "db", "migrations")); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	redisContainer, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	defer terminateContainer(t, ctx, redisContainer)

	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis connection string: %v", err)
	}

	var out bytes.Buffer
	err = maintenance.RunW1aPublicSettingsSmoke(ctx, config.Config{
		PostgresURL:     postgresURL,
		RedisStateURL:   redisURL,
		RedisNamespace:  "w1a-public-settings-smoke",
		TrustProxy:      "true",
		Host:            "127.0.0.1",
		Port:            3000,
		ShutdownTimeout: time.Second,
	}, &out)
	if err != nil {
		t.Fatalf("RunW1aPublicSettingsSmoke() error = %v, output = %s", err, out.String())
	}

	var result maintenance.W1aPublicSettingsSmokeResult
	if err := json.NewDecoder(&out).Decode(&result); err != nil {
		t.Fatalf("decode smoke result: %v", err)
	}
	if !result.Success {
		t.Fatalf("smoke result = %+v", result)
	}
	if result.PublicSettings == nil || result.PublicSettings.AppName != "聚合 AI" || result.PublicSettings.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("public settings = %+v", result.PublicSettings)
	}
	if result.RateLimit == nil || result.RateLimit.PerMinute != 600 || result.RateLimit.BurstPer10Seconds != 120 {
		t.Fatalf("rate limit = %+v", result.RateLimit)
	}
	for name, check := range result.Checks {
		if check.Status != "ok" {
			t.Fatalf("check %s = %+v", name, check)
		}
	}
}
