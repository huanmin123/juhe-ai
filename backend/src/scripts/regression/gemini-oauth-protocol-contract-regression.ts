import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { AccountConfigRevisionConflictError } from '../../storage/repositories.js'

import {
  GEMINI_CLI_DEFAULT_BASE_URL,
  GEMINI_CLI_OAUTH_CLIENT_ID,
  GEMINI_CLI_OAUTH_REDIRECT_URI,
  GEMINI_CODE_ASSIST_OAUTH_SCOPE,
  GEMINI_GOOGLE_ONE_OAUTH_SCOPE,
  GEMINI_OAUTH_AUTHORIZE_URL,
  GEMINI_OAUTH_DEFAULT_BASE_URL,
  GEMINI_OAUTH_REDIRECT_URI,
  GEMINI_OAUTH_SCOPE,
  GEMINI_OAUTH_TOKEN_URL,
  buildGeminiAuthorizeUrl,
  buildGeminiOAuthCredentials,
  generateGeminiAuthURL,
  getGeminiOAuthCapabilities,
  inferGeminiGoogleOneTier
} from '../../modules/gemini-oauth/gemini-oauth.service.js'
import {
  prepareGeminiAccountBeforeDispatch,
  type GeminiOAuthDispatchPreparationDependencies
} from '../../modules/providers/drivers/gemini/oauth-dispatch-preparation.js'

assert.equal(GEMINI_OAUTH_AUTHORIZE_URL, 'https://accounts.google.com/o/oauth2/v2/auth')
assert.equal(GEMINI_OAUTH_TOKEN_URL, 'https://oauth2.googleapis.com/token')
assert.equal(GEMINI_OAUTH_REDIRECT_URI, 'http://localhost:1455/auth/callback')
assert.equal(GEMINI_CLI_OAUTH_REDIRECT_URI, 'https://codeassist.google.com/authcode')
assert.equal(GEMINI_OAUTH_DEFAULT_BASE_URL, 'https://generativelanguage.googleapis.com')
assert.equal(GEMINI_CLI_DEFAULT_BASE_URL, 'https://cloudcode-pa.googleapis.com')
assert.equal(GEMINI_GOOGLE_ONE_OAUTH_SCOPE, GEMINI_CODE_ASSIST_OAUTH_SCOPE, '内置 Gemini CLI client 不能申请受限的 Drive scope')

const authorizeUrl = new URL(buildGeminiAuthorizeUrl({
  state: 'contract-state',
  codeChallenge: 'contract-challenge',
  scope: GEMINI_OAUTH_SCOPE,
  redirectUri: GEMINI_OAUTH_REDIRECT_URI,
  clientId: 'contract-client-id',
  projectId: 'contract-project'
}))
assert.equal(authorizeUrl.origin + authorizeUrl.pathname, GEMINI_OAUTH_AUTHORIZE_URL)
assert.equal(authorizeUrl.searchParams.get('response_type'), 'code')
assert.equal(authorizeUrl.searchParams.get('project_id'), 'contract-project')
assert.equal(authorizeUrl.searchParams.get('code_challenge_method'), 'S256')
assert.equal(authorizeUrl.searchParams.get('access_type'), 'offline')
assert.equal(authorizeUrl.searchParams.get('prompt'), 'consent')
assert.equal(authorizeUrl.searchParams.get('include_granted_scopes'), 'true')

const codeAssistSession = await generateGeminiAuthURL({
  oauthType: 'code_assist',
  projectId: 'generated-project',
  tierId: 'STANDARD',
  ownerSystemAccountId: 'gemini-oauth-contract-owner'
})
const codeAssistUrl = new URL(codeAssistSession.authUrl)
assert.match(codeAssistSession.sessionId, /^[a-f0-9]{32}$/u)
assert.match(codeAssistSession.state, /^[A-Za-z0-9_-]{43}$/u)
assert.equal(codeAssistUrl.searchParams.get('client_id'), GEMINI_CLI_OAUTH_CLIENT_ID)
assert.equal(codeAssistUrl.searchParams.get('redirect_uri'), GEMINI_CLI_OAUTH_REDIRECT_URI)
assert.equal(codeAssistUrl.searchParams.get('scope'), GEMINI_CODE_ASSIST_OAUTH_SCOPE)
assert.equal(codeAssistUrl.searchParams.get('project_id'), 'generated-project')
assert.match(codeAssistUrl.searchParams.get('code_challenge') ?? '', /^[A-Za-z0-9_-]{43}$/u)

const aiStudioSession = await generateGeminiAuthURL({
  oauthType: 'ai_studio',
  clientId: 'generated-client-id',
  clientSecret: 'generated-client-secret'
})
const aiStudioUrl = new URL(aiStudioSession.authUrl)
assert.equal(aiStudioUrl.searchParams.get('client_id'), 'generated-client-id')
assert.equal(aiStudioUrl.searchParams.get('redirect_uri'), GEMINI_OAUTH_REDIRECT_URI)
assert.equal(aiStudioUrl.searchParams.get('scope'), GEMINI_OAUTH_SCOPE)

const googleOneSession = await generateGeminiAuthURL({ oauthType: 'google_one' })
const googleOneUrl = new URL(googleOneSession.authUrl)
assert.equal(googleOneUrl.searchParams.get('client_id'), GEMINI_CLI_OAUTH_CLIENT_ID)
assert.equal(googleOneUrl.searchParams.get('redirect_uri'), GEMINI_CLI_OAUTH_REDIRECT_URI)
assert.equal(googleOneUrl.searchParams.get('scope'), GEMINI_GOOGLE_ONE_OAUTH_SCOPE)

const capabilities = getGeminiOAuthCapabilities()
assert.equal(capabilities.defaultOAuthType, 'code_assist')
assert.deepEqual(capabilities.oauthTypes.map((item) => item.oauthType), ['code_assist', 'google_one', 'ai_studio'])
assert.equal(capabilities.oauthTypes[0]?.usesBuiltInClient, true)
assert.equal(capabilities.oauthTypes[2]?.requiresClientCredentials, true)
assert.deepEqual(capabilities.oauthTypes[1]?.supportedEndpointModes, ['generate_content_json', 'generate_content_sse'])

const credentials = buildGeminiOAuthCredentials({
  accessToken: 'access-token-a',
  clientId: GEMINI_CLI_OAUTH_CLIENT_ID,
  clientSecret: 'builtin-secret',
  oauthType: 'code_assist',
  projectId: 'project-a',
  tierId: 'STANDARD',
  expiresAt: '2026-07-28T00:00:00.000Z',
  scope: GEMINI_CODE_ASSIST_OAUTH_SCOPE,
  tokenType: 'Bearer',
  baseUrl: ''
}, {
  refreshToken: 'refresh-fallback-a',
  baseUrl: GEMINI_CLI_DEFAULT_BASE_URL
})
assert.deepEqual(credentials, {
  access_token: 'access-token-a',
  client_id: GEMINI_CLI_OAUTH_CLIENT_ID,
  client_secret: 'builtin-secret',
  oauth_type: 'code_assist',
  base_url: GEMINI_CLI_DEFAULT_BASE_URL,
  refresh_token: 'refresh-fallback-a',
  expires_at: '2026-07-28T00:00:00.000Z',
  token_type: 'Bearer',
  scope: GEMINI_CODE_ASSIST_OAUTH_SCOPE,
  project_id: 'project-a',
  tier_id: 'gcp_standard',
  supported_endpoint_modes: ['generate_content_json', 'generate_content_sse']
})

const aiStudioCredentials = buildGeminiOAuthCredentials({
  accessToken: 'access-token-b',
  refreshToken: 'refresh-issued-b',
  clientId: 'client-id-b',
  clientSecret: 'client-secret-b',
  oauthType: 'ai_studio',
  quotaProjectId: 'quota-project-b',
  tierId: 'paid',
  baseUrl: GEMINI_OAUTH_DEFAULT_BASE_URL
})
assert.equal(aiStudioCredentials.oauth_type, 'ai_studio')
assert.equal(aiStudioCredentials.tier_id, 'aistudio_paid')
assert.equal(aiStudioCredentials.quota_project_id, 'quota-project-b')
assert.equal('supported_endpoint_modes' in aiStudioCredentials, false, 'AI Studio 不得被 CLI endpoint 能力覆盖')

const tebibyte = 1024 ** 4
assert.equal(inferGeminiGoogleOneTier(15 * 1024 ** 3), 'google_one_free')
assert.equal(inferGeminiGoogleOneTier(2 * tebibyte), 'google_ai_pro')
assert.equal(inferGeminiGoogleOneTier(101 * tebibyte), 'google_ai_ultra')
assert.equal(inferGeminiGoogleOneTier(0), 'google_one_unknown')

const expiredCredentials = {
  access_token: 'expired-gemini-access-token',
  refresh_token: 'rotating-gemini-refresh-token',
  client_id: GEMINI_CLI_OAUTH_CLIENT_ID,
  client_secret: 'builtin-secret',
  oauth_type: 'code_assist',
  project_id: 'project-a',
  tier_id: 'gcp_standard',
  expires_at: '2000-01-01T00:00:00.000Z',
  base_url: GEMINI_CLI_DEFAULT_BASE_URL,
  metadata_marker: 'must-survive-refresh'
}
let releaseRefresh: (() => void) | undefined
const refreshGate = new Promise<void>((resolve) => { releaseRefresh = resolve })
let refreshCalls = 0
let persistedCredentials: Record<string, unknown> | undefined
const dependencies: GeminiOAuthDispatchPreparationDependencies = {
  async loadAccount() {
    return { providerCode: GEMINI_PROVIDER_CODE, type: 'google_oauth', configRevision: 7, credentials: expiredCredentials }
  },
  async refreshToken(input) {
    refreshCalls += 1
    assert.equal(input.refreshToken, 'rotating-gemini-refresh-token')
    assert.equal(input.oauthType, 'code_assist')
    await refreshGate
    return {
      accessToken: 'fresh-gemini-access-token',
      refreshToken: 'rotated-gemini-refresh-token',
      expiresAt: '2099-01-01T00:00:00.000Z',
      clientId: GEMINI_CLI_OAUTH_CLIENT_ID,
      clientSecret: 'builtin-secret',
      oauthType: 'code_assist',
      projectId: 'project-a',
      tierId: 'gcp_standard',
      baseUrl: GEMINI_CLI_DEFAULT_BASE_URL
    }
  },
  async persistCredentials(_accountId, credentials, expectedConfigRevision) {
    assert.equal(expectedConfigRevision, 7)
    persistedCredentials = credentials
    return { providerCode: GEMINI_PROVIDER_CODE, type: 'google_oauth', configRevision: 8, credentials }
  }
}
const abortController = new AbortController()
const firstRefresh = prepareGeminiAccountBeforeDispatch(geminiDispatchAccount('gemini-refresh-shared'), abortController.signal, dependencies)
const secondRefresh = prepareGeminiAccountBeforeDispatch(geminiDispatchAccount('gemini-refresh-shared'), undefined, dependencies)
abortController.abort()
await assert.rejects(firstRefresh, /请求已取消/u, '单个下游取消只能停止自己的 Gemini token 等待')
releaseRefresh?.()
const refreshedDispatch = await secondRefresh
assert.equal(refreshCalls, 1, '同一 Gemini 物理账户并发刷新必须单飞')
assert.equal(refreshedDispatch.apiKey, 'fresh-gemini-access-token')
assert.equal(refreshedDispatch.refreshToken, 'rotated-gemini-refresh-token')
assert.equal(persistedCredentials?.metadata_marker, 'must-survive-refresh', 'Gemini 刷新不得丢弃扩展元数据')

let conflictLoadCount = 0
const conflictResult = await prepareGeminiAccountBeforeDispatch(geminiDispatchAccount('gemini-refresh-conflict'), undefined, {
  async loadAccount() {
    conflictLoadCount += 1
    return conflictLoadCount === 1
      ? { providerCode: GEMINI_PROVIDER_CODE, type: 'google_oauth', configRevision: 3, credentials: expiredCredentials }
      : {
          providerCode: GEMINI_PROVIDER_CODE,
          type: 'google_oauth',
          configRevision: 4,
          credentials: {
            ...expiredCredentials,
            access_token: 'concurrent-gemini-access-token',
            refresh_token: 'concurrent-gemini-refresh-token',
            expires_at: '2099-01-01T00:00:00.000Z'
          }
        }
  },
  async refreshToken() {
    return {
      accessToken: 'stale-gemini-access-token',
      clientId: GEMINI_CLI_OAUTH_CLIENT_ID,
      clientSecret: 'builtin-secret',
      oauthType: 'code_assist',
      baseUrl: GEMINI_CLI_DEFAULT_BASE_URL
    }
  },
  async persistCredentials() {
    throw new AccountConfigRevisionConflictError('gemini-refresh-conflict', 3, 4)
  }
})
assert.equal(conflictResult.apiKey, 'concurrent-gemini-access-token', 'CAS 冲突后必须采用数据库中胜出的 Gemini token')
assert.equal(conflictResult.refreshToken, 'concurrent-gemini-refresh-token')

const routesSource = readFileSync(resolve('src/modules/gemini-oauth/gemini-oauth.routes.ts'), 'utf8')
const serviceSource = readFileSync(resolve('src/modules/gemini-oauth/gemini-oauth.service.ts'), 'utf8')
assertRoute(serviceSource, 'if (hasGoogleDriveMetadataScope(tokenInfo.scope))', 'Google One 只能在历史授权明确包含 Drive scope 时探测配额')
assertRoute(serviceSource, "if (tokenInfo.oauthType === 'code_assist') tierId = detected.tierId || tierId", 'Google One 项目探测不得覆盖用户选择的订阅档位')
assertRoute(routesSource, "get('/capabilities'", 'Gemini OAuth 必须暴露三模式 capabilities')
assertRoute(routesSource, "post('/auth-url'", 'Gemini OAuth 必须暴露 auth-url 接口')
assertRoute(routesSource, "post('/create-from-code'", 'Gemini OAuth 必须暴露授权码建号接口')
assertRoute(routesSource, "post('/create-from-refresh-token'", 'Gemini OAuth 必须暴露 Refresh Token 建号接口')
assertRoute(routesSource, "post('/accounts/:id/refresh-token'", 'Gemini OAuth 必须暴露手动刷新接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-code'", 'Gemini OAuth 必须暴露授权码重新授权接口')
assertRoute(routesSource, "post('/accounts/:id/reauthorize-from-refresh-token'", 'Gemini OAuth 必须暴露 Refresh Token 重新授权接口')
assertRoute(routesSource, 'oauthType: oauthTypeSchema.optional()', 'Gemini OAuth 路由必须使用 camelCase oauthType')
assertRoute(routesSource, 'projectId: optionalTrimmedTextSchema', 'Gemini OAuth 路由必须使用 camelCase projectId')
assertRoute(routesSource, 'tierId: optionalTrimmedTextSchema', 'Gemini OAuth 路由必须使用 camelCase tierId')
assertRoute(routesSource, "oauthType: accountOAuthType(current.credentials)", '账户刷新必须按锁内重读的 oauth_type 选择 OAuth client')
assertRoute(routesSource, 'expectedConfigRevision: z.number().int().min(1)', 'OAuth 凭据更新必须要求客户端配置版本')
assertRoute(routesSource, 'error instanceof AccountConfigRevisionConflictError', '并发凭据更新冲突必须返回业务冲突')
assertRoute(routesSource, 'runWithProviderOAuthRefreshLock(', 'Gemini 手动刷新与重授权必须加入跨进程共享锁')
assert.ok((routesSource.match(/current\.configRevision !== parsed\.data\.expectedConfigRevision/gu) ?? []).length >= 3, 'Gemini 手动刷新与两条重授权必须在上游请求前拦截版本变化')
assert.equal(routesSource.includes('clearAccountFailureStateAsync'), false, 'Gemini 手动刷新不得清除限流或临时不可用等业务状态')
assertRoute(routesSource, 'rotateOAuthCredentialsAsync({', 'Gemini OAuth 凭据写回必须使用用途专用窄写 repository')
assertRoute(routesSource, 'oauthRotationReceipt(updated', 'Gemini OAuth 凭据更新只返回最小 mutation 回执')
assert.equal(routesSource.includes('updateAccountAsync'), false, 'Gemini OAuth 凭据更新不得复用账户宽写')
assert.equal(routesSource.includes('sanitizeAccountCredentialCarrierResponse'), false, 'Gemini OAuth 凭据更新不得返回完整账户载体')

const appSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
assertRoute(appSource, '/my-gemini-oauth', '系统 API 必须挂载 my-gemini-oauth 自有入口')
assertRoute(appSource, '/gemini-oauth', '系统 API 必须挂载 gemini-oauth 管理员入口')
const dbAccessSource = readFileSync(resolve('src/modules/system-api/system-api-db-access.ts'), 'utf8')
assertRoute(dbAccessSource, "pattern: /^\\/(?:my-)?gemini-oauth(?:\\/.*)?$/, mode: 'read'", 'Gemini OAuth GET capabilities 必须走 read DB access')

console.log('Gemini OAuth 三模式协议契约回归通过：CLI client、redirect/scopes、PKCE、capabilities、凭据与路由均符合实现')

function assertRoute(source: string, fragment: string, message: string): void {
  assert.equal(source.includes(fragment), true, message)
}

function geminiDispatchAccount(id: string): DispatchAccountSecret {
  return {
    id,
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    systemAccountId: 'system-account',
    accountOwnerSystemAccountId: 'system-account',
    groupOwnerSystemAccountId: 'system-account',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: 'google_oauth',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'generate_content_json',
    baseUrl: GEMINI_CLI_DEFAULT_BASE_URL,
    apiKey: 'expired-gemini-access-token',
    refreshToken: 'rotating-gemini-refresh-token',
    clientId: GEMINI_CLI_OAUTH_CLIENT_ID,
    expiresAt: '2000-01-01T00:00:00.000Z',
    streamFailureCount: 0,
    credentials: { ...expiredCredentials }
  }
}
