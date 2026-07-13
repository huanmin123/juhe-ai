import type { ChatMessage, ChatMessageStatus } from '@/types/domain/chat'

export interface ChatSubmissionReconciliation {
  messages: ChatMessage[]
  accepted: boolean
  terminal: boolean
  turnId?: string
  assistantStatus?: ChatMessageStatus
}

const retryDelays = [50, 100, 200, 300, 500, 750, 750]

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
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    latest = await input.listMessages()
    const userMessage = latest.find((item) => item.role === 'user' && item.clientMessageId === input.clientMessageId)
    knownTurnId = userMessage?.turnId ?? knownTurnId
    const accepted = Boolean(knownTurnId)
    const assistant = knownTurnId
      ? latest.find((item) => item.role === 'assistant' && item.turnId === knownTurnId)
      : undefined
    const terminal = Boolean(assistant && assistant.status !== 'streaming')
    if (terminal || (!accepted && !input.confirmPendingAcceptance) || attempt === maxAttempts - 1) {
      return { messages: latest, accepted, terminal, turnId: knownTurnId, assistantStatus: assistant?.status }
    }
    if (accepted) {
      try { await input.stop() } catch {}
    }
    await wait(retryDelays[Math.min(attempt, retryDelays.length - 1)]!)
  }
  return { messages: latest, accepted: Boolean(knownTurnId), terminal: false, turnId: knownTurnId }
}

function waitFor(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds))
}
