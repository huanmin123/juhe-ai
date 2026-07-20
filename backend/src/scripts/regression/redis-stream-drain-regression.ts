import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  inspectRedisStreamDrain,
  redisStreamDrainContracts,
  RedisStreamDrainStabilityTracker,
  type RedisStreamDrainCommandClient
} from '../../shared/redis-stream-drain.js'

class FakeRedisClient implements RedisStreamDrainCommandClient {
  readonly commands: string[][] = []

  constructor(private readonly replies: Map<string, unknown>) {}

  async sendCommand(command: string[]): Promise<unknown> {
    this.commands.push(command)
    const reply = this.replies.get(command.join(' '))
    if (reply instanceof Error) throw reply
    return reply
  }
}

assert.equal(redisStreamDrainContracts.length, 5, '排空工具必须覆盖五条可靠队列流')
assert.equal(new Set(redisStreamDrainContracts.map((item) => item.streamKey)).size, 5, '五条流 key 不得重复')
assert.equal(new Set(redisStreamDrainContracts.map((item) => item.groupName)).size, 5, '五个 consumer group 不得重复')

for (const [relativePath, contractName] of [
  ['../../modules/gateway/usage/record-queue.service.ts', 'usageRecords'],
  ['../../modules/audit-logs/audit-log-queue.service.ts', 'auditLogs'],
  ['../../modules/operation-logs/operation-log-queue.service.ts', 'operationLogs'],
  ['../../modules/public-api-logs/public-api-log-queue.service.ts', 'publicApiLogs'],
  ['../../modules/record-maintenance/record-maintenance-queue.service.ts', 'recordMaintenance']
] as const) {
  const source = readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')
  assert.match(source, new RegExp(`redisStreamQueueContracts\\.${contractName}`), `${relativePath} 必须复用共享 Stream 契约`)
}

const drainedReplies = new Map<string, unknown>()
for (const contract of redisStreamDrainContracts) {
  drainedReplies.set(`XLEN ${contract.streamKey}`, 0)
  drainedReplies.set(`XINFO GROUPS ${contract.streamKey}`, [[
    'name', contract.groupName,
    'consumers', 0,
    'pending', 0,
    'last-delivered-id', '0-0',
    'entries-read', 0,
    'lag', 0
  ]])
  drainedReplies.set(`XPENDING ${contract.streamKey} ${contract.groupName}`, [0, null, null, []])
  drainedReplies.set(`XPENDING ${contract.streamKey} ${contract.groupName} - + 1`, [])
}
drainedReplies.set('INFO commandstats', '# Commandstats\r\ncmdstat_xadd:calls=42,usec=10,usec_per_call=0.24\r\n')
const drainedClient = new FakeRedisClient(drainedReplies)
const drained = await inspectRedisStreamDrain(drainedClient)
assert.equal(drained.drained, true, '五条流都为空时应允许完成排空')
assert.equal(drained.xaddCalls, 42, '应记录 XADD 调用总数用于稳定窗口判断')

const activeContract = redisStreamDrainContracts[0]
assert(activeContract)
const activeReplies = new Map(drainedReplies)
activeReplies.set(`XLEN ${activeContract.streamKey}`, 3)
activeReplies.set(`XINFO GROUPS ${activeContract.streamKey}`, [[
  'name', activeContract.groupName,
  'consumers', 1,
  'pending', 1,
  'last-delivered-id', '1-0',
  'entries-read', 2,
  'lag', 1
]])
activeReplies.set(`XPENDING ${activeContract.streamKey} ${activeContract.groupName}`, [1, '1-0', '1-0', []])
activeReplies.set(`XPENDING ${activeContract.streamKey} ${activeContract.groupName} - + 1`, [['1-0', 'consumer-a', 1234, 1]])
const active = await inspectRedisStreamDrain(new FakeRedisClient(activeReplies))
assert.equal(active.drained, false, '存在 pending、lag 或未删除条目时不得误判排空完成')
assert.deepEqual(active.streams[0]?.groups[0], {
  name: activeContract.groupName,
  pending: 1,
  lag: 1,
  lastDeliveredId: '1-0',
  oldestPendingIdleMs: 1234
})

const missingReplies = new Map(drainedReplies)
missingReplies.set(`XLEN ${activeContract.streamKey}`, 2)
missingReplies.set(`XINFO GROUPS ${activeContract.streamKey}`, new Error('ERR no such key'))
const missingGroup = await inspectRedisStreamDrain(new FakeRedisClient(missingReplies))
assert.equal(missingGroup.drained, false, '流非空但 consumer group 缺失时必须阻断完成')

const emptyWithoutGroupReplies = new Map(drainedReplies)
emptyWithoutGroupReplies.set(`XINFO GROUPS ${activeContract.streamKey}`, [])
const emptyWithoutGroup = await inspectRedisStreamDrain(new FakeRedisClient(emptyWithoutGroupReplies))
assert.equal(emptyWithoutGroup.drained, false, '空流但目标 consumer group 缺失时也必须阻断完成')

const unknownLagReplies = new Map(drainedReplies)
unknownLagReplies.set(`XINFO GROUPS ${activeContract.streamKey}`, [[
  'name', activeContract.groupName,
  'consumers', 0,
  'pending', 0,
  'last-delivered-id', '0-0',
  'entries-read', null,
  'lag', null
]])
const unknownLag = await inspectRedisStreamDrain(new FakeRedisClient(unknownLagReplies))
assert.equal(unknownLag.drained, false, 'Redis 返回未知 lag 时必须阻断，不能把 null 当成 0')
assert.equal(unknownLag.streams[0]?.groups[0]?.lag, null, '未知 lag 必须在快照中显式保留为 null')

const unknownPendingReplies = new Map(drainedReplies)
unknownPendingReplies.set(`XPENDING ${activeContract.streamKey} ${activeContract.groupName}`, [null, null, null, []])
const unknownPending = await inspectRedisStreamDrain(new FakeRedisClient(unknownPendingReplies))
assert.equal(unknownPending.drained, false, 'Redis 返回未知 pending 时必须阻断，不能把 null 当成 0')
assert.equal(unknownPending.streams[0]?.groups[0]?.pending, null, '未知 pending 必须在快照中显式保留为 null')

const tracker = new RedisStreamDrainStabilityTracker(2)
assert.equal(tracker.observe({ ...drained, xaddCalls: 42 }), false, '第一个空闲窗口不得立即宣告完成')
assert.equal(tracker.observe({ ...drained, xaddCalls: 43 }), false, 'XADD 计数增长时必须重新累计稳定窗口')
assert.equal(tracker.observe({ ...drained, xaddCalls: 43 }), true, '连续两个空闲窗口且 XADD 不增长后才允许完成')
assert.equal(tracker.observe({ ...active, xaddCalls: 43 }), false, '再次出现积压时必须撤销完成状态')
assert.equal(tracker.observe({ ...drained, xaddCalls: undefined }), false, '无法读取 XADD 计数时不得误判完成')

console.log('redis stream drain regression passed')
