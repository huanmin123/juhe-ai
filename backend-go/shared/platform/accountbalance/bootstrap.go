package accountbalance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// balancePGRequiredTables is the fixed J2 four-table contract in canonical
// order; it matches the tables created by balancePostgresSchema and verified
// by checkPostgresSchema.
var balancePGRequiredTables = []string{
	"account_balance_owner_leases",
	"account_balance_account_leases",
	"account_balance_snapshots",
	"account_balance_outcomes",
}

// balanceBootstrapSetSQL carries the same transaction guard rails as the j3a /
// j3b maintenance bootstrap family (j3aproxylatency postgresSetSQL,
// j3bmodelcheck): the one-shot bootstrap runs on an external maintenance
// connection, so statement, lock, and idle-in-transaction waits must stay
// bounded instead of hanging an ALTER TABLE on a contended table lock.
const balanceBootstrapSetSQL = "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"

// BootstrapReport intentionally contains only object identifiers and counts.
// It never contains a connection URL, password, or payload.
type BootstrapReport struct {
	SchemaVerified    bool     `json:"schemaVerified"`
	StatementsApplied int      `json:"statementsApplied"`
	Tables            []string `json:"tables"`
	MissingTables     []string `json:"missingTables"`
}

// Ready reports whether the externally provisioned J2 contract is complete.
func (r BootstrapReport) Ready() bool {
	return len(r.MissingTables) == 0
}

// BootstrapPostgres is the external one-shot provisioning entrypoint for the
// J2 account-balance four-table contract (maintenance
// --apply-account-balance-postgres). It applies the exact DDL EnsureSchema's
// PostgreSQL arm applies, under the same transaction-scoped advisory lock
// 918271447, so it is mutually exclusive with any concurrent runtime
// EnsureSchema. Every statement is IF NOT EXISTS, so the command is idempotent
// and safely re-runnable. It never creates the juhe_jobs schema itself: a
// missing schema or a foreign owner fails closed exactly like the runtime
// checks (ensureBalancePGSchema), keeping schema provisioning with the
// controlled database flow. On success the four-table presence is re-verified
// inside the same transaction before commit.
func BootstrapPostgres(ctx context.Context, db *sql.DB) (BootstrapReport, error) {
	if db == nil {
		return BootstrapReport{}, errors.New("account-balance bootstrap 数据库未初始化")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapReport{}, fmt.Errorf("开始 account-balance bootstrap 事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, balanceBootstrapSetSQL); err != nil {
		return BootstrapReport{}, fmt.Errorf("配置 account-balance bootstrap 事务超时失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(918271447)"); err != nil {
		return BootstrapReport{}, fmt.Errorf("获取 account-balance bootstrap advisory lock 失败: %w", err)
	}
	if err := applyBalancePGSchemaDDL(ctx, tx); err != nil {
		return BootstrapReport{}, err
	}
	report := BootstrapReport{
		Tables:            append([]string(nil), balancePGRequiredTables...),
		StatementsApplied: balancePGSchemaStatementCount(),
	}
	seen, err := queryBalancePGTables(ctx, tx)
	if err != nil {
		return BootstrapReport{}, err
	}
	for _, name := range balancePGRequiredTables {
		if !seen[name] {
			report.MissingTables = append(report.MissingTables, name)
		}
	}
	report.SchemaVerified = len(report.MissingTables) == 0
	if err := tx.Commit(); err != nil {
		return BootstrapReport{}, fmt.Errorf("提交 account-balance bootstrap 事务失败: %w", err)
	}
	return report, nil
}

// queryBalancePGTables reads the information_schema view for the required J2
// tables inside the given transaction, mirroring checkPostgresSchema.
func queryBalancePGTables(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='juhe_jobs' AND table_name = ANY($1)`, balancePGRequiredTables)
	if err != nil {
		return nil, fmt.Errorf("读取 account-balance postgres tables 失败: %w", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	return seen, nil
}

// balancePGSchemaStatementCount counts the statements applyBalancePGSchemaDDL
// executes per invocation: every non-empty statement of balancePostgresSchema
// plus the outcomes.committed column migration.
func balancePGSchemaStatementCount() int {
	count := 0
	for _, statement := range strings.Split(balancePostgresSchema, ";") {
		if strings.TrimSpace(statement) != "" {
			count++
		}
	}
	return count + 1
}
