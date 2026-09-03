import assert from 'node:assert/strict'

import { ACCOUNT_SYNC_TABLE_POLICIES } from '../operations/rehearsal-account-sync-preflight.js'
import {
  assertTransformedReadback,
  assertGeneratedApiKeyValues,
  assertTargetReferenceClosure,
  assertClosureMatchesScope,
  assertExecuteEnvironment,
  hashStringList,
  manifestDigest,
  orderAccountRows,
  orderProviderRows,
  stableKey,
  validateGeneratedValuesManifest,
  validateScopeManifest
} from '../operations/execute-rehearsal-account-sync.js'
import { encryptJson, hashSecret } from '../../storage/crypto.js'

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
  JUHE_AI_SECRET: 'test-rehearsal-secret-for-regression-20260903',
  JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL: 'postgres://source',
  JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL: 'postgres://target'
}))
assert.throws(() => assertExecuteEnvironment({
  JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE: 'execute',
  JUHE_AI_REHEARSAL_EXECUTE_CONFIRM: 'I_UNDERSTAND_TEST_TARGET_ONLY',
  JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL: 'postgres://source',
  JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL: 'postgres://target'
}), /JUHE_AI_SECRET/)

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
const approvedEntityIds = {
  systemAccountIds: ['system-account-1'],
  systemTeamIds: ['team-1'],
  groupIds: ['group-1'],
  routeStrategyIds: ['route-1'],
  resourceAuthorizationIds: ['authorization-1'],
  resourceAuthorizationGrantIds: ['grant-1'],
  apiKeyIds: ['api-key-1'],
  apiKeyRemap: [{ sourceId: 'api-key-1', targetId: 'api-key-1' }]
}
const scope = {
  schemaVersion: 1 as const,
  approvedCanaryAccountIds: ['account-1'],
  sourceAccountIds: [],
  selectedAccountIds: ['account-1'],
  ...approvedEntityIds,
  approvedCanaryAccountIdsHash: plan.approvedCanaryAccountIdsHash,
  approvedCanaryCount: 1,
  sourceAccountIdsHash: hashStringList([]),
  selectedAccountIdsHash: hashStringList(['account-1']),
  systemAccountIdsHash: hashStringList(approvedEntityIds.systemAccountIds),
  systemTeamIdsHash: hashStringList(approvedEntityIds.systemTeamIds),
  groupIdsHash: hashStringList(approvedEntityIds.groupIds),
  routeStrategyIdsHash: hashStringList(approvedEntityIds.routeStrategyIds),
  resourceAuthorizationIdsHash: hashStringList(approvedEntityIds.resourceAuthorizationIds),
  resourceAuthorizationGrantIdsHash: hashStringList(approvedEntityIds.resourceAuthorizationGrantIds),
  apiKeyIdsHash: hashStringList(approvedEntityIds.apiKeyIds),
  apiKeyRemapHash: '',
  tables: Object.fromEntries(ACCOUNT_SYNC_TABLE_POLICIES
    .filter((item) => item.purpose === 'configuration')
    .map((item) => {
      const entityId = item.name === 'accounts'
        ? 'account-1'
        : item.name === 'system_accounts'
          ? 'system-account-1'
          : item.name === 'system_teams'
            ? 'team-1'
            : item.name === 'groups'
              ? 'group-1'
              : item.name === 'route_strategies'
                ? 'route-1'
                : item.name === 'resource_authorizations'
                  ? 'authorization-1'
                  : item.name === 'resource_authorization_grants'
                    ? 'grant-1'
                    : item.name === 'api_keys'
                      ? 'api-key-1'
                      : 'row-1'
      return [item.name, [`["${entityId}"]`]]
    }))
}
scope.apiKeyRemapHash = manifestDigest(scope.apiKeyRemap)
assert.doesNotThrow(() => validateScopeManifest(scope, plan))
const closure = {
  schemaVersion: 1 as const,
  approvedCanaryAccountIds: scope.approvedCanaryAccountIds,
  sourceAccountIds: scope.sourceAccountIds,
  selectedAccountIds: scope.selectedAccountIds,
  systemAccountIds: scope.systemAccountIds,
  systemTeamIds: scope.systemTeamIds,
  groupIds: scope.groupIds,
  routeStrategyIds: scope.routeStrategyIds,
  resourceAuthorizationIds: scope.resourceAuthorizationIds,
  resourceAuthorizationGrantIds: scope.resourceAuthorizationGrantIds,
  apiKeyIds: scope.apiKeyIds,
  apiKeyRemap: scope.apiKeyRemap,
  approvedCanaryAccountIdsHash: scope.approvedCanaryAccountIdsHash,
  sourceAccountIdsHash: scope.sourceAccountIdsHash,
  selectedAccountIdsHash: scope.selectedAccountIdsHash,
  systemAccountIdsHash: scope.systemAccountIdsHash,
  systemTeamIdsHash: scope.systemTeamIdsHash,
  groupIdsHash: scope.groupIdsHash,
  routeStrategyIdsHash: scope.routeStrategyIdsHash,
  resourceAuthorizationIdsHash: scope.resourceAuthorizationIdsHash,
  resourceAuthorizationGrantIdsHash: scope.resourceAuthorizationGrantIdsHash,
  apiKeyIdsHash: scope.apiKeyIdsHash,
  apiKeyRemapHash: scope.apiKeyRemapHash
}
assert.doesNotThrow(() => assertClosureMatchesScope(scope, closure))
assert.throws(() => assertClosureMatchesScope(scope, { ...closure, schemaVersion: 2 } as never), /closure manifest 必须是 schemaVersion=1/)
assert.throws(() => assertClosureMatchesScope(scope, { ...closure, groupIds: ['outside-group'] }), /closure\.groupIds 必须与执行 scope 完全一致/)
assert.throws(() => assertClosureMatchesScope(scope, { ...closure, apiKeyRemap: [{ sourceId: 'api-key-1', targetId: 'regenerated-key' }] }), /closure\.apiKeyRemap 必须与执行 scope 完全一致/)
assert.throws(() => assertClosureMatchesScope(scope, { ...closure, apiKeyRemapHash: '0'.repeat(64) }), /closure\.apiKeyRemapHash 必须与执行 scope 一致/)
assert.throws(() => assertClosureMatchesScope(scope, { ...closure, selectedAccountIdsHash: hashStringList(['account-9']) }), /closure\.selectedAccountIds hash 必须与执行 scope 一致/)
assert.throws(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, accounts: ['*'] } }, plan), /scope accounts 不允许使用 \*/)
assert.throws(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, account_lock_states: ['*'] } }, plan), /未批准的账户同步表/)
assert.throws(() => validateScopeManifest({ ...scope, groupIdsHash: '0'.repeat(64) }, plan), /groupIdsHash 与 ID 列表不一致/)
assert.throws(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, api_keys: ['["outside-approved-scope"]'] } }, plan), /scope\.api_keys 行键必须与 scope\.apiKeyIds 精确一致/)
assert.doesNotThrow(() => validateScopeManifest({ ...scope, tables: { ...scope.tables, group_authorization_settings: [] } }, plan), /显式空 scope 允许源表为空的配置表/)
assert.throws(
  () => validateScopeManifest({ ...scope, apiKeyRemap: [{ sourceId: 'api-key-1', targetId: 'other-key' }] }, plan),
  /scope\.apiKeyRemap 必须覆盖全部 apiKeyIds，且 source\/target 集合必须一致/
)
const swappedRemapScope = {
  ...scope,
  apiKeyIds: ['api-key-1', 'api-key-2'],
  apiKeyIdsHash: hashStringList(['api-key-1', 'api-key-2']),
  apiKeyRemap: [{ sourceId: 'api-key-1', targetId: 'api-key-2' }, { sourceId: 'api-key-2', targetId: 'api-key-1' }],
  apiKeyRemapHash: '',
  tables: { ...scope.tables, api_keys: ['["api-key-1"]', '["api-key-2"]'] }
}
swappedRemapScope.apiKeyRemapHash = manifestDigest(swappedRemapScope.apiKeyRemap)
assert.throws(() => validateScopeManifest(swappedRemapScope, plan), /当前执行器不支持 API Key ID remap；必须保留原 ID，仅重建 test 专用 key_secret/)
assert.throws(() => validateScopeManifest({ ...scope, apiKeyRemapHash: '0'.repeat(64) }, plan), /scope\.apiKeyRemapHash 与 apiKeyRemap 不一致/)
const scopeWithoutTeams = { ...scope } as Record<string, unknown>
delete scopeWithoutTeams.systemTeamIds
assert.throws(() => validateScopeManifest(scopeWithoutTeams as never, plan), /scope\.systemTeamIds 必须是非空数组/)
assert.doesNotThrow(() => validateGeneratedValuesManifest({ schemaVersion: 1, tables: { accounts: { '["account-1"]': { credentials_encrypted: 'test-only' } } } }))
assert.throws(() => validateGeneratedValuesManifest({ schemaVersion: 2, tables: {} } as never), /schemaVersion=1/)
const rehearsalKey = `sk-${'a'.repeat(64)}`
assert.doesNotThrow(() => assertGeneratedApiKeyValues({
  key_hash: hashSecret(rehearsalKey),
  key_prefix: rehearsalKey.slice(0, 8),
  key_suffix: rehearsalKey.slice(-8),
  key_secret_encrypted: encryptJson({ key: rehearsalKey })
}))
assert.throws(() => assertGeneratedApiKeyValues({
  key_hash: '0'.repeat(64),
  key_prefix: rehearsalKey.slice(0, 8),
  key_suffix: rehearsalKey.slice(-8),
  key_secret_encrypted: encryptJson({ key: rehearsalKey })
}), /key_hash.*派生值不一致/)
const closureQueries: Array<{ sql: string; parameters: unknown[] | undefined }> = []
await assert.doesNotReject(() => assertTargetReferenceClosure({
  query: async (sql: string, parameters?: unknown[]) => {
    closureQueries.push({ sql, parameters })
    return { rows: [{ count: '0' }] }
  }
} as never, scope))
assert.equal(closureQueries.length, 5)
assert.match(closureQueries[4]?.sql ?? '', /request_quota_hourly_window_scope_bindings/)
assert.deepEqual(closureQueries[0]?.parameters, [
  scope.selectedAccountIds,
  scope.systemAccountIds,
  scope.resourceAuthorizationIds,
  scope.apiKeyIds,
  scope.routeStrategyIds
])
assert.match(closureQueries[1]?.sql ?? '', /authorizations\.id <> ALL\(\$5::text\[\]\)/)
assert.match(closureQueries[2]?.sql ?? '', /grant_rows\.id <> ALL\(\$5::text\[\]\)/)
assert.match(closureQueries[3]?.sql ?? '', /resource_authorization_sources/)
assert.match(closureQueries[3]?.sql ?? '', /route_groups\.route_strategy_id <> ALL\(\$6::text\[\]\)/)
await assert.rejects(() => assertTargetReferenceClosure({
  query: async () => ({ rows: [{ count: '1' }] })
} as never, scope), /目标引用闭包校验失败/)
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
const providerRows = [
  { code: 'child', parent_code: 'source' },
  { code: 'source', parent_code: null }
]
assert.deepEqual(orderProviderRows(providerRows).map((row) => row.code), ['source', 'child'])
assert.throws(() => orderProviderRows([{ code: 'child', parent_code: 'missing' }]), /缺少 scope 内父节点/)
assert.throws(() => orderProviderRows([{ code: 'child', parent_code: ' source ' }, { code: 'source', parent_code: null }]), /缺少 scope 内父节点/)
assert.throws(() => orderProviderRows([{ code: 'same', parent_code: null }, { code: 'same', parent_code: null }]), /code 重复/)
assert.throws(() => orderProviderRows([{ code: '   ', parent_code: null }]), /空 code/)
assert.throws(() => orderProviderRows([
  { code: 'a', parent_code: 'b' },
  { code: 'b', parent_code: 'a' }
]), /拓扑存在环/)

assert.doesNotThrow(() => assertTransformedReadback(
  'accounts',
  [{ id: 'account-1', credentials_encrypted: 'test-secret', cooldown_retest_failure_count: 0, updated_at: new Date('2026-09-03T00:00:00.000Z') }],
  [{
    rowKey: '["account-1"]',
    values: { credentials_encrypted: 'test-secret', cooldown_retest_failure_count: 0, updated_at: '2026-09-03T00:00:00.000Z' },
    requiredNonNullColumns: []
  }],
  [{ name: 'id', ordinal: 1 }]
))
assert.throws(() => assertTransformedReadback(
  'accounts',
  [{ id: 'account-1', credentials_encrypted: 'wrong-secret' }],
  [{ rowKey: '["account-1"]', values: { credentials_encrypted: 'test-secret' }, requiredNonNullColumns: [] }],
  [{ name: 'id', ordinal: 1 }]
), /生成\/清空值 readback 不一致/)
assert.throws(() => assertTransformedReadback(
  'model_quality_schedules',
  [{ id: 'schedule-1', next_run_at: null }],
  [{ rowKey: '["schedule-1"]', values: {}, requiredNonNullColumns: ['next_run_at'] }],
  [{ name: 'id', ordinal: 1 }]
), /默认值 readback 为空/)

console.log('execute rehearsal account sync regression passed')
