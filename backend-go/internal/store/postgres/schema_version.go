package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const currentGooseSchemaVersionQuery = `WITH latest_versions AS (
	SELECT DISTINCT ON (version_id)
		id,
		version_id,
		is_applied
	FROM goose_db_version
	ORDER BY version_id, id DESC
)
SELECT version_id::text, is_applied
FROM latest_versions
WHERE is_applied = TRUE
ORDER BY id DESC
LIMIT 1`

const newerAppliedGooseSchemaVersionQuery = `WITH latest_versions AS (
	SELECT DISTINCT ON (version_id)
		id,
		version_id,
		is_applied
	FROM goose_db_version
	ORDER BY version_id, id DESC
)
SELECT version_id::text, is_applied
FROM latest_versions
WHERE version_id > $1 AND is_applied = TRUE
ORDER BY id DESC
LIMIT 1`

type schemaVersionQuerier interface {
	QueryRow(ctx context.Context, query string, args ...any) pgx.Row
}

func (s *Store) RequireGooseSchemaVersion(ctx context.Context, expected int64) error {
	return requireGooseSchemaVersion(ctx, s.pool, expected)
}

func requireGooseSchemaVersion(ctx context.Context, querier schemaVersionQuerier, expected int64) error {
	currentVersion, currentApplied, err := scanGooseSchemaVersion(
		querier.QueryRow(ctx, currentGooseSchemaVersionQuery),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("goose schema has no version record; expected %d", expected)
	}
	if err != nil {
		return fmt.Errorf("query current goose schema version: %w", err)
	}
	if !currentApplied {
		return fmt.Errorf("goose schema version %s is not applied; expected %d", currentVersion, expected)
	}
	parsedCurrentVersion, err := parseGooseSchemaVersion(currentVersion)
	if err != nil {
		return fmt.Errorf("parse current goose schema version: %w", err)
	}
	if parsedCurrentVersion != expected {
		return fmt.Errorf(
			"goose schema version mismatch: expected %d applied, got %d applied=true",
			expected,
			parsedCurrentVersion,
		)
	}

	newerVersion, newerApplied, err := scanGooseSchemaVersion(
		querier.QueryRow(ctx, newerAppliedGooseSchemaVersionQuery, expected),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query newer applied goose schema version: %w", err)
	}
	parsedNewerVersion, err := parseGooseSchemaVersion(newerVersion)
	if err != nil {
		return fmt.Errorf("parse newer applied goose schema version: %w", err)
	}
	if newerApplied && parsedNewerVersion > expected {
		return fmt.Errorf("goose schema has newer applied version %d; expected %d", parsedNewerVersion, expected)
	}
	return fmt.Errorf("newer applied goose schema query returned an invalid row")
}

func scanGooseSchemaVersion(row pgx.Row) (string, bool, error) {
	var version string
	var applied bool
	err := row.Scan(&version, &applied)
	return version, applied, err
}

func parseGooseSchemaVersion(raw string) (int64, error) {
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", raw, err)
	}
	return version, nil
}
