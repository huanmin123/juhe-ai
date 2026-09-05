import assert from 'node:assert/strict'

import { orderGatewayAccountsByLaneCapacityAvailability } from '../../modules/gateway/dispatch/capacity.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'

const busyNormalAccount = account('busy-normal', { concurrencyLimit: 1 })
const busySuperAccount = account('busy-super', { concurrencyLimit: 1, superPriorityEnabled: true })
const idlePeerAccount = account('idle-peer', { concurrencyLimit: 10 })
const idleLaterAccount = account('idle-later', { concurrencyLimit: 10 })
const busyDirectModelAccount = account('busy-direct-model', { concurrencyLimit: 1 })
const idleUnrestrictedModelAccount = account('idle-unrestricted-model', { concurrencyLimit: 10 })

const heldSlot = tryAcquireAccountConcurrency(busyNormalAccount.id, busyNormalAccount.concurrencyLimit)
assert.equal(heldSlot.acquired, true, '回归样本应能占满普通账号')

try {
  assert.deepEqual(
    orderGatewayAccountsByLaneCapacityAvailability([busyNormalAccount, idlePeerAccount, idleLaterAccount], 'text').map((item) => item.id),
    ['idle-peer', 'idle-later', 'busy-normal'],
    '非高并发分组调度应把已满载账号排到未满载账号后面，避免每个请求都先短等满载账号'
  )
} finally {
  heldSlot.release()
  clearAccountConcurrency()
}

const heldSuperSlot = tryAcquireAccountConcurrency(busySuperAccount.id, busySuperAccount.concurrencyLimit)
assert.equal(heldSuperSlot.acquired, true, '回归样本应能占满超级优先账号')

try {
  assert.deepEqual(
    orderGatewayAccountsByLaneCapacityAvailability([busySuperAccount, idlePeerAccount], 'text').map((item) => item.id),
    ['busy-super', 'idle-peer'],
    '容量避让不能让普通账号越过超级优先账号'
  )
} finally {
  heldSuperSlot.release()
  clearAccountConcurrency()
}

const heldDirectModelSlot = tryAcquireAccountConcurrency(busyDirectModelAccount.id, busyDirectModelAccount.concurrencyLimit)
assert.equal(heldDirectModelSlot.acquired, true, '回归样本应能占满直连模型匹配账号')

try {
  assert.deepEqual(
    orderGatewayAccountsByLaneCapacityAvailability(
      [busyDirectModelAccount, idleUnrestrictedModelAccount],
      'text',
      undefined,
      {
        requestedModel: 'gpt-4.1',
        rankByAccountId: new Map([
          [busyDirectModelAccount.id, 0],
          [idleUnrestrictedModelAccount.id, 2]
        ])
      }
    ).map((item) => item.id),
    ['busy-direct-model', 'idle-unrestricted-model'],
    '容量避让不能让低模型匹配等级账号越过直连匹配账号'
  )
} finally {
  heldDirectModelSlot.release()
  clearAccountConcurrency()
}

const allBusyFirstSlot = tryAcquireAccountConcurrency(busySuperAccount.id, busySuperAccount.concurrencyLimit)
const allBusySecondSlot = tryAcquireAccountConcurrency(idlePeerAccount.id, 1)
assert.equal(allBusyFirstSlot.acquired, true, '回归样本应能占满第一个账号')
assert.equal(allBusySecondSlot.acquired, true, '回归样本应能占满第二个账号')

try {
  assert.deepEqual(
    orderGatewayAccountsByLaneCapacityAvailability([busySuperAccount, account('idle-peer', { concurrencyLimit: 1 })], 'text').map((item) => item.id),
    ['busy-super', 'idle-peer'],
    '所有账号均满载时不应打乱原调度顺序，容量失败由后续逻辑统一处理'
  )
} finally {
  allBusyFirstSlot.release()
  allBusySecondSlot.release()
  clearAccountConcurrency()
}

console.log('gateway capacity order regression passed')

function account(id: string, input: { concurrencyLimit: number; superPriorityEnabled?: boolean }): UpstreamAccount {
  return {
    id,
    name: id,
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    status: 'active',
    baseUrl: 'https://example.test/v1',
    apiKey: 'sk-test',
    concurrencyLimit: input.concurrencyLimit,
    priority: 0,
    superPriorityEnabled: input.superPriorityEnabled ?? false,
    fallbackEnabled: false,
    schedulable: true
  } as unknown as UpstreamAccount
}
