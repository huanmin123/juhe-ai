import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { clearAccountConcurrency, getAccountCurrentConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  clearSpeedFirstCutoverReservationsForTest,
  reserveSpeedFirstCutoverTarget,
  setSpeedFirstCutoverSlotAcquirerForTest,
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

  setSpeedFirstCutoverSlotAcquirerForTest(async () => {
    throw new Error('回归注入：并发槽存储暂时不可用')
  })
  await assert.rejects(
    reserveSpeedFirstCutoverTarget({
      systemAccountId: 'sys_cutover',
      routeStrategyId: 'route_cutover',
      groupId: 'group_cutover',
      slowAccountId: 'acct_slot_error',
      targets: [target],
      lane: 'text'
    }),
    /并发槽存储暂时不可用/,
    '并发槽获取异常应原样交给调用方执行 fail-open 决策'
  )
  assert.equal(speedFirstCutoverBudgetSnapshot().length, 0, '并发槽获取异常不得泄漏共享切换预算')

  setSpeedFirstCutoverSlotAcquirerForTest(undefined)
  const reservationAfterFailure = await reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_slot_error',
    targets: [target],
    lane: 'text'
  })
  assert(reservationAfterFailure, '异常释放预算后，同一作用域必须仍能建立新 reservation')
  reservationAfterFailure.release()
  assert.equal(speedFirstCutoverBudgetSnapshot().length, 0, '异常恢复后的 reservation 也必须完整释放')

  setSpeedFirstCutoverSlotAcquirerForTest(async () => ({
    acquired: true,
    current: 1,
    limit: 1,
    lane: 'text',
    laneCurrent: 1,
    laneLimit: 1,
    markFirstOutput: () => {},
    release: () => {
      throw new Error('回归注入：并发槽释放失败')
    }
  }))
  const releaseFailureReservation = await reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_release_error',
    targets: [target],
    lane: 'text'
  })
  assert(releaseFailureReservation, '槽释放异常测试必须先取得 reservation')
  assert.throws(
    () => releaseFailureReservation.release(),
    /并发槽释放失败/,
    '槽释放异常应保留原错误，便于定位真实资源问题'
  )
  assert.equal(speedFirstCutoverBudgetSnapshot().length, 0, '槽释放异常也不得泄漏共享切换预算')
  setSpeedFirstCutoverSlotAcquirerForTest(undefined)

  console.log('速度优先切换 reservation 回归通过：无槽不跳、目标槽预占、槽获取/释放异常和共享预算释放符合预期')
} finally {
  clearSpeedFirstCutoverReservationsForTest()
  clearAccountConcurrency()
}
