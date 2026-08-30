package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

type postgresSchemaConstraint struct {
	kind    string
	columns []string
}

// CheckBusinessSQLiteSchema verifies the versioned Gateway dependency set
// without issuing DDL. It is intentionally local to Gateway because shared
// contracts may describe schema but must not own database I/O.
func CheckBusinessSQLiteSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("Business SQLite schema %s: database is nil", contracts.BusinessSQLiteSchemaVersion)
	}
	tables, err := sqliteSchemaObjects(ctx, db, "table")
	if err != nil {
		return fmt.Errorf("list Business SQLite tables: %w", err)
	}
	indexes, err := sqliteSchemaObjects(ctx, db, "index")
	if err != nil {
		return fmt.Errorf("list Business SQLite indexes: %w", err)
	}
	for table, spec := range contracts.BusinessSQLiteSchema {
		if !tables[table] {
			return fmt.Errorf("Business SQLite schema %s missing table %s", contracts.BusinessSQLiteSchemaVersion, table)
		}
		columns, err := sqliteSchemaColumns(ctx, db, table)
		if err != nil {
			return err
		}
		for _, required := range spec.Columns {
			if !columns[required] {
				return fmt.Errorf("Business SQLite schema %s missing column %s.%s", contracts.BusinessSQLiteSchemaVersion, table, required)
			}
		}
		if len(spec.PrimaryKey) > 0 {
			actual, err := sqliteSchemaPrimaryKey(ctx, db, table)
			if err != nil {
				return fmt.Errorf("inspect Business SQLite primary key %s: %w", table, err)
			}
			if !sameSchemaIndexColumns(actual, spec.PrimaryKey) {
				return fmt.Errorf("Business SQLite schema %s incompatible primary key %s: columns=%v want=%v", contracts.BusinessSQLiteSchemaVersion, table, actual, spec.PrimaryKey)
			}
		}
		for _, required := range spec.UniqueConstraints {
			ok, actual, err := sqliteSchemaHasUniqueConstraint(ctx, db, table, required)
			if err != nil {
				return fmt.Errorf("inspect Business SQLite unique constraint %s(%s): %w", table, strings.Join(required, ","), err)
			}
			if !ok {
				return fmt.Errorf("Business SQLite schema %s missing unique constraint %s(%s), observed=%v", contracts.BusinessSQLiteSchemaVersion, table, strings.Join(required, ","), actual)
			}
		}
		for _, required := range spec.Indexes {
			if !indexes[required] {
				return fmt.Errorf("Business SQLite schema %s missing index %s", contracts.BusinessSQLiteSchemaVersion, required)
			}
		}
		for _, required := range spec.IndexDefinitions {
			ok, detail, err := sqliteSchemaIndexMatches(ctx, db, table, required)
			if err != nil {
				return fmt.Errorf("inspect Business SQLite index %s.%s: %w", table, required.Name, err)
			}
			if !ok {
				return fmt.Errorf("Business SQLite schema %s incompatible index %s.%s: %s", contracts.BusinessSQLiteSchemaVersion, table, required.Name, detail)
			}
		}
		foreignKeys, err := sqliteSchemaForeignKeys(ctx, db, table)
		if err != nil {
			return err
		}
		for _, required := range spec.ForeignKeys {
			if !foreignKeys[businessSQLiteForeignKeySignature(table, required)] {
				return fmt.Errorf("Business SQLite schema %s missing foreign key %s", contracts.BusinessSQLiteSchemaVersion, businessSQLiteForeignKeySignature(table, required))
			}
		}
	}
	return nil
}

// CheckBusinessPostgresSchema verifies the same versioned dependency set for
// a PostgreSQL Business schema. It is intentionally read-only and only
// consumes information_schema plus pg_catalog constraints/indexes; schema
// lifecycle remains owned by maintenance and no DDL is issued here.
func CheckBusinessPostgresSchema(ctx context.Context, db *sql.DB, schema string) error {
	if db == nil {
		return fmt.Errorf("Business PostgreSQL schema %s: database is nil", contracts.BusinessSQLiteSchemaVersion)
	}
	schema = strings.TrimSpace(schema)
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(schema) {
		return fmt.Errorf("Business PostgreSQL schema %s: schema name is invalid", contracts.BusinessSQLiteSchemaVersion)
	}
	rows, err := db.QueryContext(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema=$1`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL columns: %w", err)
	}
	defer rows.Close()
	columns := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return fmt.Errorf("scan Business PostgreSQL columns: %w", err)
		}
		if columns[table] == nil {
			columns[table] = map[string]bool{}
		}
		columns[table][column] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Business PostgreSQL columns: %w", err)
	}
	if len(columns) == 0 {
		return fmt.Errorf("Business PostgreSQL schema %s contains no tables", schema)
	}
	tables := make([]string, 0, len(contracts.BusinessSQLiteSchema))
	for table := range contracts.BusinessSQLiteSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		spec := contracts.BusinessSQLiteSchema[table]
		actual := columns[table]
		if actual == nil {
			return fmt.Errorf("Business PostgreSQL schema %s missing table %s", schema, table)
		}
		for _, required := range spec.Columns {
			if !actual[required] {
				return fmt.Errorf("Business PostgreSQL schema %s missing column %s.%s", schema, table, required)
			}
		}
	}
	constraintRows, err := db.QueryContext(ctx, `
SELECT c.relname, con.oid, con.contype, a.attname, keys.ordinality
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class AS c ON c.oid=con.conrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid=c.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS keys(attnum, ordinality) ON TRUE
JOIN pg_catalog.pg_attribute AS a ON a.attrelid=c.oid AND a.attnum=keys.attnum
WHERE n.nspname=$1 AND con.contype IN ('p','u')
ORDER BY c.relname, con.oid, keys.ordinality`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL constraints: %w", err)
	}
	constraints := map[string]map[int64]*postgresSchemaConstraint{}
	for constraintRows.Next() {
		var table, kind, column string
		var oid, ordinal int64
		if err := constraintRows.Scan(&table, &oid, &kind, &column, &ordinal); err != nil {
			constraintRows.Close()
			return fmt.Errorf("scan Business PostgreSQL constraints: %w", err)
		}
		if constraints[table] == nil {
			constraints[table] = map[int64]*postgresSchemaConstraint{}
		}
		item := constraints[table][oid]
		if item == nil {
			item = &postgresSchemaConstraint{kind: kind}
			constraints[table][oid] = item
		}
		item.columns = append(item.columns, column)
	}
	if err := constraintRows.Err(); err != nil {
		constraintRows.Close()
		return fmt.Errorf("iterate Business PostgreSQL constraints: %w", err)
	}
	if err := constraintRows.Close(); err != nil {
		return fmt.Errorf("close Business PostgreSQL constraints: %w", err)
	}
	for _, table := range tables {
		spec := contracts.BusinessSQLiteSchema[table]
		actual := constraints[table]
		if len(spec.PrimaryKey) > 0 && !hasPostgresConstraint(actual, "p", spec.PrimaryKey) {
			return fmt.Errorf("Business PostgreSQL schema %s missing primary key %s(%s)", schema, table, strings.Join(spec.PrimaryKey, ","))
		}
		for _, required := range spec.UniqueConstraints {
			if !hasPostgresConstraint(actual, "u", required) {
				return fmt.Errorf("Business PostgreSQL schema %s missing unique constraint %s(%s)", schema, table, strings.Join(required, ","))
			}
		}
	}
	indexRows, err := db.QueryContext(ctx, `SELECT tablename,indexname FROM pg_catalog.pg_indexes WHERE schemaname=$1`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL indexes: %w", err)
	}
	defer indexRows.Close()
	indexes := map[string]map[string]bool{}
	for indexRows.Next() {
		var table, name string
		if err := indexRows.Scan(&table, &name); err != nil {
			return fmt.Errorf("scan Business PostgreSQL indexes: %w", err)
		}
		if indexes[table] == nil {
			indexes[table] = map[string]bool{}
		}
		indexes[table][name] = true
	}
	if err := indexRows.Err(); err != nil {
		return fmt.Errorf("iterate Business PostgreSQL indexes: %w", err)
	}
	for _, table := range tables {
		spec := contracts.BusinessSQLiteSchema[table]
		for _, required := range spec.Indexes {
			if !indexes[table][required] {
				return fmt.Errorf("Business PostgreSQL schema %s missing index %s.%s", schema, table, required)
			}
		}
		for _, required := range spec.IndexDefinitions {
			if !indexes[table][required.Name] {
				return fmt.Errorf("Business PostgreSQL schema %s missing index %s.%s", schema, table, required.Name)
			}
		}
	}
	return nil
}

func sqliteSchemaObjects(ctx context.Context, db *sql.DB, objectType string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type=?", objectType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		objects[name] = true
	}
	return objects, rows.Err()
}

func sqliteSchemaColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite table %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func sqliteSchemaPrimaryKey(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite primary key for table %s: %w", table, err)
	}
	defer rows.Close()
	type column struct {
		name string
		seq  int
	}
	keys := make([]column, 0)
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan Business SQLite primary key for table %s: %w", table, err)
		}
		if pk > 0 {
			keys = append(keys, column{name: name, seq: pk})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].seq < keys[j].seq })
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.name)
	}
	return result, nil
}

func sqliteSchemaHasUniqueConstraint(ctx context.Context, db *sql.DB, table string, required []string) (bool, [][]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return false, nil, fmt.Errorf("inspect unique indexes for table %s: %w", table, err)
	}
	defer rows.Close()
	type uniqueIndex struct{ name string }
	indexes := make([]uniqueIndex, 0)
	observed := make([][]string, 0)
	for rows.Next() {
		var seq, uniqueFlag, partialFlag int
		var name, origin string
		if err := rows.Scan(&seq, &name, &uniqueFlag, &origin, &partialFlag); err != nil {
			return false, nil, fmt.Errorf("scan unique indexes for table %s: %w", table, err)
		}
		if uniqueFlag != 1 || partialFlag == 1 {
			continue
		}
		indexes = append(indexes, uniqueIndex{name: name})
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	if err := rows.Close(); err != nil {
		return false, nil, err
	}
	for _, index := range indexes {
		columns, err := sqliteSchemaIndexColumns(ctx, db, index.name)
		if err != nil {
			return false, nil, err
		}
		observed = append(observed, columns)
		if sameSchemaIndexColumns(columns, required) {
			return true, observed, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	return false, observed, nil
}

func sqliteSchemaIndexColumns(ctx context.Context, db *sql.DB, requestedName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_info("+quoteSQLiteIdentifier(requestedName)+")")
	if err != nil {
		return nil, fmt.Errorf("inspect Business SQLite index %s: %w", requestedName, err)
	}
	defer rows.Close()
	type column struct {
		seq  int
		name string
	}
	columns := make([]column, 0)
	for rows.Next() {
		var seq, cid int
		var columnName sql.NullString
		if err := rows.Scan(&seq, &cid, &columnName); err != nil {
			return nil, fmt.Errorf("scan Business SQLite index %s: %w", requestedName, err)
		}
		if !columnName.Valid {
			return nil, fmt.Errorf("Business SQLite index %s contains an expression", requestedName)
		}
		columns = append(columns, column{seq: seq, name: columnName.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].seq < columns[j].seq })
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		result = append(result, column.name)
	}
	return result, nil
}

func hasPostgresConstraint(constraints map[int64]*postgresSchemaConstraint, kind string, required []string) bool {
	for _, constraint := range constraints {
		if constraint.kind == kind && sameSchemaIndexColumns(constraint.columns, required) {
			return true
		}
	}
	return false
}

func sqliteSchemaForeignKeys(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteSQLiteIdentifier(table)+")")
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
	groups := map[int][]relation{}
	for rows.Next() {
		var item relation
		var match string
		if err := rows.Scan(&item.id, &item.seq, &item.table, &item.from, &item.to, &item.onUpdate, &item.onDelete, &match); err != nil {
			return nil, fmt.Errorf("scan Business SQLite foreign keys for table %s: %w", table, err)
		}
		groups[item.id] = append(groups[item.id], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	foreignKeys := map[string]bool{}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].seq < group[j].seq })
		fromColumns := make([]string, 0, len(group))
		toColumns := make([]string, 0, len(group))
		for _, item := range group {
			fromColumns = append(fromColumns, item.from)
			toColumns = append(toColumns, item.to)
		}
		foreignKeys[fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, strings.Join(fromColumns, ","), group[0].table, strings.Join(toColumns, ","), group[0].onDelete, group[0].onUpdate)] = true
	}
	return foreignKeys, nil
}

func businessSQLiteForeignKeySignature(table string, spec contracts.SQLiteForeignKeySpec) string {
	onDelete := spec.OnDelete
	if onDelete == "" {
		onDelete = "NO ACTION"
	}
	onUpdate := spec.OnUpdate
	if onUpdate == "" {
		onUpdate = "NO ACTION"
	}
	return fmt.Sprintf("%s(%s)->%s(%s) onDelete=%s onUpdate=%s", table, strings.Join(spec.Columns, ","), spec.RefTable, strings.Join(spec.RefColumns, ","), onDelete, onUpdate)
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqliteSchemaIndexMatches(ctx context.Context, db *sql.DB, table string, required contracts.SQLiteIndexDefinition) (bool, string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return false, "read index_list failed", err
	}
	defer rows.Close()
	var found, unique, partial bool
	for rows.Next() {
		var seq, uniqueFlag, partialFlag int
		var name, origin string
		if err := rows.Scan(&seq, &name, &uniqueFlag, &origin, &partialFlag); err != nil {
			return false, "scan index_list failed", err
		}
		if name == required.Name {
			found, unique, partial = true, uniqueFlag == 1, partialFlag == 1
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
	colRows, err := db.QueryContext(ctx, "PRAGMA index_info("+quoteSQLiteIdentifier(required.Name)+")")
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
	if !sameSchemaIndexColumns(columns, required.Columns) {
		return false, fmt.Sprintf("columns=%v want=%v", columns, required.Columns), nil
	}
	var definition sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='index' AND name=?", required.Name).Scan(&definition); err != nil {
		return false, "read sqlite_master definition failed", err
	}
	actualPredicate := sqliteSchemaIndexPredicate(definition.String)
	if strings.TrimSpace(required.Predicate) == "" {
		if partial || actualPredicate != "" {
			return false, "unexpected partial predicate", nil
		}
	} else if !partial || !schemaIndexPredicatesEquivalent(actualPredicate, required.Predicate) {
		return false, fmt.Sprintf("predicate=%q want=%q", actualPredicate, required.Predicate), nil
	}
	return true, "", nil
}

func sameSchemaIndexColumns(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

func sqliteSchemaIndexPredicate(sqlText string) string {
	lower := strings.ToLower(sqlText)
	idx := strings.Index(lower, " where ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(sqlText[idx+7:])
}

func schemaIndexPredicatesEquivalent(actual, expected string) bool {
	normalize := func(value string) []string {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.NewReplacer("`", "", "[", "", "]", "", `"`, "").Replace(value)
		value = regexp.MustCompile(`::[a-z0-9_]+`).ReplaceAllString(value, "")
		value = strings.ReplaceAll(strings.ReplaceAll(value, "(", ""), ")", "")
		value = strings.Join(strings.Fields(value), " ")
		parts := strings.Split(value, " and ")
		for i := range parts {
			parts[i] = strings.Join(strings.Fields(parts[i]), " ")
		}
		sort.Strings(parts)
		return parts
	}
	return sameSchemaIndexColumns(normalize(actual), normalize(expected))
}
