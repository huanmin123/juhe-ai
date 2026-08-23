import { strict as assert } from 'node:assert'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

import { parsePostgresSchemaOwner } from '../../config/runtime.js'
import {
  checkPostgresNodeSchemaPreflight,
  enforcePostgresSchemaOwnerGate,
  NODE_POSTGRES_SCHEMA_CONTRACT_VERSION,
  POSTGRES_NODE_SCHEMA_PREFLIGHT_QUERY,
  validatePostgresNodeSchemaPreflight,
  type PostgresSchemaOwnerGatePool
} from '../../storage/postgres-schema-owner-gate.js'

const baseConfig = {
  ownerLock: { enabled: true },
  schemaOwner: 'node' as const,
  databaseDriver: 'postgres' as const,
  postgres: {
    url: 'postgres://schema-gate.example/juhe_ai',
    connectionTimeoutMs: 4321,
    statementTimeoutMs: 8765,
    lockTimeoutMs: 987
  }
}

assert.equal(NODE_POSTGRES_SCHEMA_CONTRACT_VERSION, 94)
assert.equal(parsePostgresSchemaOwner('node'), 'node')
assert.equal(parsePostgresSchemaOwner('goose'), 'goose')

assert.throws(
  () => validatePostgresNodeSchemaPreflight({
    missingRelations: ['juhe_business.accounts'],
    missingColumns: [],
    missingIndexes: [],
    gooseLedgerPresent: false
  }),
  /schema contract 94 不完整/
)
assert.throws(
  () => validatePostgresNodeSchemaPreflight({
    missingRelations: [],
    missingColumns: [],
    missingIndexes: [],
    gooseLedgerPresent: true
  }),
  /不得同时存在 Goose ledger/
)

await assertNodeOwnerDoesNotCreateLedger()
await assertMissingOwnerFailsClosed()
await assertGooseOwnerDelegatesToStrictGate()
await assertServerUsesOwnerGateBeforeListeners()
await assertCliScriptIsReadOnly()

console.log('PostgreSQL schema owner gate 回归通过')

async function assertNodeOwnerDoesNotCreateLedger(): Promise<void> {
  let ended = false
  let queryCount = 0
  const pool: PostgresSchemaOwnerGatePool = {
    async query(text) {
      queryCount += 1
      assert.equal(text, POSTGRES_NODE_SCHEMA_PREFLIGHT_QUERY)
      return {
        rows: [{
          missing_relations: [],
          missing_columns: [],
          missing_indexes: [],
          goose_ledger_present: false
        }]
      } as never
    },
    async end() {
      ended = true
    }
  }
  await enforcePostgresSchemaOwnerGate(baseConfig, () => pool)
  assert.equal(queryCount, 1)
  assert.equal(ended, true)
}

async function assertMissingOwnerFailsClosed(): Promise<void> {
  await assert.rejects(
    enforcePostgresSchemaOwnerGate({ ...baseConfig, schemaOwner: undefined }, () => {
      throw new Error('owner missing must not create a pool')
    }),
    /schema owner 未显式配置/
  )
}

async function assertGooseOwnerDelegatesToStrictGate(): Promise<void> {
  const results = [
    { rows: [{ version_id: '94', is_applied: true }] },
    { rows: [] }
  ]
  const pool: PostgresSchemaOwnerGatePool = {
    async query(_text, _values) {
      return (results.shift() ?? { rows: [] }) as never
    },
    async end() {}
  }
  await enforcePostgresSchemaOwnerGate({ ...baseConfig, schemaOwner: 'goose' }, () => pool)
}

async function assertServerUsesOwnerGateBeforeListeners(): Promise<void> {
  const source = await readFile(resolve(process.cwd(), 'src/server.ts'), 'utf8')
  const gateIndex = source.indexOf('await enforcePostgresSchemaOwnerGate()')
  const listenIndex = source.indexOf('const server = app.listen(')
  assert(gateIndex >= 0 && listenIndex > gateIndex)
  assert.equal(source.includes('await enforcePostgresGooseSchemaGate()'), false)
}

async function assertCliScriptIsReadOnly(): Promise<void> {
  const source = await readFile(resolve(process.cwd(), 'src/scripts/maintenance/postgres-schema-preflight.ts'), 'utf8')
  const normalized = source.toLowerCase()
  assert.equal(normalized.includes('insert into goose_db_version'), false)
  assert.equal(normalized.includes('update goose_db_version'), false)
  assert.equal(normalized.includes('delete from goose_db_version'), false)
  assert.match(normalized, /checkpostgresnodeschemapreflight/)
}
