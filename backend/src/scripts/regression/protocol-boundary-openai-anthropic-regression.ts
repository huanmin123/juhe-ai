import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { Request } from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount
} from '../../modules/providers/drivers/registry.js'
import { buildOpenAIToAnthropicBridgeBody } from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-openai-anthropic-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'openai-anthropic-boundary-secret'
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
    name: 'OpenAI 到 Anthropic 旧桥接边界分组',
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'OpenAI 到 Anthropic 旧桥接边界账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-boundary-upstream',
      base_url: 'https://anthropic.example.test/v1',
      supported_endpoint_modes: ['messages_json', 'messages_sse']
    },
    supportedModels: ['claude-haiku-4-5'],
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert.equal(account.modelMappings?.length ?? 0, 0, '普通 Anthropic 账户不应保存 OpenAI -> Anthropic 跨协议映射')

  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'OpenAI 到 Anthropic 边界 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '边界回归 API Key 未返回明文密钥')

  assert.throws(() => {
    createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'OpenAI 到 Anthropic 旧显式规则 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      explicitHybridRouteRules: [{
        id: 'legacy_openai_to_anthropic',
        enabled: true,
        priority: 1,
        sourceEndpointFamily: 'chat_completions',
        sourceModel: 'gpt-5.5',
        targetGroupId: group.id,
        upstreamEndpointFamily: 'messages',
        upstreamModel: 'claude-haiku-4-5'
      }],
      status: 'active'
    }, access)
  }, /explicitHybridRouteRules/, 'API Key 不应再接收旧显式混合路由规则')

  const dispatchAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, {
    ignoreAvailability: true
  })
  assert(dispatchAccount, 'Anthropic 边界账号应可从分组进入运行时窗口')
  assert.equal(providerDriverForAccount(dispatchAccount)?.id, 'anthropic')

  const openAIChatRequest = gatewayPostRequest('/v1/chat/completions', {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'ping' }],
    stream: false
  })
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(dispatchAccount, openAIChatRequest),
    [],
    '普通 Anthropic 账号不应为 OpenAI Chat 请求构造桥接上游 URL'
  )
  assert.equal(
    accountSupportsGatewayRequest(openAIChatRequest, dispatchAccount),
    false,
    '普通 Anthropic 账号不应承接 OpenAI Chat 请求；跨协议应由混合供应商账户处理'
  )

  await assert.rejects(
    () => buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/chat/completions', {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'ping' }],
      functions: [{ name: 'legacy_tool' }]
    }), {}),
    /legacy functions\/function_call 已移除/,
    'OpenAI -> Anthropic bridge 不应继续接收 Chat legacy functions'
  )
  await assert.rejects(
    () => buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/chat/completions', {
      model: 'gpt-5.5',
      messages: [{ role: 'user', content: 'ping' }],
      function_call: 'auto'
    }), {}),
    /legacy functions\/function_call 已移除/,
    'OpenAI -> Anthropic bridge 不应继续接收 Chat legacy function_call'
  )
  await assert.rejects(
    () => buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/chat/completions', {
      model: 'gpt-5.5',
      messages: [
        { role: 'assistant', function_call: { name: 'legacy_tool', arguments: '{}' } },
        { role: 'function', name: 'legacy_tool', content: '{}' }
      ]
    }), {}),
    /legacy role=function \/ assistant\.function_call 已移除/,
    'OpenAI -> Anthropic bridge 不应继续接收 Chat legacy function history'
  )

  console.log('openai anthropic protocol boundary regression passed')
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
