// Package schemasnapshot ports the read-only PostgreSQL schema snapshot
// operations tool from the archived Node backend
// (migration-backup/node/final-archive/backend/src/scripts/operations/postgres-schema-snapshot.ts).
//
// The output JSON contract (object shapes, field names, digest computation)
// is byte-compatible with the Node implementation so snapshots produced by
// either side can be compared directly:
//   - collectSnapshot replays the exact pg catalog query list and ORDER BY
//     clauses of the Node tool;
//   - stableJson reproduces the Node key-sorted, bigint-free serialization;
//   - snapshotDigest strips target/capturedAt/database and hashes the
//     schema-only remainder with sha256, exactly like the Node tool.
//
// PostgreSQL is the only supported dialect; SQLite targets are rejected with
// an explicit error by the maintenance CLI.
package schemasnapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Environment variables shared with the Node implementation.
const (
	// EnvTarget selects the audited environment: production or test.
	EnvTarget = "JUHE_AI_SCHEMA_SNAPSHOT_TARGET"
	// EnvPostgresURL carries the maintenance-scoped PostgreSQL URL.
	EnvPostgresURL = "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL"
	// EnvReadOnlyConfirm must be READ_ONLY before the tool talks to a server.
	EnvReadOnlyConfirm = "JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM"
	// ReadOnlyConfirmValue is the only accepted EnvReadOnlyConfirm value.
	ReadOnlyConfirmValue = "READ_ONLY"

	// SchemaVersion mirrors the Node schemaVersion: 1 literal.
	SchemaVersion = 1
)

// TargetProduction and TargetTest are the only allowed snapshot targets.
const (
	TargetProduction = "production"
	TargetTest       = "test"
)

// DatabaseInfo mirrors the Node SchemaSnapshot["database"].
type DatabaseInfo struct {
	Name          string  `json:"name"`
	OID           string  `json:"oid"`
	ServerAddress *string `json:"serverAddress"`
	ServerPort    *int    `json:"serverPort"`
}

// SchemaEntry mirrors the Node schemas[] rows.
type SchemaEntry struct {
	Name  string  `json:"name"`
	Owner string  `json:"owner"`
	ACL   *string `json:"aclSha256"`
}

// RoleEntry mirrors the Node roles[] rows.
type RoleEntry struct {
	Name       string `json:"name"`
	Superuser  bool   `json:"superuser"`
	CreateRole bool   `json:"createRole"`
	CreateDb   bool   `json:"createDb"`
	CanLogin   bool   `json:"canLogin"`
	Replication bool  `json:"replication"`
	BypassRls  bool   `json:"bypassRls"`
}

// ExtensionEntry mirrors the Node extensions[] rows.
type ExtensionEntry struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Schema  *string `json:"schema"`
}

// RelationEntry mirrors the Node relations[] rows.
type RelationEntry struct {
	Schema      string  `json:"schema"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Owner       string  `json:"owner"`
	Persistence string  `json:"persistence"`
	ACL         *string `json:"aclSha256"`
}

// ColumnEntry mirrors the Node columns[] rows.
type ColumnEntry struct {
	Schema       string  `json:"schema"`
	Relation     string  `json:"relation"`
	Name         string  `json:"name"`
	Ordinal      int     `json:"ordinal"`
	Type         string  `json:"type"`
	UDT          string  `json:"udt"`
	Nullable     bool    `json:"nullable"`
	DefaultSha   *string `json:"defaultSha256"`
}

// ConstraintEntry mirrors the Node constraints[] rows.
type ConstraintEntry struct {
	Schema           string `json:"schema"`
	Relation         string `json:"relation"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	DefinitionSha256 string `json:"definitionSha256"`
}

// IndexEntry mirrors the Node indexes[] rows.
type IndexEntry struct {
	Schema           string `json:"schema"`
	Relation         string `json:"relation"`
	Name             string `json:"name"`
	DefinitionSha256 string `json:"definitionSha256"`
}

// FunctionEntry mirrors the Node functions[] rows.
type FunctionEntry struct {
	Schema           string `json:"schema"`
	Name             string `json:"name"`
	IdentityArguments string `json:"identityArguments"`
	DefinitionSha256 string `json:"definitionSha256"`
}

// TriggerEntry mirrors the Node triggers[] rows.
type TriggerEntry struct {
	Schema           string `json:"schema"`
	Relation         string `json:"relation"`
	Name             string `json:"name"`
	DefinitionSha256 string `json:"definitionSha256"`
}

// ViewEntry mirrors the Node views[] rows.
type ViewEntry struct {
	Schema           string `json:"schema"`
	Name             string `json:"name"`
	Materialized     bool   `json:"materialized"`
	DefinitionSha256 string `json:"definitionSha256"`
}

// PartitionEntry mirrors the Node partitions[] rows.
type PartitionEntry struct {
	Schema         string `json:"schema"`
	Relation       string `json:"relation"`
	ParentSchema   string `json:"parentSchema"`
	ParentRelation string `json:"parentRelation"`
}

// SequenceEntry mirrors the Node sequences[] rows.
type SequenceEntry struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
}

// SchemaSnapshot mirrors the Node SchemaSnapshot output contract. Field order
// matches the Node object insertion order so the indented JSON output reads
// identically.
type SchemaSnapshot struct {
	SchemaVersion int               `json:"schemaVersion"`
	Target        string            `json:"target"`
	CapturedAt    string            `json:"capturedAt"`
	Database      DatabaseInfo      `json:"database"`
	Schemas       []SchemaEntry     `json:"schemas"`
	Roles         []RoleEntry       `json:"roles"`
	Extensions    []ExtensionEntry  `json:"extensions"`
	Relations     []RelationEntry   `json:"relations"`
	Columns       []ColumnEntry     `json:"columns"`
	Constraints   []ConstraintEntry `json:"constraints"`
	Indexes       []IndexEntry      `json:"indexes"`
	Functions     []FunctionEntry   `json:"functions"`
	Triggers      []TriggerEntry    `json:"triggers"`
	Views         []ViewEntry       `json:"views"`
	Partitions    []PartitionEntry  `json:"partitions"`
	Sequences     []SequenceEntry   `json:"sequences"`
	Digest        string            `json:"digest"`
}

// Queryable accepts both *sql.DB and *sql.Tx. The Node tool passes its
// transaction-bound client so the snapshot stays inside the READ ONLY
// transaction.
type Queryable interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// The pg catalog queries below are copied verbatim from the Node
// postgres-schema-snapshot.ts, including the ORDER BY clauses that define the
// snapshot array order and therefore the digest.
const userSchemaSQL = `
  SELECT n.nspname AS name,
         pg_get_userbyid(n.nspowner) AS owner,
         n.nspacl::text AS acl
  FROM pg_namespace n
  WHERE n.nspname <> 'information_schema'
    AND n.nspname !~ '^pg_'
  ORDER BY n.nspname
`

const identitySQL = `SELECT current_database() AS name, (SELECT oid::text FROM pg_database WHERE datname=current_database()) AS oid, inet_server_addr()::text AS "serverAddress", inet_server_port() AS "serverPort"`

const rolesSQL = `SELECT rolname AS name, rolsuper AS "superuser", rolcreaterole AS "createRole", rolcreatedb AS "createDb", rolcanlogin AS "canLogin", rolreplication AS replication, rolbypassrls AS "bypassRls" FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY rolname`

const extensionsSQL = `SELECT extname AS name, extversion AS version, n.nspname AS schema FROM pg_extension e LEFT JOIN pg_namespace n ON n.oid=e.extnamespace ORDER BY extname`

const relationsSQL = `SELECT n.nspname AS schema, c.relname AS name, c.relkind AS kind, pg_get_userbyid(c.relowner) AS owner, c.relpersistence AS persistence, c.relacl::text AS acl FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','f') ORDER BY schema,name`

const columnsSQL = `SELECT n.nspname AS schema, c.relname AS relation, a.attname AS name, a.attnum AS ordinal, format_type(a.atttypid,a.atttypmod) AS type, t.typname AS udt, NOT a.attnotnull AS nullable, pg_get_expr(d.adbin,d.adrelid) AS default_definition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped JOIN pg_type t ON t.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','f') ORDER BY schema,relation,ordinal`

const constraintsSQL = `SELECT n.nspname AS schema, c.relname AS relation, con.conname AS name, con.contype AS type, pg_get_constraintdef(con.oid,true) AS definition FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,relation,name`

const indexesSQL = `SELECT n.nspname AS schema, c.relname AS relation, i.relname AS name, pg_get_indexdef(i.oid) AS definition FROM pg_index x JOIN pg_class c ON c.oid=x.indrelid JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,relation,name`

const functionsSQL = `SELECT n.nspname AS schema, p.proname AS name, pg_get_function_identity_arguments(p.oid) AS "identityArguments", pg_get_functiondef(p.oid) AS definition FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,name,"identityArguments"`

const triggersSQL = `SELECT n.nspname AS schema, c.relname AS relation, t.tgname AS name, pg_get_triggerdef(t.oid,true) AS definition FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND NOT t.tgisinternal ORDER BY schema,relation,name`

const viewsSQL = `SELECT n.nspname AS schema, c.relname AS name, c.relkind='m' AS materialized, pg_get_viewdef(c.oid,true) AS definition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('v','m') ORDER BY schema,name`

const partitionsSQL = `SELECT child_ns.nspname AS schema, child.relname AS relation, parent_ns.nspname AS "parentSchema", parent.relname AS "parentRelation" FROM pg_inherits h JOIN pg_class child ON child.oid=h.inhrelid JOIN pg_namespace child_ns ON child_ns.oid=child.relnamespace JOIN pg_class parent ON parent.oid=h.inhparent JOIN pg_namespace parent_ns ON parent_ns.oid=parent.relnamespace WHERE child_ns.nspname=ANY($1::text[]) ORDER BY schema,relation`

const sequencesSQL = `SELECT n.nspname AS schema, c.relname AS name, pg_get_userbyid(c.relowner) AS owner FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind='S' ORDER BY schema,name`

// AssertSnapshotTarget mirrors the Node assertSnapshotTarget: only
// production/test are accepted, everything else fails with the same message.
func AssertSnapshotTarget(value string) (string, error) {
	if value == TargetProduction || value == TargetTest {
		return value, nil
	}
	return "", errors.New("JUHE_AI_SCHEMA_SNAPSHOT_TARGET 必须是 production 或 test")
}

// StableJSON mirrors the Node stableJson: key-sorted recursive JSON with
// compact separators. Supported Go value domains are the ones the snapshot
// tree produces: nil, bool, string, int/int64, []any, map[string]any, plus
// the typed snapshot structs/slices (normalized through their json tags).
// Node's bigint arm has no Go counterpart because database/sql never yields
// bigint here; Node's String(bigint) conversion is expressed as int64 text.
func StableJSON(value any) (string, error) {
	var b strings.Builder
	if err := writeStableValue(&b, value); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeStableValue(b *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		writeStableString(b, v)
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeStableValue(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeStableString(b, key)
			b.WriteByte(':')
			if err := writeStableValue(b, v[key]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		normalized, err := stableNormalize(value)
		if err != nil {
			return err
		}
		return writeStableValue(b, normalized)
	}
	return nil
}

// writeStableString serializes s with JavaScript JSON.stringify semantics for
// the value ranges produced by pg catalog text columns: escape ", \ and the
// C0 control characters; keep every other rune (non-ASCII text, <, >, &,
// U+2028/U+2029 included) as-is.
func writeStableString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// stableNormalize converts the typed Go snapshot tree (structs with json
// tags, slices, pointers) into the plain map/slice/scalar primitives the
// stableJson serializer understands.
func stableNormalize(value any) (any, error) {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return stableNormalize(rv.Elem().Interface())
	case reflect.Struct:
		out := make(map[string]any, rv.NumField())
		structType := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			field := structType.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				return nil, fmt.Errorf("stableJson: struct 字段 %s 缺少 json tag", field.Name)
			}
			fieldValue, err := stableNormalize(rv.Field(i).Interface())
			if err != nil {
				return nil, err
			}
			out[name] = fieldValue
		}
		return out, nil
case reflect.Slice, reflect.Array:
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, errors.New("stableJson: 不支持 []byte 值")
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item, err := stableNormalize(rv.Index(i).Interface())
		if err != nil {
			return nil, err
		}
		out[i] = item
	}
	return out, nil
case reflect.String:
	return value, nil
case reflect.Bool:
	return value, nil
case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
	return rv.Int(), nil
case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	return int64(rv.Uint()), nil
case reflect.Float32, reflect.Float64:
	return value, nil
case reflect.Map:
	if rv.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("stableJson: map key 非 string")
	}
	out := make(map[string]any, rv.Len())
	for _, key := range rv.MapKeys() {
		normalized, err := stableNormalize(rv.MapIndex(key).Interface())
		if err != nil {
			return nil, err
		}
		out[key.String()] = normalized
	}
	return out, nil
default:
		return nil, fmt.Errorf("stableJson: 不支持的值类型 %T", value)
	}
}

// DigestDefinition mirrors the Node digestDefinition: sha256 over
// String(value ?? ''). The snapshot feeds it only strings and nulls; other
// scalar Go types fall back to fmt.Sprint, whose bool form matches
// JavaScript String() but whose float form is intentionally out of scope.
func DigestDefinition(value any) string {
	text := ""
	if value != nil {
		text = fmt.Sprint(value)
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func nullableDigest(value any) *string {
	if value == nil {
		return nil
	}
	digest := DigestDefinition(value)
	return &digest
}

// SnapshotDigest mirrors the Node snapshotDigest: strip target/capturedAt/
// database (and the not-yet-computed digest) from the snapshot, then hash the
// schema-only remainder with StableJSON + sha256.
func SnapshotDigest(snapshot SchemaSnapshot) (string, error) {
	normalized, err := stableNormalize(snapshot)
	if err != nil {
		return "", err
	}
	record, ok := normalized.(map[string]any)
	if !ok {
		return "", errors.New("snapshotDigest: 快照必须是 JSON 对象")
	}
	delete(record, "target")
	delete(record, "capturedAt")
	delete(record, "database")
	delete(record, "digest")
	serialized, err := StableJSON(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(serialized))
	return hex.EncodeToString(sum[:]), nil
}

// CollectSnapshot mirrors the Node collectSnapshot: run the pg catalog query
// family inside the caller's READ ONLY transaction and assemble the snapshot
// with its digest. Callers own BEGIN/COMMIT; see the maintenance CLI runner.
func CollectSnapshot(ctx context.Context, q Queryable, target string) (SchemaSnapshot, error) {
	schemas, schemaNames, err := collectSchemas(ctx, q)
	if err != nil {
		return SchemaSnapshot{}, err
	}
	if len(schemaNames) == 0 {
		return SchemaSnapshot{}, errors.New("目标数据库没有可审计的用户 schema")
	}

	var database DatabaseInfo
	found, err := collectOneRow(ctx, q, identitySQL, func(rows *sql.Rows) error {
		var serverAddress sql.NullString
		var serverPort sql.NullInt64
		if err := rows.Scan(&database.Name, &database.OID, &serverAddress, &serverPort); err != nil {
			return err
		}
		database.ServerAddress = nullStringPointer(serverAddress)
		if serverPort.Valid {
			port := int(serverPort.Int64)
			database.ServerPort = &port
		}
		return nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}
	if !found {
		return SchemaSnapshot{}, errors.New("无法读取 PostgreSQL 数据库身份")
	}

	roles, err := collectRows(ctx, q, rolesSQL, func(rows *sql.Rows) (RoleEntry, error) {
		var entry RoleEntry
		if err := rows.Scan(&entry.Name, &entry.Superuser, &entry.CreateRole, &entry.CreateDb, &entry.CanLogin, &entry.Replication, &entry.BypassRls); err != nil {
			return RoleEntry{}, err
		}
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	extensions, err := collectRows(ctx, q, extensionsSQL, func(rows *sql.Rows) (ExtensionEntry, error) {
		var entry ExtensionEntry
		var schema sql.NullString
		if err := rows.Scan(&entry.Name, &entry.Version, &schema); err != nil {
			return ExtensionEntry{}, err
		}
		entry.Schema = nullStringPointer(schema)
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	relations, err := collectRows(ctx, q, relationsSQL, func(rows *sql.Rows) (RelationEntry, error) {
		var entry RelationEntry
		var acl sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Name, &entry.Kind, &entry.Owner, &entry.Persistence, &acl); err != nil {
			return RelationEntry{}, err
		}
		entry.ACL = nullableDigestOf(acl)
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	columns, err := collectRows(ctx, q, columnsSQL, func(rows *sql.Rows) (ColumnEntry, error) {
		var entry ColumnEntry
		var ordinal int16
		var defaultDefinition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Relation, &entry.Name, &ordinal, &entry.Type, &entry.UDT, &entry.Nullable, &defaultDefinition); err != nil {
			return ColumnEntry{}, err
		}
		entry.Ordinal = int(ordinal)
		entry.DefaultSha = nullableDigestOf(defaultDefinition)
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	constraints, err := collectRows(ctx, q, constraintsSQL, func(rows *sql.Rows) (ConstraintEntry, error) {
		var entry ConstraintEntry
		var definition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Relation, &entry.Name, &entry.Type, &definition); err != nil {
			return ConstraintEntry{}, err
		}
		entry.DefinitionSha256 = DigestDefinition(nullStringOrEmpty(definition))
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	indexes, err := collectRows(ctx, q, indexesSQL, func(rows *sql.Rows) (IndexEntry, error) {
		var entry IndexEntry
		var definition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Relation, &entry.Name, &definition); err != nil {
			return IndexEntry{}, err
		}
		entry.DefinitionSha256 = DigestDefinition(nullStringOrEmpty(definition))
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	functions, err := collectRows(ctx, q, functionsSQL, func(rows *sql.Rows) (FunctionEntry, error) {
		var entry FunctionEntry
		var definition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Name, &entry.IdentityArguments, &definition); err != nil {
			return FunctionEntry{}, err
		}
		entry.DefinitionSha256 = DigestDefinition(nullStringOrEmpty(definition))
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	triggers, err := collectRows(ctx, q, triggersSQL, func(rows *sql.Rows) (TriggerEntry, error) {
		var entry TriggerEntry
		var definition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Relation, &entry.Name, &definition); err != nil {
			return TriggerEntry{}, err
		}
		entry.DefinitionSha256 = DigestDefinition(nullStringOrEmpty(definition))
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	views, err := collectRows(ctx, q, viewsSQL, func(rows *sql.Rows) (ViewEntry, error) {
		var entry ViewEntry
		var definition sql.NullString
		if err := rows.Scan(&entry.Schema, &entry.Name, &entry.Materialized, &definition); err != nil {
			return ViewEntry{}, err
		}
		entry.DefinitionSha256 = DigestDefinition(nullStringOrEmpty(definition))
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	partitions, err := collectRows(ctx, q, partitionsSQL, func(rows *sql.Rows) (PartitionEntry, error) {
		var entry PartitionEntry
		if err := rows.Scan(&entry.Schema, &entry.Relation, &entry.ParentSchema, &entry.ParentRelation); err != nil {
			return PartitionEntry{}, err
		}
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	sequences, err := collectRows(ctx, q, sequencesSQL, func(rows *sql.Rows) (SequenceEntry, error) {
		var entry SequenceEntry
		if err := rows.Scan(&entry.Schema, &entry.Name, &entry.Owner); err != nil {
			return SequenceEntry{}, err
		}
		return entry, nil
	})
	if err != nil {
		return SchemaSnapshot{}, err
	}

	snapshot := SchemaSnapshot{
		SchemaVersion: SchemaVersion,
		Target:        target,
		// new Date().toISOString() emits millisecond precision in UTC.
		CapturedAt:  time.Now().UTC().Format("2006-01-01T15:04:05.000Z07:00"),
		Database:    database,
		Schemas:     schemas,
		Roles:       roles,
		Extensions:  extensions,
		Relations:   relations,
		Columns:     columns,
		Constraints: constraints,
		Indexes:     indexes,
		Functions:   functions,
		Triggers:    triggers,
		Views:       views,
		Partitions:  partitions,
		Sequences:   sequences,
	}
	digest, err := SnapshotDigest(snapshot)
	if err != nil {
		return SchemaSnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func collectSchemas(ctx context.Context, q Queryable) ([]SchemaEntry, []string, error) {
	entries, err := collectRows(ctx, q, userSchemaSQL, func(rows *sql.Rows) (SchemaEntry, error) {
		var entry SchemaEntry
		var acl sql.NullString
		if err := rows.Scan(&entry.Name, &entry.Owner, &acl); err != nil {
			return SchemaEntry{}, err
		}
		entry.ACL = nullableDigestOf(acl)
		return entry, nil
	})
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return entries, names, nil
}

func collectOneRow(ctx context.Context, q Queryable, query string, scan func(rows *sql.Rows) error) (bool, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := scan(rows); err != nil {
		return false, err
	}
	return true, rows.Err()
}

func collectRows[T any](ctx context.Context, q Queryable, query string, scan func(rows *sql.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Start non-nil so empty results marshal as [] like the Node arrays.
	out := make([]T, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func nullableDigestOf(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	digest := DigestDefinition(value.String)
	return &digest
}

func nullStringOrEmpty(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
