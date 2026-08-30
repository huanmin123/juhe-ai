package j3bmodelcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultPostgresBackfillMaxRows  int64 = DefaultPostgresReadbackMaxRows
	MaximumPostgresBackfillMaxRows  int64 = MaximumPostgresReadbackMaxRows
	DefaultPostgresBackfillMaxBytes int64 = 64 * 1024 * 1024
	MaximumPostgresBackfillMaxBytes int64 = 1024 * 1024 * 1024
)

// PostgresBackfillOptions bounds one explicit legacy-to-juhe_j3b invocation.
// Zero values use conservative defaults. The caller must still provide the
// database connection explicitly; this API never opens a connection itself.
type PostgresBackfillOptions struct {
	MaxRowsPerTable  int64
	MaxBytesPerTable int64
}

// PostgresBackfillReport records the rows copied by BackfillPostgres. The
// report is returned only after the single transaction commits successfully.
type PostgresBackfillReport struct {
	TransactionIsolation string                           `json:"transactionIsolation"`
	MaxRowsPerTable      int64                            `json:"maxRowsPerTable"`
	MaxBytesPerTable     int64                            `json:"maxBytesPerTable"`
	Tables               map[string]PostgresBackfillTable `json:"tables"`
}

type PostgresBackfillTable struct {
	SourceSchema string   `json:"sourceSchema"`
	TargetSchema string   `json:"targetSchema"`
	Projection   []string `json:"projection"`
	PrimaryKeys  []string `json:"primaryKeys"`
	SourceRows   int64    `json:"sourceRows"`
	InsertedRows int64    `json:"insertedRows"`
	SkippedRows  int64    `json:"skippedRows"`
	SourceBytes  int64    `json:"sourceBytes"`
}

type postgresBackfillColumn struct {
	Name       string
	DataType   string
	UdtName    string
	Nullable   bool
	HasDefault bool
}

// BackfillPostgres copies the five explicitly whitelisted Node legacy facts
// into juhe_j3b. It uses one serializable transaction, validates target and
// source structure before the first INSERT, and never UPDATEs or DELETEs a
// target row. Existing rows are compared over the complete validated public
// projection: equal rows are skipped, while any mismatch aborts the
// transaction.
//
// This function is deliberately not called by package initialization, CLI
// setup, or any connection factory. A caller must explicitly pass *sql.DB.
func BackfillPostgres(ctx context.Context, db *sql.DB, options PostgresBackfillOptions) (PostgresBackfillReport, error) {
	if db == nil {
		return PostgresBackfillReport{}, errors.New("J3b PostgreSQL backfill database is not initialized")
	}
	normalized, err := normalizePostgresBackfillOptions(options)
	if err != nil {
		return PostgresBackfillReport{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("begin J3b PostgreSQL backfill: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"); err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("configure J3b PostgreSQL backfill transaction: %w", err)
	}
	var transactionReadOnly string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&transactionReadOnly); err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("verify J3b PostgreSQL backfill transaction mode: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(transactionReadOnly), "on") {
		return PostgresBackfillReport{}, errors.New("J3b PostgreSQL backfill transaction is read-only")
	}
	var transactionIsolation string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_isolation").Scan(&transactionIsolation); err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("verify J3b PostgreSQL backfill isolation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(transactionIsolation), "serializable") {
		return PostgresBackfillReport{}, fmt.Errorf("J3b PostgreSQL backfill requires serializable isolation, got %q", transactionIsolation)
	}

	targetSchema, err := inspectTx(ctx, tx)
	if err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("verify J3b PostgreSQL backfill target schema: %w", err)
	}
	if !targetSchema.Ready() {
		return PostgresBackfillReport{}, fmt.Errorf("J3b PostgreSQL backfill target schema is incomplete: %+v", targetSchema)
	}
	report := PostgresBackfillReport{
		TransactionIsolation: strings.ToLower(strings.TrimSpace(transactionIsolation)),
		MaxRowsPerTable:      normalized.maxRows,
		MaxBytesPerTable:     normalized.maxBytes,
		Tables:               make(map[string]PostgresBackfillTable, len(postgresLegacyJ3bFactTables)),
	}
	for _, item := range postgresLegacyJ3bFactTables {
		result, err := backfillPostgresTable(ctx, tx, item, normalized)
		if err != nil {
			return PostgresBackfillReport{}, err
		}
		report.Tables[item.name] = result
	}
	if err := tx.Commit(); err != nil {
		return PostgresBackfillReport{}, fmt.Errorf("commit J3b PostgreSQL backfill: %w", err)
	}
	return report, nil
}

type normalizedPostgresBackfillOptions struct {
	maxRows  int64
	maxBytes int64
}

func normalizePostgresBackfillOptions(options PostgresBackfillOptions) (normalizedPostgresBackfillOptions, error) {
	maxRows := options.MaxRowsPerTable
	if maxRows == 0 {
		maxRows = DefaultPostgresBackfillMaxRows
	}
	if maxRows < 1 || maxRows > MaximumPostgresBackfillMaxRows {
		return normalizedPostgresBackfillOptions{}, fmt.Errorf("J3b PostgreSQL backfill max rows per table must be between 1 and %d", MaximumPostgresBackfillMaxRows)
	}
	maxBytes := options.MaxBytesPerTable
	if maxBytes == 0 {
		maxBytes = DefaultPostgresBackfillMaxBytes
	}
	if maxBytes < 1 || maxBytes > MaximumPostgresBackfillMaxBytes {
		return normalizedPostgresBackfillOptions{}, fmt.Errorf("J3b PostgreSQL backfill max bytes per table must be between 1 and %d", MaximumPostgresBackfillMaxBytes)
	}
	return normalizedPostgresBackfillOptions{maxRows: maxRows, maxBytes: maxBytes}, nil
}

func backfillPostgresTable(ctx context.Context, tx *sql.Tx, item postgresBackfillTable, options normalizedPostgresBackfillOptions) (PostgresBackfillTable, error) {
	result := PostgresBackfillTable{SourceSchema: item.sourceSchema, TargetSchema: SchemaName}
	sourceExists, err := postgresTableExists(ctx, tx, item.sourceSchema, item.name)
	if err != nil {
		return result, err
	}
	if !sourceExists {
		return result, fmt.Errorf("J3b PostgreSQL legacy source table %s.%s is missing", item.sourceSchema, item.name)
	}
	targetExists, err := postgresTableExists(ctx, tx, SchemaName, item.name)
	if err != nil {
		return result, err
	}
	if !targetExists {
		return result, fmt.Errorf("J3b PostgreSQL target table %s.%s is missing", SchemaName, item.name)
	}
	sourceColumns, err := postgresBackfillColumns(ctx, tx, item.sourceSchema, item.name)
	if err != nil {
		return result, err
	}
	targetColumns, err := postgresBackfillColumns(ctx, tx, SchemaName, item.name)
	if err != nil {
		return result, err
	}
	projection, err := postgresBackfillProjection(sourceColumns, targetColumns, item.name)
	if err != nil {
		return result, err
	}
	sourceKeys, err := postgresPrimaryKeys(ctx, tx, item.sourceSchema, item.name)
	if err != nil {
		return result, err
	}
	targetKeys, err := postgresPrimaryKeys(ctx, tx, SchemaName, item.name)
	if err != nil {
		return result, err
	}
	if len(sourceKeys) == 0 || len(targetKeys) == 0 || !sameStringSlice(sourceKeys, targetKeys) || !containsAll(projection, sourceKeys) {
		return result, fmt.Errorf("J3b PostgreSQL backfill table %s primary key projection mismatch", item.name)
	}
	result.Projection = append([]string(nil), projection...)
	result.PrimaryKeys = append([]string(nil), targetKeys...)

	query := "SELECT " + joinPostgresQuoted(projection) + " FROM " + postgresQualifiedIdent(item.sourceSchema, item.name) + " ORDER BY " + joinQuotedInOrder(targetKeys) + " LIMIT $1"
	rows, err := tx.QueryContext(ctx, query, options.maxRows+1)
	if err != nil {
		return result, fmt.Errorf("read J3b PostgreSQL legacy table %s.%s: %w", item.sourceSchema, item.name, err)
	}
	// Keep source rows bounded in memory, then close the result before issuing
	// target lookups. pgx exposes one active result set per connection; trying
	// to query the target while rows is still open is not portable.
	sourceRows := make([][]any, 0)
	var sourceBytes int64
	for rows.Next() {
		if int64(len(sourceRows)) >= options.maxRows {
			rows.Close()
			return result, fmt.Errorf("J3b PostgreSQL source table %s.%s exceeds max rows per table (%d)", item.sourceSchema, item.name, options.maxRows)
		}
		values := make([]any, len(projection))
		pointers := make([]any, len(values))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			rows.Close()
			return result, fmt.Errorf("scan J3b PostgreSQL legacy table %s.%s: %w", item.sourceSchema, item.name, err)
		}
		sourceBytes += postgresBackfillRowBytes(values)
		if sourceBytes > options.maxBytes {
			rows.Close()
			return result, fmt.Errorf("J3b PostgreSQL source table %s.%s exceeds max bytes per table (%d)", item.sourceSchema, item.name, options.maxBytes)
		}
		sourceRows = append(sourceRows, values)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("iterate J3b PostgreSQL legacy table %s.%s: %w", item.sourceSchema, item.name, err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close J3b PostgreSQL legacy table %s.%s: %w", item.sourceSchema, item.name, err)
	}
	insertSQL := "INSERT INTO " + postgresQualifiedIdent(SchemaName, item.name) + " (" + joinPostgresQuoted(projection) + ") VALUES (" + postgresPlaceholders(len(projection)) + ")"
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return result, fmt.Errorf("prepare J3b PostgreSQL backfill table %s: %w", item.name, err)
	}
	defer insertStmt.Close()
	projectionIndexes := make(map[string]int, len(projection))
	for index, column := range projection {
		projectionIndexes[column] = index
	}
	var targetRowSQL = "SELECT " + joinPostgresQuoted(projection) + " FROM " + postgresQualifiedIdent(SchemaName, item.name) + " WHERE " + postgresPrimaryKeyPredicate(targetKeys)
	for _, values := range sourceRows {
		result.SourceRows++
		result.SourceBytes = sourceBytes
		keys := make([]any, len(targetKeys))
		for index, key := range targetKeys {
			keys[index] = values[projectionIndexes[key]]
		}
		existing := make([]any, len(projection))
		existingPointers := make([]any, len(existing))
		for index := range existing {
			existingPointers[index] = &existing[index]
		}
		err := tx.QueryRowContext(ctx, targetRowSQL, keys...).Scan(existingPointers...)
		if err == nil {
			for index := range values {
				if !postgresBackfillValuesEqual(existing[index], values[index]) {
					return result, fmt.Errorf("J3b PostgreSQL backfill conflict in %s primary key row", item.name)
				}
			}
			result.SkippedRows++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return result, fmt.Errorf("check existing J3b PostgreSQL backfill row %s: %w", item.name, err)
		}
		if _, err := insertStmt.ExecContext(ctx, values...); err != nil {
			return result, fmt.Errorf("insert J3b PostgreSQL backfill table %s: %w", item.name, err)
		}
		result.InsertedRows++
	}
	if result.SourceRows == 0 {
		return result, fmt.Errorf("J3b PostgreSQL legacy source table %s.%s is empty; refusing fail-open backfill", item.sourceSchema, item.name)
	}
	return result, nil
}

func postgresBackfillColumns(ctx context.Context, tx *sql.Tx, schema, table string) (map[string]postgresBackfillColumn, error) {
	rows, err := tx.QueryContext(ctx, `SELECT column_name,data_type,udt_name,is_nullable,column_default,is_identity FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("read J3b PostgreSQL columns %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	columns := make(map[string]postgresBackfillColumn)
	for rows.Next() {
		var name, dataType, udtName, nullable, identity string
		var defaultValue sql.NullString
		if err := rows.Scan(&name, &dataType, &udtName, &nullable, &defaultValue, &identity); err != nil {
			return nil, fmt.Errorf("scan J3b PostgreSQL columns %s.%s: %w", schema, table, err)
		}
		columns[name] = postgresBackfillColumn{Name: name, DataType: dataType, UdtName: udtName, Nullable: nullable == "YES", HasDefault: defaultValue.Valid || identity != "NO"}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b PostgreSQL columns %s.%s: %w", schema, table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("J3b PostgreSQL table %s.%s has no columns", schema, table)
	}
	return columns, nil
}

// postgresBackfillProjection validates a complete, stable public projection.
// A source-only column is never silently dropped: its semantics are unknown
// and therefore the migration must stop. Target-only nullable/defaulted
// columns are safe to omit; required target-only columns are not.
func postgresBackfillProjection(source, target map[string]postgresBackfillColumn, table string) ([]string, error) {
	projection := make([]string, 0, len(source))
	for name, sourceColumn := range source {
		targetColumn, ok := target[name]
		if !ok {
			return nil, fmt.Errorf("J3b PostgreSQL backfill table %s has unmapped legacy source column %s", table, name)
		}
		if sourceColumn.DataType != targetColumn.DataType || sourceColumn.UdtName != targetColumn.UdtName || sourceColumn.Nullable != targetColumn.Nullable {
			return nil, fmt.Errorf("J3b PostgreSQL backfill table %s column %s type/nullability mismatch", table, name)
		}
		projection = append(projection, name)
	}
	for name, targetColumn := range target {
		if _, ok := source[name]; ok {
			continue
		}
		if !targetColumn.Nullable && !targetColumn.HasDefault {
			return nil, fmt.Errorf("J3b PostgreSQL backfill table %s target column %s is required but absent from legacy source", table, name)
		}
	}
	if len(projection) == 0 {
		return nil, fmt.Errorf("J3b PostgreSQL backfill table %s has no public projection", table)
	}
	sort.Strings(projection)
	return projection, nil
}

// postgresReadbackProjection enforces the same source-column completeness as
// the writer. Readback must never report Ready from a lossy common-column
// digest when a legacy fact has no target mapping.
func postgresReadbackProjection(source, target []string, table string) ([]string, error) {
	targetSet := make(map[string]struct{}, len(target))
	for _, name := range target {
		targetSet[name] = struct{}{}
	}
	for _, name := range source {
		if _, ok := targetSet[name]; !ok {
			return nil, fmt.Errorf("J3b PostgreSQL readback table %s has unmapped legacy source column %s", table, name)
		}
	}
	projection := intersectColumns(source, target)
	if len(projection) == 0 {
		return nil, fmt.Errorf("J3b PostgreSQL readback table %s has no public projection", table)
	}
	return projection, nil
}

func joinPostgresQuoted(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteIdent(value)
	}
	return strings.Join(quoted, ",")
}

func postgresPlaceholders(count int) string {
	placeholders := make([]string, count)
	for index := range placeholders {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
	}
	return strings.Join(placeholders, ",")
}

func postgresPrimaryKeyPredicate(keys []string) string {
	predicates := make([]string, len(keys))
	for index, key := range keys {
		predicates[index] = quoteIdent(key) + "=$" + fmt.Sprintf("%d", index+1)
	}
	return strings.Join(predicates, " AND ")
}

func postgresBackfillRowBytes(values []any) int64 {
	var total int64
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
		case []byte:
			total += int64(len(typed))
		case string:
			total += int64(len(typed))
		case time.Time:
			total += int64(len(typed.UTC().Format(time.RFC3339Nano)))
		default:
			total += int64(len(fmt.Sprintf("%v", typed)))
		}
	}
	return total
}

func postgresBackfillValuesEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftTime, leftOK := left.(time.Time)
	rightTime, rightOK := right.(time.Time)
	if leftOK || rightOK {
		return leftOK && rightOK && leftTime.Equal(rightTime)
	}
	return normalizeValue(left) == normalizeValue(right)
}
