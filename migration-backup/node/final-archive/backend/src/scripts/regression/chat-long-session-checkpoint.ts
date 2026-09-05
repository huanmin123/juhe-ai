import { createHash } from 'node:crypto'

import type { ChatLongSessionResponse, ChatLongSessionTurn } from './chat-long-session-fixture.js'

export interface ChatLongSessionResumeMessage {
  id: string
  turnId: string
  sequenceNo: number
  role: 'user' | 'assistant'
  status: 'completed' | 'streaming' | 'failed' | 'canceled'
  contentText: string
}

export interface ChatLongSessionResumePlan {
  lastCompletedTurn: number
  nextTurn: number
  replaceTurnId?: string
  completedResponses: ChatLongSessionResponse[]
}

export function buildChatLongSessionAttemptIdentity(
  turn: number,
  attempt: number,
  resumeInvocationId?: string
): { clientMessageId: string; traceId: string } {
  const turnNumber = String(turn).padStart(2, '0')
  const attemptSuffix = attempt === 1 ? '' : `-retry-${attempt}`
  const resumeSuffix = resumeInvocationId ? `-resume-${resumeInvocationId}` : ''
  return {
    clientMessageId: `long-real-${turnNumber}${attemptSuffix}${resumeSuffix}`,
    traceId: `chat-long-main-${turnNumber}${attemptSuffix}${resumeSuffix}`
  }
}

export function buildChatLongSessionResumePlan(
  fixture: readonly ChatLongSessionTurn[],
  inputMessages: readonly ChatLongSessionResumeMessage[]
): ChatLongSessionResumePlan {
  const messages = [...inputMessages].sort((left, right) => left.sequenceNo - right.sequenceNo)
  if (messages.length % 2 !== 0) throw new Error('chat long session resume message pairs are incomplete')
  const completedResponses: ChatLongSessionResponse[] = []
  let replaceTurnId: string | undefined
  for (let index = 0; index < messages.length; index += 2) {
    const user = messages[index]!
    const assistant = messages[index + 1]!
    const turn = fixture[index / 2]
    if (!turn) throw new Error('chat long session resume exceeds fixture')
    if (user.role !== 'user' || assistant.role !== 'assistant' || user.turnId !== assistant.turnId) {
      throw new Error('chat long session resume message pair mismatch')
    }
    if (hash(user.contentText) !== hash(turn.prompt)) throw new Error(`fixture prompt hash mismatch at turn ${turn.turn}`)
    if (assistant.status === 'completed') {
      if (replaceTurnId) throw new Error('chat long session resume has completed turn after terminal failure')
      completedResponses.push({ turn: turn.turn, assistantOutput: assistant.contentText })
      continue
    }
    if (index + 2 !== messages.length) throw new Error('chat long session resume terminal failure must be the latest turn')
    replaceTurnId = assistant.turnId
  }
  return {
    lastCompletedTurn: completedResponses.length,
    nextTurn: completedResponses.length + 1,
    ...(replaceTurnId ? { replaceTurnId } : {}),
    completedResponses
  }
}

export function chatLongSessionFixtureHash(fixture: readonly ChatLongSessionTurn[]): string {
  return hash(JSON.stringify(fixture))
}

export function chatLongSessionResumeCanonicalHash(
  inputMessages: readonly ChatLongSessionResumeMessage[],
  completedTurns: number
): string {
  const rows = [...inputMessages]
    .sort((left, right) => left.sequenceNo - right.sequenceNo)
    .slice(0, Math.max(0, completedTurns) * 2)
    .map((message) => ({
      id: message.id,
      turnId: message.turnId,
      sequenceNo: message.sequenceNo,
      role: message.role,
      status: message.status,
      contentHash: hash(message.contentText)
    }))
  return hash(JSON.stringify(rows))
}

function hash(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}
