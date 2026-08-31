package j3bmodelcheck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PostgresReadbackOptions bounds one maintenance readback invocation. A
// readback that cannot inspect every row within the requested bound is not
// evidence for a cutover: it returns Ready=false instead of silently sampling.
type PostgresReadbackOptions struct {
	MaxRowsPerTable int64
}

const (
	DefaultPostgresReadbackMaxRows int64 = 100000
	MaximumPostgresReadbackMaxRows int64 = 1000000
)

// PostgresBackfillVerificationReport records only observed rows. When a table
// exceeds MaxRowsPerTable, its corresponding row count is the bounded probe
// size (MaxRowsPerTable + 1), not a claimed total row count.
//
// This is deliberately a readback contract, not a PostgreSQL write path. The
// offline writer and its stop/backup approval remain a separate cutover gate.
type PostgresBackfillVerificationReport struct {
	Ready                  bool              `json:"ready"`
	TransactionReadOnly    bool              `json:"transactionReadOnly"`
	TargetSchema           Report            `json:"targetSchema"`
	Tables                 map[string]string `json:"tables"`
	SourceRows             map[string]int64  `json:"sourceRows"`
	TargetRows             map[string]int64  `json:"targetRows"`
	SourceDigest           map[string]string `json:"sourceDigest"`
	TargetDigest           map[string]string `json:"targetDigest"`
	SourceExceededRowLimit map[string]bool   `json:"sourceExceededRowLimit"`
	TargetExceededRowLimit map[string]bool   `json:"targetExceededRowLimit"`
	MaxRowsPerTable        int64             `json:"maxRowsPerTable"`
}

type postgresBackfillTable struct {
	name         string
	sourceSchema string
}

// postgresLegacyJ3bFactTables is intentionally limited to facts which used to
// live in Node dataset/stats schemas. Business policy/schedule/enforcement
// handoff is not represented as a J3b table and therefore must have its own
// Business-owner audit; treating it as migrated here would be misleading.
var postgresLegacyJ3bFactTables = []postgresBackfillTable{
	{name: "model_check_runs", sourceSchema: "juhe_dataset"},
	{name: "model_check_items", sourceSchema: "juhe_dataset"},
	{name: "model_check_observations", sourceSchema: "juhe_dataset"},
	{name: "account_quality_health_hourly", sourceSchema: "juhe_stats"},
	{name: "model_token_intercept_baseline_versions", sourceSchema: "juhe_stats"},
	{name: "model_account_trust_results", sourceSchema: "juhe_stats"},
	{name: "model_trust_latest_dirty_accounts", sourceSchema: "juhe_stats"},
	{name: "model_trust_observation_receipts", sourceSchema: "juhe_stats"},
}

// VerifyPostgresBackfill performs an explicitly bounded, repeatable-read,
// read-only comparison of legacy PostgreSQL J3b facts and juhe_j3b. It checks
// the target maintenance schema first, then for each mandatory legacy fact
// table checks source existence, public-column projection, primary-key shape,
// bounded row count and deterministic digest. Any gap produces Ready=false.
//
// The supplied database connection must already be a maintenance-scoped
// PostgreSQL connection. This function never executes DDL or DML.
func VerifyPostgresBackfill(ctx context.Context, db *sql.DB, options PostgresReadbackOptions) (PostgresBackfillVerificationReport, error) {
	if db == nil {
		return PostgresBackfillVerificationReport{}, errors.New("J3b PostgreSQL readback database is not initialized")
	}
	maxRows, err := normalizePostgresReadbackMaxRows(options.MaxRowsPerTable)
	if err != nil {
		return PostgresBackfillVerificationReport{}, err
	}
	report := PostgresBackfillVerificationReport{
		Tables:                 make(map[string]string, len(postgresLegacyJ3bFactTables)+1),
		SourceRows:             make(map[string]int64, len(postgresLegacyJ3bFactTables)+1),
		TargetRows:             make(map[string]int64, len(postgresLegacyJ3bFactTables)+1),
		SourceDigest:           make(map[string]string, len(postgresLegacyJ3bFactTables)+1),
		TargetDigest:           make(map[string]string, len(postgresLegacyJ3bFactTables)+1),
		SourceExceededRowLimit: make(map[string]bool, len(postgresLegacyJ3bFactTables)+1),
		TargetExceededRowLimit: make(map[string]bool, len(postgresLegacyJ3bFactTables)+1),
		MaxRowsPerTable:        maxRows,
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return PostgresBackfillVerificationReport{}, fmt.Errorf("begin J3b PostgreSQL readback: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"); err != nil {
		return PostgresBackfillVerificationReport{}, fmt.Errorf("configure J3b PostgreSQL readback transaction: %w", err)
	}
	var transactionReadOnly string
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&transactionReadOnly); err != nil {
		return PostgresBackfillVerificationReport{}, fmt.Errorf("verify J3b PostgreSQL readback transaction mode: %w", err)
	}
	report.TransactionReadOnly = strings.EqualFold(strings.TrimSpace(transactionReadOnly), "on")
	if !report.TransactionReadOnly {
		report.Tables["__transaction__"] = "transaction is writable"
		if err := tx.Commit(); err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		return report, nil
	}

	report.TargetSchema, err = inspectTx(ctx, tx)
	if err != nil {
		return PostgresBackfillVerificationReport{}, fmt.Errorf("verify J3b PostgreSQL readback target schema: %w", err)
	}
	// A readback role intentionally needs only SELECT across the frozen Node
	// source schemas and juhe_j3b. It need not own juhe_j3b (ownership is a
	// bootstrap/apply concern), so retain OwnerMismatch as evidence but do not
	// mistake it for structural unreadiness.
	if !postgresReadbackSchemaReady(report.TargetSchema) {
		report.Tables["__target_schema__"] = "target schema incomplete"
		if err := tx.Commit(); err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		return report, nil
	}

	ready := true
	for _, item := range postgresLegacyJ3bFactTables {
		sourceExists, err := postgresTableExists(ctx, tx, item.sourceSchema, item.name)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		if !sourceExists {
			report.Tables[item.name] = "mandatory source table absent"
			ready = false
			continue
		}
		sourceColumns, err := postgresBackfillColumns(ctx, tx, item.sourceSchema, item.name)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		targetColumns, err := postgresBackfillColumns(ctx, tx, SchemaName, item.name)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		columns, projectionErr := postgresBackfillProjection(sourceColumns, targetColumns, item.name)
		if projectionErr != nil {
			report.Tables[item.name] = projectionErr.Error()
			ready = false
			continue
		}
		sourceKeys, err := postgresPrimaryKeys(ctx, tx, item.sourceSchema, item.name)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		targetKeys, err := postgresPrimaryKeys(ctx, tx, SchemaName, item.name)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		if len(sourceKeys) == 0 || len(targetKeys) == 0 || !sameStringSlice(sourceKeys, targetKeys) || !containsAll(columns, sourceKeys) {
			report.Tables[item.name] = "primary key projection mismatch"
			ready = false
			continue
		}
		sourceEvidence, err := postgresTableEvidence(ctx, tx, item.sourceSchema, item.name, columns, sourceKeys, maxRows)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		targetEvidence, err := postgresTableEvidence(ctx, tx, SchemaName, item.name, columns, targetKeys, maxRows)
		if err != nil {
			return PostgresBackfillVerificationReport{}, err
		}
		report.SourceRows[item.name] = sourceEvidence.rows
		report.TargetRows[item.name] = targetEvidence.rows
		report.SourceDigest[item.name] = sourceEvidence.digest
		report.TargetDigest[item.name] = targetEvidence.digest
		report.SourceExceededRowLimit[item.name] = sourceEvidence.exceeded
		report.TargetExceededRowLimit[item.name] = targetEvidence.exceeded
		if sourceEvidence.exceeded {
			report.Tables[item.name] = "source exceeds row limit"
			ready = false
			continue
		}
		if targetEvidence.exceeded {
			report.Tables[item.name] = "target exceeds row limit"
			ready = false
			continue
		}
		if sourceEvidence.rows != targetEvidence.rows || sourceEvidence.digest != targetEvidence.digest {
			report.Tables[item.name] = "drift"
			ready = false
			continue
		}
		report.Tables[item.name] = "match"
	}
	trustState, err := verifyPostgresTrustAggregationState(ctx, tx, maxRows)
	if err != nil {
		return PostgresBackfillVerificationReport{}, err
	}
	report.SourceRows[trustAggregationStateTable] = trustState.source.rows
	report.TargetRows[trustAggregationStateTable] = trustState.target.rows
	report.SourceDigest[trustAggregationStateTable] = trustState.source.digest
	report.TargetDigest[trustAggregationStateTable] = trustState.target.digest
	report.SourceExceededRowLimit[trustAggregationStateTable] = trustState.source.exceeded
	report.TargetExceededRowLimit[trustAggregationStateTable] = trustState.target.exceeded
	switch {
	case trustState.source.exceeded:
		report.Tables[trustAggregationStateTable] = "source exceeds row limit"
		ready = false
	case trustState.target.exceeded:
		report.Tables[trustAggregationStateTable] = "target exceeds row limit"
		ready = false
	case trustState.source.rows != trustState.target.rows || trustState.source.digest != trustState.target.digest:
		report.Tables[trustAggregationStateTable] = "drift"
		ready = false
	default:
		report.Tables[trustAggregationStateTable] = "match"
	}
	report.Ready = ready
	if err := tx.Commit(); err != nil {
		return PostgresBackfillVerificationReport{}, fmt.Errorf("commit J3b PostgreSQL readback: %w", err)
	}
	return report, nil
}

type postgresTrustAggregationStateVerification struct {
	source postgresEvidence
	target postgresEvidence
}

func verifyPostgresTrustAggregationState(ctx context.Context, tx *sql.Tx, maxRows int64) (postgresTrustAggregationStateVerification, error) {
	if exists, err := postgresTableExists(ctx, tx, "juhe_stats", "stats_job_state"); err != nil {
		return postgresTrustAggregationStateVerification{}, err
	} else if !exists {
		return postgresTrustAggregationStateVerification{}, errors.New("J3b PostgreSQL legacy source table juhe_stats.stats_job_state is missing")
	}
	if err := validatePostgresTrustAggregationStateColumns(ctx, tx); err != nil {
		return postgresTrustAggregationStateVerification{}, err
	}
	source, err := postgresTrustAggregationStateEvidence(ctx, tx, true, maxRows)
	if err != nil {
		return postgresTrustAggregationStateVerification{}, err
	}
	target, err := postgresTrustAggregationStateEvidence(ctx, tx, false, maxRows)
	if err != nil {
		return postgresTrustAggregationStateVerification{}, err
	}
	return postgresTrustAggregationStateVerification{source: source, target: target}, nil
}

// postgresTrustAggregationStateEvidence hashes the same seven-column target
// projection on both sides. Source scope columns are asserted in the WHERE
// clause and represented by the canonical target scope key in the digest.
func postgresTrustAggregationStateEvidence(ctx context.Context, tx *sql.Tx, source bool, maxRows int64) (postgresEvidence, error) {
	var (
		query string
		args  []any
	)
	if source {
		query = `SELECT to_jsonb($1::text)::text,to_jsonb(cursor_created_at)::text,to_jsonb(cursor_id)::text,to_jsonb(last_success_at)::text,to_jsonb(last_error_message)::text,to_jsonb(lag_seconds)::text,to_jsonb(updated_at)::text FROM juhe_stats.stats_job_state WHERE scope_type=$2 AND scope_id=$3 AND job_name=$4 LIMIT $5`
		args = []any{trustAggregationStateScopeKey, trustAggregationStateScopeType, "", trustAggregationStateJobName, maxRows + 1}
	} else {
		query = `SELECT to_jsonb(scope_key)::text,to_jsonb(cursor_created_at)::text,to_jsonb(cursor_id)::text,to_jsonb(last_success_at)::text,to_jsonb(last_error_message)::text,to_jsonb(lag_seconds)::text,to_jsonb(updated_at)::text FROM juhe_j3b.model_trust_aggregation_state WHERE scope_key=$1 LIMIT $2`
		args = []any{trustAggregationStateScopeKey, maxRows + 1}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return postgresEvidence{}, fmt.Errorf("read J3b PostgreSQL trust aggregation state: %w", err)
	}
	defer rows.Close()
	digest := sha256.New()
	result := postgresEvidence{}
	for rows.Next() {
		result.rows++
		if result.rows > maxRows {
			result.exceeded = true
			break
		}
		values := make([]any, 7)
		pointers := make([]any, len(values))
		for index := range pointers {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return postgresEvidence{}, fmt.Errorf("scan J3b PostgreSQL trust aggregation state: %w", err)
		}
		writeDigestRow(digest, values)
	}
	if err := rows.Err(); err != nil {
		return postgresEvidence{}, fmt.Errorf("iterate J3b PostgreSQL trust aggregation state: %w", err)
	}
	if !result.exceeded {
		result.digest = hex.EncodeToString(digest.Sum(nil))
	}
	return result, nil
}

func postgresReadbackSchemaReady(report Report) bool {
	return !report.MissingSchema && len(report.MissingTables) == 0 && len(report.InvalidTables) == 0 && len(report.MissingIndexes) == 0 && len(report.InvalidIndexes) == 0
}

func normalizePostgresReadbackMaxRows(value int64) (int64, error) {
	if value == 0 {
		return DefaultPostgresReadbackMaxRows, nil
	}
	if value < 1 || value > MaximumPostgresReadbackMaxRows {
		return 0, fmt.Errorf("J3b PostgreSQL readback max rows per table must be between 1 and %d", MaximumPostgresReadbackMaxRows)
	}
	return value, nil
}

func postgresTableExists(ctx context.Context, tx *sql.Tx, schema, table string) (bool, error) {
	var found bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2 AND table_type='BASE TABLE')`, schema, table).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check J3b PostgreSQL table %s.%s: %w", schema, table, err)
	}
	return found, nil
}

func postgresColumns(ctx context.Context, tx *sql.Tx, schema, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("read J3b PostgreSQL columns %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("J3b PostgreSQL table %s.%s has no columns", schema, table)
	}
	return columns, nil
}

func postgresPrimaryKeys(ctx context.Context, tx *sql.Tx, schema, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT attribute.attname FROM pg_index AS idx JOIN pg_class AS relation ON relation.oid=idx.indrelid JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace JOIN unnest(idx.indkey) WITH ORDINALITY AS key(attnum,position) ON true JOIN pg_attribute AS attribute ON attribute.attrelid=relation.oid AND attribute.attnum=key.attnum WHERE namespace.nspname=$1 AND relation.relname=$2 AND idx.indisprimary ORDER BY key.position`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("read J3b PostgreSQL primary key %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

type postgresEvidence struct {
	rows     int64
	digest   string
	exceeded bool
}

func postgresTableEvidence(ctx context.Context, tx *sql.Tx, schema, table string, columns, primaryKeys []string, maxRows int64) (postgresEvidence, error) {
	projection := append([]string(nil), columns...)
	sort.Strings(projection)
	// PostgreSQL may expose equivalent legacy and target values through
	// different driver types (for example timestamptz versus text). Hash the
	// database's canonical JSON scalar rendering rather than Go's driver
	// rendering so an equivalent value is neither hidden nor falsely drifted.
	query := "SELECT " + postgresJSONProjection(projection) + " FROM " + postgresQualifiedIdent(schema, table) + " ORDER BY " + joinQuotedInOrder(primaryKeys) + " LIMIT $1"
	rows, err := tx.QueryContext(ctx, query, maxRows+1)
	if err != nil {
		return postgresEvidence{}, fmt.Errorf("read J3b PostgreSQL table %s.%s: %w", schema, table, err)
	}
	defer rows.Close()
	digest := sha256.New()
	result := postgresEvidence{}
	for rows.Next() {
		result.rows++
		if result.rows > maxRows {
			result.exceeded = true
			break
		}
		values := make([]any, len(projection))
		pointers := make([]any, len(values))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return postgresEvidence{}, fmt.Errorf("scan J3b PostgreSQL table %s.%s: %w", schema, table, err)
		}
		writeDigestRow(digest, values)
	}
	if err := rows.Err(); err != nil {
		return postgresEvidence{}, fmt.Errorf("iterate J3b PostgreSQL table %s.%s: %w", schema, table, err)
	}
	if result.exceeded {
		return result, nil
	}
	result.digest = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func postgresQualifiedIdent(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func postgresJSONProjection(columns []string) string {
	projection := append([]string(nil), columns...)
	sort.Strings(projection)
	values := make([]string, len(projection))
	for index, column := range projection {
		values[index] = "to_jsonb(" + quoteIdent(column) + ")::text"
	}
	return strings.Join(values, ",")
}

func containsAll(columns, required []string) bool {
	found := make(map[string]bool, len(columns))
	for _, column := range columns {
		found[column] = true
	}
	for _, column := range required {
		if !found[column] {
			return false
		}
	}
	return true
}
