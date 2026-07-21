import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const performanceSource = readFileSync(new URL('../../views/chat/chatConversationPerformance.ts', import.meta.url), 'utf8')

const selectConversationStart = chatViewSource.indexOf('async function selectConversation(')
const selectConversationEnd = chatViewSource.indexOf('async function ensurePendingConversationAvailability', selectConversationStart)
const selectConversationSource = chatViewSource.slice(selectConversationStart, selectConversationEnd)

assert.ok(selectConversationStart >= 0 && selectConversationEnd > selectConversationStart, '必须能定位会话选择流程')
assert.doesNotMatch(selectConversationSource, /listModels|modelLoadCoordinator\.(?:load|refresh|refreshIfExpired)|startChatConversationLoad[\s\S]*loadModels/, '打开会话和首屏不得请求模型列表')
assert.match(selectConversationSource, /selectedModel\.value\s*=\s*conversation\.lastModel/, '已有会话必须直接恢复 lastModel，不能等待模型列表')
assert.match(chatViewSource, /async function loadSelectedModelCapabilities/, '当前模型能力必须由独立按 ID 加载流程维护')
assert.match(chatViewSource, /chatApi\.getModelCapabilities\(conversationId, modelId, \{ signal \}\)/, '当前模型必须按 ID 获取能力详情并传递取消信号')
assert.match(performanceSource, /class ChatModelCapabilitiesLoadCoordinator[\s\S]*new AbortController\(\)[\s\S]*this\.cache\.set/, '能力协调器必须同时提供 AbortController 取消与结果缓存')
assert.match(chatViewSource, /async function loadModelsOnOpen[\s\S]*modelLoadCoordinator\.refreshIfExpired/, '模型列表只能由下拉首次展开触发')
assert.match(composerSource, /item\.name/, '模型下拉展示名称必须来自轻量 name 字段')
assert.match(chatApiSource, /listModels:[\s\S]*ChatModelListOption\[\]/, '模型列表 API 必须使用轻量类型')
assert.match(chatApiSource, /getModelCapabilities:[\s\S]*ChatModelCapabilities/, '模型能力必须使用独立详情 API')
assert.match(chatTypesSource, /interface ChatModelListOption\s*\{\s*id: string\s*name: string\s*\}/, '模型列表项只能包含 id 和 name')
assert.match(chatTypesSource, /defaultModel\?: ChatModelListOption/, '会话响应必须携带轻量默认模型引用')
assert.match(chatViewSource, /conversation\.lastModel \?\? conversation\.defaultModel\?\.id/, '新会话必须无需打开列表即可恢复服务端默认模型')
assert.match(chatViewSource, /ChatModelCapabilitiesLoadCoordinator/, '模型能力请求必须由可测试的缓存与取消协调器管理')
assert.ok((chatViewSource.match(/modelCapabilitiesLoadCoordinator\.cancel\(\)/g) ?? []).length >= 2, '切换会话和卸载都必须取消旧能力请求')

console.log('chat model lazy loading regression passed')
