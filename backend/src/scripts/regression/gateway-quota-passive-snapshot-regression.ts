import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import { checkGatewayApiKeyQuota, checkGatewayApiKeyQuotaAsync } from '../../modules/gateway/quota/api-key-quota.service.js'
import {
  checkGatewayAuthorizationQuotaBatchAsync,
  clearAuthorizationQuotaCache,
  clearAuthorizationQuotaCacheAsync
} from '../../modules/gateway/quota/authorization-quota.service.js'
import {
  checkGatewayAuthorizationQuotaBatchByIds,
  checkGatewayAuthorizationQuotaByIds
} from '../../modules/gateway/quota/authorization-quota.service.js'
import {
  clearGatewayQuotaSnapshot,
  gatewayQuotaSnapshotAuthorizationPageSize,
  gatewayQuotaSnapshotCostPageSize,
  gatewayQuotaSnapshotRuntime,
  invalidateGatewayAuthorizationQuotaSnapshot,
  replaceGatewayQuotaSnapshot
} from '../../modules/gateway/quota/quota-snapshot-cache.service.js'
import * as dbServiceIpc from '../../modules/db-service/db-service-ipc.js'
import { notifyAuthorizationQuotaCacheInvalidation, syncGatewayCacheInvalidationsFromRuntimeState } from '../../shared/gateway-cache-invalidation.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-quota-passive-snapshot-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-quota-passive-snapshot-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')

class FakeDbServiceChild extends EventEmitter {
  readonly pid = 525252
  readonly connected = true
  readonly sentMessages: unknown[] = []
  onSend?: (message: unknown) => void

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    this.sentMessages.push(message)
    this.onSend?.(message)
    callback?.()
    return true
  }
}

try {
  assertGatewayQuotaSnapshotSourcesBounded()
  assertGatewayQuotaRequestPathUsesAsyncOnly()
  assertLocalQuotaReadersRejectServerRole()
  assertAuthorizationQuotaInvalidationSourcesConnected()
  await clearAuthorizationQuotaCacheAsync()
  clearGatewayQuotaSnapshot()
  await syncGatewayCacheInvalidationsFromRuntimeState()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [{
      systemAccountId: 'sys_passive_quota',
      scopeType: 'api_key',
      scopeId: 'key_passive_quota',
      hourlyWindowHours: 3,
      costs: {
        hourly: 15,
        daily: 15,
        weekly: 15,
        monthly: 15,
        total: 15
      }
    }],
    authorizationEntries: [
      {
        scopeType: 'group_authorization',
        authorizationId: 'group_auth_passive_ok',
        decision: { allowed: true }
      },
      {
        scopeType: 'account_authorization',
        authorizationId: 'account_auth_passive_blocked',
        decision: { allowed: false, message: '额度已用完，请联系管理员提升额度' }
      }
    ]
  })

  const apiKeyDecision = await checkGatewayApiKeyQuotaAsync({
    id: 'key_passive_quota',
    system_account_id: 'sys_passive_quota',
    route_strategy_id: 'route_passive_quota',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'group_passive_quota',
    status: 'active',
    availability_schedule_active: 1,
    expires_at: null,
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({
      hourly: { enabled: true, hours: 3, limit: 10 },
      daily: { enabled: true, limit: 10 },
      weekly: { enabled: true, limit: 10 },
      monthly: { enabled: true, limit: 10 },
      total: { enabled: true, limit: 10 }
    })
  } as GatewayApiKeyRow)
  assert.equal(apiKeyDecision.allowed, false, 'server 请求链路应直接使用 worker 推送的 API Key 额度快照拦截')

  const authorizationDecisions = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_passive_quota',
      groupAccessType: 'authorized',
      groupAuthorizationId: 'group_auth_passive_ok'
    } as GroupUsageAccessMetadata,
    accounts: [
      passiveAccount('account_passive_ok', 'account_auth_passive_ok_missing'),
      passiveAccount('account_passive_blocked', 'account_auth_passive_blocked')
    ]
  })
  assert.equal(authorizationDecisions.get('account_passive_ok')?.allowed, true, '快照缺失的授权组合应短时放行等待 worker 追平')
  assert.equal(authorizationDecisions.get('account_passive_blocked')?.allowed, false, 'server 请求链路应直接使用 worker 推送的授权额度快照拦截')

  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [{
      systemAccountId: 'sys_passive_quota',
      scopeType: 'api_key',
      scopeId: 'key_auth_invalidation_cost_snapshot',
      costs: {
        hourly: 20,
        daily: 20,
        weekly: 20,
        monthly: 20,
        total: 20
      }
    }],
    authorizationEntries: [
      {
        scopeType: 'group_authorization',
        authorizationId: 'group_auth_stale_snapshot',
        decision: { allowed: true }
      },
      {
        scopeType: 'account_authorization',
        authorizationId: 'account_auth_stale_snapshot',
        decision: { allowed: true }
      }
    ],
    costEntriesComplete: true,
    authorizationEntriesComplete: true
  })
  const staleAuthorizationBeforeInvalidation = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_passive_quota',
      groupAccessType: 'authorized',
      groupAuthorizationId: 'group_auth_stale_snapshot',
      groupAuthorizationQuotaLimited: true
    } as GroupUsageAccessMetadata,
    accounts: [passiveAccount('account_stale_snapshot', 'account_auth_stale_snapshot', true)]
  })
  assert.equal(
    staleAuthorizationBeforeInvalidation.get('account_stale_snapshot')?.allowed,
    true,
    '失效前应能复现 server 使用旧授权快照中的允许决策'
  )
  notifyAuthorizationQuotaCacheInvalidation('gateway_quota_passive_snapshot_regression')
  const invalidatedAuthorizationRuntime = gatewayQuotaSnapshotRuntime()
  assert.equal(invalidatedAuthorizationRuntime.authorizationEntryCount, 0, '授权配额失效后 server 必须清空旧授权快照决策')
  assert.equal(invalidatedAuthorizationRuntime.authorizationEntriesComplete, false, '授权配额失效后 server 必须把授权快照标记为不完整')
  assert.equal(invalidatedAuthorizationRuntime.costEntryCount, 1, '授权配额失效不能清空 API Key 成本快照')
  assert.equal(invalidatedAuthorizationRuntime.costEntriesComplete, true, '授权配额失效不能把 API Key 成本快照误标为不完整')
  const staleAuthorizationAfterInvalidation = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_passive_quota',
      groupAccessType: 'authorized',
      groupAuthorizationId: 'group_auth_stale_snapshot',
      groupAuthorizationQuotaLimited: true
    } as GroupUsageAccessMetadata,
    accounts: [passiveAccount('account_stale_snapshot', 'account_auth_stale_snapshot', true)]
  })
  assert.equal(
    staleAuthorizationAfterInvalidation.get('account_stale_snapshot')?.allowed,
    false,
    '授权配额失效后，带额度限制但缺失新快照的授权必须 fail-closed，不能继续复用旧允许决策'
  )
  const apiKeyDecisionAfterAuthorizationInvalidation = await checkGatewayApiKeyQuotaAsync({
    id: 'key_auth_invalidation_cost_snapshot',
    system_account_id: 'sys_passive_quota',
    route_strategy_id: 'route_auth_invalidation_cost_snapshot',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'group_passive_quota',
    status: 'active',
    availability_schedule_active: 1,
    expires_at: null,
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 10 } })
  } as GatewayApiKeyRow)
  assert.equal(apiKeyDecisionAfterAuthorizationInvalidation.allowed, false, '授权配额失效不应破坏 API Key 成本快照判断')

  await assertDbServiceAuthorizationQuotaInvalidationBridge()

  clearGatewayQuotaSnapshot()
  invalidateGatewayAuthorizationQuotaSnapshot()
  const invalidatedWithoutCostSnapshotDecision = await checkGatewayApiKeyQuotaAsync({
    id: 'key_auth_invalidation_without_cost_snapshot',
    system_account_id: 'sys_passive_quota',
    route_strategy_id: 'route_auth_invalidation_without_cost_snapshot',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'group_passive_quota',
    status: 'active',
    availability_schedule_active: 1,
    expires_at: null,
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 1 } })
  } as GatewayApiKeyRow)
  assert.equal(
    invalidatedWithoutCostSnapshotDecision.allowed,
    true,
    '仅授权快照失效时不能把缺失的 API Key 成本快照误判为截断'
  )

  clearGatewayQuotaSnapshot()
  const noSnapshotDecision = await checkGatewayApiKeyQuotaAsync({
    id: 'key_passive_no_snapshot',
    system_account_id: 'sys_passive_quota',
    route_strategy_id: 'route_passive_no_snapshot',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'group_passive_quota',
    status: 'active',
    availability_schedule_active: 1,
    expires_at: null,
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 1 } })
  } as GatewayApiKeyRow)
  assert.equal(noSnapshotDecision.allowed, true, 'server 角色额度快照缺失时不应主动请求 DB service，应短时放行')

  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [],
    authorizationEntries: [],
    costEntriesComplete: false,
    authorizationEntriesComplete: true
  })
  const incompleteSnapshotApiKeyDecision = await checkGatewayApiKeyQuotaAsync({
    id: 'key_passive_missing_from_incomplete_snapshot',
    system_account_id: 'sys_passive_quota',
    route_strategy_id: 'route_passive_missing_from_incomplete_snapshot',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'group_passive_quota',
    status: 'active',
    availability_schedule_active: 1,
    expires_at: null,
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 1 } })
  } as GatewayApiKeyRow)
  assert.equal(incompleteSnapshotApiKeyDecision.allowed, false, '额度快照已截断时，启用额度的 API Key 缺失快照不能长期放行')

  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [],
    authorizationEntries: [],
    costEntriesComplete: true,
    authorizationEntriesComplete: false
  })
  const incompleteSnapshotAuthorizationDecision = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_passive_quota',
      groupAccessType: 'authorized',
      groupAuthorizationId: 'group_auth_passive_missing_from_incomplete_snapshot',
      groupAuthorizationQuotaLimited: true
    } as GroupUsageAccessMetadata,
    accounts: [passiveAccount('account_passive_missing_from_incomplete_snapshot', 'account_auth_passive_missing_from_incomplete_snapshot', true)]
  })
  assert.equal(
    incompleteSnapshotAuthorizationDecision.get('account_passive_missing_from_incomplete_snapshot')?.allowed,
    false,
    '授权额度快照已截断时，缺失授权快照不能长期放行'
  )
  const unlimitedMissingAuthorizationDecision = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess: {
      groupOwnerSystemAccountId: 'sys_passive_quota',
      groupAccessType: 'authorized',
      groupAuthorizationId: 'group_auth_passive_unlimited_missing_from_incomplete_snapshot'
    } as GroupUsageAccessMetadata,
    accounts: [passiveAccount('account_passive_unlimited_missing_from_incomplete_snapshot', 'account_auth_passive_unlimited_missing_from_incomplete_snapshot')]
  })
  assert.equal(
    unlimitedMissingAuthorizationDecision.get('account_passive_unlimited_missing_from_incomplete_snapshot')?.allowed,
    true,
    '授权额度快照已截断时，未标记额度限制的授权缺失快照仍应按无额度限制放行，避免误伤无额度授权'
  )

  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: Array.from({ length: gatewayQuotaSnapshotCostPageSize + 1 }, (_, index) => ({
      systemAccountId: 'sys_snapshot_cap',
      scopeType: 'api_key',
      scopeId: `key_snapshot_cap_${index}`,
      costs: { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
    })),
    authorizationEntries: Array.from({ length: gatewayQuotaSnapshotAuthorizationPageSize + 1 }, (_, index) => ({
      scopeType: 'account_authorization' as const,
      authorizationId: `authorization_snapshot_cap_${index}`,
      decision: { allowed: true }
    }))
  })
  const cappedRuntime = gatewayQuotaSnapshotRuntime()
  assert.equal(cappedRuntime.costEntryCount, gatewayQuotaSnapshotCostPageSize + 1, 'server 接收额度快照时不应再截断 API Key 成本窗口')
  assert.equal(cappedRuntime.authorizationEntryCount, gatewayQuotaSnapshotAuthorizationPageSize + 1, 'server 接收额度快照时不应再截断授权决策窗口')
  assert.equal(cappedRuntime.costEntriesComplete, true, 'server 接收完整 API Key 成本窗口时应默认标记快照完整')
  assert.equal(cappedRuntime.authorizationEntriesComplete, true, 'server 接收完整授权决策窗口时应默认标记快照完整')

  await assertAuthorizationQuotaBatchFallbackFansOutSharedCacheKey()

  console.log('网关额度被动快照回归通过：server 请求链路不主动查询 DB service，worker 有界构建额度快照，并禁止误调同步 SQLite 配额读取')
} finally {
  clearGatewayQuotaSnapshot()
  await clearAuthorizationQuotaCacheAsync()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await closeRedisClients()
  await closePostgresPool()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGatewayQuotaSnapshotSourcesBounded(): void {
  const repositorySource = readFileSync(new URL('../../storage/gateway-quota-snapshot.repository.ts', import.meta.url), 'utf8')
  const cacheSource = readFileSync(new URL('../../modules/gateway/quota/quota-snapshot-cache.service.ts', import.meta.url), 'utf8')
  const apiKeyRowsBody = sourceFunctionBlock(repositorySource, 'function loadApiKeyQuotaSnapshotRows')
  const authorizationRowsBody = sourceFunctionBlock(repositorySource, 'function loadAuthorizationQuotaSnapshotRows')
  const teamRowsBody = sourceFunctionBlock(repositorySource, 'function loadTeamAuthorizationQuotaSnapshotRows')
  assert(apiKeyRowsBody.includes('LIMIT ?'), 'API Key 额度快照构建不能无上限读取 api_keys')
  assert(apiKeyRowsBody.includes('maxGatewayQuotaSnapshotCostEntries + 1'), 'API Key 额度快照构建必须用哨兵行判断是否截断')
  assert(apiKeyRowsBody.includes('rows.slice(0, maxGatewayQuotaSnapshotCostEntries)'), 'API Key 额度快照发送前必须限制 IPC payload 大小')
  assert(!apiKeyRowsBody.includes('OFFSET ?'), 'API Key 额度快照构建禁止通过 OFFSET 循环读取全表')
  assert(authorizationRowsBody.includes('LIMIT ?'), '授权额度快照构建不能无上限读取 resource_authorizations')
  assert(authorizationRowsBody.includes('maxGatewayQuotaSnapshotAuthorizationEntries + 1'), '授权额度快照构建必须用哨兵行判断是否截断')
  assert(authorizationRowsBody.includes('rows.slice(0, maxGatewayQuotaSnapshotAuthorizationEntries)'), '授权额度快照发送前必须限制 IPC payload 大小')
  assert(!authorizationRowsBody.includes('OFFSET ?'), '授权额度快照构建禁止通过 OFFSET 循环读取全表')
  assert(teamRowsBody.includes('LIMIT ?'), '团队授权额度快照构建不能无上限读取 team grant')
  assert(teamRowsBody.includes('maxGatewayQuotaSnapshotAuthorizationEntries + 1'), '团队授权额度快照构建必须用哨兵行判断是否截断')
  assert(teamRowsBody.includes('rows.slice(0, maxGatewayQuotaSnapshotAuthorizationEntries)'), '团队授权额度快照发送前必须限制 IPC payload 大小')
  assert(!teamRowsBody.includes('OFFSET ?'), '团队授权额度快照构建禁止通过 OFFSET 循环读取全表')
  assert(!cacheSource.includes('.slice(0,'), 'server 接收完整额度快照时不应二次截断导致误 429')
}

function assertAuthorizationQuotaInvalidationSourcesConnected(): void {
  const dbServiceIpcSource = readFileSync(new URL('../../modules/db-service/db-service-ipc.ts', import.meta.url), 'utf8')
  const dbServiceTypesSource = readFileSync(new URL('../../modules/db-service/db-service-types.ts', import.meta.url), 'utf8')
  const authorizationQuotaSource = readFileSync(new URL('../../modules/gateway/quota/authorization-quota.service.ts', import.meta.url), 'utf8')
  const cacheSource = readFileSync(new URL('../../modules/gateway/quota/quota-snapshot-cache.service.ts', import.meta.url), 'utf8')
  assert(dbServiceTypesSource.includes("type: 'authorization_quota_cache_invalidate'"), 'DB service 子进程消息类型必须包含授权配额缓存失效')
  assert(dbServiceIpcSource.includes('registerAuthorizationQuotaCacheInvalidator(notifyServerAuthorizationQuotaCacheInvalidated)'), 'DB service 必须把授权配额失效器注册到跨进程通知链路')
  assert(dbServiceIpcSource.includes("sendDbServiceChildMessage({ type: 'authorization_quota_cache_invalidate' })"), 'DB service 角色必须把授权配额失效转发给 server')
  assert(dbServiceIpcSource.includes('authorizationQuota.clearAuthorizationQuotaCache()'), 'server 收到授权配额失效后必须清空运行时授权配额缓存')
  assert(dbServiceIpcSource.includes('invalidateGatewayAuthorizationQuotaSnapshot()'), 'server 收到授权配额失效后必须同步让授权配额快照失效')
  assert(cacheSource.includes('authorizationSnapshotInvalidated'), '授权配额快照缓存必须有独立失效标记，避免误伤 API Key 成本快照')
  assert(authorizationQuotaSource.includes('gatewayAuthorizationQuotaSnapshotVersion()'), '授权配额快照缓存版本必须参与 server 运行时缓存 key，避免复用旧决策')
}

function assertGatewayQuotaRequestPathUsesAsyncOnly(): void {
  const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
  assert(!/\bcheckGatewayApiKeyQuota\b/.test(preflightSource), '网关请求预检禁止调用同步 API Key 额度读取')
  assert(!/\bcheckGatewayAuthorizationQuota(?:ByIds|BatchByIds)?\b/.test(preflightSource), '网关请求预检禁止调用同步授权额度读取')
}

function assertLocalQuotaReadersRejectServerRole(): void {
  assert.throws(
    () => checkGatewayApiKeyQuota({
      id: 'key_sync_reject',
      system_account_id: 'sys_sync_reject',
      route_strategy_id: 'route_sync_reject',
      route_strategy_mode: 'normal',
      route_strategy_config_json: null,
      selected_group_id: 'group_sync_reject',
      status: 'active',
      availability_schedule_active: 1,
      expires_at: null,
      system_account_image_generation_enabled: 0,
      quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 1 } })
    } as GatewayApiKeyRow),
    /server 角色禁止直接同步读取 SQLite/,
    'server 角色误调用同步 API Key 额度读取时必须在触达 SQLite 前失败'
  )
  assert.throws(
    () => checkGatewayAuthorizationQuotaByIds({
      groupAuthorizationId: 'group_auth_sync_reject',
      accountAuthorizationId: 'account_auth_sync_reject'
    }),
    /server 角色禁止直接同步读取 SQLite/,
    'server 角色误调用同步授权额度读取时必须在触达 SQLite 前失败'
  )
  assert.throws(
    () => checkGatewayAuthorizationQuotaBatchByIds({
      groupAuthorizationId: 'group_auth_sync_reject',
      accounts: [{ accountId: 'account_sync_reject', accountAuthorizationId: 'account_auth_sync_reject' }]
    }),
    /server 角色禁止直接同步读取 SQLite/,
    'server 角色误调用同步批量授权额度读取时必须在触达 SQLite 前失败'
  )
}

function sourceFunctionBlock(source: string, marker: string): string {
  const start = source.indexOf(marker)
  assert(start >= 0, `未找到源码片段：${marker}`)
  const nextFunction = source.indexOf('\nfunction ', start + marker.length)
  return source.slice(start, nextFunction === -1 ? undefined : nextFunction)
}

async function assertDbServiceAuthorizationQuotaInvalidationBridge(): Promise<void> {
  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [{
      systemAccountId: 'sys_passive_quota',
      scopeType: 'api_key',
      scopeId: 'key_auth_invalidation_bridge_cost_snapshot',
      costs: {
        hourly: 20,
        daily: 20,
        weekly: 20,
        monthly: 20,
        total: 20
      }
    }],
    authorizationEntries: [
      {
        scopeType: 'group_authorization',
        authorizationId: 'group_auth_invalidation_bridge',
        decision: { allowed: true }
      },
      {
        scopeType: 'account_authorization',
        authorizationId: 'account_auth_invalidation_bridge',
        decision: { allowed: true }
      }
    ],
    costEntriesComplete: true,
    authorizationEntriesComplete: true
  })
  const fakeChild = new FakeDbServiceChild()
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'server'
    dbServiceIpc.attachDbServiceProcess(fakeChild as never)
    fakeChild.emit('message', {
      type: 'db_service_ready',
      pid: fakeChild.pid,
      httpHost: '127.0.0.1',
      httpPort: 1
    })
    await runWithDbServiceParentMessageBridge(fakeChild, () => {
      runtimeConfig.processRole = 'db-service'
      notifyAuthorizationQuotaCacheInvalidation('gateway_quota_passive_snapshot_bridge_regression')
    })
    await delay(10)
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  const runtime = gatewayQuotaSnapshotRuntime()
  assert.equal(runtime.authorizationEntryCount, 0, 'DB service 授权配额失效消息必须让 server 清空授权快照决策')
  assert.equal(runtime.authorizationEntriesComplete, false, 'DB service 授权配额失效消息必须让 server 标记授权快照不完整')
  assert.equal(runtime.costEntryCount, 1, 'DB service 授权配额失效消息不能清空 server API Key 成本快照')
  assert.equal(runtime.costEntriesComplete, true, 'DB service 授权配额失效消息不能把 server API Key 成本快照误标为不完整')
}

async function assertAuthorizationQuotaBatchFallbackFansOutSharedCacheKey(): Promise<void> {
  clearGatewayQuotaSnapshot()
  clearAuthorizationQuotaCache()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: [],
    authorizationEntries: [],
    costEntriesComplete: true,
    authorizationEntriesComplete: false
  })

  const fakeChild = new FakeDbServiceChild()
  const previousProcessRole = runtimeConfig.processRole
  let fallbackRequestCount = 0
  let fallbackRequestAccountCount = 0
  try {
    runtimeConfig.processRole = 'server'
    fakeChild.onSend = (message: unknown) => {
      const record = message as {
        type?: string
        requestId?: string
        operation?: { type?: string; accounts?: unknown[] }
      }
      if (record.type !== 'db_service_request'
        || record.operation?.type !== 'check_authorization_quota_batch'
        || typeof record.requestId !== 'string') {
        return
      }
      fallbackRequestCount += 1
      fallbackRequestAccountCount = Array.isArray(record.operation.accounts) ? record.operation.accounts.length : 0
      queueMicrotask(() => {
        fakeChild.emit('message', {
          type: 'db_service_response',
          requestId: record.requestId,
          ok: true,
          result: [{ allowed: false, message: '分组授权额度已耗尽' }]
        })
      })
    }
    dbServiceIpc.attachDbServiceProcess(fakeChild as never)
    fakeChild.emit('message', {
      type: 'db_service_ready',
      pid: fakeChild.pid,
      httpHost: '127.0.0.1',
      httpPort: 1
    })

    const decisions = await checkGatewayAuthorizationQuotaBatchAsync({
      groupAccess: {
        groupOwnerSystemAccountId: 'sys_passive_quota',
        groupAccessType: 'authorized',
        groupAuthorizationId: 'group_auth_batch_fallback_fanout',
        groupAuthorizationQuotaLimited: true
      } as GroupUsageAccessMetadata,
      accounts: [
        passiveAccount('account_auth_batch_fallback_fanout_a'),
        passiveAccount('account_auth_batch_fallback_fanout_b')
      ]
    })
    assert.equal(fallbackRequestCount, 1, '授权配额批量 fallback 应按共享 cacheKey 去重请求 DB service')
    assert.equal(fallbackRequestAccountCount, 1, '共享分组授权 cacheKey 的账号只应派生一个 DB service 补判请求')
    assert.equal(decisions.get('account_auth_batch_fallback_fanout_a')?.allowed, false, '共享分组授权补判拒绝时，第一个账号必须被拒绝')
    assert.equal(decisions.get('account_auth_batch_fallback_fanout_b')?.allowed, false, '共享分组授权补判拒绝时，同 cacheKey 的其他账号也必须被拒绝')
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

async function runWithDbServiceParentMessageBridge<T>(fakeChild: FakeDbServiceChild, operation: () => Promise<T> | T): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  const previousSend = process.send
  try {
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = (message: unknown) => {
      queueMicrotask(() => {
        const parentProcessRole = runtimeConfig.processRole
        runtimeConfig.processRole = 'server'
        try {
          fakeChild.emit('message', message)
        } finally {
          runtimeConfig.processRole = parentProcessRole
        }
      })
      return true
    }
    return await operation()
  } finally {
    runtimeConfig.processRole = previousProcessRole
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = previousSend
  }
}

function passiveAccount(id: string, accountAuthorizationId?: string, accountAuthorizationQuotaLimited?: boolean): OpenAIAccountSecret {
  return {
    id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_passive_quota',
    accountOwnerSystemAccountId: 'sys_passive_quota',
    groupOwnerSystemAccountId: 'sys_passive_quota',
    accountAccessType: 'account_authorized',
    groupAccessType: 'authorized',
    name: id,
    type: 'api_key',
    status: 'active',
    accountAuthorizationId,
    accountAuthorizationQuotaLimited,
    supportedModels: [],
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-passive-quota',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-passive-quota',
      base_url: 'https://api.openai.com/v1'
    }
  } as OpenAIAccountSecret
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
