package j3aproxylatency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	SchemaName     = "juhe_jobs"
	BootstrapEnv   = "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL"
	bootstrapLock  = int64(732_946_110_271_044_015)
	postgresSetSQL = "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"
)

var requiredTables = contracts.J3AProxyLatencyTables
var requiredIndexes = contracts.J3AProxyLatencyIndexes
var requiredColumns = contracts.J3AProxyLatencyColumns
var requiredConstraints = contracts.J3AProxyLatencyConstraints

// Report intentionally contains only object identifiers and database-role
// metadata. It never contains a connection URL, password, payload, or proxy
// credential.
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

// Open validates the maintenance-only PostgreSQL URL before opening a small,
// one-shot pool. Production commands must use a separately provisioned role;
// this helper never falls back to application URLs.
func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("J3a bootstrap 必须提供 postgres/postgresql URL")
	}
	if parsed.Hostname() == "" || strings.Trim(strings.TrimSpace(parsed.Path), "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("J3a bootstrap URL 必须包含主机、数据库和显式角色")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 J3a bootstrap PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Run checks the externally provisioned J3a jobs schema. With apply=false it
// is read-only. With apply=true it acquires a transaction-scoped advisory lock
// and adds only the J3a jobs tables/indexes; it never touches juhe_business,
// Node schema initialization, Goose ledger, or production owner switches.
func Run(ctx context.Context, db *sql.DB, apply bool) (Report, error) {
	if db == nil {
		return Report{}, errors.New("J3a bootstrap 数据库未初始化")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !apply})
	if err != nil {
		return Report{}, fmt.Errorf("开始 J3a bootstrap 事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, postgresSetSQL); err != nil {
		return Report{}, fmt.Errorf("配置 J3a bootstrap 事务超时失败: %w", err)
	}
	if apply {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", bootstrapLock); err != nil {
			return Report{}, fmt.Errorf("获取 J3a bootstrap advisory lock 失败: %w", err)
		}
	}
	report, err := inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !apply || report.Ready() {
		if err := tx.Commit(); err != nil {
			return Report{}, fmt.Errorf("提交 J3a bootstrap 检查事务失败: %w", err)
		}
		return report, nil
	}
	if report.MissingSchema {
		return Report{}, errors.New("J3a bootstrap 拒绝创建 juhe_jobs schema；必须由受控数据库流程预置且 owner 为目标 jobs role")
	}
	if report.SchemaOwner != report.CurrentRole {
		return Report{}, fmt.Errorf("J3a bootstrap 拒绝跨角色修改 juhe_jobs schema: owner=%s current=%s", report.SchemaOwner, report.CurrentRole)
	}
	if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
		return Report{}, fmt.Errorf("执行 J3a PostgreSQL jobs schema bootstrap 失败: %w", err)
	}
	report, err = inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !report.Ready() {
		return Report{}, fmt.Errorf("J3a PostgreSQL jobs schema bootstrap 后契约仍不完整: missing_tables=%s invalid_tables=%s missing_indexes=%s invalid_indexes=%s", strings.Join(report.MissingTables, ","), strings.Join(report.InvalidTables, ","), strings.Join(report.MissingIndexes, ","), strings.Join(report.InvalidIndexes, ","))
	}
	report.Applied = true
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("提交 J3a PostgreSQL jobs schema bootstrap 失败: %w", err)
	}
	return report, nil
}

func inspectTx(ctx context.Context, tx *sql.Tx) (Report, error) {
	report := Report{Schema: SchemaName}
	if err := tx.QueryRowContext(ctx, "SELECT current_database(), current_user").Scan(&report.Database, &report.CurrentRole); err != nil {
		return Report{}, fmt.Errorf("读取 J3a bootstrap PostgreSQL 身份失败: %w", err)
	}
	var owner sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1", SchemaName).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		report.MissingSchema = true
		return report, nil
	}
	if err != nil {
		return Report{}, fmt.Errorf("读取 J3a jobs schema owner 失败: %w", err)
	}
	report.SchemaOwner = owner.String
	report.OwnerMismatch = report.SchemaOwner != report.CurrentRole

	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name = ANY($2)", SchemaName, requiredTables)
	if err != nil {
		return Report{}, fmt.Errorf("读取 J3a jobs table 契约失败: %w", err)
	}
	seenTables := make(map[string]struct{}, len(requiredTables))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return Report{}, fmt.Errorf("读取 J3a jobs table 名称失败: %w", err)
		}
		seenTables[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Report{}, fmt.Errorf("遍历 J3a jobs table 契约失败: %w", err)
	}
	_ = rows.Close()
	for _, name := range requiredTables {
		if _, ok := seenTables[name]; !ok {
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
		return Report{}, fmt.Errorf("读取 J3a jobs index 契约失败: %w", err)
	}
	seenIndexes := make(map[string]string, len(requiredIndexes))
	for indexRows.Next() {
		var name, definition string
		if err := indexRows.Scan(&name, &definition); err != nil {
			_ = indexRows.Close()
			return Report{}, fmt.Errorf("读取 J3a jobs index 定义失败: %w", err)
		}
		seenIndexes[name] = strings.ToLower(strings.Join(strings.Fields(definition), " "))
	}
	if err := indexRows.Err(); err != nil {
		_ = indexRows.Close()
		return Report{}, fmt.Errorf("遍历 J3a jobs index 契约失败: %w", err)
	}
	_ = indexRows.Close()
	for name, expected := range requiredIndexes {
		definition, ok := seenIndexes[name]
		if !ok {
			report.MissingIndexes = append(report.MissingIndexes, name)
			continue
		}
		if !strings.Contains(definition, expected) {
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
	rows, err := tx.QueryContext(ctx, `SELECT table_name,column_name,data_type,udt_name,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name = ANY($2)`, SchemaName, requiredTables)
	if err != nil {
		return fmt.Errorf("读取 J3a jobs column 契约失败: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]map[string]contracts.PostgresColumnSpec, len(requiredColumns))
	for rows.Next() {
		var table, column, dataType, udtName, nullable string
		if err := rows.Scan(&table, &column, &dataType, &udtName, &nullable); err != nil {
			return fmt.Errorf("读取 J3a jobs column 定义失败: %w", err)
		}
		if seen[table] == nil {
			seen[table] = make(map[string]contracts.PostgresColumnSpec)
		}
		seen[table][column] = contracts.PostgresColumnSpec{DataType: dataType, UdtName: udtName, Nullable: nullable == "YES"}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 J3a jobs column 契约失败: %w", err)
	}
	for table, expectedColumns := range requiredColumns {
		for column, expected := range expectedColumns {
			actual, ok := seen[table][column]
			if !ok {
				report.InvalidTables = append(report.InvalidTables, table+"."+column+":missing")
				continue
			}
			if actual.DataType != expected.DataType || actual.UdtName != expected.UdtName {
				report.InvalidTables = append(report.InvalidTables, fmt.Sprintf("%s.%s:type=%s/%s", table, column, actual.DataType, actual.UdtName))
				continue
			}
			if !expected.Nullable && actual.Nullable {
				report.InvalidTables = append(report.InvalidTables, table+"."+column+":nullable")
			}
		}
	}
	return nil
}

func inspectConstraints(ctx context.Context, tx *sql.Tx, report *Report) error {
	rows, err := tx.QueryContext(ctx, `SELECT relation.relname,pg_get_constraintdef(c.oid) FROM pg_constraint AS c JOIN pg_class AS relation ON relation.oid=c.conrelid JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace WHERE namespace.nspname=$1 AND relation.relname = ANY($2) AND c.contype IN ('p','u')`, SchemaName, requiredTables)
	if err != nil {
		return fmt.Errorf("读取 J3a jobs constraint 契约失败: %w", err)
	}
	defer rows.Close()
	seen := make(map[string][]string, len(requiredConstraints))
	for rows.Next() {
		var table, definition string
		if err := rows.Scan(&table, &definition); err != nil {
			return fmt.Errorf("读取 J3a jobs constraint 定义失败: %w", err)
		}
		seen[table] = append(seen[table], strings.ToLower(strings.Join(strings.Fields(definition), " ")))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 J3a jobs constraint 契约失败: %w", err)
	}
	for table, expectedDefinitions := range requiredConstraints {
		for _, expected := range expectedDefinitions {
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
	names := make([]string, 0, len(requiredIndexes))
	for name := range requiredIndexes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_proxy_leases (
 proxy_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_input_versions (
 proxy_id TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_inputs (
 request_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, UNIQUE(proxy_id, input_version)
);
CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_execution_claims (
 request_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_id TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, input_digest TEXT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_proxy ON juhe_jobs.proxy_latency_outcomes(proxy_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_cursor ON juhe_jobs.proxy_latency_outcomes(stored_at, outcome_id);
`
