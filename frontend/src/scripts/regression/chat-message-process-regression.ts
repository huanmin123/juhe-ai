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

const malformed = projectChatMessageProcess(message([
  { id: 'unknown_1', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: 'unknown_2', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: '', type: 'unknown_tool', status: 'completed', item: { opaque: true } },
  { id: '', type: 'unknown_tool', status: 'completed', item: { opaque: true } }
]))
assert.equal(malformed.toolGroups.length, 4, '无法识别的动作必须按 callId 或事件位置 fail-open，不能误合并')
assert.equal(new Set(malformed.toolGroups.map((group) => group.key)).size, 4, '畸形动作也必须生成稳定唯一 key')

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
