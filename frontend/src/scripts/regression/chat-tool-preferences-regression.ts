import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// 用户级默认工具绑定（工具体系设计 §2.11-§2.12/§8.5-§8.6/§10.6-§10.7）+
// localStorage 偏好机制删除（§11.6）+ 绑定入口收敛为单一 /tool-defaults 统一
// 弹窗（2026-10-03）的前端契约回归。

const chatApiSource = readFileSync(new URL('../../api/domains/chat.ts', import.meta.url), 'utf8')
const chatTypesSource = readFileSync(new URL('../../types/domain/chat.ts', import.meta.url), 'utf8')
const chatViewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const bindingDialogSource = readFileSync(new URL('../../views/chat/ChatToolBindingDialog.vue', import.meta.url), 'utf8')
const composerCommandsSource = readFileSync(new URL('../../views/chat/composer/chatComposerCommands.ts', import.meta.url), 'utf8')
const composerSource = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')

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

// --- / 命令入口（§10.7，2026-10-03 收敛为单一命令） ---

assert.match(
  composerCommandsSource,
  /\{ key: 'tool-defaults', kind: 'conversation', action: 'set-tool-defaults', label: '工具模型绑定', description: '打开工具模型绑定弹窗，设置网页搜索与图像生成的全局默认绑定（账户 \+ 模型）。' \}/,
  'conversation 类命令必须注册统一的 tool-defaults（action set-tool-defaults）'
)
assert.doesNotMatch(
  composerCommandsSource,
  /image-model|image-tool-defaults/,
  '命令注册表不得保留会话级图像模型与生图绑定独立命令（统一进 /tool-defaults 弹窗）'
)

// --- ChatView：统一弹窗纯全局语义，保存后当前会话立即跟随（§10.7） ---

assert.match(chatViewSource, /const toolBindingDialogOpen = ref\(false\)/, 'ChatView 必须维护统一绑定弹窗开关')
assert.doesNotMatch(chatViewSource, /toolBindingToolId|toolBindingMode|openToolBindingDialog/, 'ChatView 不得保留弹窗工具 ID 与会话/全局双模式状态')
assert.match(
  chatViewSource,
  /if \(action === 'set-tool-defaults'\) \{[\s\S]{0,300}toolBindingDialogOpen\.value = true/,
  'set-tool-defaults 分支必须直接打开统一绑定弹窗（不依赖当前会话状态）'
)
assert.match(
  chatViewSource,
  /toolEvent\.item\?\.errorCode !== 'tool_binding_required'[\s\S]{0,700}toolBindingDialogOpen\.value = true/,
  'binding_required 事件必须 toast 提示并打开统一绑定弹窗'
)
assert.match(
  chatViewSource,
  /async function handleToolBindingSaved\(payload: ChatToolPreferencesPatch\): Promise<void> \{[\s\S]{0,200}if \(!conversation \|\| conversation\.archived\) \{\s*message\.success\('全局绑定已更新，新会话自动继承'\)/,
  '无当前会话或会话已归档时必须跳过会话同步，仅提示全局生效'
)
assert.match(
  chatViewSource,
  /searchBinding: payload\.searchBinding \?\? null,\s*imageBinding: payload\.imageBinding \?\? null,\s*defaultImageModel: payload\.defaultImageModel \?\? conversation\.defaultImageModel/,
  '当前会话同步 PATCH 必须按 UI 最终值携带 searchBinding/imageBinding/defaultImageModel 三键'
)
assert.match(chatViewSource, /message\.success\('全局绑定已更新，当前会话已同步'\)/, '会话同步成功必须提示全局绑定与当前会话已同步')
assert.doesNotMatch(chatViewSource, /label="工具能力"|label="默认图像模型"/, '会话详情弹窗必须移除工具能力与默认图像模型行（入口统一为 /tool-defaults）')

// --- ChatToolBindingDialog：统一弹窗、偏好端点读写、两级选择器、文案（§10.4/§10.6/§10.7） ---

assert.match(bindingDialogSource, /title="工具模型绑定"/, '统一弹窗标题必须为「工具模型绑定」')
assert.doesNotMatch(bindingDialogSource, /mode\?: 'conversation' \| 'user'|props\.conversation|getToolBindings/, '统一弹窗必须为纯全局语义，不得保留会话模式、conversation prop 与会话级数据源')
assert.match(
  bindingDialogSource,
  /const preferences = await chatApi\.getToolPreferences\(\)/,
  '统一弹窗状态必须来自 GET /my-chat/tool-preferences'
)
assert.match(bindingDialogSource, /\.filter\(\(tool\) => tool\.kind === 'model'\)/, '弹窗必须按偏好响应渲染全部模型工具各一区（不硬编码工具清单）')
assert.match(bindingDialogSource, /candidates: tool\.candidates \?\? \[\]/, '候选必须全部来自偏好响应 tools[].candidates')
assert.match(
  bindingDialogSource,
  /searchBinding: searchCandidate \? \{ accountId: searchCandidate\.accountId, modelId: searchCandidate\.modelId \} : null/,
  '搜索区保存必须以「账户+模型」二元组整体写入或解绑（null）'
)
assert.match(
  bindingDialogSource,
  /imageBinding: imageCandidate \? \{ accountId: imageCandidate\.accountId \} : null/,
  '生图区保存必须只写账户键（解绑传 null）'
)
assert.match(
  bindingDialogSource,
  /imageCandidate \? imageCandidate\.modelId as ChatImageModel : effectiveImageModel\.value/,
  '生图区 UI 最终模型：选定候选即候选模型，未选候选保持偏好生效值（解绑不动默认图像模型）'
)
assert.match(bindingDialogSource, /chatApi\.updateToolPreferences\(payload\)/, '一次保存必须是一个携带全部键的偏好 PATCH')
assert.match(bindingDialogSource, /\(event: 'saved', payload: ChatToolPreferencesPatch\): void/, 'saved 事件必须携带偏好 payload（供 ChatView 同步当前会话）')
assert.match(bindingDialogSource, /新会话自动继承，保存后当前会话立即同步。/, 'intro 必须说明新会话自动继承与当前会话立即同步')
assert.match(bindingDialogSource, /if \(!section\.bound\) return '未设置'/, '每区必须显示绑定状态：未设置')
assert.match(bindingDialogSource, /section\.valid \? '已绑定' : '已失效'/, '每区必须显示绑定状态：已绑定/已失效')
assert.match(bindingDialogSource, /selectedAccountId\(section\)/, '每个工具区必须为「账户 → 模型」两级选择（账户去重下拉、模型跟随账户过滤）')
assert.match(bindingDialogSource, /:disabled="!selectedAccountId\(section\)"/, '未选账户时模型下拉必须禁用')
assert.match(bindingDialogSource, /当前生效：/, '每区必须明示当前生效值（不只是单选选中态）')
assert.match(bindingDialogSource, /label: '不绑定'/, '不绑定必须是账户下拉的空值选项')
assert.doesNotMatch(bindingDialogSource, /过滤账户或模型/, '平铺候选过滤输入框已由两级下拉自带搜索取代')
assert.doesNotMatch(bindingDialogSource, /'gpt-image-2'|'grok-imagine/, '弹窗不得保留硬编码图像模型枚举（候选模型全部来自偏好响应）')

// --- 工具箱常驻入口与状态摘要（§10.7，2026-10-04 可用性收敛） ---

assert.match(composerSource, /key="tool-bindings"/, '工具箱必须提供「工具模型绑定」常驻入口（不依赖记忆 / 命令）')
assert.match(composerSource, /toolBindingsSummary\?/, '工具箱入口必须展示绑定状态摘要（保存后无感知问题的可见性修复）')
assert.match(composerSource, /emit\('open-tool-bindings'\)/, '工具箱入口必须通过 open-tool-bindings 事件打开统一弹窗')
assert.match(chatViewSource, /:tool-bindings-summary="toolBindingsSummary"/, 'ChatView 必须把绑定状态摘要传给编辑器工具箱')
assert.match(chatViewSource, /@open-tool-bindings="toolBindingDialogOpen = true"/, 'ChatView 必须响应工具箱入口打开统一弹窗')
assert.match(chatViewSource, /refreshToolBindingsSummary\(\)[\s\S]{0,120}const conversation = selectedConversation\.value/, '绑定保存后必须刷新工具箱状态摘要')
assert.doesNotMatch(bindingDialogSource, /需要时由会话内引导设置/, '弹窗不得泄漏「会话内引导」内部术语')
assert.match(bindingDialogSource, /ok-button-props="okButtonProps"/, '未发生变更时必须禁用保存按钮（dirty 判定）')
assert.match(bindingDialogSource, /JSON\.stringify\(buildPayload\(\)\) !== initialPayloadJson\.value/, '保存禁用必须基于打开时 payload 快照与当前值的比较')

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
