import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

import { buildOpenAIClientCompatibilityBody } from '../../modules/gateway/protocols/openai-v1/api-key-client-compatibility.js'
import { buildOpenAIModelMappedJsonBody } from '../../modules/gateway/protocols/openai-v1/model-mapping.js'
import { replaceGatewayJsonBody } from '../../modules/gateway/request/body.js'
import { createMemoryGatewayRequest } from '../../modules/gateway/testing/memory-gateway-http.js'
import {
  parseGatewayRequestJsonBody,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'

const requestJsonMaterializationModules = [
  'modules/gateway/codex-responses/chat-bridge-state.ts',
  'modules/gateway/codex-responses/compact-preflight.ts',
  'modules/gateway/hybrid/scoring.service.ts',
  'modules/gateway/hybrid/routing.service.ts',
  'modules/gateway/hybrid/quality-inspection.service.ts',
  'modules/gateway/hybrid/quality-repair.service.ts',
  'modules/gateway/protocols/openai-v1/model-mapping.ts',
  'modules/gateway/protocols/openai-v1/api-key-client-compatibility.ts'
] as const

try {
  const padding = 'request-cache'.repeat(32 * 1024)
  const rawBody = Buffer.from(JSON.stringify({
    model: 'gpt-5.6-sol',
    input: padding,
    tools: [{ type: 'web_search_preview' }],
    tool_choice: {
      type: 'allowed_tools',
      tools: [{ type: 'web_search_preview_2025_03_11' }]
    }
  }), 'utf8')
  const request = {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers: { 'content-type': 'application/json' },
    rawBody,
    body: undefined,
    gatewayRequestBody: {
      rawBodyBytes: rawBody.length,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'deferred_large_json',
      jsonParseWarningBytes: 0,
      model: 'gpt-5.6-sol'
    }
  } as unknown as Request & {
    gatewayParsedJsonBodyAvailable?: boolean
    gatewayParsedJsonBody?: unknown
    gatewayParsedJsonBodyPromise?: unknown
  }

  const [compatibilityBody, mappedBody] = await Promise.all([
    buildOpenAIClientCompatibilityBody(request, undefined, {
      requestClientCompatibility: 'codex_responses'
    }),
    buildOpenAIModelMappedJsonBody(request, 'gpt-5.5')
  ])
  assert(compatibilityBody, 'Codex API Key compatibility 应物化 deferred JSON 请求体')
  const sharedParsedBody = request.gatewayParsedJsonBody
  assert(sharedParsedBody && typeof sharedParsedBody === 'object', '首次协议转换后应保留请求级解析结果')
  assert.equal(request.gatewayParsedJsonBodyAvailable, true, '成功物化后必须标记请求级 JSON 缓存可用')
  assert.equal(request.body, sharedParsedBody, '并发协议转换必须共享同一请求解析对象')
  const sharedParsedRecord = sharedParsedBody as Record<string, unknown>
  assert.equal(
    (sharedParsedRecord.tools as Array<{ type?: unknown }>)[0]?.type,
    'web_search_preview',
    '兼容转换不得原地改写请求级缓存的嵌套 tools'
  )
  assert.equal(
    ((sharedParsedRecord.tool_choice as { tools?: Array<{ type?: unknown }> }).tools ?? [])[0]?.type,
    'web_search_preview_2025_03_11',
    '兼容转换不得原地改写请求级缓存的嵌套 tool_choice'
  )
  assert.equal(request.gatewayParsedJsonBodyPromise, undefined, '请求级 JSON 物化完成后必须释放 in-flight Promise')
  assert.equal(
    (JSON.parse(mappedBody.toString('utf8')) as { model?: unknown }).model,
    'gpt-5.5',
    '复用请求级解析结果后仍应完成模型改写'
  )

  const staleRawBody = Buffer.from('{"model":', 'utf8')
  const staleRequest = {
    headers: { 'content-type': 'application/json' },
    rawBody: staleRawBody,
    body: undefined,
    gatewayRequestBody: {
      rawBodyBytes: staleRawBody.length,
      contentType: 'application/json',
      isJson: true,
      jsonParseStatus: 'deferred_large_json',
      jsonParseWarningBytes: 0
    }
  } as unknown as Request
  const staleMaterialization = parseGatewayRequestJsonBody(staleRequest)
  replaceGatewayJsonBody(staleRequest, { model: 'current-model' })
  const materializedAfterReplacement = await staleMaterialization
  assert.equal(materializedAfterReplacement, staleRequest.body, '解析期间 Body 改写后不得向调用者返回旧版本对象')
  assert.deepEqual(materializedAfterReplacement, { model: 'current-model' })

  const memoryRequest = createMemoryGatewayRequest({
    method: 'POST',
    path: '/v1/responses',
    body: { model: 'before-rewrite' }
  })
  replaceGatewayJsonBody(memoryRequest, { model: 'after-rewrite' })
  assert.deepEqual(memoryRequest.body, { model: 'after-rewrite' }, '内存网关请求必须支持与 Express 请求相同的 Body 重写')
  assert.deepEqual(JSON.parse(String((memoryRequest as Request & { rawBody: Buffer }).rawBody)), { model: 'after-rewrite' })

  const serializedSyntheticRequestModules = [
    'modules/gateway/codex-responses/compact-preflight.ts',
    'modules/gateway/hybrid/scoring.service.ts',
    'modules/gateway/hybrid/quality-inspection.service.ts'
  ] as const
  for (const relativePath of serializedSyntheticRequestModules) {
    const source = readFileSync(new URL(`../../${relativePath}`, import.meta.url), 'utf8')
    assert.match(source, /serializeGatewayJsonObject\(body\)/, `${relativePath} 必须绑定 synthetic JSON Body 与 canonical Buffer`)
    assert.doesNotMatch(
      source,
      /Buffer\.from\(JSON\.stringify\(body\)/,
      `${relativePath} 不得创建未绑定的 synthetic JSON Buffer`
    )
  }

  for (const relativePath of requestJsonMaterializationModules) {
    const source = readFileSync(new URL(`../../${relativePath}`, import.meta.url), 'utf8')
    assert.match(source, /parseGatewayRequestJsonBody/, `${relativePath} 必须复用请求级 JSON 物化入口`)
    assert.doesNotMatch(source, /parseGatewayJsonBodyInWorker/, `${relativePath} 不得绕过请求级 JSON 物化入口`)
    assert.doesNotMatch(source, /JSON\.parse\(rawBody/, `${relativePath} 不得在模块内重复解析原始请求体`)
  }

  const compactPreflightSource = readFileSync(
    new URL('../../modules/gateway/codex-responses/compact-preflight.ts', import.meta.url),
    'utf8'
  )
  const accountCapabilityFilterSource = readFileSync(
    new URL('../../modules/gateway/dispatch/account-capability-filter.ts', import.meta.url),
    'utf8'
  )
  assert.match(
    compactPreflightSource,
    /synthetic\.gatewayParsedJsonBodyPromise = undefined/,
    '合成 compact 请求必须清除继承的解析 Promise'
  )
  assert.match(
    accountCapabilityFilterSource,
    /output\.gatewayParsedJsonBodyPromise = undefined/,
    '模型覆盖合成请求必须清除继承的解析 Promise'
  )
} finally {
  await stopGatewayJsonParseWorker()
}

console.log('网关请求 Body 物化边界回归通过：跨模块共享解析结果，目标模块不再绕过统一入口')
