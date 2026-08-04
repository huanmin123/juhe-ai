import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { clearAccountConcurrency, getAccountCurrentConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  clearSpeedFirstCutoverReservationsForTest,
  reserveSpeedFirstCutoverTarget,
  setSpeedFirstCutoverSlotAcquirerForTest
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
  const reservedSlot = reservation.takeForAccount(target)
  assert(reservedSlot?.acquired, '目标 dispatch 应能一次性消费预占槽')
  assert.equal(reservation.takeForAccount(target), undefined, '预占槽只能消费一次')
  reservedSlot.release()
  assert.equal(getAccountCurrentConcurrency(target.id), 0, '目标请求完成后必须释放真实并发槽')

  const parallelReservations = await Promise.all(['a', 'b', 'c'].map((suffix) => reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_same_slow',
    targets: [{ id: `acct_budget_${suffix}`, concurrencyLimit: 1 }],
    lane: 'text'
  })))
  assert(parallelReservations.every(Boolean), '速度优先切换不得再受每作用域的本地预算门禁限制')
  parallelReservations.forEach((item) => item?.release())

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
  setSpeedFirstCutoverSlotAcquirerForTest(undefined)
  const reservationAfterFailure = await reserveSpeedFirstCutoverTarget({
    systemAccountId: 'sys_cutover',
    routeStrategyId: 'route_cutover',
    groupId: 'group_cutover',
    slowAccountId: 'acct_slot_error',
    targets: [target],
    lane: 'text'
  })
  assert(reservationAfterFailure, '异常恢复后，同一作用域必须仍能建立新 reservation')
  reservationAfterFailure.release()

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
  setSpeedFirstCutoverSlotAcquirerForTest(undefined)

  console.log('速度优先切换 reservation 回归通过：无槽不跳、目标槽预占、无每作用域预算门禁且槽获取/释放异常符合预期')
} finally {
  clearSpeedFirstCutoverReservationsForTest()
  clearAccountConcurrency()
}
