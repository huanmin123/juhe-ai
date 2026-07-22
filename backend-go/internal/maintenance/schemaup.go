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
	"github.com/pressly/goose/v3/lock"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

type SchemaUpResult struct {
	Success        bool   `json:"success"`
	Directory      string `json:"directory"`
	TargetVersion  int64  `json:"targetVersion"`
	CurrentVersion int64  `json:"currentVersion"`
}

type schemaUpSourceState struct {
	gooseLedgerPresent bool
	gooseLedgerRows    int64
	juheRelationCount  int64
}

// RunSchemaUp lets Goose own both migration execution and ledger updates.
func RunSchemaUp(ctx context.Context, postgresURL, directory string, out io.Writer) (resultErr error) {
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
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(1, 30),
		lock.WithUnlockTimeout(1, 10),
	)
	if err != nil {
		return fmt.Errorf("create Goose PostgreSQL session locker: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.New("reserve PostgreSQL connection for schema-up failed")
	}
	defer func() {
		resultErr = errors.Join(resultErr, conn.Close())
	}()
	if err := locker.SessionLock(ctx, conn); err != nil {
		return fmt.Errorf("acquire Goose PostgreSQL session lock: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, locker.SessionUnlock(context.WithoutCancel(ctx), conn))
	}()
	lockedState, err := inspectSchemaUpSource(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateSchemaUpSourceState(lockedState); err != nil {
		return err
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS(directory),
	)
	if err != nil {
		return fmt.Errorf("create Goose PostgreSQL provider: %w", err)
	}

	return runSchemaUpCatalog(
		ctx,
		directory,
		out,
		func(runContext context.Context, target int64) error {
			_, err := provider.UpTo(runContext, target)
			return err
		},
		func(runContext context.Context) (int64, error) {
			return provider.GetDBVersion(runContext)
		},
	)
}

func inspectSchemaUpSource(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (schemaUpSourceState, error) {
	var state schemaUpSourceState
	if err := queryer.QueryRowContext(
		ctx,
		"SELECT to_regclass('public.goose_db_version') IS NOT NULL",
	).Scan(&state.gooseLedgerPresent); err != nil {
		return schemaUpSourceState{}, errors.New("inspect PostgreSQL Goose ledger presence failed")
	}
	if state.gooseLedgerPresent {
		if err := queryer.QueryRowContext(
			ctx,
			"SELECT COUNT(*) FROM public.goose_db_version",
		).Scan(&state.gooseLedgerRows); err != nil {
			return schemaUpSourceState{}, errors.New("inspect PostgreSQL Goose ledger rows failed")
		}
	}

	if err := queryer.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_catalog.pg_class c
INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE left(n.nspname, 5) = 'juhe_'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'i', 'I', 'S', 'f')
`).Scan(&state.juheRelationCount); err != nil {
		return schemaUpSourceState{}, errors.New("inspect PostgreSQL juhe business objects failed")
	}
	return state, nil
}

func validateSchemaUpSourceState(state schemaUpSourceState) error {
	if state.gooseLedgerRows < 0 || state.juheRelationCount < 0 {
		return errors.New("PostgreSQL schema source inspection returned an invalid count")
	}
	if state.gooseLedgerPresent {
		if state.gooseLedgerRows == 0 {
			return errors.New("PostgreSQL goose_db_version exists without migration history")
		}
		return nil
	}
	if state.gooseLedgerRows != 0 {
		return errors.New("PostgreSQL Goose ledger source state is inconsistent")
	}
	if state.juheRelationCount > 0 {
		return errors.New("PostgreSQL has juhe business objects without Goose history; rebuild offline before schema-up")
	}
	return nil
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
