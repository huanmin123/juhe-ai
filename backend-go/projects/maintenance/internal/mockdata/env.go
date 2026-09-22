package mockdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// sqliteBusyTimeoutMs 与 gateway/jobs 的 SQLite 连接保持一致：造数进程可能与
// 开发态后端并发跑，busy_timeout 让短暂的写锁竞争退让而不是立刻报错。
const sqliteBusyTimeoutMs = 5000

// sqliteDSN 是造数打开的 SQLite DSN。_txlock=immediate 让显式事务一开始就取
// 写锁（SQLite 单写者语义），避免「先读后写」在并发写者下升级锁失败。
func sqliteDSN(path string) string {
	// Windows 路径必须在 file: URI 里做斜杠归一，否则 sqlite URI 解析失败。
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_txlock=immediate", filepath.ToSlash(path), sqliteBusyTimeoutMs)
}

// openSQLiteDatabase 是打开 SQLite 句柄的注入点：默认走 modernc.org/sqlite，
// 测试替换它来驱动「打开失败 / 语句失败 / 行集迭代失败」等真实文件系统无法
// 触发的错误分支（与 maintenance 其他包的错误注入测试同一手法，例如
// internal/schema/wm_pg_fake_test.go）。
var openSQLiteDatabase = func(dsn string) (*sql.DB, error) {
	return sql.Open("sqlite", dsn)
}

// env 是一次造数运行的存储上下文：按存储名懒打开的 SQLite 句柄、日志器、
// 选项与时钟，以及域已产出数据的记账（覆盖报告与摘要都要用）。
//
// 为什么句柄懒打开：一次造数覆盖十几个库和最多 2×256 个分片文件，全部提前
// 打开会在「目标库还没 bootstrap」时创建一堆空文件；懒打开让清理与覆盖检查
// 可以先只看已存在的存储，只有真正要写入的域才创建文件。
type env struct {
	options Options
	logger  *slog.Logger

	byName map[string]store
	order  []string

	mu      sync.Mutex
	opened  map[string]*sql.DB
	results map[string]DomainResult
	users   []mockUser
	apiKeys []mockAPIKey
	owner   mockOwner
}

// newEnv 构建存储上下文。logger 为 nil 时丢弃日志（库调用方不需要日志时
// 不用自己造 discard handler）。
func newEnv(options Options, logger *slog.Logger) *env {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	stores := options.Paths.allStores()
	e := &env{
		options: options,
		logger:  logger,
		byName:  make(map[string]store, len(stores)),
		order:   make([]string, 0, len(stores)),
		opened:  map[string]*sql.DB{},
		results: map[string]DomainResult{},
	}
	for _, item := range stores {
		if _, exists := e.byName[item.Name]; exists {
			continue
		}
		e.byName[item.Name] = item
		e.order = append(e.order, item.Name)
	}
	return e
}

// allStores 展开本次造数覆盖的全部存储：固定文件、codex context state 分片、
// 以及文件系统上已存在的 usage 分片。
func (p Paths) allStores() []store {
	stores := p.fixedStores()
	stores = append(stores, p.codexContextStores()...)
	stores = append(stores, p.usageShardStores()...)
	return stores
}

// usageShardStores 递归枚举已存在的 usage 分片文件。
//
// 权威路径布局是 jobs usagewriter 的
// <root>/YYYY/MM/DD/usage-YYYYMMDD-sNN.sqlite3（dev 数据目录实测的
// usage_record_shards.file_path 就是这一形态）。这里按「相对分片根至少一层
// 子目录、文件名以 .sqlite3 结尾」递归收集：既覆盖权威布局，也容忍骨架期
// 遗留的 <root>/<bucketDateKey>/<shard>.sqlite3 两级形态（更深的目录层级同样
// 接受，布局演进不需要再改这里）。分片根下的散落 .sqlite3 不算分片——它不是
// 任何 bucket 下的分片文件；非 .sqlite3 后缀与 -wal/-shm 伴生文件同理排除。
//
// 为什么不查 usage-catalog 的分片登记表：清理与覆盖检查必须能处理「文件已存在
// 但登记表尚未写入」的半成品状态，目录枚举没有这个前置依赖；真正写分片的域
// 再按登记表语义补登。
//
// 返回顺序按分片所属 bucket 日期升序（无法识别 bucket 时按相对路径排序）：
// 目录遍历顺序取决于目录树形状（WalkDir 会先递归进 <root>/2026 再回到
// <root>/20260101），拿它当报告 / 清理顺序既不稳定也不符合「按日期看分片」的
// 直觉，因此显式排序。
func (p Paths) usageShardStores() []store {
	type shardFile struct {
		key  string
		path string
	}
	var files []shardFile
	_ = filepath.WalkDir(p.UsageShardRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 单个子目录读失败不放弃其余分片：清理/覆盖检查在部分损坏的
			// 数据目录上仍要能收敛可用部分。
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sqlite3") {
			return nil
		}
		relative, relErr := filepath.Rel(p.UsageShardRoot, path)
		if relErr != nil || !strings.Contains(relative, string(os.PathSeparator)) {
			return nil
		}
		files = append(files, shardFile{
			key:  filepath.ToSlash(strings.TrimSuffix(relative, ".sqlite3")),
			path: path,
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		left, right := usageShardBucketKey(files[i].key), usageShardBucketKey(files[j].key)
		if left != right {
			return left < right
		}
		return files[i].key < files[j].key
	})
	var stores []store
	for _, file := range files {
		stores = append(stores, store{
			Name:   StoreUsageShardPrefix + "[" + file.key + "]",
			Path:   file.path,
			Domain: DomainUsage,
		})
	}
	return stores
}

// usageShardBucketKey 从分片的相对路径键（斜杠分隔、已去掉 .sqlite3）取 bucket
// 日期键：权威布局 <YYYY>/<MM>/<DD>/<file> 拼成 YYYYMMDD，两级兼容布局
// <YYYYMMDD>/<file> 直接取首段；两种都认不出时回落到相对键本身（排序仍然稳定）。
func usageShardBucketKey(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) >= 1 && isDigits(parts[0]) && len(parts[0]) == 8 {
		return parts[0]
	}
	if len(parts) >= 3 && isDigits(parts[0]) && isDigits(parts[1]) && isDigits(parts[2]) &&
		len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return parts[0] + parts[1] + parts[2]
	}
	return key
}

// isDigits 判断整段都是 ASCII 数字（bucket 日期段判定用；非数字段直接回落）。
func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stores 返回按稳定顺序排列的存储清单（含懒打开之后才被发现的用法分片不参与，
// 调用方在打开新文件后需重新取清单）。
func (e *env) stores() []store {
	items := make([]store, 0, len(e.order))
	for _, name := range e.order {
		items = append(items, e.byName[name])
	}
	return items
}

// store 按名字取存储描述；未知名字（含未登记的新分片）返回错误而不是新建。
func (e *env) store(name string) (store, error) {
	item, ok := e.byName[name]
	if !ok {
		return store{}, fmt.Errorf("未知 mockdata 存储 %q", name)
	}
	return item, nil
}

// open 打开（缺失时创建）命名存储并缓存句柄。
func (e *env) open(name string) (*sql.DB, error) {
	item, err := e.store(name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if db, ok := e.opened[name]; ok {
		return db, nil
	}
	if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
		return nil, fmt.Errorf("创建 %s 目录 %s: %w", name, filepath.Dir(item.Path), err)
	}
	db, err := openSQLiteDatabase(sqliteDSN(item.Path))
	if err != nil {
		return nil, fmt.Errorf("打开 %s (%s): %w", name, item.Path, err)
	}
	db.SetMaxOpenConns(1)
	// WAL 是文件级设置，需要在建库后立刻生效；foreign_keys/busy_timeout 已由
	// DSN 的 _pragma 覆盖，这里只补 WAL。
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("配置 %s 的 journal_mode: %w", name, err)
	}
	e.opened[name] = db
	return db, nil
}

// openExisting 打开已存在的存储；文件不存在时返回 (nil, nil)，让清理与覆盖
// 检查可以安全跳过尚未 bootstrap 的存储而不创建空库。
func (e *env) openExisting(name string) (*sql.DB, error) {
	item, err := e.store(name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	cached, opened := e.opened[name]
	e.mu.Unlock()
	if opened {
		return cached, nil
	}
	if _, statErr := os.Stat(item.Path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("检查 %s (%s): %w", name, item.Path, statErr)
	}
	return e.open(name)
}

// Close 关闭所有已打开的句柄；错误只用于诊断，不改变调用方已经得到的结论。
// 每个存储的错误用 %w 包装后 errors.Join 聚合：调用方既能读完整文本，也能用
// errors.Is 追到驱动错误（否则关闭失败在测试与排障里只能靠字符串匹配）。
func (e *env) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.opened))
	for name := range e.opened {
		names = append(names, name)
	}
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		if err := e.opened[name].Close(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}
	e.opened = map[string]*sql.DB{}
	if len(failures) > 0 {
		return fmt.Errorf("关闭 mockdata 存储失败: %w", errors.Join(failures...))
	}
	return nil
}

// exec 在命名存储上执行一条语句。
func (e *env) exec(ctx context.Context, name, query string, args ...any) (sql.Result, error) {
	db, err := e.open(name)
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, query, args...)
}

// tx 在命名存储上执行一个 IMMEDIATE 事务：fn 返回错误或 panic 之外的中断即
// 回滚，保证多表写入（造数的业务闭环）不会留下半批数据。
func (e *env) tx(ctx context.Context, name string, fn func(*sql.Tx) error) error {
	db, err := e.open(name)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s begin: %w", name, err)
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("%s: %w (rollback: %v)", name, err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s commit: %w", name, err)
	}
	return nil
}

// insertMap 插入一行，列名排序后拼 SQL：同一批数据在任何一次运行里都生成
// 逐字相同的语句，便于用 SQL 文本或 fuzz 断言回放。
func (e *env) insertMap(ctx context.Context, name, table string, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("insertMap(%s) 需要至少一列", table)
	}
	names := make([]string, 0, len(columns))
	for column := range columns {
		names = append(names, column)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	values := make([]any, len(names))
	for i, column := range names {
		placeholders[i] = "?"
		values[i] = columns[column]
	}
	query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	_, err := e.exec(ctx, name, query, values...)
	return err
}

// queryCount 返回表行数。
func (e *env) queryCount(ctx context.Context, name, table string) (int, error) {
	db, err := e.open(name)
	if err != nil {
		return 0, err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s.%s: %w", name, table, err)
	}
	return count, nil
}

// tableColumn 是 PRAGMA table_info 的一行，autofill 的按列启发式与覆盖检查
// 都读它。
type tableColumn struct {
	Name         string
	Type         string
	NotNull      bool
	DefaultValue any
	PrimaryKey   int
}

// tableColumns 读取表的列信息。表名来自 sqlite_master 枚举结果，不是外部
// 输入，因此可以内插进 PRAGMA（PRAGMA 不支持绑定参数）。
func (e *env) tableColumns(ctx context.Context, name, table string) ([]tableColumn, error) {
	db, err := e.openExisting(name)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, nil
	}
	return queryTableColumns(ctx, db, table)
}

// queryTableColumns 是 tableColumns 的句柄版，供清理与覆盖在已持有句柄时复用。
func queryTableColumns(ctx context.Context, db *sql.DB, table string) ([]tableColumn, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()
	var columns []tableColumn
	for rows.Next() {
		var (
			cid     int
			column  tableColumn
			notNull int
			pk      int
		)
		if err := rows.Scan(&cid, &column.Name, &column.Type, &notNull, &column.DefaultValue, &pk); err != nil {
			return nil, fmt.Errorf("table_info(%s): %w", table, err)
		}
		column.NotNull = notNull != 0
		column.PrimaryKey = pk
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	return columns, nil
}

// tableNames 列出用户表（排除 sqlite_ 内部表）；按名字排序保证报告稳定。
func (e *env) tableNames(ctx context.Context, name string) ([]string, error) {
	db, err := e.openExisting(name)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, nil
	}
	return queryTableNames(ctx, db)
}

// queryTableNames 是 tableNames 的句柄版。
func queryTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		names = append(names, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

// existsTable 判断表是否存在。
func (e *env) existsTable(ctx context.Context, name, table string) (bool, error) {
	db, err := e.openExisting(name)
	if err != nil {
		return false, err
	}
	if db == nil {
		return false, nil
	}
	return queryExistsTable(ctx, db, table)
}

// queryExistsTable 是 existsTable 的句柄版。
func queryExistsTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?", table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// recordDomainResult 记账一次域执行结果：Counts 为空即视为该域尚未接线，
// 覆盖报告据此把该域的断言标成 not-covered 而不是 Ready 失败。
func (e *env) recordDomainResult(result DomainResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.results[result.Name] = result
}

// domainWired 报告本次运行中该域是否产出了数据。
func (e *env) domainWired(domain string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	result, ok := e.results[domain]
	return ok && len(result.Counts) > 0
}

// domainResults 返回按注册顺序排列的域结果，供报告输出。
func (e *env) domainResults() []DomainResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	results := make([]DomainResult, 0, len(e.results))
	for _, seed := range domainSeeds {
		if result, ok := e.results[seed.Name]; ok {
			results = append(results, result)
		}
	}
	return results
}
