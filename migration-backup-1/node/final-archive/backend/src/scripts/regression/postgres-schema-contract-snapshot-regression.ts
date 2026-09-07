import assert from 'node:assert/strict'

import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import { NODE_POSTGRES_SCHEMA_CONTRACT_VERSION } from '../../storage/postgres-schema-owner-gate.js'
import { CURRENT_RELEASE_SCHEMA_VERSION } from '../../shared/release-schema-version.js'

const statements = collectPostgresSchemaStatements()
assert.ok(statements.length > 0, 'PostgreSQL schema contract must contain statements')
assert.equal(NODE_POSTGRES_SCHEMA_CONTRACT_VERSION, 96, 'Node schema contract version must remain explicit')
assert.equal(CURRENT_RELEASE_SCHEMA_VERSION, 94, 'release schema version must remain explicit until Goose owner handoff')

const schemaNames = [...new Set(statements.map((statement) => statement.schemaName))]
assert.deepEqual(schemaNames, [
  'juhe_business',
  'juhe_chat',
  'juhe_dataset',
  'juhe_usage',
  'juhe_stats',
  'juhe_codex_context'
])

for (const statement of statements) {
  assert.ok(statement.source.length > 0, 'every schema statement must identify its source')
  assert.ok(statement.sql.trim().length > 0, 'every schema statement must contain SQL')
}

const trigramStatements = statements.filter((statement) => statement.sql.includes('gin_trgm_ops'))
assert.ok(trigramStatements.length > 0, 'PostgreSQL schema contract must retain trigram lookup indexes')
for (const statement of trigramStatements) {
  assert.match(
    statement.sql,
    /juhe_business\.gin_trgm_ops/,
    'pg_trgm is installed in juhe_business in the deployment topology, so the operator class must not depend on connection search_path'
  )
}

const serialized = statements.map((statement) => `${statement.schemaName}\u0000${statement.source}\u0000${statement.sql}`).join('\u0001')
const repeated = collectPostgresSchemaStatements().map((statement) => `${statement.schemaName}\u0000${statement.source}\u0000${statement.sql}`).join('\u0001')
assert.equal(serialized, repeated, 'schema contract statement ordering must be deterministic')

// Trigger/function bodies legitimately contain INSERT/UPDATE statements. The
// contract snapshot is a source DDL summary, so destructive-DML screening is
// performed by the execution runner, not by searching function text here.

console.log(`postgres schema contract snapshot regression passed: ${statements.length} statements, Node contract ${NODE_POSTGRES_SCHEMA_CONTRACT_VERSION}, release schema ${CURRENT_RELEASE_SCHEMA_VERSION}`)
