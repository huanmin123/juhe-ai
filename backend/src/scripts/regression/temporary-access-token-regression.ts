import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-temporary-access-token-'))
let server: http.Server | undefined
try {
  process.env.NODE_ENV = 'production'
  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_AUTH_CAPTCHA_DISABLED = 'false'
  process.env.JUHE_AI_TEMPORARY_ACCESS_IP_ALLOWLIST = '127.0.0.1'
  process.env.JUHE_AI_TRUST_PROXY = '1'
  process.env.JUHE_AI_SECRET = 'temporary-access-token-regression-secret-1234567890'
  process.env.JUHE_AI_ALLOWED_ORIGINS = 'http://127.0.0.1'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

  const repositories = await import('../../storage/repositories.js')
  const { isTemporaryAccessToken, resolveSystemAccessToken } = await import('../../modules/auth/temporary-access-token.js')
  const credentials = await repositories.verifySystemAccountCredentialsForSessionAsync('admin', 'admin')
  assert.ok(credentials, '管理员凭据应可申请临时访问令牌')
  const issued = await repositories.createTemporaryAccessTokenAsync(credentials.account.id, credentials.credentialRevision, 1)
  assert.ok(issued, '应创建临时访问令牌')
  assert.equal(isTemporaryAccessToken(issued.token), true)
  assert.deepEqual(resolveSystemAccessToken(`Bearer ${issued.token}`, undefined), {
    kind: 'token',
    access: { token: issued.token, kind: 'temporary' }
  })
  assert.deepEqual(resolveSystemAccessToken(undefined, 'cookie-session'), {
    kind: 'token',
    access: { token: 'cookie-session', kind: 'cookie' }
  })
  assert.equal(resolveSystemAccessToken('Bearer ordinary-session', 'cookie-session').kind, 'invalid')
  assert.equal(resolveSystemAccessToken('Bearer invalid-token', undefined).kind, 'invalid')
  assert.ok(await repositories.findSessionByTokenAsync(issued.token), '临时令牌应映射到管理员会话')
  await repositories.revokeSessionAsync(issued.token)
  assert.equal(await repositories.findSessionByTokenAsync(issued.token), undefined, '撤销后临时令牌不可用')

  const expiring = await repositories.createTemporaryAccessTokenAsync(credentials.account.id, credentials.credentialRevision, 1)
  assert.ok(expiring, '应能创建短 TTL 临时令牌')
  await new Promise((resolve) => setTimeout(resolve, 1100))
  assert.equal(await repositories.findSessionByTokenAsync(expiring.token), undefined, '过期后临时令牌不可用')

  const { createSystemApiApp } = await import('../../modules/system-api/system-api-app.js')
  server = await listen(createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    trustProxy: true,
    bypassSystemApiRateLimitForTest: true
  }))
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
  const deniedResponse = await fetch(`${baseUrl}/__aisys__/api/auth/temporary-access-tokens`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-forwarded-for': '203.0.113.17' },
    body: JSON.stringify({ username: 'admin', password: 'admin', ttlSeconds: 60 })
  })
  assert.equal(deniedResponse.status, 403, '非白名单来源不得申请临时令牌')
  const issueResponse = await fetch(`${baseUrl}/__aisys__/api/auth/temporary-access-tokens`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin', ttlSeconds: 60 })
  })
  assert.equal(issueResponse.status, 200, '管理员应能通过 HTTP 申请临时令牌')
  const issuedBody = await issueResponse.json() as { data?: { token?: string; tokenType?: string } }
  const httpToken = issuedBody.data?.token
  assert.ok(httpToken && isTemporaryAccessToken(httpToken), 'HTTP 响应应返回临时令牌')
  assert.equal(issuedBody.data?.tokenType, 'Bearer')

  const meResponse = await fetch(`${baseUrl}/__aisys__/api/auth/me`, {
    headers: { authorization: `Bearer ${httpToken}` }
  })
  assert.equal(meResponse.status, 200, '临时令牌应能访问当前管理员资料')
  const meBody = await meResponse.json() as { data?: { username?: string; role?: string } }
  assert.equal(meBody.data?.username, 'admin')
  assert.equal(meBody.data?.role, 'super_admin')

  const adminResponse = await fetch(`${baseUrl}/__aisys__/api/system-accounts`, {
    headers: { authorization: `Bearer ${httpToken}` }
  })
  assert.equal(adminResponse.status, 200, '临时令牌应继承管理员 RBAC')

  const revokeResponse = await fetch(`${baseUrl}/__aisys__/api/auth/temporary-access-tokens/revoke`, {
    method: 'POST',
    headers: { authorization: `Bearer ${httpToken}` }
  })
  assert.equal(revokeResponse.status, 200, '临时令牌应能撤销自身')
  const authRouteSource = readFileSync(new URL('../../modules/auth/auth.routes.ts', import.meta.url), 'utf8')
  assert.match(authRouteSource, /operationKey: 'auth\.temporary_access_token\.revoke'/u, '撤销应写入独立操作审计事件')
  assert.match(authRouteSource, /resourceId: context\.sessionId/u, '撤销审计只记录会话 ID')
  assert.doesNotMatch(authRouteSource, /resourceId: resolution\.access\.token/u, '撤销审计不得记录临时 token 原文')
  const revokedResponse = await fetch(`${baseUrl}/__aisys__/api/auth/me`, {
    headers: { authorization: `Bearer ${httpToken}` }
  })
  assert.equal(revokedResponse.status, 401, '撤销后的临时令牌不得继续访问管理接口')
  console.log('temporary-access-token-regression passed')
} finally {
  if (server) await closeServer(server)
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function listen(app: import('express').Express): Promise<http.Server> {
  return new Promise((resolve, reject) => {
    const nextServer = app.listen(0, '127.0.0.1', () => resolve(nextServer))
    nextServer.once('error', reject)
  })
}

function closeServer(target: http.Server): Promise<void> {
  return new Promise((resolve) => target.close(() => resolve()))
}

function serverAddress(target: http.Server): { port: number } {
  const address = target.address()
  assert(address && typeof address === 'object', 'HTTP 回归服务应分配监听端口')
  return { port: address.port }
}
