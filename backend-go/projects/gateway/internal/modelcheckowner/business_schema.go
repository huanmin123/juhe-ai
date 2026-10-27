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

type postgresSchemaForeignKey struct {
	refSchema  string
	refTable   string
	columns    []string
	refColumns []string
	onDelete   string
	onUpdate   string
}

type postgresSchemaIndex struct {
	unique     bool
	columns    []string
	expression bool
	predicate  string
}

func CheckBusinessPostgresSchema(ctx context.Context, db *sql.DB, schema string) error {
	if db == nil {
		return fmt.Errorf("Business PostgreSQL schema %s: database is nil", contracts.BusinessSchemaVersion)
	}
	schema = strings.TrimSpace(schema)
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(schema) {
		return fmt.Errorf("Business PostgreSQL schema %s: schema name is invalid", contracts.BusinessSchemaVersion)
	}
	tableRows, err := db.QueryContext(ctx, `
SELECT tables.table_name, tables.table_type, relation.relkind::text
FROM information_schema.tables AS tables
JOIN pg_catalog.pg_class AS relation ON relation.relname=tables.table_name
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid=relation.relnamespace AND namespace.nspname=tables.table_schema
WHERE tables.table_schema=$1`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL tables: %w", err)
	}
	tablesByName := map[string]struct {
		tableType string
		relkind   string
	}{}
	for tableRows.Next() {
		var table, tableType, relkind string
		if err := tableRows.Scan(&table, &tableType, &relkind); err != nil {
			tableRows.Close()
			return fmt.Errorf("scan Business PostgreSQL tables: %w", err)
		}
		tablesByName[table] = struct {
			tableType string
			relkind   string
		}{tableType: tableType, relkind: relkind}
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return fmt.Errorf("iterate Business PostgreSQL tables: %w", err)
	}
	// rows.Err() 已吸收 EOF 后驱动 Close 错误，显式 Close 恒为 nil（w14m 甄别：不可达防御臂）。
	_ = tableRows.Close()
	if len(tablesByName) == 0 {
		return fmt.Errorf("Business PostgreSQL schema %s contains no tables", schema)
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
	tables := make([]string, 0, len(contracts.BusinessSchema))
	for table := range contracts.BusinessSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		spec := contracts.BusinessSchema[table]
		tableShape, tableExists := tablesByName[table]
		if !tableExists {
			return fmt.Errorf("Business PostgreSQL schema %s missing table %s", schema, table)
		}
		if tableShape.tableType != "BASE TABLE" {
			return fmt.Errorf("Business PostgreSQL schema %s table %s has type %s, want BASE TABLE", schema, table, tableShape.tableType)
		}
		// Ordinary and partitioned tables are both represented as BASE TABLE by
		// information_schema. Views, materialized views, and foreign tables must
		// never satisfy this owner contract.
		if tableShape.relkind != "r" && tableShape.relkind != "p" {
			return fmt.Errorf("Business PostgreSQL schema %s table %s has relkind %s, want r or p", schema, table, tableShape.relkind)
		}
		actual := columns[table]
		if actual == nil {
			return fmt.Errorf("Business PostgreSQL schema %s table %s contains no columns", schema, table)
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
	// 同上：显式 Close 错误不可达（w14m 甄别）。
	_ = constraintRows.Close()
	for _, table := range tables {
		spec := contracts.BusinessSchema[table]
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
	foreignKeyRows, err := db.QueryContext(ctx, `
SELECT source.relname, con.oid, ref_namespace.nspname, referenced_relation.relname,
       con.confdeltype::text, con.confupdtype::text,
       source_attribute.attname, reference_attribute.attname, keys.ordinality
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class AS source ON source.oid=con.conrelid
JOIN pg_catalog.pg_namespace AS source_namespace ON source_namespace.oid=source.relnamespace
JOIN pg_catalog.pg_class AS referenced_relation ON referenced_relation.oid=con.confrelid
JOIN pg_catalog.pg_namespace AS ref_namespace ON ref_namespace.oid=referenced_relation.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS keys(attnum, ordinality) ON TRUE
JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS refkeys(attnum, ordinality) ON refkeys.ordinality=keys.ordinality
JOIN pg_catalog.pg_attribute AS source_attribute ON source_attribute.attrelid=source.oid AND source_attribute.attnum=keys.attnum
JOIN pg_catalog.pg_attribute AS reference_attribute ON reference_attribute.attrelid=referenced_relation.oid AND reference_attribute.attnum=refkeys.attnum
WHERE source_namespace.nspname=$1 AND con.contype='f'
ORDER BY source.relname, con.oid, keys.ordinality`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL foreign keys: %w", err)
	}
	foreignKeys := map[string]map[int64]*postgresSchemaForeignKey{}
	for foreignKeyRows.Next() {
		var table, refSchema, refTable, onDeleteCode, onUpdateCode, column, refColumn string
		var oid, ordinal int64
		if err := foreignKeyRows.Scan(&table, &oid, &refSchema, &refTable, &onDeleteCode, &onUpdateCode, &column, &refColumn, &ordinal); err != nil {
			foreignKeyRows.Close()
			return fmt.Errorf("scan Business PostgreSQL foreign keys: %w", err)
		}
		if foreignKeys[table] == nil {
			foreignKeys[table] = map[int64]*postgresSchemaForeignKey{}
		}
		item := foreignKeys[table][oid]
		if item == nil {
			item = &postgresSchemaForeignKey{
				refSchema: refSchema,
				refTable:  refTable,
				onDelete:  postgresReferentialAction(onDeleteCode),
				onUpdate:  postgresReferentialAction(onUpdateCode),
			}
			foreignKeys[table][oid] = item
		}
		item.columns = append(item.columns, column)
		item.refColumns = append(item.refColumns, refColumn)
	}
	if err := foreignKeyRows.Err(); err != nil {
		foreignKeyRows.Close()
		return fmt.Errorf("iterate Business PostgreSQL foreign keys: %w", err)
	}
	// 同上：显式 Close 错误不可达（w14m 甄别）。
	_ = foreignKeyRows.Close()
	for _, table := range tables {
		spec := contracts.BusinessSchema[table]
		for _, required := range spec.ForeignKeys {
			if !hasPostgresForeignKey(foreignKeys[table], schema, required) {
				return fmt.Errorf("Business PostgreSQL schema %s missing foreign key %s", schema, schemaForeignKeySignature(table, required))
			}
		}
	}
	indexRows, err := db.QueryContext(ctx, `
SELECT relation.relname, index_relation.relname, index_data.indisunique,
       attribute.attname, keys.ordinality,
       COALESCE(pg_catalog.pg_get_expr(index_data.indpred, index_data.indrelid), '')
FROM pg_catalog.pg_index AS index_data
JOIN pg_catalog.pg_class AS relation ON relation.oid=index_data.indrelid
JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid=index_data.indexrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid=relation.relnamespace
JOIN LATERAL unnest(index_data.indkey) WITH ORDINALITY AS keys(attnum, ordinality) ON TRUE
LEFT JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid=relation.oid AND attribute.attnum=keys.attnum
WHERE namespace.nspname=$1
ORDER BY relation.relname, index_relation.relname, keys.ordinality`, schema)
	if err != nil {
		return fmt.Errorf("list Business PostgreSQL indexes: %w", err)
	}
	indexes := map[string]map[string]*postgresSchemaIndex{}
	for indexRows.Next() {
		var table, name string
		var unique bool
		var column sql.NullString
		var ordinal int64
		var predicate string
		if err := indexRows.Scan(&table, &name, &unique, &column, &ordinal, &predicate); err != nil {
			indexRows.Close()
			return fmt.Errorf("scan Business PostgreSQL indexes: %w", err)
		}
		if indexes[table] == nil {
			indexes[table] = map[string]*postgresSchemaIndex{}
		}
		item := indexes[table][name]
		if item == nil {
			item = &postgresSchemaIndex{unique: unique, predicate: predicate}
			indexes[table][name] = item
		}
		if !column.Valid {
			item.expression = true
		} else {
			item.columns = append(item.columns, column.String)
		}
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return fmt.Errorf("iterate Business PostgreSQL indexes: %w", err)
	}
	// 同上：显式 Close 错误不可达（w14m 甄别）。
	_ = indexRows.Close()
	for _, table := range tables {
		spec := contracts.BusinessSchema[table]
		for _, required := range spec.Indexes {
			if indexes[table][required] == nil {
				return fmt.Errorf("Business PostgreSQL schema %s missing index %s.%s", schema, table, required)
			}
		}
		for _, required := range spec.IndexDefinitions {
			actual := indexes[table][required.Name]
			if actual == nil {
				return fmt.Errorf("Business PostgreSQL schema %s missing index %s.%s", schema, table, required.Name)
			}
			if detail := postgresSchemaIndexMismatch(*actual, required); detail != "" {
				return fmt.Errorf("Business PostgreSQL schema %s incompatible index %s.%s: %s", schema, table, required.Name, detail)
			}
		}
	}
	return nil
}

// schemaForeignKeySignature 渲染契约外键的可读签名（错误信息共用；原
// businessSQLiteForeignKeySignature 更名——PG 校验路径同样消费）。
func schemaForeignKeySignature(table string, spec contracts.BusinessForeignKeySpec) string {
	return fmt.Sprintf("%s(%s)->%s(%s)", table, strings.Join(spec.Columns, ","), spec.RefTable, strings.Join(spec.RefColumns, ","))
}

func hasPostgresConstraint(constraints map[int64]*postgresSchemaConstraint, kind string, required []string) bool {
	for _, constraint := range constraints {
		if constraint.kind == kind && sameSchemaIndexColumns(constraint.columns, required) {
			return true
		}
	}
	return false
}

func hasPostgresForeignKey(foreignKeys map[int64]*postgresSchemaForeignKey, schema string, required contracts.BusinessForeignKeySpec) bool {
	wantDelete := required.OnDelete
	if wantDelete == "" {
		wantDelete = "NO ACTION"
	}
	wantUpdate := required.OnUpdate
	if wantUpdate == "" {
		wantUpdate = "NO ACTION"
	}
	for _, actual := range foreignKeys {
		if actual.refSchema != schema || actual.refTable != required.RefTable {
			continue
		}
		if !sameSchemaIndexColumns(actual.columns, required.Columns) || !sameSchemaIndexColumns(actual.refColumns, required.RefColumns) {
			continue
		}
		if !strings.EqualFold(actual.onDelete, wantDelete) || !strings.EqualFold(actual.onUpdate, wantUpdate) {
			continue
		}
		return true
	}
	return false
}

func postgresReferentialAction(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	default:
		return ""
	}
}

func postgresSchemaIndexMismatch(actual postgresSchemaIndex, required contracts.BusinessIndexDefinition) string {
	if actual.expression {
		return "index contains an expression"
	}
	if actual.unique != required.Unique {
		return fmt.Sprintf("unique=%t want=%t", actual.unique, required.Unique)
	}
	if !sameSchemaIndexColumns(actual.columns, required.Columns) {
		return fmt.Sprintf("columns=%v want=%v", actual.columns, required.Columns)
	}
	actualPredicate := strings.TrimSpace(actual.predicate)
	if strings.TrimSpace(required.Predicate) == "" {
		if actualPredicate != "" {
			return fmt.Sprintf("predicate=%q want none", actualPredicate)
		}
	} else if !schemaIndexPredicatesEquivalent(actualPredicate, required.Predicate) {
		return fmt.Sprintf("predicate=%q want=%q", actualPredicate, required.Predicate)
	}
	return ""
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
