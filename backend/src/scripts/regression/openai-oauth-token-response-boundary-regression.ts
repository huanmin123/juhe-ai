import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { openAIOAuthTokenRequestTimeoutMs, openAIOAuthTokenResponseMaxBytes, sanitizeOpenAIOAuthErrorMessage } from '../../modules/openai-oauth/openai-oauth.service.js'

assert.equal(openAIOAuthTokenResponseMaxBytes, 256 * 1024, 'OAuth token 响应体上限应固定为 256KB')
assert.equal(openAIOAuthTokenRequestTimeoutMs, 25_000, 'OAuth token 请求超时必须短于 DB service HTTP proxy 30s 超时')

const source = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.service.ts'), 'utf8')
assert.match(source, /new BoundedBufferCollector\(openAIOAuthTokenResponseMaxBytes\)/, 'OAuth token 响应必须使用有界 buffer 收集')
assert.match(source, /body\.truncated[\s\S]*request\.destroy\(new Error\('OpenAI OAuth 令牌响应体过大'\)\)/, 'OAuth token 响应超限时必须主动中断请求')
assert.match(source, /sanitizeOpenAIOAuthErrorMessage\(normalizeString\(payload\.error_description\)[\s\S]*\|\|\s*text\)/, 'OAuth token endpoint 非 2xx 错误描述必须先脱敏再进入 Error.message')
assert.match(source, /timeout:\s*openAIOAuthTokenRequestTimeoutMs/, 'OAuth token endpoint 请求必须使用命名短超时，避免系统 API 504')
assert.doesNotMatch(source, /timeout:\s*120000/, 'OAuth token endpoint 不能继续使用 120s 长超时')
assert.doesNotMatch(source, /const chunks: Buffer\[\]/, 'OAuth token 响应不能无界保存 chunk 数组')
assert.doesNotMatch(source, /Buffer\.concat\(chunks\)/, 'OAuth token 响应不能无界拼接完整响应体')

const sanitizedMessage = sanitizeOpenAIOAuthErrorMessage(
  'token endpoint failed Authorization: Bearer oauth-boundary-bearer-token sk-oauth-boundary-secret-token refresh_token=oauth-boundary-refresh-token client_secret=oauth-boundary-client-secret proxy=https://oauth-proxy-user:oauth-proxy-password@example.com'
)
assertNoLeak(sanitizedMessage, [
  'oauth-boundary-bearer-token',
  'sk-oauth-boundary-secret-token',
  'oauth-boundary-refresh-token',
  'oauth-boundary-client-secret',
  'oauth-proxy-user',
  'oauth-proxy-password'
], 'OAuth token 错误消息清洗不应保留敏感原文')

const refreshSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth-access-token-refresh.service.ts'), 'utf8')
assert.match(refreshSource, /sanitizeOpenAIOAuthErrorMessage\(error instanceof Error \? error\.message : 'OpenAI OAuth 访问令牌刷新失败'\)/, 'OAuth 后台刷新失败写入账户异常前必须清洗错误消息')
const routesSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.routes.ts'), 'utf8')
assert.match(routesSource, /function oauthErrorMessage[\s\S]*sanitizeOpenAIOAuthErrorMessage/, 'OAuth 路由返回 502 错误前必须统一清洗错误消息')

console.log('OpenAI OAuth token 响应边界回归通过：token endpoint 响应体有界收集，OAuth 错误消息会清理敏感 token 和密钥')

function assertNoLeak(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}
