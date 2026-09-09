package schemasnapshot

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The expected digests below were computed independently of this package with
// `printf '%s' '<stableJson output>' | sha256sum`, mirroring the archived
// Node implementation exactly.

func TestStableJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "null"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"empty string", "", `""`},
		{"plain string", "public", `"public"`},
		{
			// JS JSON.stringify escaping: ", \, control characters; all
			// other runes (including non-ASCII) stay literal.
			name:  "escaped string",
			value: "a\"b\\c\nd\re\tf\x01\x1f聚合",
			want:  "\"a\\\"b\\\\c\\nd\\re\\tf\\u0001\\u001f聚合\"",
		},
		{"sorted keys", map[string]any{"b": 1, "a": true}, `{"a":true,"b":1}`},
		{"nested", map[string]any{"z": []any{1, "x", nil, map[string]any{"k": 2}}}, `{"z":[1,"x",null,{"k":2}]}`},
		{"empty array", []any{}, `[]`},
		{"empty object", map[string]any{}, `{}`},
		{"int64 text", int64(9007199254740993), "9007199254740993"},
		{
			name:  "struct normalizes via json tags",
			value: struct {
				A int     `json:"a"`
				B *string `json:"b"`
			}{A: 1},
			want: `{"a":1,"b":null}`,
		},
		{
			name:  "pointer to struct dereferences",
			value: (*struct{ X string `json:"x"` })(nil),
			want:  "null",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StableJSON(tc.value)
			if err != nil {
				t.Fatalf("StableJSON() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("StableJSON() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStableJSONUnsupportedType(t *testing.T) {
	if _, err := StableJSON(make(chan int)); err == nil {
		t.Fatal("StableJSON(chan int) 应返回错误")
	}
}

func TestDigestDefinition(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			// sha256sum of the empty string mirrors Node String(undefined ?? '').
			name:  "nil becomes empty text",
			value: nil,
			want:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:  "definition text",
			value: "CREATE INDEX idx_x ON t (id)",
			want:  "6a97357c6127230fc29aeee9fd919c3b2f56e56e13d7f8d2bedee99ea3ba301d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DigestDefinition(tc.value); got != tc.want {
				t.Fatalf("DigestDefinition(%v) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

func TestSnapshotDigestFixedSample(t *testing.T) {
	snapshot := SchemaSnapshot{
		SchemaVersion: 1,
		Target:        TargetProduction,
		CapturedAt:    "2026-01-01T00:00:00.000Z",
		Database:      DatabaseInfo{Name: "db", OID: "16384", ServerPort: intPtr(5432)},
		Schemas:       []SchemaEntry{{Name: "public", Owner: "postgres"}},
		Roles:         []RoleEntry{{Name: "juhe_app", CreateRole: true, CanLogin: true}},
	}
	// 期望值按归档 snapshotDigest 算法手工复核：剔除 target/capturedAt/
	// database 后对 schema-only 部分做 stableJson（键序稳定、紧凑分隔符）
	// 再取 sha256，与 Node postgres-schema-snapshot.ts:88-91 逐字节一致。
	want := "ad6a0ffe0d03949c3df76c5153151192250872c1e4ff4ae63663e9e3415b349c"
	got, err := SnapshotDigest(snapshot)
	if err != nil {
		t.Fatalf("SnapshotDigest() error = %v", err)
	}
	if got != want {
		t.Fatalf("SnapshotDigest() = %s, want %s", got, want)
	}
}

func TestSnapshotDigestExcludesEnvironment(t *testing.T) {
	base := SchemaSnapshot{
		SchemaVersion: 1,
		Target:        TargetProduction,
		CapturedAt:    "2026-01-01T00:00:00.000Z",
		Database:      DatabaseInfo{Name: "db", OID: "16384"},
		Schemas:       []SchemaEntry{{Name: "public", Owner: "postgres"}},
	}
	baseDigest, err := SnapshotDigest(base)
	if err != nil {
		t.Fatalf("SnapshotDigest() error = %v", err)
	}
	variants := []struct {
		name  string
		mutate func(*SchemaSnapshot)
	}{
		{"target", func(s *SchemaSnapshot) { s.Target = TargetTest }},
		{"capturedAt", func(s *SchemaSnapshot) { s.CapturedAt = "2030-06-01T12:00:00.000Z" }},
		{"database", func(s *SchemaSnapshot) { s.Database = DatabaseInfo{Name: "other", OID: "99999", ServerAddress: strPtr("10.0.0.9")} }},
		{"digest placeholder", func(s *SchemaSnapshot) { s.Digest = "stale" }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			mutated := base
			variant.mutate(&mutated)
			got, err := SnapshotDigest(mutated)
			if err != nil {
				t.Fatalf("SnapshotDigest() error = %v", err)
			}
			if got != baseDigest {
				t.Fatalf("digest changed for %s: %s != %s", variant.name, got, baseDigest)
			}
		})
	}
}

func TestAssertSnapshotTarget(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "production", value: TargetProduction, want: TargetProduction},
		{name: "test", value: TargetTest, want: TargetTest},
		{name: "empty", value: "", wantError: true},
		{name: "uppercase production", value: "Production", wantError: true},
		{name: "prod abbreviation", value: "prod", wantError: true},
		{name: "development", value: "development", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AssertSnapshotTarget(tc.value)
			if tc.wantError {
				if err == nil {
					t.Fatalf("AssertSnapshotTarget(%q) 应返回错误", tc.value)
				}
				if err.Error() != "JUHE_AI_SCHEMA_SNAPSHOT_TARGET 必须是 production 或 test" {
					t.Fatalf("AssertSnapshotTarget(%q) 错误消息 = %q", tc.value, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("AssertSnapshotTarget(%q) error = %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("AssertSnapshotTarget(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestCollectSnapshotAssemblesRowsAndDigest(t *testing.T) {
	aclDigest := DigestDefinition("value-acl")
	defaultDigest := DigestDefinition("'x'::text")

	conn := &fakeConn{routes: map[string]fakeResult{
		"pg_get_userbyid(n.nspowner)": {
			columns: []string{"name", "owner", "acl"},
			rows: [][]driver.Value{
				{"public", "postgres", nil},
				{"juhe_business", "juhe_app", "value-acl"},
			},
		},
		"current_database()": {
			columns: []string{"name", "oid", "serverAddress", "serverPort"},
			rows:    [][]driver.Value{{"snapdb", "16384", nil, int64(5432)}},
		},
		"pg_roles": {
			columns: []string{"name", "superuser", "createRole", "createDb", "canLogin", "replication", "bypassRls"},
			rows: [][]driver.Value{
				{"juhe_app", false, true, false, true, false, false},
				{"snap_owner", true, true, true, true, true, false},
			},
		},
		"pg_extension": {
			columns: []string{"name", "version", "schema"},
			rows:    [][]driver.Value{{"plpgsql", "1.0", nil}},
		},
		"c.relacl": {
			columns: []string{"schema", "name", "kind", "owner", "persistence", "acl"},
			rows: [][]driver.Value{
				{"public", "api_keys", "r", "postgres", "p", nil},
				{"juhe_business", "usage_daily", "p", "juhe_app", "p", "value-acl"},
			},
		},
		"pg_attribute": {
			columns: []string{"schema", "relation", "name", "ordinal", "type", "udt", "nullable", "default_definition"},
			rows: [][]driver.Value{
				{"public", "api_keys", "id", int64(1), "bigint", "int8", false, nil},
				{"public", "api_keys", "key", int64(2), "text", "text", true, "'x'::text"},
			},
		},
		"pg_constraint": {
			columns: []string{"schema", "relation", "name", "type", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "api_keys_pkey", "p", "PRIMARY KEY (id)"}},
		},
		"pg_index": {
			columns: []string{"schema", "relation", "name", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "api_keys_pkey", "CREATE UNIQUE INDEX api_keys_pkey ON public.api_keys USING btree (id)"}},
		},
		"pg_proc": {
			columns: []string{"schema", "name", "identityArguments", "definition"},
			rows:    [][]driver.Value{{"public", "touch_ts", "()", "CREATE OR REPLACE FUNCTION public.touch_ts() ..."}},
		},
		"pg_trigger": {
			columns: []string{"schema", "relation", "name", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "touch_ts_trigger", "CREATE TRIGGER touch_ts_trigger BEFORE UPDATE ON public.api_keys ..."}},
		},
		"pg_get_viewdef": {
			columns: []string{"schema", "name", "materialized", "definition"},
			rows:    [][]driver.Value{{"public", "active_keys", false, "SELECT id FROM public.api_keys"}},
		},
		"pg_inherits": {
			columns: []string{"schema", "relation", "parentSchema", "parentRelation"},
			rows:    [][]driver.Value{{"juhe_business", "usage_2026_09", "juhe_business", "usage_daily"}},
		},
		"relkind='S'": {
			columns: []string{"schema", "name", "owner"},
			rows:    [][]driver.Value{{"public", "api_keys_id_seq", "postgres"}},
		},
	}}

	db := sql.OpenDB(fakeConnector{conn: conn})
	defer db.Close()

	snapshot, err := CollectSnapshot(context.Background(), db, TargetProduction)
	if err != nil {
		t.Fatalf("CollectSnapshot() error = %v", err)
	}

	if snapshot.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", snapshot.SchemaVersion)
	}
	if snapshot.Target != TargetProduction {
		t.Fatalf("target = %q", snapshot.Target)
	}
	// database identity: nullable server address stays null, port becomes a number.
	if snapshot.Database.Name != "snapdb" || snapshot.Database.OID != "16384" {
		t.Fatalf("database identity = %+v", snapshot.Database)
	}
	if snapshot.Database.ServerAddress != nil {
		t.Fatalf("serverAddress = %v, want null", *snapshot.Database.ServerAddress)
	}
	if snapshot.Database.ServerPort == nil || *snapshot.Database.ServerPort != 5432 {
		t.Fatalf("serverPort = %v, want 5432", snapshot.Database.ServerPort)
	}

	// schemas: nullable acl stays null, non-null acl becomes a sha256 digest.
	if len(snapshot.Schemas) != 2 {
		t.Fatalf("schemas = %d entries, want 2", len(snapshot.Schemas))
	}
	if snapshot.Schemas[0].ACL != nil {
		t.Fatalf("schemas[0].aclSha256 = %v, want null", *snapshot.Schemas[0].ACL)
	}
	if snapshot.Schemas[1].ACL == nil || *snapshot.Schemas[1].ACL != aclDigest {
		t.Fatalf("schemas[1].aclSha256 = %v, want %s", snapshot.Schemas[1].ACL, aclDigest)
	}

	if !reflect.DeepEqual(snapshot.Roles, []RoleEntry{
		{Name: "juhe_app", CreateRole: true, CanLogin: true},
		{Name: "snap_owner", Superuser: true, CreateRole: true, CreateDb: true, CanLogin: true, Replication: true},
	}) {
		t.Fatalf("roles = %+v", snapshot.Roles)
	}
	if !reflect.DeepEqual(snapshot.Extensions, []ExtensionEntry{{Name: "plpgsql", Version: "1.0", Schema: nil}}) {
		t.Fatalf("extensions = %+v", snapshot.Extensions)
	}
	if !reflect.DeepEqual(snapshot.Relations, []RelationEntry{
		{Schema: "public", Name: "api_keys", Kind: "r", Owner: "postgres", Persistence: "p"},
		{Schema: "juhe_business", Name: "usage_daily", Kind: "p", Owner: "juhe_app", Persistence: "p", ACL: &aclDigest},
	}) {
		t.Fatalf("relations = %+v", snapshot.Relations)
	}
	if !reflect.DeepEqual(snapshot.Columns, []ColumnEntry{
		{Schema: "public", Relation: "api_keys", Name: "id", Ordinal: 1, Type: "bigint", UDT: "int8", Nullable: false},
		{Schema: "public", Relation: "api_keys", Name: "key", Ordinal: 2, Type: "text", UDT: "text", Nullable: true, DefaultSha: &defaultDigest},
	}) {
		t.Fatalf("columns = %+v", snapshot.Columns)
	}
	if len(snapshot.Constraints) != 1 || snapshot.Constraints[0].DefinitionSha256 != DigestDefinition("PRIMARY KEY (id)") {
		t.Fatalf("constraints = %+v", snapshot.Constraints)
	}
	if len(snapshot.Indexes) != 1 || snapshot.Indexes[0].DefinitionSha256 != DigestDefinition("CREATE UNIQUE INDEX api_keys_pkey ON public.api_keys USING btree (id)") {
		t.Fatalf("indexes = %+v", snapshot.Indexes)
	}
	if len(snapshot.Functions) != 1 || snapshot.Functions[0].IdentityArguments != "()" {
		t.Fatalf("functions = %+v", snapshot.Functions)
	}
	if len(snapshot.Triggers) != 1 || snapshot.Triggers[0].Relation != "api_keys" {
		t.Fatalf("triggers = %+v", snapshot.Triggers)
	}
	if len(snapshot.Views) != 1 || snapshot.Views[0].Materialized {
		t.Fatalf("views = %+v", snapshot.Views)
	}
	if !reflect.DeepEqual(snapshot.Partitions, []PartitionEntry{{Schema: "juhe_business", Relation: "usage_2026_09", ParentSchema: "juhe_business", ParentRelation: "usage_daily"}}) {
		t.Fatalf("partitions = %+v", snapshot.Partitions)
	}
	if !reflect.DeepEqual(snapshot.Sequences, []SequenceEntry{{Schema: "public", Name: "api_keys_id_seq", Owner: "postgres"}}) {
		t.Fatalf("sequences = %+v", snapshot.Sequences)
	}

	// The assembled digest must equal a recomputation over the same rows.
	recomputed, err := SnapshotDigest(snapshot)
	if err != nil {
		t.Fatalf("SnapshotDigest() error = %v", err)
	}
	if snapshot.Digest != recomputed {
		t.Fatalf("digest = %s, recomputed %s", snapshot.Digest, recomputed)
	}
	if len(snapshot.Digest) != 64 {
		t.Fatalf("digest length = %d, want 64 hex chars", len(snapshot.Digest))
	}
}

func TestCollectSnapshotRejectsEmptyUserSchemas(t *testing.T) {
	conn := &fakeConn{routes: map[string]fakeResult{
		"pg_get_userbyid(n.nspowner)": {columns: []string{"name", "owner", "acl"}, rows: [][]driver.Value{}},
	}}
	db := sql.OpenDB(fakeConnector{conn: conn})
	defer db.Close()
	if _, err := CollectSnapshot(context.Background(), db, TargetTest); err == nil || err.Error() != "目标数据库没有可审计的用户 schema" {
		t.Fatalf("CollectSnapshot() error = %v, want 用户 schema 为空错误", err)
	}
}

func TestCollectSnapshotRejectsMissingIdentity(t *testing.T) {
	conn := &fakeConn{routes: map[string]fakeResult{
		"pg_get_userbyid(n.nspowner)": {
			columns: []string{"name", "owner", "acl"},
			rows:    [][]driver.Value{{"public", "postgres", nil}},
		},
		"current_database()": {columns: []string{"name", "oid", "serverAddress", "serverPort"}, rows: [][]driver.Value{}},
	}}
	db := sql.OpenDB(fakeConnector{conn: conn})
	defer db.Close()
	if _, err := CollectSnapshot(context.Background(), db, TargetTest); err == nil || err.Error() != "无法读取 PostgreSQL 数据库身份" {
		t.Fatalf("CollectSnapshot() error = %v, want 数据库身份缺失错误", err)
	}
}

func TestSnapshotJSONFieldOrderMatchesNode(t *testing.T) {
	// Field order mirrors the Node object insertion order so the indented
	// output reads identically after JSON.stringify(snapshot, null, 2).
	snapshot := SchemaSnapshot{SchemaVersion: 1, Target: TargetTest, Schemas: []SchemaEntry{}}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(encoded)
	wantOrder := []string{
		`"schemaVersion"`, `"target"`, `"capturedAt"`, `"database"`, `"schemas"`,
		`"roles"`, `"extensions"`, `"relations"`, `"columns"`, `"constraints"`,
		`"indexes"`, `"functions"`, `"triggers"`, `"views"`, `"partitions"`,
		`"sequences"`, `"digest"`,
	}
	lastIndex := -1
	for _, key := range wantOrder {
		index := strings.Index(text, key)
		if index < 0 {
			t.Fatalf("输出缺少字段 %s: %s", key, text)
		}
		if index <= lastIndex {
			t.Fatalf("字段 %s 顺序错误: %s", key, text)
		}
		lastIndex = index
	}
}

func strPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }

type fakeResult struct {
	columns []string
	rows    [][]driver.Value
}

type fakeConn struct {
	routes map[string]fakeResult
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake snapshot driver: Prepare 不应被调用")
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake snapshot driver: Begin 不应被调用")
}

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	for key, result := range c.routes {
		if strings.Contains(query, key) {
			return &fakeRows{columns: result.columns, rows: result.rows}, nil
		}
	}
	return nil, fmt.Errorf("fake snapshot driver: unexpected query %.80s", query)
}

type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *fakeRows) Columns() []string { return r.columns }

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type fakeConnector struct {
	conn *fakeConn
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}

func (c fakeConnector) Driver() driver.Driver {
	return fakeDriver{}
}

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake snapshot driver: use sql.OpenDB")
}
