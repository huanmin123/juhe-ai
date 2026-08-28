package j3bmodelcheck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type BackfillReport struct {
	SourceRows   map[string]int64  `json:"sourceRows"`
	InsertedRows map[string]int64  `json:"insertedRows"`
	TargetRows   map[string]int64  `json:"targetRows"`
	SourceDigest map[string]string `json:"sourceDigest"`
	TargetDigest map[string]string `json:"targetDigest"`
}

// BackfillVerificationReport is a read-only, post-cutover evidence report.
// It compares the legacy dataset/stats files with the dedicated J3b file
// after backfill. No source or target file is opened with write permissions.
type BackfillVerificationReport struct {
	Ready          bool              `json:"ready"`
	PathsDistinct  bool              `json:"pathsDistinct"`
	SourceReadOnly bool              `json:"sourceReadOnly"`
	TargetReadOnly bool              `json:"targetReadOnly"`
	Tables         map[string]string `json:"tables"`
	SourceRows     map[string]int64  `json:"sourceRows"`
	TargetRows     map[string]int64  `json:"targetRows"`
	SourceDigest   map[string]string `json:"sourceDigest"`
	TargetDigest   map[string]string `json:"targetDigest"`
}

// VerifySQLiteBackfill compares every J3b fact table in the legacy dataset
// and stats files with the dedicated target. It is intentionally read-only:
// this command is safe to run after cutover and is suitable for rollback
// evidence. Missing mandatory source tables, shared physical files, row
// count drift, or digest drift all fail closed (Ready=false).
func VerifySQLiteBackfill(ctx context.Context, targetPath, datasetPath, statsPath string) (BackfillVerificationReport, error) {
	targetPath = strings.TrimSpace(targetPath)
	datasetPath = strings.TrimSpace(datasetPath)
	statsPath = strings.TrimSpace(statsPath)
	if targetPath == "" || datasetPath == "" || statsPath == "" {
		return BackfillVerificationReport{}, errors.New("J3b SQLite readback requires target, dataset and stats paths")
	}
	distinct, err := distinctSQLitePaths(targetPath, datasetPath, statsPath)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	report := BackfillVerificationReport{
		PathsDistinct: distinct,
		Tables:        map[string]string{},
		SourceRows:    map[string]int64{},
		TargetRows:    map[string]int64{},
		SourceDigest:  map[string]string{},
		TargetDigest:  map[string]string{},
	}
	if !distinct {
		report.Tables["__paths__"] = "shared physical file"
		report.Ready = false
		return report, nil
	}
	target, err := openReadOnlySQLite(targetPath)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	defer target.Close()
	dataset, err := openReadOnlySQLite(datasetPath)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	defer dataset.Close()
	stats, err := openReadOnlySQLite(statsPath)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	defer stats.Close()
	report.SourceReadOnly, err = verifyQueryOnly(ctx, dataset)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	report.TargetReadOnly, err = verifyQueryOnly(ctx, target)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	statsReadOnly, err := verifyQueryOnly(ctx, stats)
	if err != nil {
		return BackfillVerificationReport{}, err
	}
	if !report.SourceReadOnly || !report.TargetReadOnly || !statsReadOnly {
		report.Tables["__query_only__"] = "one or more readers are writable"
		report.Ready = false
		return report, nil
	}
	if schema, err := inspectSQLite(ctx, target); err != nil {
		return BackfillVerificationReport{}, fmt.Errorf("verify J3b readback target schema: %w", err)
	} else if !schema.Ready() {
		report.Tables["__schema__"] = "target schema incomplete"
		return report, nil
	}
	ready := true
	for _, item := range []struct {
		db       *sql.DB
		table    string
		optional bool
	}{
		{dataset, "model_check_runs", false}, {dataset, "model_check_items", false}, {dataset, "model_check_observations", false},
		{stats, "account_quality_health_hourly", false}, {stats, "model_token_intercept_baseline_versions", false},
		{dataset, "model_check_input_versions", true}, {dataset, "model_check_inputs", true}, {dataset, "model_check_execution_claims", true}, {dataset, "model_check_outcomes", true}, {dataset, "model_check_scheduler_tasks", true},
	} {
		exists, err := sqliteTableExists(ctx, item.db, item.table)
		if err != nil {
			return BackfillVerificationReport{}, err
		}
		if !exists {
			if item.optional {
				report.Tables[item.table] = "optional source absent"
				report.SourceRows[item.table] = 0
				report.TargetRows[item.table], err = tableRowCount(ctx, target, item.table)
				if err != nil {
					return BackfillVerificationReport{}, err
				}
				if report.TargetRows[item.table] != 0 {
					report.Tables[item.table] = "target has rows but source absent"
					ready = false
				}
				continue
			}
			report.Tables[item.table] = "mandatory source table absent"
			ready = false
			continue
		}
		sourceRows, sourceDigest, err := sqliteTableEvidence(ctx, item.db, item.table)
		if err != nil {
			return BackfillVerificationReport{}, err
		}
		targetRows, targetDigest, err := sqliteTableEvidenceAgainstSource(ctx, target, item.db, item.table)
		if err != nil {
			return BackfillVerificationReport{}, err
		}
		report.SourceRows[item.table], report.TargetRows[item.table] = sourceRows, targetRows
		report.SourceDigest[item.table], report.TargetDigest[item.table] = sourceDigest, targetDigest
		if sourceRows != targetRows || sourceDigest != targetDigest {
			report.Tables[item.table] = "drift"
			ready = false
		} else {
			report.Tables[item.table] = "match"
		}
	}
	report.Ready = ready
	return report, nil
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
	report := BackfillReport{SourceRows: map[string]int64{}, InsertedRows: map[string]int64{}, TargetRows: map[string]int64{}, SourceDigest: map[string]string{}, TargetDigest: map[string]string{}}
	for _, item := range []struct {
		db       *sql.DB
		table    string
		optional bool
	}{
		// Node legacy dataset owns run/item/observation facts. The input,
		// claim, outcome and scheduler tables are Go-owned additions and are
		// intentionally absent before cutover; their empty target tables are
		// still validated by inspectSQLite above.
		{dataset, "model_check_input_versions", true}, {dataset, "model_check_inputs", true}, {dataset, "model_check_execution_claims", true}, {dataset, "model_check_outcomes", true},
		{dataset, "model_check_runs", false}, {dataset, "model_check_items", false}, {dataset, "model_check_observations", false}, {dataset, "model_check_scheduler_tasks", true}, {stats, "account_quality_health_hourly", false}, {stats, "model_token_intercept_baseline_versions", false},
	} {
		exists, err := sqliteTableExists(ctx, item.db, item.table)
		if err != nil {
			return BackfillReport{}, err
		}
		if !exists {
			if item.optional {
				var targetRows int64
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(item.table)).Scan(&targetRows); err != nil {
					return BackfillReport{}, fmt.Errorf("count J3b target table %s: %w", item.table, err)
				}
				report.SourceRows[item.table], report.InsertedRows[item.table], report.TargetRows[item.table] = 0, 0, targetRows
				report.SourceDigest[item.table] = ""
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
		report.SourceDigest[item.table] = copied.digest
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(item.table)).Scan(&count); err != nil {
			return BackfillReport{}, err
		}
		report.TargetRows[item.table] = count
	}
	if err := tx.Commit(); err != nil {
		return BackfillReport{}, fmt.Errorf("commit J3b SQLite backfill: %w", err)
	}
	for table := range report.TargetRows {
		digest, err := sqliteTableDigest(ctx, target, table)
		if err != nil {
			return BackfillReport{}, fmt.Errorf("digest J3b target table %s: %w", table, err)
		}
		report.TargetDigest[table] = digest
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

func tableRowCount(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count J3b table %s: %w", table, err)
	}
	return count, nil
}

func sqliteTableEvidence(ctx context.Context, db *sql.DB, table string) (int64, string, error) {
	columns, err := sqliteColumns(ctx, db, table)
	if err != nil {
		return 0, "", err
	}
	digest, err := sqliteTableDigestColumns(ctx, db, table, columns)
	if err != nil {
		return 0, "", err
	}
	count, err := tableRowCount(ctx, db, table)
	return count, digest, err
}

// sqliteTableEvidenceAgainstSource hashes the target using exactly the
// columns present in the source. Legacy files may predate newly-added target
// columns; comparing the common projection keeps readback deterministic while
// still detecting row/value drift.
func sqliteTableEvidenceAgainstSource(ctx context.Context, target, source *sql.DB, table string) (int64, string, error) {
	sourceColumns, err := sqliteColumns(ctx, source, table)
	if err != nil {
		return 0, "", err
	}
	targetColumns, err := sqliteColumns(ctx, target, table)
	if err != nil {
		return 0, "", err
	}
	columns := intersectColumns(sourceColumns, targetColumns)
	if len(columns) == 0 {
		return 0, "", fmt.Errorf("J3b readback table %s has no common columns", table)
	}
	digest, err := sqliteTableDigestColumns(ctx, target, table, columns)
	if err != nil {
		return 0, "", err
	}
	count, err := tableRowCount(ctx, target, table)
	return count, digest, err
}

func sqliteTableDigestColumns(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
	keys, err := sqlitePrimaryKeysDB(ctx, db, table)
	if err != nil || len(keys) == 0 {
		return "", fmt.Errorf("table %s has no primary key: %w", table, err)
	}
	columnSet := make(map[string]bool, len(columns))
	for _, column := range columns {
		columnSet[column] = true
	}
	for _, key := range keys {
		if !columnSet[key] {
			return "", fmt.Errorf("table %s primary key %s is not in digest projection", table, key)
		}
	}
	query := "SELECT " + joinQuoted(columns) + " FROM " + quoteIdent(table) + " ORDER BY " + joinQuoted(keys)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return "", err
		}
		writeDigestRow(digest, values)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func distinctSQLitePaths(paths ...string) (bool, error) {
	resolved := make([]string, len(paths))
	infos := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return false, err
		}
		resolved[i] = abs
		info, err := os.Stat(abs)
		if err != nil {
			return false, fmt.Errorf("stat J3b SQLite path %s: %w", abs, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("J3b SQLite path %s is not a regular file", abs)
		}
		infos[i] = info
	}
	for i := range resolved {
		for j := i + 1; j < len(resolved); j++ {
			if resolved[i] == resolved[j] || os.SameFile(infos[i], infos[j]) {
				return false, nil
			}
		}
	}
	return true, nil
}

func openReadOnlySQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+strings.TrimSpace(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open J3b legacy SQLite reader: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func verifyQueryOnly(ctx context.Context, db *sql.DB) (bool, error) {
	var value int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&value); err != nil {
		return false, fmt.Errorf("verify SQLite query_only: %w", err)
	}
	return value == 1, nil
}

type copyStats struct {
	source, inserted int64
	digest           string
}

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
	digest := sha256.New()
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
		writeDigestRow(digest, values)
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
	stats.digest = hex.EncodeToString(digest.Sum(nil))
	return stats, nil
}

func sqliteTableDigest(ctx context.Context, db *sql.DB, table string) (string, error) {
	columns, err := sqliteColumns(ctx, db, table)
	if err != nil {
		return "", err
	}
	keys, err := sqlitePrimaryKeysDB(ctx, db, table)
	if err != nil || len(keys) == 0 {
		return "", fmt.Errorf("table %s has no primary key: %w", table, err)
	}
	query := "SELECT " + joinQuoted(columns) + " FROM " + quoteIdent(table) + " ORDER BY " + joinQuoted(keys)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return "", err
		}
		writeDigestRow(digest, values)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeDigestRow(digest interface{ Write([]byte) (int, error) }, values []any) {
	for _, value := range values {
		encoded := normalizeValue(value)
		_, _ = fmt.Fprintf(digest, "%d:", len(encoded))
		_, _ = digest.Write([]byte(encoded))
	}
	_, _ = digest.Write([]byte{0})
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

func sqlitePrimaryKeysDB(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
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
