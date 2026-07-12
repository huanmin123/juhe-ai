import type { DatabaseClient } from './database-client.js'

const partitionPrefix = 'chat_messages_'
const ensuredDateKeys = new Set<string>()

export function chatMessagePartitionDateKeyFromIso(value: string | undefined | null): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(value ?? '').trim())
  if (!match) return undefined
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])))
  if (date.getUTCFullYear() !== Number(match[1]) || date.getUTCMonth() !== Number(match[2]) - 1 || date.getUTCDate() !== Number(match[3])) return undefined
  return `${match[1]}${match[2]}${match[3]}`
}

export function postgresChatMessagePartitionName(dateKey: string): string {
  const normalized = normalizeDateKey(dateKey)
  if (!normalized) throw new Error(`AI 问答消息分区日期无效：${dateKey}`)
  return `${partitionPrefix}${normalized}`
}

export function chatMessagePartitionBounds(dateKey: string): { startDate: string; endDate: string } {
  const normalized = normalizeDateKey(dateKey)
  if (!normalized) throw new Error(`AI 问答消息分区日期无效：${dateKey}`)
  const startDate = `${normalized.slice(0, 4)}-${normalized.slice(4, 6)}-${normalized.slice(6, 8)}`
  const date = new Date(`${startDate}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() + 1)
  return { startDate, endDate: date.toISOString().slice(0, 10) }
}

export async function ensurePostgresChatMessagePartitions(client: DatabaseClient, createdAt: string): Promise<void> {
  if (client.driver !== 'postgres') return
  const current = chatMessagePartitionDateKeyFromIso(createdAt)
  if (!current) throw new Error(`AI 问答消息时间无效：${createdAt}`)
  const next = nextDateKey(current)
  for (const dateKey of [current, next]) {
    if (ensuredDateKeys.has(dateKey)) continue
    const bounds = chatMessagePartitionBounds(dateKey)
    const name = postgresChatMessagePartitionName(dateKey)
    await client.execute(`
      CREATE TABLE IF NOT EXISTS juhe_chat."${name}"
      PARTITION OF juhe_chat.chat_messages
      FOR VALUES FROM ('${bounds.startDate}') TO ('${bounds.endDate}')
    `)
    ensuredDateKeys.add(dateKey)
  }
}

function nextDateKey(dateKey: string): string {
  const bounds = chatMessagePartitionBounds(dateKey)
  return bounds.endDate.replace(/-/g, '')
}

function normalizeDateKey(value: string): string | undefined {
  if (!/^\d{8}$/.test(value)) return undefined
  return chatMessagePartitionDateKeyFromIso(`${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)}`)
}
