import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount
} from '../../modules/providers/drivers/registry.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-anthropic-openai-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'anthropic-openai-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: 'Anthropic 到 OpenAI 旧桥接边界分组',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    enabled: true
  }, access)

  assert.throws(() => {
    repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '显式客户端画像边界账户',
      type: 'api_key',
      clientCompatibility: 'openai_standard',
      credentials: {
        api_key: 'sk-openai-compatible-boundary-client-profile',
        base_url: 'https://openai-compatible.example.test/v1',
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      supportedModels: ['gpt-5.5'],
      groupId: group.id,
      status: 'active',
      schedulable: true
    }, access)
  }, /clientCompatibility/, '账户创建不应再接收显式客户端画像，客户端画像应由内部识别')

  const account = repositories.createAccount({
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: 'Anthropic 到 OpenAI 旧桥接边界账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-openai-compatible-boundary-upstream',
      base_url: 'https://openai-compatible.example.test/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse']
    },
    supportedModels: ['gpt-5.5'],
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert.equal(account.clientCompatibility, 'openai_standard', 'OpenAI 兼容账号客户端画像应由后端自动派生')
  assert.equal(account.modelMappings?.length ?? 0, 0, '普通 OpenAI 兼容账户不应保存 Anthropic -> OpenAI 跨协议映射')

  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Anthropic 到 OpenAI 边界 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '边界回归 API Key 未返回明文密钥')

  assert.throws(() => {
    createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'Anthropic 到 OpenAI 旧显式规则 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      explicitHybridRouteRules: [{
        id: 'legacy_anthropic_to_openai',
        enabled: true,
        priority: 1,
        sourceEndpointFamily: 'messages',
        sourceModel: 'claude-haiku-4-5',
        targetGroupId: group.id,
        upstreamEndpointFamily: 'chat_completions',
        upstreamModel: 'gpt-5.5'
      }],
      status: 'active'
    }, access)
  }, /explicitHybridRouteRules/, 'API Key 不应再接收旧显式混合路由规则')

  const dispatchAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, {
    ignoreAvailability: true
  })
  assert(dispatchAccount, 'OpenAI 兼容边界账号应可从分组进入运行时窗口')
  assert.equal(providerDriverForAccount(dispatchAccount)?.id, 'openai-compatible')

  const anthropicMessagesRequest = gatewayPostRequest('/v1/messages', {
    model: 'claude-haiku-4-5',
    max_tokens: 16,
    messages: [{ role: 'user', content: 'ping' }],
    stream: false
  })
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(dispatchAccount, anthropicMessagesRequest),
    [],
    '普通 OpenAI 兼容账号不应为 Anthropic Messages 请求构造桥接上游 URL'
  )
  assert.equal(
    accountSupportsGatewayRequest(anthropicMessagesRequest, dispatchAccount),
    false,
    '普通 OpenAI 兼容账号不应承接 Anthropic Messages 请求；跨协议应由混合供应商账户处理'
  )

  console.log('anthropic openai protocol boundary regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function gatewayPostRequest(path: string, body: Record<string, unknown>): Request {
  return {
    method: 'POST',
    path,
    originalUrl: path,
    headers: {
      accept: body.stream === true ? 'text/event-stream' : 'application/json',
      'content-type': 'application/json'
    },
    body
  } as unknown as Request
}
