import assert from 'node:assert/strict'

import {
  ACCOUNT_SYNC_RUNTIME_RESET_TABLES,
  AUXILIARY_RUNTIME_RESET_TABLES,
  ACCOUNT_SYNC_TABLE_POLICIES,
  assertDistinctDatabaseIdentities,
  assertTargetDatabaseName,
  compareForeignKeySets,
  findForeignKeysOutsidePolicy,
  findForeignKeysOutsidePolicyInEitherDatabase,
  foreignKeySignature,
  validatePolicyManifest
} from '../operations/rehearsal-account-sync-preflight.js'

validatePolicyManifest()
assert.ok(ACCOUNT_SYNC_TABLE_POLICIES.some((item) => item.name === 'accounts'))
assert.ok(ACCOUNT_SYNC_TABLE_POLICIES.some((item) => item.name === 'account_schedule_status_events' && item.purpose === 'runtime-reset'))
assert.ok(ACCOUNT_SYNC_TABLE_POLICIES.some((item) => item.name === 'api_key_schedule_status_events' && item.purpose === 'runtime-reset'))
assert.ok(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.includes('account_schedule_status_events'))
assert.ok(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.includes('api_key_schedule_status_events'))
assert.ok(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.includes('account_quality_enforcements'))
assert.ok(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.includes('account_name_search_terms'))
assert.ok(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.includes('account_name_search_documents'))
assert.equal(ACCOUNT_SYNC_RUNTIME_RESET_TABLES.length, 28)
assert.equal(new Set(ACCOUNT_SYNC_RUNTIME_RESET_TABLES).size, ACCOUNT_SYNC_RUNTIME_RESET_TABLES.length)
assert.deepEqual(AUXILIARY_RUNTIME_RESET_TABLES.map((item) => `${item.schema}.${item.name}`), [
  'juhe_jobs.account_health_outcomes',
  'juhe_jobs.account_balance_outcomes',
  'juhe_stats.background_job_leases'
])
assert.throws(() => assertTargetDatabaseName('juhe_ai_test'), /隔离数据库/u)
assert.doesNotThrow(() => assertTargetDatabaseName('juhe_ai_test_rehearsal_20260902'))
assert.throws(() => assertDistinctDatabaseIdentities(
  { databaseName: 'same', databaseOid: '1' },
  { databaseName: 'same', databaseOid: '1' }
), /同一 PostgreSQL 数据库/u)

const sourceForeignKey = {
  constraintName: 'accounts_provider_code_fkey',
  parentSchema: 'juhe_business',
  parentTable: 'providers',
  definition: 'FOREIGN KEY (provider_code) REFERENCES juhe_business.providers(code)'
}
const targetForeignKey = { ...sourceForeignKey }
assert.equal(foreignKeySignature(sourceForeignKey), 'accounts_provider_code_fkey|juhe_business|providers|FOREIGN KEY (provider_code) REFERENCES juhe_business.providers(code)')
assert.deepEqual(compareForeignKeySets([sourceForeignKey], [targetForeignKey]), [])
assert.deepEqual(compareForeignKeySets([sourceForeignKey], []), [
  'missing-target:accounts_provider_code_fkey|juhe_business|providers|FOREIGN KEY (provider_code) REFERENCES juhe_business.providers(code)'
])
assert.deepEqual(findForeignKeysOutsidePolicy([sourceForeignKey], new Set(['accounts', 'providers'])), [])
assert.deepEqual(findForeignKeysOutsidePolicy([
  { ...sourceForeignKey, parentTable: 'external_sources' }
], new Set(['accounts', 'providers'])), ['accounts_provider_code_fkey|juhe_business.external_sources'])
assert.deepEqual(findForeignKeysOutsidePolicy([
  { ...sourceForeignKey, parentSchema: 'external_schema' }
], new Set(['accounts', 'providers'])), ['accounts_provider_code_fkey|external_schema.providers'])
assert.deepEqual(findForeignKeysOutsidePolicyInEitherDatabase(
  [sourceForeignKey],
  [{ ...sourceForeignKey, parentTable: 'external_sources' }],
  new Set(['accounts', 'providers'])
), ['target:accounts_provider_code_fkey|juhe_business.external_sources'])
assert.deepEqual(findForeignKeysOutsidePolicyInEitherDatabase(
  [{ ...sourceForeignKey, parentSchema: 'external_schema' }],
  [{ ...sourceForeignKey, parentTable: 'external_sources' }],
  new Set(['accounts', 'providers'])
), [
  'source:accounts_provider_code_fkey|external_schema.providers',
  'target:accounts_provider_code_fkey|juhe_business.external_sources'
])

console.log(`rehearsal account sync preflight regression passed: ${ACCOUNT_SYNC_TABLE_POLICIES.length} policy tables, ${ACCOUNT_SYNC_RUNTIME_RESET_TABLES.length} runtime reset tables`)
