import assert from 'node:assert/strict'
import { projectChatMessageProcess } from '../../views/chat/chatMessageProcess'
import type { ChatMessage } from '../../types/domain/chat'

function message(toolEvents: ChatMessage['toolEvents'], reasoningText = '先分析'): ChatMessage {
  return { toolEvents, reasoningText } as ChatMessage
}

const lifecycle = projectChatMessageProcess(message([
  { id: 'search_1', type: 'web_search_call', status: 'started', item: { action: { query: ' 北京   天气 ' } } },
  { id: 'search_1', type: 'web_search_call', status: 'updated', item: { action: { query: '北京 天气' } } },
  { id: 'search_1', type: 'web_search_call', status: 'completed', item: { action: { query: '北京 天气' } } }
]))
assert.equal(lifecycle.reasoningText, '先分析')
assert.equal(lifecycle.toolGroups.length, 1, '同一 callId 的生命周期只能显示一组')
assert.equal(lifecycle.toolGroups[0]?.status, 'completed', '同一调用必须采用最新状态')
assert.equal(lifecycle.toolGroups[0]?.callCount, 1, '生命周期更新不能重复计数')
assert.deepEqual(lifecycle.toolGroups[0]?.summaries, ['北京 天气'])

const streamedFunction = projectChatMessageProcess(message([
  { id: 'fn_stream', type: 'function_call', status: 'started', item: { id: 'fn_stream', type: 'function_call', name: 'lookup', arguments: '{"q":"北京"}' } },
  { id: 'tool.updated-1', type: 'tool', status: 'updated', item: { type: 'response.function_call_arguments.delta', item_id: 'fn_stream', delta: '京"}' } }
]))
assert.equal(streamedFunction.toolGroups.length, 1, '函数参数增量必须按 item_id 回到原调用生命周期')
assert.equal(streamedFunction.toolGroups[0]?.status, 'updated')
assert.match(streamedFunction.toolGroups[0]?.summaries[0] ?? '', /^lookup/, '增量事件不能丢失前一阶段的函数名与可读参数')

const persistedTerminalFunction = projectChatMessageProcess({
  contentBlocks: [
    { type: 'tool_call', id: 'fn_persisted', toolType: 'function_call', status: 'completed', item: { id: 'fn_persisted', type: 'function_call', name: 'lookup', arguments: '{"q":"北京"}' } },
    { type: 'tool_call', id: 'tool_2', toolType: 'response.function_call_arguments.delta', status: 'updated', item: { type: 'response.function_call_arguments.delta', item_id: 'fn_persisted', delta: '京"}' } }
  ]
} as ChatMessage)
assert.equal(persistedTerminalFunction.toolGroups.length, 1, '持久化的后置参数增量必须按 item_id 归回原调用')
assert.equal(persistedTerminalFunction.toolGroups[0]?.callCount, 1)
assert.equal(persistedTerminalFunction.toolGroups[0]?.status, 'completed', '后置 updated 不能把已完成生命周期降级')
assert.match(persistedTerminalFunction.toolGroups[0]?.summaries[0] ?? '', /^lookup/, '终态合并后必须保留原函数摘要')

const duplicateSearch = projectChatMessageProcess(message([
  { id: 'search_a', type: 'web_search_call', status: 'completed', item: { action: { queries: [' 上海 天气 ', '北京   天气', '北京 天气'] } } },
  { id: 'search_b', type: 'web_search_call', status: 'completed', item: { action: { queries: ['北京 天气', '上海 天气'] } } }
]))
assert.equal(duplicateSearch.toolGroups.length, 1, '不同 callId 的相同搜索条件必须聚合')
assert.equal(duplicateSearch.toolGroups[0]?.callCount, 2)
assert.equal(duplicateSearch.toolGroups[0]?.duplicateCount, 1)
assert.deepEqual(duplicateSearch.toolGroups[0]?.summaries, ['上海 天气', '北京 天气'].sort((left, right) => left.localeCompare(right, 'zh-CN')))

const differentSearch = projectChatMessageProcess(message([
  { id: 'search_beijing', type: 'web_search_call', status: 'completed', item: { query: '北京天气' } },
  { id: 'search_shanghai', type: 'web_search_call', status: 'completed', item: { query: '上海天气' } }
]))
assert.equal(differentSearch.toolGroups.length, 2, '不同搜索条件不能误合并')

const sameQueryDifferentAction = projectChatMessageProcess(message([
  { id: 'search_action', type: 'web_search_call', status: 'completed', item: { action: { type: 'search', query: '同一个目标' } } },
  { id: 'open_action', type: 'web_search_call', status: 'completed', item: { action: { type: 'open_page', query: '同一个目标', url: 'https://example.com/detail' } } }
]))
assert.equal(sameQueryDifferentAction.toolGroups.length, 2, '同 query 的不同完整 action 不能误合并')

const duplicateOpenPage = projectChatMessageProcess(message([
  { id: 'open_1', type: 'web_search_call', status: 'completed', item: { action: { type: 'open_page', url: 'https://example.com/detail' } } },
  { id: 'open_2', type: 'web_search_call', status: 'completed', item: { action: { url: 'https://example.com/detail', type: 'open_page' } } }
]))
assert.equal(duplicateOpenPage.toolGroups.length, 1, '无 query 的相同 open_page action 仍应聚合')
assert.match(duplicateOpenPage.toolGroups[0]?.summaries[0] ?? '', /打开页面.*example\.com/, '无 query action 必须提供可读动作与页面目标')

const volatileSearchFields = projectChatMessageProcess(message([
  { id: 'volatile_1', type: 'web_search_call', status: 'completed', item: { action: { type: 'open_page', url: 'https://example.com/detail', id: 'first', status: 'running', time: 1 } } },
  { id: 'volatile_2', type: 'web_search_call', status: 'completed', item: { action: { time: 999, status: 'done', id: 'second', url: 'https://example.com/detail', type: 'open_page' } } }
]))
assert.equal(volatileSearchFields.toolGroups.length, 1, '完整 action 中 id/status/time 等易变字段不能阻止聚合')

const stableFunction = projectChatMessageProcess(message([
  { id: 'fn_1', type: 'function_call', status: 'completed', item: { name: 'lookup', arguments: '{"b":2,"a":{"y":2,"x":1}}' } },
  { id: 'fn_2', type: 'function_call', status: 'completed', item: { arguments: '{ "a": { "x": 1, "y": 2 }, "b": 2 }', name: 'lookup' } }
]))
assert.equal(stableFunction.toolGroups.length, 1, '函数参数对象键顺序不能影响分组')
assert.equal(stableFunction.toolGroups[0]?.callCount, 2)

const fileSearch = projectChatMessageProcess(message([
  { id: 'file_1', type: 'file_search_call', status: 'completed', item: { queries: [' 设计 文档 ', '规范'] } },
  { id: 'file_2', type: 'file_search_call', status: 'completed', item: { queries: ['规范', '设计 文档'] } }
]))
assert.equal(fileSearch.toolGroups.length, 1, '文件检索 queries 必须按规范化后的集合聚合')
assert.deepEqual(fileSearch.toolGroups[0]?.summaries, ['规范', '设计 文档'].sort((left, right) => left.localeCompare(right, 'zh-CN')))

const computer = projectChatMessageProcess(message([
  { id: 'computer_1', type: 'computer_call', status: 'completed', item: { action: { type: 'click', x: 10, y: 20, time: 1, id: 'volatile-a' } } },
  { id: 'computer_2', type: 'computer_call', status: 'completed', item: { action: { y: 20, x: 10, type: 'click', time: 999, id: 'volatile-b', status: 'done' } } }
]))
assert.equal(computer.toolGroups.length, 1, '计算机操作应忽略 id/status/time 等易变字段')

const statusPriority = projectChatMessageProcess(message([
  { id: 'status_1', type: 'web_search_call', status: 'completed', item: { query: '状态' } },
  { id: 'status_2', type: 'web_search_call', status: 'started', item: { query: '状态' } },
  { id: 'status_3', type: 'web_search_call', status: 'updated', item: { query: '状态' } }
]))
assert.equal(statusPriority.toolGroups[0]?.status, 'updated', '未终态组应优先显示执行中')
const failedPriority = projectChatMessageProcess(message([
  ...statusPriority.toolGroups.flatMap(() => [
    { id: 'failed_1', type: 'web_search_call', status: 'completed' as const, item: { query: '失败优先' } },
    { id: 'failed_2', type: 'web_search_call', status: 'failed' as const, item: { query: '失败优先' } }
  ])
]))
assert.equal(failedPriority.toolGroups[0]?.status, 'failed', '任一调用失败时整组必须显示失败')

const canceledOnly = projectChatMessageProcess(message([
  { id: 'canceled_1', type: 'web_search_call', status: 'canceled', item: { query: '已取消' } }
]))
assert.equal(canceledOnly.toolGroups[0]?.status, 'canceled', '仅包含取消事件的工具组必须显示已取消')
const completedAndCanceled = projectChatMessageProcess(message([
  { id: 'completed_1', type: 'web_search_call', status: 'completed', item: { query: '混合终态' } },
  { id: 'canceled_2', type: 'web_search_call', status: 'canceled', item: { query: '混合终态' } }
]))
assert.equal(completedAndCanceled.toolGroups[0]?.status, 'canceled', '同组存在取消事件时不能回落为已完成')

const malformed = projectChatMessageProcess(message([
  { id: 'unknown_1', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: 'unknown_2', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: '', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: '', type: 'unknown_tool', status: 'completed', item: { opaque: true } }
]))
assert.equal(malformed.toolGroups.length, 4, '无法识别的动作必须按 callId 或事件位置 fail-open，不能误合并')
assert.equal(new Set(malformed.toolGroups.map((group) => group.key)).size, 4, '畸形动作也必须生成稳定唯一 key')
assert(malformed.toolGroups.every((group) => group.summaries.length === 0), 'fail-open callId 只能留在内部 key，不能进入用户摘要')
assert.doesNotMatch(JSON.stringify(malformed.toolGroups.map((group) => group.summaries)), /unknown_|event-|tool_/i, '畸形工具摘要不得泄露 callId 或协议 fallback id')

const searchPreparing = projectChatMessageProcess(message([
  { id: 'ws_private_protocol_id', type: 'web_search_call', status: 'started', item: { id: 'ws_private_protocol_id', type: 'web_search_call', status: 'in_progress' } }
]))
assert.equal(searchPreparing.toolGroups.length, 1)
assert.deepEqual(searchPreparing.toolGroups[0]?.summaries, [], '尚无可识别 action 的搜索只能显示通用状态行')
assert.doesNotMatch(JSON.stringify(searchPreparing.toolGroups[0]?.summaries), /ws_|private|protocol/i, '搜索准备态不能显示协议 id')

const persisted = projectChatMessageProcess({
  contentBlocks: [
    { type: 'reasoning', text: '历史思考' },
    { type: 'tool_call', id: 'history_1', toolType: 'web_search_call', status: 'completed', item: { query: '历史' } }
  ]
} as ChatMessage)
assert.equal(persisted.reasoningText, '历史思考')
assert.equal(persisted.toolGroups[0]?.summaries[0], '历史', '历史 contentBlocks 必须复用相同投影')

assert(duplicateSearch.toolGroups.every((group) => group.summaries.every((summary) => summary.length <= 160)), '摘要必须有长度上限')
console.log('AI 问答工具生命周期、动作聚合与历史投影回归通过')
