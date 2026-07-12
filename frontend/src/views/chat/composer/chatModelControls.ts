import type { ChatModelOption, ChatReasoningEffort } from '@/types/domain/chat'

const contextPresets = [16_000, 32_000, 64_000, 128_000, 256_000, 512_000, 1_000_000, 2_000_000]

export function chatContextOptions(model?: ChatModelOption): Array<{ label: string; value: number }> {
  const max = model?.contextWindowTokens
  if (!max) return [{ label: '上下文 自动', value: 0 }]
  const values = contextPresets.filter((value) => value < max)
  values.push(max)
  return [{ label: '上下文 自动', value: 0 }, ...[...new Set(values)].map((value) => ({ label: `上下文 ${formatContextTokens(value)}`, value }))]
}

export function reasoningEffortLabel(value: ChatReasoningEffort): string {
  return ({ none: '无思考', minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高' })[value]
}

function formatContextTokens(value: number): string {
  return value >= 1_000_000 ? `${Number((value / 1_000_000).toFixed(1))}M` : `${Math.round(value / 1000)}K`
}
