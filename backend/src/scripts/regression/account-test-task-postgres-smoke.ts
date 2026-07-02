import { strict as assert } from 'node:assert'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  accountTestTaskCancelMessageAsync,
  cancelAccountTestSessionAsync,
  createAccountTestSessionAsync,
  createAccountTestTaskAsync,
  getAccountTestTaskRecordAsync,
  heartbeatAccountTestSessionAsync,
  isAccountTestTaskCancelRequestedAsync,
  listRunnableAccountTestTaskIdsAsync,
  markAccountTestTaskRunningAsync,
  updateAccountTestTaskMessageAsync,
  completeAccountTestTaskAsync
} from '../../storage/account-test-tasks.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账号测试任务 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `account_test_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: `sys_${marker}`, role: 'admin' }
const account = {
  id: `acc_${marker}`,
  name: `账号测试 PG smoke ${marker}`,
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

const taskIds: string[] = []
const sessionIds: string[] = []

try {
  const session = await createAccountTestSessionAsync(access)
  sessionIds.push(session.id)
  const heartbeat = await heartbeatAccountTestSessionAsync(session.id, access)
  assert.equal(heartbeat?.status, 'running', 'PG session heartbeat should keep session running')

  const task = await createAccountTestTaskAsync({
    account,
    access,
    diagnostics: 'full',
    sessionId: session.id,
    model: 'gpt-4.1-mini'
  })
  taskIds.push(task.id)

  const runnableIds = await listRunnableAccountTestTaskIdsAsync(20)
  assert.ok(runnableIds.includes(task.id), 'PG queued account test task should be runnable')

  const running = await markAccountTestTaskRunningAsync(task.id)
  assert.equal(running?.status, 'running', 'PG account test task should move to running')

  const updated = await updateAccountTestTaskMessageAsync(task.id, 'PG smoke running')
  assert.equal(updated?.message, 'PG smoke running', 'PG account test task message should update')
  assert.equal(await isAccountTestTaskCancelRequestedAsync(task.id), false, 'PG running task should not be canceled by default')

  const result: AccountTestResult = {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    success: true,
    message: 'PG smoke success',
    model: 'gpt-4.1-mini'
  }
  const completed = await completeAccountTestTaskAsync(task.id, result)
  assert.equal(completed?.status, 'success', 'PG account test task should complete')
  assert.equal((await getAccountTestTaskRecordAsync(task.id))?.result?.success, true, 'PG completed task result should be readable')

  const cancelSession = await createAccountTestSessionAsync(access)
  sessionIds.push(cancelSession.id)
  const cancelTask = await createAccountTestTaskAsync({
    account,
    access,
    diagnostics: 'limited',
    sessionId: cancelSession.id
  })
  taskIds.push(cancelTask.id)
  const canceledSession = await cancelAccountTestSessionAsync(cancelSession.id, access, 'PG smoke cancel')
  assert.ok(canceledSession?.taskIds.includes(cancelTask.id), 'PG session cancel should return affected queued task')
  assert.equal(await isAccountTestTaskCancelRequestedAsync(cancelTask.id), true, 'PG session cancel should mark task canceled')
  assert.equal(await accountTestTaskCancelMessageAsync(cancelTask.id), 'PG smoke cancel', 'PG cancel message should be readable')

  console.log(JSON.stringify({
    message: '账号测试任务 PG smoke 通过',
    taskCount: taskIds.length,
    sessionCount: sessionIds.length
  }))
} finally {
  await cleanupSmokeRows(taskIds, sessionIds)
  await closePostgresPool()
}

async function cleanupSmokeRows(taskIdsToDelete: string[], sessionIdsToDelete: string[]): Promise<void> {
  const pool = await getPostgresPool()
  if (taskIdsToDelete.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_test_session_tasks WHERE task_id = ANY($1::text[])', [taskIdsToDelete])
    await pool.query('DELETE FROM juhe_business.account_test_tasks WHERE id = ANY($1::text[])', [taskIdsToDelete])
  }
  if (sessionIdsToDelete.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_test_sessions WHERE id = ANY($1::text[])', [sessionIdsToDelete])
  }
}
