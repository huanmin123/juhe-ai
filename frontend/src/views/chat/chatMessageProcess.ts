import type { ChatMessage, ChatToolEvent, ChatToolStatus } from '@/types/domain/chat'

export interface ChatToolProcessGroup {
  key: string
  type: string
  status: ChatToolStatus
  callCount: number
  duplicateCount: number
  summaries: string[]
}

interface LifecycleTool {
  callId: string
  type: string
  status: ChatToolStatus
  item?: Record<string, unknown>
  fallbackIndex: number
}

interface CanonicalToolAction {
  key: string
  summaries: string[]
}

const summaryLimit = 160

export function projectChatMessageProcess(message: ChatMessage): { reasoningText: string; toolGroups: ChatToolProcessGroup[] } {
  const reasoningText = message.reasoningText ?? (message.contentBlocks ?? [])
    .filter((block): block is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'reasoning' }> => block.type === 'reasoning')
    .map((block) => block.text)
    .join('\n')
  const persistedTools = (message.contentBlocks ?? [])
    .filter((block): block is Extract<NonNullable<ChatMessage['contentBlocks']>[number], { type: 'tool_call' }> => block.type === 'tool_call')
    .map((block) => ({ id: block.id, type: block.toolType, status: block.status, item: block.item }))
  const rawTools = message.toolEvents?.length ? message.toolEvents : persistedTools
  return { reasoningText, toolGroups: groupToolEvents(rawTools) }
}

function groupToolEvents(events: ChatToolEvent[]): ChatToolProcessGroup[] {
  const lifecycle = new Map<string, LifecycleTool>()
  events.forEach((event, index) => {
    const type = resolveToolType(event)
    const callId = resolveToolCallId(event)
    const lifecycleKey = stableJson([type, callId || `event-${index}`])
    const previous = lifecycle.get(lifecycleKey)
    lifecycle.set(lifecycleKey, {
      callId,
      type,
      status: event.status,
      item: mergeToolItem(previous?.item, event.item),
      fallbackIndex: previous?.fallbackIndex ?? index
    })
  })

  const grouped = new Map<string, { type: string; statuses: ChatToolStatus[]; callIds: Set<string>; summaries: Set<string> }>()
  for (const tool of lifecycle.values()) {
    const canonical = canonicalizeToolAction(tool)
    const existing = grouped.get(canonical.key) ?? {
      type: tool.type,
      statuses: [],
      callIds: new Set<string>(),
      summaries: new Set<string>()
    }
    existing.statuses.push(tool.status)
    existing.callIds.add(tool.callId || `event-${tool.fallbackIndex}`)
    canonical.summaries.forEach((summary) => existing.summaries.add(limitSummary(summary)))
    grouped.set(canonical.key, existing)
  }

  return [...grouped.entries()].map(([key, group]) => ({
    key,
    type: group.type,
    status: resolveGroupStatus(group.statuses),
    callCount: group.callIds.size,
    duplicateCount: Math.max(0, group.callIds.size - 1),
    summaries: [...group.summaries]
  }))
}

function canonicalizeToolAction(tool: LifecycleTool): CanonicalToolAction {
  const item = tool.item ?? {}
  if (tool.type === 'web_search_call') {
    const queries = extractQueries(item)
    if (queries.length) return { key: stableJson([tool.type, { queries }]), summaries: queries }
  }
  if (tool.type === 'file_search_call') {
    const queries = extractQueries(item)
    if (queries.length) return { key: stableJson([tool.type, { queries }]), summaries: queries }
  }
  if (tool.type === 'function_call') {
    const name = normalizeWhitespace(readString(item.name) || readString(asRecord(item.action)?.name))
    const argumentsValue = item.arguments ?? asRecord(item.action)?.arguments
    if (name && argumentsValue !== undefined) {
      const normalizedArguments = normalizeFunctionArguments(argumentsValue)
      const summary = `${name} · ${humanizeValue(normalizedArguments)}`
      return { key: stableJson([tool.type, { name, arguments: normalizedArguments }]), summaries: [summary] }
    }
  }
  if (tool.type === 'computer_call') {
    const action = stripVolatileFields(asRecord(item.action) ?? item)
    if (Object.keys(action).length) {
      return { key: stableJson([tool.type, action]), summaries: [describeComputerAction(action)] }
    }
  }
  const failOpenId = tool.callId || `event-${tool.fallbackIndex}`
  return {
    key: stableJson([tool.type, { unrecognizedCallId: failOpenId }]),
    summaries: [`${toolLabel(tool.type)} · ${failOpenId}`]
  }
}

function extractQueries(item: Record<string, unknown>): string[] {
  const action = asRecord(item.action)
  const candidates = [action?.queries, action?.query, item.queries, item.query]
  const queries = candidates.flatMap((candidate) => Array.isArray(candidate) ? candidate : [candidate])
    .filter((candidate): candidate is string => typeof candidate === 'string')
    .map(normalizeWhitespace)
    .filter(Boolean)
  return [...new Set(queries)].sort((left, right) => left.localeCompare(right, 'zh-CN'))
}

function normalizeFunctionArguments(value: unknown): unknown {
  if (typeof value !== 'string') return normalizeJsonValue(value)
  const trimmed = value.trim()
  try { return normalizeJsonValue(JSON.parse(trimmed)) } catch { return normalizeWhitespace(trimmed) }
}

function normalizeJsonValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(normalizeJsonValue)
  const record = asRecord(value)
  if (record) return Object.fromEntries(Object.keys(record).sort().map((key) => [key, normalizeJsonValue(record[key])]))
  return typeof value === 'string' ? normalizeWhitespace(value) : value
}

function stripVolatileFields(value: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.keys(value)
    .filter((key) => !/^(?:id|call_?id|status|time|timestamp|created_?at|updated_?at)$/i.test(key))
    .sort()
    .map((key) => {
      const child = value[key]
      if (Array.isArray(child)) return [key, child.map((entry) => asRecord(entry) ? stripVolatileFields(entry) : normalizeJsonValue(entry))]
      const record = asRecord(child)
      return [key, record ? stripVolatileFields(record) : normalizeJsonValue(child)]
    }))
}

function describeComputerAction(action: Record<string, unknown>): string {
  const type = normalizeWhitespace(readString(action.type) || readString(action.action) || '计算机操作')
  const coordinates = ['x', 'y'].filter((key) => typeof action[key] === 'number').map((key) => `${key}=${action[key]}`)
  const text = normalizeWhitespace(readString(action.text) || readString(action.value))
  return [type, coordinates.join(', '), text].filter(Boolean).join(' · ')
}

function humanizeValue(value: unknown): string {
  if (value === null) return 'null'
  if (typeof value === 'string') return value || '空参数'
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value)) return value.map(humanizeValue).join('，') || '空参数'
  const record = asRecord(value)
  if (!record) return '参数'
  const entries = Object.entries(record).map(([key, entry]) => `${key}: ${humanizeValue(entry)}`)
  return entries.join('；') || '空参数'
}

function stableJson(value: unknown): string {
  return JSON.stringify(normalizeJsonValue(value))
}

function resolveGroupStatus(statuses: ChatToolStatus[]): ChatToolStatus {
  if (statuses.includes('failed')) return 'failed'
  if (statuses.includes('updated')) return 'updated'
  if (statuses.includes('started')) return 'started'
  return 'completed'
}

function resolveToolType(event: ChatToolEvent): string {
  const itemType = normalizeIdentifier(event.item?.type)
  if (['web_search_call', 'file_search_call', 'function_call', 'computer_call'].includes(itemType)) return itemType
  if (itemType.startsWith('response.function_call_arguments.')) return 'function_call'
  return normalizeIdentifier(event.type) || 'tool'
}

function resolveToolCallId(event: ChatToolEvent): string {
  return [event.item?.id, event.item?.call_id, event.item?.item_id, event.id]
    .map(normalizeIdentifier)
    .find(Boolean) ?? ''
}

function mergeToolItem(previous: Record<string, unknown> | undefined, current: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  if (!previous) return current
  if (!current) return previous
  const previousAction = asRecord(previous.action)
  const currentAction = asRecord(current.action)
  const merged = { ...previous, ...current }
  if (previousAction || currentAction) merged.action = { ...previousAction, ...currentAction }
  return merged
}

function toolLabel(type: string): string {
  return ({ web_search_call: '联网搜索', file_search_call: '文件检索', function_call: '函数调用', computer_call: '计算机操作' }[type] ?? '工具调用')
}

function normalizeIdentifier(value: unknown): string { return typeof value === 'string' ? value.trim() : '' }
function normalizeWhitespace(value: string): string { return value.replace(/\s+/g, ' ').trim() }
function limitSummary(value: string): string { return value.length <= summaryLimit ? value : `${value.slice(0, summaryLimit - 1)}…` }
function readString(value: unknown): string { return typeof value === 'string' ? value : '' }
function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}
