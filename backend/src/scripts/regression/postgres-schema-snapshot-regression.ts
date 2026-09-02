import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

import {
  assertSnapshotTarget,
  digestDefinition,
  snapshotDigest,
  stableJson
} from '../operations/postgres-schema-snapshot.js'

const sourcePath = fileURLToPath(new URL('../operations/postgres-schema-snapshot.ts', import.meta.url))
const source = await readFile(sourcePath, 'utf8')

assert.equal(assertSnapshotTarget('production'), 'production')
assert.equal(assertSnapshotTarget('test'), 'test')
assert.throws(() => assertSnapshotTarget(undefined), /production 或 test/)
assert.throws(() => assertSnapshotTarget('staging'), /production 或 test/)

const snapshot = {
  schemaVersion: 1 as const,
  target: 'test' as const,
  capturedAt: '2026-01-01T00:00:00.000Z',
  database: { name: 'db', oid: '1', serverAddress: null, serverPort: 5432 },
  schemas: [], roles: [], extensions: [], relations: [], columns: [], constraints: [], indexes: [],
  functions: [{ schema: 'juhe_business', name: 'f', identityArguments: '', definitionSha256: digestDefinition('CREATE FUNCTION f()') }],
  triggers: [], views: [], partitions: [], sequences: []
}
assert.equal(snapshotDigest(snapshot), snapshotDigest({ ...snapshot, functions: [...snapshot.functions] }))
assert.equal(snapshotDigest({ ...snapshot, capturedAt: '2027-01-01T00:00:00.000Z' }), snapshotDigest(snapshot))
assert.equal(snapshotDigest({ ...snapshot, target: 'production' }), snapshotDigest(snapshot))
assert.equal(stableJson({ b: 2, a: 1 }), '{"a":1,"b":2}')

assert.match(source, /BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY/)
assert.match(source, /JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM !== 'READ_ONLY'/)
assert.doesNotMatch(source, /\b(?:INSERT\s+INTO|UPDATE\s+\w|DELETE\s+FROM|TRUNCATE\s+\w|DROP\s+TABLE|ALTER\s+TABLE|CREATE\s+TABLE)\b/i)
assert.match(source, /pg_get_functiondef/)
assert.match(source, /pg_get_triggerdef/)
assert.match(source, /pg_get_viewdef/)
assert.match(source, /pg_inherits/)

console.log('postgres schema snapshot regression passed')
