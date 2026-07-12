export interface ChatContextMessage {
  role: 'user' | 'assistant'
  content: string
}

const unknownModelContextTokens = 16_000
const maxHistoryTokens = 64_000
const outputReserveTokens = 8_000
const protocolReserveTokens = 4_000
const messageOverheadTokens = 12

export function trimChatContextToBudget(input: {
  history: ChatContextMessage[]
  currentUserContent: string
  contextWindowTokens?: number
}): ChatContextMessage[] {
  const contextWindow = positiveInteger(input.contextWindowTokens) ?? unknownModelContextTokens
  const currentTokens = estimateChatTokens(input.currentUserContent)
  const historyBudget = Math.max(0, Math.min(
    maxHistoryTokens,
    contextWindow - outputReserveTokens - protocolReserveTokens - currentTokens
  ))
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
