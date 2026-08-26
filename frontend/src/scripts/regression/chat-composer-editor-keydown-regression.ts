import assert from 'node:assert/strict'
import { Editor } from '@tiptap/core'
import { TextSelection } from '@tiptap/pm/state'
import StarterKit from '@tiptap/starter-kit'
import { Window } from 'happy-dom'

import { createChatComposerKeyDownHandler } from '../../views/chat/composer/chatComposerKeyDownHandler'

const domWindow = new Window({ url: 'http://127.0.0.1/' })
Object.assign(globalThis, {
  window: domWindow,
  document: domWindow.document,
  Node: domWindow.Node,
  Element: domWindow.Element,
  HTMLElement: domWindow.HTMLElement,
  MutationObserver: domWindow.MutationObserver,
  DOMParser: domWindow.DOMParser,
  getComputedStyle: domWindow.getComputedStyle.bind(domWindow)
})

if (typeof globalThis.KeyboardEvent === 'undefined') {
  class NodeKeyboardEvent {
    key: string
    code: string
    shiftKey: boolean
    ctrlKey: boolean
    altKey: boolean
    metaKey: boolean
    constructor(_type: string, init: KeyboardEventInit = {}) {
      this.key = init.key ?? ''
      this.code = init.code ?? ''
      this.shiftKey = Boolean(init.shiftKey)
      this.ctrlKey = Boolean(init.ctrlKey)
      this.altKey = Boolean(init.altKey)
      this.metaKey = Boolean(init.metaKey)
    }
    preventDefault(): void {}
  }
  Object.assign(globalThis, { KeyboardEvent: NodeKeyboardEvent })
}

interface TestKeyboardEvent {
  key: string
  isComposing: boolean
  shiftKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  defaultPrevented: boolean
  preventDefault: () => void
}

function keyboardEvent(input: Partial<TestKeyboardEvent> = {}): TestKeyboardEvent {
  const event: TestKeyboardEvent = {
    key: 'Enter',
    isComposing: false,
    shiftKey: false,
    ctrlKey: false,
    metaKey: false,
    defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true },
    ...input
  }
  return event
}

function createEditor(content: Record<string, unknown>): Editor {
  return new Editor({ element: null, extensions: [StarterKit], content })
}

function createMountedEditor(content: Record<string, unknown>): Editor {
  const element = document.createElement('div')
  document.body.append(element)
  return new Editor({ element, extensions: [StarterKit], content })
}

function selectDocumentEnd(editor: Editor): void {
  editor.commands.setTextSelection(TextSelection.atEnd(editor.state.doc).from)
}

function typeThroughStarterKitInputRules(editor: Editor, text: string): void {
  for (const character of text) {
    const { from, to } = editor.view.state.selection
    let handled = false
    editor.view.someProp('handleTextInput', (handler) => {
      if (!handler(editor.view, from, to, character)) return false
      handled = true
      return true
    })
    if (!handled) editor.view.dispatch(editor.view.state.tr.insertText(character, from, to))
  }
}

function dispatchEnter(editor: Editor, event = keyboardEvent()): { handled: boolean; submits: number; event: TestKeyboardEvent } {
  let submits = 0
  const handler = createChatComposerKeyDownHandler({
    commandOpen: () => false,
    commandItemCount: () => 0,
    moveCommand: () => undefined,
    selectCommand: () => false,
    closeCommand: () => undefined,
    submit: () => { submits += 1 }
  })
  const handled = handler({ state: editor.state }, event as KeyboardEvent)
  if (!handled && event.key === 'Enter' && !event.isComposing && !event.shiftKey) {
    if (editor.isActive('bulletList') || editor.isActive('orderedList')) {
      if (editor.state.selection.$from.parent.content.size === 0) editor.commands.liftListItem('listItem')
      else editor.commands.splitListItem('listItem')
    } else if (editor.isActive('codeBlock')) {
      editor.commands.newlineInCode()
    } else {
      editor.commands.splitBlock()
    }
  }
  return { handled, submits, event }
}

const listEditor = createEditor({ type: 'doc', content: [{ type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '手机列表' }] }] }] }] })
selectDocumentEnd(listEditor)
const listContinuation = dispatchEnter(listEditor)
assert.equal(listContinuation.handled, false, '列表 Enter 必须交给 Tiptap')
assert.equal(listContinuation.submits, 0, '列表 Enter 不能提交消息')
assert.equal(listEditor.getJSON().content?.[0]?.content?.length, 2, '真实 Tiptap Enter 必须续写第二个列表项')

const listExit = dispatchEnter(listEditor)
assert.equal(listExit.handled, false, '空列表项 Enter 必须继续交给 Tiptap')
assert.equal(listExit.submits, 0, '双 Enter 退出列表时不能提交')
assert.equal(listEditor.getJSON().content?.at(-1)?.type, 'paragraph', '第二次真实 Tiptap Enter 必须退出列表到普通段落')

const paragraphSubmit = dispatchEnter(listEditor)
assert.equal(paragraphSubmit.handled, true, '退出列表后的普通段落 Enter 必须由聊天发送处理')
assert.equal(paragraphSubmit.submits, 1)
assert.equal(paragraphSubmit.event.defaultPrevented, true)
listEditor.destroy()

const typedListEditor = createMountedEditor({ type: 'doc', content: [{ type: 'paragraph' }] })
typeThroughStarterKitInputRules(typedListEditor, '- 验收列表')
assert.equal(typedListEditor.getJSON().content?.[0]?.type, 'bulletList', '真实逐键输入必须触发 StarterKit 列表 input rule')
const typedListEnter = dispatchEnter(typedListEditor)
assert.equal(typedListEnter.handled, false, '逐键输入形成的列表 Enter 必须交给 Tiptap')
assert.equal(typedListEnter.submits, 0, '逐键输入形成的列表不能被自动提交')
typedListEditor.destroy()

const headingEditor = createEditor({ type: 'doc', content: [{ type: 'heading', attrs: { level: 2 }, content: [{ type: 'text', text: '标题' }] }] })
selectDocumentEnd(headingEditor)
const headingExit = dispatchEnter(headingEditor)
assert.equal(headingExit.handled, false, '标题末尾 Enter 必须交给 Tiptap 退出到普通段落')
assert.equal(headingExit.submits, 0, '标题 Enter 不能提交消息')
assert.equal(headingEditor.getJSON().content?.at(-1)?.type, 'paragraph', '标题末尾真实 Tiptap Enter 必须创建普通段落')
const headingParagraphSubmit = dispatchEnter(headingEditor)
assert.equal(headingParagraphSubmit.handled, true, '标题退出后的普通段落 Enter 才能提交')
assert.equal(headingParagraphSubmit.submits, 1)
headingEditor.destroy()

for (const type of ['blockquote', 'codeBlock'] as const) {
  const content = type === 'blockquote'
    ? { type: 'doc', content: [{ type, content: [{ type: 'paragraph', content: [{ type: 'text', text: '结构内容' }] }] }] }
    : { type: 'doc', content: [{ type, content: [{ type: 'text', text: 'const value = 1' }] }] }
  const editor = createEditor(content)
  selectDocumentEnd(editor)
  const result = dispatchEnter(editor)
  assert.equal(result.handled, false, `${type} Enter 必须交给 Tiptap`)
  assert.equal(result.submits, 0, `${type} Enter 不能提交消息`)
  editor.destroy()
}

const paragraphEditor = createEditor({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '普通消息' }] }] })
selectDocumentEnd(paragraphEditor)
for (const input of [
  { shiftKey: true },
  { isComposing: true },
] as const) {
  const result = dispatchEnter(paragraphEditor, keyboardEvent(input))
  assert.equal(result.handled, false, 'Shift+Enter 与 IME Enter 必须交给编辑器')
  assert.equal(result.submits, 0)
}
for (const input of [{ ctrlKey: true }, { metaKey: true }] as const) {
  const result = dispatchEnter(paragraphEditor, keyboardEvent(input))
  assert.equal(result.handled, true, 'Ctrl/Meta+Enter 必须强制发送')
  assert.equal(result.submits, 1)
}
paragraphEditor.destroy()

console.log('chat composer real editor keydown regression passed')
