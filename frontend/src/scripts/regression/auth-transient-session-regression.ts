import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const source = readFileSync(fileURLToPath(new URL('../../composables/useAuth.ts', import.meta.url)), 'utf8')
const routerSource = readFileSync(fileURLToPath(new URL('../../router/index.ts', import.meta.url)), 'utf8')
const layoutSource = readFileSync(fileURLToPath(new URL('../../layouts/AppLayout.vue', import.meta.url)), 'utf8')

assert.match(source, /catch \(error: unknown\)/, 'loadCurrentUser 必须区分明确 401 与临时错误')
assert.match(source, /isExplicitUnauthorized\(error\)/, '只有明确 401 才能清空认证状态')
assert.match(source, /return currentUser\.value/, '503 或网络错误必须保留已有登录用户')
assert.match(source, /throw error/, '冷启动临时失败必须报告给路由')
assert.match(source, /authStateVersion[\s\S]*requestVersion !== authStateVersion/, '迟到认证请求不得覆盖较新状态')
assert.match(source, /authLoadGeneration[\s\S]*loadGeneration !== authLoadGeneration/, '较早认证读取不得覆盖较新读取')
assert.match(routerSource, /AUTH_RETRY_DELAYS_MS[\s\S]*loadCurrentUserForNavigation/, '路由认证必须有界退避')
assert.match(routerSource, /path: '\/service-recovering'[\s\S]*public: true/, '恢复页必须公开可访问')
assert.match(routerSource, /loadCurrentUser\(true\)[\s\S]*?\.catch\(/, '强制刷新必须处理临时失败')
const logoutBody = source.match(/export async function logout\(\): Promise<void> \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert(logoutBody.indexOf('clearAuthState()') > logoutBody.indexOf('await api.auth.logout()'), '只有服务端 logout 成功后才能清空本地状态')
assert.match(layoutSource, /try \{\s*await logout\(\)[\s\S]*catch/, '退出失败必须保留页面并报告')

console.log('AUTH_TRANSIENT_SESSION_TEST_OK')
