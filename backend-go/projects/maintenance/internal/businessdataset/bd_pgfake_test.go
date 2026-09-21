package businessdataset

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// bdPGCatalog 是面向 businessdataset 导出/导入链路的内存 PostgreSQL 模拟器。
// 它只实现导出/导入两个方向实际使用的查询族与写入行为：
//   - 目录状态（列/主键/外键/序列/行）由测试显式构造；
//   - 事务配置（read only / isolation）按 driver.TxOptions 如实回报给 SHOW
//     transaction_read_only / SHOW transaction_isolation，使只读写入守卫成为
//     真实断言而非脚本回放；
//   - forceReadOnly 模拟“库端角色只读导致写入事务仍只读”，ignoreReadOnly
//     模拟“客户端只读选项不生效”的真实故障。
//
// 已知简化：fake 不执行外键约束（插入顺序正确性由插入日志与读回 digest 断言）；
// 排序按渲染字符串比较（导出/读回两侧一致，digest 对比语义不变）。
type bdPGCatalog struct {
	mu            sync.Mutex
	database      string
	tables        map[string]*bdFakeTable
	sequences     map[string]bdFakeSequence
	injectedFails []string
	// 目录行级故障旋钮：attrScanFault 让 pg_attribute 结果含坏 attnum 行
	// （Scan 进 int64 失败）；attrRowsErr/conRowsErr 让对应 Rows.Err 报错。
	attrScanFault bool
	attrRowsErr   bool
	conRowsErr    bool
	// forceReadOnly/ignoreReadOnly 复刻 wm_pgfake 的事务模式故障注入。
	forceReadOnly  bool
	ignoreReadOnly bool
	// onInsert 在每行插入后调用，用于注入读回 digest 漂移。
	onInsert func(table string, row map[string]driver.Value)
	// rowsFailFragment 命中查询时返回“先出一行再报错”的 Rows，模拟真实
	// PostgreSQL 在行迭代中途断链（网络/语句错误），覆盖 rows.Err 分支。
	rowsFailFragment string
	// commitFails 模拟事务提交失败（如 serializable 冲突）。
	commitFails bool
	// beginFails 模拟开启事务失败（连接刚断开等）。
	beginFails bool
	// rowsFailImmediate 让故障 Rows 在第一轮 Next 即报错（覆盖 QueryRow.Scan
	// 错误分支）；默认先出一行再报错（覆盖循环扫描的 rows.Err 分支）。
	rowsFailImmediate bool
	// insertLog 记录实际插入顺序，断言自引用表父先于子。
	insertLog []bdFakeInsert
	// deleteLog 记录 DELETE 目标表的执行顺序，断言 replace-existing 的
	// 拓扑逆序（子先父后）清空。
	deleteLog []string
	commits   int
	rollbacks int
}

type bdFakeTable struct {
	columns     []bdFakeColumn
	primaryKey  []string
	foreignKeys []bdFakeForeignKey
	rows        []map[string]driver.Value
	sequence    string
}

type bdFakeColumn struct {
	name     string
	dataType string
	nullable bool
}

type bdFakeForeignKey struct {
	name       string
	columns    []string
	refTable   string
	refColumns []string
}

type bdFakeSequence struct {
	value    int64
	isCalled bool
}

type bdFakeInsert struct {
	Table string
	Row   map[string]driver.Value
}

func newBDPGCatalog(database string) *bdPGCatalog {
	return &bdPGCatalog{
		database:  database,
		tables:    map[string]*bdFakeTable{},
		sequences: map[string]bdFakeSequence{},
	}
}

func (c *bdPGCatalog) addTable(name string, columns []bdFakeColumn, primaryKey []string) *bdFakeTable {
	table := &bdFakeTable{columns: columns, primaryKey: primaryKey}
	c.tables[name] = table
	return table
}

func (c *bdPGCatalog) addColumn(table string, column bdFakeColumn) {
	c.tables[table].columns = append(c.tables[table].columns, column)
}

func (c *bdPGCatalog) addForeignKey(table, name string, columns []string, refTable string, refColumns []string) {
	c.tables[table].foreignKeys = append(c.tables[table].foreignKeys, bdFakeForeignKey{name: name, columns: columns, refTable: refTable, refColumns: refColumns})
}

func (c *bdPGCatalog) seedRow(table string, values map[string]driver.Value) {
	c.tables[table].rows = append(c.tables[table].rows, values)
}

func (c *bdPGCatalog) setSequence(table, sequenceName string, value int64) {
	c.tables[table].sequence = sequenceName
	c.sequences[sequenceName] = bdFakeSequence{value: value, isCalled: true}
}

var (
	bdSelectRe    = regexp.MustCompile(`(?is)^SELECT (.+?) FROM juhe_business\."([A-Za-z_][A-Za-z0-9_]*)" ORDER BY (.+)$`)
	bdCountRe     = regexp.MustCompile(`(?is)^SELECT COUNT\(\*\) FROM juhe_business\."([A-Za-z_][A-Za-z0-9_]*)"$`)
	bdInsertRe    = regexp.MustCompile(`(?is)^INSERT INTO juhe_business\."([A-Za-z_][A-Za-z0-9_]*)" \((.+?)\) VALUES \((.+?)\)$`)
	bdDeleteRe    = regexp.MustCompile(`^DELETE FROM juhe_business\."([A-Za-z_][A-Za-z0-9_]*)"$`)
	bdMaxRe       = regexp.MustCompile(`(?is)^SELECT MAX\("([^"]+)"\) FROM juhe_business\."([A-Za-z_][A-Za-z0-9_]*)"$`)
	bdQualifiedRe = regexp.MustCompile(`^juhe_business\.([A-Za-z_][A-Za-z0-9_]*)$`)
	bdQuotedIdent = regexp.MustCompile(`"([^"]+)"`)
)

func (c *bdPGCatalog) query(txReadOnly bool, txIsolation string, query string, args []driver.NamedValue) (*bdFakeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.injectedFail(query); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(query, "SHOW transaction_read_only"):
		value := "off"
		if txReadOnly {
			value = "on"
		}
		return &bdFakeResult{columns: []string{"transaction_read_only"}, rows: [][]driver.Value{{value}}}, nil
	case strings.Contains(query, "SHOW transaction_isolation"):
		return &bdFakeResult{columns: []string{"transaction_isolation"}, rows: [][]driver.Value{{txIsolation}}}, nil
	case strings.Contains(query, "current_database()"):
		return &bdFakeResult{columns: []string{"current_database"}, rows: [][]driver.Value{{c.database}}}, nil
	case strings.Contains(query, "information_schema.columns"):
		schemaName := bdArgText(args[0].Value)
		tableName := bdArgText(args[1].Value)
		var rows [][]driver.Value
		if schemaName == SchemaName {
			if table := c.tables[tableName]; table != nil {
				for _, column := range table.columns {
					rows = append(rows, []driver.Value{column.name, column.dataType, bdNullableText(column.nullable)})
				}
			}
		}
		return &bdFakeResult{columns: []string{"column_name", "data_type", "is_nullable"}, rows: rows}, nil
	case strings.Contains(query, "indisprimary"):
		table := c.tables[bdArgText(args[1].Value)]
		var rows [][]driver.Value
		if table != nil {
			for _, key := range table.primaryKey {
				rows = append(rows, []driver.Value{key})
			}
		}
		return &bdFakeResult{columns: []string{"attname"}, rows: rows}, nil
	case strings.Contains(query, "pg_attribute"):
		var rows [][]driver.Value
		for _, name := range bdSortedTableNames(c.tables) {
			for index, column := range c.tables[name].columns {
				rows = append(rows, []driver.Value{name, int64(index + 1), column.name})
			}
		}
		if c.attrScanFault {
			rows = append(rows, []driver.Value{"bdrt_bad", "not-an-int", "col"})
		}
		res := &bdFakeResult{columns: []string{"relname", "attnum", "attname"}, rows: rows}
		if c.attrRowsErr {
			res.rowsErr = fmt.Errorf("bdPGCatalog: 注入 pg_attribute 行集错误")
		}
		return res, nil
	case strings.Contains(query, "pg_constraint"):
		var rows [][]driver.Value
		for _, tableName := range bdSortedTableNames(c.tables) {
			table := c.tables[tableName]
			for _, fk := range table.foreignKeys {
				rows = append(rows, []driver.Value{
					fk.name, tableName, fk.refTable,
					bdAttnumInt16(fk.columns, table),
					bdAttnumInt16(fk.refColumns, c.tables[fk.refTable]),
				})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return bdArgText(rows[i][0]) < bdArgText(rows[j][0]) })
		res := &bdFakeResult{columns: []string{"conname", "src", "dst", "conkey", "confkey"}, rows: rows}
		if c.conRowsErr {
			res.rowsErr = fmt.Errorf("bdPGCatalog: 注入 pg_constraint 行集错误")
		}
		return res, nil
	case strings.Contains(query, "pg_get_serial_sequence"):
		match := bdQualifiedRe.FindStringSubmatch(bdArgText(args[0].Value))
		table := &bdFakeTable{}
		if match != nil {
			table = c.tables[match[1]]
		}
		if table == nil || table.sequence == "" {
			return &bdFakeResult{columns: []string{"pg_get_serial_sequence"}, rows: [][]driver.Value{{nil}}}, nil
		}
		return &bdFakeResult{columns: []string{"pg_get_serial_sequence"}, rows: [][]driver.Value{{table.sequence}}}, nil
	case strings.Contains(query, "setval("):
		sequenceName := bdArgText(args[0].Value)
		value, ok := args[1].Value.(int64)
		if !ok {
			return nil, fmt.Errorf("bdPGCatalog: setval 值类型 %T 不支持", args[1].Value)
		}
		isCalled, ok := args[2].Value.(bool)
		if !ok {
			return nil, fmt.Errorf("bdPGCatalog: setval is_called 类型 %T 不支持", args[2].Value)
		}
		c.sequences[sequenceName] = bdFakeSequence{value: value, isCalled: isCalled}
		return &bdFakeResult{columns: []string{"setval"}, rows: [][]driver.Value{{value}}}, nil
	case strings.Contains(query, "SELECT MAX("):
		match := bdMaxRe.FindStringSubmatch(strings.TrimSpace(query))
		if match == nil {
			return nil, fmt.Errorf("bdPGCatalog: 无法解析 MAX 查询 %.120s", query)
		}
		var max driver.Value
		if table := c.tables[match[2]]; table != nil {
			for _, row := range table.rows {
				value, ok := row[match[1]].(int64)
				if !ok {
					continue
				}
				if max == nil || value > max.(int64) {
					max = value
				}
			}
		}
		return &bdFakeResult{columns: []string{"max"}, rows: [][]driver.Value{{max}}}, nil
	default:
		return c.plainSelect(query)
	}
}

func (c *bdPGCatalog) injectedFail(query string) error {
	for _, fragment := range c.injectedFails {
		if strings.Contains(query, fragment) {
			return fmt.Errorf("bdPGCatalog: 注入故障 %q", fragment)
		}
	}
	return nil
}

func bdSortedTableNames(tables map[string]*bdFakeTable) []string {
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func bdAttnumInt16(columns []string, table *bdFakeTable) []int16 {
	out := make([]int16, 0, len(columns))
	for _, column := range columns {
		if table == nil {
			out = append(out, int16(len(out)+1))
			continue
		}
		for index, candidate := range table.columns {
			if candidate.name == column {
				out = append(out, int16(index+1))
				break
			}
		}
	}
	return out
}

// plainSelect 处理固定形状 SELECT "c", ... FROM juhe_business."t" ORDER BY "o", ...
func (c *bdPGCatalog) plainSelect(query string) (*bdFakeResult, error) {
	trimmed := strings.TrimSpace(query)
	// 结构-only 表的源行数统计：SELECT COUNT(*) FROM <schema>.<table>。
	if m := bdCountRe.FindStringSubmatch(trimmed); m != nil {
		table := c.tables[m[1]]
		count := 0
		if table != nil {
			count = len(table.rows)
		}
		return &bdFakeResult{columns: []string{"count"}, rows: [][]driver.Value{{int64(count)}}}, nil
	}
	match := bdSelectRe.FindStringSubmatch(trimmed)
	if match == nil {
		return nil, fmt.Errorf("bdPGCatalog: 不支持的查询 %.160s", query)
	}
	var projection, orderKeys []string
	for _, column := range bdQuotedIdent.FindAllStringSubmatch(match[1], -1) {
		projection = append(projection, column[1])
	}
	for _, column := range bdQuotedIdent.FindAllStringSubmatch(match[3], -1) {
		orderKeys = append(orderKeys, column[1])
	}
	table := c.tables[match[2]]
	rows := make([][]driver.Value, 0)
	if table != nil {
		for _, row := range table.rows {
			values := make([]driver.Value, len(projection))
			for i, column := range projection {
				values[i] = row[column]
			}
			rows = append(rows, values)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, key := range orderKeys {
			left := bdRenderCell(bdRowCell(rows[i], projection, key))
			right := bdRenderCell(bdRowCell(rows[j], projection, key))
			if left != right {
				return left < right
			}
		}
		return false
	})
	return &bdFakeResult{columns: projection, rows: rows}, nil
}

func bdRowCell(row []driver.Value, projection []string, key string) driver.Value {
	for i, column := range projection {
		if column == key {
			return row[i]
		}
	}
	return nil
}

func (c *bdPGCatalog) exec(query string, args []driver.NamedValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.injectedFail(query); err != nil {
		return err
	}
	if strings.Contains(query, "setval(") {
		if len(args) != 3 {
			return fmt.Errorf("bdPGCatalog: setval 参数数 %d 不合法", len(args))
		}
		sequenceName := bdArgText(args[0].Value)
		value, ok := args[1].Value.(int64)
		if !ok {
			return fmt.Errorf("bdPGCatalog: setval 值类型 %T 不支持", args[1].Value)
		}
		isCalled, ok := args[2].Value.(bool)
		if !ok {
			return fmt.Errorf("bdPGCatalog: setval is_called 类型 %T 不支持", args[2].Value)
		}
		c.sequences[sequenceName] = bdFakeSequence{value: value, isCalled: isCalled}
		return nil
	}
	if match := bdDeleteRe.FindStringSubmatch(strings.TrimSpace(query)); match != nil {
		table := c.tables[match[1]]
		if table == nil {
			return fmt.Errorf("bdPGCatalog: DELETE 目标表 %s 不存在", match[1])
		}
		table.rows = nil
		c.deleteLog = append(c.deleteLog, match[1])
		return nil
	}
	match := bdInsertRe.FindStringSubmatch(strings.TrimSpace(query))
	if match == nil {
		return fmt.Errorf("bdPGCatalog: 不支持的写入 %.160s", query)
	}
	table := c.tables[match[1]]
	if table == nil {
		return fmt.Errorf("bdPGCatalog: INSERT 目标表 %s 不存在", match[1])
	}
	var columns []string
	for _, column := range bdQuotedIdent.FindAllStringSubmatch(match[2], -1) {
		columns = append(columns, column[1])
	}
	if len(columns) != len(args) {
		return fmt.Errorf("bdPGCatalog: INSERT 列数 %d 与参数数 %d 不一致", len(columns), len(args))
	}
	row := make(map[string]driver.Value, len(columns))
	for i, column := range columns {
		row[column] = args[i].Value
	}
	// 主键冲突模拟：与真实 PostgreSQL 一致，INSERT 撞上同主键现存行即报
	// duplicate key（replace=false + 目标已 seed 的 fail-closed 固化依赖此行为）。
	if len(table.primaryKey) > 0 {
		for _, existing := range table.rows {
			duplicate := true
			for _, key := range table.primaryKey {
				if bdRenderCell(existing[key]) != bdRenderCell(row[key]) {
					duplicate = false
					break
				}
			}
			if duplicate {
				return fmt.Errorf("pq: duplicate key value violates unique constraint %q", "juhe_business_"+match[1]+"_pkey")
			}
		}
	}
	table.rows = append(table.rows, row)
	c.insertLog = append(c.insertLog, bdFakeInsert{Table: match[1], Row: row})
	if c.onInsert != nil {
		c.onInsert(match[1], row)
	}
	return nil
}

func bdArgText(value driver.Value) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprintf("%v", value)
}

func bdNullableText(nullable bool) string {
	if nullable {
		return "YES"
	}
	return "NO"
}

func bdRenderCell(value driver.Value) string {
	switch typed := value.(type) {
	case nil:
		return "\x00null"
	case []byte:
		return string(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// bdFakeResult 与驱动接口实现。

type bdFakeResult struct {
	columns []string
	rows    [][]driver.Value
	rowsErr error
}

type bdFakeRows struct {
	result *bdFakeResult
	pos    int
}

func (r *bdFakeRows) Columns() []string { return r.result.columns }
func (r *bdFakeRows) Close() error      { return nil }
func (r *bdFakeRows) Err() error        { return r.result.rowsErr }
func (r *bdFakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.result.rows) {
		if r.result.rowsErr != nil {
			return r.result.rowsErr
		}
		return io.EOF
	}
	copy(dest, r.result.rows[r.pos])
	r.pos++
	return nil
}

type bdFakeConnector struct {
	catalog        *bdPGCatalog
	forceReadOnly  bool
	ignoreReadOnly bool
}

func (c bdFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &bdFakeConn{catalog: c.catalog, forceReadOnly: c.forceReadOnly, ignoreReadOnly: c.ignoreReadOnly, rowsFailFragment: c.catalog.rowsFailFragment, commitFails: c.catalog.commitFails, beginFails: c.catalog.beginFails, rowsFailImmediate: c.catalog.rowsFailImmediate, txIsolation: "read committed"}, nil
}

// bdErrorRows 先返回一行再注入错误，模拟行迭代中途失败。列名按查询投影推导，
// 使行值 Scan 正常进行、错误落在 rows.Err 分支（与真实断链一致）。
type bdErrorRows struct {
	columns  []string
	err      error
	sent     bool
	errFirst bool
}

func bdErrorColumns(query string) []string {
	match := regexp.MustCompile(`(?is)^SELECT (.+?) FROM `).FindStringSubmatch(strings.TrimSpace(query))
	if match == nil {
		return []string{"boom"}
	}
	var names []string
	for _, ident := range bdQuotedIdent.FindAllStringSubmatch(match[1], -1) {
		names = append(names, ident[1])
	}
	if len(names) == 0 {
		for _, raw := range strings.Split(match[1], ",") {
			names = append(names, strings.TrimSpace(strings.TrimPrefix(raw, "c.")))
		}
	}
	return names
}

func (r *bdErrorRows) Columns() []string { return r.columns }
func (r *bdErrorRows) Close() error      { return nil }
func (r *bdErrorRows) Next(dest []driver.Value) error {
	if r.errFirst {
		return r.err
	}
	if !r.sent {
		r.sent = true
		dest[0] = "boom"
		return nil
	}
	return r.err
}

func (c bdFakeConnector) Driver() driver.Driver { return bdFakeDriver{} }

type bdFakeDriver struct{}

func (bdFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("bd fake driver: 请使用 sql.OpenDB")
}

type bdFakeConn struct {
	catalog           *bdPGCatalog
	forceReadOnly     bool
	ignoreReadOnly    bool
	rowsFailFragment  string
	commitFails       bool
	beginFails        bool
	rowsFailImmediate bool
	txReadOnly        bool
	txIsolation       string
	inTx              bool
}

func (c *bdFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &bdFakeStmt{conn: c, query: query}, nil
}

func (c *bdFakeConn) Close() error { return nil }

func (c *bdFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("bd fake driver: Begin 不应被调用（走 BeginTx）")
}

func (c *bdFakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.beginFails {
		return nil, errBDRowsBroken
	}
	switch {
	case c.forceReadOnly:
		c.txReadOnly = true
	case c.ignoreReadOnly:
		c.txReadOnly = false
	default:
		c.txReadOnly = opts.ReadOnly
	}
	c.txIsolation = bdIsolationText(opts.Isolation)
	c.inTx = true
	return &bdFakeTx{conn: c}, nil
}

func bdIsolationText(level driver.IsolationLevel) string {
	switch level {
	case driver.IsolationLevel(sql.LevelSerializable):
		return "serializable"
	case driver.IsolationLevel(sql.LevelRepeatableRead):
		return "repeatable read"
	default:
		return "read committed"
	}
}

func (c *bdFakeConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, int64, float64, bool, []byte, string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("bd fake driver: 不支持的参数类型 %T", typed)
	}
}

func (c *bdFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.inTx {
		// 生产实现的所有写入都在事务内；事务外写入按故障处理。
		return nil, errors.New("bd fake driver: 事务外写入被拒绝")
	}
	if err := c.catalog.exec(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *bdFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.rowsFailFragment != "" && strings.Contains(query, c.rowsFailFragment) {
		return &bdErrorRows{columns: bdErrorColumns(query), err: errBDRowsBroken, errFirst: c.rowsFailImmediate}, nil
	}
	result, err := c.catalog.query(c.txReadOnly, c.txIsolation, query, args)
	if err != nil {
		return nil, err
	}
	return &bdFakeRows{result: result}, nil
}

var errBDRowsBroken = errors.New("bdPGCatalog: 行迭代中途故障")

type bdFakeTx struct{ conn *bdFakeConn }

func (t *bdFakeTx) Commit() error {
	if t.conn.commitFails {
		t.conn.inTx = false
		t.conn.catalog.mu.Lock()
		t.conn.catalog.rollbacks++
		t.conn.catalog.mu.Unlock()
		return errBDRowsBroken
	}
	t.conn.inTx = false
	t.conn.catalog.mu.Lock()
	t.conn.catalog.commits++
	t.conn.catalog.mu.Unlock()
	return nil
}

func (t *bdFakeTx) Rollback() error {
	t.conn.inTx = false
	t.conn.catalog.mu.Lock()
	t.conn.catalog.rollbacks++
	t.conn.catalog.mu.Unlock()
	return nil
}

type bdFakeStmt struct {
	conn  *bdFakeConn
	query string
}

func (s *bdFakeStmt) Close() error  { return nil }
func (s *bdFakeStmt) NumInput() int { return -1 }

func (s *bdFakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, bdNamedArgs(args))
}

func (s *bdFakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, bdNamedArgs(args))
}

func bdNamedArgs(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, 0, len(args))
	for index, value := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: value})
	}
	return named
}

// openBDFakePG 打开一个指向目录模拟器的 *sql.DB；forceReadOnly/ignoreReadOnly
// 从目录上的注入字段读取，模拟两类真实事务模式故障。
func openBDFakePG(catalog *bdPGCatalog) *sql.DB {
	return sql.OpenDB(bdFakeConnector{catalog: catalog, forceReadOnly: catalog.forceReadOnly, ignoreReadOnly: catalog.ignoreReadOnly})
}

// bdBuildWhitelistCatalog 构造 40 表 whitelist 就绪目录：默认三列 + 各表特化
// 列/主键/外键/序列，形状与真实 DDL 的拓扑同构。
func bdBuildWhitelistCatalog(c *bdPGCatalog) {
	base := []bdFakeColumn{
		{name: "id", dataType: "text", nullable: false},
		{name: "name", dataType: "text", nullable: true},
		{name: "created_at", dataType: "text", nullable: false},
	}
	for _, name := range contracts.BusinessDatasetTables {
		c.addTable(name, append([]bdFakeColumn(nil), base...), []string{"id"})
	}
	// protocols：code/version 特化列（provider_protocol_profiles 复合外键目标）。
	c.addColumn("protocols", bdFakeColumn{name: "code", dataType: "text", nullable: false})
	c.addColumn("protocols", bdFakeColumn{name: "version", dataType: "text", nullable: false})
	// providers：code 列 + parent_code 自引用（引用 code 而非主键）。
	c.addColumn("providers", bdFakeColumn{name: "code", dataType: "text", nullable: false})
	c.addColumn("providers", bdFakeColumn{name: "parent_code", dataType: "text", nullable: true})
	c.addForeignKey("providers", "providers_parent_code_fk", []string{"parent_code"}, "providers", []string{"code"})
	// provider_protocol_profiles：复合外键列指向 protocols。
	for _, column := range []bdFakeColumn{
		{name: "protocol_code", dataType: "text", nullable: false},
		{name: "protocol_version", dataType: "text", nullable: false},
		{name: "provider_code", dataType: "text", nullable: false},
	} {
		c.addColumn("provider_protocol_profiles", column)
	}
	c.addForeignKey("provider_protocol_profiles", "ppp_provider_fk", []string{"provider_code"}, "providers", []string{"code"})
	c.addForeignKey("provider_protocol_profiles", "ppp_protocol_fk", []string{"protocol_code", "protocol_version"}, "protocols", []string{"code", "version"})
	// provider_protocol_profile_families：复合主键 + 指向 profile，无序列。
	families := c.tables["provider_protocol_profile_families"]
	families.columns = []bdFakeColumn{
		{name: "profile_id", dataType: "text", nullable: false},
		{name: "family_code", dataType: "text", nullable: false},
		{name: "created_at", dataType: "text", nullable: false},
	}
	families.primaryKey = []string{"profile_id", "family_code"}
	c.addForeignKey("provider_protocol_profile_families", "pppf_profile_fk", []string{"profile_id"}, "provider_protocol_profiles", []string{"id"})
	// accounts → providers / system_accounts。
	c.addColumn("accounts", bdFakeColumn{name: "provider_code", dataType: "text", nullable: true})
	c.addColumn("accounts", bdFakeColumn{name: "system_account_id", dataType: "text", nullable: false})
	c.addForeignKey("accounts", "accounts_provider_fk", []string{"provider_code"}, "providers", []string{"code"})
	c.addForeignKey("accounts", "accounts_system_account_fk", []string{"system_account_id"}, "system_accounts", []string{"id"})
	// group_accounts → groups / accounts。
	c.addColumn("group_accounts", bdFakeColumn{name: "group_id", dataType: "text", nullable: false})
	c.addColumn("group_accounts", bdFakeColumn{name: "account_id", dataType: "text", nullable: false})
	c.addForeignKey("group_accounts", "group_accounts_group_fk", []string{"group_id"}, "groups", []string{"id"})
	c.addForeignKey("group_accounts", "group_accounts_account_fk", []string{"account_id"}, "accounts", []string{"id"})
	// resource_authorization_sources → resource_authorizations。
	c.addColumn("resource_authorization_sources", bdFakeColumn{name: "authorization_id", dataType: "text", nullable: false})
	c.addForeignKey("resource_authorization_sources", "ras_authorization_fk", []string{"authorization_id"}, "resource_authorizations", []string{"id"})
	// 序列：api_keys 单列主键带序列；其余单列主键表无序列（pg_get_serial_sequence
	// 返回 NULL 即跳过）。
	c.setSequence("api_keys", "juhe_business.api_keys_id_seq", 100)
}

// bdSeedSourceRows 在 whitelist 目录中填充满足外键关系的迷你业务数据集。
// providers 子行 id 刻意小于父行 id，使文件（主键）顺序与外键插入顺序相反。
func bdSeedSourceRows(c *bdPGCatalog) {
	c.seedRow("system_accounts", map[string]driver.Value{"id": "sa-1", "name": "root", "created_at": "2026-01-01T00:00:00Z"})
	c.seedRow("providers", map[string]driver.Value{"id": "a-child", "name": "child", "created_at": "2026-01-02T00:00:00Z", "code": "prov-child", "parent_code": "prov-a"})
	c.seedRow("providers", map[string]driver.Value{"id": "z-parent", "name": "parent", "created_at": "2026-01-01T00:00:00Z", "code": "prov-a", "parent_code": nil})
	c.seedRow("protocols", map[string]driver.Value{"id": "pr-1", "name": "openai protocol", "created_at": "2026-01-01T00:00:00Z", "code": "openai", "version": "v1"})
	c.seedRow("provider_protocol_profiles", map[string]driver.Value{"id": "ppp-1", "name": "profile", "created_at": "2026-01-01T00:00:00Z", "protocol_code": "openai", "protocol_version": "v1", "provider_code": "prov-a"})
	c.seedRow("provider_protocol_profile_families", map[string]driver.Value{"profile_id": "ppp-1", "family_code": "chat", "created_at": "2026-01-01T00:00:00Z"})
	c.seedRow("accounts", map[string]driver.Value{"id": "acc-1", "name": "account", "created_at": "2026-01-01T00:00:00Z", "provider_code": "prov-a", "system_account_id": "sa-1"})
	c.seedRow("groups", map[string]driver.Value{"id": "g-1", "name": "group", "created_at": "2026-01-01T00:00:00Z"})
	c.seedRow("group_accounts", map[string]driver.Value{"id": "ga-1", "name": "binding", "created_at": "2026-01-01T00:00:00Z", "group_id": "g-1", "account_id": "acc-1"})
	c.seedRow("resource_authorizations", map[string]driver.Value{"id": "ra-1", "name": "authz", "created_at": "2026-01-01T00:00:00Z"})
	c.seedRow("resource_authorization_sources", map[string]driver.Value{"id": "ras-1", "name": "source", "created_at": "2026-01-01T00:00:00Z", "authorization_id": "ra-1"})
	// api_keys 承载整型主键 + 序列语义（setval 断言目标）。
	c.seedRow("api_keys", map[string]driver.Value{"id": int64(41), "name": "key", "created_at": "2026-01-01T00:00:00Z"})
	// 其余数据表各补一行通用数据，保证 37 张数据表全链路往返。
	for _, name := range contracts.BusinessDatasetTables {
		if contracts.IsBusinessDatasetStructuralOnlyTable(name) {
			continue
		}
		if c.tables[name] != nil && len(c.tables[name].rows) == 0 {
			c.seedRow(name, map[string]driver.Value{"id": "gen-" + name, "name": "gen", "created_at": "2026-01-01T00:00:00Z"})
		}
	}
}
