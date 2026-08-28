package businesshandoff

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

// SchemaReport is a read-only, machine-readable proof of the Business SQLite
// shape required before Gateway may set schemaReady=true. No DDL is executed.
type SchemaReport struct {
	Path               string              `json:"path"`
	SchemaVersion      string              `json:"schemaVersion"`
	ReadOnly           bool                `json:"readOnly"`
	UserDBTouched      bool                `json:"userDatabaseTouched"`
	RequiredTables     int                 `json:"requiredTables"`
	PresentTables      int                 `json:"presentTables"`
	MissingTables      []string            `json:"missingTables,omitempty"`
	MissingColumns     map[string][]string `json:"missingColumns,omitempty"`
	MissingIndexes     map[string][]string `json:"missingIndexes,omitempty"`
	MissingForeignKeys map[string][]string `json:"missingForeignKeys,omitempty"`
	Ready              bool                `json:"ready"`
	Errors             []string            `json:"errors,omitempty"`
}

// VerifySQLiteSchema opens path in SQLite read-only/query_only mode and checks
// the versioned shared contract. It never creates tables, indexes, or rows.
func VerifySQLiteSchema(ctx context.Context, path string) (SchemaReport, error) {
	path = canonicalPath(path)
	report := SchemaReport{Path: path, SchemaVersion: contracts.BusinessSQLiteSchemaVersion, MissingColumns: map[string][]string{}, MissingIndexes: map[string][]string{}, MissingForeignKeys: map[string][]string{}}
	if path == "" {
		report.Errors = []string{"Business SQLite path is empty"}
		return report, nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		report.Errors = []string{fmt.Sprintf("open read-only Business SQLite: %v", err)}
		return report, nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return report, fmt.Errorf("read Business SQLite query_only pragma: %w", err)
	}
	report.ReadOnly = queryOnly == 1
	if !report.ReadOnly {
		report.Errors = append(report.Errors, "Business SQLite query_only pragma is not enabled")
	}

	tables, err := sqliteObjects(ctx, db, "table")
	if err != nil {
		return report, fmt.Errorf("list Business SQLite tables: %w", err)
	}
	indexes, err := sqliteObjects(ctx, db, "index")
	if err != nil {
		return report, fmt.Errorf("list Business SQLite indexes: %w", err)
	}
	report.RequiredTables = len(contracts.BusinessSQLiteSchema)
	for table, spec := range contracts.BusinessSQLiteSchema {
		if !tables[table] {
			report.MissingTables = append(report.MissingTables, table)
			continue
		}
		report.PresentTables++
		columns, err := sqliteColumns(ctx, db, table)
		if err != nil {
			return report, err
		}
		for _, required := range spec.Columns {
			if !columns[required] {
				report.MissingColumns[table] = append(report.MissingColumns[table], required)
			}
		}
		for _, required := range spec.Indexes {
			if !indexes[required] {
				report.MissingIndexes[table] = append(report.MissingIndexes[table], required)
			}
		}
		foreignKeys, err := sqliteForeignKeys(ctx, db, table)
		if err != nil {
			return report, err
		}
		for _, required := range spec.ForeignKeys {
			signature := foreignKeySignature(table, required)
			if !foreignKeys[signature] {
				report.MissingForeignKeys[table] = append(report.MissingForeignKeys[table], signature)
			}
		}
	}
	sort.Strings(report.MissingTables)
	for table := range report.MissingColumns {
		sort.Strings(report.MissingColumns[table])
	}
	for table := range report.MissingIndexes {
		sort.Strings(report.MissingIndexes[table])
	}
	for table := range report.MissingForeignKeys {
		sort.Strings(report.MissingForeignKeys[table])
	}
	report.UserDBTouched = false
	report.Ready = report.ReadOnly && report.RequiredTables == report.PresentTables && len(report.MissingTables) == 0 && len(report.MissingColumns) == 0 && len(report.MissingIndexes) == 0 && len(report.MissingForeignKeys) == 0 && len(report.Errors) == 0
	return report, nil
}

func sqliteObjects(ctx context.Context, db *sql.DB, objectType string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type=?", objectType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func sqliteColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite table %s: %w", table, err)
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func sqliteForeignKeys(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite foreign keys for table %s: %w", table, err)
	}
	defer rows.Close()
	type relation struct {
		id       int
		seq      int
		table    string
		from     string
		to       string
		onUpdate string
		onDelete string
	}
	relations := make([]relation, 0)
	for rows.Next() {
		var item relation
		var match string
		if err := rows.Scan(&item.id, &item.seq, &item.table, &item.from, &item.to, &item.onUpdate, &item.onDelete, &match); err != nil {
			return nil, fmt.Errorf("scan Business SQLite foreign keys for table %s: %w", table, err)
		}
		relations = append(relations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	groups := map[int][]relation{}
	for _, item := range relations {
		groups[item.id] = append(groups[item.id], item)
	}
	result := map[string]bool{}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].seq < group[j].seq })
		if len(group) == 0 {
			continue
		}
		fromColumns := make([]string, 0, len(group))
		toColumns := make([]string, 0, len(group))
		for _, item := range group {
			fromColumns = append(fromColumns, item.from)
			toColumns = append(toColumns, item.to)
		}
		result[fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, strings.Join(fromColumns, ","), group[0].table, strings.Join(toColumns, ","), group[0].onDelete, group[0].onUpdate)] = true
	}
	return result, nil
}

func foreignKeySignature(table string, spec contracts.SQLiteForeignKeySpec) string {
	from := strings.Join(spec.Columns, ",")
	to := strings.Join(spec.RefColumns, ",")
	onDelete := spec.OnDelete
	if onDelete == "" {
		onDelete = "NO ACTION"
	}
	onUpdate := spec.OnUpdate
	if onUpdate == "" {
		onUpdate = "NO ACTION"
	}
	return fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, from, spec.RefTable, to, onDelete, onUpdate)
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
