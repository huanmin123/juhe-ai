package mockdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// env 是一次造数运行的存储上下文：目标 PostgreSQL 库的单个连接池、日志器、
// 选项与时钟，以及域已产出数据的记账（覆盖报告与摘要都要用）。
//
// PG 单形态下全部 schema 共享同一个 *sql.DB：写入按 schema 全限定名路由，
// 不再存在 sqlite 时代的「每文件一个句柄、懒打开」拓扑。
type env struct {
	options Options
	logger  *slog.Logger

	db *sql.DB

	mu      sync.Mutex
	results map[string]DomainResult
	users   []mockUser
	apiKeys []mockAPIKey
	owner   mockOwner

	// pkCache 缓存「schema.table → 主键列序」，insertMap 生成 ON CONFLICT
	// 目标用。主键在造数运行期内不变，进程内缓存一次查询即可。
	pkCache map[string][]string
}

// openEnvDatabase 是打开目标 PostgreSQL 连接的注入点：默认走 pgx/stdlib，
// 与 gateway/jobs/maintenance 的驱动一致。
var openEnvDatabase = func(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}

// newEnv 构建存储上下文并打开目标库连接。logger 为 nil 时丢弃日志（库调用方
// 不需要日志时不用自己造 discard handler）。
func newEnv(options Options, logger *slog.Logger) (*env, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	db, err := openEnvDatabase(options.DSN)
	if err != nil {
		return nil, fmt.Errorf("打开 mockdata 目标 PostgreSQL 连接失败: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mockdata 目标 PostgreSQL 连接不可用: %w", err)
	}
	db.SetMaxOpenConns(2)
	return &env{
		options: options,
		logger:  logger,
		db:      db,
		results: map[string]DomainResult{},
		pkCache: map[string][]string{},
	}, nil
}

// Close 关闭目标库连接。错误只用于诊断，不改变调用方已经得到的结论。
func (e *env) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.db == nil {
		return nil
	}
	if err := e.db.Close(); err != nil {
		return fmt.Errorf("关闭 mockdata 目标库连接失败: %w", err)
	}
	e.db = nil
	return nil
}

// DB 暴露目标库连接（属主探测与分区确保等框架步骤复用同一连接）。
func (e *env) DB() *sql.DB { return e.db }

// tx 在目标库上执行一个事务：fn 返回错误即回滚，保证多表写入（造数的业务
// 闭环）不会留下半批数据。
func (e *env) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mockdata begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("mockdata: %w (rollback: %v)", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mockdata commit: %w", err)
	}
	return nil
}

// insertMapTx 插入一行：列名排序后拼 SQL（同一批数据在任何一次运行里都生成
// 逐字相同的语句，便于用 SQL 文本或 fuzz 断言回放），占位符用 PostgreSQL 的
// $n，冲突目标取表主键，冲突时整行更新（对齐 sqlite 实现 INSERT OR REPLACE
// 的幂等语义：重复执行不报错也不叠加）。表名必须是全限定 schema.table。
func (e *env) insertMapTx(ctx context.Context, tx *sql.Tx, schemaTable string, columns map[string]any) error {
	statement, values, err := e.buildInsert(ctx, tx, schemaTable, columns)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, statement, values...); err != nil {
		return fmt.Errorf("写入 %s: %w", schemaTable, err)
	}
	return nil
}

// buildInsert 生成 insertMapTx 的语句与参数。导出给测试断言语句形状。
func (e *env) buildInsert(ctx context.Context, tx *sql.Tx, schemaTable string, columns map[string]any) (string, []any, error) {
	if len(columns) == 0 {
		return "", nil, fmt.Errorf("insertMap(%s) 需要至少一列", schemaTable)
	}
	schema, table, err := splitQualified(schemaTable)
	if err != nil {
		return "", nil, err
	}
	names := make([]string, 0, len(columns))
	for column := range columns {
		names = append(names, column)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	quotedNames := make([]string, len(names))
	values := make([]any, len(names))
	for i, column := range names {
		placeholders[i] = "$" + fmt.Sprint(i+1)
		quotedNames[i] = quotePGIdentifier(column)
		values[i] = columns[column]
	}
	statement := "INSERT INTO " + quotePGIdentifier(schema) + "." + quotePGIdentifier(table) +
		" (" + strings.Join(quotedNames, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	pk, err := e.primaryKeyColumns(ctx, tx, schema, table)
	if err != nil {
		return "", nil, err
	}
	if len(pk) > 0 {
		quotedPK := make([]string, len(pk))
		updates := make([]string, 0, len(names))
		pkSet := map[string]bool{}
		for i, column := range pk {
			quotedPK[i] = quotePGIdentifier(column)
			pkSet[column] = true
		}
		for _, column := range names {
			if pkSet[column] {
				continue
			}
			updates = append(updates, quotePGIdentifier(column)+" = EXCLUDED."+quotePGIdentifier(column))
		}
		if len(updates) > 0 {
			statement += " ON CONFLICT (" + strings.Join(quotedPK, ", ") + ") DO UPDATE SET " + strings.Join(updates, ", ")
		} else {
			statement += " ON CONFLICT (" + strings.Join(quotedPK, ", ") + ") DO NOTHING"
		}
	}
	return statement, values, nil
}

// primaryKeyColumns 返回表的主键列（按键序）。information_schema 枚举一次并
// 缓存；表不存在返回空切片（调用方按业务语义决定报错或跳过）。
func (e *env) primaryKeyColumns(ctx context.Context, querier queryer, schema, table string) ([]string, error) {
	key := schema + "." + table
	e.mu.Lock()
	cached, ok := e.pkCache[key]
	e.mu.Unlock()
	if ok {
		return cached, nil
	}
	rows, err := querier.QueryContext(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.constraint_schema = kcu.constraint_schema
		 AND tc.table_name = kcu.table_name
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 主键: %w", key, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, fmt.Errorf("查询 %s 主键: %w", key, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("查询 %s 主键: %w", key, err)
	}
	e.mu.Lock()
	e.pkCache[key] = columns
	e.mu.Unlock()
	return columns, nil
}

// queryer 是只读查询面；*sql.DB 与 *sql.Tx 都满足。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// splitQualified 把 schema.table 拆成两段；缺 schema 即错（PG 造数的所有 SQL
// 必须全限定 schema，不允许 search_path 隐式解析）。
func splitQualified(schemaTable string) (schema, table string, err error) {
	schema, table, found := strings.Cut(schemaTable, ".")
	if !found || schema == "" || table == "" || strings.Contains(table, ".") {
		return "", "", fmt.Errorf("mockdata 表名必须全限定为 schema.table: %q", schemaTable)
	}
	return schema, table, nil
}

// quotePGIdentifier 双引号引用 PG 标识符（内部双引号翻倍）。
func quotePGIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// queryCount 返回全限定表的行数（覆盖检查用）。
func (e *env) queryCount(ctx context.Context, schemaTable string) (int, error) {
	var count int
	if err := e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+schemaTable).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", schemaTable, err)
	}
	return count, nil
}

// recordDomainResult 记录一个域的结果；同名后到覆盖先到（域不会重复执行，
// 这是防御）。
func (e *env) recordDomainResult(result DomainResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.results[result.Name] = result
}

// domainResults 返回按注册表顺序排列的域结果（未产出的域补空结果，保证报告
// 始终列出全部注册域）。
func (e *env) domainResults() []DomainResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	ordered := make([]DomainResult, 0, len(domainRegistry))
	for _, domain := range domainRegistry {
		if result, ok := e.results[domain.Name]; ok {
			ordered = append(ordered, result)
			continue
		}
		ordered = append(ordered, DomainResult{Name: domain.Name})
	}
	return ordered
}

// domainWired 判断域是否已产出数据（Counts 非空）。覆盖断言与摘要的 wired
// 判定共用这一个定义。
func (e *env) domainWired(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	result, ok := e.results[name]
	return ok && len(result.Counts) > 0
}

// addMockUser 登记一个配套用户（域实现者在创建 system_accounts 行后调用）。
func (e *env) addMockUser(user mockUser) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.users = append(e.users, user)
}

// addAPIKey 登记一条 API Key（域实现者在创建 api_keys 行后调用）。
func (e *env) addAPIKey(key mockAPIKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.apiKeys = append(e.apiKeys, key)
}

// setOwner 登记 owner（admin）；重复调用以最后一次为准，避免域重跑时叠加。
func (e *env) setOwner(owner mockOwner) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.owner = owner
}

// summarySnapshot 取一份摘要输入的浅拷贝，保证写文件时不再持锁。空集合保持
// nil（不预先补成空切片）：nil 的统一兜底只留在 writeSummary 一处。
func (e *env) summarySnapshot() (mockOwner, []mockUser, []mockAPIKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	users := append([]mockUser(nil), e.users...)
	keys := append([]mockAPIKey(nil), e.apiKeys...)
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return e.owner, users, keys
}
