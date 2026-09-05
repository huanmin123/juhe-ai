import { createHash } from 'node:crypto'
import { pathToFileURL } from 'node:url'

import pg from 'pg'

const USER_SCHEMA_SQL = `
  SELECT n.nspname AS name,
         pg_get_userbyid(n.nspowner) AS owner,
         n.nspacl::text AS acl
  FROM pg_namespace n
  WHERE n.nspname <> 'information_schema'
    AND n.nspname !~ '^pg_'
  ORDER BY n.nspname
`

export interface SchemaSnapshot {
  schemaVersion: 1
  target: 'production' | 'test'
  capturedAt: string
  database: {
    name: string
    oid: string
    serverAddress: string | null
    serverPort: number | null
  }
  schemas: Array<{ name: string; owner: string; aclSha256: string | null }>
  roles: Array<{
    name: string
    superuser: boolean
    createRole: boolean
    createDb: boolean
    canLogin: boolean
    replication: boolean
    bypassRls: boolean
  }>
  extensions: Array<{ name: string; version: string; schema: string | null }>
  relations: Array<{
    schema: string
    name: string
    kind: string
    owner: string
    persistence: string
    aclSha256: string | null
  }>
  columns: Array<{
    schema: string
    relation: string
    name: string
    ordinal: number
    type: string
    udt: string
    nullable: boolean
    defaultSha256: string | null
  }>
  constraints: Array<{ schema: string; relation: string; name: string; type: string; definitionSha256: string }>
  indexes: Array<{ schema: string; relation: string; name: string; definitionSha256: string }>
  functions: Array<{ schema: string; name: string; identityArguments: string; definitionSha256: string }>
  triggers: Array<{ schema: string; relation: string; name: string; definitionSha256: string }>
  views: Array<{ schema: string; name: string; materialized: boolean; definitionSha256: string }>
  partitions: Array<{ schema: string; relation: string; parentSchema: string; parentRelation: string }>
  sequences: Array<{ schema: string; name: string; owner: string }>
  digest: string
}

type Queryable = Pick<pg.Client, 'query'>
type QueryRow = Record<string, unknown>

export function assertSnapshotTarget(value: string | undefined): 'production' | 'test' {
  if (value === 'production' || value === 'test') return value
  throw new Error('JUHE_AI_SCHEMA_SNAPSHOT_TARGET 必须是 production 或 test')
}

export function stableJson(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (typeof value === 'bigint') return JSON.stringify(value.toString())
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableJson(record[key])}`).join(',')}}`
  }
  return JSON.stringify(value)
}

export function digestDefinition(value: unknown): string {
  return createHash('sha256').update(String(value ?? '')).digest('hex')
}

export function snapshotDigest(snapshot: Omit<SchemaSnapshot, 'digest'>): string {
  const { target: _target, capturedAt: _capturedAt, database: _database, ...schemaOnly } = snapshot
  return createHash('sha256').update(stableJson(schemaOnly)).digest('hex')
}

async function main(): Promise<void> {
  const target = assertSnapshotTarget(process.env.JUHE_AI_SCHEMA_SNAPSHOT_TARGET)
  const connectionString = requiredEnv('JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL')
  if (process.env.JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM !== 'READ_ONLY') {
    throw new Error('必须设置 JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY；该工具只允许只读快照')
  }

  const client = new pg.Client({
    connectionString,
    application_name: `juhe-ai-schema-snapshot-${target}`,
    connectionTimeoutMillis: 10_000
  })
  await client.connect()
  try {
    await client.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
    await client.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)")
    const snapshot = await collectSnapshot(client, target)
    await client.query('COMMIT')
    process.stdout.write(`${JSON.stringify(snapshot, null, 2)}\n`)
  } catch (error) {
    await client.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    await client.end()
  }
}

export async function collectSnapshot(client: Queryable, target: 'production' | 'test'): Promise<SchemaSnapshot> {
  const schemaRows = await client.query<QueryRow>(USER_SCHEMA_SQL)
  const schemaNames = schemaRows.rows.map((row) => String(row.name))
  if (schemaNames.length === 0) throw new Error('目标数据库没有可审计的用户 schema')

  const identity = await client.query<QueryRow>(`SELECT current_database() AS name, (SELECT oid::text FROM pg_database WHERE datname=current_database()) AS oid, inet_server_addr()::text AS "serverAddress", inet_server_port() AS "serverPort"`)
  const roles = await client.query<QueryRow>(`SELECT rolname AS name, rolsuper AS "superuser", rolcreaterole AS "createRole", rolcreatedb AS "createDb", rolcanlogin AS "canLogin", rolreplication AS replication, rolbypassrls AS "bypassRls" FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY rolname`)
  const extensions = await client.query<QueryRow>(`SELECT extname AS name, extversion AS version, n.nspname AS schema FROM pg_extension e LEFT JOIN pg_namespace n ON n.oid=e.extnamespace ORDER BY extname`)
  const relations = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS name, c.relkind AS kind, pg_get_userbyid(c.relowner) AS owner, c.relpersistence AS persistence, c.relacl::text AS acl FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','f') ORDER BY schema,name`, [schemaNames])
  const columns = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS relation, a.attname AS name, a.attnum AS ordinal, format_type(a.atttypid,a.atttypmod) AS type, t.typname AS udt, NOT a.attnotnull AS nullable, pg_get_expr(d.adbin,d.adrelid) AS default_definition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped JOIN pg_type t ON t.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','f') ORDER BY schema,relation,ordinal`, [schemaNames])
  const constraints = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS relation, con.conname AS name, con.contype AS type, pg_get_constraintdef(con.oid,true) AS definition FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,relation,name`, [schemaNames])
  const indexes = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS relation, i.relname AS name, pg_get_indexdef(i.oid) AS definition FROM pg_index x JOIN pg_class c ON c.oid=x.indrelid JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,relation,name`, [schemaNames])
  const functions = await client.query<QueryRow>(`SELECT n.nspname AS schema, p.proname AS name, pg_get_function_identity_arguments(p.oid) AS "identityArguments", pg_get_functiondef(p.oid) AS definition FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=ANY($1::text[]) ORDER BY schema,name,"identityArguments"`, [schemaNames])
  const triggers = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS relation, t.tgname AS name, pg_get_triggerdef(t.oid,true) AS definition FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND NOT t.tgisinternal ORDER BY schema,relation,name`, [schemaNames])
  const views = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS name, c.relkind='m' AS materialized, pg_get_viewdef(c.oid,true) AS definition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('v','m') ORDER BY schema,name`, [schemaNames])
  const partitions = await client.query<QueryRow>(`SELECT child_ns.nspname AS schema, child.relname AS relation, parent_ns.nspname AS "parentSchema", parent.relname AS "parentRelation" FROM pg_inherits h JOIN pg_class child ON child.oid=h.inhrelid JOIN pg_namespace child_ns ON child_ns.oid=child.relnamespace JOIN pg_class parent ON parent.oid=h.inhparent JOIN pg_namespace parent_ns ON parent_ns.oid=parent.relnamespace WHERE child_ns.nspname=ANY($1::text[]) ORDER BY schema,relation`, [schemaNames])
  const sequences = await client.query<QueryRow>(`SELECT n.nspname AS schema, c.relname AS name, pg_get_userbyid(c.relowner) AS owner FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1::text[]) AND c.relkind='S' ORDER BY schema,name`, [schemaNames])

  const withoutDigest = {
    schemaVersion: 1 as const,
    target,
    capturedAt: new Date().toISOString(),
    database: mapDatabase(identity.rows[0]),
    schemas: schemaRows.rows.map((row) => ({ name: String(row.name), owner: String(row.owner), aclSha256: nullableDigest(row.acl) })),
    roles: roles.rows.map((row) => ({ name: String(row.name), superuser: Boolean(row.superuser), createRole: Boolean(row.createRole), createDb: Boolean(row.createDb), canLogin: Boolean(row.canLogin), replication: Boolean(row.replication), bypassRls: Boolean(row.bypassRls) })),
    extensions: extensions.rows.map((row) => ({ name: String(row.name), version: String(row.version), schema: row.schema == null ? null : String(row.schema) })),
    relations: relations.rows.map((row) => ({ schema: String(row.schema), name: String(row.name), kind: String(row.kind), owner: String(row.owner), persistence: String(row.persistence), aclSha256: nullableDigest(row.acl) })),
    columns: columns.rows.map((row) => ({ schema: String(row.schema), relation: String(row.relation), name: String(row.name), ordinal: Number(row.ordinal), type: String(row.type), udt: String(row.udt), nullable: Boolean(row.nullable), defaultSha256: nullableDigest(row.default_definition) })),
    constraints: constraints.rows.map((row) => ({ schema: String(row.schema), relation: String(row.relation), name: String(row.name), type: String(row.type), definitionSha256: digestDefinition(row.definition) })),
    indexes: indexes.rows.map((row) => ({ schema: String(row.schema), relation: String(row.relation), name: String(row.name), definitionSha256: digestDefinition(row.definition) })),
    functions: functions.rows.map((row) => ({ schema: String(row.schema), name: String(row.name), identityArguments: String(row.identityArguments), definitionSha256: digestDefinition(row.definition) })),
    triggers: triggers.rows.map((row) => ({ schema: String(row.schema), relation: String(row.relation), name: String(row.name), definitionSha256: digestDefinition(row.definition) })),
    views: views.rows.map((row) => ({ schema: String(row.schema), name: String(row.name), materialized: Boolean(row.materialized), definitionSha256: digestDefinition(row.definition) })),
    partitions: partitions.rows.map((row) => ({ schema: String(row.schema), relation: String(row.relation), parentSchema: String(row.parentSchema), parentRelation: String(row.parentRelation) })),
    sequences: sequences.rows.map((row) => ({ schema: String(row.schema), name: String(row.name), owner: String(row.owner) }))
  }
  return { ...withoutDigest, digest: snapshotDigest(withoutDigest) }
}

function mapDatabase(row: QueryRow | undefined): SchemaSnapshot['database'] {
  if (!row) throw new Error('无法读取 PostgreSQL 数据库身份')
  return { name: String(row.name), oid: String(row.oid), serverAddress: row.serverAddress == null ? null : String(row.serverAddress), serverPort: row.serverPort == null ? null : Number(row.serverPort) }
}

function nullableDigest(value: unknown): string | null {
  return value == null ? null : digestDefinition(value)
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 未配置`)
  return value
}

const entryPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (entryPath === import.meta.url) {
  void main().catch((error: unknown) => {
    console.error(`PostgreSQL schema snapshot failed: ${error instanceof Error ? error.message : String(error)}`)
    process.exitCode = 1
  })
}
