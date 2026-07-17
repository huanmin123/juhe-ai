import assert from 'node:assert/strict'

import {
  beginActiveChatAcceptance,
  cancelActiveChatPreparation,
  claimActiveChatPreparation,
  deleteActiveChatPreparationIfMatches,
  deleteActiveChatStreamIfMatches,
  hasActiveChatPreparation,
  type ActiveChatPreparation
} from '../../modules/chat/chat-active-streams.js'

const previous = { turnId: 'turn_a', controller: new AbortController() }
const current = { turnId: 'turn_b', controller: new AbortController() }
const streams = new Map([['conv_1', current]])

assert.equal(deleteActiveChatStreamIfMatches(streams, 'conv_1', previous.turnId), false, '旧轮次 finally 不得删除同会话的新流')
assert.equal(streams.get('conv_1'), current)
assert.equal(deleteActiveChatStreamIfMatches(streams, 'conv_1', current.turnId), true, '当前轮次 finally 应清理自己的流')
assert.equal(streams.has('conv_1'), false)

const preparations = new Map<string, ActiveChatPreparation>()
const preparation = claimActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_1' })
assert.ok(preparation, '首个发送准备必须取得会话内排他权')
assert.equal(hasActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_1' }), true, '准备状态必须能按 owner 和 clientMessageId 权威查询')
assert.equal(hasActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_other' }), false, '准备状态不得把同会话其他消息误报为 preparing')
assert.equal(claimActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_2' }), undefined, '同会话并发准备必须在任何收费压缩或上游调用前被拒绝')
assert.equal(cancelActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_other', clientMessageId: 'client_1' }), undefined, '其他 owner 不得取消准备请求')
assert.equal(cancelActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_other' }), undefined, '其他 clientMessageId 不得取消准备请求')
assert.equal(cancelActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_1' }), 'preparing', '停止必须按 owner 与 clientMessageId 精确取消 preparing')
assert.equal(preparation.controller.signal.aborted, true, '取消 preparing 必须传播 AbortSignal')
assert.equal(beginActiveChatAcceptance(preparations, 'conv_1', preparation.token), false, '已取消的 preparing 不得进入 accept')
assert.equal(deleteActiveChatPreparationIfMatches(preparations, 'conv_1', Symbol('stale')), false, '旧准备 token 不得释放当前请求')
assert.equal(deleteActiveChatPreparationIfMatches(preparations, 'conv_1', preparation.token), true, '当前请求 finally 必须释放准备占用')
const acceptingPreparation = claimActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_2' })
assert.ok(acceptingPreparation, '释放后下一次请求必须能继续')
assert.equal(beginActiveChatAcceptance(preparations, 'conv_1', acceptingPreparation.token), true, '未取消的 preparing 必须原子进入 accepting')
assert.equal(acceptingPreparation.phase, 'accepting')
assert.equal(cancelActiveChatPreparation(preparations, { conversationId: 'conv_1', ownerId: 'sys_1', clientMessageId: 'client_2' }), 'accepting', 'accepting 边界收到停止仍必须中断后续上游请求')
assert.equal(acceptingPreparation.controller.signal.aborted, true)

console.log('AI 问答活动流条件清理回归通过')
