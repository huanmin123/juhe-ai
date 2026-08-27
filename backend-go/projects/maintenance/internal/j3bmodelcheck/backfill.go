package j3bmodelcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type BackfillReport struct {
	SourceRows   map[string]int64 `json:"sourceRows"`
	InsertedRows map[string]int64 `json:"insertedRows"`
	TargetRows   map[string]int64 `json:"targetRows"`
}

// BackfillSQLite copies only J3b fact tables from read-only legacy SQLite
// files into the dedicated target file. It never updates or deletes existing
// rows; callers must perform conflict/digest review before owner cutover.
func BackfillSQLite(ctx context.Context, target *sql.DB, datasetPath, statsPath string) (BackfillReport, error) {
	if target == nil || strings.TrimSpace(datasetPath) == "" || strings.TrimSpace(statsPath) == "" {
		return BackfillReport{}, errors.New("J3b SQLite backfill requires target, dataset and stats paths")
	}
	dataset, err := openReadOnlySQLite(datasetPath)
	if err != nil {
		return BackfillReport{}, err
	}
	defer dataset.Close()
	stats, err := openReadOnlySQLite(statsPath)
	if err != nil {
		return BackfillReport{}, err
	}
	defer stats.Close()
	ready, err := inspectSQLite(ctx, target)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("verify J3b target schema: %w", err)
	}
	if !ready.Ready() {
		return BackfillReport{}, errors.New("J3b backfill target schema is incomplete")
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("begin J3b SQLite backfill: %w", err)
	}
	defer tx.Rollback()
	report := BackfillReport{SourceRows: map[string]int64{}, InsertedRows: map[string]int64{}, TargetRows: map[string]int64{}}
	for _, item := range []struct {
		db       *sql.DB
		table    string
		optional bool
	}{
		{dataset, "model_check_input_versions", false}, {dataset, "model_check_inputs", false}, {dataset, "model_check_execution_claims", false}, {dataset, "model_check_outcomes", false},
		{dataset, "model_check_runs", false}, {dataset, "model_check_items", false}, {dataset, "model_check_observations", false}, {dataset, "model_check_scheduler_tasks", true}, {stats, "account_quality_health_hourly", false},
	} {
		exists, err := sqliteTableExists(ctx, item.db, item.table)
		if err != nil {
			return BackfillReport{}, err
		}
		if !exists {
			if item.optional {
				report.SourceRows[item.table], report.InsertedRows[item.table], report.TargetRows[item.table] = 0, 0, 0
				continue
			}
			return BackfillReport{}, fmt.Errorf("legacy J3b source table %s is missing", item.table)
		}
		copied, err := copySQLiteTable(ctx, tx, item.db, item.table)
		if err != nil {
			return BackfillReport{}, err
		}
		report.SourceRows[item.table] = copied.source
		report.InsertedRows[item.table] = copied.inserted
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(item.table)).Scan(&count); err != nil {
			return BackfillReport{}, err
		}
		report.TargetRows[item.table] = count
	}
	if err := tx.Commit(); err != nil {
		return BackfillReport{}, fmt.Errorf("commit J3b SQLite backfill: %w", err)
	}
	return report, nil
}

func sqliteTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var found string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == table, nil
}

func openReadOnlySQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+strings.TrimSpace(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open J3b legacy SQLite reader: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

type copyStats struct{ source, inserted int64 }

func copySQLiteTable(ctx context.Context, tx *sql.Tx, source *sql.DB, table string) (copyStats, error) {
	sourceColumns, err := sqliteColumns(ctx, source, table)
	if err != nil {
		return copyStats{}, err
	}
	targetColumns, err := sqliteColumnsTx(ctx, tx, table)
	if err != nil {
		return copyStats{}, err
	}
	columns := intersectColumns(sourceColumns, targetColumns)
	if len(columns) == 0 {
		return copyStats{}, fmt.Errorf("J3b backfill table %s has no compatible columns", table)
	}
	sort.Strings(columns)
	rows, err := source.QueryContext(ctx, "SELECT "+joinQuoted(columns)+" FROM "+quoteIdent(table))
	if err != nil {
		return copyStats{}, fmt.Errorf("read legacy J3b table %s: %w", table, err)
	}
	defer rows.Close()
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO "+quoteIdent(table)+" ("+joinQuoted(columns)+") VALUES ("+strings.Join(placeholders, ",")+")")
	if err != nil {
		return copyStats{}, fmt.Errorf("prepare J3b backfill table %s: %w", table, err)
	}
	defer stmt.Close()
	primaryKeys, err := sqlitePrimaryKeys(ctx, tx, table)
	if err != nil || len(primaryKeys) == 0 {
		return copyStats{}, fmt.Errorf("J3b backfill table %s has no primary key: %w", table, err)
	}
	index := make(map[string]int, len(columns))
	for i, column := range columns {
		index[column] = i
	}
	for _, key := range primaryKeys {
		if _, ok := index[key]; !ok {
			return copyStats{}, fmt.Errorf("J3b backfill table %s primary key %s is not copyable", table, key)
		}
	}
	var stats copyStats
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return copyStats{}, fmt.Errorf("scan legacy J3b table %s: %w", table, err)
		}
		stats.source++
		where := make([]string, len(primaryKeys))
		args := make([]any, len(primaryKeys))
		for i, key := range primaryKeys {
			where[i] = quoteIdent(key) + "=?"
			args[i] = values[index[key]]
		}
		existing := make([]any, len(columns))
		existingPointers := make([]any, len(existing))
		for i := range existing {
			existingPointers[i] = &existing[i]
		}
		err = tx.QueryRowContext(ctx, "SELECT "+joinQuoted(columns)+" FROM "+quoteIdent(table)+" WHERE "+strings.Join(where, " AND "), args...).Scan(existingPointers...)
		if err == nil {
			for i := range values {
				if normalizeValue(existing[i]) != normalizeValue(values[i]) {
					return copyStats{}, fmt.Errorf("J3b backfill conflict in %s primary key row", table)
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return copyStats{}, fmt.Errorf("check existing J3b backfill row %s: %w", table, err)
		}
		result, err := stmt.ExecContext(ctx, values...)
		if err != nil {
			return copyStats{}, fmt.Errorf("insert J3b backfill table %s: %w", table, err)
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			stats.inserted++
		}
	}
	if err := rows.Err(); err != nil {
		return copyStats{}, err
	}
	return stats, nil
}

func sqlitePrimaryKeys(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[int]string{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			keys[pk] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	positions := make([]int, 0, len(keys))
	for position := range keys {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	result := make([]string, len(positions))
	for i, position := range positions {
		result[i] = keys[position]
	}
	return result, nil
}

func normalizeValue(value any) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case nil:
		return "<nil>"
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func sqliteColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("legacy J3b table %s is missing", table)
	}
	return columns, nil
}

func sqliteColumnsTx(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("target J3b table %s is missing", table)
	}
	return columns, nil
}

func intersectColumns(source, target []string) []string {
	set := make(map[string]bool, len(target))
	for _, value := range target {
		set[value] = true
	}
	result := make([]string, 0, len(source))
	for _, value := range source {
		if set[value] {
			result = append(result, value)
		}
	}
	return result
}

func quoteIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func joinQuoted(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	quoted := make([]string, len(copied))
	for i, value := range copied {
		quoted[i] = quoteIdent(value)
	}
	return strings.Join(quoted, ",")
}
