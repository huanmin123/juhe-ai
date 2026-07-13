import assert from 'node:assert/strict'

import { stopActiveChatGeneration } from '../../views/chat/chatStopGeneration'

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

console.log('AI 问答停止门禁与 controller 隔离回归通过')
