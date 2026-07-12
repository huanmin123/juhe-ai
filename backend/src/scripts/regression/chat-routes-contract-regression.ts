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
  /chatRouter\.get\('\/conversations\/:conversationId'/,
  /chatRouter\.patch\('\/conversations\/:conversationId'/,
  /chatRouter\.get\('\/conversations\/:conversationId\/models'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stream'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stop'/,
  /chatRouter\.delete\('\/conversations\/:conversationId'/
]) {
  assert.match(routesSource, contract)
}
assert.match(routesSource, /findApiKeySecretAsync/, '模型和发送必须在服务端按当前用户读取真实 API Key')
assert.match(routesSource, /buildChatTransportRequest/, 'AI 问答模型调用必须通过 Chat\/Responses transport 重新进入现有网关')
const duplicateLookupIndex = routesSource.indexOf('await findChatTurnByClientMessageId')
const gatewayValidationIndex = routesSource.indexOf('const gatewayKey = await validateGatewayApiKeyAsync')
const acceptTurnIndex = routesSource.indexOf('accepted = await acceptChatTurn')
const contextReadIndex = routesSource.indexOf('const storedContext = await listChatContextMessages')
const transportBuildIndex = routesSource.indexOf('const transport = buildChatTransportRequest')
assert(duplicateLookupIndex >= 0 && duplicateLookupIndex < gatewayValidationIndex, '重复 clientMessageId 必须在昂贵网关校验和路由解析前返回 409')
assert.match(routesSource, /validateFixedChatInputBudget/, '固定输入预算必须在接受轮次前独立预检')
assert(acceptTurnIndex >= 0 && acceptTurnIndex < contextReadIndex, '最终上下文必须在 accept 成功后读取，避免遗漏刚完成的上一轮')
assert(contextReadIndex < transportBuildIndex, '最终 transport 必须使用 accept 后读取的最新完整历史')
assert.match(routesSource, /chat_image_not_supported/, 'Chat-only 图片输入必须返回稳定错误码')
assert.match(routesSource, /resolveChatBudgetContent/, '上下文预算必须按 transport 实际发送文本计算')
assert.doesNotMatch(routesSource, /baseUrl|base_url|proxyProfile/, 'AI 问答路由不能直接拼上游 Base URL 或代理配置')
assert.match(serverSource, /chatHttpProxy = createDbServiceHttpProxy\(\{ maxInFlight: 128, timeoutMs: 15 \* 60_000 \}\)/, 'AI 问答长连接必须使用独立代理池和 15 分钟超时')
assert(serverSource.indexOf('app.use(`${systemApiPrefix}/my-chat`, chatHttpProxy)') < serverSource.indexOf('app.use(systemApiPrefix, dbServiceHttpProxy)'), 'AI 问答独立代理必须挂在通用 System API 代理之前')

console.log('AI 问答路由契约回归通过')
