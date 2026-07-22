import assert from 'node:assert/strict'

import { Schema } from '@tiptap/pm/model'
import { EditorState, TextSelection } from '@tiptap/pm/state'

import {
  chatComposerCommands,
  filterChatComposerCommands,
  findChatComposerCommandQuery,
  moveChatComposerCommandIndex
} from '../../views/chat/composer/chatComposerCommands'

const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { content: 'inline*', group: 'block' },
    text: { group: 'inline' },
    hard_break: { inline: true, group: 'inline' },
    inline_atom: { inline: true, group: 'inline', atom: true },
    bullet_list: { content: 'list_item+', group: 'block' },
    list_item: { content: 'paragraph block*' }
  }
})

const paragraph = (text = '') => schema.node('paragraph', null, text ? schema.text(text) : undefined)
const stateAt = (doc: ReturnType<typeof schema.node>, anchor: number, head = anchor) =>
  EditorState.create({ doc, selection: TextSelection.create(doc, anchor, head) })

assert.deepEqual(filterChatComposerCommands('代码').map((item) => item.key), ['code'])
assert.deepEqual(filterChatComposerCommands('列表'), [])
assert.deepEqual(chatComposerCommands.map((item) => item.key), ['clear-input', 'code', 'image', 'image-model', 'compact', 'clear'])
assert.deepEqual(chatComposerCommands.find((item) => item.key === 'clear-input'), {
  key: 'clear-input', kind: 'editor', label: '清空输入', description: '清除当前编辑内容', insert: ''
})
assert.deepEqual(chatComposerCommands.find((item) => item.key === 'compact'), {
  key: 'compact', kind: 'conversation', action: 'compact-context', label: '压缩上下文', description: '整理当前会话的较早内容'
})
assert.deepEqual(chatComposerCommands.find((item) => item.key === 'image-model'), {
  key: 'image-model', kind: 'conversation', action: 'set-image-model', label: '默认图像模型', description: '设置当前会话的图片生成模型'
})
assert.deepEqual(chatComposerCommands.find((item) => item.key === 'clear'), {
  key: 'clear', kind: 'conversation', action: 'clear-conversation', label: '清空会话', description: '清除消息但保留会话壳'
})
assert.equal(moveChatComposerCommandIndex(0, -1, 3), 2)
assert.equal(moveChatComposerCommandIndex(2, 1, 3), 0)
assert.equal(moveChatComposerCommandIndex(0, 1, 0), 0)

const codeDoc = schema.node('doc', null, [paragraph('/code')])
assert.deepEqual(findChatComposerCommandQuery(stateAt(codeDoc, 6)), {
  query: 'code',
  range: { from: 1, to: 6 }
})

const multiParagraphDoc = schema.node('doc', null, [paragraph('第一段'), paragraph('输入 /co 后续'), paragraph('第三段')])
assert.deepEqual(findChatComposerCommandQuery(stateAt(multiParagraphDoc, 12)), {
  query: 'co',
  range: { from: 9, to: 12 }
})

const leadingWhitespaceDoc = schema.node('doc', null, [paragraph('  /image')])
assert.deepEqual(findChatComposerCommandQuery(stateAt(leadingWhitespaceDoc, 9)), {
  query: 'image',
  range: { from: 3, to: 9 }
})

const listExitDoc = schema.node('doc', null, [
  schema.node('bullet_list', null, [schema.node('list_item', null, [paragraph('项目')])]),
  paragraph(),
  paragraph('/')
])
assert.deepEqual(findChatComposerCommandQuery(stateAt(listExitDoc, 12)), {
  query: '',
  range: { from: 11, to: 12 }
})

const contentAfterCursorDoc = schema.node('doc', null, [paragraph('前缀 /code 后续')])
assert.deepEqual(findChatComposerCommandQuery(stateAt(contentAfterCursorDoc, 9)), {
  query: 'code',
  range: { from: 4, to: 9 }
})

assert.equal(findChatComposerCommandQuery(stateAt(codeDoc, 1, 6)), undefined)

const noSlashDoc = schema.node('doc', null, [paragraph('plain text')])
assert.equal(findChatComposerCommandQuery(stateAt(noSlashDoc, 11)), undefined)

const secondSlashDoc = schema.node('doc', null, [paragraph('/code/next')])
assert.equal(findChatComposerCommandQuery(stateAt(secondSlashDoc, 11)), undefined)

const inlineAtomDoc = schema.node('doc', null, [
  schema.node('paragraph', null, [schema.node('inline_atom'), schema.text('/code')])
])
const inlineAtomState = stateAt(inlineAtomDoc, 7)
const inlineAtomCommand = findChatComposerCommandQuery(inlineAtomState)
assert.deepEqual(inlineAtomCommand, { query: 'code', range: { from: 2, to: 7 } })
assert.deepEqual(inlineAtomState.apply(inlineAtomState.tr.delete(inlineAtomCommand!.range.from, inlineAtomCommand!.range.to)).doc.toJSON(), {
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'inline_atom' }] }]
}, '删除命令 query 时必须保留前导行内图片节点')

const hardBreakDoc = schema.node('doc', null, [
  schema.node('paragraph', null, [schema.text('前文'), schema.node('hard_break'), schema.text('/code')])
])
const hardBreakState = stateAt(hardBreakDoc, 9)
const hardBreakCommand = findChatComposerCommandQuery(hardBreakState)
assert.deepEqual(hardBreakCommand, { query: 'code', range: { from: 4, to: 9 } })
assert.deepEqual(hardBreakState.apply(hardBreakState.tr.delete(hardBreakCommand!.range.from, hardBreakCommand!.range.to)).doc.toJSON(), {
  type: 'doc',
  content: [{ type: 'paragraph', content: [{ type: 'text', text: '前文' }, { type: 'hard_break' }] }]
}, '删除命令 query 时必须保留前导 hardBreak')

console.log('chat composer commands regression passed')
