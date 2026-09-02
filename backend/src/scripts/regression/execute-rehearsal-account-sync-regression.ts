import assert from 'node:assert/strict'

import { ACCOUNT_SYNC_TABLE_POLICIES } from '../operations/rehearsal-account-sync-preflight.js'
import {
  assertExecuteEnvironment,
  hashStringList,
  orderAccountRows,
  stableKey,
  validateGeneratedValuesManifest,
  validateScopeManifest
} from '../operations/execute-rehearsal-account-sync.js'

assert.equal(stableKey(['account-1']), '["account-1"]')
assert.equal(hashStringList(['b', 'a']), hashStringList(['a', 'b']))
assert.throws(() => assertExecuteEnvironment({
  JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE: 'plan',
  JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL: 'postgres://source',
  JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL: 'postgres://target'
}), /MODE=execute/)
assert.doesNotThrow(() => assertExecuteEnvironment({
  JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE: 'execute',
  JUHE_AI_REHEARSAL_EXECUTE_CONFIRM: 'I_UNDERSTAND_TEST_TARGET_ONLY',
  JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL: 'postgres://source',
  JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL: 'postgres://target'
}))

const plan = {
  schemaVersion: 1 as const,
  mode: 'field-level-plan' as const,
  preflightSha256: '0'.repeat(64),
  credentialsPolicy: 'test-only-equivalent' as const,
  credentialsEvidenceRef: 'private/credentials.json',
  approvedCanaryAccountIdsHash: hashStringList(['account-1']),
  approvedCanaryCount: 1,
  tables: [],
  runtimeResetTables: [],
  auxiliaryRuntimeResetTables: []
}
const scope = {
  schemaVersion: 1 as const,
  approvedCanaryAccountIds: ['account-1'],
  sourceAccountIds: [],
  selectedAccountIds: ['account-1'],
  approvedCanaryAccountIdsHash: plan.approvedCanaryAccountIdsHash,
  approvedCanaryCount: 1,
  sourceAccountIdsHash: hashStringList([]),
  selectedAccountIdsHash: hashStringList(['account-1']),
  tables: Object.fromEntries(ACCOUNT_SYNC_TABLE_POLICIES
    .filter((item) => item.purpose === 'configuration')
    .map((item) => [item.name, item.name === 'accounts' ? ['["account-1"]'] : ['["row-1"]']]))
}
assert.doesNotThrow(() => validateScopeManifest(scope, plan))
assert.throws(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, accounts: ['*'] } }, plan), /scope accounts 不允许使用 \*/)
assert.throws(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, account_lock_states: ['*'] } }, plan), /未批准的账户同步表/)
assert.doesNotThrow(() => validateGeneratedValuesManifest({ schemaVersion: 1, tables: { accounts: { '["account-1"]': { credentials_encrypted: 'test-only' } } } }))
assert.throws(() => validateGeneratedValuesManifest({ schemaVersion: 2, tables: {} } as never), /schemaVersion=1/)
const accountRows = [
  { id: 'child', authorization_instance_source_account_id: 'source' },
  { id: 'source', authorization_instance_source_account_id: null }
]
assert.deepEqual(orderAccountRows(accountRows).map((row) => row.id), ['source', 'child'])
assert.throws(() => orderAccountRows([{ id: 'child', authorization_instance_source_account_id: 'missing' }]), /缺失 source 或环/)
assert.throws(() => orderAccountRows([
  { id: 'a', authorization_instance_source_account_id: 'b' },
  { id: 'b', authorization_instance_source_account_id: 'a' }
]), /缺失 source 或环/)

console.log('execute rehearsal account sync regression passed')
