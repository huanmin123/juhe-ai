import assert from 'node:assert/strict'

import { buildPostgresSchemaSql, collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

const statements = collectPostgresSchemaStatements()
const sql = buildPostgresSchemaSql()
const schemaNames = new Set(statements.map((statement) => statement.schemaName))

assert.ok(statements.length > 100, 'PostgreSQL schema 应从现有 SQLite DDL 收集到完整建表和索引语句')
assert.deepEqual(
  [...schemaNames].sort(),
  ['juhe_business', 'juhe_codex_context', 'juhe_dataset', 'juhe_stats', 'juhe_usage'].sort(),
  'PostgreSQL schema 应覆盖现有业务、数据集、使用记录、统计和 Codex context 存储边界'
)
assert.equal(
  statements.some((statement) => statement.schemaName === 'juhe_usage' && statement.source === 'usage-catalog'),
  true,
  'juhe_usage schema 应包含使用记录目录表'
)
assert.equal(
  statements.some((statement) => statement.schemaName === 'juhe_usage' && statement.source === 'usage-records'),
  true,
  'juhe_usage schema 应包含高性能模式使用记录主表'
)
assert.equal(
  statements.some((statement) => statement.schemaName === 'juhe_usage' && statement.source === 'usage-records-supplemental'),
  true,
  'juhe_usage schema 应包含使用记录主表补列语句'
)
assertPostgresCreateTableOrder(statements)

for (const schemaName of schemaNames) {
  assert.match(sql, new RegExp(`CREATE SCHEMA IF NOT EXISTS "${schemaName}"`), `${schemaName} 应生成 CREATE SCHEMA`)
}

assert.match(sql, /CREATE TABLE IF NOT EXISTS system_accounts/, '应包含业务库 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS audit_logs/, '应包含数据集库 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_records/, '应包含使用记录主表 schema')
assert.match(sql, /ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS failure_attribution text/, '应包含使用记录失败归因补列语句')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_stats_totals/, '应包含统计库 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS codex_context_sessions/, '应包含 Codex context schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategies/, '应包含策略路由表 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategy_groups/, '应包含策略路由分组绑定表 schema')
assert.match(sql, /route_strategy_id text NOT NULL/, 'api_keys 建表语句应强制绑定 route_strategy_id')
assert.doesNotMatch(sql, /client_profile text NOT NULL DEFAULT 'auto'/, 'api_keys 建表语句不应再包含 client_profile 字段')
assert.doesNotMatch(sql, /explicit_hybrid_route_rules_json text/, 'api_keys 建表语句不应再包含 explicit_hybrid_route_rules_json 字段')
assert.match(sql, /container_id text/, 'openai_compatible_files 建表语句应包含 container_id 字段')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS route_strategy_id text/, 'PostgreSQL schema 不应再为 api_keys.route_strategy_id 生成历史补列语句')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS client_profile text NOT NULL DEFAULT 'auto'/, 'PostgreSQL schema 不应再补 client_profile')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS explicit_hybrid_route_rules_json text/, 'PostgreSQL schema 不应再补 explicit_hybrid_route_rules_json')
assert.doesNotMatch(sql, /ALTER TABLE openai_compatible_files ADD COLUMN container_id\b/, 'PostgreSQL schema 不应重复为 openai_compatible_files.container_id 补列')

assert.doesNotMatch(sql, /\bPRAGMA\b/i, 'PostgreSQL SQL 不应残留 SQLite PRAGMA')
assert.doesNotMatch(sql, /COLLATE\s+NOCASE/i, 'PostgreSQL SQL 不应残留 SQLite NOCASE collation')
assert.doesNotMatch(sql, /\bjson_valid\b|\bjson_type\b/i, 'PostgreSQL SQL 不应残留 SQLite JSON 函数')
assert.match(sql, /jsonb_typeof\(match_json::jsonb\) = 'object'/, 'SQLite JSON object 校验应映射到 PostgreSQL jsonb_typeof')
assert.match(sql, /lower\(name\)/, 'SQLite NOCASE 索引应映射为 lower 表达式索引')
assert.match(sql, /\bdouble precision\b/, 'SQLite REAL 类型应映射为 PostgreSQL double precision')

for (const statement of statements) {
  assert.equal(statement.sql.endsWith(';'), false, `单条语句不应带尾部分号：${statement.source}`)
}

console.log('postgres-schema-sql-regression passed')

function assertPostgresCreateTableOrder(input: typeof statements): void {
  const tableNamesBySchema = new Map<string, Set<string>>()
  for (const statement of input) {
    const tableName = extractCreatedTableName(statement.sql)
    if (!tableName) continue
    const tableNames = tableNamesBySchema.get(statement.schemaName) ?? new Set<string>()
    tableNames.add(tableName)
    tableNamesBySchema.set(statement.schemaName, tableNames)
  }

  const seenBySchema = new Map<string, Set<string>>()
  for (const statement of input) {
    const tableName = extractCreatedTableName(statement.sql)
    if (!tableName) continue
    const schemaTables = tableNamesBySchema.get(statement.schemaName) ?? new Set<string>()
    const seen = seenBySchema.get(statement.schemaName) ?? new Set<string>()
    for (const referencedTableName of extractReferencedTableNames(statement.sql)) {
      if (referencedTableName === tableName || !schemaTables.has(referencedTableName)) continue
      assert.equal(
        seen.has(referencedTableName),
        true,
        `${statement.schemaName}.${tableName} 的外键引用 ${referencedTableName}，但被引用表尚未创建`
      )
    }
    seen.add(tableName)
    seenBySchema.set(statement.schemaName, seen)
  }
}

function extractCreatedTableName(sql: string): string | undefined {
  const match = /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/i.exec(sql.trim())
  return match?.[1]?.toLowerCase()
}

function extractReferencedTableNames(sql: string): string[] {
  const tableNames: string[] = []
  for (const match of sql.matchAll(/\bREFERENCES\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/gi)) {
    tableNames.push(match[1].toLowerCase())
  }
  return tableNames
}
