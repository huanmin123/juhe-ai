import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccessScope } from '../../storage/access-scope.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-api-key-demand-write-'))
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '0'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

const [repositories, databaseModule, cacheInvalidation, gatewayRuntimeCache, { logger }] = await Promise.all([
  import('../../storage/repositories.js'),
  import('../../storage/database.js'),
  import('../../shared/gateway-cache-invalidation.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../shared/logger.js')
])

const access: AccessScope = { systemAccountId: 'sys_admin', role: 'user' }
const database = databaseModule.getBusinessDatabase()
const unregisterInvalidators: Array<() => void> = []
logger.level = 'silent'

try {
  const preferredRoute = await repositories.findPreferredDefaultRouteStrategyReferenceAsync(access.systemAccountId)
  assert(preferredRoute, '回归夹具应存在启用的 GPT 默认策略路由')

  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const created = await repositories.createApiKeyRecordAsync({
    name: `按需写异步 Key ${suffix}`,
    description: '初始说明',
    quotaLimits: { hourly: { enabled: true, hours: 3, limit: 1.25 } }
  }, access)
  assert.equal(created.routeStrategyId, preferredRoute.id, '异步创建省略 routeStrategyId 时应在事务内选择 GPT 默认路由')

  const syncCreated = repositories.createApiKeyRecord({ name: `按需写同步 Key ${suffix}` }, access)
  assert.equal(syncCreated.routeStrategyId, preferredRoute.id, 'SQLite 同步创建省略 routeStrategyId 时也应选择 GPT 默认路由')
  assert.equal(typeof syncCreated.revision, 'string', '同步创建回执应包含列表可用的 revision')
  const initiallyDisabled = await repositories.createApiKeyRecordAsync({
    name: `按需写冷负缓存 Key ${suffix}`,
    status: 'disabled'
  }, access)
  const defaultGptGroup = database.prepare(`
    SELECT id
    FROM groups
    WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1
    LIMIT 1
  `).get(access.systemAccountId) as { id: string } | undefined
  assert(defaultGptGroup, 'API Key 运行时失效回归需要默认 GPT 分组')
  const alternateRoute = repositories.createRouteStrategy({
    name: `按需写缓存切换路由 ${suffix}`,
    mode: 'normal',
    groupBindings: [{ groupId: defaultGptGroup.id, priority: 1, status: 'active' }]
  }, access)

  seedApiKeyUsage(created.id)
  const listed = repositories.listApiKeysPage(access, { keyword: created.name, page: 1, pageSize: 20 }).items
    .find((item) => item.id === created.id)
  assert(listed, 'API Key 窄列表应返回新建记录')
  assert.deepEqual(
    Object.keys(listed.usage).sort(),
    ['requestCount', 'totalCost', 'totalTokens'],
    'API Key 列表 usage 必须严格限制为三个可展示字段'
  )
  assert.deepEqual(listed.usage, { requestCount: 11, totalTokens: 3100, totalCost: 0.0456 })
  assert.equal(Object.prototype.hasOwnProperty.call(listed, 'key'), false, 'API Key 列表不得返回完整密钥')
  assert.equal(Object.prototype.hasOwnProperty.call(listed, 'systemAccountId'), false, '个人列表不得返回 owner 字段')
  assert.equal(listed.revision, created.revision, '列表应返回 PATCH 使用的 revision')

  const secret = await repositories.findApiKeySecretAsync(created.id, access)
  assert(secret, 'secret 查询应返回现有 API Key')
  assert.equal(secret.key, created.key)
  assert.deepEqual(
    Object.keys(secret).sort(),
    ['id', 'key', 'keyPrefix', 'keySuffix', 'name', 'systemAccountId'],
    'secret repository 结果只能包含审计与密钥展示实际需要的字段'
  )

  const runtimeInvalidations: string[] = []
  const quotaInvalidations: Array<string | undefined> = []
  unregisterInvalidators.push(
    cacheInvalidation.registerGatewayRuntimeCacheInvalidator((reason, metadata) => {
      if (metadata.source === 'local') runtimeInvalidations.push(reason)
    }),
    cacheInvalidation.registerApiKeyQuotaCacheInvalidator((apiKeyId) => {
      quotaInvalidations.push(apiKeyId)
    }),
    cacheInvalidation.registerGatewayApiKeyValidationServerInvalidator(async () => undefined)
  )

  const initialBinding = quotaBinding(created.id)
  assert.equal(initialBinding?.window_hours, 3, '创建应在同一事务写入小时额度窗口绑定')

  gatewayRuntimeCache.clearGatewayRuntimeCacheLocal()
  const initiallyDisabledRuntime = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(initiallyDisabled.key)
  assert.equal(initiallyDisabledRuntime.apiKey, undefined, '冷负缓存回归必须先缓存从未启用过的无效 Key')
  const enabledFromColdNegative = await repositories.patchApiKeyAsync(
    initiallyDisabled.id,
    { status: 'active' },
    initiallyDisabled.revision,
    access
  )
  assert(enabledFromColdNegative)
  const runtimeAfterColdNegativeEnable = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(initiallyDisabled.key)
  assert.equal(
    runtimeAfterColdNegativeEnable.apiKey?.id,
    initiallyDisabled.id,
    '首次读取即为负缓存的 Key 启用后也必须立即生效，不能等待负缓存软过期'
  )

  database.exec(`
    CREATE TRIGGER api_key_demand_write_unrelated_columns_guard
    BEFORE UPDATE OF route_strategy_id, status, expires_at, quota_limits_json,
      availability_schedule_json, availability_schedule_next_check_at ON api_keys
    BEGIN
      SELECT RAISE(ABORT, 'unrelated api key column updated');
    END
  `)
  const noOpCapture = await captureBusinessSql(() => (
    repositories.patchApiKeyAsync(created.id, { description: '初始说明' }, created.revision, access)
  ))
  const noOp = noOpCapture.result
  assert(noOp)
  assert.deepEqual(noOp.result.changedFields, [], '相同值 PATCH 应为 no-op')
  assert.equal(noOp.result.revision, created.revision, 'no-op 不得推进 revision')
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(noOpCapture.sql),
    ['description', 'id', 'name', 'system_account_id', 'updated_at'],
    '说明 PATCH 只应读取定位、审计、revision 与说明旧值'
  )
  assert.equal(noOpCapture.sql.length, 1, '说明 no-op 应只执行一条窄 SELECT，不得产生 DML 或关系同步')
  assert.deepEqual(runtimeInvalidations, [], 'no-op 不得失效 gateway runtime')
  assert.deepEqual(quotaInvalidations, [], 'no-op 不得失效 quota cache')

  const descriptionPatch = await repositories.patchApiKeyAsync(
    created.id,
    { description: '只改说明' },
    noOp.result.revision,
    access
  )
  assert(descriptionPatch)
  assert.deepEqual(descriptionPatch.result.changedFields, ['description'])
  assert.deepEqual(descriptionPatch.result.rowPatch, {
    revision: descriptionPatch.result.revision,
    description: '只改说明'
  }, '单字段 PATCH 响应只应返回行合并所需字段')
  assert.equal(descriptionPatch.ownerSystemAccountId, access.systemAccountId, '字段级 PATCH 必须保留审计 owner')
  assert.equal(descriptionPatch.resourceName, created.name, '非名称 PATCH 必须保留当前审计资源名')
  assert.deepEqual(descriptionPatch.before, { description: '初始说明' }, '审计 before 只应包含真实变化字段')
  assert.deepEqual(descriptionPatch.after, { description: '只改说明' }, '审计 after 只应包含真实变化字段')
  assert.equal(quotaBinding(created.id)?.updated_at, initialBinding?.updated_at, '说明变更不得重建小时额度窗口')
  assert.deepEqual(runtimeInvalidations, [], '说明变更不得失效 gateway runtime')
  assert.deepEqual(quotaInvalidations, [], '说明变更不得失效 quota cache')
  database.exec('DROP TRIGGER api_key_demand_write_unrelated_columns_guard')

  const staleRevision = descriptionPatch.result.revision
  const namePatchCapture = await captureBusinessSql(() => repositories.patchApiKeyAsync(
    created.id,
    { name: `${created.name} 改` },
    staleRevision,
    access
  ))
  const namePatch = namePatchCapture.result
  assert(namePatch)
  assert.deepEqual(namePatch.result.changedFields, ['name'])
  assert.equal(namePatch.resourceName, `${created.name} 改`, '名称 PATCH 审计资源名必须使用提交后的名称')
  assert.deepEqual(namePatch.before, { name: created.name })
  assert.deepEqual(namePatch.after, { name: `${created.name} 改` })
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(namePatchCapture.sql),
    ['id', 'is_default', 'name', 'purpose', 'system_account_id', 'updated_at'],
    '名称 PATCH 只应额外读取默认 Key 限制所需字段'
  )
  assert.deepEqual(runtimeInvalidations, [], '名称变更不得失效 gateway runtime')
  assert.deepEqual(quotaInvalidations, [], '名称变更不得失效 quota cache')

  await assert.rejects(
    repositories.patchApiKeyAsync(
      created.id,
      { expiresAt: '2099-01-01T00:00:00.000Z' },
      staleRevision,
      access
    ),
    (error: unknown) => error instanceof repositories.ApiKeyRevisionConflictError,
    '相同 revision 的并发分离字段更新不得静默覆盖'
  )
  const afterConflict = rawApiKeyRow(created.id)
  assert.equal(afterConflict.name, `${created.name} 改`, '先成功的字段不得被冲突请求覆盖')
  assert.equal(afterConflict.expires_at, null, '冲突请求不得写入自己的字段')

  const initialGatewayRuntime = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(created.key)
  assert.equal(initialGatewayRuntime.apiKey?.route_strategy_id, preferredRoute.id, '运行时预热应读取变更前路由')
  const routePatchCapture = await captureBusinessSql(() => repositories.patchApiKeyAsync(
    created.id,
    { routeStrategyId: alternateRoute.id },
    namePatch.result.revision,
    access
  ))
  const routePatch = routePatchCapture.result
  assert(routePatch)
  assert.deepEqual(routePatch.result.changedFields, ['routeStrategyId'])
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(routePatchCapture.sql),
    ['id', 'is_default', 'key_hash', 'name', 'purpose', 'route_strategy_id', 'system_account_id', 'updated_at'],
    '路由 PATCH 只应读取默认 Key 限制、旧路由与定点鉴权失效所需字段'
  )
  const gatewayRuntimeAfterRoutePatch = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(created.key)
  assert.equal(
    gatewayRuntimeAfterRoutePatch.apiKey?.route_strategy_id,
    alternateRoute.id,
    '路由 PATCH 提交后下一次运行时读取必须立即看到新路由，不能命中 60 秒旧快照'
  )

  const expiresPatchCapture = await captureBusinessSql(() => repositories.patchApiKeyAsync(
    created.id,
    { expiresAt: '2099-01-01T00:00:00.000Z' },
    routePatch.result.revision,
    access
  ))
  const expiresPatch = expiresPatchCapture.result
  assert(expiresPatch)
  assert.deepEqual(expiresPatch.result.changedFields, ['expiresAt'])
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(expiresPatchCapture.sql),
    ['expires_at', 'id', 'key_hash', 'name', 'system_account_id', 'updated_at'],
    '有效期 PATCH 只应额外读取旧有效期与定点鉴权失效所需字段'
  )
  assert.equal(runtimeInvalidations.length, 0, '有效期变更只需定点 validation 失效，不得清理全局 gateway runtime')
  assert.deepEqual(quotaInvalidations, [], '有效期变更不得失效 quota cache')
  assert.equal(quotaBinding(created.id)?.updated_at, initialBinding?.updated_at, '有效期变更不得重建小时额度窗口')

  database.exec(`
    CREATE TRIGGER api_key_demand_write_quota_binding_failure
    BEFORE INSERT ON request_quota_hourly_window_scope_bindings
    WHEN NEW.source_type = 'api_key' AND NEW.source_id = '${created.id}' AND NEW.window_hours = 7
    BEGIN
      SELECT RAISE(ABORT, 'forced quota binding failure');
    END
  `)
  await assert.rejects(
    repositories.patchApiKeyAsync(created.id, {
      quotaLimits: { hourly: { enabled: true, hours: 7, limit: 2.5 } }
    }, expiresPatch.result.revision, access),
    /forced quota binding failure/,
    '额度窗口写失败应回滚 API Key 主表更新'
  )
  assert.equal(rawApiKeyRow(created.id).updated_at, expiresPatch.result.revision, '额度窗口失败后 revision 必须回滚')
  assert.equal(quotaBinding(created.id)?.window_hours, 3, '额度窗口失败后原绑定必须保留')
  assert.equal(runtimeInvalidations.length, 0, '回滚事务不得触发 runtime 失效')
  assert.deepEqual(quotaInvalidations, [], '回滚事务不得触发 quota 失效')
  database.exec('DROP TRIGGER api_key_demand_write_quota_binding_failure')

  const quotaPatchCapture = await captureBusinessSql(() => repositories.patchApiKeyAsync(created.id, {
    quotaLimits: { hourly: { enabled: true, hours: 7, limit: 2.5 } }
  }, expiresPatch.result.revision, access))
  const quotaPatch = quotaPatchCapture.result
  assert(quotaPatch)
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(quotaPatchCapture.sql),
    ['id', 'key_hash', 'name', 'quota_limits_json', 'status', 'system_account_id', 'updated_at'],
    '额度 PATCH 只应额外读取额度同步与鉴权失效所需字段'
  )
  assert.equal(quotaBinding(created.id)?.window_hours, 7, '真实额度变化应在同一事务更新小时窗口')
  assert.equal(runtimeInvalidations.length, 0, '额度变化只需定点 validation/quota 失效，不得清理全局 gateway runtime')
  assert.deepEqual(quotaInvalidations, [created.id], '额度变化应只失效当前 API Key 的 quota cache')

  const disabled = await repositories.patchApiKeyAsync(created.id, { status: 'disabled' }, quotaPatch.result.revision, access)
  assert(disabled)
  assert.equal(quotaBinding(created.id), undefined, '真实停用状态变化应移除小时额度窗口绑定')
  assert.equal(runtimeInvalidations.length, 0, '停用只需定点 validation 失效，不得清理全局 gateway runtime')
  const gatewayRuntimeAfterDisable = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(created.key)
  assert.equal(gatewayRuntimeAfterDisable.apiKey, undefined, '停用 PATCH 后下一次运行时读取必须立即拒绝旧缓存中的 Key')

  const active = await repositories.patchApiKeyAsync(created.id, { status: 'active' }, disabled.result.revision, access)
  assert(active)
  assert.equal(quotaBinding(created.id)?.window_hours, 7, '重新启用应恢复小时额度窗口绑定')
  assert.equal(runtimeInvalidations.length, 0, '启用只需定点 validation 失效，不得清理全局 gateway runtime')
  const gatewayRuntimeAfterEnable = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(created.key)
  assert.equal(gatewayRuntimeAfterEnable.apiKey?.status, 'active', '重新启用后下一次运行时读取必须立即恢复当前 Key')

  const statusNoOpCapture = await captureBusinessSql(() => (
    repositories.patchApiKeyAsync(created.id, { status: 'active' }, active.result.revision, access)
  ))
  const statusNoOp = statusNoOpCapture.result
  assert(statusNoOp)
  assert.deepEqual(statusNoOp.result.changedFields, [], '相同状态 PATCH 应为 no-op')
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(statusNoOpCapture.sql),
    ['id', 'key_hash', 'name', 'quota_limits_json', 'status', 'system_account_id', 'updated_at'],
    '状态 PATCH 必须携带额度联动与鉴权失效所需最小字段'
  )
  assert.equal(statusNoOpCapture.sql.length, 1, '状态 no-op 应只执行一条窄 SELECT，不得同步额度或写 revision')
  assert.equal(runtimeInvalidations.length, 0, '相同状态不得触发 gateway runtime 失效')

  const scheduleNoOpCapture = await captureBusinessSql(() => (
    repositories.patchApiKeyAsync(created.id, { availabilitySchedule: null }, statusNoOp.result.revision, access)
  ))
  const scheduleNoOp = scheduleNoOpCapture.result
  assert(scheduleNoOp)
  assert.deepEqual(scheduleNoOp.result.changedFields, [], '相同时间计划 PATCH 应为 no-op')
  assert.deepEqual(
    apiKeyPatchSelectColumnsFromSql(scheduleNoOpCapture.sql),
    ['availability_schedule_json', 'id', 'key_hash', 'name', 'quota_limits_json', 'status', 'system_account_id', 'updated_at'],
    '时间计划 PATCH 只应读取计划、状态联动、额度同步与鉴权失效所需字段'
  )
  assert.equal(scheduleNoOpCapture.sql.length, 1, '时间计划 no-op 应只执行一条窄 SELECT，不得产生 DML')

  const runtimeInvalidationsBeforeRefresh = runtimeInvalidations.length
  const quotaInvalidationsBeforeRefresh = quotaInvalidations.length
  const refreshed = await repositories.refreshApiKeySecretForManagementAsync(created.id, access)
  assert(refreshed, '密钥刷新应返回最小管理 outcome')
  assert.equal(refreshed.validationCacheError, undefined, '正常 validation cache 失效不得产生失败结果')
  assert.equal(runtimeInvalidations.length, runtimeInvalidationsBeforeRefresh, '密钥刷新不得清理无关 gateway runtime cache')
  assert.equal(quotaInvalidations.length, quotaInvalidationsBeforeRefresh, '密钥刷新不得清理无关 quota cache')
  const gatewayRuntimeForPreviousSecret = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(created.key)
  assert.equal(gatewayRuntimeForPreviousSecret.apiKey, undefined, '密钥刷新后旧密钥必须立即失效，不能命中旧运行时快照')
  const gatewayRuntimeForRefreshedSecret = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(refreshed.result.key)
  assert.equal(gatewayRuntimeForRefreshedSecret.apiKey?.id, created.id, '密钥刷新后新密钥必须立即读取当前运行时')

  let persistentInvalidationAttempts = 0
  const unregisterFailingValidationInvalidator = cacheInvalidation.registerGatewayApiKeyValidationCacheInvalidator((apiKeyId, metadata) => {
    if (apiKeyId === created.id && metadata.source === 'local') {
      persistentInvalidationAttempts += 1
      throw new Error('forced validation cache invalidation failure')
    }
  })
  try {
    const committedWithValidationFailure = await repositories.patchApiKeyAsync(created.id, {
      expiresAt: '2097-01-01T00:00:00.000Z'
    }, refreshed.result.revision, access)
    assert(committedWithValidationFailure, 'validation cache 失败后 PATCH 仍应返回已提交 outcome 供路由记录真实日志')
    assert(
      committedWithValidationFailure.validationCacheError instanceof repositories.ApiKeyValidationCacheInvalidationError,
      'validation cache 失败必须作为独立错误状态传给路由'
    )
    assert.equal(
      rawApiKeyRow(created.id).updated_at,
      committedWithValidationFailure.result.revision,
      'validation cache 失败不得回滚或伪装已经提交的数据库更新'
    )
  } finally {
    unregisterFailingValidationInvalidator()
  }
  assert.equal(persistentInvalidationAttempts, 3, 'validation cache 持续失败应在有界重试耗尽后报错')

  let transientInvalidationAttempts = 0
  const unregisterTransientValidationInvalidator = cacheInvalidation.registerGatewayApiKeyValidationCacheInvalidator((apiKeyId, metadata) => {
    if (apiKeyId === created.id && metadata.source === 'local') {
      transientInvalidationAttempts += 1
      if (transientInvalidationAttempts < 3) {
        throw new Error('forced transient validation cache invalidation failure')
      }
    }
  })
  try {
    const recoveredAfterRetry = await repositories.patchApiKeyAsync(created.id, {
      expiresAt: '2098-01-01T00:00:00.000Z'
    }, rawApiKeyRow(created.id).updated_at, access)
    assert(recoveredAfterRetry, 'validation cache 短暂失败后 PATCH 应保留已提交 outcome')
    assert.equal(recoveredAfterRetry.validationCacheError, undefined, '有界重试成功后不应伪造失败')
    assert.equal(transientInvalidationAttempts, 3, '短暂失败应在第三次尝试恢复')
  } finally {
    unregisterTransientValidationInvalidator()
  }

  const gatewayRuntimeBeforeDelete = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(syncCreated.key)
  assert.equal(gatewayRuntimeBeforeDelete.apiKey?.id, syncCreated.id, '删除回归必须先预热待删除 Key 的运行时快照')
  const deleted = await repositories.deleteApiKeyWithRelatedCleanupAsync(syncCreated.id, access)
  assert.equal(deleted.deleted, true, '定向删除回归应删除普通 API Key')
  if (deleted.deleted) {
    assert.equal(deleted.ownerSystemAccountId, access.systemAccountId, '删除 outcome 应携带日志 owner')
    assert.equal(deleted.resourceName, syncCreated.name, '删除 outcome 应携带日志资源名称')
  }
  const gatewayRuntimeAfterDelete = await gatewayRuntimeCache.readCachedGatewayRuntimeAsync(syncCreated.key)
  assert.equal(gatewayRuntimeAfterDelete.apiKey, undefined, '删除后旧密钥必须立即失效，不能继续命中运行时快照')

  assertSourceContracts()
  console.log('api-key-demand-write-regression passed')
} finally {
  for (const unregister of unregisterInvalidators.reverse()) unregister()
  const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedApiKeyUsage(apiKeyId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id,
      request_count, input_tokens, output_tokens, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, 'api_key', ?, ?, ?, ?, ?, ?, ?)
  `).run(access.systemAccountId, apiKeyId, 11, 2200, 900, 0.0456, now, now)
}

function quotaBinding(apiKeyId: string): { window_hours: number; updated_at: string } | undefined {
  return database.prepare(`
    SELECT window_hours, updated_at
    FROM request_quota_hourly_window_scope_bindings
    WHERE source_type = 'api_key' AND source_id = ?
  `).get(apiKeyId) as { window_hours: number; updated_at: string } | undefined
}

function rawApiKeyRow(apiKeyId: string): {
  name: string
  expires_at: string | null
  quota_limits_json: string | null
  updated_at: string
} {
  const row = database.prepare(`
    SELECT name, expires_at, quota_limits_json, updated_at
    FROM api_keys
    WHERE id = ?
  `).get(apiKeyId) as {
    name: string
    expires_at: string | null
    quota_limits_json: string | null
    updated_at: string
  } | undefined
  assert(row, 'API Key 主表记录应存在')
  return row
}

async function captureBusinessSql<T>(operation: () => Promise<T>): Promise<{ result: T; sql: string[] }> {
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const sql: string[] = []
  database.prepare = ((statementSql: string) => {
    sql.push(statementSql)
    return originalPrepare(statementSql)
  }) as typeof database.prepare
  try {
    return { result: await operation(), sql }
  } finally {
    database.prepare = originalPrepare
  }
}

function apiKeyPatchSelectColumnsFromSql(sql: readonly string[]): string[] {
  const selectSql = sql.find((statementSql) => (
    /\bFROM\s+["`]?api_keys["`]?\s+api_keys\b/i.test(statementSql)
    && /\bWHERE\s+api_keys\.id\s*=\s*\?/i.test(statementSql)
  ))
  assert(selectSql, `未捕获到 API Key PATCH 定位 SELECT：${sql.join('\n')}`)
  const selectList = selectSql.match(/\bSELECT\b([\s\S]*?)\bFROM\s+["`]?api_keys["`]?\s+api_keys\b/i)?.[1]
  assert(selectList, `无法解析 API Key PATCH SELECT 投影：${selectSql}`)
  return [...selectList.matchAll(/\bapi_keys\.([a-z_]+)\b/gi)]
    .map((match) => match[1].toLowerCase())
    .sort()
}

function assertSourceContracts(): void {
  const repositorySource = readFileSync(fileURLToPath(new URL('../../storage/api-key.repository.ts', import.meta.url)), 'utf8')
  const listSource = sourceBetween(repositorySource, 'function queryApiKeys(', 'function buildPostgresApiKeyKeywordCte(')
  assert.match(listSource, /apiKeyListItemsFromRows/)
  assert.doesNotMatch(listSource, /apiKeySummariesFromRows/, '列表不得回退到完整 ApiKeySummary mapper')

  const pageColumns = sourceBetween(repositorySource, 'function apiKeyPageColumns(', 'function apiKeyPageJoins(')
  assert.doesNotMatch(pageColumns, /key_secret_encrypted|input_tokens|output_tokens/, '列表 SELECT 不得读取密钥或完整用量字段')

  const secretColumns = sourceBetween(repositorySource, 'function apiKeySecretColumns(', 'function apiKeySecretRecordFromRow(')
  assert.doesNotMatch(secretColumns, /updated_at|route_strategy|quota_limits|availability_schedule/, 'secret SELECT 必须保持极小字段集')

  const patchSource = sourceBetween(repositorySource, 'export async function patchApiKeyAsync(', 'function apiKeyStatusForScheduleMutation(')
  assert.match(patchSource, /SET \$\{setClauses\.join\(', '\)\}/, 'PATCH 必须按真实变化动态生成 SET')
  assert.match(patchSource, /AND updated_at = \?/, 'PATCH 必须由数据库 CAS 保护 revision')
  assert.match(patchSource, /apiKeyPatchSelectColumns\(tx, input\)/, 'PATCH 定位 SELECT 必须按提交字段生成投影')
  assert.doesNotMatch(patchSource, /findApiKeySummary/, 'PATCH 不得在写前或写后物化完整摘要')
  assert.doesNotMatch(patchSource, /notifyGatewayRuntimeCacheInvalidation|runtime:\s*true/, 'PATCH 定点 validation 失效后不得清理无关全局 runtime cache')

  const legacyAsyncUpdateSource = sourceBetween(repositorySource, 'export async function updateApiKeyAsync(', 'export async function patchApiKeyAsync(')
  assert.match(legacyAsyncUpdateSource, /patchApiKeyAsync/, '生产异步兼容 writer 必须复用字段级 PATCH')
  assert.match(legacyAsyncUpdateSource, /findApiKeyUpdateSnapshotAsync/, '生产异步兼容 writer 应读取公开响应实际需要的窄快照')
  assert.match(legacyAsyncUpdateSource, /applyApiKeyMutationRowPatch/, '生产异步兼容 writer 应在内存合并字段级 PATCH 回执')
  assert.doesNotMatch(legacyAsyncUpdateSource, /SET name|findApiKeySummaryAsync|apiKeySummariesFromRowsAsync/, '异步兼容 writer 不得回退整行覆盖或完整摘要物化')
  const updateSnapshotSource = sourceBetween(repositorySource, 'async function findApiKeyUpdateSnapshotAsync(', 'function applyApiKeyMutationRowPatch(')
  assert.match(updateSnapshotSource, /api_keys\.key_prefix/, '公开更新回执窄快照应保留实际返回的 Key 前缀')
  assert.doesNotMatch(updateSnapshotSource, /usage|quota_limits|description|key_suffix|system_accounts/, '公开更新回执不得读取未返回字段、用户关联或 usage')

  const revisionSource = sourceBetween(repositorySource, 'function apiKeyRevisionSelectExpression(', 'function apiKeyPageJoins(')
  assert.match(revisionSource, /return `\$\{tableAlias\}\.updated_at`/, 'revision 必须直接读取数据库 text 原值')
  assert.doesNotMatch(revisionSource, /to_char|AT TIME ZONE/, 'text revision 不得当作 timestamptz 转换')

  const bestEffortCacheSource = sourceBetween(
    repositorySource,
    'async function invalidateCommittedApiKeyCachesBestEffortAsync(',
    'async function invalidateRequiredApiKeyValidationCacheAsync('
  )
  assert.doesNotMatch(bestEffortCacheSource, /invalidateGatewayApiKeyCacheByIdAsync/, 'validation cache 不得混入 best-effort 失效集合')
  const requiredValidationCacheSource = sourceBetween(
    repositorySource,
    'async function invalidateRequiredApiKeyValidationCacheAsync(',
    'export function refreshApiKeySecret('
  )
  assert.match(requiredValidationCacheSource, /await notifyGatewayApiKeyValidationCacheInvalidationAsync\(apiKeyId, reason, keyHashes\)/, '提交后 validation cache 必须等待本地与跨进程失效结果并携带定点 hash')
  assert.match(requiredValidationCacheSource, /ApiKeyValidationCacheInvalidationError/, 'validation cache 失败必须形成可传递的失败结果')

  const gatewayCacheInvalidationSource = readFileSync(fileURLToPath(new URL('../../shared/gateway-cache-invalidation.ts', import.meta.url)), 'utf8')
  assert.match(gatewayCacheInvalidationSource, /'gateway_api_key_validation_cache'/, 'validation cache 必须使用独立跨进程失效主题')
  assert.match(gatewayCacheInvalidationSource, /publishGatewayCacheInvalidationToRuntimeStateAsync\([\s\S]*?'gateway_api_key_validation_cache'/, 'validation cache 必需失效必须等待 runtime state 发布')
  assert.match(gatewayCacheInvalidationSource, /getJsonMany<GatewayCacheInvalidationState>/, '鉴权热路径必须用一次 MGET 批量读取全部失效主题')
  assert.match(gatewayCacheInvalidationSource, /handler\(undefined,\s*\{[\s\S]*?source:\s*'runtime_state'/, '跨进程 validation 单槽事件必须全清，避免连续 Key 事件覆盖后漏失效')

  const gatewayRuntimeCacheSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/runtime/runtime-cache.service.ts', import.meta.url)), 'utf8')
  assert.match(gatewayRuntimeCacheSource, /gatewayRuntimeCacheKeysByApiKeyId/, '网关运行时缓存必须按 API Key ID 建立反向索引')
  assert.match(gatewayRuntimeCacheSource, /registerGatewayApiKeyValidationCacheInvalidator\(\(apiKeyId, metadata\)/, '网关运行时缓存必须复用必需 validation 失效主题')
  assert.match(gatewayRuntimeCacheSource, /metadata\.source === 'local' \? metadata\.keyHashes/, '本地失效必须接收事务内读取的密钥 hash 以删除冷负缓存')
  assert.match(gatewayRuntimeCacheSource, /pendingGatewayRuntimeLoads\.delete\(cacheKey\)/, '定点失效不得中断其他 API Key 的并发运行时加载')
  assert.match(gatewayRuntimeCacheSource, /gatewayRuntimeCache\.delete\(cacheKey\)/, 'API Key 失效必须精确删除关联运行时快照')

  const refreshManagementSource = sourceBetween(
    repositorySource,
    'export async function refreshApiKeySecretForManagementAsync(',
    'export interface ApiKeyDeleteCleanupTarget'
  )
  assert.match(refreshManagementSource, /invalidateRequiredApiKeyValidationCacheAsync/, '刷新密钥提交后必须执行 validation cache 必需失效')
  assert.doesNotMatch(refreshManagementSource, /invalidateCommittedApiKeyCachesBestEffortAsync|quota:\s*true|notifyApiKeyQuotaCacheInvalidation/, '刷新密钥不得清理无关 lookup、runtime 或 quota cache')

  const apiKeyRoutesSource = readFileSync(fileURLToPath(new URL('../../modules/api-keys/api-keys.routes.ts', import.meta.url)), 'utf8')
  const refreshRouteSource = sourceBetween(apiKeyRoutesSource, "apiKeysRouter.post('/:id/refresh-key'", "apiKeysRouter.post('/', mutationGuard(")
  assert.match(refreshRouteSource, /statusCode:\s*outcome\.validationCacheError \? 500 : 200/, '刷新已提交但 validation 失效失败时日志必须记录 500')
  assert.match(refreshRouteSource, /next\(outcome\.validationCacheError\)/, '刷新 validation 失效失败必须走通用 500 错误处理')

  const createRouteSource = sourceBetween(apiKeyRoutesSource, "apiKeysRouter.post('/', mutationGuard(", "apiKeysRouter.patch('/:id'")
  assert.match(createRouteSource, /next\(error\)/, '创建未知错误必须交给全局错误处理')
  assert.doesNotMatch(createRouteSource, /message\.includes\('已存在'\) \? 409 : 400/, '创建不得把未知错误一律映射成 400')

  const patchRouteSource = sourceBetween(apiKeyRoutesSource, "apiKeysRouter.patch('/:id'", "apiKeysRouter.delete('/:id'")
  assert.match(patchRouteSource, /statusCode:\s*outcome\.validationCacheError \? 500 : 200/, '更新已提交但 validation 失效失败时日志必须记录 500')
  assert.match(patchRouteSource, /next\(outcome\.validationCacheError\)/, '更新 validation 失效失败必须走通用 500 错误处理')
  assert.match(patchRouteSource, /next\(error\)/, '更新未知错误必须交给全局错误处理')

  const deleteRouteSource = sourceBetween(apiKeyRoutesSource, "apiKeysRouter.delete('/:id'", 'function parseApiKeyListOptions(')
  assert.doesNotMatch(deleteRouteSource, /findApiKeySummaryAsync/, '删除路由不得为日志先宽读完整摘要')
  assert.match(deleteRouteSource, /deleteResult\.ownerSystemAccountId[\s\S]*deleteResult\.resourceName/, '删除日志应直接使用删除 outcome 元数据')
  assert.match(deleteRouteSource, /statusCode:\s*deleteResult\.validationCacheError \? 500 : 204/, '删除日志必须记录真实最终 HTTP 状态')
  assert.match(deleteRouteSource, /next\(deleteResult\.validationCacheError\)/, '删除 validation 失效失败必须走通用 500 错误处理')
  assert.match(deleteRouteSource, /setNoStoreHeaders\(res\)[\s\S]*res\.status\(204\)\.send\(\)/, '删除 204 必须禁止响应缓存')

  const deleteRepositorySource = sourceBetween(
    repositorySource,
    'export async function deleteApiKeyWithRelatedCleanupAsync(',
    'export function ensureDefaultApiKeysForSystemAccount('
  )
  assert.doesNotMatch(deleteRepositorySource, /notifyGatewayRuntimeCacheInvalidation|runtime:\s*true/, '删除定点 validation 失效后不得清理无关全局 runtime cache')

  const externalPushSource = readFileSync(fileURLToPath(new URL('../../modules/external-integrations/external-public-account-push.service.ts', import.meta.url)), 'utf8')
  const externalAsyncUpdateSource = sourceBetween(externalPushSource, 'export async function updatePublicApiKeyAsync(', 'export function deletePublicApiKey(')
  assert.doesNotMatch(externalAsyncUpdateSource, /findApiKeySummaryAsync/, '外部公开 API Key 异步更新不得在写前宽读完整摘要')
  assert.match(externalAsyncUpdateSource, /updateApiKeyAsync\(apiKeyId,/, '外部公开 API Key 生产更新必须走字段级异步 writer')
  const externalAsyncDeleteSource = sourceBetween(externalPushSource, 'export async function deletePublicApiKeyAsync(', 'export function listPublicApiKeys(')
  assert.match(externalAsyncDeleteSource, /if \(result\.deleted && result\.validationCacheError\)[\s\S]*throw result\.validationCacheError/, '外部公开删除不得吞掉已提交后的 validation cache 失效失败')

  const externalRoutesSource = readFileSync(fileURLToPath(new URL('../../modules/external-integrations/external-integrations.routes.ts', import.meta.url)), 'utf8')
  const externalDeleteRouteSource = sourceBetween(externalRoutesSource, "'/api-key/del'", "'/account/add'")
  assert.match(externalDeleteRouteSource, /ApiKeyValidationCacheInvalidationError[\s\S]*res\.status\(500\)/, '外部公开删除应把提交后失效失败分类为服务端错误')

  const userReferenceSource = readFileSync(fileURLToPath(new URL('../../storage/user-reference-data.repository.ts', import.meta.url)), 'utf8')
  const preferredDefaultSource = sourceBetween(userReferenceSource, 'export async function findPreferredDefaultRouteStrategyReferenceAsync(', 'function userReferenceDataFromRows(')
  assert.match(preferredDefaultSource, /FOR UPDATE OF route_strategies, route_strategy_groups, groups/, '事务内默认 GPT 路由选择必须锁定路由、绑定和默认分组')
  assert.doesNotMatch(userReferenceSource, /return client\.driver === 'postgres' \? 'TRUE'/, 'PG 整型标志位不得与 boolean TRUE 比较')
  assert.match(repositorySource, /findPreferredDefaultRouteStrategyReferenceAsync\(systemAccountId, tx, true\)/, 'API Key 默认路由必须在创建事务内启用行锁')
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert(startIndex >= 0 && endIndex > startIndex, `无法提取源码片段：${start} -> ${end}`)
  return source.slice(startIndex, endIndex)
}
