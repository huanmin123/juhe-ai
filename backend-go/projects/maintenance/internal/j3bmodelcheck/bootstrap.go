package j3bmodelcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	SchemaName    = "juhe_jobs"
	BootstrapEnv  = "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"
	bootstrapLock = int64(732_946_110_271_044_016)
)

type Report struct {
	Database       string   `json:"database"`
	Schema         string   `json:"schema"`
	CurrentRole    string   `json:"currentRole"`
	SchemaOwner    string   `json:"schemaOwner"`
	MissingSchema  bool     `json:"missingSchema"`
	OwnerMismatch  bool     `json:"ownerMismatch"`
	MissingTables  []string `json:"missingTables"`
	InvalidTables  []string `json:"invalidTables"`
	MissingIndexes []string `json:"missingIndexes"`
	InvalidIndexes []string `json:"invalidIndexes"`
	Applied        bool     `json:"applied"`
}

func (r Report) Ready() bool {
	return !r.MissingSchema && !r.OwnerMismatch && len(r.MissingTables) == 0 && len(r.InvalidTables) == 0 && len(r.MissingIndexes) == 0 && len(r.InvalidIndexes) == 0
}

func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("J3b bootstrap 必须提供包含主机、数据库和显式角色的 postgres URL")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 J3b bootstrap PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func Run(ctx context.Context, db *sql.DB, apply bool) (Report, error) {
	if db == nil {
		return Report{}, errors.New("J3b bootstrap 数据库未初始化")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !apply})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"); err != nil {
		return Report{}, err
	}
	if apply {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", bootstrapLock); err != nil {
			return Report{}, err
		}
	}
	report, err := inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !apply || report.Ready() {
		if err := tx.Commit(); err != nil {
			return Report{}, err
		}
		return report, nil
	}
	if report.MissingSchema {
		return Report{}, errors.New("J3b bootstrap 拒绝创建 juhe_jobs schema；必须由受控数据库流程预置")
	}
	if report.SchemaOwner != report.CurrentRole {
		return Report{}, fmt.Errorf("J3b bootstrap 拒绝跨角色修改 juhe_jobs schema: owner=%s current=%s", report.SchemaOwner, report.CurrentRole)
	}
	if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
		return Report{}, fmt.Errorf("执行 J3b PostgreSQL jobs schema bootstrap 失败: %w", err)
	}
	report, err = inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !report.Ready() {
		return Report{}, errors.New("J3b PostgreSQL jobs schema bootstrap 后契约仍不完整")
	}
	report.Applied = true
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func inspectTx(ctx context.Context, tx *sql.Tx) (Report, error) {
	report := Report{Schema: SchemaName}
	if err := tx.QueryRowContext(ctx, "SELECT current_database(), current_user").Scan(&report.Database, &report.CurrentRole); err != nil {
		return Report{}, err
	}
	var owner sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1", SchemaName).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		report.MissingSchema = true
		return report, nil
	}
	if err != nil {
		return Report{}, err
	}
	report.SchemaOwner, report.OwnerMismatch = owner.String, owner.String != report.CurrentRole
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name = ANY($2)", SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return Report{}, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return Report{}, err
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, err
	}
	rows.Close()
	for _, name := range contracts.J3BModelCheckTables {
		if !seen[name] {
			report.MissingTables = append(report.MissingTables, name)
		}
	}
	if len(report.MissingTables) == 0 {
		if err := inspectColumns(ctx, tx, &report); err != nil {
			return Report{}, err
		}
		if err := inspectConstraints(ctx, tx, &report); err != nil {
			return Report{}, err
		}
	}
	indexRows, err := tx.QueryContext(ctx, "SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=$1 AND indexname = ANY($2)", SchemaName, requiredIndexNames())
	if err != nil {
		return Report{}, err
	}
	indexes := map[string]string{}
	for indexRows.Next() {
		var name, definition string
		if err := indexRows.Scan(&name, &definition); err != nil {
			indexRows.Close()
			return Report{}, err
		}
		indexes[name] = strings.ToLower(strings.Join(strings.Fields(definition), " "))
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return Report{}, err
	}
	indexRows.Close()
	for name, expected := range contracts.J3BModelCheckIndexes {
		actual, ok := indexes[name]
		if !ok {
			report.MissingIndexes = append(report.MissingIndexes, name)
			continue
		}
		if !strings.Contains(actual, expected) {
			report.InvalidIndexes = append(report.InvalidIndexes, name)
		}
	}
	sort.Strings(report.MissingTables)
	sort.Strings(report.InvalidTables)
	sort.Strings(report.MissingIndexes)
	sort.Strings(report.InvalidIndexes)
	return report, nil
}

func inspectColumns(ctx context.Context, tx *sql.Tx, report *Report) error {
	rows, err := tx.QueryContext(ctx, `SELECT table_name,column_name,data_type,udt_name,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name = ANY($2)`, SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]map[string]contracts.PostgresColumnSpec{}
	for rows.Next() {
		var table, column, dataType, udtName, nullable string
		if err := rows.Scan(&table, &column, &dataType, &udtName, &nullable); err != nil {
			return err
		}
		if seen[table] == nil {
			seen[table] = map[string]contracts.PostgresColumnSpec{}
		}
		seen[table][column] = contracts.PostgresColumnSpec{DataType: dataType, UdtName: udtName, Nullable: nullable == "YES"}
	}
	for table, columns := range contracts.J3BModelCheckColumns {
		for column, expected := range columns {
			actual, ok := seen[table][column]
			if !ok || actual.DataType != expected.DataType || actual.UdtName != expected.UdtName || actual.Nullable != expected.Nullable {
				report.InvalidTables = append(report.InvalidTables, table+"."+column)
			}
		}
	}
	return rows.Err()
}

func inspectConstraints(ctx context.Context, tx *sql.Tx, report *Report) error {
	rows, err := tx.QueryContext(ctx, `SELECT relation.relname,pg_get_constraintdef(c.oid) FROM pg_constraint AS c JOIN pg_class AS relation ON relation.oid=c.conrelid JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace WHERE namespace.nspname=$1 AND relation.relname = ANY($2) AND c.contype IN ('p','u')`, SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var table, definition string
		if err := rows.Scan(&table, &definition); err != nil {
			return err
		}
		seen[table] = append(seen[table], strings.ToLower(strings.Join(strings.Fields(definition), " ")))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for table, expectedList := range contracts.J3BModelCheckConstraints {
		for _, expected := range expectedList {
			found := false
			for _, actual := range seen[table] {
				if strings.Contains(actual, expected) {
					found = true
					break
				}
			}
			if !found {
				report.InvalidTables = append(report.InvalidTables, table+":constraint="+expected)
			}
		}
	}
	return nil
}

func requiredIndexNames() []string {
	names := make([]string, 0, len(contracts.J3BModelCheckIndexes))
	for name := range contracts.J3BModelCheckIndexes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version BIGINT NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, UNIQUE(identity_key,input_version), UNIQUE(identity_key,input_digest));
CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE);
CREATE INDEX IF NOT EXISTS idx_model_check_outcomes_cursor ON juhe_jobs.model_check_outcomes(stored_at,outcome_id);
CREATE INDEX IF NOT EXISTS idx_model_check_inputs_target ON juhe_jobs.model_check_inputs(target_id,issued_at);
`
