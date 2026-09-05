import assert from 'node:assert/strict'
import { createHash, randomBytes } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import { renameSync, writeFileSync } from 'node:fs'
import { once } from 'node:events'
import { relative, resolve } from 'node:path'

import { createLocalJWKSet, decodeProtectedHeader, jwtVerify, type JWK } from 'jose'

import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { createSession } from '../../storage/repositories.js'

const providerPort = requiredPort('JUHE_AI_EXTERNAL_CLIENT_E2E_PROVIDER_PORT')
const readyFile = requiredEnvironment('JUHE_AI_EXTERNAL_CLIENT_E2E_READY_FILE')
const timeoutMs = 90_000

assertIsolatedE2EEnvironment()

const providerBaseUrl = `http://[::1]:${providerPort}`
const authorizationState = randomBytes(24).toString('base64url')
const pkceVerifier = randomBytes(48).toString('base64url')
const pkceChallenge = createHash('sha256').update(pkceVerifier).digest('base64url')
const nonce = randomBytes(24).toString('base64url')
const browserLoginTicket = randomBytes(32).toString('base64url')

let providerServer: Server | undefined
let clientServer: Server | undefined
let timeout: NodeJS.Timeout | undefined
let resolveFlow: (() => void) | undefined
let rejectFlow: ((error: Error) => void) | undefined
let callbackHandled = false
let clientId = ''
let redirectUri = ''
let browserSessionToken = ''
let browserLoginTicketUsed = false
let completed = false

try {
  getBusinessDatabase()
  browserSessionToken = createSession('sys_admin', 1).token
  const providerApp = createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    publicApiPrefix: '/__aipublic__',
    bypassSystemApiRateLimitForTest: true
  })
  providerApp.get('/__e2e__/browser-login', (request, response) => {
    const ticket = typeof request.query.ticket === 'string' ? request.query.ticket : ''
    const redirect = typeof request.query.redirect === 'string' ? request.query.redirect : ''
    if (
      browserLoginTicketUsed
      || ticket !== browserLoginTicket
      || !redirect.startsWith(`${providerBaseUrl}/oauth/authorize?`)
    ) {
      response.status(404).send('Not found')
      return
    }
    browserLoginTicketUsed = true
    response.setHeader('Set-Cookie', `juhe_ai_session=${browserSessionToken}; Path=/; HttpOnly; SameSite=Lax`)
    response.redirect(302, redirect)
  })
  providerServer = providerApp.listen(providerPort, '::1')
  await once(providerServer, 'listening')

  clientServer = createServer((request, response) => {
    void handleClientRequest(request.url ?? '/', request.headers.cookie, response)
  }).listen(0, '127.0.0.1')
  await once(clientServer, 'listening')
  const callbackPort = serverPort(clientServer)
  redirectUri = `http://127.0.0.1:${callbackPort}/callback`

  const client = await createPublicClient(redirectUri)
  clientId = client.clientId
  const startUrl = `http://127.0.0.1:${callbackPort}/start`
  const flowCompletion = new Promise<void>((resolve, reject) => {
    resolveFlow = resolve
    rejectFlow = reject
    timeout = setTimeout(() => reject(new Error('等待浏览器完成第三方 Client 授权超时')), timeoutMs)
    timeout.unref()
  })
  const temporaryReadyFile = `${readyFile}.tmp`
  writeFileSync(temporaryReadyFile, `${JSON.stringify({
    startUrl,
    providerBaseUrl,
    clientId,
    redirectUri
  })}\n`, 'utf8')
  renameSync(temporaryReadyFile, readyFile)
  process.stdout.write(`E2E_READY ${startUrl}\n`)

  await flowCompletion
  assert(callbackHandled, '客户端必须实际接收到浏览器回调')
  console.log('oidc-public-client-browser-e2e: passed')
  completed = true
} finally {
  if (timeout) clearTimeout(timeout)
  await closeServer(clientServer)
  await closeServer(providerServer)
  closeStorageDatabases()
}

if (completed) process.exit(0)

async function createPublicClient(redirectUri: string): Promise<{ clientId: string }> {
  const response = await fetch(`${providerBaseUrl}/__aisys__/api/oauth/clients`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      // The external client is public, but registration is an isolated administrator action.
      cookie: `juhe_ai_session=${browserSessionToken}`
    },
    body: JSON.stringify({
      displayName: 'External browser E2E public client',
      clientType: 'public',
      redirectUris: [redirectUri],
      allowedScopes: ['openid', 'profile', 'juhe:profile.read', 'juhe:request_limits.read']
    })
  })
  const payload = await response.json() as { data?: { clientId?: string }, message?: string }
  assert.equal(response.status, 201, `第三方 Client 注册失败：${payload.message ?? '未知错误'}`)
  assert(payload.data?.clientId, '第三方 Client 注册响应缺少 clientId')
  return { clientId: payload.data.clientId }
}

async function handleClientRequest(path: string, cookieHeader: string | undefined, response: import('node:http').ServerResponse): Promise<void> {
  if (path === '/start') {
    const authorizationUrl = new URL(`${providerBaseUrl}/oauth/authorize`)
    authorizationUrl.search = new URLSearchParams({
      response_type: 'code',
      client_id: clientId,
      redirect_uri: redirectUri,
      scope: 'openid profile juhe:profile.read juhe:request_limits.read',
      state: authorizationState,
      nonce,
      code_challenge: pkceChallenge,
      code_challenge_method: 'S256'
    }).toString()
    const loginUrl = new URL(`${providerBaseUrl}/__e2e__/browser-login`)
    loginUrl.search = new URLSearchParams({ ticket: browserLoginTicket, redirect: authorizationUrl.toString() }).toString()
    response.writeHead(302, { location: loginUrl.toString(), 'cache-control': 'no-store' })
    response.end()
    return
  }

  if (path.startsWith('/callback')) {
    if (callbackHandled) {
      response.writeHead(409, { 'content-type': 'text/plain; charset=utf-8' })
      response.end('回调已处理')
      return
    }
    callbackHandled = true
    try {
      assert(
        !cookieHeader?.split(';').some((cookie) => cookie.trim().startsWith('juhe_ai_session=')),
        'Provider 会话 Cookie 不得发送给外部 Client 回调地址'
      )
      const callback = new URL(path, clientBaseUrl())
      const code = callback.searchParams.get('code')
      const state = callback.searchParams.get('state')
      assert(code, '浏览器回调缺少授权码')
      assert.equal(state, authorizationState, '浏览器回调 state 不匹配')
      const result = await exchangeAndRead(code)
      response.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' })
      response.end(`<!doctype html><title>外部 Client 验证成功</title><main><h1>外部 Client 验证成功</h1><p>已完成 PKCE 授权、ID Token 验签、userinfo、个人资料和请求限额读取。</p><ul><li>用户：${escapeHtml(result.username)}</li><li>显示名：${escapeHtml(result.displayName)}</li><li>限额窗口：${result.requestLimitWindows}</li></ul></main>`)
      resolveFlow?.()
    } catch (error) {
      const message = error instanceof Error ? error.message : '未知错误'
      response.writeHead(500, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' })
      response.end(`<!doctype html><title>外部 Client 验证失败</title><main><h1>外部 Client 验证失败</h1><p>${escapeHtml(message)}</p></main>`)
      rejectFlow?.(error instanceof Error ? error : new Error(message))
    }
    return
  }

  response.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' })
  response.end('Not found')
}

async function exchangeAndRead(code: string): Promise<{ username: string, displayName: string, requestLimitWindows: number }> {
  const tokenResponse = await fetch(`${providerBaseUrl}/oauth/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: clientId,
      code,
      redirect_uri: redirectUri,
      code_verifier: pkceVerifier
    })
  })
  const token = await tokenResponse.json() as { access_token?: string, id_token?: string, token_type?: string, message?: string }
  assert.equal(tokenResponse.status, 200, `PKCE 换 Token 失败：${token.message ?? '未知错误'}`)
  assert.equal(token.token_type, 'Bearer', 'Token 类型必须为 Bearer')
  assert(token.access_token && token.id_token, 'Token 响应必须包含 access_token 和 id_token')

  const discoveryResponse = await fetch(`${providerBaseUrl}/.well-known/openid-configuration`)
  const discovery = await discoveryResponse.json() as { issuer?: string, jwks_uri?: string, userinfo_endpoint?: string }
  assert.equal(discoveryResponse.status, 200, 'Discovery 获取失败')
  assert.equal(discovery.issuer, providerBaseUrl, 'Discovery issuer 不匹配')
  assert(discovery.jwks_uri && discovery.userinfo_endpoint, 'Discovery 缺少 JWKS 或 UserInfo 地址')
  const jwksResponse = await fetch(discovery.jwks_uri)
  const jwks = await jwksResponse.json() as { keys?: JWK[] }
  assert.equal(jwksResponse.status, 200, 'JWKS 获取失败')
  assert(jwks.keys?.length, 'JWKS 未返回公钥')
  const verified = await jwtVerify(token.id_token, createLocalJWKSet({ keys: jwks.keys }), {
    issuer: providerBaseUrl,
    audience: clientId
  })
  assert(verified.payload.sub, 'ID Token 缺少稳定 sub')
  assert.equal(verified.payload.nonce, nonce, 'ID Token nonce 不匹配')
  assert.equal(decodeProtectedHeader(token.id_token).alg, 'RS256', 'ID Token 必须使用 RS256')

  const headers = { authorization: `Bearer ${token.access_token}` }
  const userinfoResponse = await fetch(discovery.userinfo_endpoint, { headers })
  const userinfo = await userinfoResponse.json() as { sub?: string, name?: string, preferred_username?: string }
  assert.equal(userinfoResponse.status, 200, 'UserInfo 读取失败')
  assert(userinfo.sub && userinfo.name && userinfo.preferred_username, 'UserInfo 缺少已授权的身份字段')
  assert.equal(userinfo.sub, verified.payload.sub, 'UserInfo sub 必须与 ID Token sub 一致')

  const profileResponse = await fetch(`${providerBaseUrl}/__aidelegated__/v1/profile`, { headers })
  const profile = await profileResponse.json() as { data?: { username?: string, displayName?: string } }
  assert.equal(profileResponse.status, 200, '个人资料读取失败')
  assert.equal(profile.data?.username, userinfo.preferred_username, '个人资料与 UserInfo 用户名不一致')
  assert.equal(profile.data?.displayName, userinfo.name, '个人资料与 UserInfo 显示名不一致')

  const requestLimitsResponse = await fetch(`${providerBaseUrl}/__aidelegated__/v1/request-limits`, { headers })
  const requestLimits = await requestLimitsResponse.json() as {
    data?: {
      windows?: Record<string, {
        limit?: unknown
        limitMode?: unknown
        usageTracked?: unknown
        used?: unknown
        remaining?: unknown
        source?: unknown
        resetsAt?: unknown
      }>
      usageStatus?: unknown
    }
  }
  assert.equal(requestLimitsResponse.status, 200, '请求限额读取失败')
  const requestLimitWindows = requestLimits.data?.windows
  const expectedWindowNames = ['perMinute', 'perDay', 'perWeek', 'perMonth']
  assert(
    requestLimitWindows
      && expectedWindowNames.every((windowName) => Object.hasOwn(requestLimitWindows, windowName)),
    '请求限额响应缺少每分钟、每日、每周或每月窗口快照'
  )
  assert(
    requestLimits.data?.usageStatus === 'estimated'
      || requestLimits.data?.usageStatus === 'unavailable'
      || requestLimits.data?.usageStatus === 'not_tracked',
    '请求限额响应的 usageStatus 无效'
  )
  for (const windowName of expectedWindowNames) {
    const window = requestLimitWindows?.[windowName]
    assert(window && typeof window === 'object', `${windowName} 限额窗口不是对象`)
    assert('limit' in window, `${windowName} 缺少 limit`)
    assert(window.limitMode === 'limited' || window.limitMode === 'unlimited', `${windowName} 的 limitMode 无效`)
    assert(typeof window.usageTracked === 'boolean', `${windowName} 的 usageTracked 无效`)
    assert('used' in window && 'remaining' in window, `${windowName} 缺少 used 或 remaining`)
    assert(window.source === 'global' || window.source === 'user', `${windowName} 的 source 无效`)
    assert(window.resetsAt === null || typeof window.resetsAt === 'string', `${windowName} 的 resetsAt 无效`)
  }

  return {
    username: profile.data.username ?? '',
    displayName: profile.data.displayName ?? '',
    requestLimitWindows: expectedWindowNames.length
  }
}

function clientBaseUrl(): string {
  return redirectUri.replace('/callback', '')
}

function requiredPort(name: string): number {
  const value = Number(requiredEnvironment(name))
  if (!Number.isInteger(value) || value < 1 || value > 65535) throw new Error(`${name} 必须是有效端口`)
  return value
}

function requiredEnvironment(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`缺少 ${name}`)
  return value
}

function assertIsolatedE2EEnvironment(): void {
  assert.equal(process.env.JUHE_AI_EXTERNAL_CLIENT_E2E_CHILD, '1', 'E2E Client 必须经隔离启动器运行')
  assert.equal(process.env.NODE_ENV, 'test', 'E2E Client 只能在 NODE_ENV=test 下运行')
  assert.equal(process.env.JUHE_AI_PROCESS_ROLE, 'db-service', 'E2E Client 必须使用 db-service 角色')
  assert.equal(process.env.JUHE_AI_LOG_CONSOLE_ENABLED, 'false', 'E2E Client 必须关闭控制台日志')
  assert.equal(process.env.JUHE_AI_LOG_FILE_ENABLED, 'false', 'E2E Client 必须关闭文件日志')
  assert.equal(process.env.JUHE_AI_AUDIT_LOG_ENABLED, 'false', 'E2E Client 必须关闭审计日志')

  const temporaryRoot = requiredEnvironment('JUHE_AI_EXTERNAL_CLIENT_E2E_TEMP_ROOT')
  const isolatedPaths = [
    'JUHE_AI_DATABASE_PATH',
    'JUHE_AI_CHAT_DATABASE_PATH',
    'JUHE_AI_DATASET_DATABASE_PATH',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
    'JUHE_AI_STATS_DATABASE_PATH',
    'JUHE_AI_AUDIT_LOG_DATABASE_PATH',
    'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY',
    'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY',
    'JUHE_AI_LOG_DIR',
    'JUHE_AI_USAGE_SHARD_ROOT',
    'JUHE_AI_CODEX_CONTEXT_ROOT',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT',
    'JUHE_AI_EXTERNAL_CLIENT_E2E_READY_FILE'
  ]
  for (const name of isolatedPaths) {
    assertPathWithinTemporaryRoot(requiredEnvironment(name), temporaryRoot, name)
  }
}

function assertPathWithinTemporaryRoot(path: string, temporaryRoot: string, name: string): void {
  const pathRelativeToRoot = relative(resolve(temporaryRoot), resolve(path))
  assert(
    pathRelativeToRoot && !pathRelativeToRoot.startsWith('..') && !pathRelativeToRoot.includes(':\\'),
    `${name} 必须位于隔离临时目录内`
  )
}

function serverPort(server: Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('客户端监听地址无效')
  return address.port
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server) return
  server.closeIdleConnections?.()
  server.closeAllConnections?.()
  await Promise.race([
    new Promise<void>((resolve) => server.close(() => resolve())),
    waitForServerCloseGracePeriod()
  ])
  server.unref()
}

function waitForServerCloseGracePeriod(): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, 1_000)
    timer.unref()
  })
}

function escapeHtml(value: string): string {
  const entities: Record<string, string> = {
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }
  return value.replace(/[&<>"']/g, (character) => entities[character] ?? character)
}
