import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import type { AccessScope } from '../../storage/access-scope.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-cancel-rpc-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-test-cancel-rpc-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const backendRoot = resolve('src')
const queueSource = readFileSync(resolve(backendRoot, 'modules', 'accounts', 'account-test-task-queue.service.ts'), 'utf8')
const queueRunSource = extractFunction(queueSource, 'async function runAccountTestQueueItem')

const databaseModule = await import('../../storage/database.js')
const accountTestTasks = await import('../../storage/account-test-tasks.repository.js')

databaseModule.getBusinessDatabase()

const access: AccessScope = { systemAccountId: 'sys_cancel_rpc', role: 'admin' }
const account = sampleAccount('acc_cancel_rpc')

try {
  await assertCancelWinsOverComplete()
  await assertCancelWinsOverFail()
  await assertHundredTaskCompletionDoesNotNeedCancelReads()
  assertQueueHasNoCancelPolling()
  console.log('account-test-cancel-rpc-deamplification-regression passed')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
  } catch {
    // ignore cleanup close errors
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertCancelWinsOverComplete(): Promise<void> {
  const task = accountTestTasks.createAccountTestTask({
    account,
    access,
    diagnostics: 'limited',
    model: 'gpt-4.1-mini'
  })
  const running = accountTestTasks.markAccountTestTaskRunning(task.id)
  assert.equal(running?.status, 'running', '任务应进入 running')

  const canceled = accountTestTasks.cancelAccountTestTask(task.id, access)
  assert.equal(canceled?.cancelRequested, true, '运行中取消应设置 cancel_requested')

  const completed = accountTestTasks.completeAccountTestTask(task.id, successResult(account))
  const finalRecord = accountTestTasks.getAccountTestTaskRecord(task.id)
  assert.equal(finalRecord?.status, 'canceled', '取消后 complete 不得覆盖为 success/failed')
  assert.equal(finalRecord?.cancelRequested, true, '取消后 complete 必须保留 cancel_requested')
  assert.equal(completed?.status, 'canceled', 'complete 返回值也应反映最终 canceled')
}

async function assertCancelWinsOverFail(): Promise<void> {
  const task = accountTestTasks.createAccountTestTask({
    account: sampleAccount('acc_cancel_fail'),
    access,
    diagnostics: 'limited'
  })
  assert.equal(accountTestTasks.markAccountTestTaskRunning(task.id)?.status, 'running')
  assert.equal(accountTestTasks.cancelAccountTestTask(task.id, access)?.cancelRequested, true)

  const failed = accountTestTasks.failAccountTestTask(task.id, 'simulated failure')
  const finalRecord = accountTestTasks.getAccountTestTaskRecord(task.id)
  assert.equal(finalRecord?.status, 'canceled', '取消后 fail 不得覆盖为 failed')
  assert.equal(finalRecord?.cancelRequested, true, '取消后 fail 必须保留 cancel_requested')
  assert.equal(failed?.status, 'canceled', 'fail 返回值也应反映最终 canceled')
}

async function assertHundredTaskCompletionDoesNotNeedCancelReads(): Promise<void> {
  const taskIds: string[] = []
  for (let index = 0; index < 100; index += 1) {
    const task = accountTestTasks.createAccountTestTask({
      account: sampleAccount(`acc_batch_${index}`),
      access,
      diagnostics: 'limited',
      model: 'gpt-4.1-mini'
    })
    taskIds.push(task.id)
    assert.equal(accountTestTasks.markAccountTestTaskRunning(task.id)?.status, 'running')
  }

  for (const taskId of taskIds) {
    const completed = accountTestTasks.completeAccountTestTask(taskId, successResult(sampleAccount('acc_batch')))
    assert.equal(completed?.status, 'success', '正常未取消任务应完成')
  }

  const cancelReadCallSites = countMatches(
    queueRunSource,
    /isAccountTestTaskCancelRequestedViaDbService|accountTestTaskCancelMessageViaDbService|is_account_test_task_cancel_requested|read_account_test_task_cancel_message/g
  )
  assert.equal(cancelReadCallSites, 0, '100 任务正常完成路径不得包含取消状态读 RPC')
}

function assertQueueHasNoCancelPolling(): void {
  assert.doesNotMatch(
    queueRunSource,
    /is_account_test_task_cancel_requested|read_account_test_task_cancel_message|isAccountTestTaskCancelRequestedViaDbService|accountTestTaskCancelMessageViaDbService/,
    '账号测试队列正常路径不得轮询取消状态或取消消息'
  )
  assert.match(
    queueSource,
    /cancelAccountTestTaskLocal|AbortController|mark_account_test_task_canceled/,
    '账号测试取消仍应通过本地 AbortController 与 canceled 写入收口'
  )
}

function sampleAccount(id: string): AccountSummary {
  return {
    id,
    name: `cancel-rpc-${id}`,
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

function successResult(target: AccountSummary): AccountTestResult {
  return {
    accountId: target.id,
    accountName: target.name,
    providerCode: target.providerCode,
    providerProtocolProfileId: target.providerProtocolProfileId,
    protocolCode: target.protocolCode,
    protocolVersion: target.protocolVersion,
    type: target.type,
    success: true,
    message: 'cancel-rpc regression success',
    model: 'gpt-4.1-mini'
  }
}

function extractFunction(source: string, signature: string): string {
  const start = source.indexOf(signature)
  assert.ok(start >= 0, `无法定位函数：${signature}`)
  let index = source.indexOf('{', start)
  assert.ok(index > start, `无法定位函数体：${signature}`)
  let depth = 0
  for (; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(start, index + 1)
      }
    }
  }
  throw new Error(`函数体未闭合：${signature}`)
}

function countMatches(source: string, pattern: RegExp): number {
  return source.match(pattern)?.length ?? 0
}
