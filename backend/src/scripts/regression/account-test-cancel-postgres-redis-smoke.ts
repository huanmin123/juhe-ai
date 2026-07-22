import assert from 'node:assert/strict'

import { createClient } from 'redis'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  cancelAccountTestTaskAsync,
  completeAccountTestTaskAsync,
  createAccountTestTaskAsync,
  failAccountTestTaskAsync,
  getAccountTestTaskRecordAsync,
  markAccountTestTaskRunningAsync
} from '../../storage/account-test-tasks.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'PG/Redis smoke requires PostgreSQL mode')

const marker = `account_test_cancel_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: `sys_${marker}`, role: 'admin' }
const taskIds: string[] = []

try {
  await assertRedisIsolation()
  await assertHundredTaskCompletion()
  await assertCancelWinsOverCompleteAndFail()
  console.log(JSON.stringify({
    message: 'account test cancellation PostgreSQL/Redis smoke passed',
    taskCount: taskIds.length,
    redisRoleCount: 3
  }))
} finally {
  await cleanupSmokeRows(taskIds)
  await closeRedisClients()
  await closePostgresPool()
}

async function assertHundredTaskCompletion(): Promise<void> {
  const tasks = await Promise.all(Array.from({ length: 100 }, async (_, index) => {
    const account = sampleAccount(`batch_${index}`)
    const task = await createAccountTestTaskAsync({ account, access, diagnostics: 'limited' })
    taskIds.push(task.id)
    return { task, account }
  }))

  const running = await Promise.all(tasks.map(({ task }) => markAccountTestTaskRunningAsync(task.id)))
  assert.equal(running.filter((task) => task?.status === 'running').length, 100, 'all 100 tasks must be claimed')

  const completed = await Promise.all(tasks.map(({ task, account }) => completeAccountTestTaskAsync(task.id, successResult(account))))
  assert.equal(completed.filter((task) => task?.status === 'success').length, 100, 'all 100 tasks must complete without cancellation reads')
}

async function assertCancelWinsOverCompleteAndFail(): Promise<void> {
  const completeAccount = sampleAccount('cancel_complete')
  const completeTask = await createAccountTestTaskAsync({ account: completeAccount, access, diagnostics: 'limited' })
  taskIds.push(completeTask.id)
  assert.equal((await markAccountTestTaskRunningAsync(completeTask.id))?.status, 'running')
  assert.equal((await cancelAccountTestTaskAsync(completeTask.id, access))?.cancelRequested, true)
  assert.equal((await completeAccountTestTaskAsync(completeTask.id, successResult(completeAccount)))?.status, 'canceled')
  assert.equal((await getAccountTestTaskRecordAsync(completeTask.id))?.status, 'canceled')

  const failAccount = sampleAccount('cancel_fail')
  const failTask = await createAccountTestTaskAsync({ account: failAccount, access, diagnostics: 'limited' })
  taskIds.push(failTask.id)
  assert.equal((await markAccountTestTaskRunningAsync(failTask.id))?.status, 'running')
  assert.equal((await cancelAccountTestTaskAsync(failTask.id, access))?.cancelRequested, true)
  assert.equal((await failAccountTestTaskAsync(failTask.id, 'simulated failure'))?.status, 'canceled')
  assert.equal((await getAccountTestTaskRecordAsync(failTask.id))?.status, 'canceled')
}

async function assertRedisIsolation(): Promise<void> {
  const urls = [
    process.env.JUHE_AI_REDIS_CACHE_URL,
    process.env.JUHE_AI_REDIS_STATE_URL,
    process.env.JUHE_AI_REDIS_QUEUE_URL
  ]
  assert.ok(urls.every(Boolean), 'cache/state/queue Redis URLs are required')
  const origins = urls.map((url) => {
    const parsed = new URL(url!)
    return `${parsed.hostname}:${parsed.port || '6379'}`
  })
  assert.equal(new Set(origins).size, 3, 'cache/state/queue must use three Redis processes')

  for (const url of urls) {
    const client = createClient({ url })
    try {
      await client.connect()
      assert.equal(await client.ping(), 'PONG')
    } finally {
      await client.close().catch(() => undefined)
    }
  }
}

async function cleanupSmokeRows(ids: string[]): Promise<void> {
  if (ids.length === 0) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.account_test_session_tasks WHERE task_id = ANY($1::text[])', [ids])
  await pool.query('DELETE FROM juhe_business.account_test_tasks WHERE id = ANY($1::text[])', [ids])
}

function sampleAccount(suffix: string): AccountSummary {
  return {
    id: `acc_${marker}_${suffix}`,
    name: `account test cancel ${suffix}`,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    status: 'active',
    clientCompatibility: 'openai_standard',
    accessType: 'owner',
    permissions: { canUse: true }
  } as AccountSummary
}

function successResult(account: AccountSummary): AccountTestResult {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    success: true,
    message: 'smoke success'
  }
}
