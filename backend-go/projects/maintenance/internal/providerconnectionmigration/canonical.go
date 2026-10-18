// Package providerconnectionmigration 实现《供应商与上游协议面重构设计》§10
// 确定性迁移的 dry-run 切片：只读旧 schema 存量业务库，产出可复核 manifest。
//
// 本文件实现 §10（docs/functions/供应商与上游协议面重构设计.md :465）的
// canonical source snapshot 序列化器：
//
//	UTF-8 JSON、无 BOM、无空白缩进；对象键按 UTF-8 字节序排序；
//	字符串不转义非 ASCII，必须转义 "、\ 和 U+0000..U+001F（\b\f\n\r\t 用短写，
//	其余用小写 \u00xx），不转义 /；
//	table 按 UTF-8 字节序排序，column 按源 schema ordinal 排序，
//	row 按完整 primary-key tuple 各 cell 的 compact JSON bytes 做字节序比较，
//	row 内 cell 与 column 顺序一致；
//	每个 cell 用 [type_tag, value] 表示：NULL=["null",null]、TEXT=["text",原字符串]、
//	INTEGER=["integer",十进制字符串]、REAL/DOUBLE=["real64",16 位小写 IEEE-754 bit hex]、
//	BOOLEAN=["boolean",true|false]、BLOB=["blob_base64",标准 Base64]、
//	timestamp=["timestamp_utc",RFC3339Nano UTC 字符串]。
//
// source_snapshot_hash = SHA-256(canonical source snapshot bytes)。
package providerconnectionmigration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CellType 是 snapshot cell 的 type_tag 词表（§10 :465 固定七种）。
type CellType string

const (
	CellNull         CellType = "null"
	CellText         CellType = "text"
	CellInteger      CellType = "integer"
	CellReal64       CellType = "real64"
	CellBoolean      CellType = "boolean"
	CellBlobBase64   CellType = "blob_base64"
	CellTimestampUTC CellType = "timestamp_utc"
)

// Cell 是 canonical snapshot 的单个值。Str 承载 text 原文、integer 十进制、
// real64 的 16 位小写 IEEE-754 bit hex、blob 的标准 Base64 与 timestamp 的
// RFC3339Nano UTC 字符串；Bool 只在 boolean 类型使用。
type Cell struct {
	Type CellType
	Str  string
	Bool bool
}

// NullCell / TextCell / IntegerCell / IntegerDecimalCell / Real64Cell /
// BoolCell / BlobCell / TimestampCell 是七种 cell 的构造器。TimestampCell
// 统一转为 UTC 后按 RFC3339Nano 渲染。
func NullCell() Cell { return Cell{Type: CellNull} }

func TextCell(value string) Cell { return Cell{Type: CellText, Str: value} }

func IntegerCell(value int64) Cell {
	return Cell{Type: CellInteger, Str: strconv.FormatInt(value, 10)}
}

// UnsignedIntegerCell 承载超出 int64 的无符号整数十进制值。
func UnsignedIntegerCell(value uint64) Cell {
	return Cell{Type: CellInteger, Str: strconv.FormatUint(value, 10)}
}

// Real64Cell 渲染为 16 位小写 IEEE-754 bit hex（含 NaN/Inf 的位型，不做数值
// 解释；源库出现 NaN/Inf 时 canonical bytes 仍逐字节稳定）。
func Real64Cell(value float64) Cell {
	return Cell{Type: CellReal64, Str: fmt.Sprintf("%016x", math.Float64bits(value))}
}

func BoolCell(value bool) Cell { return Cell{Type: CellBoolean, Bool: value} }

func BlobCell(value []byte) Cell {
	return Cell{Type: CellBlobBase64, Str: base64.StdEncoding.EncodeToString(value)}
}

func TimestampCell(value time.Time) Cell {
	return Cell{Type: CellTimestampUTC, Str: value.UTC().Format(time.RFC3339Nano)}
}

// DisplayValue 返回 PK/manifest 场景使用的可读字符串（boolean 之外的类型取
// Str；PK 列不含 blob/timestamp 时与原值一致）。
func (c Cell) DisplayValue() string {
	if c.Type == CellBoolean {
		if c.Bool {
			return "true"
		}
		return "false"
	}
	return c.Str
}

// canonicalValue 是 canonical JSON 的最小值树；encode 输出无空白 compact bytes。
type canonicalValue interface {
	encode(builder *strings.Builder)
}

type canonicalNull struct{}

func (canonicalNull) encode(builder *strings.Builder) { builder.WriteString("null") }

type canonicalBool bool

func (v canonicalBool) encode(builder *strings.Builder) {
	if v {
		builder.WriteString("true")
	} else {
		builder.WriteString("false")
	}
}

type canonicalString string

func (v canonicalString) encode(builder *strings.Builder) {
	appendCanonicalString(builder, string(v))
}

type canonicalArray []canonicalValue

func (v canonicalArray) encode(builder *strings.Builder) {
	builder.WriteByte('[')
	for index, item := range v {
		if index > 0 {
			builder.WriteByte(',')
		}
		item.encode(builder)
	}
	builder.WriteByte(']')
}

// canonicalObject 的键在 encode 时按 UTF-8 字节序排序（Go 字符串比较即按字节
// 字典序，与 UTF-8 字节序一致）。
type canonicalObject struct {
	keys   []string
	values map[string]canonicalValue
}

func newCanonicalObject(pairs [][2]any) canonicalObject {
	object := canonicalObject{values: make(map[string]canonicalValue, len(pairs))}
	for _, pair := range pairs {
		key := pair[0].(string)
		if _, duplicate := object.values[key]; duplicate {
			panic(fmt.Sprintf("providerconnectionmigration: canonical object duplicate key %q", key))
		}
		object.keys = append(object.keys, key)
		object.values[key] = pair[1].(canonicalValue)
	}
	sort.Strings(object.keys)
	return object
}

func (v canonicalObject) encode(builder *strings.Builder) {
	builder.WriteByte('{')
	for index, key := range v.keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		appendCanonicalString(builder, key)
		builder.WriteByte(':')
		v.values[key].encode(builder)
	}
	builder.WriteByte('}')
}

// appendCanonicalString 实现 §10 :465 的字符串转义规则：不转义非 ASCII 与
// /；必须转义 "、\ 与 U+0000..U+001F（短写 \b\f\n\r\t，其余小写 \u00xx）。
// 输入含非法 UTF-8 字节时按 Go range 语义替换为 U+FFFD（源库 TEXT 列由驱动
// 保证 UTF-8；二进制载荷必须走 BLOB cell，不得塞进 TEXT）。
func appendCanonicalString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"':
			builder.WriteString(`\"`)
		case r == '\\':
			builder.WriteString(`\\`)
		case r == '\b':
			builder.WriteString(`\b`)
		case r == '\f':
			builder.WriteString(`\f`)
		case r == '\n':
			builder.WriteString(`\n`)
		case r == '\r':
			builder.WriteString(`\r`)
		case r == '\t':
			builder.WriteString(`\t`)
		case r >= 0 && r < 0x20:
			builder.WriteString(fmt.Sprintf(`\u%04x`, r))
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
}

// canonicalJSON 返回该 cell 的 ["type_tag", value] compact bytes。
func (c Cell) canonicalJSON() canonicalValue {
	tag := canonicalString(c.Type)
	switch c.Type {
	case CellNull:
		return canonicalArray{tag, canonicalNull{}}
	case CellBoolean:
		return canonicalArray{tag, canonicalBool(c.Bool)}
	case CellText, CellInteger, CellReal64, CellBlobBase64, CellTimestampUTC:
		return canonicalArray{tag, canonicalString(c.Str)}
	default:
		panic(fmt.Sprintf("providerconnectionmigration: unknown cell type %q", c.Type))
	}
}

// SnapshotColumn 是 snapshot 内一列的名称与源 SQL 类型（ordinal 顺序）。
type SnapshotColumn struct {
	Name    string
	SQLType string
}

// SnapshotTable 是一张源表的 snapshot：列按源 schema ordinal、主键声明与全部
// source row（row 顺序由 CanonicalBytes 按主键 tuple bytes 排序）。
type SnapshotTable struct {
	Name       string
	Columns    []SnapshotColumn
	PrimaryKey []string
	Rows       [][]Cell
}

// Snapshot 是完整 canonical source snapshot：source schema version、全部表
// （CanonicalBytes 内按表名字节序排序）与全部行。
type Snapshot struct {
	SchemaVersion string
	Tables        []SnapshotTable
}

// CanonicalBytes 渲染 §10 :465 的 canonical bytes。行按主键 tuple cell bytes
// 字节序排序；表按表名字节序排序；列保持 ordinal 顺序。
func (s *Snapshot) CanonicalBytes() ([]byte, error) {
	tables := make([]canonicalValue, 0, len(s.Tables))
	sorted := append([]SnapshotTable{}, s.Tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, table := range sorted {
		encoded, err := encodeSnapshotTable(table)
		if err != nil {
			return nil, err
		}
		tables = append(tables, encoded)
	}
	document := newCanonicalObject([][2]any{
		{"schema_version", canonicalString(s.SchemaVersion)},
		{"tables", canonicalArray(tables)},
	})
	var builder strings.Builder
	document.encode(&builder)
	return []byte(builder.String()), nil
}

// HashHex 返回 SHA-256(canonical bytes) 的 64 位小写十六进制。
func (s *Snapshot) HashHex() (string, error) {
	bytes, err := s.CanonicalBytes()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:]), nil
}

func encodeSnapshotTable(table SnapshotTable) (canonicalValue, error) {
	columnIndex := make(map[string]int, len(table.Columns))
	columns := make([]canonicalValue, 0, len(table.Columns))
	for ordinal, column := range table.Columns {
		if _, duplicate := columnIndex[column.Name]; duplicate {
			return nil, fmt.Errorf("snapshot 表 %s 列 %s 重复", table.Name, column.Name)
		}
		columnIndex[column.Name] = ordinal
		columns = append(columns, newCanonicalObject([][2]any{
			{"name", canonicalString(column.Name)},
			{"sql_type", canonicalString(column.SQLType)},
		}))
	}
	pkOrdinals := make([]int, 0, len(table.PrimaryKey))
	for _, name := range table.PrimaryKey {
		ordinal, present := columnIndex[name]
		if !present {
			return nil, fmt.Errorf("snapshot 表 %s 主键列 %s 不在列清单内", table.Name, name)
		}
		pkOrdinals = append(pkOrdinals, ordinal)
	}
	type rowKey struct {
		cells canonicalValue
		bytes []byte
		row   canonicalValue
	}
	rows := make([]rowKey, 0, len(table.Rows))
	for _, row := range table.Rows {
		if len(row) != len(table.Columns) {
			return nil, fmt.Errorf("snapshot 表 %s 行 cell 数 %d 与列数 %d 不一致", table.Name, len(row), len(table.Columns))
		}
		cells := make([]canonicalValue, 0, len(row))
		for _, cell := range row {
			cells = append(cells, cell.canonicalJSON())
		}
		rowCells := canonicalArray(cells)
		pkCells := make([]canonicalValue, 0, len(pkOrdinals))
		for _, ordinal := range pkOrdinals {
			pkCells = append(pkCells, row[ordinal].canonicalJSON())
		}
		pkTuple := canonicalArray(pkCells)
		var builder strings.Builder
		pkTuple.encode(&builder)
		rows = append(rows, rowKey{cells: rowCells, bytes: []byte(builder.String()), row: rowCells})
	}
	// row 按完整 primary-key tuple 各 cell 的 compact JSON bytes 做字节序比较。
	sort.SliceStable(rows, func(i, j int) bool {
		return string(rows[i].bytes) < string(rows[j].bytes)
	})
	orderedRows := make([]canonicalValue, 0, len(rows))
	for _, item := range rows {
		orderedRows = append(orderedRows, item.row)
	}
	return newCanonicalObject([][2]any{
		{"columns", canonicalArray(columns)},
		{"name", canonicalString(table.Name)},
		{"primary_key", canonicalStringArray(table.PrimaryKey)},
		{"rows", canonicalArray(orderedRows)},
	}), nil
}

func canonicalStringArray(values []string) canonicalArray {
	array := make([]canonicalValue, 0, len(values))
	for _, value := range values {
		array = append(array, canonicalString(value))
	}
	return array
}
