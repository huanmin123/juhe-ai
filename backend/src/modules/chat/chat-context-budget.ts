export interface ChatContextMessage {
  role: 'user' | 'assistant'
  content: string | Array<{ type: 'input_text' | 'input_image'; text?: string; dataUrl?: string }>
}

import { countChatJsonTokens, countChatTextTokens } from './chat-token-count.js'

const protocolReserveTokens = 4_000
const toolDefinitionReserveTokens = 2_048
const messageOverheadTokens = 12

export class ChatContextBudgetError extends Error {
  constructor(public readonly code: 'chat_input_exceeds_context' = 'chat_input_exceeds_context') {
    super('当前输入超过模型上下文窗口，请缩短消息或减少图片后重试')
    this.name = 'ChatContextBudgetError'
  }
}

interface FixedChatInputBudget {
  currentUserContent: string
  instructions: string
  toolsEnabled: boolean
  imageTokenEstimate: number
  maxInputTokens?: number
}

export function validateFixedChatInputBudget(input: FixedChatInputBudget): void {
  const maxInputTokens = positiveInteger(input.maxInputTokens)
  if (!maxInputTokens) return
  if (fixedChatInputTokens(input) > maxInputTokens) throw new ChatContextBudgetError()
}

export function trimChatContextToBudget(input: FixedChatInputBudget & {
  history: ChatContextMessage[]
}): ChatContextMessage[] {
  validateFixedChatInputBudget(input)
  const completeTurns = toCompleteTurns(input.history)
  const maxInputTokens = positiveInteger(input.maxInputTokens)
  if (!maxInputTokens) return completeTurns.flat()
  const historyBudget = maxInputTokens - fixedChatInputTokens(input)
  let usedTokens = 0
  const selected: ChatContextMessage[][] = []
  for (let index = completeTurns.length - 1; index >= 0; index -= 1) {
    const turn = completeTurns[index]
    const turnTokens = turn.reduce((total, message) => total + estimateChatContentTokens(message.content) + messageOverheadTokens, 0)
    if (usedTokens + turnTokens > historyBudget) break
    selected.push(turn)
    usedTokens += turnTokens
  }
  return selected.reverse().flat()
}

export function estimateChatInputTokens(input: FixedChatInputBudget & { history: ChatContextMessage[] }): number {
  return fixedChatInputTokens(input) + input.history.reduce(
    (total, message) => total + estimateChatContentTokens(message.content) + messageOverheadTokens,
    0
  )
}

function fixedChatInputTokens(input: FixedChatInputBudget): number {
  return protocolReserveTokens
    + estimateChatTokens(input.instructions)
    + estimateChatTokens(input.currentUserContent)
    + messageOverheadTokens * 2
    + (input.toolsEnabled ? toolDefinitionReserveTokens : 0)
    + Math.max(0, Math.floor(input.imageTokenEstimate))
}

export function estimateChatTokens(content: string): number {
  return Math.max(1, countChatTextTokens(content))
}

function estimateChatContentTokens(content: ChatContextMessage['content']): number {
  return typeof content === 'string' ? estimateChatTokens(content) : Math.max(1, countChatJsonTokens(content))
}

function toCompleteTurns(history: ChatContextMessage[]): ChatContextMessage[][] {
  const turns: ChatContextMessage[][] = []
  for (let index = 0; index + 1 < history.length; index += 2) {
    const user = history[index]
    const assistant = history[index + 1]
    if (user.role !== 'user' || assistant.role !== 'assistant') continue
    turns.push([user, assistant])
  }
  return turns
}

function positiveInteger(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}
