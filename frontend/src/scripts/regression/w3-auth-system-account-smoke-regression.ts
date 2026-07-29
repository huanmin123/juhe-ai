import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { api } from '@/api/client'
import { http } from '@/api/http'
import type { CurrentUserSummary, SystemAccountListResult, SystemAccountPrincipalSummary, SystemAccountSummary } from '@/types/domain'

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

console.log('W3 前端 auth / 系统账户 smoke 通过：会话管理能力已移除，系统账户写入口和敏感字段边界已固定')

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

  assert.equal('sessions' in api.auth, false, 'auth API 不应继续暴露会话列表能力')
  assert.equal('revokeSession' in api.auth, false, 'auth API 不应继续暴露会话撤销能力')

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
  const contractsSource = readSource('src/api/contracts.ts')
  const paramsSource = readSource('src/api/params.ts')
  const appHeaderSource = readSource('src/layouts/AppHeader.vue')
  const appLayoutSource = readSource('src/layouts/AppLayout.vue')
  const systemAccountsViewSource = readSource('src/views/system-accounts/SystemAccountsView.vue')
  const frontendPackageJson = JSON.parse(readSource('package.json')) as { scripts?: Record<string, string> }

  assertNotIncludes(authSource, '/auth/sessions', 'auth API 不应保留会话列表或撤销端点')
  assertNotIncludes(contractsSource, 'AuthSessionListParams', 'API 契约不应保留会话列表参数')
  assertNotIncludes(paramsSource, 'authSessionListParams', 'API 参数层不应保留会话列表参数转换')
  assertNotIncludes(identitySource, 'AuthSession', '前端领域类型不应保留会话对象')
  assertNotIncludes(appHeaderSource, 'key="sessions"', '头像菜单不应保留会话管理入口')
  assertNotIncludes(appHeaderSource, '会话管理', '头像菜单不应保留会话管理文案')
  assertNotIncludes(appLayoutSource, 'SessionManagementModal', '应用壳不应挂载会话管理弹窗')
  assertNotIncludes(appLayoutSource, 'api.auth.sessions', '应用壳不应加载登录会话')
  assertNotIncludes(appLayoutSource, 'api.auth.revokeSession', '应用壳不应撤销登录会话')
  assert.equal(
    existsSync(resolve(frontendRoot, 'src/layouts/SessionManagementModal.vue')),
    false,
    '会话管理弹窗组件必须删除'
  )

  assertIncludes(systemAccountsViewSource, 'canManageSystemAccounts = computed(() => isSuperAdminRole', '系统账户写入口必须只对 super_admin 展示')
  assertIncludes(systemAccountsViewSource, 'await api.systemAccounts.create(payload)', '系统账户页必须调用 create API')
  assertIncludes(systemAccountsViewSource, 'buildSystemAccountEditablePatch(editingBaseline.value, editableValues)', '系统账户页编辑必须构造字段级 delta')
  assertIncludes(systemAccountsViewSource, 'expectedUpdatedAt: editingVersion.value', '系统账户页编辑必须携带列表 CAS 版本')
  assertIncludes(systemAccountsViewSource, 'expectedUpdatedAt: resettingVersion.value', '系统账户页重置密码必须携带列表 CAS 版本')
  assertIncludes(systemAccountsViewSource, 'mustChangePassword: isAdminRole(form.role) ? false : form.mustChangePassword', '管理员账户创建/编辑必须关闭初始改密提醒')

  for (const forbidden of ['passwordHash', 'tokenHash', 'DefaultAPIKey', 'defaultAPIKey', 'keySecret', 'key_secret']) {
    assertNotIncludes(identitySource, forbidden, `前端 identity 类型不应暴露敏感字段：${forbidden}`)
    assertNotIncludes(systemAccountsViewSource, forbidden, `系统账户页不应引用敏感字段：${forbidden}`)
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
