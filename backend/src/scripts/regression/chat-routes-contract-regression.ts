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
  /chatRouter\.get\('\/conversations\/:conversationId\/context-status'/,
  /chatRouter\.get\('\/conversations\/:conversationId'/,
  /chatRouter\.patch\('\/conversations\/:conversationId'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/assets'/,
  /chatRouter\.get\('\/conversations\/:conversationId\/assets\/:assetId\/content'/,
  /chatRouter\.get\('\/conversations\/:conversationId\/models'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stream'/,
  /chatRouter\.post\('\/conversations\/:conversationId\/stop'/,
  /chatRouter\.delete\('\/conversations\/:conversationId'/
]) {
  assert.match(routesSource, contract)
}
assert.match(routesSource, /findApiKeySecretAsync/, '模型和发送必须在服务端按当前用户读取真实 API Key')
assert.match(routesSource, /buildChatTransportRequest/, 'AI 问答模型调用必须通过 Chat\/Responses transport 重新进入现有网关')
const streamRouteIndex = routesSource.indexOf("chatRouter.post('/conversations/:conversationId/stream'")
const duplicateLookupIndex = routesSource.indexOf('await findChatTurnByClientMessageId', streamRouteIndex)
const gatewayValidationIndex = routesSource.indexOf('const gatewayKey = await validateGatewayApiKeyAsync', streamRouteIndex)
const acceptTurnIndex = routesSource.indexOf('accepted = await acceptChatTurn', streamRouteIndex)
const contextReadIndex = routesSource.indexOf('await loadChatTransportHistory', streamRouteIndex)
const transportBuildIndex = routesSource.indexOf('buildChatTransportRequest({', contextReadIndex)
const activeStreamIndex = routesSource.indexOf('activeStreams.set(conversation.id')
const upstreamFetchIndex = routesSource.indexOf('const upstream = await fetch')
assert(duplicateLookupIndex >= 0 && duplicateLookupIndex < gatewayValidationIndex, '重复 clientMessageId 必须在昂贵网关校验和路由解析前返回 409')
assert.match(routesSource, /validateFixedChatInputBudget/, '固定输入预算必须在接受轮次前独立预检')
assert(contextReadIndex >= 0 && contextReadIndex < acceptTurnIndex, '检查点上下文和硬水位必须在接受轮次前完成，避免超限消息半成功')
assert(contextReadIndex < transportBuildIndex && transportBuildIndex < acceptTurnIndex, '最终 transport 与请求体字节预检必须在消息落库前完成')
assert(acceptTurnIndex < activeStreamIndex && activeStreamIndex < upstreamFetchIndex, 'accept 完成后才能登记 activeStreams 和请求上游')
assert.match(routesSource, /compactChatContextOnce/, '硬水位必须在发送前执行有界压缩')
assert.match(routesSource, /error instanceof ChatModelContextError[\s\S]{0,120}error\.reason !== 'load_limit'[\s\S]{0,300}compactChatContextOnce/, '超过本地 512 条装载上限时必须先分页压缩再重试，不能直接卡死会话')
assert.match(routesSource, /Buffer\.byteLength\(serializedTransportBody[\s\S]{0,300}!contextCompacted[\s\S]{0,300}compactChatContextOnce/, '请求体超过内部字节阈值时必须先尝试压缩历史')
assert.match(routesSource, /preparedContext\.unresolvedAssetIds\.length[\s\S]{0,700}历史图片语义说明仍在生成/, '历史图片说明未完成时必须短暂等待后拒绝本轮，不能回灌历史 Base64')
assert.match(routesSource, /scheduleChatContextCompaction/, '软水位必须异步调度压缩')
assert.match(routesSource, /maxInternalChatRequestBytes/, '模型请求必须在内部网关前实测 JSON 字节')
assert.match(routesSource, /chat_image_not_supported/, 'Chat-only 图片输入必须返回稳定错误码')
assert.match(routesSource, /resolveChatBudgetContent/, '上下文预算必须按 transport 实际发送文本计算')
assert.match(routesSource, /recordChatContextUsage/, '完成响应后必须保存真实或明确标记的估算上下文用量')
assert.match(routesSource, /replaceTurnId:\s*z\.string\(\)\.trim\(\)\.min\(1\)\.max\(100\)\.optional\(\)/, 'stream body 必须接受最多 100 字符的 replaceTurnId')
assert.match(routesSource, /beforeSequenceNo:[\s\S]*max\(2_147_483_647/, '消息游标必须在 HTTP schema 层限制为 PostgreSQL int4')
assert.match(routesSource, /contentBlocks:\s*body\.contentBlocks/, 'acceptChatTurn 必须保存实际输入块类型标记')
assert.match(routesSource, /type:\s*z\.literal\('input_image'\),\s*assetId:/, '图片输入 HTTP 契约只能接受资产 ID，不能接受 Data URL')
assert.doesNotMatch(routesSource, /type:\s*z\.literal\('input_image'\),\s*dataUrl:/, '图片 Data URL 不得进入 stream JSON 契约')
assert.match(routesSource, /uploadChatAsset\(\{[\s\S]*req,[\s\S]*conversationId:\s*conversation\.id/, '图片上传必须使用所属会话和当前登录用户写入资产')
assert.match(routesSource, /openChatAssetObject\(asset\.storageKey\)/, '图片内容读取必须从受控资产存储打开对象')
assert.match(routesSource, /Cache-Control',\s*'private, max-age=3600'/, '图片内容响应必须使用私有缓存策略')
assert.match(routesSource, /replaceTurnId:\s*body\.replaceTurnId/, 'acceptChatTurn 必须收到 replaceTurnId')
assert.match(routesSource, /error instanceof ChatConflictError[\s\S]*status\(409\)[\s\S]*code: error\.code/, 'chat_replace_conflict 必须稳定映射到 HTTP 409 与机器码')
assert.doesNotMatch(routesSource, /baseUrl|base_url|proxyProfile/, 'AI 问答路由不能直接拼上游 Base URL 或代理配置')
assert.match(serverSource, /chatHttpProxy = createDbServiceHttpProxy\(\{ maxInFlight: 128, timeoutMs: 15 \* 60_000 \}\)/, 'AI 问答长连接必须使用独立代理池和 15 分钟超时')
assert(serverSource.indexOf('app.use(`${systemApiPrefix}/my-chat`, chatHttpProxy)') < serverSource.indexOf('app.use(systemApiPrefix, dbServiceHttpProxy)'), 'AI 问答独立代理必须挂在通用 System API 代理之前')

console.log('AI 问答路由契约回归通过')
