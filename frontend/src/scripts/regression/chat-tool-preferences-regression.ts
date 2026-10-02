import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// 用户级默认工具绑定（工具体系设计 §2.11-§2.12/§8.5-§8.6/§10.6-§10.7）+
// localStorage 偏好机制删除（§11.6）的前端契约回归。

const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const bindingDialogSource = readFileSync(new URL('../../views/chat/ChatToolBindingDialog.vue', import.meta.url), 'utf8')
const composerCommandsSource = readFileSync(new URL('../../views/chat/composer/chatComposerCommands.ts', import.meta.url), 'utf8')

// --- chatApi：偏好端点方法存在与路径（§8.5/§8.6） ---

const { chatApi } = await import('../../api/domains/chat')
assert.equal(typeof chatApi.getToolPreferences, 'function', 'chatApi 必须提供 getToolPreferences 方法')
assert.equal(typeof chatApi.updateToolPreferences, 'function', 'chatApi 必须提供 updateToolPreferences 方法')
assert.match(
  chatApiSource,
  /getToolPreferences: \(\) => unwrap<ChatConversationToolCapabilities>\(http\.get\('\/my-chat\/tool-preferences'\)\)/,
  'GET /my-chat/tool-preferences 必须返回与 tool-bindings 同形状 payload'
)
assert.match(
  chatApiSource,
  /updateToolPreferences: \(payload: ChatToolPreferencesPatch\) => unwrap<ChatConversationToolCapabilities>\(http\.patch\('\/my-chat\/tool-preferences', payload\)\)/,
  'PATCH /my-chat/tool-preferences 必须接收严格键集并返回 GET 同形状 payload'
)
assert.match(
  chatTypesSource,
  /export interface ChatToolPreferencesPatch \{\s*searchBinding\?: \{ accountId: string; modelId: string \} \| null\s*imageBinding\?: \{ accountId: string \} \| null\s*defaultImageModel\?: ChatImageModel\s*\}/,
  'ChatToolPreferencesPatch 必须是 §8.6 的严格键集（三元组均可选可空）'
)

// --- / 命令入口（§10.7） ---

assert.match(
  composerCommandsSource,
  /\{ key: 'tool-defaults', kind: 'conversation', action: 'set-tool-defaults', label: '搜索默认绑定', description: '设置网页搜索的全局默认绑定「账户 \+ 模型」；新会话自动继承，会话内修改会同步为默认。' \}/,
  'conversation 类命令必须注册 tool-defaults（action set-tool-defaults）'
)

// --- ChatView：/ 命令打开全局模式弹窗，user 保存不触碰会话 ---

assert.match(chatViewSource, /const toolBindingMode = ref<'conversation' \| 'user'>\('conversation'\)/, 'ChatView 必须维护绑定弹窗模式状态（默认会话模式）')
assert.match(chatViewSource, /:mode="toolBindingMode"/, '绑定弹窗必须按模式状态传 mode')
assert.match(
  chatViewSource,
  /if \(action === 'set-tool-defaults'\) \{[\s\S]{0,400}openToolBindingDialog\('web_search', 'user'\)/,
  'set-tool-defaults 分支必须以 user 模式打开 web_search 绑定弹窗'
)
assert.match(
  chatViewSource,
  /if \(action === 'set-image-tool-defaults'\) \{[\s\S]{0,400}openToolBindingDialog\('generate_image', 'user'\)/,
  'set-image-tool-defaults 分支必须以 user 模式打开 generate_image 绑定弹窗（生图全局默认入口）'
)
assert.match(
  chatViewSource,
  /function openToolBindingDialog\(toolId: string, mode: 'conversation' \| 'user' = 'conversation'\): void \{/,
  '绑定弹窗打开辅助必须默认会话模式、可选全局模式'
)
assert.match(
  chatViewSource,
  /async function handleToolBindingSaved\(updated\?: ChatConversation\): Promise<void> \{[\s\S]{0,260}if \(toolBindingMode\.value === 'user'\) \{\s*message\.success\('全局默认已更新，新会话自动继承'\)\s*return\s*\}/,
  'user 模式保存回调必须只轻提示，不 replaceConversation、不刷新详情'
)

// --- ChatToolBindingDialog：mode 分支、偏好端点读写、全局默认置顶、文案（§10.4/§10.6/§10.7） ---

assert.match(bindingDialogSource, /mode\?: 'conversation' \| 'user'/, '弹窗必须接受可选 mode prop')
assert.match(bindingDialogSource, /conversation\?: ChatConversation/, 'conversation prop 必须放宽为可选（user 模式允许为空）')
assert.match(
  bindingDialogSource,
  /if \(mode\.value === 'user'\) \{[\s\S]{0,260}const preferences = await chatApi\.getToolPreferences\(\)/,
  'user 模式状态必须来自 GET /my-chat/tool-preferences'
)
assert.match(
  bindingDialogSource,
  /chatApi\.getToolBindings\(props\.conversation!\.id\),\s*chatApi\.getToolPreferences\(\)\.catch\(\(\) => undefined\)/,
  'conversation 模式必须并行拉取 tool-bindings 与用户默认偏好（偏好失败不阻断）'
)
assert.match(
  bindingDialogSource,
  /chatApi\.updateToolPreferences\(props\.toolId === 'generate_image'\s*\? \{\s*imageBinding: candidate \? \{ accountId: candidate\.accountId \} : null,/,
  'user 模式生图保存必须 PATCH imageBinding（解绑传 null）'
)
assert.match(
  bindingDialogSource,
  /: \{ searchBinding: candidate \? \{ accountId: candidate\.accountId, modelId: candidate\.modelId \} : null \}\)/,
  'user 模式搜索保存必须 PATCH searchBinding 二元组（解绑传 null）'
)
assert.match(
  bindingDialogSource,
  /candidate && candidate\.modelId !== effectiveImageModel\.value\s*\? \{ defaultImageModel: candidate\.modelId as ChatImageModel \}/,
  'user 模式生图候选模型与偏好生效默认不同时必须一并 PATCH defaultImageModel'
)
assert.match(
  bindingDialogSource,
  /defaultTopCandidateKey\.value = preferredTool\?\.binding \? candidateKey\(preferredTool\.binding\) : undefined/,
  '全局默认置顶基准必须来自用户默认偏好的对应工具 binding'
)
assert.match(
  bindingDialogSource,
  /\.sort\(\(left, right\) => Number\(candidateKey\(left\) !== top\) - Number\(candidateKey\(right\) !== top\)\)/,
  '命中全局默认的候选必须置顶且其余保持接口顺序'
)
assert.match(bindingDialogSource, /dialogTitle = computed\(\(\) => mode\.value === 'user' \? `设置\$\{toolTitle\.value\}全局默认绑定`/, 'user 模式标题必须带「全局默认」字样')
assert.match(bindingDialogSource, /新会话自动继承，会话内修改会同步为默认。/, 'user 模式 intro 必须说明新会话自动继承与会话内修改同步为默认')
assert.match(bindingDialogSource, /\(event: 'saved', conversation\?: ChatConversation\): void/, 'saved 事件载荷必须放宽为可选（user 模式无会话载荷）')

// --- localStorage 偏好机制删除（§11.6）：源码全局无残留 ---

assert.doesNotMatch(chatViewSource, /loadConversationPreferences|saveConversationPreferences|recordToolBindingPreference|applyConversationPreferences/, 'ChatView 不得保留偏好读写与应用链')
assert.doesNotMatch(bindingDialogSource, /loadConversationPreferences|resolveLastUsedCandidateKey/, '弹窗不得保留最近使用置顶链')

// 扫描运行时源码（src 下除 scripts 外的 .ts/.vue）；回归脚本自身的断言文本
// 不属于运行面，不在删除口径内。
function listSourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const full = `${directory}/${entry.name}`
    if (entry.isDirectory()) return entry.name === 'scripts' ? [] : listSourceFiles(full)
    return /\.(ts|vue)$/.test(entry.name) ? [full] : []
  })
}

const frontendRoot = fileURLToPath(new URL('../../..', import.meta.url))
for (const file of listSourceFiles(`${frontendRoot}/src`)) {
  const source = readFileSync(file, 'utf8')
  assert.doesNotMatch(source, /juhe-ai:chat:conversation-preferences/, `不得残留偏好存储键：${file}`)
  assert.doesNotMatch(source, /chatConversationPreferences/, `不得残留偏好模块引用：${file}`)
}

console.log('用户级默认工具绑定与偏好机制删除契约回归通过')
