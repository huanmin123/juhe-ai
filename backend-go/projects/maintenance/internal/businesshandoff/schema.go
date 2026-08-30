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
	MissingConstraints map[string][]string `json:"missingConstraints,omitempty"`
	Ready              bool                `json:"ready"`
	Errors             []string            `json:"errors,omitempty"`
}

// VerifySQLiteSchema opens path in SQLite read-only/query_only mode and checks
// the versioned shared contract. It never creates tables, indexes, or rows.
func VerifySQLiteSchema(ctx context.Context, path string) (SchemaReport, error) {
	path = canonicalPath(path)
	report := SchemaReport{Path: path, SchemaVersion: contracts.BusinessSQLiteSchemaVersion, MissingColumns: map[string][]string{}, MissingIndexes: map[string][]string{}, MissingForeignKeys: map[string][]string{}, MissingConstraints: map[string][]string{}}
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
		if len(spec.PrimaryKey) > 0 {
			actual, err := sqlitePrimaryKey(ctx, db, table)
			if err != nil {
				return report, err
			}
			if !sameStrings(actual, spec.PrimaryKey) {
				report.MissingConstraints[table] = append(report.MissingConstraints[table], "PRIMARY KEY ("+strings.Join(spec.PrimaryKey, ",")+")")
			}
		}
		for _, required := range spec.UniqueConstraints {
			ok, err := sqliteHasUniqueConstraint(ctx, db, table, required)
			if err != nil {
				return report, err
			}
			if !ok {
				report.MissingConstraints[table] = append(report.MissingConstraints[table], "UNIQUE ("+strings.Join(required, ",")+")")
			}
		}
		for _, required := range spec.Indexes {
			if !indexes[required] {
				report.MissingIndexes[table] = append(report.MissingIndexes[table], required)
			}
		}
		for _, required := range spec.IndexDefinitions {
			ok, detail, err := sqliteIndexMatches(ctx, db, table, required)
			if err != nil {
				return report, fmt.Errorf("inspect Business SQLite index %s.%s: %w", table, required.Name, err)
			}
			if !ok {
				report.MissingIndexes[table] = append(report.MissingIndexes[table], required.Name)
				report.Errors = append(report.Errors, fmt.Sprintf("Business SQLite index %s.%s is incompatible: %s", table, required.Name, detail))
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
	for table := range report.MissingConstraints {
		sort.Strings(report.MissingConstraints[table])
	}
	report.UserDBTouched = false
	report.Ready = report.ReadOnly && report.RequiredTables == report.PresentTables && len(report.MissingTables) == 0 && len(report.MissingColumns) == 0 && len(report.MissingIndexes) == 0 && len(report.MissingForeignKeys) == 0 && len(report.MissingConstraints) == 0 && len(report.Errors) == 0
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

func sqlitePrimaryKey(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite primary key for table %s: %w", table, err)
	}
	defer rows.Close()
	type item struct {
		name string
		seq  int
	}
	items := make([]item, 0)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			items = append(items, item{name: name, seq: pk})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].seq < items[j].seq })
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.name)
	}
	return result, nil
}

func sqliteHasUniqueConstraint(ctx context.Context, db *sql.DB, table string, required []string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteIdentifier(table)+")")
	if err != nil {
		return false, err
	}
	type index struct{ name string }
	indexes := make([]index, 0)
	for rows.Next() {
		var seq, uniqueFlag, partialFlag int
		var name, origin string
		if err := rows.Scan(&seq, &name, &uniqueFlag, &origin, &partialFlag); err != nil {
			rows.Close()
			return false, err
		}
		if uniqueFlag == 1 && partialFlag == 0 {
			indexes = append(indexes, index{name: name})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, index := range indexes {
		columns, err := sqliteIndexColumns(ctx, db, index.name)
		if err != nil {
			return false, err
		}
		if sameStrings(columns, required) {
			return true, nil
		}
	}
	return false, nil
}

func sqliteIndexColumns(ctx context.Context, db *sql.DB, name string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_info("+quoteIdentifier(name)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type item struct {
		name string
		seq  int
	}
	items := make([]item, 0)
	for rows.Next() {
		var seq, cid int
		var column sql.NullString
		if err := rows.Scan(&seq, &cid, &column); err != nil {
			return nil, err
		}
		if !column.Valid {
			return nil, fmt.Errorf("Business SQLite index %s contains an expression", name)
		}
		items = append(items, item{name: column.String, seq: seq})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].seq < items[j].seq })
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.name)
	}
	return result, nil
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

func sqliteIndexMatches(ctx context.Context, db *sql.DB, table string, required contracts.SQLiteIndexDefinition) (bool, string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteIdentifier(table)+")")
	if err != nil {
		return false, "read index_list failed", err
	}
	defer rows.Close()
	var found, unique, partial bool
	for rows.Next() {
		var seq int
		var name, origin string
		var u, p int
		if err := rows.Scan(&seq, &name, &u, &origin, &p); err != nil {
			return false, "scan index_list failed", err
		}
		if name == required.Name {
			found, unique, partial = true, u == 1, p == 1
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, "read index_list failed", err
	}
	if err := rows.Close(); err != nil {
		return false, "close index_list failed", err
	}
	if !found {
		return false, "index is missing", nil
	}
	if unique != required.Unique {
		return false, fmt.Sprintf("unique=%t want=%t", unique, required.Unique), nil
	}
	colRows, err := db.QueryContext(ctx, "PRAGMA index_info("+quoteIdentifier(required.Name)+")")
	if err != nil {
		return false, "read index_info failed", err
	}
	defer colRows.Close()
	columns := make([]string, 0, len(required.Columns))
	for colRows.Next() {
		var seq, cid int
		var name sql.NullString
		if err := colRows.Scan(&seq, &cid, &name); err != nil {
			return false, "scan index_info failed", err
		}
		if !name.Valid {
			return false, "index contains an expression", nil
		}
		columns = append(columns, name.String)
	}
	if err := colRows.Err(); err != nil {
		return false, "read index_info failed", err
	}
	if err := colRows.Close(); err != nil {
		return false, "close index_info failed", err
	}
	if !sameStrings(columns, required.Columns) {
		return false, fmt.Sprintf("columns=%v want=%v", columns, required.Columns), nil
	}
	var definition sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='index' AND name=?", required.Name).Scan(&definition); err != nil {
		return false, "read sqlite_master definition failed", err
	}
	actualPredicate := sqliteIndexPredicate(definition.String)
	if strings.TrimSpace(required.Predicate) == "" {
		if partial || actualPredicate != "" {
			return false, "unexpected partial predicate", nil
		}
	} else {
		if !partial || !predicatesEquivalent(actualPredicate, required.Predicate) {
			return false, fmt.Sprintf("predicate=%q want=%q", actualPredicate, required.Predicate), nil
		}
	}
	return true, "", nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sqliteIndexPredicate(sqlText string) string {
	lower := strings.ToLower(sqlText)
	idx := strings.Index(lower, " where ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(sqlText[idx+7:])
}

func predicatesEquivalent(actual, expected string) bool {
	normalize := func(v string) []string {
		v = strings.ToLower(strings.TrimSpace(v))
		v = strings.NewReplacer("`", "", "[", "", "]", "", `"`, "").Replace(v)
		v = strings.NewReplacer("::text", "", "::character varying", "", "::varchar", "").Replace(v)
		v = strings.ReplaceAll(strings.ReplaceAll(v, "(", ""), ")", "")
		v = strings.Join(strings.Fields(v), " ")
		for strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			v = strings.TrimSpace(v[1 : len(v)-1])
		}
		parts := strings.Split(v, " and ")
		for i := range parts {
			parts[i] = strings.Join(strings.Fields(parts[i]), " ")
		}
		sort.Strings(parts)
		return parts
	}
	return sameStrings(normalize(actual), normalize(expected))
}
