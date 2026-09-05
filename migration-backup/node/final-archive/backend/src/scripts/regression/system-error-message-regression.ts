import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  localizeSystemErrorMessage,
  localizeSystemErrorPayload,
  systemErrorMessageForStatus
} from '../../shared/system-error-message.js'
import { OAuthUpstreamResponseError, isOAuthUpstreamResponseError } from '../../shared/oauth-upstream-response-error.js'
import { localizedGatewayErrorPayload } from '../../modules/gateway/response/responses.js'
import { buildDiagnosticUpstreamError } from '../../modules/gateway/upstream/error-helpers.js'
import { ANTHROPIC_ANTHROPIC_V1_PROFILE_ID } from '../../domain/provider-protocol.js'

assert.equal(localizeSystemErrorMessage('Invalid request body', 400), '请求参数无效', '英文系统错误必须映射为中文')
assert.equal(localizeSystemErrorMessage('API Key 格式不正确', 400), 'API Key 格式不正确', '已有中文系统错误必须保留具体说明')
assert.equal(systemErrorMessageForStatus(503), '服务暂时不可用，请稍后重试', '服务不可用必须有稳定中文文案')
assert.deepEqual(
  localizeSystemErrorPayload({ message: 'Database connection refused' }, 500),
  { message: '请求处理失败，请稍后重试' },
  '管理 API 直出的英文异常必须在响应出口中文化'
)
assert.deepEqual(
  localizeSystemErrorPayload({ error: { message: 'Missing required field', type: 'invalid_request_error' } }, 400),
  { error: { message: '请求参数无效', type: 'invalid_request_error' } },
  '标准网关错误包络中的系统英文必须中文化'
)
assert.deepEqual(
  localizeSystemErrorPayload({ message: 'upstream invalid_api_key' }, 401, true),
  { message: 'upstream invalid_api_key' },
  '显式上游错误标记必须保留原文'
)
assert.equal(isOAuthUpstreamResponseError(new OAuthUpstreamResponseError('upstream invalid_api_key', 401)), true, '只有显式的 OAuth 上游响应错误才能保留原文')
assert.equal(isOAuthUpstreamResponseError(new Error('fetch failed')), false, '未知本地或运行时错误不得伪装为上游错误')
assert.equal(
  localizedGatewayErrorPayload({ error: { message: 'File not found', type: 'invalid_request_error' } }, 404).error.message,
  '请求的资源不存在',
  '网关 JSON/SSE 共享错误包络必须中文化本地英文文案'
)

const diagnostic = buildDiagnosticUpstreamError({
  accountId: 'system-error-message-anthropic-account',
  accountName: 'system-error-message-anthropic-account',
  providerCode: 'anthropic',
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  upstreamUrl: 'https://api.anthropic.com/v1/messages',
  status: 429,
  responseHeaders: { 'content-type': 'application/json' },
  responseBodyText: JSON.stringify({
    error: {
      message: 'upstream quota exhausted token=upstream-regression-token',
      type: 'rate_limit_error'
    }
  })
} as Parameters<typeof buildDiagnosticUpstreamError>[0], '上游请求失败')
assert.equal(diagnostic?.payload.error.message, 'upstream quota exhausted token=upstream-regression-token', '上游诊断错误不得脱敏或翻译')
assert.equal(diagnostic?.preserveUpstreamMessage, true, '解析到上游错误载荷时才允许保留原文')
const runtimeDiagnostic = buildDiagnosticUpstreamError({
  accountId: 'system-error-message-runtime-account',
  accountName: 'system-error-message-runtime-account',
  transportFailureKind: 'connection',
  message: 'fetch failed'
} as Parameters<typeof buildDiagnosticUpstreamError>[0], '上游请求失败')
assert.equal(runtimeDiagnostic?.preserveUpstreamMessage, false, '本地运行时诊断不得伪装为上游原文')
assert.deepEqual(
  localizeSystemErrorPayload(runtimeDiagnostic?.payload, runtimeDiagnostic?.statusCode ?? 502, runtimeDiagnostic?.preserveUpstreamMessage),
  { error: { message: '服务处理上游响应失败，请稍后重试', type: 'upstream_transport_error', code: 'upstream_connection' } },
  '本地运行时诊断必须通过系统中文化出口'
)

const serverSource = readFileSync(resolve('src/server.ts'), 'utf8')
const systemApiSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
const gatewayResponseSource = readFileSync(resolve('src/modules/gateway/response/responses.ts'), 'utf8')
const gatewayRoutesSource = readFileSync(resolve('src/modules/gateway/routes.ts'), 'utf8')
const filesSource = readFileSync(resolve('src/modules/openai-compatible-files/files.routes.ts'), 'utf8')
const vectorStoresSource = readFileSync(resolve('src/modules/openai-compatible-vector-stores/vector-stores.routes.ts'), 'utf8')
const vectorIndexerSource = readFileSync(resolve('src/modules/openai-compatible-vector-stores/text-indexer.ts'), 'utf8')
const fileSearchSource = readFileSync(resolve('src/modules/openai-compatible-vector-stores/file-search-executor.ts'), 'utf8')
assert.match(serverSource, /app\.use\(systemErrorMessageLocalizationMiddleware\)/, '主网关必须安装系统错误中文化中间件')
assert.match(systemApiSource, /app\.use\(systemErrorMessageLocalizationMiddleware\)/, '系统 API 必须安装系统错误中文化中间件')
assert.match(gatewayResponseSource, /localizedGatewayErrorPayload\(payload, statusCode\)/, '网关流式错误必须在 JSON 之外单独中文化')
assert.match(gatewayRoutesSource, /preserveUpstreamErrorMessage:\s*diagnosticError\?\.preserveUpstreamMessage \?\? false/, '只有含上游错误载荷的诊断才可标记为原文透传')
for (const routePath of [
  'src/modules/openai-oauth/openai-oauth.routes.ts',
  'src/modules/anthropic-oauth/anthropic-oauth.routes.ts',
  'src/modules/gemini-oauth/gemini-oauth.routes.ts',
  'src/modules/grok-oauth/grok-oauth.routes.ts'
]) {
  const routeSource = readFileSync(resolve(routePath), 'utf8')
  assert.match(routeSource, /if \(isOAuthUpstreamResponseError\(error\)\) \{[\s\S]*markResponseErrorMessageAsUpstream/, `${routePath} 只能为已标记的上游响应保留原文`)
  assert.doesNotMatch(routeSource, /if \(error instanceof Error\) \{[\s\S]{0,120}markResponseErrorMessageAsUpstream/, `${routePath} 不得把任意本地 Error 当作上游错误`)
}
assert.doesNotMatch(filesSource, /new OpenAICompatibleFilesRequestError\(\s*['"](?![^'"]*[\u3400-\u9fff])[^'"]+['"]/, '文件接口的本地错误文案必须包含中文说明')
assert.doesNotMatch(vectorStoresSource, /new OpenAICompatibleVectorStoresRequestError\(\s*['"](?![^'"]*[\u3400-\u9fff])[^'"]+['"]/, '向量存储接口的本地错误文案必须包含中文说明')
assert.doesNotMatch(vectorStoresSource, /message:\s*error instanceof Error \? error\.message/, '向量存储索引的未知本地错误不得直接写入客户端 last_error')
assert.doesNotMatch(vectorIndexerSource, /new OpenAICompatibleVectorStoreIndexingError\(\s*`(?![^`]*[\u3400-\u9fff])[^`]*`/, '向量索引本地错误文案必须包含中文说明')
assert.doesNotMatch(fileSearchSource, /new GatewayRequestValidationError\(\s*`(?![^`]*[\u3400-\u9fff])[^`]*`/, '文件搜索桥接本地错误文案必须包含中文说明')

console.log('系统错误消息回归通过：系统错误中文化，上游错误原文保留')
