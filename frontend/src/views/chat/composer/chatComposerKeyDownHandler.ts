import type { EditorState } from '@tiptap/pm/state'

import { resolveChatComposerKeyAction } from './chatComposerKeyboard'

interface ChatComposerKeyDownView {
  state: EditorState
}

interface ChatComposerKeyDownContext {
  commandOpen: () => boolean
  commandItemCount: () => number
  moveCommand: (direction: 'next' | 'previous') => void
  selectCommand: () => boolean
  closeCommand: () => void
  submit: () => void
}

const structuredBlockNames = new Set(['bulletList', 'orderedList', 'listItem', 'blockquote', 'codeBlock', 'heading'])

export function isChatComposerStructuredBlock(state: EditorState): boolean {
  const { $from } = state.selection
  for (let depth = $from.depth; depth >= 0; depth -= 1) {
    if (structuredBlockNames.has($from.node(depth).type.name)) return true
  }
  return false
}

export function createChatComposerKeyDownHandler(context: ChatComposerKeyDownContext) {
  return (view: ChatComposerKeyDownView, event: KeyboardEvent): boolean => {
    const action = resolveChatComposerKeyAction({
      key: event.key,
      isComposing: event.isComposing,
      shiftKey: event.shiftKey,
      ctrlKey: event.ctrlKey,
      metaKey: event.metaKey,
      commandOpen: context.commandOpen(),
      commandItemCount: context.commandItemCount(),
      structuredBlock: isChatComposerStructuredBlock(view.state)
    })
    if (action === 'delegate') return false
    if (typeof action === 'object') {
      event.preventDefault()
      context.moveCommand(action.direction)
      return true
    }
    if (action === 'select-command') {
      if (!context.selectCommand()) return false
      event.preventDefault()
      return true
    }
    if (action === 'close-command') {
      context.closeCommand()
      return true
    }
    event.preventDefault()
    context.submit()
    return true
  }
}
