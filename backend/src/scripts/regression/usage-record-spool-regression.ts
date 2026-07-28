import assert from 'node:assert/strict'
import { readFileSync, readdirSync, rmSync } from 'node:fs'
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { UsageRecordInput } from '../../storage/repositories.js'
import {
  getUsageRecordSpoolRuntime,
  persistUsageRecordToSpool,
  startUsageRecordSpoolReplay,
  stopUsageRecordSpoolReplay
} from '../../modules/gateway/usage/usage-record-spool.js'

const spoolSource = readFileSync(new URL('../../modules/gateway/usage/usage-record-spool.ts', import.meta.url), 'utf8')
assert.match(
  spoolSource,
  /syncDirectory\(dirname\(filePath\)\)/,
  '损坏文件隔离后必须 fsync 真实父目录，macOS 上不能把文件路径拼接 .. 当目录打开'
)

const tempRoot = await mkdtemp(join(tmpdir(), 'juhe-ai-usage-spool-'))
runtimeConfig.runtimeMode = 'performance'
runtimeConfig.instanceId = 'gateway-regression-1'
runtimeConfig.usageSpool.directory = tempRoot
runtimeConfig.usageSpool.replayBatchSize = 10
runtimeConfig.usageSpool.replayIntervalMs = 20

const input = {
  id: 'usage_spool_regression_1',
  createdAt: '2026-07-26T00:00:00.000Z',
  traceId: 'trace_spool_regression'
} as UsageRecordInput
const replayed: UsageRecordInput[] = []

try {
  runtimeConfig.usageSpool.replayIntervalMs = 2_000
  const stopStartedAt = Date.now()
  startUsageRecordSpoolReplay(async (record) => {
    replayed.push(record)
  })
  await stopUsageRecordSpoolReplay()
  assert.equal(replayed.length, 0, '空 spool 扫描期间发出的停止信号不得触发任何重放')
  assert.ok(
    Date.now() - stopStartedAt < 1_000,
    '空 spool 扫描期间发出的停止信号不得被后续完整退避休眠漏掉'
  )
  runtimeConfig.usageSpool.replayIntervalMs = 20

  await persistUsageRecordToSpool(input)
  const instanceDirectory = join(tempRoot, runtimeConfig.instanceId)
  assert.equal(readdirSync(instanceDirectory).filter((name) => name.endsWith('.json')).length, 1)
  assert.equal(getUsageRecordSpoolRuntime().persistedCount, 1)

  startUsageRecordSpoolReplay(async (record) => {
    replayed.push(record)
  })
  await waitUntil(() => replayed.length === 1 && pendingJsonFiles(instanceDirectory) === 0)
  await stopUsageRecordSpoolReplay()

  assert.equal(replayed[0]?.id, input.id, 'spool 重放必须保留首次生成的稳定 usage ID')
  assert.equal(replayed[0]?.createdAt, input.createdAt, 'spool 重放必须保留分区时间')
  assert.equal(getUsageRecordSpoolRuntime().replayedCount, 1)
  assert.equal(getUsageRecordSpoolRuntime().persistFailureCount, 0)
  assert.equal(getUsageRecordSpoolRuntime().replayFailureCount, 0)

  await writeFile(join(instanceDirectory, '000-corrupt.json'), '{not-json', 'utf8')
  await persistUsageRecordToSpool({
    ...input,
    id: 'usage_spool_regression_2',
    createdAt: '2026-07-26T00:00:01.000Z'
  })
  startUsageRecordSpoolReplay(async (record) => {
    replayed.push(record)
  })
  await waitUntil(() => replayed.length === 2 && pendingJsonFiles(instanceDirectory) === 0)
  await stopUsageRecordSpoolReplay()
  assert.equal(replayed[1]?.id, 'usage_spool_regression_2', '损坏文件不得阻断后续正常 usage 重放')
  assert.equal(readdirSync(instanceDirectory).filter((name) => name.endsWith('.corrupt')).length, 1)
  assert.equal(getUsageRecordSpoolRuntime().replayFailureCount, 1)

  const busyDirectory = join(tempRoot, 'gateway-fair-a')
  const quietDirectory = join(tempRoot, 'gateway-fair-b')
  await mkdir(busyDirectory)
  await mkdir(quietDirectory)
  for (let index = 0; index < 20; index += 1) {
    await writeSpoolFixture(busyDirectory, `busy-${index}`, `usage_spool_busy_${index}`)
  }
  await writeSpoolFixture(quietDirectory, 'quiet-0', 'usage_spool_quiet_0')
  runtimeConfig.usageSpool.replayBatchSize = 1
  const fairnessIds: string[] = []
  let releaseThirdReplay: (() => void) | undefined
  const thirdReplayGate = new Promise<void>((resolve) => {
    releaseThirdReplay = resolve
  })
  startUsageRecordSpoolReplay(async (record) => {
    const recordId = record.id
    assert.ok(recordId, 'spool fixture 必须保留稳定 usage ID')
    fairnessIds.push(recordId)
    if (fairnessIds.length === 3) await thirdReplayGate
  })
  await waitUntil(() => fairnessIds.length >= 3)
  assert.equal(
    fairnessIds.includes('usage_spool_quiet_0'),
    true,
    '持续繁忙 Gateway 的 spool 不得让其他实例目录长期饥饿'
  )
  releaseThirdReplay?.()
  await waitUntil(() => pendingJsonFiles(busyDirectory) === 0 && pendingJsonFiles(quietDirectory) === 0)
  await stopUsageRecordSpoolReplay()

  console.log('Usage 持久补偿回归通过：原子落盘、稳定 ID、损坏隔离、多 Gateway 公平轮转和后续重放均符合预期')
} finally {
  await stopUsageRecordSpoolReplay()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function writeSpoolFixture(directory: string, fileName: string, id: string): Promise<void> {
  await writeFile(join(directory, `${fileName}.json`), `${JSON.stringify({
    id,
    createdAt: '2026-07-26T00:00:02.000Z'
  })}\n`, 'utf8')
}

function pendingJsonFiles(directory: string): number {
  return readdirSync(directory).filter((name) => name.endsWith('.json')).length
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
  throw new Error('等待 usage spool 重放超时')
}
