import { strict as assert } from 'node:assert'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  updateAccountHealthCheckModelAsync
} from '../../storage/repositories.js'
import {
  accountTestTaskCancelMessageAsync,
  cancelAccountTestSessionAsync,
  completeAccountTestSessionAsync,
  createAccountTestSessionAsync,
  createAccountTestTaskAsync,
  getAccountTestTaskRecordAsync,
  heartbeatAccountTestSessionAsync,
  isAccountTestTaskCancelRequestedAsync,
  listRunnableAccountTestTaskIdsAsync,
  markAccountTestTaskRunningAsync,
  updateAccountTestTaskMessageAsync,
  completeAccountTestTaskAsync,
  failAccountTestTaskAsync,
  cancelAccountTestTaskAsync
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
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

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
  const completedSession = await completeAccountTestSessionAsync(session.id, access)
  assert.equal(completedSession?.status, 'completed', 'PG settled account test session should complete')

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

  const raceSession = await createAccountTestSessionAsync(access)
  sessionIds.push(raceSession.id)
  const raceCompleteTask = await createAccountTestTaskAsync({
    account,
    access,
    diagnostics: 'limited',
    sessionId: raceSession.id
  })
  taskIds.push(raceCompleteTask.id)
  assert.equal((await markAccountTestTaskRunningAsync(raceCompleteTask.id))?.status, 'running', 'PG race complete task should start')
  assert.equal((await cancelAccountTestTaskAsync(raceCompleteTask.id, access))?.cancelRequested, true, 'PG race complete task should accept cancel')
  const raceCompleted = await completeAccountTestTaskAsync(raceCompleteTask.id, result)
  assert.equal(raceCompleted?.status, 'canceled', 'PG cancel must win over complete')
  assert.equal((await getAccountTestTaskRecordAsync(raceCompleteTask.id))?.status, 'canceled', 'PG complete race final status must stay canceled')

  const raceFailTask = await createAccountTestTaskAsync({
    account,
    access,
    diagnostics: 'limited',
    sessionId: raceSession.id
  })
  taskIds.push(raceFailTask.id)
  assert.equal((await markAccountTestTaskRunningAsync(raceFailTask.id))?.status, 'running', 'PG race fail task should start')
  assert.equal((await cancelAccountTestTaskAsync(raceFailTask.id, access))?.cancelRequested, true, 'PG race fail task should accept cancel')
  const raceFailed = await failAccountTestTaskAsync(raceFailTask.id, 'PG smoke fail race')
  assert.equal(raceFailed?.status, 'canceled', 'PG cancel must win over fail')
  assert.equal((await getAccountTestTaskRecordAsync(raceFailTask.id))?.status, 'canceled', 'PG fail race final status must stay canceled')

  const accountAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
  const group = await createGroupAsync({
    name: `账号测试模型 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, accountAccess)
  createdGroupIds.push(group.id)
  const storedAccount = await createAccountAsync({
    name: `账号测试模型 PG smoke 账户 ${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'active',
    groupId: group.id,
    credentials: {
      api_key: `sk-account-test-model-pg-${marker}`,
      base_url: 'https://example.invalid/v1'
    },
    supportedModels: ['gpt-5.6-sol']
  }, accountAccess)
  createdAccountIds.push(storedAccount.id)
  const firstEnsuredModel = await updateAccountHealthCheckModelAsync(
    storedAccount.id,
    'gpt-5.6-terra',
    accountAccess,
    true
  )
  assert(firstEnsuredModel?.supportedModels?.includes('gpt-5.6-sol'), 'PG 原子追加不能删除账户原支持模型')
  assert(firstEnsuredModel?.supportedModels?.includes('gpt-5.6-terra'), 'PG 应追加第一个成功模型')
  const secondEnsuredModel = await updateAccountHealthCheckModelAsync(
    storedAccount.id,
    'gpt-5.6-luna',
    accountAccess,
    true
  )
  assert(secondEnsuredModel?.supportedModels?.includes('gpt-5.6-terra'), 'PG 连续追加第二个模型不能删除第一个成功模型')
  assert(secondEnsuredModel?.supportedModels?.includes('gpt-5.6-luna'), 'PG 应追加第二个成功模型')
  assert.equal(secondEnsuredModel?.healthCheckModel, 'gpt-5.6-luna', 'PG 连续更新后应保存最后检查模型')

  console.log(JSON.stringify({
    message: '账号测试任务 PG smoke 通过',
    taskCount: taskIds.length,
    sessionCount: sessionIds.length,
    ensuredModelCount: 2
  }))
} finally {
  const accountAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
  for (const accountId of createdAccountIds.reverse()) {
    await deleteAccountAsync(accountId, accountAccess).catch(() => false)
  }
  for (const groupId of createdGroupIds.reverse()) {
    await deleteGroupAsync(groupId, accountAccess).catch(() => undefined)
  }
  await cleanupSmokeRows(taskIds, sessionIds)
  await closeRedisClients()
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
