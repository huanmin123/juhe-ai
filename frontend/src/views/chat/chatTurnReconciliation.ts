import type { ChatMessage, ChatMessageStatus } from '@/types/domain/chat'

export interface ChatSubmissionReconciliation {
  messages: ChatMessage[]
  confirmed: boolean
  accepted: boolean
  terminal: boolean
  turnId?: string
  assistantStatus?: ChatMessageStatus
}

const retryDelays = [50, 100, 200, 300, 500, 750, 750]

export async function applyChatReconciliationIfActive<T>(input: {
  reconcile: () => Promise<T>
  isDisposed: () => boolean
  apply: (reconciliation: T) => Promise<void> | void
}): Promise<boolean> {
  const reconciliation = await input.reconcile()
  if (input.isDisposed()) return false
  await input.apply(reconciliation)
  return true
}

export async function reconcileChatSubmission(input: {
  clientMessageId: string
  acceptedTurnId?: string
  confirmPendingAcceptance?: boolean
  listMessages: () => Promise<ChatMessage[]>
  stop: () => Promise<void>
  wait?: (milliseconds: number) => Promise<void>
  maxAttempts?: number
}): Promise<ChatSubmissionReconciliation> {
  const maxAttempts = Math.max(1, Math.min(input.maxAttempts ?? 8, 8))
  const wait = input.wait ?? waitFor
  let latest: ChatMessage[] = []
  let knownTurnId = input.acceptedTurnId
  let acceptedReadConfirmed = false
  let lastAssistantStatus: ChatMessageStatus | undefined
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    try {
      latest = await input.listMessages()
    } catch {
      if (attempt === maxAttempts - 1) {
        return { messages: latest, confirmed: acceptedReadConfirmed, accepted: Boolean(knownTurnId), terminal: false, turnId: knownTurnId, assistantStatus: lastAssistantStatus }
      }
      await wait(retryDelays[Math.min(attempt, retryDelays.length - 1)]!)
      continue
    }
    const userMessage = latest.find((item) => item.role === 'user' && item.clientMessageId === input.clientMessageId)
    knownTurnId = userMessage?.turnId ?? knownTurnId
    const accepted = Boolean(knownTurnId)
    const assistant = knownTurnId
      ? latest.find((item) => item.role === 'assistant' && item.turnId === knownTurnId)
      : undefined
    acceptedReadConfirmed ||= Boolean(knownTurnId && latest.some((item) => item.turnId === knownTurnId))
    lastAssistantStatus = assistant?.status ?? lastAssistantStatus
    const terminal = Boolean(assistant && assistant.status !== 'streaming')
    if (terminal || (!accepted && !input.confirmPendingAcceptance) || attempt === maxAttempts - 1) {
      return { messages: latest, confirmed: true, accepted, terminal, turnId: knownTurnId, assistantStatus: assistant?.status }
    }
    if (accepted) {
      try { await input.stop() } catch {}
    }
    await wait(retryDelays[Math.min(attempt, retryDelays.length - 1)]!)
  }
  return { messages: latest, confirmed: false, accepted: Boolean(knownTurnId), terminal: false, turnId: knownTurnId, assistantStatus: lastAssistantStatus }
}

function waitFor(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds))
}
