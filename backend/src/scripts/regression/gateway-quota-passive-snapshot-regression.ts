import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { checkGatewayApiKeyQuota, checkGatewayApiKeyQuotaAsync } from '../../modules/gateway/api-key-quota.service.js'
import { checkGatewayAuthorizationQuotaBatchAsync } from '../../modules/gateway/authorization-quota.service.js'
import {
  checkGatewayAuthorizationQuotaBatchByIds,
  checkGatewayAuthorizationQuotaByIds
} from '../../modules/gateway/authorization-quota.service.js'
import {
  clearGatewayQuotaSnapshot,
  gatewayQuotaSnapshotRuntime,
  maxGatewayQuotaSnapshotAuthorizationEntries,
  maxGatewayQuotaSnapshotCostEntries,
  replaceGatewayQuotaSnapshot
} from '../../modules/gateway/gateway-quota-snapshot-cache.service.js'
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

try {
  assertGatewayQuotaSnapshotSourcesBounded()
  assertGatewayQuotaRequestPathUsesAsyncOnly()
  assertLocalQuotaReadersRejectServerRole()
  clearGatewayQuotaSnapshot()
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
    selected_group_id: 'group_passive_quota',
    status: 'active',
    expires_at: null,
    group_route_strategy: 'priority_failover',
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
  const noSnapshotDecision = await checkGatewayApiKeyQuotaAsync({
    id: 'key_passive_no_snapshot',
    system_account_id: 'sys_passive_quota',
    selected_group_id: 'group_passive_quota',
    status: 'active',
    expires_at: null,
    group_route_strategy: 'priority_failover',
    system_account_image_generation_enabled: 0,
    quota_limits_json: JSON.stringify({ daily: { enabled: true, limit: 1 } })
  } as GatewayApiKeyRow)
  assert.equal(noSnapshotDecision.allowed, true, 'server 角色额度快照缺失时不应主动请求 DB service，应短时放行')

  clearGatewayQuotaSnapshot()
  replaceGatewayQuotaSnapshot({
    generatedAt: new Date().toISOString(),
    costEntries: Array.from({ length: maxGatewayQuotaSnapshotCostEntries + 1 }, (_, index) => ({
      systemAccountId: 'sys_snapshot_cap',
      scopeType: 'api_key',
      scopeId: `key_snapshot_cap_${index}`,
      costs: { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
    })),
    authorizationEntries: Array.from({ length: maxGatewayQuotaSnapshotAuthorizationEntries + 1 }, (_, index) => ({
      scopeType: 'account_authorization' as const,
      authorizationId: `authorization_snapshot_cap_${index}`,
      decision: { allowed: true }
    }))
  })
  const cappedRuntime = gatewayQuotaSnapshotRuntime()
  assert.equal(cappedRuntime.costEntryCount, maxGatewayQuotaSnapshotCostEntries, 'server 接收额度快照时必须截断 API Key 成本窗口')
  assert.equal(cappedRuntime.authorizationEntryCount, maxGatewayQuotaSnapshotAuthorizationEntries, 'server 接收额度快照时必须截断授权决策窗口')

  console.log('网关额度被动快照回归通过：server 请求链路不主动查询 DB service，直接读取 worker 推送的有界额度快照，并禁止误调同步 SQLite 配额读取')
} finally {
  clearGatewayQuotaSnapshot()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGatewayQuotaSnapshotSourcesBounded(): void {
  const repositorySource = readFileSync(new URL('../../storage/gateway-quota-snapshot.repository.ts', import.meta.url), 'utf8')
  const cacheSource = readFileSync(new URL('../../modules/gateway/gateway-quota-snapshot-cache.service.ts', import.meta.url), 'utf8')
  const apiKeyRowsBody = sourceFunctionBlock(repositorySource, 'function loadApiKeyQuotaSnapshotRows')
  const authorizationRowsBody = sourceFunctionBlock(repositorySource, 'function loadAuthorizationQuotaSnapshotRows')
  const teamRowsBody = sourceFunctionBlock(repositorySource, 'function loadTeamAuthorizationQuotaSnapshotRows')
  assert(apiKeyRowsBody.includes('LIMIT ?'), 'API Key 额度快照构建不能无上限读取 api_keys')
  assert(authorizationRowsBody.includes('LIMIT ?'), '授权额度快照构建不能无上限读取 resource_authorizations')
  assert(teamRowsBody.includes('LIMIT ?'), '团队授权额度快照构建不能无上限读取 team grant')
  assert(cacheSource.includes('slice(0, maxGatewayQuotaSnapshotCostEntries)'), 'server 接收 API Key 额度快照必须二次截断')
  assert(cacheSource.includes('slice(0, maxGatewayQuotaSnapshotAuthorizationEntries)'), 'server 接收授权额度快照必须二次截断')
}

function assertGatewayQuotaRequestPathUsesAsyncOnly(): void {
  const preflightSource = readFileSync(new URL('../../modules/gateway/openai-gateway-request-preflight.ts', import.meta.url), 'utf8')
  assert(!/\bcheckGatewayApiKeyQuota\b/.test(preflightSource), '网关请求预检禁止调用同步 API Key 额度读取')
  assert(!/\bcheckGatewayAuthorizationQuota(?:ByIds|BatchByIds)?\b/.test(preflightSource), '网关请求预检禁止调用同步授权额度读取')
}

function assertLocalQuotaReadersRejectServerRole(): void {
  assert.throws(
    () => checkGatewayApiKeyQuota({
      id: 'key_sync_reject',
      system_account_id: 'sys_sync_reject',
      selected_group_id: 'group_sync_reject',
      status: 'active',
      expires_at: null,
      group_route_strategy: 'priority_failover',
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

function passiveAccount(id: string, accountAuthorizationId?: string): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'sys_passive_quota',
    accountOwnerSystemAccountId: 'sys_passive_quota',
    groupOwnerSystemAccountId: 'sys_passive_quota',
    accountAccessType: 'account_authorized',
    groupAccessType: 'authorized',
    name: id,
    type: 'api_key',
    status: 'active',
    accountAuthorizationId,
    supportedModels: [],
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-passive-quota',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-passive-quota',
      base_url: 'https://api.openai.com/v1'
    }
  } as OpenAIAccountSecret
}
