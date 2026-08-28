package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
		foreignKeys, err := sqliteSchemaForeignKeys(ctx, db, table)
		if err != nil {
			return err
		}
		for _, required := range spec.ForeignKeys {
			if !foreignKeys[businessSQLiteForeignKeySignature(table, required)] {
				return fmt.Errorf("Business SQLite schema %s missing foreign key %s", contracts.BusinessSQLiteSchemaVersion, businessSQLiteForeignKeySignature(table, required))
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

func sqliteSchemaForeignKeys(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite foreign keys for table %s: %w", table, err)
	}
	defer rows.Close()
	type relation struct {
		id       int
		seq      int
		table    string
		from     string
		to       string
		onUpdate string
		onDelete string
	}
	groups := map[int][]relation{}
	for rows.Next() {
		var item relation
		var match string
		if err := rows.Scan(&item.id, &item.seq, &item.table, &item.from, &item.to, &item.onUpdate, &item.onDelete, &match); err != nil {
			return nil, fmt.Errorf("scan Business SQLite foreign keys for table %s: %w", table, err)
		}
		groups[item.id] = append(groups[item.id], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	foreignKeys := map[string]bool{}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].seq < group[j].seq })
		fromColumns := make([]string, 0, len(group))
		toColumns := make([]string, 0, len(group))
		for _, item := range group {
			fromColumns = append(fromColumns, item.from)
			toColumns = append(toColumns, item.to)
		}
		foreignKeys[fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, strings.Join(fromColumns, ","), group[0].table, strings.Join(toColumns, ","), group[0].onDelete, group[0].onUpdate)] = true
	}
	return foreignKeys, nil
}

func businessSQLiteForeignKeySignature(table string, spec contracts.SQLiteForeignKeySpec) string {
	onDelete := spec.OnDelete
	if onDelete == "" {
		onDelete = "NO ACTION"
	}
	onUpdate := spec.OnUpdate
	if onUpdate == "" {
		onUpdate = "NO ACTION"
	}
	return fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, strings.Join(spec.Columns, ","), spec.RefTable, strings.Join(spec.RefColumns, ","), onDelete, onUpdate)
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
