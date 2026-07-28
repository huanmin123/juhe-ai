import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  ANTHROPIC_PROVIDER_CODE,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { AccountConfigRevisionConflictError } from '../../storage/repositories.js'

import {
  ANTHROPIC_OAUTH_API_SCOPE,
  ANTHROPIC_OAUTH_AUTHORIZE_URL,
  ANTHROPIC_OAUTH_BROWSER_SCOPE,
  ANTHROPIC_OAUTH_CLIENT_ID,
  ANTHROPIC_OAUTH_REDIRECT_URI,
  ANTHROPIC_OAUTH_TOKEN_URL,
  buildAnthropicAuthorizeUrl,
  buildAnthropicOAuthCredentials,
  generateAnthropicAuthURL
} from '../../modules/anthropic-oauth/anthropic-oauth.service.js'
import {
  prepareAnthropicAccountBeforeDispatch,
  type AnthropicOAuthDispatchPreparationDependencies
} from '../../modules/providers/drivers/anthropic/oauth-dispatch-preparation.js'

assert.equal(ANTHROPIC_OAUTH_CLIENT_ID, '9d1c250a-e61b-44d9-88ed-5944d1962f5e', 'Anthropic OAuth client id 必须对齐当前已验证实现')
assert.equal(ANTHROPIC_OAUTH_AUTHORIZE_URL, 'https://claude.ai/oauth/authorize', 'Anthropic OAuth authorize URL 必须固定为 Claude 官方地址')
assert.equal(ANTHROPIC_OAUTH_TOKEN_URL, 'https://platform.claude.com/v1/oauth/token', 'Anthropic OAuth token URL 必须对齐 Claude platform OAuth endpoint')
assert.equal(ANTHROPIC_OAUTH_REDIRECT_URI, 'https://platform.claude.com/oauth/code/callback', 'Anthropic OAuth redirect URI 必须对齐 Claude platform code callback')
assert.equal(ANTHROPIC_OAUTH_BROWSER_SCOPE, 'org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload', 'Anthropic OAuth 浏览器 scope 必须覆盖当前托管授权所需能力')
assert.equal(ANTHROPIC_OAUTH_API_SCOPE, 'user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload', 'Anthropic OAuth API scope 必须保持与当前 Bearer 能力一致')

const authorizeUrl = new URL(buildAnthropicAuthorizeUrl({
  state: 'contract-state',
  codeChallenge: 'contract-challenge',
  scope: ANTHROPIC_OAUTH_BROWSER_SCOPE,
  redirectUri: ANTHROPIC_OAUTH_REDIRECT_URI,
  clientId: ANTHROPIC_OAUTH_CLIENT_ID
}))

assert.equal(authorizeUrl.origin + authorizeUrl.pathname, ANTHROPIC_OAUTH_AUTHORIZE_URL)
assert.equal(authorizeUrl.searchParams.get('code'), 'true')
assert.equal(authorizeUrl.searchParams.get('client_id'), ANTHROPIC_OAUTH_CLIENT_ID)
assert.equal(authorizeUrl.searchParams.get('response_type'), 'code')
assert.equal(authorizeUrl.searchParams.get('redirect_uri'), ANTHROPIC_OAUTH_REDIRECT_URI)
assert.equal(authorizeUrl.searchParams.get('scope'), ANTHROPIC_OAUTH_BROWSER_SCOPE)
assert.equal(authorizeUrl.searchParams.get('code_challenge'), 'contract-challenge')
assert.equal(authorizeUrl.searchParams.get('code_challenge_method'), 'S256')
assert.equal(authorizeUrl.searchParams.get('state'), 'contract-state')

const generatedSession = await generateAnthropicAuthURL('anthropic-oauth-contract-owner')
const generatedUrl = new URL(generatedSession.authUrl)
assert.ok(generatedSession.sessionId.length >= 16, 'Anthropic OAuth 授权会话 id 必须非空且保持随机性')
assert.equal(generatedUrl.origin + generatedUrl.pathname, ANTHROPIC_OAUTH_AUTHORIZE_URL)
assert.equal(generatedUrl.searchParams.get('client_id'), ANTHROPIC_OAUTH_CLIENT_ID)
assert.equal(generatedUrl.searchParams.get('redirect_uri'), ANTHROPIC_OAUTH_REDIRECT_URI)
assert.equal(generatedUrl.searchParams.get('scope'), ANTHROPIC_OAUTH_BROWSER_SCOPE)
assert.equal(generatedUrl.searchParams.get('code_challenge_method'), 'S256')
assert.ok(generatedUrl.searchParams.get('state'), 'Anthropic OAuth 授权链接必须包含 state')
assert.ok(generatedUrl.searchParams.get('code_challenge'), 'Anthropic OAuth 授权链接必须包含 PKCE code_challenge')

const credentialsWithFallbackRefresh = buildAnthropicOAuthCredentials({
  accessToken: 'access-token-a',
  clientId: ANTHROPIC_OAUTH_CLIENT_ID,
  expiresAt: '2026-07-27T00:00:00.000Z',
  email: 'user@example.com',
  accountId: 'account-a',
  organizationId: 'org-a',
  scope: ANTHROPIC_OAUTH_API_SCOPE,
  tokenType: 'Bearer'
}, { refreshToken: 'refresh-fallback-a' })

assert.deepEqual(credentialsWithFallbackRefresh, {
  access_token: 'access-token-a',
  base_url: 'https://api.anthropic.com/v1',
  client_id: ANTHROPIC_OAUTH_CLIENT_ID,
  refresh_token: 'refresh-fallback-a',
  expires_at: '2026-07-27T00:00:00.000Z',
  email: 'user@example.com',
  account_id: 'account-a',
  organization_id: 'org-a',
  scope: ANTHROPIC_OAUTH_API_SCOPE,
  token_type: 'Bearer'
}, 'Anthropic OAuth 凭据归一化必须补齐 base_url、client_id 与 fallback refresh token')

const credentialsWithIssuedRefresh = buildAnthropicOAuthCredentials({
  accessToken: 'access-token-b',
  refreshToken: 'refresh-issued-b',
  clientId: 'custom-client-b'
}, { refreshToken: 'refresh-fallback-b' })

assert.equal(credentialsWithIssuedRefresh.refresh_token, 'refresh-issued-b', 'Anthropic OAuth token 响应携带 refresh_token 时必须优先使用响应值')
assert.equal(credentialsWithIssuedRefresh.base_url, 'https://api.anthropic.com/v1', 'Anthropic OAuth 凭据必须固定写入官方 Messages 根路径')
assert.equal(credentialsWithIssuedRefresh.client_id, 'custom-client-b', 'Anthropic OAuth 凭据必须保留实际换码 client_id')

const expiredCredentials = {
  access_token: 'expired-access-token',
  refresh_token: 'rotating-refresh-token',
  client_id: ANTHROPIC_OAUTH_CLIENT_ID,
  expires_at: '2000-01-01T00:00:00.000Z',
  base_url: 'https://anthropic-proxy.example/v1',
  metadata_marker: 'must-survive-refresh'
}
let releaseRefresh: (() => void) | undefined
const refreshGate = new Promise<void>((resolve) => { releaseRefresh = resolve })
let refreshCalls = 0
let persistedCredentials: Record<string, unknown> | undefined
const dependencies: AnthropicOAuthDispatchPreparationDependencies = {
  async loadAccount() {
    return { providerCode: ANTHROPIC_PROVIDER_CODE, type: 'oauth', configRevision: 7, credentials: expiredCredentials }
  },
  async refreshToken(input) {
    refreshCalls += 1
    assert.equal(input.refreshToken, 'rotating-refresh-token')
    await refreshGate
    return {
      accessToken: 'fresh-access-token',
      refreshToken: 'rotated-refresh-token',
      expiresAt: '2099-01-01T00:00:00.000Z',
      clientId: ANTHROPIC_OAUTH_CLIENT_ID
    }
  },
  async persistCredentials(_accountId, credentials, expectedConfigRevision) {
    assert.equal(expectedConfigRevision, 7)
    persistedCredentials = credentials
    return { providerCode: ANTHROPIC_PROVIDER_CODE, type: 'oauth', configRevision: 8, credentials }
  }
}
const abortController = new AbortController()
const firstRefresh = prepareAnthropicAccountBeforeDispatch(anthropicDispatchAccount('anthropic-refresh-shared'), abortController.signal, dependencies)
const secondRefresh = prepareAnthropicAccountBeforeDispatch(anthropicDispatchAccount('anthropic-refresh-shared'), undefined, dependencies)
abortController.abort()
await assert.rejects(firstRefresh, /请求已取消/u, '单个下游取消只能停止自己的等待')
releaseRefresh?.()
const refreshedDispatch = await secondRefresh
assert.equal(refreshCalls, 1, '同一物理账户并发刷新必须单飞')
assert.equal(refreshedDispatch.apiKey, 'fresh-access-token')
assert.equal(refreshedDispatch.refreshToken, 'rotated-refresh-token')
assert.equal(refreshedDispatch.baseUrl, 'https://anthropic-proxy.example/v1')
assert.equal(persistedCredentials?.metadata_marker, 'must-survive-refresh', '刷新不得丢弃账户扩展元数据')
assert.equal(persistedCredentials?.base_url, 'https://anthropic-proxy.example/v1', '刷新不得覆盖自定义 Anthropic base_url')

let conflictLoadCount = 0
const conflictResult = await prepareAnthropicAccountBeforeDispatch(
  anthropicDispatchAccount('anthropic-refresh-conflict'),
  undefined,
  {
    async loadAccount() {
      conflictLoadCount += 1
      return conflictLoadCount === 1
        ? { providerCode: ANTHROPIC_PROVIDER_CODE, type: 'oauth', configRevision: 3, credentials: expiredCredentials }
        : {
            providerCode: ANTHROPIC_PROVIDER_CODE,
            type: 'oauth',
            configRevision: 4,
            credentials: {
              ...expiredCredentials,
              access_token: 'concurrent-fresh-access-token',
              refresh_token: 'concurrent-rotated-refresh-token',
              expires_at: '2099-01-01T00:00:00.000Z'
            }
          }
    },
    async refreshToken() {
      return { accessToken: 'stale-refresh-result', clientId: ANTHROPIC_OAUTH_CLIENT_ID }
    },
    async persistCredentials() {
      throw new AccountConfigRevisionConflictError('anthropic-refresh-conflict', 3, 4)
    }
  }
)
assert.equal(conflictResult.apiKey, 'concurrent-fresh-access-token', 'CAS 冲突后必须采用数据库中已胜出的有效令牌')
assert.equal(conflictResult.refreshToken, 'concurrent-rotated-refresh-token')

let rebaseLoadCount = 0
const rebasePersistRevisions: number[] = []
const rebaseResult = await prepareAnthropicAccountBeforeDispatch(
  anthropicDispatchAccount('anthropic-refresh-rebase'),
  undefined,
  {
    async loadAccount() {
      rebaseLoadCount += 1
      return {
        providerCode: ANTHROPIC_PROVIDER_CODE,
        type: 'oauth',
        configRevision: rebaseLoadCount === 1 ? 10 : 11,
        credentials: {
          ...expiredCredentials,
          base_url: rebaseLoadCount === 1 ? 'https://anthropic-proxy.example/v1' : 'https://edited-proxy.example/v1',
          metadata_marker: rebaseLoadCount === 1 ? 'before-edit' : 'after-edit'
        }
      }
    },
    async refreshToken() {
      return {
        accessToken: 'rebased-access-token',
        refreshToken: 'rebased-rotated-refresh-token',
        expiresAt: '2099-01-01T00:00:00.000Z',
        clientId: ANTHROPIC_OAUTH_CLIENT_ID
      }
    },
    async persistCredentials(_accountId, credentials, expectedConfigRevision) {
      rebasePersistRevisions.push(expectedConfigRevision)
      if (expectedConfigRevision === 10) {
        throw new AccountConfigRevisionConflictError('anthropic-refresh-rebase', 10, 11)
      }
      return { providerCode: ANTHROPIC_PROVIDER_CODE, type: 'oauth', configRevision: 12, credentials }
    }
  }
)
assert.deepEqual(rebasePersistRevisions, [10, 11], '无关配置冲突后必须基于最新 revision 有界重试写回旋转令牌')
assert.equal(rebaseResult.refreshToken, 'rebased-rotated-refresh-token')
assert.equal(rebaseResult.baseUrl, 'https://edited-proxy.example/v1', 'CAS rebase 必须保留并发编辑后的 base_url')

const routesSource = readFileSync(resolve('src/modules/anthropic-oauth/anthropic-oauth.routes.ts'), 'utf8')
assertRoute(routesSource, "post('/auth-url'", 'Anthropic OAuth 必须暴露 auth-url 接口')
assertRoute(routesSource, "post('/create-from-code'", 'Anthropic OAuth 必须暴露授权码建号接口')
assertRoute(routesSource, "post('/create-from-refresh-token'", 'Anthropic OAuth 必须暴露 Refresh Token 建号接口')
assertRoute(routesSource, "post('/accounts/:id/refresh-token'", 'Anthropic OAuth 必须暴露手动刷新 access token 接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-code'", 'Anthropic OAuth 必须暴露授权码重新授权接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-refresh-token'", 'Anthropic OAuth 必须暴露 Refresh Token 重新授权接口')
assertRoute(routesSource, 'Anthropic OAuth 账户缺少 Refresh Token', 'Anthropic OAuth 刷新接口必须对缺失 Refresh Token 做显式拒绝')
assertRoute(routesSource, 'expectedConfigRevision: account.configRevision ?? 1', 'Anthropic OAuth 手动刷新与重授权必须使用 config revision CAS')
assertRoute(routesSource, 'runWithProviderOAuthRefreshLock(', 'Anthropic OAuth 手动刷新与重授权必须加入跨进程刷新锁')
assertRoute(routesSource, 'oauthTokensChanged(account.credentials, current.credentials)', 'Anthropic 手动刷新等待锁后必须识别其他节点已完成的刷新')
assert.ok((routesSource.match(/oauthTokensChanged\(account\.credentials, current\.credentials\)/gu) ?? []).length >= 3, 'Anthropic 手动刷新与两条重授权路径都必须拦截锁内凭据变化')
assertRoute(routesSource, 'error instanceof AccountConfigRevisionConflictError', 'Anthropic OAuth 并发写回冲突必须返回业务冲突')
assertRoute(routesSource, "const existingBaseUrl = stringCredential(account.credentials, 'base_url')", 'Anthropic OAuth 刷新必须读取账户现有 base_url')
assertRoute(routesSource, 'if (existingBaseUrl) credentials.base_url = existingBaseUrl', 'Anthropic OAuth 刷新与重授权必须保留账户现有 base_url')
assert.equal(routesSource.includes('clearAccountFailureStateAsync'), false, 'Anthropic OAuth 手动刷新不得清除限流或临时不可用等业务状态')
assertRoute(routesSource, "sanitizeAccountCredentialCarrierResponse(updatedAccount)", 'Anthropic OAuth 刷新接口必须返回脱敏后的账户响应')

const appSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
assertRoute(appSource, '/my-anthropic-oauth', '系统 API 必须挂载 my-anthropic-oauth 自有入口')
assertRoute(appSource, '/anthropic-oauth', '系统 API 必须挂载 anthropic-oauth 管理员入口')

const dbAccessSource = readFileSync(resolve('src/modules/system-api/system-api-db-access.ts'), 'utf8')
assertRoute(dbAccessSource, "pattern: /^\\/(?:my-)?anthropic-oauth(?:\\/.*)?$/, mode: 'read'", 'system-api DB access 规则必须允许 Anthropic OAuth 读请求')

console.log('Anthropic OAuth 协议契约回归通过：authorize 参数、凭据归一化、系统 API 挂载与路由面均符合当前实现')

function assertRoute(source: string, fragment: string, message: string): void {
  assert.equal(source.includes(fragment), true, message)
}

function anthropicDispatchAccount(id: string): DispatchAccountSecret {
  return {
    id,
    providerCode: ANTHROPIC_PROVIDER_CODE,
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    protocolCode: ANTHROPIC_PROTOCOL_CODE,
    protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
    systemAccountId: 'system-account',
    accountOwnerSystemAccountId: 'system-account',
    groupOwnerSystemAccountId: 'system-account',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: 'oauth',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'messages_json',
    baseUrl: 'https://anthropic-proxy.example/v1',
    apiKey: 'expired-access-token',
    refreshToken: 'rotating-refresh-token',
    clientId: ANTHROPIC_OAUTH_CLIENT_ID,
    expiresAt: '2000-01-01T00:00:00.000Z',
    streamFailureCount: 0,
    credentials: { ...expiredCredentials }
  }
}
