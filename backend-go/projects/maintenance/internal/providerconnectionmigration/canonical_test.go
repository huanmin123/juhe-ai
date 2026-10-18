package providerconnectionmigration

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// canonical 单元测试：§10 :465 的逐字契约（cell 编码、字符串转义、键/表/行
// 排序、hash 稳定性、无 BOM 无空白）。

func TestCellCanonicalJSONTypes(t *testing.T) {
	timestamp := time.Date(2026, 10, 18, 1, 2, 3, 123456789, time.UTC)
	nonUTC := time.Date(2026, 10, 18, 3, 2, 3, 123456789, time.FixedZone("UTC+2", 2*3600))
	cases := []struct {
		name string
		cell Cell
		want string
	}{
		{"null", NullCell(), `["null",null]`},
		{"text plain", TextCell("gpt-6-sol"), `["text","gpt-6-sol"]`},
		{"text escapes", TextCell("a\"b\\c\n\t\b\f\r\x01héllo/不转义"), "[\"text\",\"a\\\"b\\\\c\\n\\t\\b\\f\\r\\u0001héllo/不转义\"]"},
		{"text control zero", TextCell("a\x00b"), `["text","a\u0000b"]`},
		{"text unit separator lower hex", TextCell("a\x1fb"), `["text","a\u001fb"]`},
		{"integer positive", IntegerCell(42), `["integer","42"]`},
		{"integer negative", IntegerCell(-7), `["integer","-7"]`},
		{"integer zero", IntegerCell(0), `["integer","0"]`},
		{"unsigned large", UnsignedIntegerCell(18446744073709551615), `["integer","18446744073709551615"]`},
		{"real64 1.5", Real64Cell(1.5), `["real64","3ff8000000000000"]`},
		{"real64 zero", Real64Cell(0), `["real64","0000000000000000"]`},
		{"real64 negative", Real64Cell(-2.5), `["real64","c004000000000000"]`},
		{"boolean true", BoolCell(true), `["boolean",true]`},
		{"boolean false", BoolCell(false), `["boolean",false]`},
		{"blob", BlobCell([]byte{0x00, 0xff, 0x10}), `["blob_base64","AP8Q"]`},
		{"blob empty", BlobCell([]byte{}), `["blob_base64",""]`},
		{"timestamp nano", TimestampCell(timestamp), `["timestamp_utc","2026-10-18T01:02:03.123456789Z"]`},
		{"timestamp timezone converted to UTC", TimestampCell(nonUTC), `["timestamp_utc","2026-10-18T01:02:03.123456789Z"]`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var builder strings.Builder
			testCase.cell.canonicalJSON().encode(&builder)
			if builder.String() != testCase.want {
				t.Fatalf("canonical cell mismatch:\n got: %s\nwant: %s", builder.String(), testCase.want)
			}
		})
	}
}

func TestReal64CellBitHexMatchesFloat64bits(t *testing.T) {
	// 16 位小写 hex 反解后必须等于 math.Float64bits（含 Inf/0 位型）。
	for _, value := range []float64{3.14, 0, math.Inf(1), math.Inf(-1), math.MaxFloat64, math.SmallestNonzeroFloat64} {
		cell := Real64Cell(value)
		bits, err := strconv.ParseUint(cell.Str, 16, 64)
		if err != nil {
			t.Fatalf("real64 hex 解析失败 %q: %v", cell.Str, err)
		}
		if bits != math.Float64bits(value) {
			t.Fatalf("real64 bit hex 不符: %q != %016x", cell.Str, math.Float64bits(value))
		}
	}
}

func TestCanonicalObjectKeySorting(t *testing.T) {
	// 对象键按 UTF-8 字节序排序，与插入顺序无关。
	document := newCanonicalObject([][2]any{
		{"tables", canonicalString("z")},
		{"schema_version", canonicalString("a")},
		{"accounts", canonicalString("m")},
	})
	var builder strings.Builder
	document.encode(&builder)
	if got, want := builder.String(), `{"accounts":"m","schema_version":"a","tables":"z"}`; got != want {
		t.Fatalf("键排序不符:\n got: %s\nwant: %s", got, want)
	}
}

func TestSnapshotCanonicalBytesLayout(t *testing.T) {
	snapshot := &Snapshot{
		SchemaVersion: SourceSchemaVersion,
		Tables: []SnapshotTable{
			{
				Name:       "zebra",
				Columns:    []SnapshotColumn{{Name: "id", SQLType: "TEXT"}, {Name: "n", SQLType: "INTEGER"}},
				PrimaryKey: []string{"id"},
				Rows:       [][]Cell{{TextCell("b"), IntegerCell(1)}, {TextCell("a"), IntegerCell(2)}},
			},
			{
				Name:       "alpha",
				Columns:    []SnapshotColumn{{Name: "id", SQLType: "TEXT"}},
				PrimaryKey: []string{"id"},
				Rows:       [][]Cell{{TextCell("x")}},
			},
		},
	}
	encoded, err := snapshot.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes 失败: %v", err)
	}
	text := string(encoded)
	if !strings.HasPrefix(text, "{") || strings.HasPrefix(text, "\ufeff") {
		t.Fatalf("canonical bytes 必须无 BOM 且以 { 开始: %q", text[:8])
	}
	if strings.ContainsAny(text, " \n\t\r") {
		t.Fatalf("canonical bytes 不得含空白: %s", text)
	}
	// 表按名字节序排序：alpha 在 zebra 之前。
	alphaIndex := strings.Index(text, `"name":"alpha"`)
	zebraIndex := strings.Index(text, `"name":"zebra"`)
	if alphaIndex < 0 || zebraIndex < 0 || alphaIndex > zebraIndex {
		t.Fatalf("表未按字节序排序: %s", text)
	}
	// 列按 ordinal 顺序、row 内 cell 与 column 顺序一致；行按主键 tuple bytes
	// 排序（"a" 行在 "b" 行前）。
	wantZebraRows := `"rows":[["a","2"],["b","1"]]`
	// cell 实际渲染为 [type_tag, value] 二元组。
	wantZebraRows = `"rows":[[["text","a"],["integer","2"]],[["text","b"],["integer","1"]]]`
	if !strings.Contains(text, wantZebraRows) {
		t.Fatalf("zebra 行渲染/排序不符: %s", text)
	}
}

func TestSnapshotRowSortedByPKTupleBytes(t *testing.T) {
	// 主键 tuple 的 compact JSON bytes 字节序比较（非数值序）：["a","10"] <
	// ["a","9"]（'1' < '9'）。
	snapshot := &Snapshot{
		SchemaVersion: "test",
		Tables: []SnapshotTable{{
			Name:       "t",
			Columns:    []SnapshotColumn{{Name: "k1", SQLType: "TEXT"}, {Name: "k2", SQLType: "INTEGER"}},
			PrimaryKey: []string{"k1", "k2"},
			Rows: [][]Cell{
				{TextCell("a"), IntegerCell(9)},
				{TextCell("a"), IntegerCell(10)},
				{TextCell("b"), IntegerCell(1)},
			},
		}},
	}
	encoded, err := snapshot.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes 失败: %v", err)
	}
	text := string(encoded)
	a10 := strings.Index(text, `["text","a"],["integer","10"]`)
	a9 := strings.Index(text, `["text","a"],["integer","9"]`)
	if a10 < 0 || a9 < 0 || a10 > a9 {
		t.Fatalf("行未按主键 tuple bytes 排序: %s", text)
	}
}

func TestSnapshotHashStability(t *testing.T) {
	build := func(model string) *Snapshot {
		return &Snapshot{
			SchemaVersion: SourceSchemaVersion,
			Tables: []SnapshotTable{{
				Name:       "t",
				Columns:    []SnapshotColumn{{Name: "id", SQLType: "TEXT"}, {Name: "m", SQLType: "TEXT"}},
				PrimaryKey: []string{"id"},
				Rows:       [][]Cell{{TextCell("row-1"), TextCell(model)}},
			}},
		}
	}
	first, err := build("a").HashHex()
	if err != nil {
		t.Fatalf("HashHex 失败: %v", err)
	}
	second, err := build("a").HashHex()
	if err != nil {
		t.Fatalf("HashHex 失败: %v", err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("同一快照 hash 必须逐字节相同且为 64 位十六进制: %q vs %q", first, second)
	}
	third, err := build("b").HashHex()
	if err != nil {
		t.Fatalf("HashHex 失败: %v", err)
	}
	if third == first {
		t.Fatalf("单字节变化必须改变 hash")
	}
}

func TestSnapshotDuplicateColumnRejected(t *testing.T) {
	snapshot := &Snapshot{
		SchemaVersion: "test",
		Tables: []SnapshotTable{{
			Name:       "t",
			Columns:    []SnapshotColumn{{Name: "id", SQLType: "TEXT"}, {Name: "id", SQLType: "TEXT"}},
			PrimaryKey: []string{"id"},
		}},
	}
	if _, err := snapshot.CanonicalBytes(); err == nil {
		t.Fatalf("重复列必须被拒绝")
	}
}

func TestCanonicalJSONRoundTripViaParsing(t *testing.T) {
	// canonical bytes 是合法 JSON，可用标准库解析交叉验证（值语义等价）。
	snapshot := &Snapshot{
		SchemaVersion: "v-test",
		Tables: []SnapshotTable{{
			Name:       "accounts",
			Columns:    []SnapshotColumn{{Name: "id", SQLType: "text"}, {Name: "n", SQLType: "integer"}, {Name: "b", SQLType: "boolean"}},
			PrimaryKey: []string{"id"},
			Rows:       [][]Cell{{TextCell("a\"b"), IntegerCell(-3), BoolCell(true)}},
		}},
	}
	encoded, err := snapshot.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes 失败: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatalf("canonical bytes 必须是合法 JSON: %v", err)
	}
	if parsed["schema_version"] != "v-test" {
		t.Fatalf("schema_version 解析不符: %v", parsed["schema_version"])
	}
	tables := parsed["tables"].([]any)
	rows := tables[0].(map[string]any)["rows"].([]any)
	firstRow := rows[0].([]any)
	if firstRow[0].([]any)[1] != "a\"b" {
		t.Fatalf("TEXT cell 值解析不符: %v", firstRow[0])
	}
	if firstRow[1].([]any)[1] != "-3" {
		t.Fatalf("INTEGER cell 必须是十进制字符串: %v", firstRow[1])
	}
	if firstRow[2].([]any)[1] != true {
		t.Fatalf("BOOLEAN cell 值解析不符: %v", firstRow[2])
	}
}
