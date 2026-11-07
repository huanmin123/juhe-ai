package schema

import "testing"

func TestConcurrentIndexSQL(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX IF NOT EXISTS idx_a ON t(a)":               "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_a ON t(a)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_b ON t(lower(b))": "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_b ON t(lower(b))",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_c ON t(c)":  "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_c ON t(c)",
		"CREATE TABLE t (id text)":                               "CREATE TABLE t (id text)",
	}
	for input, want := range cases {
		if got := concurrentIndexSQL(input); got != want {
			t.Fatalf("concurrentIndexSQL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIndexNameFromStatement(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX IF NOT EXISTS idx_a ON t(a)":               "idx_a",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_b ON t(lower(b))": "idx_b",
		"CREATE INDEX idx_c ON t(c)":                             "idx_c",
	}
	for input, want := range cases {
		got, ok := indexNameFromStatement(input)
		if !ok || got != want {
			t.Fatalf("indexNameFromStatement(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := indexNameFromStatement("CREATE TABLE t (id text)"); ok {
		t.Fatal("非索引语句不应解析出索引名")
	}
}

func TestParseCreateIndexStatement(t *testing.T) {
	statement := `CREATE INDEX IF NOT EXISTS idx_usage_records_stats_pending ON usage_records(created_at, id) WHERE usage_stats_aggregated_at IS NULL`
	spec, ok := parseCreateIndexStatement(statement, "juhe_usage")
	if !ok {
		t.Fatal("应能解析分区表索引语句")
	}
	if spec.Unique || spec.IndexName != "idx_usage_records_stats_pending" ||
		spec.TableSchema != "juhe_usage" || spec.TableName != "usage_records" {
		t.Fatalf("解析结果不符: %+v", spec)
	}
	wantSuffix := `(created_at, id) WHERE usage_stats_aggregated_at IS NULL`
	if spec.Suffix != wantSuffix {
		t.Fatalf("Suffix = %q, want %q", spec.Suffix, wantSuffix)
	}
	if got := parentIndexSQL(statement, spec); got != `CREATE INDEX IF NOT EXISTS idx_usage_records_stats_pending ON ONLY "juhe_usage"."usage_records" (created_at, id) WHERE usage_stats_aggregated_at IS NULL` {
		t.Fatalf("parentIndexSQL = %q", got)
	}
	partIndex := partitionIndexName(spec.IndexName, "juhe_usage", "usage_records_202601")
	if len(partIndex) > 63 {
		t.Fatalf("分区索引名超长: %d", len(partIndex))
	}
	if got := partitionIndexSQL(spec, "juhe_usage", "usage_records_202601", partIndex); got != `CREATE INDEX CONCURRENTLY IF NOT EXISTS "`+partIndex+`" ON "juhe_usage"."usage_records_202601" (created_at, id) WHERE usage_stats_aggregated_at IS NULL` {
		t.Fatalf("partitionIndexSQL = %q", got)
	}
}

func TestParseCreateIndexStatementQualifiedUnique(t *testing.T) {
	statement := `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS "idx_u" ON "other"."events" (lower(name))`
	spec, ok := parseCreateIndexStatement(statement, "juhe_usage")
	if !ok {
		t.Fatal("应能解析带 schema 的唯一索引")
	}
	if !spec.Unique || spec.TableSchema != "other" || spec.TableName != "events" || spec.Suffix != `(lower(name))` {
		t.Fatalf("解析结果不符: %+v", spec)
	}
	if got := parentIndexSQL(statement, spec); got != `CREATE UNIQUE INDEX IF NOT EXISTS "idx_u" ON ONLY "other"."events" (lower(name))` {
		t.Fatalf("parentIndexSQL = %q", got)
	}
}

func TestParseCreateIndexStatementUsingMethod(t *testing.T) {
	statement := `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_trgm_lookup ON accounts USING gin (name gin_trgm_ops)`
	spec, ok := parseCreateIndexStatement(statement, "juhe_business")
	if !ok {
		t.Fatal("应能解析 USING gin 索引")
	}
	if spec.TableSchema != "juhe_business" || spec.TableName != "accounts" || spec.Suffix != `USING gin (name gin_trgm_ops)` {
		t.Fatalf("解析结果不符: %+v", spec)
	}
	if got := parentIndexSQL(statement, spec); got != `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_trgm_lookup ON ONLY "juhe_business"."accounts" USING gin (name gin_trgm_ops)` {
		t.Fatalf("parentIndexSQL = %q", got)
	}
}

func TestParseAllSchemaIndexStatements(t *testing.T) {
	for i, statement := range postgresSchemaStatements {
		if !isIndexStatement(statement.SQL) {
			continue
		}
		if _, ok := parseCreateIndexStatement(statement.SQL, statement.SchemaName); !ok {
			t.Fatalf("statement %d (%s/%s) 无法解析: %s", i, statement.SchemaName, statement.Source, statement.SQL)
		}
	}
}

func TestIsIndexStatement(t *testing.T) {
	for _, input := range []string{
		"CREATE INDEX idx ON t(a)",
		"CREATE UNIQUE INDEX idx ON t(a)",
	} {
		if !isIndexStatement(input) {
			t.Fatalf("%q 应判定为索引语句", input)
		}
	}
	for _, input := range []string{
		"CREATE TABLE t (id text)",
		"ALTER TABLE t ADD COLUMN c text",
		"DROP INDEX IF EXISTS idx",
	} {
		if isIndexStatement(input) {
			t.Fatalf("%q 不应判定为索引语句", input)
		}
	}
}
