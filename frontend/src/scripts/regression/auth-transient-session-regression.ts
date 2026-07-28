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
const logoutVersionGuardIndex = logoutBody.indexOf('operationVersion !== authStateVersion')
const logoutVersionGuards = logoutBody.match(/operationVersion !== authStateVersion/g) ?? []
const chatCleanupIndex = logoutBody.indexOf('await clearCurrentAccountChatState(systemAccountId)')
const clearAuthIndex = logoutBody.indexOf('clearAuthState()')
assert(apiLogoutIndex >= 0 && clearAuthIndex > apiLogoutIndex, '只有服务端 logout 成功后才能清空本地登录态')
assert(
  logoutVersionGuardIndex > apiLogoutIndex && chatCleanupIndex > logoutVersionGuardIndex && clearAuthIndex > chatCleanupIndex,
  'logout 返回后必须先确认仍是当前认证操作，再按 chat cleanup → clear state 顺序清理旧账户'
)
assert.equal(logoutVersionGuards.length, 2, '聊天清理期间认证操作变化时也不得清空较新的登录态')
const loginBody = source.match(/export async function login\([\s\S]*?\): Promise<CurrentUserSummary> \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert(loginBody.indexOf('const operationVersion = advanceAuthStateVersion()') < loginBody.indexOf('await api.auth.login(payload)'), 'login 必须在请求发出前建立认证操作版本')
assert.match(layoutSource, /try \{\s*await logout\(\)[\s\S]*router\.replace\('\/login'\)[\s\S]*catch/, '退出失败时页面必须保留当前路由并显示错误')
assert.match(layoutSource, /if \(!authState\.currentUser\.value\)[\s\S]*?router\.replace\('\/login'\)/, '服务端已退出但本地聊天清理失败时仍必须离开受保护页面')
assert.match(source, /function applyCurrentUser\([\s\S]*previous\.id !== user\.id[\s\S]*previous\.role !== user\.role/, '认证结果必须经单一 helper 比较身份与角色变化')
assert.match(source, /function advanceAuthStateVersion\(\): number \{\s*authStateVersion \+= 1\s*return authStateVersion/, '并发认证版本不得把失败请求误计为已生效的认证 revision')

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
  const failedLogin = login({ username: 'failed-user', password: 'secret' })
  failedLoginGate.reject(new Error('login unavailable'))
  await assert.rejects(failedLogin, /login unavailable/)
  assert.equal(authState.currentUser.value?.id, 'existing-user', 'login 失败不得覆盖现有认证状态')

  const firstLoginGate = deferred<CurrentUserSummary>()
  const secondLoginGate = deferred<CurrentUserSummary>()
  let loginCalls = 0
  api.auth.login = async () => (++loginCalls === 1 ? firstLoginGate.promise : secondLoginGate.promise)
  const firstLogin = login({ username: 'first-user', password: 'secret' })
  const secondLogin = login({ username: 'second-user', password: 'secret' })
  secondLoginGate.resolve(user('second-user'))
  await secondLogin
  firstLoginGate.resolve(user('first-user'))
  await firstLogin
  assert.equal(authState.currentUser.value?.id, 'second-user', '迟到的旧 login 不得覆盖较新的 login')

  const staleLogoutGate = deferred<void>()
  const replacementLoginGate = deferred<CurrentUserSummary>()
  api.auth.logout = async () => staleLogoutGate.promise
  api.auth.login = async () => replacementLoginGate.promise
  const staleLogout = logout()
  const replacementLogin = login({ username: 'replacement-user', password: 'secret' })
  replacementLoginGate.resolve(user('replacement-user'))
  await replacementLogin
  staleLogoutGate.resolve()
  await staleLogout
  assert.equal(
    authState.currentUser.value?.id,
    'replacement-user',
    '迟到的旧 logout 不得清除较新的 login 状态'
  )

  const failedLogoutGate = deferred<void>()
  api.auth.logout = async () => failedLogoutGate.promise
  const failedLogout = logout()
  failedLogoutGate.reject(new Error('logout unavailable'))
  await assert.rejects(failedLogout, /logout unavailable/)
  assert.equal(authState.currentUser.value?.id, 'replacement-user', 'logout 失败必须保留当前登录用户')

  authState.authChecked.value = false
  api.auth.me = async () => user('replacement-user', 'admin')
  await loadCurrentUser(true)
  assert.equal(authState.currentUser.value?.role, 'admin')

  api.auth.me = async () => { throw new Error('temporary outage') }
  assert.equal((await loadCurrentUser(true))?.id, 'replacement-user', '已有用户时临时认证失败必须保留当前用户')

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
  assert.equal(await loadCurrentUser(true), undefined)
  assert.equal(authState.currentUser.value, undefined, '明确 401 必须清空当前用户')
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
