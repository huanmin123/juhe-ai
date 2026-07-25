import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { readFileSync } from 'node:fs'
import type { ClientRequest, IncomingMessage, RequestOptions } from 'node:http'
import https from 'node:https'
import { syncBuiltinESMExports } from 'node:module'
import { resolve } from 'node:path'
import { PassThrough } from 'node:stream'
import { mock } from 'node:test'

import { openAIOAuthTokenRequestTimeoutMs, openAIOAuthTokenResponseMaxBytes, parseOpenAIOAuthExpiresIn, refreshOpenAIOAuthToken, sanitizeOpenAIOAuthErrorMessage } from '../../modules/openai-oauth/openai-oauth.service.js'

assert.equal(openAIOAuthTokenResponseMaxBytes, 256 * 1024, 'OAuth token 响应体上限应固定为 256KB')
assert.equal(openAIOAuthTokenRequestTimeoutMs, 25_000, 'OAuth token 请求超时必须短于 DB service HTTP proxy 30s 超时')
assert.equal(parseOpenAIOAuthExpiresIn(3600), 3600, 'OAuth expires_in 正数应保留')
assert.throws(() => parseOpenAIOAuthExpiresIn(0), /expires_in/, 'OAuth expires_in 为 0 时必须拒绝')
assert.throws(() => parseOpenAIOAuthExpiresIn(-1), /expires_in/, 'OAuth expires_in 为负数时必须拒绝')
assert.throws(() => parseOpenAIOAuthExpiresIn('not-a-number'), /expires_in/, 'OAuth expires_in 非数字时必须拒绝')

const source = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.service.ts'), 'utf8')
assert.match(source, /new BoundedBufferCollector\(openAIOAuthTokenResponseMaxBytes\)/, 'OAuth token 响应必须使用有界 buffer 收集')
assert.match(source, /body\.truncated[\s\S]*const error = new Error\('OpenAI OAuth 令牌响应体过大'\)[\s\S]*settleResponse\(error\)[\s\S]*request\.destroy\(error\)/, 'OAuth token 响应超限时必须先拒绝并主动中断请求')
assert.match(source, /sanitizeOpenAIOAuthErrorMessage\(normalizeString\(payload\.error_description\)[\s\S]*\|\|\s*text\)/, 'OAuth token endpoint 非 2xx 错误描述必须先脱敏再进入 Error.message')
assert.match(source, /timeout:\s*openAIOAuthTokenRequestTimeoutMs/, 'OAuth token endpoint 请求必须使用命名短超时，避免系统 API 504')
assert.doesNotMatch(source, /timeout:\s*120000/, 'OAuth token endpoint 不能继续使用 120s 长超时')
assert.doesNotMatch(source, /const chunks: Buffer\[\]/, 'OAuth token 响应不能无界保存 chunk 数组')
assert.doesNotMatch(source, /Buffer\.concat\(chunks\)/, 'OAuth token 响应不能无界拼接完整响应体')

const interruptedResponseResults = []
for (const scenario of ['aborted', 'error', 'close'] as const) {
  interruptedResponseResults.push(await runInterruptedTokenResponse(scenario))
}
assert.match(interruptedResponseResults[0], /中断|aborted/i, 'OAuth token 响应 aborted 时必须拒绝，不能永久 pending')
assert.match(interruptedResponseResults[1], /mock token response error/, 'OAuth token 响应 error 时必须保留原始失败原因并拒绝')
assert.match(interruptedResponseResults[2], /提前关闭|closed/i, 'OAuth token 响应在 end 前 close 时必须拒绝，不能永久 pending')

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
assert.match(refreshSource, /sanitizeOpenAIOAuthErrorMessage\(error instanceof Error \? error\.message : 'OpenAI OAuth 访问令牌刷新失败'\)/, 'OAuth 后台刷新诊断与本地配置异常消息必须统一清洗')
assert.match(refreshSource, /failureKind = isOpenAIOAuthRefreshLocalConfigurationError\(error\)[\s\S]*'local_configuration'[\s\S]*'untrusted_upstream_or_runtime'/, 'OAuth 后台刷新必须按本地产生的专用错误类型区分状态变更资格')
assert.match(refreshSource, /failureState\.localConfigurationCount >= oauthTokenRefreshFailureThreshold/, 'OAuth 后台刷新只允许连续本地配置错误触发账户异常')
assert.doesNotMatch(refreshSource, /failureState\.count >= oauthTokenRefreshFailureThreshold/, 'OAuth 上游或网络失败总计数不得触发账户异常')
assert.match(refreshSource, /if \(account\.localConfigurationError\) \{[\s\S]*throw new OpenAIOAuthRefreshLocalConfigurationError/, 'DB service 返回的结构化代理配置错误必须进入本地配置分类')
assert.match(refreshSource, /const resolution = resolveProxyUrlsForProfiles\(\[account\.proxyProfileId\]\)[\s\S]*if \(!resolution\?\.proxyUrl\) \{[\s\S]*throw new OpenAIOAuthRefreshLocalConfigurationError/, '同步代理解析只允许结构化不可用结果进入本地配置分类')
assert.doesNotMatch(refreshSource, /catch \(cause\)[\s\S]*ProxyProfileUnavailableError/, 'OAuth 代理解析不得把 DB/runtime 异常按错误文案或通用 catch 归为本地配置')
assert.match(refreshSource, /account\.status === 'error' && isManagedOpenAIOAuthRefreshErrorCode\(account\.lastErrorCode\)[\s\S]*\? clearAccountFailureState/, 'OAuth 刷新成功只允许清理本刷新路径管理的 error 状态')
assert.doesNotMatch(refreshSource, /account\.status !== 'pending_test' && account\.status !== 'disabled'/, 'OAuth 刷新成功不得通过排除法清理显式 temporary_unavailable 或 rate_limited')
const routesSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.routes.ts'), 'utf8')
const mutationSource = readFileSync(resolve('src/storage/account-runtime-mutation.repository.ts'), 'utf8')
const dbServiceTypesSource = readFileSync(resolve('src/modules/db-service/db-service-types.ts'), 'utf8')
const dbServiceHandlersSource = readFileSync(resolve('src/modules/db-service/db-service-handlers.ts'), 'utf8')
assert.match(routesSource, /post\('\/auth-url', async \(req, res, next\) => \{[\s\S]*try \{[\s\S]*generateOpenAIAuthURL\([^)]*\)[\s\S]*catch \(error\) \{[\s\S]*next\(error\)/, 'OAuth auth-url session 写入失败必须进入 Express next 错误链路')
assert.match(routesSource, /generateOpenAIAuthURL\(getRequestAccessScope\(\)\?\.systemAccountId\)/, 'OAuth auth-url session 必须绑定当前登录系统账户')
assert.match(routesSource, /function oauthErrorMessage[\s\S]*sanitizeOpenAIOAuthErrorMessage/, 'OAuth 路由返回 502 错误前必须统一清洗错误消息')
assert.match(routesSource, /post\('\/accounts\/:id\/refresh-token'[\s\S]*error instanceof ProxyProfileUnavailableError \|\| isOpenAIOAuthRefreshLocalConfigurationError\(error\)[\s\S]*res\.status\(400\)/, 'OAuth 手动刷新遇到本地配置错误必须返回可修正的 400，不得退化为上游 502')
assert.match(routesSource, /updated\.status === 'error' && isManagedOpenAIOAuthRefreshErrorCode\(updated\.lastErrorCode\)[\s\S]*clearAccountFailureStateAsync/, 'OAuth 重新授权成功只允许清理本刷新路径管理的 error 状态')
assert.match(dbServiceTypesSource, /type: 'clear_account_failure_state'[\s\S]*expectedLastErrorCodes\?: string\[\]/, 'DB service clear operation 必须携带 OAuth 错误 provenance 条件')
assert.match(dbServiceHandlersSource, /expectedLastErrorCodes: operation\.expectedLastErrorCodes/, 'DB service handler 必须把 OAuth provenance 条件传入 SQLite/PG repository')
assert.match(mutationSource, /function expectedLastErrorCodePredicate/, 'SQLite/PG failure-state clear 必须集中生成 provenance SQL 条件')
assert((mutationSource.match(/\$\{expectedLastErrorClause\}/g) ?? []).length >= 6, 'SQLite 与 PostgreSQL 的多个原子清理分支都必须带 expected last_error_code 条件')
assert.match(routesSource, /createAccountAsync/, 'OAuth 管理端创建账户必须走 async repository，避免高性能模式误入同步写库')
assert.match(routesSource, /findAccountForTestAsync/, 'OAuth 管理端读取账户必须走 async repository，避免阻塞系统 API')
assert.match(routesSource, /findGroupSummaryAsync/, 'OAuth 管理端分组校验必须走 async repository')
assert.match(routesSource, /listProvidersAsync/, 'OAuth 管理端供应商读取必须走 async repository')
assert.match(routesSource, /resolveProxyUrlForProfileAsync/, 'OAuth 管理端代理解析必须走 async repository')
assert.match(routesSource, /updateAccountAsync/, 'OAuth 管理端更新账户凭据必须走 async repository')
assert.match(routesSource, /clearAccountFailureStateAsync/, 'OAuth 管理端异常恢复必须走 async repository')
assert.match(routesSource, /runLoggedOperationAsync/, 'OAuth 管理端操作日志包裹必须使用 async 版本')
assert.match(routesSource, /recordOperationLogAsync/, 'OAuth 手动刷新 token 后的操作日志必须使用 async 版本')
assert.match(routesSource, /createAccountAsync\(\{[\s\S]*?status:\s*'pending_test'/, 'OAuth 新建账户必须直接进入待后台检查状态')
assert.match(routesSource, /healthCheckModel:\s*parsed\.data\.healthCheckModel/, 'OAuth 新建账户必须透传表单检查模型')
assert.doesNotMatch(routesSource, /activationTestTaskId|accountCreateStatusFromActivationTest/, 'OAuth 创建流程不能继续依赖人工测试任务激活')
assert.doesNotMatch(routesSource, /import \{[^}]*\bcreateAccount\b[^}]*\} from '..\/..\/storage\/repositories\.js'/, 'OAuth 路由不能重新导入同步 createAccount')
assert.doesNotMatch(routesSource, /import \{[^}]*\bfindAccountForTest\b[^}]*\} from '..\/..\/storage\/repositories\.js'/, 'OAuth 路由不能重新导入同步 findAccountForTest')
assert.doesNotMatch(routesSource, /import \{[^}]*\brecordOperationLog\b[^}]*\} from '..\/operation-logs\/operation-log\.service\.js'/, 'OAuth 路由不能重新导入同步操作日志写入')

console.log('OpenAI OAuth token 响应边界回归通过：token endpoint 响应体有界收集，OAuth 错误消息会清理敏感 token 和密钥')

function assertNoLeak(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}

async function runInterruptedTokenResponse(scenario: 'aborted' | 'error' | 'close'): Promise<string> {
  const response = new PassThrough()
  Object.assign(response, { statusCode: 200, complete: false })
  // The production listener must handle the error; this fallback prevents an
  // unhandled EventEmitter error from terminating the regression before timeout.
  response.on('error', () => undefined)

  const request = new EventEmitter() as EventEmitter & {
    destroy: ClientRequest['destroy']
    end: ClientRequest['end']
  }
  let responseCallback: ((incoming: IncomingMessage) => void) | undefined
  request.destroy = ((error?: Error) => {
    if (error) queueMicrotask(() => request.emit('error', error))
    return request as unknown as ClientRequest
  }) as ClientRequest['destroy']
  request.end = (() => {
    queueMicrotask(() => {
      responseCallback?.(response as unknown as IncomingMessage)
      request.emit('response', response)
      if (scenario === 'error') {
        response.emit('error', new Error('mock token response error'))
      } else {
        response.emit(scenario)
      }
    })
    return request as unknown as ClientRequest
  }) as ClientRequest['end']

  const requestMock = mock.method(https, 'request', ((
    _url: string | URL,
    _options: RequestOptions,
    callback?: (incoming: IncomingMessage) => void
  ) => {
    responseCallback = callback
    return request as unknown as ClientRequest
  }) as typeof https.request)
  syncBuiltinESMExports()

  try {
    const outcome = await Promise.race([
      refreshOpenAIOAuthToken({ refreshToken: 'mock-refresh-token' }).then(
        () => 'unexpected success',
        (error: unknown) => error instanceof Error ? error.message : String(error)
      ),
      new Promise<string>((resolvePromise) => setTimeout(() => resolvePromise('timed out while pending'), 150))
    ])
    return outcome
  } finally {
    requestMock.mock.restore()
    syncBuiltinESMExports()
  }
}
