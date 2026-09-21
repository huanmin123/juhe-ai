// Package businessdataset implements the read-only export and the
// all-or-nothing authoritative import of the fixed 40-table business dataset
// used by the test-environment launch data stage. Export streams every
// whitelist table from a READ ONLY transaction into canonical JSONL files plus
// a hashed manifest; import replays one dataset directory into an
// authoritative PostgreSQL database inside a single transaction, verifies row
// counts and per-table digests against the manifest, and commits only when
// everything matches.
package businessdataset

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// SchemaName is the single PostgreSQL schema that holds all 40 whitelist
// tables.
const SchemaName = "juhe_business"

// ManifestFileName is the manifest file written inside the dataset directory.
const ManifestFileName = "business-dataset-manifest.json"

// DefaultExportProducer identifies the exporting tool in the manifest.
const DefaultExportProducer = "juhe-ai-maintenance/businessdataset-export-v1"

// DefaultTargetIdentity is used when ExportOptions.TargetIdentity is empty.
// It is by definition a placeholder, never a name captured from
// current_database(), so the import-side source-identity gate refuses it.
const DefaultTargetIdentity = "authoritative-business-postgres"

// IsPlaceholderIdentity reports whether value is an empty or known placeholder
// dataset identity rather than a database name captured from
// current_database(). The import-side source-identity gate uses it so a
// hand-edited manifest whose sourceIdentity was never a real database cannot
// pass the identity check.
func IsPlaceholderIdentity(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || trimmed == DefaultTargetIdentity
}

// Open validates an explicit PostgreSQL URL (SQLite and every other dialect
// are rejected), pins the maintenance application identity and opens a
// database/sql pool over the pgx stdlib driver. The caller owns Close.
func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("business dataset URL 必须是 postgres/postgresql URL；仅支持 PostgreSQL 方言，不支持 SQLite")
	}
	query := parsed.Query()
	query.Set("application_name", "juhe-ai-maintenance-business-dataset")
	query.Set("connect_timeout", "10")
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 business dataset PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// bdColumn is one inspected table column in ordinal order.
type bdColumn struct {
	Name     string
	DataType string
	Nullable bool
}

// bdForeignKey is one inspected foreign key edge with resolved column names.
type bdForeignKey struct {
	ConstraintName string
	FromTable      string
	FromColumns    []string
	ToTable        string
	ToColumns      []string
}

func quoteIdent(name string) (string, error) {
	if name == "" || strings.Contains(name, "\"") || strings.Contains(name, "\x00") {
		return "", fmt.Errorf("非法标识符 %q", name)
	}
	return `"` + name + `"`, nil
}

// bdQualifiedTable renders schema.table with the table name quoted.
func bdQualifiedTable(table string) (string, error) {
	quoted, err := quoteIdent(table)
	if err != nil {
		return "", err
	}
	return SchemaName + "." + quoted, nil
}

// bdLoadColumns inspects one table's columns in ordinal order from
// information_schema. An absent table yields an empty list without error; the
// callers turn that into an explicit blocker.
func bdLoadColumns(ctx context.Context, q queryer, table string) ([]bdColumn, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`,
		SchemaName, table)
	if err != nil {
		return nil, fmt.Errorf("查询表 %s 列目录: %w", table, err)
	}
	defer rows.Close()
	var columns []bdColumn
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, fmt.Errorf("扫描表 %s 列目录: %w", table, err)
		}
		columns = append(columns, bdColumn{Name: name, DataType: dataType, Nullable: strings.EqualFold(strings.TrimSpace(nullable), "YES")})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历表 %s 列目录: %w", table, err)
	}
	return columns, nil
}

// bdLoadPrimaryKey inspects one table's primary key columns in column order.
// A table without a primary key yields an empty list without error.
func bdLoadPrimaryKey(ctx context.Context, q queryer, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT a.attname FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY (i.indkey) WHERE i.indisprimary AND n.nspname = $1 AND c.relname = $2 ORDER BY a.attnum`,
		SchemaName, table)
	if err != nil {
		return nil, fmt.Errorf("查询表 %s 主键目录: %w", table, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("扫描表 %s 主键目录: %w", table, err)
		}
		keys = append(keys, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历表 %s 主键目录: %w", table, err)
	}
	return keys, nil
}

// bdLoadForeignKeys inspects every foreign key of the schema with resolved
// column names. The pg_constraint conkey/confkey attnum arrays are joined to
// column names in Go against one pg_attribute pass so the SQL stays simple.
func bdLoadForeignKeys(ctx context.Context, q queryer) ([]bdForeignKey, error) {
	attnums := map[string]map[int64]string{}
	attrRows, err := q.QueryContext(ctx,
		`SELECT c.relname, a.attnum, a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY c.relname, a.attnum`,
		SchemaName)
	if err != nil {
		return nil, fmt.Errorf("查询 schema 列序号目录: %w", err)
	}
	for attrRows.Next() {
		var table, name string
		var attnum int64
		if err := attrRows.Scan(&table, &attnum, &name); err != nil {
			attrRows.Close()
			return nil, fmt.Errorf("扫描 schema 列序号目录: %w", err)
		}
		if attnums[table] == nil {
			attnums[table] = map[int64]string{}
		}
		attnums[table][attnum] = name
	}
	if err := attrRows.Err(); err != nil {
		attrRows.Close()
		return nil, fmt.Errorf("遍历 schema 列序号目录: %w", err)
	}
	attrRows.Close()

	conRows, err := q.QueryContext(ctx,
		`SELECT c.conname, src.relname, dst.relname, c.conkey, c.confkey FROM pg_constraint c JOIN pg_class src ON src.oid = c.conrelid JOIN pg_class dst ON dst.oid = c.confrelid JOIN pg_namespace n ON n.oid = src.relnamespace WHERE c.contype = 'f' AND n.nspname = $1 ORDER BY c.conname`,
		SchemaName)
	if err != nil {
		return nil, fmt.Errorf("查询外键目录: %w", err)
	}
	defer conRows.Close()
	var keys []bdForeignKey
	for conRows.Next() {
		var name, fromTable, toTable string
		var conkey, confkey any
		if err := conRows.Scan(&name, &fromTable, &toTable, &conkey, &confkey); err != nil {
			return nil, fmt.Errorf("扫描外键目录: %w", err)
		}
		fromCols, err := bdResolveAttnums(attnums, fromTable, conkey)
		if err != nil {
			return nil, err
		}
		toCols, err := bdResolveAttnums(attnums, toTable, confkey)
		if err != nil {
			return nil, err
		}
		keys = append(keys, bdForeignKey{ConstraintName: name, FromTable: fromTable, FromColumns: fromCols, ToTable: toTable, ToColumns: toCols})
	}
	if err := conRows.Err(); err != nil {
		return nil, fmt.Errorf("遍历外键目录: %w", err)
	}
	return keys, nil
}

func bdResolveAttnums(attnums map[string]map[int64]string, table string, raw any) ([]string, error) {
	numbers, err := bdAttnumList(raw)
	if err != nil {
		return nil, fmt.Errorf("解析表 %s 外键列序号: %w", table, err)
	}
	columns := make([]string, 0, len(numbers))
	for _, number := range numbers {
		name, ok := attnums[table][number]
		if !ok {
			return nil, fmt.Errorf("表 %s 外键列序号 %d 无对应列", table, number)
		}
		columns = append(columns, name)
	}
	return columns, nil
}

// bdAttnumList normalizes the smallint[] attnum arrays returned by the
// drivers into []int64.
func bdAttnumList(raw any) ([]int64, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case int:
		return []int64{int64(typed)}, nil
	case int32:
		return []int64{int64(typed)}, nil
	case int64:
		return []int64{typed}, nil
	case []int16:
		out := make([]int64, 0, len(typed))
		for _, value := range typed {
			out = append(out, int64(value))
		}
		return out, nil
	case []int32:
		out := make([]int64, 0, len(typed))
		for _, value := range typed {
			out = append(out, int64(value))
		}
		return out, nil
	case []int64:
		return append([]int64(nil), typed...), nil
	case []any:
		out := make([]int64, 0, len(typed))
		for _, value := range typed {
			number, err := bdAttnumList(value)
			if err != nil || len(number) != 1 {
				return nil, fmt.Errorf("非整型列序号 %v", value)
			}
			out = append(out, number[0])
		}
		return out, nil
	case []byte:
		return bdParseAttnumText(string(typed))
	case string:
		return bdParseAttnumText(typed)
	default:
		return nil, fmt.Errorf("不支持的列序号类型 %T", raw)
	}
}

func bdParseAttnumText(text string) ([]int64, error) {
	// pgx stdlib 把 smallint[] 以 PostgreSQL 数组字面量文本交给 database/sql
	// 的 any 目的地（如 "{1,2}"；空数组 "{}"）。同时容忍空格/逗号分隔的裸整
	// 数形式（fake 驱动与其他驱动的潜在返回形态）。
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimSuffix(strings.TrimPrefix(trimmed, "{"), "}")
	if trimmed == "" {
		return []int64{}, nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	out := make([]int64, 0, len(fields))
	for _, field := range fields {
		var value int64
		if _, err := fmt.Sscanf(strings.TrimSpace(field), "%d", &value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

// bdCanonicalRowJSON renders one row as canonical JSON: an object with every
// column key present (encoding/json sorts map keys, so the byte form is
// deterministic). It is the unit of both the JSONL files and the per-table
// digest.
func bdCanonicalRowJSON(values map[string]any) ([]byte, error) {
	data, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("渲染行 canonical JSON: %w", err)
	}
	return data, nil
}

// bdNormalizeValue maps a driver-scanned cell onto a JSON-stable value. The
// same normalization runs on export, on import readback and on manifest
// verification, so both sides always render identical bytes for identical
// data.
func bdNormalizeValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case bool, string, int64, float64:
		return typed, nil
	case time.Time:
		return typed, nil
	case []byte:
		return base64.StdEncoding.EncodeToString(typed), nil
	default:
		if valuer, ok := value.(driver.Valuer); ok {
			inner, err := valuer.Value()
			if err != nil {
				return nil, fmt.Errorf("规范化 %T 值: %w", value, err)
			}
			return bdNormalizeValue(inner)
		}
		return fmt.Sprintf("%v", value), nil
	}
}

// bdScanRows streams one SELECT and invokes emit for every row as a normalized
// column->value map. Scan destinations are plain any so pgx decides the
// natural Go type per column.
func bdScanRows(ctx context.Context, q queryer, description string, query string, columns []string, emit func(map[string]any) error) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("查询 %s: %w", description, err)
	}
	defer rows.Close()
	raw := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range raw {
		dest[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("扫描 %s 行: %w", description, err)
		}
		values := make(map[string]any, len(columns))
		for i, column := range columns {
			normalized, err := bdNormalizeValue(raw[i])
			if err != nil {
				return fmt.Errorf("规范化 %s 列 %s: %w", description, column, err)
			}
			values[column] = normalized
		}
		if err := emit(values); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 %s: %w", description, err)
	}
	return nil
}

// bdSelectAll renders the fixed-shape whole-table SELECT used for export and
// readback: SELECT "c", ... FROM juhe_business."t" ORDER BY "o", ...
func bdSelectAll(table string, columns, order []string) (string, error) {
	qualified, err := bdQualifiedTable(table)
	if err != nil {
		return "", err
	}
	projection := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted, err := quoteIdent(column)
		if err != nil {
			return "", fmt.Errorf("表 %s: %w", table, err)
		}
		projection = append(projection, quoted)
	}
	orderings := make([]string, 0, len(order))
	for _, column := range order {
		quoted, err := quoteIdent(column)
		if err != nil {
			return "", fmt.Errorf("表 %s: %w", table, err)
		}
		orderings = append(orderings, quoted)
	}
	if len(orderings) == 0 {
		return "", fmt.Errorf("表 %s 无法确定稳定排序", table)
	}
	return fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(projection, ", "), qualified, strings.Join(orderings, ", ")), nil
}

// bdInsert renders one fixed-shape INSERT with explicit columns and ordinal
// placeholders: INSERT INTO juhe_business."t" ("c", ...) VALUES ($1, ...).
func bdInsert(table string, columns []string) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("表 %s 的 INSERT 列清单为空", table)
	}
	qualified, err := bdQualifiedTable(table)
	if err != nil {
		return "", err
	}
	projection := make([]string, 0, len(columns))
	placeholders := make([]string, 0, len(columns))
	for i, column := range columns {
		quoted, err := quoteIdent(column)
		if err != nil {
			return "", fmt.Errorf("表 %s: %w", table, err)
		}
		projection = append(projection, quoted)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qualified, strings.Join(projection, ", "), strings.Join(placeholders, ", ")), nil
}

// bdParamValue converts one JSON-decoded cell into a driver parameter. JSON
// numbers are preserved losslessly as json.Number on decode; here they become
// int64 or float64 without precision loss for integer literals.
func bdParamValue(value any) (driver.Value, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case bool, int64, float64, string:
		return typed, nil
	case json.Number:
		text := typed.String()
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed, nil
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("JSON 数字 %s 不是合法数值", text)
		}
		return parsed, nil
	case map[string]any, []any:
		data, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("渲染嵌套 JSON 参数: %w", err)
		}
		return string(data), nil
	default:
		return nil, fmt.Errorf("不支持的 JSON 参数类型 %T", value)
	}
}

// bdReadDatasetFile streams one JSONL file: it hashes the raw bytes while
// decoding every non-empty line into a normalized row map. Column sets are
// asserted identical across rows; the first row defines the file's column set.
func bdReadDatasetFile(path string) (sha256Hex string, columns []string, rows []map[string]any, rowCount int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, nil, 0, err
	}
	defer file.Close()
	digest := sha256.New()
	reader := bufio.NewReader(file)
	columnSet := map[string]struct{}{}
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			// sha256.Write 按标准库契约不会返回错误。
			_, _ = digest.Write(line)
			text := strings.TrimRight(string(line), "\r\n")
			if strings.TrimSpace(text) != "" {
				decoder := json.NewDecoder(strings.NewReader(text))
				decoder.UseNumber()
				var decoded map[string]any
				if err := decoder.Decode(&decoded); err != nil {
					return "", nil, nil, 0, fmt.Errorf("解码 %s 行 %d: %w", filepath.Base(path), rowCount+1, err)
				}
				if columns == nil {
					for name := range decoded {
						columnSet[name] = struct{}{}
					}
					columns = make([]string, 0, len(columnSet))
					for name := range columnSet {
						columns = append(columns, name)
					}
					sort.Strings(columns)
				} else if len(decoded) != len(columnSet) {
					return "", nil, nil, 0, fmt.Errorf("%s 行 %d 列集合与首行不一致", filepath.Base(path), rowCount+1)
				} else {
					for name := range decoded {
						if _, ok := columnSet[name]; !ok {
							return "", nil, nil, 0, fmt.Errorf("%s 行 %d 列集合与首行不一致", filepath.Base(path), rowCount+1)
						}
					}
				}
				rows = append(rows, decoded)
				rowCount++
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", nil, nil, 0, readErr
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), columns, rows, rowCount, nil
}

// bdValueKey renders one decoded cell as a canonical comparison key for
// row-level foreign key graphs.
func bdValueKey(value any) string {
	switch typed := value.(type) {
	case nil:
		return "\x00null"
	case json.Number:
		return "n:" + typed.String()
	case string:
		return "s:" + typed
	case bool:
		if typed {
			return "b:true"
		}
		return "b:false"
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("x:%T:%v", value, value)
		}
		return "j:" + string(data)
	}
}

// bdValueKeys renders a multi-column comparison key.
func bdValueKeys(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = bdValueKey(value)
	}
	return strings.Join(parts, "\x1f")
}

// bdLoadManifest reads and decodes the manifest file of a dataset directory.
// Structural validation is a separate step so callers can distinguish input
// errors from self-inconsistent manifests.
func bdLoadManifest(dir string) (contracts.BusinessDatasetManifest, string, error) {
	if strings.TrimSpace(dir) == "" {
		return contracts.BusinessDatasetManifest{}, "", errors.New("business dataset 目录为空")
	}
	path := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return contracts.BusinessDatasetManifest{}, "", err
	}
	manifest, err := contracts.DecodeBusinessDatasetManifest(data)
	if err != nil {
		return contracts.BusinessDatasetManifest{}, "", fmt.Errorf("解码 %s: %w", ManifestFileName, err)
	}
	return manifest, path, nil
}

// bdWriteManifestFile assembles, validates and writes the manifest for a
// fully exported dataset directory. It refuses to write a manifest whose
// validation fails.
func bdWriteManifestFile(dir string, manifest contracts.BusinessDatasetManifest) (string, error) {
	if errors := contracts.ValidateBusinessDatasetManifest(manifest); len(errors) > 0 {
		return "", fmt.Errorf("business dataset manifest 无效: %s", strings.Join(errors, "; "))
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, ManifestFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadBusinessDatasetManifest reads and decodes one dataset directory's
// manifest file. It is the runner-level input check: decode failures are usage
// errors, while structural validation happens inside ImportBusinessDataset and
// surfaces as report blockers.
func LoadBusinessDatasetManifest(dir string) (contracts.BusinessDatasetManifest, string, error) {
	return bdLoadManifest(dir)
}
