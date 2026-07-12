export interface ChatContextMessage {
  role: 'user' | 'assistant'
  content: string
}

const unknownModelContextTokens = 16_000
const maxHistoryTokens = 64_000
const outputReserveTokens = 8_000
const protocolReserveTokens = 4_000
const toolDefinitionReserveTokens = 2_048
const imageReserveTokens = 4_096
const messageOverheadTokens = 12

export class ChatContextBudgetError extends Error {
  constructor(public readonly code: 'chat_input_exceeds_context' = 'chat_input_exceeds_context') {
    super('当前输入超过模型上下文窗口，请缩短消息或减少图片后重试')
    this.name = 'ChatContextBudgetError'
  }
}

export function trimChatContextToBudget(input: {
  history: ChatContextMessage[]
  currentUserContent: string
  instructions: string
  toolsEnabled: boolean
  imageCount: number
  contextWindowTokens?: number
}): ChatContextMessage[] {
  const contextWindow = positiveInteger(input.contextWindowTokens) ?? unknownModelContextTokens
  const fixedInputTokens = outputReserveTokens
    + protocolReserveTokens
    + estimateChatTokens(input.instructions)
    + estimateChatTokens(input.currentUserContent)
    + messageOverheadTokens * 2
    + (input.toolsEnabled ? toolDefinitionReserveTokens : 0)
    + Math.max(0, Math.floor(input.imageCount)) * imageReserveTokens
  if (fixedInputTokens > contextWindow) throw new ChatContextBudgetError()
  const historyBudget = Math.min(maxHistoryTokens, contextWindow - fixedInputTokens)
  const completeTurns = toCompleteTurns(input.history)
  let usedTokens = 0
  const selected: ChatContextMessage[][] = []
  for (let index = completeTurns.length - 1; index >= 0; index -= 1) {
    const turn = completeTurns[index]
    const turnTokens = turn.reduce((total, message) => total + estimateChatTokens(message.content) + messageOverheadTokens, 0)
    if (usedTokens + turnTokens > historyBudget) break
    selected.push(turn)
    usedTokens += turnTokens
  }
  return selected.reverse().flat()
}

export function resolveEffectiveChatContextWindowTokens(input: {
  clientContextWindowTokens?: number
  serverContextWindowTokens?: number
}): number {
  const serverWindow = positiveInteger(input.serverContextWindowTokens) ?? unknownModelContextTokens
  const clientWindow = positiveInteger(input.clientContextWindowTokens) ?? serverWindow
  return Math.min(clientWindow, serverWindow)
}

export function estimateChatTokens(content: string): number {
  return Math.max(1, Math.ceil(Buffer.byteLength(content, 'utf8') / 3))
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
