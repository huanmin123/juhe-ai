import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import { http } from '@/api/http'
import type { AuthSessionListResult, AuthSessionRevokeResult, CurrentUserSummary, SystemAccountListResult, SystemAccountPrincipalSummary, SystemAccountSummary } from '@/types/domain'

type HttpMethod = 'get' | 'post' | 'patch' | 'delete'

interface HttpCall {
  method: HttpMethod
  url: string
  payload?: unknown
  config?: unknown
}

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../../..')
const calls: HttpCall[] = []
const httpMethods = http as unknown as Record<HttpMethod, (...args: unknown[]) => Promise<unknown>>
const originalHttpMethods = {
  get: httpMethods.get,
  post: httpMethods.post,
  patch: httpMethods.patch,
  delete: httpMethods.delete
}

installHttpStubs()

try {
  await assertAuthApiContract()
  await assertSystemAccountApiContract()
  assertSourceBoundaries()
} finally {
  Object.assign(httpMethods, originalHttpMethods)
}

console.log('W3 前端 auth / 系统账户 smoke 通过：会话管理、系统账户写入口和敏感字段边界已固定')

async function assertAuthApiContract(): Promise<void> {
  calls.length = 0
  const captcha = await api.auth.captcha()
  assert.equal(captcha.captchaId, 'captcha_w3_frontend')
  assertLastCall('get', '/auth/captcha', undefined, undefined, '验证码应走 Go W3 captcha 路径')

  await api.auth.login({
    username: 'admin',
    password: 'LoginPass123',
    captchaId: 'captcha_w3_frontend',
    captchaCode: '1234'
  })
  assertLastCall('post', '/auth/login', {
    username: 'admin',
    password: 'LoginPass123',
    captchaId: 'captcha_w3_frontend',
    captchaCode: '1234'
  }, undefined, '登录应提交验证码和密码到 auth/login')

  await api.auth.me()
  assertLastCall('get', '/auth/me', undefined, undefined, '当前用户读取应走 auth/me')

  await api.auth.updateProfile({ displayName: '管理员' })
  assertLastCall('patch', '/auth/me', { displayName: '管理员' }, undefined, '显示名修改应 PATCH auth/me')

  await api.auth.changePassword({ oldPassword: 'old-pass', newPassword: 'new-pass' })
  assertLastCall('post', '/auth/change-password', { oldPassword: 'old-pass', newPassword: 'new-pass' }, undefined, '改密应 POST auth/change-password')

  const sessions = await api.auth.sessions({ page: 2, pageSize: 10 })
  assert.equal(sessions.items[0]?.current, true, '会话列表应保留 current 标记')
  assertLastCall('get', '/auth/sessions', undefined, { params: { page: 2, pageSize: 10 } }, '会话列表应带 page/pageSize 查询参数')

  const revoke = await api.auth.revokeSession('sess/current')
  assert.equal(revoke.revoked, true, '撤销响应应保留 revoked 标记')
  assertLastCall('delete', '/auth/sessions/sess%2Fcurrent', undefined, undefined, '会话撤销应编码 session ID 后拼接路径')

  await api.auth.logout()
  assertLastCall('post', '/auth/logout', undefined, undefined, '登出应走 auth/logout')
}

async function assertSystemAccountApiContract(): Promise<void> {
  calls.length = 0
  await api.systemAccounts.listPage({ keyword: ' 管理员 ', page: 3, pageSize: 20 })
  assertLastCall('get', '/system-accounts', undefined, { params: { keyword: '管理员', page: 3, pageSize: 20 } }, '系统账户列表应 trim keyword 并分页')

  await api.systemAccounts.options({ ids: ['sys_a', 'sys_b'], keyword: ' 用户 ', limit: 10 })
  assertLastCall('get', '/system-accounts/options', undefined, { params: { ids: 'sys_a,sys_b', keyword: '用户', limit: 10 } }, '系统账户 options 应压缩 ids 并 trim keyword')

  const createPayload = {
    username: 'created-user',
    displayName: '新用户',
    description: '前端 smoke',
    password: 'CreatePass123',
    role: 'user',
    status: 'active',
    mustChangePassword: true,
    imageGenerationEnabled: true
  }
  await api.systemAccounts.create(createPayload)
  assertLastCall('post', '/system-accounts', createPayload, undefined, '系统账户创建应只提交表单字段，不提交默认资源 ID')

  const updatePayload = {
    displayName: '更新用户',
    description: '更新说明',
    role: 'admin',
    status: 'active',
    mustChangePassword: false,
    imageGenerationEnabled: false
  }
  await api.systemAccounts.update('sys_target', updatePayload)
  assertLastCall('patch', '/system-accounts/sys_target', updatePayload, undefined, '系统账户编辑应走统一 mixed PATCH')
}

function assertSourceBoundaries(): void {
  const authSource = readSource('src/api/domains/auth.ts')
  const identitySource = readSource('src/types/domain/identity.ts')
  const appHeaderSource = readSource('src/layouts/AppHeader.vue')
  const appLayoutSource = readSource('src/layouts/AppLayout.vue')
  const sessionModalSource = readSource('src/layouts/SessionManagementModal.vue')
  const systemAccountsViewSource = readSource('src/views/system-accounts/SystemAccountsView.vue')
  const frontendPackageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }

  assertIncludes(authSource, "http.get('/auth/sessions'", 'auth API 必须暴露当前用户会话列表')
  assertIncludes(authSource, 'http.delete(`/auth/sessions/${encodeURIComponent(sessionId)}`', 'auth API 必须编码 session ID 后撤销会话')

  assertIncludes(appHeaderSource, 'key="sessions">会话管理', '头像菜单必须提供会话管理入口')
  assertIncludes(appLayoutSource, '<SessionManagementModal', '应用壳必须挂载会话管理弹窗')
  assertIncludes(appLayoutSource, 'const result = await api.auth.sessions', '会话弹窗加载必须调用 auth.sessions')
  assertIncludes(appLayoutSource, 'const result = await api.auth.revokeSession(session.id)', '会话撤销必须调用 auth.revokeSession')
  assertIncludes(appLayoutSource, 'clearAuthState()', '撤销当前会话后必须清理本地登录态')
  assertIncludes(sessionModalSource, '撤销当前会话后需要重新登录', '撤销当前会话必须有明确确认文案')
  assertIncludes(sessionModalSource, 'formatDateTime(record.lastSeenAt)', '会话列表必须展示最近活跃时间')

  assertIncludes(systemAccountsViewSource, 'canManageSystemAccounts = computed(() => isSuperAdminRole', '系统账户写入口必须只对 super_admin 展示')
  assertIncludes(systemAccountsViewSource, 'await api.systemAccounts.create(payload)', '系统账户页必须调用 create API')
  assertIncludes(systemAccountsViewSource, 'await api.systemAccounts.update(editingId.value, basePayload)', '系统账户页编辑必须走统一 update API')
  assertIncludes(systemAccountsViewSource, 'await api.systemAccounts.update(resettingId.value, { password: resetPassword.value', '系统账户页重置密码必须走统一 update API')
  assertIncludes(systemAccountsViewSource, 'mustChangePassword: isAdminRole(form.role) ? false : form.mustChangePassword', '管理员账户创建/编辑必须关闭初始改密提醒')

  for (const forbidden of ['passwordHash', 'tokenHash', 'DefaultAPIKey', 'defaultAPIKey', 'keySecret', 'key_secret']) {
    assertNotIncludes(identitySource, forbidden, `前端 identity 类型不应暴露敏感字段：${forbidden}`)
    assertNotIncludes(systemAccountsViewSource, forbidden, `系统账户页不应引用敏感字段：${forbidden}`)
    assertNotIncludes(sessionModalSource, forbidden, `会话弹窗不应引用敏感字段：${forbidden}`)
  }

  assert.equal(
    frontendPackageJson.scripts?.['test:w3-auth-system-account-smoke'],
    'pnpm --dir ../backend exec tsx --tsconfig ../frontend/tsconfig.json ../frontend/src/scripts/regression/w3-auth-system-account-smoke-regression.ts',
    '前端 package script 应暴露 W3 auth / 系统账户 smoke'
  )
}

function installHttpStubs(): void {
  httpMethods.get = async (url, config) => recordCall('get', String(url), undefined, config)
  httpMethods.post = async (url, payload, config) => recordCall('post', String(url), payload, config)
  httpMethods.patch = async (url, payload, config) => recordCall('patch', String(url), payload, config)
  httpMethods.delete = async (url, config) => recordCall('delete', String(url), undefined, config)
}

async function recordCall(method: HttpMethod, url: string, payload?: unknown, config?: unknown): Promise<unknown> {
  calls.push({ method, url, payload, config })
  return { data: { data: fixtureFor(method, url) } }
}

function fixtureFor(method: HttpMethod, url: string): unknown {
  if (method === 'get' && url === '/auth/captcha') {
    return { captchaId: 'captcha_w3_frontend', image: 'data:image/png;base64,AA==', expiresAt: '2026-07-09T10:00:00Z' }
  }
  if ((method === 'get' && url === '/auth/me') || (method === 'post' && url === '/auth/login') || (method === 'patch' && url === '/auth/me') || (method === 'post' && url === '/auth/change-password')) {
    return currentUserFixture()
  }
  if (method === 'post' && url === '/auth/logout') {
    return { loggedOut: true }
  }
  if (method === 'get' && url === '/auth/sessions') {
    return sessionListFixture()
  }
  if (method === 'delete' && url.startsWith('/auth/sessions/')) {
    return { id: decodeURIComponent(url.slice('/auth/sessions/'.length)), revoked: true, current: true } satisfies AuthSessionRevokeResult
  }
  if (method === 'get' && url === '/system-accounts') {
    return systemAccountListFixture()
  }
  if (method === 'get' && url === '/system-accounts/options') {
    return [{ id: 'sys_target', username: 'created-user', displayName: '新用户', status: 'active' }] satisfies SystemAccountPrincipalSummary[]
  }
  if ((method === 'post' && url === '/system-accounts') || (method === 'patch' && url === '/system-accounts/sys_target')) {
    return systemAccountFixture()
  }
  throw new Error(`未覆盖的 HTTP stub：${method.toUpperCase()} ${url}`)
}

function currentUserFixture(): CurrentUserSummary {
  return {
    id: 'sys_current',
    username: 'admin',
    displayName: '管理员',
    role: 'super_admin',
    mustChangePassword: false
  }
}

function sessionListFixture(): AuthSessionListResult {
  return {
    items: [{
      id: 'sess/current',
      current: true,
      createdAt: '2026-07-09T09:00:00Z',
      lastSeenAt: '2026-07-09T09:30:00Z',
      expiresAt: '2026-07-23T09:00:00Z'
    }],
    total: 11,
    hasMore: true,
    page: 2,
    pageSize: 10
  }
}

function systemAccountListFixture(): SystemAccountListResult {
  return {
    items: [systemAccountFixture()],
    total: 1,
    hasMore: false,
    page: 3,
    pageSize: 20
  }
}

function systemAccountFixture(): SystemAccountSummary {
  return {
    id: 'sys_target',
    username: 'created-user',
    displayName: '新用户',
    description: '前端 smoke',
    role: 'user',
    status: 'active',
    mustChangePassword: true,
    imageGenerationEnabled: true,
    createdAt: '2026-07-09T09:00:00Z',
    updatedAt: '2026-07-09T09:00:00Z'
  }
}

function assertLastCall(method: HttpMethod, url: string, payload: unknown, config: unknown, message: string): void {
  const call = calls.at(-1)
  assert(call, `${message}，未记录到 HTTP 调用`)
  assert.equal(call.method, method, message)
  assert.equal(call.url, url, message)
  assert.deepEqual(call.payload, payload, message)
  assert.deepEqual(call.config, config, message)
}

function readSource(relativePath: string): string {
  return readFileSync(resolve(frontendRoot, relativePath), 'utf8')
}

function assertIncludes(source: string, marker: string, message: string): void {
  assert(source.includes(marker), message)
}

function assertNotIncludes(source: string, marker: string, message: string): void {
  assert.equal(source.includes(marker), false, message)
}
