export interface ChatComposerKeyInput {
  key: string
  isComposing: boolean
  shiftKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  commandOpen: boolean
  commandItemCount: number
  structuredBlock: boolean
}

export type ChatComposerKeyAction =
  | 'delegate'
  | 'submit'
  | 'select-command'
  | 'close-command'
  | { type: 'move-command'; direction: 'next' | 'previous' }

export function resolveChatComposerKeyAction(input: ChatComposerKeyInput): ChatComposerKeyAction {
  if (input.isComposing) return 'delegate'

  const hasCommandItems = input.commandOpen && input.commandItemCount > 0
  if (hasCommandItems && input.key === 'ArrowDown') return { type: 'move-command', direction: 'next' }
  if (hasCommandItems && input.key === 'ArrowUp') return { type: 'move-command', direction: 'previous' }
  if (hasCommandItems && input.key === 'Enter') return 'select-command'
  if (input.commandOpen && input.key === 'Escape') return 'close-command'

  if (input.key !== 'Enter') return 'delegate'
  if (input.ctrlKey || input.metaKey) return 'submit'
  if (input.shiftKey || input.structuredBlock) return 'delegate'
  return 'submit'
}
