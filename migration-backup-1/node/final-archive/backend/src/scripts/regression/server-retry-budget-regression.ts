import { strict as assert } from 'node:assert'

import {
  ServerRetryBudget,
  shouldHandoffClient
} from '../../modules/gateway/runtime/server-retry-budget.js'
import {
  GatewayRequestWallBudget
} from '../../modules/gateway/routing/route-coordination.js'

assert.equal(shouldHandoffClient({
  availability: 'dispatchable_now',
  noAvailableElapsedMs: 300_000,
  waitBudgetMs: 270_000
}), false, '同一 tick 出现可派发账号时应优先派发')
assert.equal(shouldHandoffClient({
  availability: 'recoverable_later',
  noAvailableElapsedMs: 300_000,
  waitBudgetMs: 270_000
}), true, '可恢复账号等待耗尽后应交给客户端重试')
assert.equal(shouldHandoffClient({
  availability: 'recoverable_later',
  noAvailableElapsedMs: 10_000,
  waitBudgetMs: 270_000
}), false, '可恢复账号仍在等待预算内时应继续服务端接管')
assert.equal(shouldHandoffClient({
  availability: 'hard_exhausted',
  noAvailableElapsedMs: 10_000,
  waitBudgetMs: 270_000
}), true, '硬耗尽时继续等待不会产生可用账号')

const budget = new ServerRetryBudget(270_000)
budget.beginNoAvailableWait(1_000)
assert.equal(budget.remainingMs(11_000), 260_000)
budget.pauseNoAvailableWait(11_000)
assert.equal(budget.remainingMs(311_000), 260_000, '账号执行时间不得消耗无账号等待预算')
budget.beginNoAvailableWait(500_000)
assert.equal(budget.remainingMs(760_000), 0)
budget.pauseNoAvailableWait(760_000)
assert.equal(budget.elapsedMs(), 270_000)

const observedBudget = new ServerRetryBudget(1000)
const observedTransitions: string[] = []
observedBudget.setWaitObserver({
  onWaitStarted: () => observedTransitions.push('started'),
  onWaitPaused: () => observedTransitions.push('paused')
})
observedBudget.beginNoAvailableWait(100)
observedBudget.beginNoAvailableWait(200)
observedBudget.pauseNoAvailableWait(300)
assert.deepEqual(observedTransitions, ['started', 'paused'], '重复 begin 不得启动多个 SSE 心跳定时器')

let wallNowMs = 1_000
const wallBudget = new GatewayRequestWallBudget({
  requestAcceptedAtMs: wallNowMs,
  budgetMs: 270_000,
  now: () => wallNowMs
})
wallNowMs = 11_000
assert.equal(wallBudget.remainingMs(), 260_000)
wallNowMs = 311_000
assert.equal(wallBudget.remainingMs(), 0, '整请求墙钟预算不得在账号执行期间暂停')
const pausedRetryBudget = new ServerRetryBudget(270_000)
pausedRetryBudget.beginNoAvailableWait(1_000)
pausedRetryBudget.pauseNoAvailableWait(11_000)
assert.equal(pausedRetryBudget.remainingMs(311_000), 260_000, 'ServerRetryBudget 仍只累计无账号等待，两类预算不得互相替代')

console.log('server retry budget regression passed')
