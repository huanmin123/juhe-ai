package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

type SchemaUpResult struct {
	Success        bool   `json:"success"`
	Directory      string `json:"directory"`
	TargetVersion  int64  `json:"targetVersion"`
	CurrentVersion int64  `json:"currentVersion"`
}

// RunSchemaUp executes the catalog through Goose. It intentionally does not
// write goose_db_version directly; Goose records each migration only after its
// SQL has committed successfully.
func RunSchemaUp(ctx context.Context, postgresURL, directory string, out io.Writer) error {
	if strings.TrimSpace(postgresURL) == "" {
		return errors.New("JUHE_AI_POSTGRES_URL is required for schema-up")
	}
	if err := validateCurrentMigrationCatalog(directory); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	db, err := sql.Open("pgx", postgresURL)
	if err != nil {
		return errors.New("open PostgreSQL for schema-up failed")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("ping PostgreSQL for schema-up failed")
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set Goose PostgreSQL dialect: %w", err)
	}

	return runSchemaUpCatalog(
		ctx,
		directory,
		out,
		func(runContext context.Context, target int64) error {
			return goose.UpToContext(runContext, db, directory, target)
		},
		func(runContext context.Context) (int64, error) {
			return goose.GetDBVersionContext(runContext, db)
		},
	)
}

func runSchemaUpCatalog(
	ctx context.Context,
	directory string,
	out io.Writer,
	migrate func(context.Context, int64) error,
	currentVersion func(context.Context) (int64, error),
) error {
	if err := validateCurrentMigrationCatalog(directory); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target := migrationcatalog.CurrentSchemaVersion
	if err := migrate(ctx, target); err != nil {
		return fmt.Errorf("goose up to schema %d: %w", target, err)
	}
	version, err := currentVersion(ctx)
	if err != nil {
		return fmt.Errorf("read current Goose version: %w", err)
	}
	if version != target {
		return fmt.Errorf("current Goose version is %d, want %d", version, target)
	}
	result := SchemaUpResult{
		Success:        true,
		Directory:      filepath.Clean(directory),
		TargetVersion:  target,
		CurrentVersion: version,
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("write schema-up result: %w", err)
	}
	return nil
}

func validateCurrentMigrationCatalog(directory string) error {
	if strings.TrimSpace(directory) == "" {
		return errors.New("migration directory is required for schema-up")
	}
	catalog, err := migrationcatalog.Inspect(os.DirFS(directory))
	if err != nil {
		return fmt.Errorf("inspect migration catalog for schema-up: %w", err)
	}
	if len(catalog.Entries) != int(migrationcatalog.CurrentSchemaVersion) {
		return fmt.Errorf(
			"migration catalog has %d entries, want %d",
			len(catalog.Entries),
			migrationcatalog.CurrentSchemaVersion,
		)
	}
	for index, entry := range catalog.Entries {
		want := int64(index + 1)
		if entry.Version != want {
			return fmt.Errorf("migration catalog entry %d has version %d, want %d", index, entry.Version, want)
		}
	}
	return nil
}
