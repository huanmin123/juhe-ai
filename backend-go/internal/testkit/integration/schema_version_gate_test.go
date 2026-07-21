//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/version"
)

func TestGoServerSchemaVersionGateSmokeUsesGooseForFutureVersion(t *testing.T) {
	source, err := os.ReadFile("schema_version_gate_test.go")
	if err != nil {
		t.Fatalf("read schema version gate smoke source: %v", err)
	}
	sourceText := string(source)
	if strings.Contains(sourceText, "db."+"ExecContext") {
		t.Fatal("future-version setup must use Goose instead of directly writing goose_db_version")
	}
	for _, want := range []string{
		"os." + "WriteFile",
		"goose." + "DownTo(db, schemaGateMigrationDir, version.SchemaVersion)",
		"goose." + "UpTo(db, schemaGateMigrationDir, version.SchemaVersion+1)",
	} {
		if !strings.Contains(sourceText, want) {
			t.Fatalf("future-version setup source missing %q", want)
		}
	}
}

func TestGoServerSchemaVersionGatePostgresSmoke(t *testing.T) {
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

	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, connString)
	defer closeSQLDB(t, db)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.UpTo(db, migrationDir, version.SchemaVersion-1); err != nil {
		t.Fatalf("goose up to %d: %v", version.SchemaVersion-1, err)
	}

	store, err := postgresstore.Open(ctx, connString)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	if err := store.RequireGooseSchemaVersion(ctx, version.SchemaVersion); err == nil || !strings.Contains(err.Error(), "expected 63") {
		t.Fatalf("schema gate at version %d error = %v, want version mismatch", version.SchemaVersion-1, err)
	}
	if err := goose.UpTo(db, migrationDir, version.SchemaVersion); err != nil {
		t.Fatalf("goose up to %d: %v", version.SchemaVersion, err)
	}
	if err := store.RequireGooseSchemaVersion(ctx, version.SchemaVersion); err != nil {
		t.Fatalf("schema gate at version %d: %v", version.SchemaVersion, err)
	}

	schemaGateMigrationDir := t.TempDir()
	const schemaGateMigration = `-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
`
	if err := os.WriteFile(
		filepath.Join(schemaGateMigrationDir, "000064_schema_gate_test.sql"),
		[]byte(schemaGateMigration),
		0o600,
	); err != nil {
		t.Fatalf("write schema gate future-version migration: %v", err)
	}
	if err := goose.UpTo(db, schemaGateMigrationDir, version.SchemaVersion+1); err != nil {
		t.Fatalf("goose up to %d with schema gate test migration: %v", version.SchemaVersion+1, err)
	}
	if err := goose.DownTo(db, schemaGateMigrationDir, version.SchemaVersion); err != nil {
		t.Fatalf("goose down to %d with schema gate test migration: %v", version.SchemaVersion, err)
	}
	if err := store.RequireGooseSchemaVersion(ctx, version.SchemaVersion); err != nil {
		t.Fatalf("schema gate after future-version rollback to %d: %v", version.SchemaVersion, err)
	}
	if err := goose.UpTo(db, schemaGateMigrationDir, version.SchemaVersion+1); err != nil {
		t.Fatalf("goose reapply version %d with schema gate test migration: %v", version.SchemaVersion+1, err)
	}
	if err := store.RequireGooseSchemaVersion(ctx, version.SchemaVersion); err == nil {
		t.Fatalf("schema gate with applied version %d error = nil, want rejection", version.SchemaVersion+1)
	}
}
