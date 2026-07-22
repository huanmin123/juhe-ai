import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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

console.log('AUTH_TRANSIENT_SESSION_TEST_OK')
