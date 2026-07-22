import assert from 'node:assert/strict'

import {
  resolveChatComposerKeyAction,
  type ChatComposerKeyAction,
  type ChatComposerKeyInput
} from '../../views/chat/composer/chatComposerKeyboard'

const resolve = (input: Partial<ChatComposerKeyInput>): ChatComposerKeyAction => resolveChatComposerKeyAction({
  key: 'Enter',
  isComposing: false,
  shiftKey: false,
  ctrlKey: false,
  metaKey: false,
  commandOpen: false,
  commandItemCount: 0,
  structuredBlock: false,
  ...input
})

assert.equal(resolve({ isComposing: true, commandOpen: true, commandItemCount: 2 }), 'delegate', 'IME 组合输入优先交给编辑器')
assert.equal(resolve({ isComposing: true, ctrlKey: true }), 'delegate', 'IME 组合输入优先于 Ctrl+Enter')
assert.equal(resolve({ isComposing: true, metaKey: true }), 'delegate', 'IME 组合输入优先于 Meta+Enter')
assert.deepEqual(resolve({ key: 'ArrowDown', commandOpen: true, commandItemCount: 2 }), { type: 'move-command', direction: 'next' }, '命令候选向下移动')
assert.deepEqual(resolve({ key: 'ArrowUp', commandOpen: true, commandItemCount: 2 }), { type: 'move-command', direction: 'previous' }, '命令候选向上移动')
assert.equal(resolve({ commandOpen: true, commandItemCount: 2 }), 'select-command', 'Enter 选择当前命令候选')
assert.equal(resolve({ commandOpen: true, commandItemCount: 2, ctrlKey: true }), 'select-command', '命令候选选择优先于 Ctrl+Enter')
assert.equal(resolve({ commandOpen: true, commandItemCount: 2, shiftKey: true }), 'select-command', '命令候选选择优先于 Shift+Enter')
assert.equal(resolve({ key: 'Escape', commandOpen: true, commandItemCount: 2 }), 'close-command', 'Escape 关闭命令面板')
assert.equal(resolve({ ctrlKey: true, structuredBlock: true }), 'submit', 'Ctrl+Enter 强制发送结构块')
assert.equal(resolve({ metaKey: true, structuredBlock: true }), 'submit', 'Meta+Enter 强制发送结构块')
assert.equal(resolve({ ctrlKey: true, shiftKey: true }), 'submit', 'Ctrl+Enter 优先于 Shift+Enter')
assert.equal(resolve({ metaKey: true, shiftKey: true }), 'submit', 'Meta+Enter 优先于 Shift+Enter')
assert.equal(resolve({}), 'submit', '普通段落 Enter 发送')
assert.equal(resolve({ structuredBlock: true }), 'delegate', '结构块中的普通 Enter 交给编辑器')
assert.equal(resolve({ shiftKey: true }), 'delegate', 'Shift+Enter 交给编辑器换行')
assert.equal(resolve({ commandOpen: true, commandItemCount: 0 }), 'submit', '无候选命令面板不得吞掉普通段落 Enter')
assert.equal(resolve({ commandOpen: true, commandItemCount: 0, structuredBlock: true }), 'delegate', '无候选命令面板继续遵守结构块 Enter 语义')
assert.equal(resolve({ key: 'ArrowDown', commandOpen: true, commandItemCount: 0 }), 'delegate', '无候选命令面板不得吞掉方向键')
assert.equal(resolve({ key: 'Escape', commandOpen: true, commandItemCount: 0 }), 'close-command', '无候选命令面板仍可用 Escape 关闭')
assert.equal(resolve({ key: 'Escape', commandOpen: false }), 'delegate', '未打开命令面板时 Escape 交给编辑器')

console.log('chat composer keyboard regression passed')
