import assert from 'node:assert/strict'

import { resolveChatStopTarget, shouldRebuildChatStopTarget, stopActiveChatGeneration } from '../../views/chat/chatStopGeneration'

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void
  return { promise: new Promise<void>((next) => { resolve = next }), resolve }
}

const oldController = new AbortController()
const newController = new AbortController()
const stopRequest = deferred()
const oldSendSettled = deferred()
let stopCalled = false

const stopping = stopActiveChatGeneration({
  controller: oldController,
  stop: async () => { stopCalled = true; await stopRequest.promise },
  sendSettled: oldSendSettled.promise
})

assert.equal(oldController.signal.aborted, true, '必须先立即中断旧本地流，再等待慢 stop HTTP')
assert.equal(stopCalled, true)
assert.equal(newController.signal.aborted, false)

let finished = false
void stopping.then(() => { finished = true })
stopRequest.resolve()
await Promise.resolve()
assert.equal(finished, false, 'stop HTTP 返回后仍需等待旧发送完成终态对账，期间 stopping gate 不能释放')
assert.equal(newController.signal.aborted, false, '旧 stop 回调不得触碰后续新 controller')

oldSendSettled.resolve()
await stopping
assert.equal(finished, true)
assert.equal(newController.signal.aborted, false)

const rejectedSendSettled = deferred()
const failedStop = stopActiveChatGeneration({
  stop: async () => { throw new Error('stop request failed') },
  sendSettled: rejectedSendSettled.promise
})
let failedStopSettled = false
void failedStop.catch(() => { failedStopSettled = true })
await Promise.resolve()
assert.equal(failedStopSettled, false, '停止 HTTP 失败后仍须等待旧发送收口再向页面报告')
rejectedSendSettled.resolve()
await assert.rejects(failedStop, /stop request failed/, '停止请求失败必须抛给页面显示，不能被 allSettled 吞掉')

const pendingTarget = resolveChatStopTarget({
  selectedConversationId: 'conversation-pending',
  pending: {
    conversationId: 'conversation-pending',
    clientMessageId: 'client-pending',
    turnId: 'turn-pending'
  }
})
assert.deepEqual(pendingTarget, {
  conversationId: 'conversation-pending',
  clientMessageId: 'client-pending',
  turnId: 'turn-pending'
}, '活动流句柄已释放后，停止按钮仍必须使用待确权记录精确停止 preparing/streaming 轮次')
assert.equal(resolveChatStopTarget({
  selectedConversationId: 'conversation-other',
  pending: pendingTarget
}), undefined, '待确权停止目标不能跨会话误用')

assert.equal(shouldRebuildChatStopTarget({
  conversationId: 'conversation-edit',
  clientMessageId: 'client-old-terminal'
}), true, '无活动停止目标时必须允许重建（attach/恢复场景依赖此路径）')
assert.equal(shouldRebuildChatStopTarget({
  active: { conversationId: 'conversation-other', clientMessageId: 'client-attached', lifecycleEpoch: 3 },
  conversationId: 'conversation-edit',
  clientMessageId: 'client-old-terminal'
}), true, '活动停止目标属于其他会话时必须允许重建')
assert.equal(shouldRebuildChatStopTarget({
  active: { conversationId: 'conversation-edit', clientMessageId: 'client-edit-resend', lifecycleEpoch: 7 },
  conversationId: 'conversation-edit',
  clientMessageId: 'client-edit-resend'
}), false, '同一本地在途请求的 runtime 投影不得触发重建')
assert.equal(shouldRebuildChatStopTarget({
  active: { conversationId: 'conversation-edit', clientMessageId: 'client-edit-resend', lifecycleEpoch: 7 },
  conversationId: 'conversation-edit',
  clientMessageId: 'client-old-terminal'
}), false, '旧终端轮回放不得覆盖本地在途编辑请求，否则 replaceTurnId 丢失导致编辑态卡死在 submitting')
assert.equal(shouldRebuildChatStopTarget({
  active: { conversationId: 'conversation-edit', clientMessageId: 'client-attached' },
  conversationId: 'conversation-edit',
  clientMessageId: 'client-old-terminal'
}), true, '同会话但非本地发起（无 lifecycleEpoch）的停止目标才允许被回放轮次重建')

console.log('AI 问答停止门禁与 controller 隔离回归通过')
