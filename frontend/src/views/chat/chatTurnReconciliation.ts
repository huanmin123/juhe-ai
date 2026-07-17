import type { ChatMessage, ChatMessageStatus, ChatSubmissionStatus } from '@/types/domain/chat'
import type { ChatPendingSubmission } from './chatPendingSubmissionStorage'

export interface ChatSubmissionReconciliation {
  messages: ChatMessage[]
  confirmed: boolean
  accepted: boolean
  terminal: boolean
  submissionState?: ChatSubmissionStatus['state']
  turnId?: string
  assistantStatus?: ChatMessageStatus
  lookupError?: unknown
}

export type ChatPendingConversationAvailability = 'ready' | 'not_found' | 'retry'

export interface ChatPendingSubmissionRecovery {
  action: 'missing' | 'retry' | 'apply'
  pending: ChatPendingSubmission
  reconciliation?: ChatSubmissionReconciliation
}

const retryDelays = [100, 200, 350, 500, 750, 1_000, 1_500]
const requiredNotFoundConfirmations = 3
const notFoundGraceMilliseconds = 1_000
const terminalStatuses = new Set<ChatMessageStatus>(['completed', 'failed', 'canceled'])

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

export async function reconcileChatPendingSubmissionRecovery(input: {
  pending: ChatPendingSubmission
  ensureConversation: () => Promise<ChatPendingConversationAvailability>
  reconcile: (initial: {
    initialAcceptedTurnId?: string
    initialAssistantStatus?: ChatMessageStatus
  }) => Promise<ChatSubmissionReconciliation>
}): Promise<ChatPendingSubmissionRecovery> {
  const availability = await input.ensureConversation()
  if (availability === 'not_found') return { action: 'missing', pending: input.pending }

  const initialAcceptedTurnId = input.pending.startedTurnId
  const initialAssistantStatus = input.pending.acceptedAssistantStatus
    ?? (input.pending.streamStarted && initialAcceptedTurnId ? 'streaming' : undefined)
  const reconciliation = await input.reconcile({ initialAcceptedTurnId, initialAssistantStatus })
  const pending = reconciliation.accepted
    ? {
        ...input.pending,
        streamStarted: true,
        startedTurnId: reconciliation.turnId ?? input.pending.startedTurnId,
        acceptedAssistantStatus: reconciliation.assistantStatus ?? initialAssistantStatus
      }
    : input.pending

  if (availability !== 'ready' || !reconciliation.confirmed || (reconciliation.accepted && !reconciliation.terminal)) {
    return { action: 'retry', pending, reconciliation }
  }
  return { action: 'apply', pending, reconciliation }
}

export async function reconcileChatSubmission(input: {
  initialAcceptedTurnId?: string
  initialAssistantStatus?: ChatMessageStatus
  getSubmissionStatus: () => Promise<ChatSubmissionStatus>
  listMessages: () => Promise<ChatMessage[]>
  stop: (turnId: string) => Promise<void>
  wait?: (milliseconds: number) => Promise<void>
  now?: () => number
  maxAttempts?: number
}): Promise<ChatSubmissionReconciliation> {
  const maxAttempts = Math.max(1, Math.min(input.maxAttempts ?? 8, 8))
  const wait = input.wait ?? waitFor
  const now = input.now ?? Date.now
  let latest: ChatMessage[] = []
  let knownTurnId = normalizeTurnId(input.initialAcceptedTurnId)
  let lastAssistantStatus: ChatMessageStatus | undefined = knownTurnId
    ? input.initialAssistantStatus ?? 'streaming'
    : undefined
  let terminalStatus: Exclude<ChatMessageStatus, 'streaming'> | undefined = lastAssistantStatus && terminalStatuses.has(lastAssistantStatus)
    ? lastAssistantStatus as Exclude<ChatMessageStatus, 'streaming'>
    : undefined
  let notFoundConfirmations = 0
  let notFoundStartedAt: number | undefined
  let lookupError: unknown

  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    if (knownTurnId && terminalStatus) {
      try {
        latest = await input.listMessages()
        return acceptedResult({
          messages: latest,
          confirmed: true,
          terminal: true,
          turnId: knownTurnId,
          assistantStatus: terminalStatus
        })
      } catch (error) {
        lookupError = error
        if (attempt === maxAttempts - 1) {
          return acceptedResult({
            messages: latest,
            confirmed: false,
            terminal: false,
            turnId: knownTurnId,
            assistantStatus: terminalStatus,
            lookupError
          })
        }
        await wait(retryDelay(attempt))
        continue
      }
    }

    let rawStatus: unknown
    try {
      rawStatus = await input.getSubmissionStatus()
    } catch (error) {
      lookupError = error
      resetNotFoundWindow()
      if (!isRetryableChatSubmissionLookupError(error)) {
        if (knownTurnId) {
          return acceptedResult({
            messages: latest,
            confirmed: true,
            terminal: false,
            turnId: knownTurnId,
            assistantStatus: lastAssistantStatus,
            lookupError
          })
        }
        return { messages: latest, confirmed: true, accepted: false, terminal: false, lookupError }
      }
      if (attempt === maxAttempts - 1) return unresolvedResult()
      await wait(retryDelay(attempt))
      continue
    }

    if (!isChatSubmissionStatus(rawStatus)) {
      lookupError = new Error('提交状态接口返回了无效响应')
      resetNotFoundWindow()
      if (attempt === maxAttempts - 1) return unresolvedResult()
      await wait(retryDelay(attempt))
      continue
    }

    const status = rawStatus
    if (knownTurnId) {
      if (status.state !== 'accepted' || status.turnId !== knownTurnId) {
        lookupError = new Error('已接受的提交状态发生了非单调变化')
        resetNotFoundWindow()
        if (attempt === maxAttempts - 1) return unresolvedResult()
        await wait(retryDelay(attempt))
        continue
      }
    } else if (status.state === 'not_found') {
      const checkedAt = now()
      if (notFoundStartedAt === undefined) notFoundStartedAt = checkedAt
      notFoundConfirmations += 1
      if (notFoundConfirmations >= requiredNotFoundConfirmations && checkedAt - notFoundStartedAt >= notFoundGraceMilliseconds) {
        return { messages: latest, confirmed: true, accepted: false, terminal: false, submissionState: 'not_found' }
      }
      if (attempt === maxAttempts - 1) return unresolvedResult('not_found')
      const graceRemaining = notFoundConfirmations >= requiredNotFoundConfirmations
        ? Math.max(0, notFoundGraceMilliseconds - (checkedAt - notFoundStartedAt))
        : 0
      await wait(Math.max(retryDelay(attempt), graceRemaining))
      continue
    } else if (status.state === 'preparing') {
      resetNotFoundWindow()
      if (attempt === maxAttempts - 1) return unresolvedResult('preparing')
      await wait(retryDelay(attempt))
      continue
    }

    if (status.state !== 'accepted') {
      lookupError = new Error('提交状态接口返回了无法识别的状态')
      resetNotFoundWindow()
      if (attempt === maxAttempts - 1) return unresolvedResult()
      await wait(retryDelay(attempt))
      continue
    }

    resetNotFoundWindow()
    knownTurnId = status.turnId
    lastAssistantStatus = status.assistantStatus
    if (terminalStatuses.has(status.assistantStatus)) {
      terminalStatus = status.assistantStatus as Exclude<ChatMessageStatus, 'streaming'>
      try {
        latest = await input.listMessages()
        return acceptedResult({
          messages: latest,
          confirmed: true,
          terminal: true,
          turnId: knownTurnId,
          assistantStatus: terminalStatus
        })
      } catch (error) {
        lookupError = error
        if (attempt === maxAttempts - 1) return unresolvedResult('accepted')
        await wait(retryDelay(attempt))
        continue
      }
    }

    if (attempt === maxAttempts - 1) return unresolvedResult('accepted')
    await wait(retryDelay(attempt))
  }

  return unresolvedResult()

  function resetNotFoundWindow(): void {
    notFoundConfirmations = 0
    notFoundStartedAt = undefined
  }

  function unresolvedResult(submissionState?: ChatSubmissionStatus['state']): ChatSubmissionReconciliation {
    if (knownTurnId) {
      return acceptedResult({
        messages: latest,
        confirmed: terminalStatus === undefined,
        terminal: false,
        submissionState: submissionState ?? 'accepted',
        turnId: knownTurnId,
        assistantStatus: lastAssistantStatus,
        lookupError
      })
    }
    return { messages: latest, confirmed: false, accepted: false, terminal: false, submissionState, lookupError }
  }
}

export function isRetryableChatSubmissionLookupError(error: unknown): boolean {
  const candidate = error as { status?: unknown; response?: { status?: unknown } } | undefined
  const rawStatus = candidate?.response?.status ?? candidate?.status
  if (typeof rawStatus !== 'number') return true
  return rawStatus === 408 || rawStatus === 425 || rawStatus === 429 || rawStatus >= 500
}

function isChatSubmissionStatus(value: unknown): value is ChatSubmissionStatus {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const candidate = value as { state?: unknown; turnId?: unknown; assistantStatus?: unknown }
  if (candidate.state === 'preparing' || candidate.state === 'not_found') return true
  return candidate.state === 'accepted'
    && typeof candidate.turnId === 'string'
    && Boolean(candidate.turnId.trim())
    && (candidate.assistantStatus === 'streaming' || terminalStatuses.has(candidate.assistantStatus as ChatMessageStatus))
}

function normalizeTurnId(value: unknown): string | undefined {
  return typeof value === 'string' && Boolean(value.trim()) ? value : undefined
}

function acceptedResult(input: {
  messages: ChatMessage[]
  confirmed: boolean
  terminal: boolean
  submissionState?: ChatSubmissionStatus['state']
  turnId: string
  assistantStatus?: ChatMessageStatus
  lookupError?: unknown
}): ChatSubmissionReconciliation {
  return {
    messages: input.messages,
    confirmed: input.confirmed,
    accepted: true,
    terminal: input.terminal,
    submissionState: input.submissionState ?? 'accepted',
    turnId: input.turnId,
    assistantStatus: input.assistantStatus,
    lookupError: input.lookupError
  }
}

function retryDelay(attempt: number): number {
  return retryDelays[Math.min(attempt, retryDelays.length - 1)]!
}

function waitFor(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds))
}
