import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY,
  loadConversationPreferences,
  mergeConversationPreferences,
  parseConversationPreferences,
  saveConversationPreferences
} from '../../views/chat/chatConversationPreferences'

// 会话偏好存储（工具体系设计 §10.6）+ 账户联动默认（§10.5）+ 绑定弹窗过滤
// 排序（§10.4）+ 账户/模型下拉模糊搜索的前端契约回归。

class MemoryStorage implements Storage {
  private readonly map = new Map<string, string>()
  get length(): number { return this.map.size }
  clear(): void { this.map.clear() }
  getItem(key: string): string | null { return this.map.has(key) ? this.map.get(key)! : null }
  key(index: number): string | null { return [...this.map.keys()][index] ?? null }
  removeItem(key: string): void { this.map.delete(key) }
  setItem(key: string, value: string): void { this.map.set(key, String(value)) }
}

class ThrowingWriteStorage extends MemoryStorage {
  setItem(): never { throw new Error('quota exceeded') }
}

const emptyPreferences = { accountId: null, model: null, searchBinding: null, imageBinding: null, defaultImageModel: null }

// --- 解析纯函数：空/坏 JSON/坏形状返回 null，不抛 ---

assert.equal(parseConversationPreferences(null), null, '空输入必须返回 null')
assert.equal(parseConversationPreferences(''), null, '空字符串必须返回 null')
assert.equal(parseConversationPreferences('not json'), null, '坏 JSON 必须返回 null 而不是抛错')
assert.equal(parseConversationPreferences('123'), null, '非对象 JSON 必须返回 null')
assert.equal(parseConversationPreferences('"str"'), null, '字符串 JSON 必须返回 null')
assert.equal(parseConversationPreferences('[]'), null, '数组 JSON 必须返回 null')
assert.deepEqual(
  parseConversationPreferences('{"accountId":"acc_1","model":"gpt-6","searchBinding":{"accountId":"acc_2","modelId":"gpt-6-sol"},"imageBinding":{"accountId":"acc_3"},"defaultImageModel":"gpt-image-2"}'),
  { accountId: 'acc_1', model: 'gpt-6', searchBinding: { accountId: 'acc_2', modelId: 'gpt-6-sol' }, imageBinding: { accountId: 'acc_3' }, defaultImageModel: 'gpt-image-2' },
  '合法形状必须完整解析'
)
assert.deepEqual(
  parseConversationPreferences('{"accountId":123,"searchBinding":"bad","imageBinding":{"accountId":9},"extra":true}'),
  { ...emptyPreferences },
  '字段类型不符按缺失处理（宽容解析），坏绑定按未绑定处理'
)

// --- 合并纯函数：undefined 保持原值，null 显式清空 ---

assert.deepEqual(mergeConversationPreferences(null, {}), emptyPreferences, '无 base 无 patch 得到全空偏好')
assert.deepEqual(
  mergeConversationPreferences({ ...emptyPreferences, accountId: 'acc_1', model: 'gpt-6' }, { model: 'gpt-6-mini' }),
  { ...emptyPreferences, accountId: 'acc_1', model: 'gpt-6-mini' },
  'patch 未覆盖字段必须保持原值'
)
assert.deepEqual(
  mergeConversationPreferences({ ...emptyPreferences, accountId: 'acc_1' }, { accountId: null, searchBinding: { accountId: 'acc_2', modelId: 'gpt-6-sol' } }),
  { ...emptyPreferences, searchBinding: { accountId: 'acc_2', modelId: 'gpt-6-sol' } },
  'null 必须显式清空对应字段'
)

// --- 读写往返：upsert、存储不可用、写入失败均不抛 ---

const storage = new MemoryStorage()
assert.equal(loadConversationPreferences(storage), null, '未写入前读取为 null')
assert.equal(saveConversationPreferences({ accountId: 'acc_1' }, storage), true)
assert.deepEqual(loadConversationPreferences(storage), { ...emptyPreferences, accountId: 'acc_1' }, '首次写入得到单项偏好')
assert.equal(saveConversationPreferences({ model: 'gpt-6', searchBinding: { accountId: 'acc_2', modelId: 'gpt-6-sol' } }, storage), true)
assert.equal(saveConversationPreferences({ imageBinding: { accountId: 'acc_3' }, defaultImageModel: 'gpt-image-2' }, storage), true)
assert.deepEqual(
  loadConversationPreferences(storage),
  { accountId: 'acc_1', model: 'gpt-6', searchBinding: { accountId: 'acc_2', modelId: 'gpt-6-sol' }, imageBinding: { accountId: 'acc_3' }, defaultImageModel: 'gpt-image-2' },
  '分路径 upsert 后偏好必须是各路径字段的并集'
)
assert.equal(saveConversationPreferences({ searchBinding: null }, storage), true)
assert.equal(loadConversationPreferences(storage)?.searchBinding, null, '解绑保存后偏好绑定必须清空')
assert.equal(saveConversationPreferences({}, undefined), false, '存储不可用时写入返回 false 不抛')
assert.equal(loadConversationPreferences(undefined), null, '存储不可用时读取返回 null 不抛')
assert.equal(saveConversationPreferences({ model: 'gpt-6' }, new ThrowingWriteStorage()), false, '写入抛异常（隐私模式/配额）必须吞掉并返回 false')
storage.setItem(CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY, 'corrupted{')
assert.equal(loadConversationPreferences(storage), null, '存储内容损坏时读取返回 null（按无偏好处理）')
storage.removeItem(CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY)
assert.equal(CHAT_CONVERSATION_PREFERENCES_STORAGE_KEY, 'juhe-ai:chat:conversation-preferences:v1', '偏好存储键必须与设计 §10.6 固定的键一致')

// --- ChatView 接线：写入路径、新会话应用、账户联动默认、失败轮重试 ---

const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
assert.match(chatViewSource, /import \{ loadConversationPreferences, saveConversationPreferences \} from '\.\/chatConversationPreferences'/, 'ChatView 必须接入会话偏好模块')
assert.match(chatViewSource, /async function changeAccount[\s\S]{0,2200}saveConversationPreferences\(\{ accountId: updated\.bindAccountId \?\? null \}\)/, '账户切换成功必须写入偏好账户')
assert.match(chatViewSource, /watch\(selectedModel, \(modelId\) => \{[\s\S]{0,260}if \(modelId\) saveConversationPreferences\(\{ model: modelId \}\)/, '模型选定必须写入偏好模型')
assert.match(chatViewSource, /async function handleToolBindingSaved[\s\S]{0,180}recordToolBindingPreference\(toolBindingToolId\.value, updated\)/, '绑定保存成功必须写入偏好绑定')
assert.match(chatViewSource, /async function saveDefaultImageModel[\s\S]{0,900}saveConversationPreferences\(\{ defaultImageModel: updated\.defaultImageModel \?\? null \}\)/, '默认图像模型保存成功必须写入偏好')
assert.match(chatViewSource, /async function createConversationDirectly[\s\S]{0,700}void applyConversationPreferences\(item\)/, '新建会话进入后必须后台应用偏好（不阻塞进入）')
assert.match(chatViewSource, /async function applyConversationPreferences[\s\S]{0,1400}chatApi\.listChatAccounts\(\)/, '偏好应用必须校验账户在可派发账户列表内')
assert.match(chatViewSource, /accountId === preferences\.searchBinding\?\.accountId && item\.modelId === preferences\.searchBinding\?\.modelId/, '搜索绑定校验必须是账户+模型精确匹配候选')
assert.match(chatViewSource, /item\.accountId === preferences\.imageBinding\?\.accountId/, '生图绑定校验必须是账户精确匹配候选')
assert.match(chatViewSource, /console\.debug\('\[chat-preferences\]/, '失效项跳过只允许 console.debug，不得向用户报错')
assert.match(chatViewSource, /message\.success\('已按上次配置初始化'\)/, '偏好应用成功必须轻提示')
assert.match(chatViewSource, /const preferences = loadConversationPreferences\(\)[\s\S]{0,120}if \(!preferences\) return/, '偏好不存在必须直接结束（行为与现状一致）')
assert.match(chatViewSource, /if \(conversation\.lastModel && !updated\.lastModel\) \{[\s\S]{0,120}selectedModel\.value = undefined/, '切换账户导致 lastModel 联动清空时必须重置本地模型选择')
assert.match(chatViewSource, /void applyAccountSwitchDefaults\(conversation, updated\)/, '账户切换成功后必须触发后台联动默认')
assert.match(chatViewSource, /async function applyAccountSwitchDefaults[\s\S]{0,1500}message\.success\(`模型已自动切换为 \$\{first\.name\}`\)/, 'lastModel 为空时联动必须自动取首项并提示')
assert.match(chatViewSource, /tool\.bound && tool\.valid\) continue/, '联动不得覆盖已绑定且有效的绑定')
assert.match(chatViewSource, /\(tool\.candidates \?\? \[\]\)\.find\(\(item\) => item\.accountId === accountId\)/, '联动补全必须取同账户第一个候选')
assert.match(chatViewSource, /candidate\.modelId !== latest\.defaultImageModel \? \{ defaultImageModel: candidate\.modelId as ChatConversation\['defaultImageModel'\] \} : \{\}/, '生图联动候选模型与默认图像模型不同时必须一并更新')
// 失败轮重试复用既有最新轮机制（§10.3）：retryableTurn 门控接线，不做任意
// 消息位的重复按钮（非最新轮 replaceTurn 必然 conflict）。
assert.match(chatViewSource, /:retryable-message-id="generating \|\| submissionBlocked \|\| modelsLoading \? undefined : retryableTurn\?\.userMessageId"/, '最新失败轮重试入口必须带生成中/提交阻断/模型加载门控')
assert.match(chatViewSource, /const retryableTurn = computed\(\(\) => beginLatestTurnRetry\(messages\.value\)\)/, '失败轮重试必须复用最新轮重试判定（限定最新失败/停止轮）')

// --- 绑定弹窗：宽度自适应、输入过滤、最近使用优先（§10.4） ---

const bindingDialogSource = readFileSync(new URL('../../views/chat/ChatToolBindingDialog.vue', import.meta.url), 'utf8')
assert.match(bindingDialogSource, /:width="'min\(92vw, 720px\)'"\r?\n/, '绑定弹窗宽度必须自适应 min(92vw, 720px)')
assert.match(bindingDialogSource, /placeholder="过滤账户或模型"/, '候选列表必须提供输入过滤框')
assert.match(bindingDialogSource, /accountName\.toLowerCase\(\)\.includes\(query\) \|\| item\.modelName\.toLowerCase\(\)\.includes\(query\)/, '过滤必须按账户名/模型名模糊包含且大小写不敏感')
assert.match(bindingDialogSource, /loadConversationPreferences\(\)/, '最近使用排序必须从会话偏好读取上次绑定组合')
assert.match(bindingDialogSource, /sort\(\(left, right\) => Number\(candidateKey\(left\) !== top\) - Number\(candidateKey\(right\) !== top\)\)/, '上次绑定组合命中的候选必须置顶且其余保持接口顺序')
assert.match(bindingDialogSource, /max-height: 300px; overflow-y: auto;/, '候选超过阈值时保持滚动折叠展示')

// --- 账户/模型下拉模糊搜索（§10.4 首句） ---

const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')
assert.match(composerSource, /aria-label="选择 AI 账户"[\s\S]{0,220}show-search/, '账户下拉必须支持输入搜索')
assert.match(composerSource, /aria-label="选择模型"[^\n]*show-search/, '模型下拉必须支持输入搜索')
assert.match(composerSource, /function filterSelectOptionByLabel[\s\S]{0,240}toLowerCase\(\)\.includes\(query\)/, '下拉过滤必须按 label 大小写不敏感包含匹配')
assert.match(composerSource, /@accounts-open="loadAccounts"|emit\('accounts-open'\)/, '账户下拉展开按需刷新行为不得变化')

console.log('会话偏好存储与联动契约回归通过')
