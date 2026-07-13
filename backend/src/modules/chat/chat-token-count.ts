import { encode } from 'gpt-tokenizer'

export function countChatTextTokens(value: string): number {
  return value ? encode(value).length : 0
}

export function countChatJsonTokens(value: unknown): number {
  return countChatTextTokens(JSON.stringify(value))
}

export function estimateChatImageTokens(width: number | undefined, height: number | undefined): number {
  if (!width || !height || width < 1 || height < 1) return 2_500
  return Math.max(1, Math.ceil(width / 32) * Math.ceil(height / 32)) + 85
}
