import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const appSource = readFileSync('src/modules/system-api/system-api-app.ts', 'utf8')
const routesSource = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
const serverSource = readFileSync('src/server.ts', 'utf8')

assert.match(appSource, /my-chat.*forceSelfAccessScope.*chatRouter/, 'AI 问答路由必须挂在登录态和个人作用域之后')
for (const contract of [
  /chatRouter\.get\('\/api-keys'/,
  /chatRouter\.get\('\/conversations'/,
  /chatRouter\.post\('\/conversations'/,
  /chatRouter\.get\('\/conversations\/:conversationId\/messages'/,
  /chatRouter\.get\('\/conversations\/:conversationId\/models'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stream'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stop'/,
  /chatRouter\.delete\('\/conversations\/:conversationId'/
]) {
  assert.match(routesSource, contract)
}
assert.match(routesSource, /findApiKeySecretAsync/, '模型和发送必须在服务端按当前用户读取真实 API Key')
assert.match(routesSource, /\/v1\/chat\/completions/, 'AI 问答模型调用必须重新进入现有 Chat Completions 网关')
assert.doesNotMatch(routesSource, /baseUrl|base_url|proxyProfile/, 'AI 问答路由不能直接拼上游 Base URL 或代理配置')
assert.match(serverSource, /chatHttpProxy = createDbServiceHttpProxy\(\{ maxInFlight: 128, timeoutMs: 15 \* 60_000 \}\)/, 'AI 问答长连接必须使用独立代理池和 15 分钟超时')
assert(serverSource.indexOf('app.use(`${systemApiPrefix}/my-chat`, chatHttpProxy)') < serverSource.indexOf('app.use(systemApiPrefix, dbServiceHttpProxy)'), 'AI 问答独立代理必须挂在通用 System API 代理之前')

console.log('AI 问答路由契约回归通过')
