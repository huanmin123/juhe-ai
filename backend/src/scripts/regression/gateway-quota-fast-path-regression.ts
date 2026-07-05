import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import { checkGatewayApiKeyQuotaAsync } from '../../modules/gateway/quota/api-key-quota.service.js'
import {
  checkGatewayAuthorizationQuotaAsync,
  checkGatewayAuthorizationQuotaBatchAsync
} from '../../modules/gateway/quota/authorization-quota.service.js'
import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-quota-fast-path-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-quota-fast-path-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

try {
  const apiKeyDecision = await checkGatewayApiKeyQuotaAsync(apiKeyWithoutQuota())
  assert.equal(apiKeyDecision.allowed, true, '未启用 API Key 额度时应本地快速放行')

  const groupAccess = groupAccessWithoutAuthorizationQuota()
  const authorizationDecision = await checkGatewayAuthorizationQuotaAsync({ groupAccess })
  assert.equal(authorizationDecision.allowed, true, '没有分组授权和账户授权 ID 时应本地快速放行')

  const batchDecision = await checkGatewayAuthorizationQuotaBatchAsync({
    groupAccess,
    accounts: [
      upstreamAccountWithoutAuthorizationQuota('acc_fast_path_1'),
      upstreamAccountWithoutAuthorizationQuota('acc_fast_path_2')
    ]
  })
  assert.equal(batchDecision.size, 2, '批量快速放行仍应返回每个候选账户的决策')
  assert.equal(batchDecision.get('acc_fast_path_1')?.allowed, true)
  assert.equal(batchDecision.get('acc_fast_path_2')?.allowed, true)

  console.log('网关额度快速路径回归通过：无额度配置时不依赖 DB service IPC')
} finally {
  closeStorageDatabases()
  await closeRedisClients()
  await closePostgresPool()
  rmSync(tempRoot, { recursive: true, force: true })
}

function apiKeyWithoutQuota(): GatewayApiKeyRow {
  return {
    id: 'key_fast_path',
    system_account_id: 'sys_fast_path',
    route_strategy_id: 'route_fast_path',
    route_strategy_mode: 'normal',
    route_strategy_config_json: null,
    selected_group_id: 'grp_fast_path',
    status: 'active',
    expires_at: null,
    quota_limits_json: null,
    system_account_image_generation_enabled: 0
  } as GatewayApiKeyRow
}

function groupAccessWithoutAuthorizationQuota(): GroupUsageAccessMetadata {
  return {
    groupOwnerSystemAccountId: 'sys_fast_path',
    providerCode: 'gpt',
    groupAccessType: 'owner'
  }
}

function upstreamAccountWithoutAuthorizationQuota(id: string): OpenAIAccountSecret {
  return {
    id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_fast_path',
    accountOwnerSystemAccountId: 'sys_fast_path',
    groupOwnerSystemAccountId: 'sys_fast_path',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: 'api_key',
    status: 'active',
    supportedModels: [],
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-fast-path',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-fast-path',
      base_url: 'https://api.openai.com/v1'
    }
  }
}
