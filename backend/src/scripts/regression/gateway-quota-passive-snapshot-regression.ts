import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { checkGatewayApiKeyQuotaAsync } from '../../modules/gateway/api-key-quota.service.js'
import { checkGatewayAuthorizationQuotaBatchAsync } from '../../modules/gateway/authorization-quota.service.js'
import {
  clearGatewayQuotaSnapshot,
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

  console.log('网关额度被动快照回归通过：server 请求链路不主动查询 DB service，直接读取 worker 推送的额度快照')
} finally {
  clearGatewayQuotaSnapshot()
  rmSync(tempRoot, { recursive: true, force: true })
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
