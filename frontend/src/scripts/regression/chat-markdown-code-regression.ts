import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync('../frontend/src/views/chat/ChatMarkdown.vue', 'utf8')
const copyStateSource = readFileSync('../frontend/src/views/chat/chatCodeCopyState.ts', 'utf8')

assert.match(source, /html\(\{\s*text\s*\}\)[\s\S]{0,120}escapeHtml\(text\)/, '模型原始 HTML 必须按文本转义，不能进入 v-html DOM')
assert.match(source, /class="chat-code-block"/, '代码 renderer 必须生成独立代码块 wrapper')
assert.match(source, /class="chat-code-language"/, '代码块必须显示语言标签')
assert.match(source, /class="chat-code-copy"\s+type="button"\s+data-copy-code/, '代码块必须生成可委托的复制按钮')
assert.doesNotMatch(source, /<code[^>]*data-copy-code/, '源码 code 节点不能携带 data-copy-code，避免内容伪造操作入口')
assert.match(source, /root\.value\?\.addEventListener\('click',\s*handleRootClick\)/, '代码复制只能在根节点绑定一次 click 委托')
assert.match(source, /root\.value\?\.removeEventListener\('click',\s*handleRootClick\)/, '卸载时必须移除根 click 委托')
assert.match(source, /target\.closest<HTMLButtonElement>\('button\.chat-code-copy\[data-copy-code\]'\)/, '委托必须先严格匹配复制按钮')
assert.match(source, /button\.closest<HTMLElement>\('\.chat-code-block'\)/, '委托必须限制在最近代码块 wrapper')
assert.match(source, /wrapper\.querySelector<HTMLElement>\(':scope\s*>\s*pre\s*>\s*code'\)/, '复制只能读取 wrapper 的直接 pre/code')
assert.match(source, /code\.textContent/, '复制必须读取 textContent，不能读取 HTML 或响应式原文')
assert.match(source, /import\s*\{\s*writeTextToClipboard\s*\}\s*from\s*'@\/shared\/clipboard'/, '代码复制必须复用带选区降级的公共剪贴板能力')
assert.match(source, /writeTextToClipboard\(value\)/, '代码复制不能只依赖 navigator.clipboard')
assert.match(copyStateSource, /已复制/, '成功后必须短暂显示“已复制”')
assert.match(source, /复制失败，请稍后重试/, '代码复制失败必须显示中文提示')
assert.doesNotMatch(source, /let copyResetTimer:/, '不能用单一 timer 让快速复制的不同代码块互相取消恢复')
assert.match(source, /ChatCodeCopyResetController/, '代码块复制恢复必须使用按按钮隔离的状态 helper')
assert.match(source, /ChatCodeCopyLifecycle/, '异步剪贴板必须由带 mounted generation 的生命周期 helper 隔离')
assert.match(source, /mermaid\.render\(/, 'Mermaid 必须先通过 render 得到 SVG 字符串')
assert.match(source, /DOMPurify\.sanitize\([^)]*svg[\s\S]{0,180}USE_PROFILES:\s*\{\s*svg:\s*true,\s*svgFilters:\s*true\s*\}/, 'Mermaid SVG 插入前必须使用 SVG profile 再净化')
assert.doesNotMatch(source, /mermaid\.run\(/, '不能在已净化 DOM 上调用 mermaid.run 注入未净化 SVG')
assert.match(source, /version !== renderVersion/, '异步 Mermaid 渲染必须保留版本竞态保护')
assert.match(source, /\.chat-code-block[\s\S]{0,120}overflow/, '代码块必须局部横向滚动')
assert.match(source, /\.chat-markdown\s+:deep\(table\)[\s\S]{0,120}overflow-x:\s*auto/, '表格必须局部横向滚动')
assert.match(source, /\.chat-markdown\s+:deep\(\.katex-display\)[\s\S]{0,100}overflow-x:\s*auto/, '公式必须局部横向滚动')

const { ChatCodeCopyLifecycle, ChatCodeCopyResetController } = await import('../../views/chat/chatCodeCopyState')
const scheduled = new Map<number, () => void>()
const canceled: number[] = []
let nextTimer = 0
const controller = new ChatCodeCopyResetController(
  (callback) => { const timer = ++nextTimer; scheduled.set(timer, callback); return timer },
  (timer) => { canceled.push(timer); scheduled.delete(timer) },
  1200
)
const firstButton = { textContent: '复制', isConnected: true }
const secondButton = { textContent: '复制', isConnected: true }
controller.markCopied(firstButton)
controller.markCopied(secondButton)
assert.equal(firstButton.textContent, '已复制')
assert.equal(secondButton.textContent, '已复制')
assert.equal(scheduled.size, 2, '每个代码块必须拥有独立恢复 timer')
scheduled.get(1)?.()
assert.equal(firstButton.textContent, '复制')
assert.equal(secondButton.textContent, '已复制', '第一块恢复不能提前恢复第二块')
scheduled.delete(1)
scheduled.get(2)?.()
assert.equal(secondButton.textContent, '复制')
scheduled.delete(2)
controller.markCopied(firstButton)
controller.markCopied(secondButton)
controller.dispose()
assert.equal(scheduled.size, 0, '组件卸载时必须清理全部代码块 timer')
assert.deepEqual(canceled.slice(-2), [3, 4])
firstButton.textContent = '复制'
controller.markCopied(firstButton)
assert.equal(firstButton.textContent, '复制', 'reset controller dispose 后必须永久拒绝新状态与 timer')
assert.equal(scheduled.size, 0)

function deferred(): { promise: Promise<void>; resolve: () => void; reject: (error: Error) => void } {
  let resolve!: () => void
  let reject!: (error: Error) => void
  const promise = new Promise<void>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise })
  return { promise, resolve, reject }
}
function lifecycleHarness() {
  const pendingTimers = new Map<number, () => void>()
  let timerId = 0
  const reset = new ChatCodeCopyResetController(
    (callback) => { const timer = ++timerId; pendingTimers.set(timer, callback); return timer },
    (timer) => { pendingTimers.delete(timer) }
  )
  return { lifecycle: new ChatCodeCopyLifecycle(reset), pendingTimers }
}

const resolvedCopy = deferred()
const resolvedHarness = lifecycleHarness()
const resolvedButton = { textContent: '复制', isConnected: true }
let resolvedFailureNotices = 0
resolvedHarness.lifecycle.activate()
const resolving = resolvedHarness.lifecycle.copy(
  resolvedButton,
  'const answer = 42',
  () => resolvedCopy.promise,
  () => true,
  () => { resolvedFailureNotices += 1 }
)
resolvedHarness.lifecycle.dispose()
resolvedCopy.resolve()
await resolving
assert.equal(resolvedButton.textContent, '复制', 'clipboard resolve 晚于卸载时不得改写旧按钮')
assert.equal(resolvedHarness.pendingTimers.size, 0, 'clipboard resolve 晚于卸载时不得创建恢复 timer')
assert.equal(resolvedFailureNotices, 0)

const rejectedCopy = deferred()
const rejectedHarness = lifecycleHarness()
const rejectedButton = { textContent: '复制', isConnected: true }
let rejectedFailureNotices = 0
rejectedHarness.lifecycle.activate()
const rejecting = rejectedHarness.lifecycle.copy(
  rejectedButton,
  'const answer = 42',
  () => rejectedCopy.promise,
  () => true,
  () => { rejectedFailureNotices += 1 }
)
rejectedHarness.lifecycle.dispose()
rejectedCopy.reject(new Error('clipboard denied'))
await rejecting
assert.equal(rejectedButton.textContent, '复制')
assert.equal(rejectedHarness.pendingTimers.size, 0)
assert.equal(rejectedFailureNotices, 0, 'clipboard reject 晚于卸载时不得显示过期失败提示')

console.log('AI 问答 Markdown、代码复制与 Mermaid 安全回归通过')
