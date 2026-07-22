import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  OPENAI_OAUTH_AUTHORIZE_URL,
  OPENAI_OAUTH_CLIENT_ID,
  OPENAI_OAUTH_DEFAULT_REDIRECT_URI,
  OPENAI_OAUTH_DEFAULT_SCOPES,
  OPENAI_OAUTH_TOKEN_URL,
  buildOpenAIOAuthAuthorizeUrl,
  buildOpenAIOAuthCredentials,
  buildOpenAIOAuthTokenHttpRequest,
  extractCodeAndState,
  openAIOAuthMemorySessionMaxEntries,
  openAIOAuthOwnerSessionMaxEntries,
  openAIOAuthTokenRequestTimeoutMs,
  openAIOAuthTokenResponseMaxBytes,
  parseOpenAIOAuthExpiresIn
} from '../../modules/openai-oauth/openai-oauth.service.js'

interface OAuthMigrationGolden {
  version: number
  authority: string
  transport: {
    mode: string
    mounts: Array<{ prefix: string; scope: string }>
    routerEndpoints: Array<{
      method: string
      path: string
      successStatus: number
      mutationGuardOperationKey?: string
    }>
    absentDedicatedEndpoints: string[]
  }
  authorization: {
    authorizeUrl: string
    tokenUrl: string
    clientId: string
    redirectUri: string
    scopes: string
    query: Record<string, string>
  }
  session: {
    ttlSeconds: number
    globalMaxEntries: number
    ownerMaxEntries: number
    capacityScope: string
    stateBytes: number
    codeVerifierBytes: number
    sessionIdBytes: number
    consume: string
    ownerBound: boolean
  }
  tokenExchange: {
    authorizationCodeContentType: string
    refreshTokenContentType: string
    refreshHeaders: Record<string, string>
    timeoutMs: number
    responseMaxBytes: number
    expiresIn: string
    refreshLeadSeconds: number
  }
  idempotency: {
    authUrl: string
    codeSession: string
    guardedOperations: Array<{ operationKey: string; processingTtlMs: number }>
    unguardedOperations: string[]
  }
  persistence: {
    createOrder: string[]
    createdAccount: Record<string, unknown>
    credentialKeys: string[]
    response: string
    reauthorize: string[]
  }
  errorStatusScope: string
  errors: Array<{ operation: string; statuses: number[] }>
  knownNodeDefects: Array<{
    id: string
    currentBehavior: string
    goMigration: string
    disposition: string
  }>
}

const fixturePath = resolve('..', 'testdata', 'openai-oauth-contract', 'v1', 'node-authority.json')
const golden = JSON.parse(readFileSync(fixturePath, 'utf8')) as OAuthMigrationGolden

assert.equal(golden.version, 1)
assert.equal(golden.authority, 'node')

const routesSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.routes.ts'), 'utf8')
const serviceSource = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.service.ts'), 'utf8')
const runtimeStateStoreSource = readFileSync(resolve('src/shared/runtime-state-store.ts'), 'utf8')
const appSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
const frontendApiSource = readFileSync(resolve('..', 'frontend/src/api/domains/openaiOAuth.ts'), 'utf8')

const actualRouterEndpoints = [...routesSource.matchAll(/openAIOAuthRouter\.post\('([^']+)'/g)]
  .map((match) => match[1])
assert.deepEqual(
  actualRouterEndpoints,
  golden.transport.routerEndpoints.map((endpoint) => endpoint.path),
  'Node OAuth POST 路由发生漂移时必须显式更新迁移 golden'
)
for (const endpoint of golden.transport.routerEndpoints) {
  assert.equal(endpoint.method, 'POST', `${endpoint.path} 当前只允许 POST`)
  if (!endpoint.mutationGuardOperationKey) continue
  assert.match(
    routesSource,
    new RegExp(`post\\('${escapeRegExp(endpoint.path)}', mutationGuard\\(\\{[\\s\\S]*?operationKey: '${escapeRegExp(endpoint.mutationGuardOperationKey)}'`),
    `${endpoint.path} 必须保留当前 mutation guard operation key`
  )
}
for (const guarded of golden.idempotency.guardedOperations) {
  assert.match(
    routesSource,
    new RegExp(`operationKey: '${escapeRegExp(guarded.operationKey)}'[\\s\\S]*?processingTtlMs: ${String(guarded.processingTtlMs).replace(/000$/, '_000')}`),
    `${guarded.operationKey} 的幂等处理租约发生漂移`
  )
}
assert.equal(golden.idempotency.authUrl, 'new_session_per_request')
assert.equal(golden.idempotency.codeSession, 'atomic_compare_delete_once')
for (const operation of golden.idempotency.unguardedOperations) {
  assert.doesNotMatch(routesSource, new RegExp(`post\\('[^']+', mutationGuard\\(\\{[\\s\\S]*?operationKey: '${escapeRegExp(operation)}'`))
}

assert.deepEqual(golden.transport.mounts, [
  { prefix: '/__aisys__/api/my-openai-oauth', scope: 'authenticated_self' },
  { prefix: '/__aisys__/api/openai-oauth', scope: 'admin' }
])
assert.match(appSource, /app\.use\(`\$\{systemApiPrefix\}\/my-openai-oauth`, forceSelfAccessScope, openAIOAuthRouter\)/)
assert.match(appSource, /app\.use\(`\$\{systemApiPrefix\}\/openai-oauth`, requireAdmin, openAIOAuthRouter\)/)
assert.equal(golden.transport.mode, 'manual_callback_url_submission')
for (const endpoint of golden.transport.absentDedicatedEndpoints) {
  assert.doesNotMatch(routesSource, new RegExp(`openAIOAuthRouter\\.(?:get|post|patch|delete)\\('${escapeRegExp(endpoint)}'`))
  assert.doesNotMatch(frontendApiSource, new RegExp(escapeRegExp(endpoint)))
}

assert.equal(OPENAI_OAUTH_AUTHORIZE_URL, golden.authorization.authorizeUrl)
assert.equal(OPENAI_OAUTH_TOKEN_URL, golden.authorization.tokenUrl)
assert.equal(OPENAI_OAUTH_CLIENT_ID, golden.authorization.clientId)
assert.equal(OPENAI_OAUTH_DEFAULT_REDIRECT_URI, golden.authorization.redirectUri)
assert.equal(OPENAI_OAUTH_DEFAULT_SCOPES, golden.authorization.scopes)
const authorizeUrl = new URL(buildOpenAIOAuthAuthorizeUrl({
  clientId: 'golden-client',
  redirectUri: 'http://localhost:1455/auth/callback',
  state: 'golden-state',
  codeChallenge: 'golden-challenge'
}))
assert.deepEqual(Object.fromEntries(authorizeUrl.searchParams), {
  ...golden.authorization.query,
  client_id: 'golden-client',
  redirect_uri: 'http://localhost:1455/auth/callback',
  scope: golden.authorization.scopes,
  state: 'golden-state',
  code_challenge: 'golden-challenge'
})

assert.equal(golden.session.globalMaxEntries, openAIOAuthMemorySessionMaxEntries)
assert.equal(golden.session.ownerMaxEntries, openAIOAuthOwnerSessionMaxEntries)
assert.equal(golden.session.capacityScope, 'memory_only_redis_has_no_global_or_owner_cap')
assert.equal(golden.session.ttlSeconds % 60, 0)
assert.match(serviceSource, new RegExp(`const sessionTtlMs = ${golden.session.ttlSeconds / 60} \\* 60 \\* 1000`))
assert.match(serviceSource, new RegExp(`randomBytes\\(${golden.session.stateBytes}\\)\\.toString\\('hex'\\)`))
assert.match(serviceSource, new RegExp(`randomBytes\\(${golden.session.codeVerifierBytes}\\)\\.toString\\('hex'\\)`))
assert.match(serviceSource, new RegExp(`randomBytes\\(${golden.session.sessionIdBytes}\\)\\.toString\\('hex'\\)`))
assert.equal(golden.session.consume, 'atomic_compare_delete_once')
assert.equal(golden.session.ownerBound, true)
assert.match(serviceSource, /if \(expectedOwner && actualOwner !== expectedOwner\)/)
assert.match(serviceSource, /compareDeleteJson\(input\.sessionId, session\)/)
assert.match(serviceSource, /const session = await consumeOpenAIOAuthSession\(input\)[\s\S]*?const tokenInfo = await requestOpenAIToken/)

const codeRequest = buildOpenAIOAuthTokenHttpRequest({
  grant_type: 'authorization_code',
  client_id: 'golden-client',
  code: 'golden-code',
  redirect_uri: 'http://localhost:1455/auth/callback',
  code_verifier: 'golden-verifier'
})
assert.equal(codeRequest.headers['content-type'], golden.tokenExchange.authorizationCodeContentType)
const refreshRequest = buildOpenAIOAuthTokenHttpRequest({
  grant_type: 'refresh_token',
  refresh_token: 'golden-refresh',
  client_id: 'golden-client'
})
assert.equal(refreshRequest.headers['content-type'], golden.tokenExchange.refreshTokenContentType)
for (const [name, value] of Object.entries(golden.tokenExchange.refreshHeaders)) {
  assert.equal(refreshRequest.headers[name], value)
}
assert.equal(openAIOAuthTokenRequestTimeoutMs, golden.tokenExchange.timeoutMs)
assert.equal(openAIOAuthTokenResponseMaxBytes, golden.tokenExchange.responseMaxBytes)
assert.equal(parseOpenAIOAuthExpiresIn('3600'), 3600)
assert.throws(() => parseOpenAIOAuthExpiresIn(0), /有限正数/)
assert.equal(golden.tokenExchange.expiresIn, 'finite_positive_integer_seconds')
assert.match(serviceSource, new RegExp(`expiresAt - Date\\.now\\(\\) < ${golden.tokenExchange.refreshLeadSeconds}_000`))

assert.deepEqual(extractCodeAndState({
  callbackUrl: 'http://localhost:1455/auth/callback?code=golden-code&state=golden-state'
}), { code: 'golden-code', state: 'golden-state' })
assert.throws(() => extractCodeAndState({ callbackUrl: 'http://localhost:1455/auth/callback?code=only-code' }), /code 和 state/)

const credentials = buildOpenAIOAuthCredentials({
  accessToken: 'golden-access',
  refreshToken: 'golden-refresh',
  idToken: 'golden-id',
  expiresIn: 3600,
  expiresAt: '2030-01-01T01:00:00.000Z',
  clientId: 'golden-client',
  email: 'golden@example.com',
  accountId: 'golden-account',
  chatgptUserId: 'golden-user',
  planType: 'golden-plan'
})
assert.deepEqual(Object.keys(credentials).sort(), [...golden.persistence.credentialKeys].sort())
assert.deepEqual(golden.persistence.createOrder, [
  'validate_request_and_scope',
  'validate_provider_group_and_policies',
  'exchange_or_refresh_token',
  'create_account_and_operation_log',
  'dispatch_pending_health_check',
  'return_sanitized_account'
])
assert.deepEqual(golden.persistence.createdAccount, {
  providerCode: 'gpt',
  type: 'oauth',
  status: 'pending_test',
  schedulable: false
})
assert.match(routesSource, /const tokenInfo = await exchangeOpenAIAuthCode[\s\S]*?const account = await runLoggedOperationAsync[\s\S]*?createAccountAsync\(\{[\s\S]*?providerCode: GPT_VENDOR_CODE,[\s\S]*?type: 'oauth',[\s\S]*?status: 'pending_test',[\s\S]*?schedulable: false/)
assert.match(routesSource, /dispatchPendingAccountHealthCheck\(account\)[\s\S]*?res\.status\(201\)\.json\(ok\(sanitizeAccountResponse\(account\)\)\)/)
assert.equal(golden.persistence.response, 'sanitized_account_envelope')
assert.deepEqual(golden.persistence.reauthorize, [
  'preserve_existing_non_token_credentials',
  'overwrite_server_token_fields',
  'update_credentials_only',
  'clear_only_refresh_failure_state'
])
assert.match(routesSource, /return \{[\s\S]*?\.\.\.currentCredentials,[\s\S]*?\.\.\.buildOpenAIOAuthCredentials\(tokenInfo, fallback\)[\s\S]*?\}/)
assert.match(routesSource, /updateAccountAsync\(account\.id, \{[\s\S]*?credentials[\s\S]*?\}, access\)/)

assert.equal(
  golden.errorStatusScope,
  'explicit_route_handler_branches_only_excludes_middleware_and_uncaught_async_errors'
)
assert.deepEqual(golden.errors, [
  { operation: 'auth_url', statuses: [200, 400, 500] },
  { operation: 'create_from_code', statuses: [201, 400, 409, 502] },
  { operation: 'create_from_refresh_token', statuses: [201, 400, 409, 502] },
  { operation: 'refresh_token', statuses: [200, 400, 404, 502] },
  { operation: 'reauthorize_from_code', statuses: [200, 400, 404, 409, 502] },
  { operation: 'reauthorize_from_refresh_token', statuses: [200, 400, 404, 409, 502] }
])
const errorOperations: Record<string, string> = {
  auth_url: '/auth-url',
  create_from_code: '/create-from-code',
  create_from_refresh_token: '/create-from-refresh-token',
  refresh_token: '/accounts/:id/refresh-token',
  reauthorize_from_code: '/accounts/:id/reauthorize-from-code',
  reauthorize_from_refresh_token: '/accounts/:id/reauthorize-from-refresh-token'
}
const accountUpdateErrorSource = routesSource.slice(
  routesSource.indexOf('function handleOAuthAccountUpdateError'),
  routesSource.indexOf('function oauthErrorMessage')
)
for (const error of golden.errors) {
  const path = errorOperations[error.operation]
  const start = routesSource.indexOf(`openAIOAuthRouter.post('${path}'`)
  const nextStart = routesSource.indexOf('openAIOAuthRouter.post(', start + 1)
  const block = routesSource.slice(start, nextStart < 0 ? routesSource.length : nextStart)
  const statuses = new Set<number>([...block.matchAll(/res\.status\((\d+)\)/g)].map((match) => Number(match[1])))
  if (/res\.json\(/.test(block)) statuses.add(200)
  if (/next\(error\)/.test(block)) statuses.add(500)
  if (/handleOAuthAccountUpdateError/.test(block)) {
    for (const match of accountUpdateErrorSource.matchAll(/res\.status\((\d+)\)/g)) statuses.add(Number(match[1]))
  }
  assert.deepEqual([...statuses].sort((a, b) => a - b), [...error.statuses].sort((a, b) => a - b), `${error.operation} HTTP 状态发生漂移`)
}

assert.ok(golden.knownNodeDefects.length >= 2, '明确 Node 缺陷必须单列，不能混入 Go 必须兼容的契约')
for (const defect of golden.knownNodeDefects) {
  assert.equal(defect.disposition, 'fix_in_go_do_not_copy')
  assert.ok(defect.currentBehavior.trim())
  assert.ok(defect.goMigration.trim())
}
assert.ok(golden.knownNodeDefects.some((defect) => defect.id === 'session-consumed-before-token-success'))
assert.ok(golden.knownNodeDefects.some((defect) => defect.id === 'no-stable-machine-error-code'))
assert.ok(golden.knownNodeDefects.some((defect) => defect.id === 'reauthorize-refresh-token-without-idempotency-or-cas'))
assert.ok(golden.knownNodeDefects.some((defect) => defect.id === 'oauth-session-plaintext-in-redis'))
assert.ok(golden.knownNodeDefects.some((defect) => defect.id === 'redis-session-capacity-unbounded'))

assert.match(serviceSource, /createRuntimeStateStore\('openai-oauth:sessions'\)/)
const redisRuntimeStateStoreStart = runtimeStateStoreSource.indexOf('class RedisRuntimeStateStore')
assert.ok(redisRuntimeStateStoreStart >= 0)
const runtimeStateSetStart = runtimeStateStoreSource.indexOf('async setJson<T>', redisRuntimeStateStoreStart)
const runtimeStateSetEnd = runtimeStateStoreSource.indexOf('\n  async compareSetJson<T>', runtimeStateSetStart)
assert.ok(runtimeStateSetStart >= 0 && runtimeStateSetEnd > runtimeStateSetStart)
const runtimeStateSetBlock = runtimeStateStoreSource.slice(runtimeStateSetStart, runtimeStateSetEnd)
assert.match(runtimeStateSetBlock, /JSON\.stringify\(value\)/)
assert.doesNotMatch(runtimeStateSetBlock, /(?:encrypt|seal|aead)/i)
assert.doesNotMatch(runtimeStateSetBlock, /(?:maxEntries|maxOwnerSessions|ownerSystemAccountId)/)

const refreshTokenReauthorizeStart = routesSource.indexOf("openAIOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token'")
const refreshTokenReauthorizeEnd = routesSource.indexOf('\ntype OpenAIOAuthProvider', refreshTokenReauthorizeStart)
assert.ok(refreshTokenReauthorizeStart >= 0 && refreshTokenReauthorizeEnd > refreshTokenReauthorizeStart)
const refreshTokenReauthorizeBlock = routesSource.slice(refreshTokenReauthorizeStart, refreshTokenReauthorizeEnd)
assert.doesNotMatch(refreshTokenReauthorizeBlock, /mutationGuard\(/)
const credentialUpdateStart = routesSource.indexOf('async function updateOpenAIOAuthAccountCredentials')
const credentialUpdateEnd = routesSource.indexOf('\nexport function buildReauthorizedOpenAIOAuthCredentials', credentialUpdateStart)
assert.ok(credentialUpdateStart >= 0 && credentialUpdateEnd > credentialUpdateStart)
const credentialUpdateBlock = routesSource.slice(credentialUpdateStart, credentialUpdateEnd)
assert.match(credentialUpdateBlock, /updateAccountAsync\(account\.id/)
assert.doesNotMatch(credentialUpdateBlock, /configRevision|expectedRevision|ExpectedConfigRevision/)

console.log('OpenAI OAuth Node->Go 迁移 golden 通过：实际 HTTP、PKCE/session、token、错误、幂等与落库边界已冻结')

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
