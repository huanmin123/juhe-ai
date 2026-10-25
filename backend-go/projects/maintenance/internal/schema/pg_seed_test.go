// Tests for the full PostgreSQL seed port (pg_seed.go): statement rendering
// without a server, plus the opt-in integration smoke behind the same env
// gate as the schema golden test.

package schema

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var pgSeedTestClock = time.Date(2026, 9, 4, 8, 0, 0, 123456000, time.UTC)

func TestBuildPostgresModelSeedUpsertRendering(t *testing.T) {
	rows, err := modelSeedTargetRows("2026-09-04")
	if err != nil {
		t.Fatalf("modelSeedTargetRows: %v", err)
	}
	query, args := buildPostgresModelSeedUpsert(rows, "2026-09-04T08:00:00.123Z")
	if got := len(rows) * 40; len(args) != got {
		t.Fatalf("bulk upsert arg count = %d, want %d", len(args), got)
	}
	if !strings.Contains(query, `INSERT INTO "juhe_business"."models"`) {
		t.Fatal("bulk upsert misses the qualified insert target")
	}
	if !strings.Contains(query, "ON CONFLICT (coalesce(vendor_code, ''), scope, coalesce(system_account_id, ''), upstream_model) DO UPDATE SET") {
		t.Fatal("bulk upsert misses the identity-index conflict clause")
	}
	for _, guard := range []string{"manual-override", "manual-visibility-override", "excluded.catalog_visible"} {
		if !strings.Contains(query, guard) {
			t.Fatalf("bulk upsert misses guard %q", guard)
		}
	}
	// scope/system_account_id/currency are per-row literals, never parameters.
	if got := strings.Count(query, "'builtin'"); got != len(rows) {
		t.Fatalf("'builtin' literal count = %d, want %d", got, len(rows))
	}
	if got := strings.Count(query, "'USD'"); got != len(rows) {
		t.Fatalf("'USD' literal count = %d, want %d", got, len(rows))
	}
	// 第二行行首元素格式校验：每行 40 参数，第二行从 $41 重启，且字面量列序
	// 与首行一致（'builtin' 后紧跟 system_account_id 的 NULL，再接 status）。
	if !strings.Contains(query, "),\n          ($41, $42, $43, $44, 'builtin', NULL, $45") {
		t.Fatal("bulk upsert second row misses the 40-parameter restart with builtin/NULL literals")
	}
	if !strings.Contains(query, "$"+strconv.Itoa(len(args))) {
		t.Fatalf("bulk upsert misses the final placeholder $%d", len(args))
	}
	if strings.Contains(query, "$"+strconv.Itoa(len(args)+1)) {
		t.Fatal("bulk upsert has a placeholder beyond the arg count")
	}
	// First row args mirror the first projected row.
	first := rows[0]
	if args[0] != first.ID || args[1] != first.VendorCode || args[2] != first.PublicModel || args[3] != first.UpstreamModel {
		t.Fatalf("first row identity args = %v/%v/%v/%v", args[0], args[1], args[2], args[3])
	}
	if args[4] != first.Status {
		t.Fatalf("first row status arg = %v, want %v", args[4], first.Status)
	}
	if args[39] != seedNullableFloat64(first.OutputUsdPerImage) {
		t.Fatalf("first row final arg = %v, want OutputUsdPerImage %v (currency 是语句内字面量)", args[39], first.OutputUsdPerImage)
	}
}

// --- models bulk upsert arity 防线（PG 与 SQLite 双侧）---

// splitSeedUpsertTopLevelCommas 按顶层逗号切分 VALUES 元组体：单引号字符串
// 字面量内的逗号与 '' 转义不参与切分；$n / ? 占位符按普通 token 返回。
func splitSeedUpsertTopLevelCommas(body string) []string {
	var elements []string
	var current strings.Builder
	inString := false
	for index := 0; index < len(body); index++ {
		switch ch := body[index]; {
		case inString:
			current.WriteByte(ch)
			if ch == '\'' {
				if index+1 < len(body) && body[index+1] == '\'' {
					current.WriteByte('\'')
					index++
					continue
				}
				inString = false
			}
		case ch == '\'':
			inString = true
			current.WriteByte(ch)
		case ch == ',':
			elements = append(elements, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	return append(elements, strings.TrimSpace(current.String()))
}

// scanSeedUpsertTuples 提取 VALUES 段内全部顶层 (...) 元组（单引号字面量内的
// 括号与逗号不参与结构计数）。
func scanSeedUpsertTuples(section string) []string {
	var tuples []string
	depth := 0
	inString := false
	start := -1
	for index := 0; index < len(section); index++ {
		switch ch := section[index]; {
		case inString:
			if ch == '\'' {
				if index+1 < len(section) && section[index+1] == '\'' {
					index++
					continue
				}
				inString = false
			}
		case ch == '\'':
			inString = true
		case ch == '(':
			if depth == 0 {
				start = index
			}
			depth++
		case ch == ')':
			depth--
			if depth == 0 && start >= 0 {
				tuples = append(tuples, section[start:index+1])
				start = -1
			}
		}
	}
	return tuples
}

// seedUpsertInsertColumnCount 解析 INSERT 语句的目标列清单（首个括号组，列名
// 是普通标识符），返回列数 N。
func seedUpsertInsertColumnCount(t *testing.T, insertStatement string) int {
	t.Helper()
	open := strings.Index(insertStatement, "(")
	if open < 0 {
		t.Fatalf("insert statement misses the column list: %.80s", insertStatement)
	}
	depth := 0
	inString := false
	for index := open; index < len(insertStatement); index++ {
		switch ch := insertStatement[index]; {
		case inString:
			if ch == '\'' {
				inString = false
			}
		case ch == '\'':
			inString = true
		case ch == '(':
			depth++
		case ch == ')':
			depth--
			if depth == 0 {
				columns := splitSeedUpsertTopLevelCommas(insertStatement[open+1 : index])
				if len(columns) == 0 {
					t.Fatalf("insert statement has an empty column list: %.120s", insertStatement)
				}
				return len(columns)
			}
		}
	}
	t.Fatalf("insert statement has an unterminated column list: %.120s", insertStatement)
	return 0
}

// TestPostgresModelSeedUpsertRowArityMatchesTargetColumns 是 PG models 批量
// upsert 的 arity 防线：目标列数 N 取自 pgSeedModelsColumns 本身，对渲染出的
// 每一行 VALUES 元组做顶层逗号元素计数，逐行断言 == N，且每行恰有 40 个 $n
// 占位符（40 参数 + 8 字面量）。历史 blocker：目标列 48 列而每行只渲染 43 值，
// PG 执行报 42601 且第 25 列起整体错位。
func TestPostgresModelSeedUpsertRowArityMatchesTargetColumns(t *testing.T) {
	columnCount := seedUpsertInsertColumnCount(t, pgSeedModelsColumns)
	rows, err := modelSeedTargetRows("2026-09-04")
	if err != nil {
		t.Fatalf("modelSeedTargetRows: %v", err)
	}
	query, args := buildPostgresModelSeedUpsert(rows, "2026-09-04T08:00:00.123Z")
	if len(args) != len(rows)*40 {
		t.Fatalf("bulk upsert arg count = %d, want %d", len(args), len(rows)*40)
	}
	valuesIndex := strings.Index(query, ") VALUES")
	conflictIndex := strings.Index(query, "ON CONFLICT")
	if valuesIndex < 0 || conflictIndex < 0 || valuesIndex > conflictIndex {
		t.Fatalf("bulk upsert misses the VALUES...ON CONFLICT frame: values=%d conflict=%d", valuesIndex, conflictIndex)
	}
	tuples := scanSeedUpsertTuples(query[valuesIndex+len(") VALUES") : conflictIndex])
	if len(tuples) != len(rows) {
		t.Fatalf("rendered VALUES tuples = %d, want %d", len(tuples), len(rows))
	}
	for index, tuple := range tuples {
		elements := splitSeedUpsertTopLevelCommas(strings.TrimSuffix(strings.TrimPrefix(tuple, "("), ")"))
		if len(elements) != columnCount {
			t.Fatalf("VALUES row %d element count = %d, want %d (pgSeedModelsColumns)", index+1, len(elements), columnCount)
		}
		placeholders := 0
		for _, element := range elements {
			if strings.HasPrefix(element, "$") {
				placeholders++
			}
		}
		if placeholders != 40 {
			t.Fatalf("VALUES row %d placeholder count = %d, want 40", index+1, placeholders)
		}
	}
}
