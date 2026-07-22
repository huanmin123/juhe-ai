import assert from 'node:assert/strict'

import { history, undo } from '@tiptap/pm/history'
import { Schema } from '@tiptap/pm/model'
import { EditorState } from '@tiptap/pm/state'

import { replaceEditorContentWithoutHistory } from '../../views/chat/composer/chatEditorDocumentBoundary'

const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { content: 'inline*', group: 'block' },
    text: { group: 'inline' }
  }
})
const paragraph = (text: string) => schema.node('doc', undefined, [schema.node('paragraph', undefined, text ? [schema.text(text)] : [])])
let state = EditorState.create({ schema, doc: paragraph('被挤走的原草稿'), plugins: [history()] })
state = state.apply(state.tr.insertText('可撤销输入', state.doc.content.size - 1))
assert.equal(undo(state), true, '边界前真实 ProseMirror history 必须存在')

const editorBoundary = {
  schema,
  get state() { return state },
  view: { updateState(next: EditorState) { state = next } },
  commands: {
    setContent(document: Record<string, unknown>) {
      const nextDoc = schema.nodeFromJSON(document)
      state = state.apply(state.tr.replaceWith(0, state.doc.content.size, nextDoc.content))
      return true
    }
  }
}
replaceEditorContentWithoutHistory(editorBoundary, { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '最近消息 Markdown' }] }] })
assert.equal(state.doc.textContent, '最近消息 Markdown')
assert.equal(undo(state), false, 'setText/restore 边界后 Ctrl+Z 不能回到 displaced draft')

state = state.apply(state.tr.insertText(' 新输入', state.doc.content.size - 1))
assert.equal(undo(state), true, '边界内正常键入仍必须可以撤销')

replaceEditorContentWithoutHistory(editorBoundary, { type: 'doc', content: [{ type: 'paragraph' }] })
assert.equal(state.doc.textContent, '')
assert.equal(undo(state), false, '成功发送后的空文档不能通过 Ctrl+Z 恢复已发送草稿')

console.log('AIComposer ProseMirror UndoRedo 边界隔离回归通过')
