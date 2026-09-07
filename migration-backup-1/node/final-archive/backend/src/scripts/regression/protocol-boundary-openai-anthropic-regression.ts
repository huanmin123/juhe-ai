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
import {
  buildOpenAIToAnthropicBridgeBody,
  transformOpenAIToAnthropicBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'
import { GatewayRequestValidationError } from '../../modules/gateway/request/validation-error.js'
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
    healthCheckModel: 'claude-haiku-4-5',
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

  const adaptiveThinkingBody = JSON.parse((await buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/responses', {
    model: 'claude-fable-5-1',
    input: '深入分析',
    reasoning: { effort: 'max' }
  }), {})).toString('utf8')) as Record<string, unknown>
  assert.deepEqual(adaptiveThinkingBody.thinking, { type: 'adaptive' }, 'Anthropic 新模型思考级别应转换为 adaptive thinking')
  assert.deepEqual(adaptiveThinkingBody.output_config, { effort: 'max' }, 'Anthropic max 思考级别应写入官方 output_config.effort')

  const chatBridgeBody = JSON.parse((await buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/chat/completions', {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: '需要检索时继续分析' }],
    tools: [
      { type: 'web_search' },
      {
        type: 'function',
        function: {
          name: 'local_lookup',
          description: '本地查询',
          parameters: { type: 'object', properties: { query: { type: 'string' } } }
        }
      }
    ],
    tool_choice: 'auto'
  }), {})).toString('utf8')) as Record<string, unknown>
  assert.match(
    String(chatBridgeBody.system ?? ''),
    /OpenAI hosted tools unavailable in this Anthropic bridge: web_search/,
    'Chat 非强制托管工具应转成上游 system 内部约束'
  )
  assert.doesNotMatch(String(chatBridgeBody.system ?? ''), /能力未执行|建议下一步/, '内部约束不应沿用用户可见 guidance 文案')
  assert.deepEqual(
    (chatBridgeBody.tools as Array<Record<string, unknown>>).map((tool) => tool.name),
    ['local_lookup'],
    'Chat 非强制托管工具应被剥离，普通 function 工具应继续桥接'
  )

  const responsesBridgeBody = JSON.parse((await buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/responses', {
    model: 'gpt-5.5',
    input: '需要检索时继续回答',
    tools: [
      { type: 'web_search_preview' },
      {
        type: 'function',
        name: 'local_lookup',
        description: '本地查询',
        parameters: { type: 'object', properties: { query: { type: 'string' } } }
      }
    ],
    tool_choice: 'auto'
  }), {})).toString('utf8')) as Record<string, unknown>
  assert.match(
    String(responsesBridgeBody.system ?? ''),
    /OpenAI hosted tools unavailable in this Anthropic bridge: web_search_preview/,
    'Responses 非强制托管工具应转成上游 system 内部约束'
  )
  assert.doesNotMatch(String(responsesBridgeBody.system ?? ''), /能力未执行|建议下一步/, 'Responses 内部约束不应成为用户可见 guidance 文案')
  assert.deepEqual(
    (responsesBridgeBody.tools as Array<Record<string, unknown>>).map((tool) => tool.name),
    ['local_lookup'],
    'Responses 非强制托管工具应被剥离，普通 function 工具应继续桥接'
  )

  await assert.rejects(
    () => buildOpenAIToAnthropicBridgeBody(gatewayPostRequest('/v1/responses', {
      model: 'gpt-5.5',
      input: '必须搜索',
      tools: [{ type: 'web_search_preview' }],
      tool_choice: { type: 'web_search_preview' }
    }), {}),
    (error: unknown) => error instanceof GatewayRequestValidationError
      && error.code === 'openai_anthropic_bridge_unsupported_hosted_tool_choice',
    '强制选择不可桥接托管工具时应返回请求级错误，而不是 200 guidance'
  )

  const originalAnthropicErrorText = JSON.stringify({
    type: 'error',
    error: {
      type: 'overloaded_error',
      message: 'raw anthropic error',
      vendor_detail: 'policy-keyword'
    }
  })
  let upstreamErrorBodyReads = 0
  const upstreamErrorResponse = {
    status: 529,
    ok: false,
    headers: new Headers({ 'content-type': 'application/json; charset=utf-8' }),
    body: (async function * () {
      upstreamErrorBodyReads += 1
      yield Buffer.from(originalAnthropicErrorText, 'utf8')
    })()
  } satisfies GatewayUpstreamResponse
  const preservedErrorResponse = transformOpenAIToAnthropicBridgeUpstreamResponse(
    gatewayPostRequest('/v1/chat/completions', { model: 'gpt-5.5', messages: [] }),
    upstreamErrorResponse
  )
  assert.strictEqual(preservedErrorResponse, upstreamErrorResponse, '非 2xx 必须保留真实 Anthropic 响应给失败调度')
  assert.equal(upstreamErrorBodyReads, 0, '跨协议桥接不得预读或解析 generic 非 2xx 正文')
  const preservedChunks: Buffer[] = []
  for await (const chunk of preservedErrorResponse.body ?? []) preservedChunks.push(Buffer.from(chunk))
  assert.equal(Buffer.concat(preservedChunks).toString('utf8'), originalAnthropicErrorText, '原始 Anthropic 错误字段必须完整保留给策略匹配')

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
