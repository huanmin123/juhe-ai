package j3bmodelcheck

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// wmPGCatalog 是面向 j3bmodelcheck PG 链路的内存目录模拟器。它只实现
// BackfillPostgres / VerifyPostgresBackfill / Run / inspectTx 实际使用的
// information_schema / pg_catalog 查询族与 INSERT 行为：
//   - 目录状态（schema/table/列/主键/索引/约束/行）由测试显式构造；
//   - 事务配置（read only / isolation）按 driver.TxOptions 如实回报给 SHOW
//     transaction_read_only / SHOW transaction_isolation，使事务模式守卫成为
//     真实断言而非脚本回放；
//   - ExecContext 收到 juhe_j3b DDL 时安装契约就绪目录，模拟 DDL 生效。
//
// 已知简化：回滚不撤销已写入的行；to_jsonb 渲染不做 JSON 引号转义（两侧渲染
// 一致，digest 对比语义不变）。真实 PG 行为另有 env 门控 smoke 覆盖。
type wmPGCatalog struct {
	mu            sync.Mutex
	schemas       map[string]map[string]*wmPGTable
	owners        map[string]string
	database      string
	currentUser   string
	execCount     int
	injectedFails []string
}

type wmPGTable struct {
	columns     []wmPGColumnSpec
	primaryKeys []string
	indexes     map[string]string
	constraints []string
	rows        []map[string]driver.Value
}

type wmPGColumnSpec struct {
	name     string
	dataType string
	udtName  string
	nullable bool
}

func newWMpgCatalog() *wmPGCatalog {
	return &wmPGCatalog{
		schemas:     map[string]map[string]*wmPGTable{},
		owners:      map[string]string{},
		database:    "wm_j3b_db",
		currentUser: "juhe_maintenance",
	}
}

func (c *wmPGCatalog) ensureSchema(name, owner string) {
	if _, ok := c.schemas[name]; !ok {
		c.schemas[name] = map[string]*wmPGTable{}
	}
	if owner != "" {
		c.owners[name] = owner
	}
}

// wmContractTargetCatalog 依据 contracts 契约构造契约就绪的 juhe_j3b 目录。
// 列形状来自 J3BModelCheckColumns，索引/约束定义直接采用契约期望串（inspect
// 逻辑按 contains 匹配），主键复用 SQLite 契约的同名表主键（两条路径共用同
// 一份表名单）。
func wmContractTargetCatalog(c *wmPGCatalog) {
	c.ensureSchema(SchemaName, c.currentUser)
	for _, tableName := range contracts.J3BModelCheckTables {
		columnSpecs := contracts.J3BModelCheckColumns[tableName]
		names := make([]string, 0, len(columnSpecs))
		for name := range columnSpecs {
			names = append(names, name)
		}
		sort.Strings(names)
		table := &wmPGTable{indexes: map[string]string{}}
		for _, name := range names {
			spec := columnSpecs[name]
			table.columns = append(table.columns, wmPGColumnSpec{name: name, dataType: spec.DataType, udtName: spec.UdtName, nullable: spec.Nullable})
		}
		if pks, ok := sqliteRequiredPrimaryKeys[tableName]; ok {
			table.primaryKeys = append([]string(nil), pks...)
		}
		for indexName, definition := range contracts.J3BModelCheckIndexes {
			if strings.Contains(definition, "on "+SchemaName+"."+tableName) {
				table.indexes[indexName] = definition
			}
		}
		table.constraints = append([]string(nil), contracts.J3BModelCheckConstraints[tableName]...)
		c.schemas[SchemaName][tableName] = table
	}
}

// wmAddLegacyTable 在 legacy schema 中加入指定列规格的事实表。
func (c *wmPGCatalog) wmAddLegacyTable(schema, table string, columns []wmPGColumnSpec, primaryKeys []string) {
	c.ensureSchema(schema, "node_legacy")
	c.schemas[schema][table] = &wmPGTable{
		columns:     append([]wmPGColumnSpec(nil), columns...),
		primaryKeys: append([]string(nil), primaryKeys...),
		indexes:     map[string]string{},
	}
}

// wmSeedLegacyRows 用确定性字符串/整数填充一张表（值由列名+行号推导），供
// backfill 复制与 digest 对比。
func (c *wmPGCatalog) wmSeedLegacyRows(schema, table string, rowCount int) {
	table2 := c.schemas[schema][table]
	for row := 1; row <= rowCount; row++ {
		values := make(map[string]driver.Value, len(table2.columns))
		for _, column := range table2.columns {
			switch column.udtName {
			case "int8", "int4", "integer", "bigint":
				values[column.name] = int64(row)
			default:
				values[column.name] = fmt.Sprintf("wm-%s-r%d", column.name, row)
			}
		}
		table2.rows = append(table2.rows, values)
	}
}

// wmLegacyColumnsOf 返回目标契约表中同名列规格（legacy 与目标形状一致即满足
// 完整投影校验）。
func wmLegacyColumnsOf(c *wmPGCatalog, table string) []wmPGColumnSpec {
	return append([]wmPGColumnSpec(nil), c.schemas[SchemaName][table].columns...)
}

// wmPopulateAllLegacySources 为全部白名单事实表建立 legacy 形状、填充行，并
// 建立 trust 游标源表（形状必须与 validatePostgresTrustAggregationStateColumns
// 期望完全一致）。
func (c *wmPGCatalog) wmPopulateAllLegacySources(rowsPerTable int) {
	for _, item := range postgresLegacyJ3bFactTables {
		columns := wmLegacyColumnsOf(c, item.name)
		var pks []string
		if required, ok := sqliteRequiredPrimaryKeys[item.name]; ok {
			pks = required
		}
		c.wmAddLegacyTable(item.sourceSchema, item.name, columns, pks)
		if rowsPerTable > 0 {
			c.wmSeedLegacyRows(item.sourceSchema, item.name, rowsPerTable)
		}
	}
	c.wmAddLegacyTable("juhe_stats", "stats_job_state", []wmPGColumnSpec{
		{name: "scope_type", dataType: "text", udtName: "text"},
		{name: "scope_id", dataType: "text", udtName: "text"},
		{name: "job_name", dataType: "text", udtName: "text"},
		{name: "cursor_created_at", dataType: "text", udtName: "text", nullable: true},
		{name: "cursor_id", dataType: "text", udtName: "text", nullable: true},
		{name: "last_success_at", dataType: "text", udtName: "text", nullable: true},
		{name: "last_error_message", dataType: "text", udtName: "text", nullable: true},
		{name: "lag_seconds", dataType: "integer", udtName: "int4", nullable: true},
		{name: "updated_at", dataType: "text", udtName: "text"},
	}, []string{"scope_type", "scope_id", "job_name"})
	c.schemas["juhe_stats"]["stats_job_state"].rows = append(c.schemas["juhe_stats"]["stats_job_state"].rows, map[string]driver.Value{
		"scope_type":         trustAggregationStateScopeType,
		"scope_id":           "",
		"job_name":           trustAggregationStateJobName,
		"cursor_created_at":  "2026-08-27T10:00:00Z",
		"cursor_id":          "obs-1",
		"last_success_at":    "2026-08-27T10:01:00Z",
		"last_error_message": nil,
		"lag_seconds":        int64(3),
		"updated_at":         "2026-08-27T10:01:00Z",
	})
}

// wmDropSourceTable 模拟 legacy 源表缺失。
func (c *wmPGCatalog) wmDropSourceTable(schema, table string) {
	delete(c.schemas[schema], table)
}

// wmMutateTargetColumn 篡改一个目标列形状，验证 inspect 契约校验失败闭环。
func (c *wmPGCatalog) wmMutateTargetColumn(table, column, dataType string) {
	for index := range c.schemas[SchemaName][table].columns {
		if c.schemas[SchemaName][table].columns[index].name == column {
			c.schemas[SchemaName][table].columns[index].dataType = dataType
			return
		}
	}
}

// wmMutateTargetOwner 把 schema owner 改成非当前角色。
func (c *wmPGCatalog) wmMutateTargetOwner(owner string) {
	c.owners[SchemaName] = owner
}

var (
	wmSelectSplit = regexp.MustCompile(`(?is)^SELECT\s+(.*?)\s+FROM\s+(.+)$`)
	wmWhereSplit  = regexp.MustCompile(`(?is)\sWHERE\s`)
	wmOrderSplit  = regexp.MustCompile(`(?is)\sORDER\s+BY\s`)
	wmLimitSplit  = regexp.MustCompile(`(?is)\sLIMIT\s+(\$\d+|\d+)\s*$`)
	wmJSONExpr    = regexp.MustCompile(`to_jsonb\(([^)]+?)\)::text`)
	wmWherePred   = regexp.MustCompile(`"?([A-Za-z_][A-Za-z0-9_]*)"?\s*=\s*\$(\d+)`)
	wmQuotedIdent = regexp.MustCompile(`"([^"]+)"`)
	wmInsertStmt  = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+(.+?)\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)\s*$`)
)

func wmStripQuotes(value string) string {
	return strings.ReplaceAll(value, `"`, "")
}

func wmSplitQualified(reference string) (string, string) {
	parts := strings.SplitN(wmStripQuotes(reference), ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", parts[0]
}

func wmArgText(value driver.Value) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprintf("%v", value)
}

func wmArgStringList(value driver.Value) []string {
	if typed, ok := value.([]string); ok {
		return typed
	}
	return nil
}

func wmFirstToken(fromClause string) string {
	fields := strings.Fields(strings.TrimSpace(fromClause))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// wmSplitTopLevel 按顶层逗号切分 SELECT 投影（to_jsonb(col1,col2) 内部不切）。
func wmSplitTopLevel(expression string) []string {
	var parts []string
	depth := 0
	var current strings.Builder
	for _, r := range expression {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteRune(r)
	}
	parts = append(parts, current.String())
	return parts
}

// wmQuery 在目录上执行一次查询模拟。
func (c *wmPGCatalog) wmQuery(query string, args []driver.NamedValue) (*wmResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.wmInjectFailure(query); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(query, "current_database()"):
		return &wmResult{columns: []string{"current_database", "current_user"}, rows: [][]driver.Value{{c.database, c.currentUser}}}, nil
	case strings.Contains(query, "pg_get_userbyid(nspowner)"):
		owner, ok := c.owners[wmArgText(args[0].Value)]
		if !ok {
			return &wmResult{columns: []string{"owner"}}, nil
		}
		return &wmResult{columns: []string{"owner"}, rows: [][]driver.Value{{owner}}}, nil
	case strings.Contains(query, "information_schema.tables") && strings.Contains(query, "EXISTS"):
		_, ok := c.schemas[wmArgText(args[0].Value)][wmArgText(args[1].Value)]
		return &wmResult{columns: []string{"exists"}, rows: [][]driver.Value{{ok}}}, nil
	case strings.Contains(query, "information_schema.tables"):
		schemaName := wmArgText(args[0].Value)
		var rows [][]driver.Value
		for _, name := range wmArgStringList(args[1].Value) {
			if _, ok := c.schemas[schemaName][name]; ok {
				rows = append(rows, []driver.Value{name})
			}
		}
		return &wmResult{columns: []string{"table_name"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.columns") && strings.Contains(query, "ANY("):
		schemaName := wmArgText(args[0].Value)
		var rows [][]driver.Value
		for _, tableName := range wmArgStringList(args[1].Value) {
			table := c.schemas[schemaName][tableName]
			if table == nil {
				continue
			}
			for _, column := range table.columns {
				rows = append(rows, []driver.Value{tableName, column.name, column.dataType, column.udtName, wmNullableText(column.nullable)})
			}
		}
		return &wmResult{columns: []string{"table_name", "column_name", "data_type", "udt_name", "is_nullable"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.columns") && strings.Contains(query, "column_default"):
		// postgresBackfillColumns 的六列形状（含 default/identity）。
		schemaName := wmArgText(args[0].Value)
		tableName := wmArgText(args[1].Value)
		table := c.schemas[schemaName][tableName]
		var rows [][]driver.Value
		if table != nil {
			for _, column := range table.columns {
				rows = append(rows, []driver.Value{column.name, column.dataType, column.udtName, wmNullableText(column.nullable), nil, "NO"})
			}
		}
		return &wmResult{columns: []string{"column_name", "data_type", "udt_name", "is_nullable", "column_default", "is_identity"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.columns"):
		// postgresColumns 的单列形状（只取列名）。
		schemaName := wmArgText(args[0].Value)
		tableName := wmArgText(args[1].Value)
		table := c.schemas[schemaName][tableName]
		var rows [][]driver.Value
		if table != nil {
			for _, column := range table.columns {
				rows = append(rows, []driver.Value{column.name})
			}
		}
		return &wmResult{columns: []string{"column_name"}, rows: rows}, nil
	case strings.Contains(query, "pg_indexes"):
		schemaName := wmArgText(args[0].Value)
		var rows [][]driver.Value
		for _, table := range c.schemas[schemaName] {
			names := make([]string, 0, len(table.indexes))
			for name := range table.indexes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				rows = append(rows, []driver.Value{name, table.indexes[name]})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return wmRenderCell(rows[i][0]) < wmRenderCell(rows[j][0]) })
		return &wmResult{columns: []string{"indexname", "indexdef"}, rows: rows}, nil
	case strings.Contains(query, "pg_constraint"):
		schemaName := wmArgText(args[0].Value)
		var rows [][]driver.Value
		for tableName, table := range c.schemas[schemaName] {
			for _, definition := range table.constraints {
				rows = append(rows, []driver.Value{tableName, definition})
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			left, right := wmRenderCell(rows[i][0]), wmRenderCell(rows[j][0])
			if left != right {
				return left < right
			}
			return wmRenderCell(rows[i][1]) < wmRenderCell(rows[j][1])
		})
		return &wmResult{columns: []string{"relname", "constraintdef"}, rows: rows}, nil
	case strings.Contains(query, "pg_index"):
		schemaName := wmArgText(args[0].Value)
		tableName := wmArgText(args[1].Value)
		var rows [][]driver.Value
		if table := c.schemas[schemaName][tableName]; table != nil {
			for _, key := range table.primaryKeys {
				rows = append(rows, []driver.Value{key})
			}
		}
		return &wmResult{columns: []string{"attname"}, rows: rows}, nil
	case strings.Contains(query, "to_jsonb("):
		return c.wmEvidenceQuery(query, args)
	default:
		return c.wmPlainSelect(query, args)
	}
}

func wmNullableText(nullable bool) string {
	if nullable {
		return "YES"
	}
	return "NO"
}

// wmEvidenceQuery 处理 to_jsonb(col)::text 证据投影；to_jsonb($n) 解析为参数值。
func (c *wmPGCatalog) wmEvidenceQuery(query string, args []driver.NamedValue) (*wmResult, error) {
	rest := wmSelectSplit.FindStringSubmatch(strings.TrimSpace(query))
	if rest == nil {
		return nil, fmt.Errorf("wmPGCatalog: 无法解析证据查询 %.120s", query)
	}
	projectionExprs := wmSplitTopLevel(rest[1])
	matches := wmJSONExpr.FindAllStringSubmatch(projectionExprs[0], -1)
	for _, extra := range projectionExprs[1:] {
		matches = append(matches, wmJSONExpr.FindAllStringSubmatch(extra, -1)...)
	}
	columns := make([]string, 0, len(matches))
	type evidenceSource struct {
		argIndex int
		column   string
	}
	sources := make([]evidenceSource, 0, len(matches))
	for _, match := range matches {
		inner := strings.TrimSpace(match[1])
		if strings.HasPrefix(inner, "$") {
			index, _ := strconv.Atoi(strings.TrimPrefix(strings.Split(inner, "::")[0], "$"))
			columns = append(columns, "param")
			sources = append(sources, evidenceSource{argIndex: index})
			continue
		}
		name := wmStripQuotes(strings.Split(inner, "::")[0])
		columns = append(columns, name)
		sources = append(sources, evidenceSource{column: name})
	}
	schemaName, tableName := wmSplitQualified(wmFirstToken(rest[2]))
	table := c.schemas[schemaName][tableName]
	if table == nil {
		return &wmResult{columns: columns}, nil
	}
	rows := wmFilterRows(table, rest[2], args)
	rows = wmSortRows(rows, rest[2], args)
	rows = wmApplyLimit(rows, rest[2], args)
	out := make([][]driver.Value, 0, len(rows))
	for _, row := range rows {
		values := make([]driver.Value, 0, len(sources))
		for _, source := range sources {
			if source.argIndex > 0 {
				values = append(values, args[source.argIndex-1].Value)
				continue
			}
			values = append(values, row[source.column])
		}
		out = append(out, values)
	}
	return &wmResult{columns: columns, rows: out}, nil
}

// wmPlainSelect 处理 SELECT 列 FROM schema.table [WHERE ...] [ORDER BY ...] [LIMIT ...]。
func (c *wmPGCatalog) wmPlainSelect(query string, args []driver.NamedValue) (*wmResult, error) {
	rest := wmSelectSplit.FindStringSubmatch(strings.TrimSpace(query))
	if rest == nil {
		return nil, fmt.Errorf("wmPGCatalog: 不支持的查询 %.120s", query)
	}
	projectionExprs := wmSplitTopLevel(rest[1])
	schemaName, tableName := wmSplitQualified(wmFirstToken(rest[2]))
	table := c.schemas[schemaName][tableName]
	columns := make([]string, 0, len(projectionExprs))
	for _, expression := range projectionExprs {
		columns = append(columns, wmStripQuotes(strings.TrimSpace(expression)))
	}
	if table == nil {
		return &wmResult{columns: columns}, nil
	}
	rows := wmFilterRows(table, rest[2], args)
	rows = wmSortRows(rows, rest[2], args)
	rows = wmApplyLimit(rows, rest[2], args)
	out := make([][]driver.Value, 0, len(rows))
	for _, row := range rows {
		values := make([]driver.Value, 0, len(columns))
		for _, column := range columns {
			values = append(values, row[column])
		}
		out = append(out, values)
	}
	return &wmResult{columns: columns, rows: out}, nil
}

func wmFilterRows(table *wmPGTable, fromRemainder string, args []driver.NamedValue) []map[string]driver.Value {
	rows := make([]map[string]driver.Value, 0, len(table.rows))
	for _, row := range table.rows {
		if match := wmWhereSplit.FindStringIndex(fromRemainder); match != nil {
			if !wmRowMatches(row, fromRemainder[match[1]:], args) {
				continue
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func wmRowMatches(row map[string]driver.Value, whereClause string, args []driver.NamedValue) bool {
	for _, match := range wmWherePred.FindAllStringSubmatch(whereClause, -1) {
		index, _ := strconv.Atoi(match[2])
		if wmRenderCell(row[match[1]]) != wmRenderCell(args[index-1].Value) {
			return false
		}
	}
	return true
}

func wmSortRows(rows []map[string]driver.Value, fromRemainder string, args []driver.NamedValue) []map[string]driver.Value {
	orderMatch := wmOrderSplit.FindStringIndex(fromRemainder)
	if orderMatch == nil {
		return rows
	}
	rest := fromRemainder[orderMatch[1]:]
	if limitMatch := wmLimitSplit.FindStringIndex(rest); limitMatch != nil {
		rest = rest[:limitMatch[0]]
	}
	var keys []string
	for _, match := range wmQuotedIdent.FindAllStringSubmatch(rest, -1) {
		keys = append(keys, match[1])
	}
	if len(keys) == 0 {
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, key := range keys {
			left, right := wmRenderCell(rows[i][key]), wmRenderCell(rows[j][key])
			if left != right {
				return left < right
			}
		}
		return false
	})
	return rows
}

func wmApplyLimit(rows []map[string]driver.Value, fromRemainder string, args []driver.NamedValue) []map[string]driver.Value {
	match := wmLimitSplit.FindStringSubmatch(fromRemainder)
	if match == nil {
		return rows
	}
	var limit int64
	if strings.HasPrefix(match[1], "$") {
		index, _ := strconv.Atoi(strings.TrimPrefix(match[1], "$"))
		if value, ok := args[index-1].Value.(int64); ok {
			limit = value
		}
	} else {
		limit, _ = strconv.ParseInt(match[1], 10, 64)
	}
	if limit > 0 && limit < int64(len(rows)) {
		return rows[:limit]
	}
	return rows
}

func wmRenderCell(value driver.Value) string {
	switch typed := value.(type) {
	case nil:
		return "\x00null"
	case []byte:
		return string(typed)
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func (c *wmPGCatalog) wmExec(query string, args []driver.NamedValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.wmInjectFailure(query); err != nil {
		return err
	}
	c.execCount++
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(trimmed, "SET ") || strings.HasPrefix(trimmed, "PRAGMA") {
		return nil
	}
	if strings.Contains(query, "pg_advisory_xact_lock") || strings.Contains(query, "set_config(") {
		return nil
	}
	// juhe_j3b DDL：模拟 DDL 生效，安装契约就绪目录。
	if strings.Contains(query, "CREATE TABLE IF NOT EXISTS "+SchemaName+".") {
		wmContractTargetCatalog(c)
		return nil
	}
	if match := wmInsertStmt.FindStringSubmatch(trimmed); match != nil {
		schemaName, tableName := wmSplitQualified(match[1])
		table := c.schemas[schemaName][tableName]
		if table == nil {
			return fmt.Errorf("wmPGCatalog: INSERT 目标表 %s.%s 不存在", schemaName, tableName)
		}
		columnNames := strings.Split(match[2], ",")
		row := make(map[string]driver.Value, len(columnNames))
		for index, rawColumn := range columnNames {
			var value driver.Value
			if index < len(args) {
				value = args[index].Value
			}
			row[wmStripQuotes(strings.TrimSpace(rawColumn))] = value
		}
		table.rows = append(table.rows, row)
		return nil
	}
	return nil
}

func (c *wmPGCatalog) wmInjectFailure(query string) error {
	for _, fragment := range c.injectedFails {
		if strings.Contains(query, fragment) {
			return fmt.Errorf("wmPGCatalog: 注入故障 %q", fragment)
		}
	}
	return nil
}

// wmResult 与驱动接口实现。

type wmResult struct {
	columns []string
	rows    [][]driver.Value
}

type wmRows struct {
	result *wmResult
	pos    int
}

func (r *wmRows) Columns() []string { return r.result.columns }
func (r *wmRows) Close() error      { return nil }
func (r *wmRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.result.rows) {
		return io.EOF
	}
	copy(dest, r.result.rows[r.pos])
	r.pos++
	return nil
}

// wmFakeConnector.ignoreReadOnly 模拟“数据库默认只读、客户端选项不生效”的
// 真实故障；forceReadOnly 模拟角色只读事务导致写入路径必须失败闭环。
type wmFakeConnector struct {
	catalog        *wmPGCatalog
	ignoreReadOnly bool
	forceReadOnly  bool
}

func (c wmFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmFakeConn{catalog: c.catalog, ignoreReadOnly: c.ignoreReadOnly, forceReadOnly: c.forceReadOnly, txIsolation: "read committed"}, nil
}

func (c wmFakeConnector) Driver() driver.Driver { return wmFakeDriver{} }

type wmFakeDriver struct{}

func (wmFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm fake driver: 请使用 sql.OpenDB")
}

type wmFakeConn struct {
	catalog        *wmPGCatalog
	ignoreReadOnly bool
	forceReadOnly  bool
	txReadOnly     bool
	txIsolation    string
}

func (c *wmFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &wmFakeStmt{conn: c, query: query}, nil
}

func (c *wmFakeConn) Close() error { return nil }

func (c *wmFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm fake driver: Begin 不应被调用（走 BeginTx）")
}

func (c *wmFakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	switch {
	case c.forceReadOnly:
		c.txReadOnly = true
	case c.ignoreReadOnly:
		c.txReadOnly = false
	default:
		c.txReadOnly = opts.ReadOnly
	}
	c.txIsolation = wmIsolationText(opts.Isolation)
	return &wmFakeTx{conn: c}, nil
}

func wmIsolationText(level driver.IsolationLevel) string {
	switch level {
	case driver.IsolationLevel(sql.LevelSerializable):
		return "serializable"
	case driver.IsolationLevel(sql.LevelRepeatableRead):
		return "repeatable read"
	case driver.IsolationLevel(sql.LevelReadUncommitted):
		return "read uncommitted"
	default:
		return "read committed"
	}
}

func (c *wmFakeConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, int64, float64, bool, []byte, string, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm fake driver: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmFakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.catalog.wmExec(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *wmFakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SHOW transaction_read_only") {
		value := "off"
		if c.txReadOnly {
			value = "on"
		}
		return wmRowsFor(&wmResult{columns: []string{"transaction_read_only"}, rows: [][]driver.Value{{value}}}), nil
	}
	if strings.Contains(query, "SHOW transaction_isolation") {
		return wmRowsFor(&wmResult{columns: []string{"transaction_isolation"}, rows: [][]driver.Value{{c.txIsolation}}}), nil
	}
	result, err := c.catalog.wmQuery(query, args)
	if err != nil {
		return nil, err
	}
	return wmRowsFor(result), nil
}

func wmRowsFor(result *wmResult) driver.Rows { return &wmRows{result: result} }

type wmFakeTx struct{ conn *wmFakeConn }

func (t *wmFakeTx) Commit() error   { return nil }
func (t *wmFakeTx) Rollback() error { return nil }

type wmFakeStmt struct {
	conn  *wmFakeConn
	query string
}

func (s *wmFakeStmt) Close() error  { return nil }
func (s *wmFakeStmt) NumInput() int { return -1 }

func (s *wmFakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.ExecContext(context.Background(), s.query, wmNamedArgs(args))
}

func (s *wmFakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.QueryContext(context.Background(), s.query, wmNamedArgs(args))
}

func wmNamedArgs(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, 0, len(args))
	for index, value := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: value})
	}
	return named
}

// openWMFakePG 打开一个指向目录模拟器的 *sql.DB；连接池关闭由调用方负责。
func openWMFakePG(catalog *wmPGCatalog) *sql.DB {
	return sql.OpenDB(wmFakeConnector{catalog: catalog})
}
