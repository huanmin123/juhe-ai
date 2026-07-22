import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { clearAccountConcurrency, getAccountCurrentConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  clearSpeedFirstCutoverReservationsForTest,
  reserveSpeedFirstCutoverTarget,
  speedFirstCutoverBudgetSnapshot
} from '../../modules/gateway/runtime/speed-first-cutover-reservation.service.js'

runtimeConfig.runtimeStateDriver = 'memory'

const target = {
  id: 'acct_target',
  concurrencyLimit: 1
}

try {
  const occupiedTarget = tryAcquireAccountConcurrency(target.id, 1)
  assert.equal(occupiedTarget.acquired, true)
  const unavailable = await reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_slow',
    targets: [target],
    lane: 'text'
  })
  assert.equal(unavailable, undefined, '目标账户没有真实并发槽时不得中止原上游')
  occupiedTarget.release()

  const reservation = await reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_slow',
    targets: [target],
    lane: 'text'
  })
  assert(reservation, '目标账户有槽时应建立切换 reservation')
  assert.equal(getAccountCurrentConcurrency(target.id), 1, 'reservation 必须真实占用目标账户并发槽')
  assert.equal(speedFirstCutoverBudgetSnapshot()[0]?.active, 1, 'reservation 应同时占用共享切换预算')
  const reservedSlot = reservation.takeForAccount(target)
  assert(reservedSlot?.acquired, '目标 dispatch 应能一次性消费预占槽')
  assert.equal(reservation.takeForAccount(target), undefined, '预占槽只能消费一次')
  reservedSlot.release()
  assert.equal(getAccountCurrentConcurrency(target.id), 0, '目标请求完成后必须释放真实并发槽')
  assert.equal(speedFirstCutoverBudgetSnapshot().length, 0, '目标请求完成后必须释放共享切换预算')

  const budgetReservations = await Promise.all(['a', 'b', 'c'].map((suffix) => reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_same_slow',
    targets: [{ id: `acct_budget_${suffix}`, concurrencyLimit: 1 }],
    lane: 'text'
  })))
  assert(budgetReservations[0] && budgetReservations[1], '同一慢账户前两个迁移应能取得共享预算')
  assert.equal(budgetReservations[2], undefined, '同一慢账户第三个并发迁移必须被共享预算阻止')
  budgetReservations[0]?.release()
  budgetReservations[1]?.release()
  assert.equal(speedFirstCutoverBudgetSnapshot().length, 0, '未消费 reservation 也必须释放共享预算')

  console.log('速度优先切换 reservation 回归通过：无槽不跳、目标槽预占和共享预算释放符合预期')
} finally {
  clearSpeedFirstCutoverReservationsForTest()
  clearAccountConcurrency()
}
