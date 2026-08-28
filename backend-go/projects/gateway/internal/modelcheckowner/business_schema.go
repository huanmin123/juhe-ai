package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// CheckBusinessSQLiteSchema verifies the versioned Gateway dependency set
// without issuing DDL. It is intentionally local to Gateway because shared
// contracts may describe schema but must not own database I/O.
func CheckBusinessSQLiteSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("Business SQLite schema %s: database is nil", contracts.BusinessSQLiteSchemaVersion)
	}
	tables, err := sqliteSchemaObjects(ctx, db, "table")
	if err != nil {
		return fmt.Errorf("list Business SQLite tables: %w", err)
	}
	indexes, err := sqliteSchemaObjects(ctx, db, "index")
	if err != nil {
		return fmt.Errorf("list Business SQLite indexes: %w", err)
	}
	for table, spec := range contracts.BusinessSQLiteSchema {
		if !tables[table] {
			return fmt.Errorf("Business SQLite schema %s missing table %s", contracts.BusinessSQLiteSchemaVersion, table)
		}
		columns, err := sqliteSchemaColumns(ctx, db, table)
		if err != nil {
			return err
		}
		for _, required := range spec.Columns {
			if !columns[required] {
				return fmt.Errorf("Business SQLite schema %s missing column %s.%s", contracts.BusinessSQLiteSchemaVersion, table, required)
			}
		}
		for _, required := range spec.Indexes {
			if !indexes[required] {
				return fmt.Errorf("Business SQLite schema %s missing index %s", contracts.BusinessSQLiteSchemaVersion, required)
			}
		}
	}
	return nil
}

func sqliteSchemaObjects(ctx context.Context, db *sql.DB, objectType string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type=?", objectType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		objects[name] = true
	}
	return objects, rows.Err()
}

func sqliteSchemaColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite table %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
