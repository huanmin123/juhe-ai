package providerconnectionmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 只读全量拉取 §10 覆盖的 source 表并物化为内存 snapshot 输入。全程 SELECT
// （含 COUNT），不开写事务、不写任何数据库对象。行在加载后立即按主键 tuple
// canonical bytes 排序，保证 manifest 与 snapshot 的确定性。

type sourceColumn struct {
	name    string
	sqlType string
}

type sourceRow struct {
	cells  []Cell
	values map[string]Cell
	pk     []Cell
}

func (r *sourceRow) pkDisplay() []string {
	display := make([]string, 0, len(r.pk))
	for _, cell := range r.pk {
		display = append(display, cell.DisplayValue())
	}
	return display
}

type sourceTable struct {
	def     sourceTableDef
	columns []sourceColumn
	rows    []sourceRow
}

type sourceData struct {
	tables        map[string]*sourceTable
	historyCounts map[string]int
	// historyMissing 记录清点时物理缺失的历史表（按 0 行计并写 manifest note）。
	historyMissing []string
}

func (s *sourceData) table(name string) *sourceTable {
	return s.tables[name]
}

// findRow 按主键第一列值定位行（配置表主键首列均为 id 或复合键首列；加载时
// 已按主键排序，量级为存量配置行数，线性查找可接受）。
func (t *sourceTable) findRow(pkFirst string) *sourceRow {
	for index := range t.rows {
		if len(t.rows[index].pk) > 0 && t.rows[index].pk[0].DisplayValue() == pkFirst {
			return &t.rows[index]
		}
	}
	return nil
}

// snapshot 从已加载数据构建 canonical snapshot 输入（列 ordinal 与 SQL 类型
// 取自物理表，主键声明取自本包定义）。
func (s *sourceData) snapshot() *Snapshot {
	names := make([]string, 0, len(s.tables))
	for name := range s.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	snapshot := &Snapshot{SchemaVersion: SourceSchemaVersion}
	for _, name := range names {
		table := s.tables[name]
		columns := make([]SnapshotColumn, 0, len(table.columns))
		for _, column := range table.columns {
			columns = append(columns, SnapshotColumn{Name: column.name, SQLType: column.sqlType})
		}
		rows := make([][]Cell, 0, len(table.rows))
		for index := range table.rows {
			rows = append(rows, table.rows[index].cells)
		}
		snapshot.Tables = append(snapshot.Tables, SnapshotTable{
			Name:       name,
			Columns:    columns,
			PrimaryKey: table.def.pk,
			Rows:       rows,
		})
	}
	return snapshot
}

// loadSource 全量只读加载 11 张配置表并按表清点历史行。
func loadSource(ctx context.Context, db *sql.DB) (*sourceData, error) {
	data := &sourceData{tables: map[string]*sourceTable{}, historyCounts: map[string]int{}}
	for _, def := range configSourceTables {
		table, err := loadConfigTable(ctx, db, def)
		if err != nil {
			return nil, err
		}
		data.tables[def.name] = table
	}
	for _, def := range historySourceTables {
		count, missing, err := countHistoryTable(ctx, db, def)
		if err != nil {
			return nil, err
		}
		data.historyCounts[def.name] = count
		if missing {
			data.historyMissing = append(data.historyMissing, def.name)
		}
	}
	return data, nil
}

func loadConfigTable(ctx context.Context, db *sql.DB, def sourceTableDef) (*sourceTable, error) {
	query := fmt.Sprintf(`SELECT * FROM %s`, qualifiedName(def))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("只读拉取 %s 失败: %w", def.name, err)
	}
	defer rows.Close()
	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 列清单失败: %w", def.name, err)
	}
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("读取 %s 列类型失败: %w", def.name, err)
	}
	columnIndex := map[string]int{}
	columns := make([]sourceColumn, 0, len(columnNames))
	for ordinal, name := range columnNames {
		columnIndex[name] = ordinal
		columns = append(columns, sourceColumn{name: name, sqlType: columnTypes[ordinal].DatabaseTypeName()})
	}
	for _, required := range def.required {
		if _, present := columnIndex[required]; !present {
			return nil, fmt.Errorf("源表 %s 缺少迁移必需列 %q（旧 schema 契约见 providerconnectionmigration 包定义）", def.name, required)
		}
	}
	table := &sourceTable{def: def, columns: columns}
	for rows.Next() {
		rawValues := make([]any, len(columnNames))
		pointers := make([]any, len(columnNames))
		for index := range rawValues {
			pointers[index] = &rawValues[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("读取 %s 行失败: %w", def.name, err)
		}
		cells := make([]Cell, 0, len(rawValues))
		for index, raw := range rawValues {
			cell, err := classifyDriverValue(raw)
			if err != nil {
				return nil, fmt.Errorf("表 %s 列 %s: %w", def.name, columnNames[index], err)
			}
			cells = append(cells, cell)
		}
		values := map[string]Cell{}
		for ordinal, cell := range cells {
			values[columnNames[ordinal]] = cell
		}
		pk := make([]Cell, 0, len(def.pk))
		for _, name := range def.pk {
			pk = append(pk, values[name])
		}
		table.rows = append(table.rows, sourceRow{cells: cells, values: values, pk: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 %s 失败: %w", def.name, err)
	}
	// 行按主键 tuple canonical bytes 排序，保证确定性（§10 row 排序规则的
	// 加载侧复用）。
	sort.SliceStable(table.rows, func(i, j int) bool {
		left := canonicalKeyBytes(table.rows[i].pk)
		right := canonicalKeyBytes(table.rows[j].pk)
		return string(left) < string(right)
	})
	return table, nil
}

// countHistoryTable 只做 SELECT COUNT(*)；表缺失（PG 42P01 / SQLite no such
// table）按缺失处理，其余错误原样返回。
func countHistoryTable(ctx context.Context, db *sql.DB, def sourceTableDef) (count int, missing bool, err error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, qualifiedName(def))
	if scanErr := db.QueryRowContext(ctx, query).Scan(&count); scanErr != nil {
		if historyTableMissing(scanErr) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("清点历史表 %s 失败: %w", def.name, scanErr)
	}
	return count, false, nil
}

func historyTableMissing(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "42P01" {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "no such table") || strings.Contains(message, "does not exist")
}

// classifyDriverValue 把 database/sql 扫描出的动态值映射为七种 canonical
// cell（§10 :465）。未知动态类型 fail-closed，不做有损转换。
func classifyDriverValue(value any) (Cell, error) {
	switch typed := value.(type) {
	case nil:
		return NullCell(), nil
	case bool:
		return BoolCell(typed), nil
	case int64:
		return IntegerCell(typed), nil
	case int:
		return IntegerCell(int64(typed)), nil
	case int32:
		return IntegerCell(int64(typed)), nil
	case uint64:
		return UnsignedIntegerCell(typed), nil
	case float64:
		return Real64Cell(typed), nil
	case float32:
		return Real64Cell(float64(typed)), nil
	case time.Time:
		return TimestampCell(typed), nil
	case string:
		return TextCell(typed), nil
	case []byte:
		return BlobCell(typed), nil
	default:
		return Cell{}, fmt.Errorf("无法映射的驱动值类型 %T（canonical snapshot 只接受 NULL/TEXT/INTEGER/REAL/BOOLEAN/BLOB/timestamp）", value)
	}
}

// canonicalKeyBytes 渲染主键 tuple 的 canonical bytes（行排序键）。
func canonicalKeyBytes(pk []Cell) []byte {
	cells := make([]canonicalValue, 0, len(pk))
	for _, cell := range pk {
		cells = append(cells, cell.canonicalJSON())
	}
	var builder strings.Builder
	canonicalArray(cells).encode(&builder)
	return []byte(builder.String())
}
