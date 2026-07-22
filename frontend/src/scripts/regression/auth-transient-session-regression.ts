import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { api } from '../../api/client'
import {
  authState,
  loadCurrentUser,
  login,
  logout
} from '../../composables/useAuth'
import { currentPageDataSecurityGeneration } from '../../shared/pageDataGenerationFences'
import type { CurrentUserSummary } from '../../types/domain'

const source = readFileSync(fileURLToPath(new URL('../../composables/useAuth.ts', import.meta.url)), 'utf8')
const routerSource = readFileSync(fileURLToPath(new URL('../../router/index.ts', import.meta.url)), 'utf8')
const layoutSource = readFileSync(fileURLToPath(new URL('../../layouts/AppLayout.vue', import.meta.url)), 'utf8')

assert.match(source, /catch \(error: unknown\)/, 'loadCurrentUser 必须区分明确 401 与临时错误')
assert.match(source, /isExplicitUnauthorized\(error\)/, '只有明确 401 才能清空认证状态')
assert.match(source, /return currentUser\.value/, '503 或网络错误必须保留已有登录用户')
assert.match(source, /throw error/, '冷启动遇到 503 或网络错误时必须向路由报告临时失败，不能伪装成未登录')
assert.match(source, /authStateVersion[\s\S]*requestVersion !== authStateVersion/, '迟到的认证请求不得覆盖较新的登录或退出状态')
assert.match(source, /authLoadGeneration[\s\S]*loadGeneration !== authLoadGeneration/, '较早启动的认证读取不得覆盖较新的认证读取结果')
assert.match(routerSource, /AUTH_RETRY_DELAYS_MS[\s\S]*loadCurrentUserForNavigation/, '路由认证只能执行有上限的退避重试')
assert.match(routerSource, /navigationGeneration[\s\S]*generation !== navigationGeneration/, '过期导航不得继续重试或返回恢复页')
assert.match(routerSource, /catch[\s\S]*path: '\/service-recovering'[\s\S]*redirect: to\.fullPath/, '重试耗尽后必须进入安全恢复页，不能跳登录或放行受保护页面')
assert.match(routerSource, /path: '\/service-recovering'[\s\S]*public: true/, '服务恢复页必须在认证暂不可用时仍可访问')
assert.match(routerSource, /loadCurrentUser\(true\)[\s\S]*?\.catch\(/, '强制刷新登录态必须处理临时失败')

const logoutBody = source.match(/export async function logout\(\): Promise<void> \{([\s\S]*?)\n\}/)?.[1] ?? ''
const apiLogoutIndex = logoutBody.indexOf('await api.auth.logout()')
const clearAuthIndex = logoutBody.indexOf('clearAuthState()')
assert(apiLogoutIndex >= 0 && clearAuthIndex > apiLogoutIndex, '只有服务端 logout 成功后才能清空本地登录态')
const loginBody = source.match(/export async function login\([\s\S]*?\): Promise<CurrentUserSummary> \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert(
  loginBody.indexOf('beginAuthSessionTransition(operationVersion)') >= 0
    && loginBody.indexOf('beginAuthSessionTransition(operationVersion)') < loginBody.indexOf('await api.auth.login(payload)'),
  'login 必须在请求发出前推进 session/permission generation'
)
assert(
  logoutBody.indexOf('beginAuthSessionTransition(operationVersion)') >= 0
    && logoutBody.indexOf('beginAuthSessionTransition(operationVersion)') < apiLogoutIndex,
  'logout 必须在请求发出前推进 session/permission generation，即使请求失败也保守失效'
)
assert.match(layoutSource, /try \{\s*await logout\(\)[\s\S]*router\.replace\('\/login'\)[\s\S]*catch/, '退出失败时页面必须保留当前路由并显示错误')
assert.match(layoutSource, /if \(!authState\.currentUser\.value\)[\s\S]*?router\.replace\('\/login'\)/, '服务端已退出但本地聊天清理失败时仍必须离开受保护页面')
assert.match(source, /function applyCurrentUser\(/, '认证结果必须经单一 helper 比较身份与角色变化')
assert.match(source, /advancePageDataAuthenticationGeneration\(\)/, '登录身份建立或清理时必须推进 session/permission generation')
assert.match(source, /advancePageDataPermissionGeneration\(\)/, '同一用户角色变化必须只推进 permission generation')

const originalAuthApi = { ...api.auth }
const previousUser = authState.currentUser.value
const previousAuthChecked = authState.authChecked.value
const user = (id: string, role = 'user'): CurrentUserSummary => ({
  id,
  username: id,
  displayName: id,
  role,
  mustChangePassword: false
})

try {
  authState.currentUser.value = user('existing-user')
  authState.authChecked.value = true

  const failedLoginGate = deferred<CurrentUserSummary>()
  api.auth.login = async () => failedLoginGate.promise
  const beforeFailedLogin = currentPageDataSecurityGeneration()
  const failedLogin = login({ username: 'failed-user', password: 'secret' })
  assert.equal(
    currentPageDataSecurityGeneration(),
    beforeFailedLogin + 1,
    'login 必须在网络请求完成前推进 security generation'
  )
  failedLoginGate.reject(new Error('login unavailable'))
  await assert.rejects(failedLogin, /login unavailable/)
  assert.equal(authState.currentUser.value?.id, 'existing-user', 'login 失败不得覆盖现有认证状态')

  const firstLoginGate = deferred<CurrentUserSummary>()
  const secondLoginGate = deferred<CurrentUserSummary>()
  let loginCalls = 0
  api.auth.login = async () => (++loginCalls === 1 ? firstLoginGate.promise : secondLoginGate.promise)
  const beforeConcurrentLogin = currentPageDataSecurityGeneration()
  const firstLogin = login({ username: 'first-user', password: 'secret' })
  const secondLogin = login({ username: 'second-user', password: 'secret' })
  assert.equal(
    currentPageDataSecurityGeneration(),
    beforeConcurrentLogin + 2,
    '每个并发 login 都必须在发出请求时独立推进 generation'
  )
  secondLoginGate.resolve(user('second-user'))
  await secondLogin
  firstLoginGate.resolve(user('first-user'))
  await firstLogin
  assert.equal(authState.currentUser.value?.id, 'second-user', '迟到的旧 login 不得覆盖较新的 login')

  const failedLogoutGate = deferred<void>()
  api.auth.logout = async () => failedLogoutGate.promise
  const beforeFailedLogout = currentPageDataSecurityGeneration()
  const failedLogout = logout()
  assert.equal(
    currentPageDataSecurityGeneration(),
    beforeFailedLogout + 1,
    'logout 必须在网络请求完成前推进 security generation'
  )
  failedLogoutGate.reject(new Error('logout unavailable'))
  await assert.rejects(failedLogout, /logout unavailable/)
  assert.equal(authState.currentUser.value?.id, 'second-user', 'logout 失败必须保留当前登录用户')

  authState.authChecked.value = false
  api.auth.me = async () => user('second-user', 'admin')
  const beforeRoleRefresh = currentPageDataSecurityGeneration()
  await loadCurrentUser(true)
  assert.equal(authState.currentUser.value?.role, 'admin')
  assert.equal(
    currentPageDataSecurityGeneration(),
    beforeRoleRefresh + 1,
    'loadCurrentUser 发现同用户权限变化时必须推进 permission generation'
  )

  api.auth.me = async () => { throw new Error('temporary outage') }
  const beforeTransientFailure = currentPageDataSecurityGeneration()
  assert.equal((await loadCurrentUser(true))?.id, 'second-user', '已有用户时临时认证失败必须保留当前用户')
  assert.equal(currentPageDataSecurityGeneration(), beforeTransientFailure, '临时认证失败不得伪造安全上下文变化')

  const firstMeGate = deferred<CurrentUserSummary>()
  const secondMeGate = deferred<CurrentUserSummary>()
  let meCalls = 0
  api.auth.me = async () => (++meCalls === 1 ? firstMeGate.promise : secondMeGate.promise)
  const firstMe = loadCurrentUser(true)
  const secondMe = loadCurrentUser(true)
  secondMeGate.resolve(user('newest-me'))
  await secondMe
  firstMeGate.resolve(user('stale-me'))
  await firstMe
  assert.equal(authState.currentUser.value?.id, 'newest-me', '迟到的旧 loadCurrentUser 不得覆盖较新的读取')

  api.auth.me = async () => { throw { isAxiosError: true, response: { status: 401 } } }
  const beforeUnauthorized = currentPageDataSecurityGeneration()
  assert.equal(await loadCurrentUser(true), undefined)
  assert.equal(authState.currentUser.value, undefined, '明确 401 必须清空当前用户')
  assert.equal(
    currentPageDataSecurityGeneration(),
    beforeUnauthorized + 1,
    '明确 401 清空认证状态时必须推进 generation'
  )
} finally {
  Object.assign(api.auth, originalAuthApi)
  authState.currentUser.value = previousUser
  authState.authChecked.value = previousAuthChecked
}

interface Deferred<T> {
  promise: Promise<T>
  resolve(value: T): void
  reject(error: unknown): void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

console.log('AUTH_TRANSIENT_SESSION_TEST_OK')
