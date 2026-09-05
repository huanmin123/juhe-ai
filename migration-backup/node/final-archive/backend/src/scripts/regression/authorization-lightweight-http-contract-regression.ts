import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-lightweight-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-lightweight-http-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { authorizationsRouter },
  { authorizationOptionsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  { closeSqliteReadWorkerPool },
  databaseModule,
  repositories,
  usageStatsHelpers
] = await Promise.all([
  import('../../modules/authorizations/authorizations.routes.js'),
  import('../../modules/authorization-options/authorization-options.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats-helpers.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/authorizations', requireAdmin, authorizationsRouter)
app.use('/__aisys__/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/__aisys__/api/my-authorization-options', forceSelfAccessScope, authorizationOptionsRouter)

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const admin = repositories.createSystemAccount({
    username: 'authorization_lightweight_admin',
    displayName: '轻量授权管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const owner = repositories.createSystemAccount({
    username: 'authorization_lightweight_owner',
    displayName: '轻量授权所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_lightweight_grantee',
    displayName: '轻量授权接收者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const adminAccess = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({ name: '轻量授权资源分组', providerCode: 'gpt' }, ownerAccess)
  const team = repositories.createSystemTeam({ name: '轻量授权用量团队' }, adminAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '轻量列表契约',
    expiresAt: '2099-01-01T00:00:00.000Z',
    limits: { daily: { enabled: true, limit: 5 } }
  }, ownerAccess)
  const statsDatabase = databaseModule.getStatsDatabase()
  const usageRange = usageStatsHelpers.normalizeAccountUsageStatsRange({}, await usageStatsHelpers.usageStatsTimezoneAsync())
  statsDatabase.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, '', 'all', '', 11, 110, 22, 0, 0, 0.11, NULL, '2026-01-01T00:10:00.000Z'),
             (?, ?, ?, ?, 'group', ?, 11, 110, 22, 0, 0, 0.11, '2026-01-01T00:01:00.000Z', '2026-01-01T00:10:00.000Z')
  `).run(owner.id, usageRange.startDate, usageRange.endDate, owner.id, usageRange.startDate, usageRange.endDate, team.id, group.id)
  statsDatabase.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, '', '', 'all', '', 13, 130, 26, 0, 0, 0.13, NULL, '2026-01-01T00:10:00.000Z'),
             (?, ?, ?, '', ?, 'group', ?, 13, 130, 26, 0, 0, 0.13, '2026-01-01T00:02:00.000Z', '2026-01-01T00:10:00.000Z')
  `).run(owner.id, usageRange.startDate, usageRange.endDate, owner.id, usageRange.startDate, usageRange.endDate, grantee.id, group.id)

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('授权轻量 HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const adminCookie = sessionCookie(admin.id)
  const ownerCookie = sessionCookie(owner.id)
  const granteeCookie = sessionCookie(grantee.id)

  const page = await requestEnvelope<{ items: Array<Record<string, unknown>> }>(
    baseUrl,
    '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=20',
    ownerCookie
  )
  const item = page.items.find((candidate) => candidate.resourceId === group.id)
  assert(item, '授权列表应返回新建授权')
  assert.deepEqual(Object.keys(item).sort(), [
    'createdAt',
    'effectiveSourceType',
    'expiresAt',
    'granteeSystemAccountId',
    'granteeSystemAccountName',
    'granteeType',
    'granteeUsername',
    'id',
    'limits',
    'permissions',
    'remark',
    'resourceId',
    'resourceName',
    'resourceOwnerSystemAccountId',
    'resourceOwnerSystemAccountName',
    'resourceType',
    'sourceSummary',
    'status',
    'updatedAt'
  ].sort(), '授权列表项只能返回页面展示、方向判断和操作权限所需字段')
  assert.deepEqual(Object.keys(item.permissions as Record<string, unknown>).sort(), ['canAuthorize', 'canEdit'])
  assert.deepEqual(Object.keys(item.sourceSummary as Record<string, unknown>).sort(), ['activeSourceCount', 'hasManual', 'hasTeam', 'teamSources'])

  const initialUpdatedAt = String(item.updatedAt)
  const unchanged = await requestMutationEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/${String(item.id)}/expire`,
    ownerCookie,
    { expectedUpdatedAt: initialUpdatedAt, limits: item.limits }
  )
  assert.deepEqual(Object.keys(unchanged).sort(), ['expiresAt', 'id', 'limits', 'status', 'updatedAt'].sort(), '授权 no-op 也只能返回最小 mutation result')
  assert.equal(unchanged.updatedAt, initialUpdatedAt, '授权 no-op 不得推进版本')

  const changed = await requestMutationEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/${String(item.id)}/expire`,
    ownerCookie,
    { expectedUpdatedAt: initialUpdatedAt, expiresAt: '2099-02-01T00:00:00.000Z' }
  )
  assert.deepEqual(Object.keys(changed).sort(), ['expiresAt', 'id', 'limits', 'status', 'updatedAt'].sort(), '授权字段级 PATCH 只能返回最小 mutation result')
  assert.notEqual(changed.updatedAt, initialUpdatedAt, '授权真实变化必须推进 CAS 版本')
  assert.equal(changed.expiresAt, '2099-02-01T00:00:00.000Z')

  const staleResponse = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/${String(item.id)}/expire`, {
    method: 'PATCH',
    headers: { cookie: ownerCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: initialUpdatedAt, expiresAt: '2099-03-01T00:00:00.000Z' })
  })
  assert.equal(staleResponse.status, 409, '旧授权版本必须返回 409 冲突')

  const cleared = await requestMutationEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/${String(item.id)}/expire`,
    ownerCookie,
    { expectedUpdatedAt: changed.updatedAt, expiresAt: null, limits: null }
  )
  assert.deepEqual(Object.keys(cleared).sort(), ['expiresAt', 'id', 'limits', 'status', 'updatedAt'].sort(), '清空授权字段时最小 mutation result 仍必须保留 nullable key')
  assert.equal(cleared.expiresAt, null, '清空 expiresAt 必须显式返回 null，不能被 JSON 省略')
  assert.equal(cleared.limits, null, '清空 limits 必须显式返回 null，不能被 JSON 省略')

  const groups = await requestEnvelope<Array<Record<string, unknown>>>(
    baseUrl,
    `/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=${encodeURIComponent(grantee.id)}&providerCode=gpt&limit=20`,
    ownerCookie
  )
  assert(groups.length > 0, '被授权用户应返回目标分组选项')
  for (const option of groups) {
    assert.deepEqual(Object.keys(option).sort(), ['id', 'name'], '授权目标分组选项只能返回 id/name')
  }
  const defaultGroup = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1 LIMIT 1")
    .get(grantee.id) as unknown as { id?: string } | undefined
  assert.equal(groups[0]?.id, defaultGroup?.id, '目标分组仅返回 id/name 后仍应通过排序把默认分组放在首项')

  const teamSummary = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/usage/team-summary?startDate=${usageRange.startDate}&endDate=${usageRange.endDate}`,
    ownerCookie
  )
  assert.deepEqual(Object.keys(teamSummary).sort(), ['range', 'summary'], '团队独立摘要响应只能返回 range/summary')
  assert.deepEqual(Object.keys(teamSummary.summary as Record<string, unknown>).sort(), ['cacheWriteTokens', 'inputTokens', 'requestCount', 'totalCost', 'totalTokens'].sort(), '团队 summary 内层必须保持五个必填字段并仅按需携带 lastUsedAt')
  assert.equal((teamSummary.summary as { requestCount?: number }).requestCount, 11, 'self 团队汇总必须经过 worker 读取当前用户窗口')
  const teamRows = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/usage/team-details?startDate=${usageRange.startDate}&endDate=${usageRange.endDate}&page=1&pageSize=20`,
    ownerCookie
  )
  assert.deepEqual(Object.keys(teamRows).sort(), ['hasMore', 'page', 'pageSize', 'range', 'rows', 'total'].sort(), '团队明细必须固定返回严格 rows DTO')
  assert.equal((teamRows.rows as unknown[]).length, 1, 'self 团队 rows 必须经过 worker 返回非空明细')
  const teamRow = (teamRows.rows as Array<Record<string, unknown>>)[0] ?? {}
  assert.deepEqual(Object.keys(teamRow.usage as Record<string, unknown>).sort(), ['requestCount', 'totalCost', 'totalTokens'].sort(), '团队 row usage 必须保持三字段窄投影')

  const userSummary = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/usage/user-summary?startDate=${usageRange.startDate}&endDate=${usageRange.endDate}`,
    ownerCookie
  )
  assert.deepEqual(Object.keys(userSummary).sort(), ['range', 'summary'], '用户独立摘要响应只能返回 range/summary')
  assert.deepEqual(Object.keys(userSummary.summary as Record<string, unknown>).sort(), ['cacheWriteTokens', 'inputTokens', 'requestCount', 'totalCost', 'totalTokens'].sort(), '用户 summary 内层必须保持五个必填字段并仅按需携带 lastUsedAt')
  assert.equal((userSummary.summary as { requestCount?: number }).requestCount, 13, 'self 用户汇总必须经过 worker 读取当前用户窗口')
  const userRows = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/usage/user-details?startDate=${usageRange.startDate}&endDate=${usageRange.endDate}&page=1&pageSize=20`,
    ownerCookie
  )
  assert.deepEqual(Object.keys(userRows).sort(), ['hasMore', 'page', 'pageSize', 'range', 'rows', 'total'].sort(), '用户明细必须固定返回严格 rows DTO')
  assert.equal((userRows.rows as unknown[]).length, 1, 'self 用户 rows 必须经过 worker 返回非空明细')
  const userRow = (userRows.rows as Array<Record<string, unknown>>)[0] ?? {}
  assert.deepEqual(Object.keys(userRow.usage as Record<string, unknown>).sort(), ['requestCount', 'totalCost', 'totalTokens'].sort(), '用户 row usage 必须保持三字段窄投影')

  const forgedSelfSummary = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/usage/team-summary?systemAccountId=${encodeURIComponent(grantee.id)}&startDate=${usageRange.startDate}&endDate=${usageRange.endDate}`,
    ownerCookie
  )
  assert.equal((forgedSelfSummary.summary as { requestCount?: number }).requestCount, 11, 'self 接口必须忽略伪造 owner scope')

  const adminSummary = await requestEnvelope<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/authorizations/usage/user-summary?systemAccountId=${encodeURIComponent(owner.id)}&startDate=${usageRange.startDate}&endDate=${usageRange.endDate}`,
    adminCookie
  )
  assert.equal((adminSummary.summary as { requestCount?: number }).requestCount, 13, 'admin 接口必须按显式 owner scope 读取窗口')
  const forbiddenManagement = await fetch(`${baseUrl}/__aisys__/api/authorizations/usage/team-summary`, { headers: { cookie: ownerCookie } })
  assert.equal(forbiddenManagement.status, 403, '普通用户不得访问管理端授权用量接口')

  const invalidLegacyRows = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/usage/team-details?includeSummary=false`, { headers: { cookie: ownerCookie } })
  assert.equal(invalidLegacyRows.status, 400, 'rows 接口必须拒绝已废弃的 includeSummary 开关')
  const invalidTeamGrantee = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/usage/team-summary?granteeSystemAccountId=${encodeURIComponent(grantee.id)}`, { headers: { cookie: ownerCookie } })
  assert.equal(invalidTeamGrantee.status, 400, 'team 接口必须拒绝不适用的 granteeSystemAccountId')
  const invalidOwnerAlias = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/usage/user-summary?resourceOwnerSystemAccountId=${encodeURIComponent(owner.id)}`, { headers: { cookie: ownerCookie } })
  assert.equal(invalidOwnerAlias.status, 400, '授权用量接口必须拒绝未实现的 owner alias')
  const invalidResourcePair = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/usage/user-summary?resourceId=${encodeURIComponent(group.id)}`, { headers: { cookie: ownerCookie } })
  assert.equal(invalidResourcePair.status, 400, 'resourceId 缺少 resourceType 时必须返回 400')

  const invalidSummary = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/usage/team-summary?page=2`, { headers: { cookie: ownerCookie } })
  assert.equal(invalidSummary.status, 400, '独立摘要接口必须拒绝分页参数')

  const mutationGroup = repositories.createGroup({ name: '轻量授权 mutation 分组', providerCode: 'gpt' }, ownerAccess)
  const createPayload = {
    resourceType: 'group',
    resourceId: mutationGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: 'mutation 幂等契约'
  }
  const createdMutation = await requestJson<Record<string, unknown>>(
    baseUrl,
    '/__aisys__/api/my-authorizations',
    ownerCookie,
    'POST',
    createPayload,
    201
  )
  assert.deepEqual(Object.keys(createdMutation).sort(), ['created', 'item'], 'create mutation 只能返回 created/item 和按需出现的 previousStatus')
  assert.equal(createdMutation.created, true, '首次创建必须标记 created=true')
  const createdItem = createdMutation.item as Record<string, unknown>
  const mutationPage = await requestEnvelope<{ items: Array<Record<string, unknown>> }>(
    baseUrl,
    '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=20',
    ownerCookie
  )
  const listedMutationItem = mutationPage.items.find((candidate) => candidate.id === createdItem.id)
  assert(listedMutationItem, 'create mutation 对应授权必须能出现在列表中')
  assert.deepEqual(createdItem, listedMutationItem, 'create mutation item 必须与同一授权的列表窄 DTO 完全一致')

  const duplicateCapture = await captureAuthorizationDml(
    () => repositories.createResourceAuthorizationMutationAsync(createPayload, ownerAccess) as unknown as Promise<Record<string, unknown>>
  )
  const duplicateMutation = duplicateCapture.result
  assert.deepEqual(Object.keys(duplicateMutation).sort(), ['created', 'item'], '重复 active 同值创建不得增加额外响应字段')
  assert.equal(duplicateMutation.created, false, '重复 active 同值创建必须成为幂等 no-op')
  assert.equal((duplicateMutation.item as Record<string, unknown>).updatedAt, createdItem.updatedAt, '重复 active 同值创建不得推进版本')
  assert.equal(duplicateCapture.dmlCount, 0, '重复 active 同值创建必须为 0 DML')

  const revoked = await requestJson<Record<string, unknown>>(
    baseUrl,
    `/__aisys__/api/my-authorizations/${String(createdItem.id)}`,
    ownerCookie,
    'DELETE',
    { expectedUpdatedAt: createdItem.updatedAt },
    200
  )
  assert.deepEqual(Object.keys(revoked).sort(), ['id', 'status', 'updatedAt'], 'revoke 只能返回终态最小回执')
  assert.equal(revoked.status, 'revoked')
  assert.notEqual(revoked.updatedAt, createdItem.updatedAt, '真实 revoke 必须推进 CAS 版本')

  const staleRevoke = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/${String(createdItem.id)}`, {
    method: 'DELETE',
    headers: { cookie: ownerCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: createdItem.updatedAt })
  })
  assert.equal(staleRevoke.status, 409, '终态授权收到旧 revoke 版本仍必须先返回 409')

  const noOpRevokeCapture = await captureAuthorizationDml(() => requestJson<Record<string, unknown>>(
      baseUrl,
      `/__aisys__/api/my-authorizations/${String(createdItem.id)}`,
      ownerCookie,
      'DELETE',
      { expectedUpdatedAt: revoked.updatedAt },
      200
    )
  )
  const noOpRevoke = noOpRevokeCapture.result
  assert.equal(noOpRevoke.updatedAt, revoked.updatedAt, '同版本重复 revoke 必须保持版本不变')
  assert.equal(noOpRevokeCapture.dmlCount, 0, '同版本重复 revoke 必须为 0 DML')

  const reactivatedMutation = await repositories.createResourceAuthorizationMutationAsync(createPayload, ownerAccess) as unknown as Record<string, unknown>
  assert.equal(reactivatedMutation.created, false, '终态复用不得伪装成新记录')
  assert.equal(reactivatedMutation.previousStatus, 'revoked', '终态复用必须返回 previousStatus 供日志说明')
  const reactivatedItem = reactivatedMutation.item as Record<string, unknown>
  assert.equal(reactivatedItem.id, createdItem.id, '重新授权必须复用稳定授权 ID')
  assert.notEqual(reactivatedItem.updatedAt, revoked.updatedAt, '重新激活必须推进 CAS 版本')

  const returned = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/${String(createdItem.id)}/return`, {
    method: 'DELETE',
    headers: { cookie: granteeCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: reactivatedItem.updatedAt })
  })
  assert.equal(returned.status, 204, 'return 成功应使用无正文最小回执')
  const returnedVersion = String((databaseModule.getBusinessDatabase()
    .prepare('SELECT updated_at FROM resource_authorization_grants WHERE id = ?')
    .get(String(createdItem.id)) as { updated_at?: string } | undefined)?.updated_at)
  assert.notEqual(returnedVersion, reactivatedItem.updatedAt, '真实 return 必须推进 CAS 版本')

  const staleReturn = await fetch(`${baseUrl}/__aisys__/api/my-authorizations/${String(createdItem.id)}/return`, {
    method: 'DELETE',
    headers: { cookie: granteeCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt: reactivatedItem.updatedAt })
  })
  assert.equal(staleReturn.status, 409, '终态授权收到旧 return 版本仍必须先返回 409')
  const noOpReturnCapture = await captureAuthorizationDml(() => fetch(`${baseUrl}/__aisys__/api/my-authorizations/${String(createdItem.id)}/return`, {
      method: 'DELETE',
      headers: { cookie: granteeCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ expectedUpdatedAt: returnedVersion })
    })
  )
  const noOpReturn = noOpReturnCapture.result
  assert.equal(noOpReturn.status, 204, '同版本重复 return 必须成为成功 no-op')
  assert.equal(noOpReturnCapture.dmlCount, 0, '同版本重复 return 必须为 0 DML')
  const returnedVersionAfterNoOp = (databaseModule.getBusinessDatabase()
    .prepare('SELECT updated_at FROM resource_authorization_grants WHERE id = ?')
    .get(String(createdItem.id)) as { updated_at?: string } | undefined)?.updated_at
  assert.equal(returnedVersionAfterNoOp, returnedVersion, '重复 return 不得推进版本')
  assertMutationQueriesAreNarrow()

  console.log('授权轻量 HTTP 契约回归通过：列表、create/revoke/return mutation、目标选项与用量均保持窄契约和 CAS/no-op')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.getStatsDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function requestEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as ApiEnvelope<T>
  return payload.data as T
}

async function requestMutationEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as ApiEnvelope<T>
  return payload.data as T
}

async function requestJson<T>(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'POST' | 'DELETE',
  body: Record<string, unknown>,
  expectedStatus: number
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${method} ${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data as T
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function assertMutationQueriesAreNarrow(): void {
  const writeSource = readFileSync(resolve(process.cwd(), 'src/storage/resource-authorization-write.repository.ts'), 'utf8')
  const returnSource = readFileSync(resolve(process.cwd(), 'src/storage/resource-authorization-return.repository.ts'), 'utf8')
  const routeSource = readFileSync(resolve(process.cwd(), 'src/modules/authorizations/authorizations.routes.ts'), 'utf8')
  for (const name of [
    'upsertResourceAuthorizationGrantMutation',
    'upsertResourceAuthorizationGrantMutationAsync',
    'revokeResourceAuthorizationMutationSqlite',
    'revokeResourceAuthorizationMutationPostgresAsync'
  ]) {
    assert.doesNotMatch(functionSource(writeSource, name), /SELECT\s+\*/i, `${name} 不得退回 SELECT *`)
  }
  for (const name of ['findDirectGrantForReturnMutation', 'findDirectGrantForReturnMutationAsync']) {
    assert.doesNotMatch(functionSource(returnSource, name), /SELECT\s+\*/i, `${name} 不得退回 SELECT *`)
  }
  assert.match(functionSource(writeSource, 'revokeResourceAuthorizationMutationSqlite'), /resource_owner_system_account_id\s*=\s*\?/, 'SQLite revoke 定位 SQL 必须下推 owner 条件')
  assert.match(functionSource(writeSource, 'revokeResourceAuthorizationMutationPostgresAsync'), /resource_owner_system_account_id\s*=\s*\?/, 'PostgreSQL revoke 锁行 SQL 必须下推 owner 条件')
  assert.match(functionSource(returnSource, 'findDirectGrantForReturnMutation'), /grantee_system_account_id\s*=\s*\?/, 'SQLite return 定位 SQL 必须下推 grantee 条件')
  assert.match(functionSource(returnSource, 'findDirectGrantForReturnMutationAsync'), /grantee_system_account_id\s*=\s*\?/, 'PostgreSQL return 锁行 SQL 必须下推 grantee 条件')
  assert.match(functionSource(writeSource, 'resourceAuthorizationCreateResourceContextAsync'), /LIMIT\s+1\s+FOR\s+UPDATE/i, 'PostgreSQL create 必须先锁资源行串行化不存在 grant 的并发创建')
  const createRoute = routeSource.slice(routeSource.indexOf("authorizationsRouter.post('/', mutationGuard"), routeSource.indexOf("authorizationsRouter.get('/:id'"))
  for (const field of ['remark', 'expiresAt', 'limits']) {
    assert.match(createRoute, new RegExp(`${field}:\\s*bodyField\\(req, '${field}'\\)`), `create mutationGuard 指纹必须包含 ${field}`)
  }
}

async function captureAuthorizationDml<T>(run: () => Promise<T>): Promise<{ result: T; dmlCount: number }> {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let dmlCount = 0
  database.prepare = ((sql: string) => {
    if (/^\s*(?:UPDATE|INSERT|DELETE)\s+(?:resource_authorization_grants|resource_authorizations|resource_authorization_sources|group_accounts|accounts)\b/i.test(sql)) {
      dmlCount += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    return { result: await run(), dmlCount }
  } finally {
    database.prepare = originalPrepare
  }
}

function functionSource(source: string, name: string): string {
  const start = source.indexOf(`function ${name}`)
  assert(start >= 0, `应存在 ${name}`)
  const remaining = source.slice(start + 10)
  const nextMatch = /\n(?:export\s+)?(?:async\s+)?function\s+/.exec(remaining)
  const end = nextMatch ? start + 10 + nextMatch.index : source.length
  return source.slice(start, end)
}
