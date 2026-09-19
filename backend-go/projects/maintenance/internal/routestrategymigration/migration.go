// Package routestrategymigration contains the one-shot maintenance migration
// that rewrites legacy hybrid_smart route strategies after the hybrid smart
// routing mode removal. It is the routestrategies counterpart of the
// goruntimemetrics bootstrap: read-only by default, explicit --confirm to
// write, idempotent on rerun.
//
// Migration contract (fixed by the hybrid smart removal):
//   - every juhe_business.route_strategies row with mode='hybrid_smart'
//     becomes mode='failover', status='disabled' and config_json=NULL
//     (hybrid strategies only ever stored hybridRoutingConfig in config_json,
//     so NULL clears the hybrid remnant; failover defaults re-use the
//     route_strategy_groups priority ordering);
//   - route_strategy_groups bindings and api_keys bindings are never touched:
//     the disabled strategy keeps its API Keys unavailable until users
//     reconfigure, which is the accepted behavior;
//   - the write runs in a single transaction and is idempotent: a second run
//     finds no hybrid_smart rows and reports migratedRows=0.
//
// The CLI entry (cmd/juhe-ai-maintenance --migrate-hybrid-smart-strategies)
// is PostgreSQL-only via --dsn; SQLite support exists for the in-memory test
// suite and any future maintenance-only reuse of Run.
package routestrategymigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// SchemaName is the PostgreSQL business schema owning route_strategies.
const SchemaName = "juhe_business"

const (
	SourceMode   = "hybrid_smart"
	TargetMode   = "failover"
	TargetStatus = "disabled"
)

// Dialect selects schema qualification and placeholder syntax. The migration
// SQL is otherwise identical between PostgreSQL and SQLite.
type Dialect int

const (
	DialectPostgres Dialect = iota
	DialectSQLite
)

// String returns the report driver label.
func (d Dialect) String() string {
	if d == DialectSQLite {
		return "sqlite"
	}
	return "postgres"
}

// table qualifies a business table name for the dialect. SQLite business
// databases have no schema namespace; PostgreSQL rows live in juhe_business.
func (d Dialect) table(name string) string {
	if d == DialectSQLite {
		return name
	}
	return SchemaName + "." + name
}

// placeholder renders the nth (1-based) bind placeholder.
func (d Dialect) placeholder(n int) string {
	if d == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}

// Open validates the explicit maintenance-scoped PostgreSQL URL before
// opening a one-shot connection. The command is PostgreSQL-only: SQLite is
// rejected so an operator can never point the production migration at a file
// by accident. No application URL fallback is permitted.
func Open(rawURL string) (*sql.DB, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "postgres://") && !strings.HasPrefix(rawURL, "postgresql://") {
		return nil, errors.New("hybrid_smart strategy migration 必须提供 postgres:// 或 postgresql:// URL（本命令仅支持 PostgreSQL，不支持 SQLite）")
	}
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		return nil, fmt.Errorf("打开 hybrid_smart strategy migration PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// StrategyRow is one route strategy as reported before/after the migration.
type StrategyRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Mode         string `json:"mode,omitempty"`
	BoundAPIKeys int    `json:"boundApiKeys"`
}

// Report is the JSON result printed by the command. Strategies carries the
// pre-migration state of every row the run targets; Migrated carries the
// post-migration state of exactly those rows after --confirm.
type Report struct {
	Driver               string         `json:"driver"`
	Confirm              bool           `json:"confirm"`
	DryRun               bool           `json:"dryRun"`
	HybridSmartCount     int            `json:"hybridSmartCount"`
	Strategies           []StrategyRow  `json:"strategies"`
	UpdatedAt            string         `json:"updatedAt,omitempty"`
	MigratedRows         int64          `json:"migratedRows,omitempty"`
	Migrated             []StrategyRow  `json:"migrated,omitempty"`
	ModeDistribution     map[string]int `json:"modeDistribution,omitempty"`
	RemainingHybridSmart int            `json:"remainingHybridSmart"`
	Note                 string         `json:"note,omitempty"`
}

// Ready reports the post-condition: after a confirmed run no hybrid_smart row
// may remain. Dry-run reports are informational and always ready.
func (r Report) Ready() bool {
	if !r.Confirm {
		return true
	}
	return r.RemainingHybridSmart == 0
}

// sqlQuerier is the common query surface of *sql.DB and *sql.Tx.
type sqlQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Run performs the read-only dry-run inventory, or (with confirm) the single
// transaction migration followed by the self-check queries. now is the
// updated_at timestamp written to migrated rows (ISO milliseconds UTC, the
// gateway's stored format).
func Run(ctx context.Context, db *sql.DB, dialect Dialect, confirm bool, now time.Time) (Report, error) {
	if db == nil {
		return Report{}, errors.New("hybrid_smart strategy migration 数据库未初始化")
	}
	if !confirm {
		return runDryRun(ctx, db, dialect)
	}
	return runConfirm(ctx, db, dialect, now)
}

func runDryRun(ctx context.Context, db *sql.DB, dialect Dialect) (Report, error) {
	strategies, err := listHybridStrategies(ctx, db, dialect)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Driver:           dialect.String(),
		DryRun:           true,
		HybridSmartCount: len(strategies),
		Strategies:       strategies,
		// Dry-run writes nothing, so the remaining count equals the found count.
		RemainingHybridSmart: len(strategies),
	}
	if len(strategies) == 0 {
		report.Note = "no hybrid_smart rows"
	}
	return report, nil
}

func runConfirm(ctx context.Context, db *sql.DB, dialect Dialect, now time.Time) (Report, error) {
	updatedAt := isoMillis(now)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, fmt.Errorf("开启 hybrid_smart strategy migration 事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	strategies, err := listHybridStrategies(ctx, tx, dialect)
	if err != nil {
		return Report{}, err
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET mode = '%s', status = '%s', config_json = NULL, updated_at = %s WHERE mode = '%s'`,
		dialect.table("route_strategies"), TargetMode, TargetStatus, dialect.placeholder(1), SourceMode,
	), updatedAt)
	if err != nil {
		return Report{}, fmt.Errorf("迁移 hybrid_smart route strategies 失败: %w", err)
	}
	migratedRows, err := result.RowsAffected()
	if err != nil {
		return Report{}, fmt.Errorf("读取 hybrid_smart strategy migration affected rows 失败: %w", err)
	}
	distribution, err := modeDistribution(ctx, tx, dialect)
	if err != nil {
		return Report{}, err
	}
	var migrated []StrategyRow
	if len(strategies) > 0 {
		migrated, err = listStrategiesByIDs(ctx, tx, dialect, strategyIDs(strategies))
		if err != nil {
			return Report{}, err
		}
	}
	remaining, err := countHybridStrategies(ctx, tx, dialect)
	if err != nil {
		return Report{}, err
	}
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("提交 hybrid_smart strategy migration 事务失败: %w", err)
	}
	report := Report{
		Driver:               dialect.String(),
		Confirm:              true,
		HybridSmartCount:     len(strategies),
		Strategies:           strategies,
		UpdatedAt:            updatedAt,
		MigratedRows:         migratedRows,
		Migrated:             migrated,
		ModeDistribution:     distribution,
		RemainingHybridSmart: remaining,
	}
	if len(strategies) == 0 {
		report.Note = "no hybrid_smart rows"
	}
	return report, nil
}

// hybridStrategyListSQL returns the pre-migration row inventory (bound API
// Key count included). Bindings themselves are never modified.
func hybridStrategyListSQL(dialect Dialect) string {
	return fmt.Sprintf(`SELECT r.id, r.name, r.status, r.mode,
  (SELECT COUNT(*) FROM %s k WHERE k.route_strategy_id = r.id AND k.system_account_id = r.system_account_id) AS bound_api_keys
FROM %s r
WHERE r.mode = %s
ORDER BY r.id`, dialect.table("api_keys"), dialect.table("route_strategies"), dialect.placeholder(1))
}

func listHybridStrategies(ctx context.Context, q sqlQuerier, dialect Dialect) ([]StrategyRow, error) {
	return listStrategies(ctx, q, dialect, hybridStrategyListSQL(dialect), SourceMode)
}

// listStrategiesByIDs returns the post-migration state of exactly the rows
// captured before the UPDATE (same transaction), identified by primary key
// without a mode filter: the rows no longer carry the source mode.
func listStrategiesByIDs(ctx context.Context, q sqlQuerier, dialect Dialect, ids []string) ([]StrategyRow, error) {
	if len(ids) == 0 {
		return []StrategyRow{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		placeholders[i] = dialect.placeholder(i + 1)
		args = append(args, id)
	}
	query := fmt.Sprintf(`SELECT r.id, r.name, r.status, r.mode,
  (SELECT COUNT(*) FROM %s k WHERE k.route_strategy_id = r.id AND k.system_account_id = r.system_account_id) AS bound_api_keys
FROM %s r
WHERE r.id IN (%s)
ORDER BY r.id`,
		dialect.table("api_keys"), dialect.table("route_strategies"), strings.Join(placeholders, ","))
	return listStrategies(ctx, q, dialect, query, args...)
}

func listStrategies(ctx context.Context, q sqlQuerier, dialect Dialect, query string, args ...any) ([]StrategyRow, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 hybrid_smart route strategies 失败: %w", err)
	}
	defer rows.Close()
	var strategies []StrategyRow
	for rows.Next() {
		var row StrategyRow
		var mode sql.NullString
		if err := rows.Scan(&row.ID, &row.Name, &row.Status, &mode, &row.BoundAPIKeys); err != nil {
			return nil, fmt.Errorf("读取 hybrid_smart route strategy 行失败: %w", err)
		}
		row.Mode = mode.String
		strategies = append(strategies, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 hybrid_smart route strategies 失败: %w", err)
	}
	if strategies == nil {
		strategies = []StrategyRow{}
	}
	return strategies, nil
}

func countHybridStrategies(ctx context.Context, q sqlQuerier, dialect Dialect) (int, error) {
	var count int
	if err := q.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE mode = '%s'`,
		dialect.table("route_strategies"), SourceMode,
	)).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计残留 hybrid_smart route strategies 失败: %w", err)
	}
	return count, nil
}

func modeDistribution(ctx context.Context, q sqlQuerier, dialect Dialect) (map[string]int, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT mode, COUNT(*) FROM %s GROUP BY mode ORDER BY mode`,
		dialect.table("route_strategies"),
	))
	if err != nil {
		return nil, fmt.Errorf("读取 route strategies mode 分布失败: %w", err)
	}
	defer rows.Close()
	distribution := map[string]int{}
	for rows.Next() {
		var mode string
		var count int
		if err := rows.Scan(&mode, &count); err != nil {
			return nil, fmt.Errorf("读取 route strategies mode 分布行失败: %w", err)
		}
		distribution[mode] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 route strategies mode 分布失败: %w", err)
	}
	return distribution, nil
}

// isoMillis mirrors the gateway stored timestamp format (Node nowIso():
// UTC ISO string with millisecond precision).
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

func strategyIDs(strategies []StrategyRow) []string {
	ids := make([]string, 0, len(strategies))
	for _, row := range strategies {
		ids = append(ids, row.ID)
	}
	return ids
}
