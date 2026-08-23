import { readFileSync } from 'node:fs'
import assert from 'node:assert/strict'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { accountTestTaskQueuedDeadlineMs, parseTaskTime } from '../../src/views/accounts/accountTestTaskHelpers'
import { accountTestTaskTimeoutResult, waitForAccountTestResult } from '../../src/views/accounts/accountTestTaskPolling'
import type { AccountListItem, AccountTestTask } from '../../src/types/domain'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestDialogPath = resolve(frontendRoot, 'src/views/accounts/AccountTestModal.vue')
const accountTestTaskPollingPath = resolve(frontendRoot, 'src/views/accounts/accountTestTaskPolling.ts')

const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestDialogSource = readFileSync(accountTestDialogPath, 'utf8')
const accountTestTaskPollingSource = readFileSync(accountTestTaskPollingPath, 'utf8')
const waitForSubmittedAccountTestResultSource = accountTestModalSource.slice(
  accountTestModalSource.indexOf('function waitForSubmittedAccountTestResult'),
  accountTestModalSource.indexOf('function persistAccountTestRunSession')
)

const strictEpoch = 1784032496123
assert.equal(parseTaskTime('2026-07-14T12:34:56.123Z'), strictEpoch, 'RFC3339 startedAt 应按绝对时间解析')
assert.equal(parseTaskTime('2026-07-14 20:34:56.123'), undefined, '无时区 legacy startedAt 必须拒绝')
assert.equal(parseTaskTime('2026-07-14T12:34:56'), undefined, '无时区 startedAt 必须拒绝')
assert.equal(parseTaskTime('2026-02-30T12:34:56Z'), undefined, '非法日期 startedAt 必须拒绝')

const testAccount = { id: 'account-time-contract', name: '时间契约账号', providerCode: 'openai', type: 'api_key' } as AccountListItem
const invalidTask = {
  id: 'task-invalid-time',
  accountId: testAccount.id,
  accountName: testAccount.name,
  providerCode: testAccount.providerCode,
  type: testAccount.type,
  status: 'running',
  model: 'gpt-test',
  createdAt: '2026-07-14T12:34:56Z',
  queuedAt: '2026-07-14T12:34:56Z',
  updatedAt: '2026-07-14T12:34:56Z'
} as AccountTestTask
const invalidStartedAtResult = accountTestTaskTimeoutResult({
  account: testAccount,
  testEndpointMode: 'account_default',
  model: 'gpt-test',
  task: invalidTask
})
assert.match(invalidStartedAtResult?.message ?? '', /缺少 startedAt/, '缺失 startedAt 必须返回显式失败')
const expiredTask = { ...invalidTask, id: 'task-expired-time', startedAt: '2020-01-01T00:00:00Z' } as AccountTestTask
assert.match(accountTestTaskTimeoutResult({ account: testAccount, testEndpointMode: 'account_default', model: 'gpt-test', task: expiredTask })?.message ?? '', /超过/, '真实超时 startedAt 必须返回超时失败')
const queuedTask = {
  ...invalidTask,
  id: 'task-queued-timeout',
  status: 'queued',
  queuedDeadlineAt: '2020-01-01T00:00:00Z'
} as AccountTestTask
assert.equal(accountTestTaskQueuedDeadlineMs(queuedTask), Date.parse('2020-01-01T00:00:00Z'), 'queued 任务必须优先使用服务端下发的截止时间')
assert.match(
  accountTestTaskTimeoutResult({ account: testAccount, testEndpointMode: 'account_default', model: 'gpt-test', task: queuedTask })?.message ?? '',
  /worker 未在排队窗口内接收账号测试任务/,
  'queued 任务超过排队窗口必须返回显式失败'
)
const invalidQueuedTask = { ...queuedTask, id: 'task-invalid-queued-time', queuedAt: '2026-02-30T12:34:56Z', queuedDeadlineAt: undefined } as AccountTestTask
assert.match(
  accountTestTaskTimeoutResult({ account: testAccount, testEndpointMode: 'account_default', model: 'gpt-test', task: invalidQueuedTask })?.message ?? '',
  /queuedAt 无法严格解析/,
  'queued 任务缺少可解析排队时间必须返回显式失败'
)
let invalidTaskCancelCount = 0
const invalidTaskResult = await waitForAccountTestResult({
  account: testAccount,
  cancelTask: async () => { invalidTaskCancelCount += 1 },
  currentTestEndpointMode: () => 'account_default',
  currentModel: () => 'gpt-test',
  fetchTask: async () => invalidTask,
  initialTask: invalidTask,
  signal: new AbortController().signal
})
assert.equal(invalidTaskCancelCount, 1, 'invalid startedAt 必须停止后台任务')
assert.match(invalidTaskResult.message, /缺少 startedAt/, '轮询 invalid startedAt 必须返回显式失败')
const canceledTaskResult = await waitForAccountTestResult({
  account: testAccount,
  cancelTask: async () => undefined,
  currentTestEndpointMode: () => 'account_default',
  currentModel: () => 'gpt-test',
  fetchTask: async () => { throw new Error('canceled task 不应继续轮询') },
  initialTask: { ...expiredTask, status: 'canceled', message: '后台已取消' },
  signal: new AbortController().signal
})
assert.match(canceledTaskResult.message, /后台已取消/, '服务端 canceled 任务必须返回可见失败，而非伪装成本地 Abort')
const queuedCancelFailureResult = await waitForAccountTestResult({
  account: testAccount,
  cancelTask: async () => { throw new Error('取消接口不可用') },
  currentTestEndpointMode: () => 'account_default',
  currentModel: () => 'gpt-test',
  fetchTask: async () => { throw new Error('超时任务不应继续轮询') },
  initialTask: queuedTask,
  signal: new AbortController().signal
})
assert.match(queuedCancelFailureResult.message, /停止后台任务失败：取消接口不可用/, '排队超时取消失败时仍必须返回明确超时结果')

assertIncludes(accountTestModalSource, "import { waitForAccountTestResult } from './accountTestTaskPolling'", '账户测试弹窗应通过 task polling helper 等待后台任务结果')
assertIncludes(accountTestTaskPollingSource, 'export async function waitForAccountTestResult', '任务轮询 helper 应导出后台测试任务结果等待函数')
assertIncludes(accountTestTaskPollingSource, 'export function accountTestTaskTimeoutResult', '任务轮询 helper 应负责测试任务超时结果构造')
assertIncludes(accountTestTaskPollingSource, 'waitForPollDelay', '任务轮询 helper 应负责 poll delay 等待')
assertIncludes(accountTestTaskPollingSource, 'accountTestTaskRemainingWaitMs(task)', '任务轮询 helper 应按任务剩余等待窗口控制轮询延迟')
assertIncludes(accountTestTaskPollingSource, 'accountTestTaskMaxWaitMs', '任务轮询 helper 应负责最大等待时间判断')
assertIncludes(accountTestTaskPollingSource, 'accountTestTaskQueuedDeadlineMs', '任务轮询 helper 应负责 queued 排队截止时间判断')
assertIncludes(accountTestTaskPollingSource, 'failedAccountTestResult', '任务轮询 helper 应负责无结果任务和超时任务的失败结果兜底')
assertIncludes(accountTestTaskPollingSource, 'startedAt 无法严格解析', '非法 startedAt 必须显式失败')
assertIncludes(accountTestTaskPollingSource, '缺少 startedAt', '缺失 startedAt 必须显式失败')
assertNotIncludes(accountTestTaskPollingSource, 'Date.parse(task.startedAt)', '任务轮询不得直接使用 Date.parse')
assertNotIncludes(accountTestTaskPollingSource, ': Date.now()', '任务轮询不得以 Date.now 静默补 startedAt')
assertIncludes(accountTestTaskPollingSource, 'fetchTask(task.id, account, signal)', '任务轮询 helper 应通过调用方传入的 fetchTask 拉取最新任务')
assertIncludes(accountTestTaskPollingSource, 'cancelTask(task.id, account)', '任务轮询 helper 应通过调用方传入的 cancelTask 停止超时任务')
assertIncludes(accountTestTaskPollingSource, '停止后台任务失败', '取消超时任务失败时必须保留可见的超时结果')
assertIncludes(accountTestTaskPollingSource, 'onTaskSettled?.(task.id)', '任务轮询 helper 应通知调用方清理已结束任务')

assertNotIncludes(accountTestModalSource, 'waitForPollDelay', '账户测试弹窗不应直接持有轮询延迟循环')
assertNotIncludes(accountTestModalSource, 'accountTestTaskRemainingWaitMs', '账户测试弹窗不应直接计算任务剩余等待窗口')
assertNotIncludes(accountTestModalSource, 'accountTestTaskMaxWaitMs', '账户测试弹窗不应直接判断任务最大等待时间')
assertNotIncludes(accountTestModalSource, 'function accountTestTaskTimeoutResult', '账户测试弹窗不应直接构造任务超时结果')
assertNotIncludes(accountTestModalSource, "task.status === 'success' || task.status === 'failed'", '账户测试弹窗不应直接判断轮询终态')
assertNotIncludes(waitForSubmittedAccountTestResultSource, "task.status === 'canceled'", '账户测试弹窗的轮询适配层不应直接处理取消终态')
assertNotIncludes(accountTestModalSource, 'while (true)', '账户测试弹窗不应直接持有任务轮询主循环')
assertNotIncludes(accountTestModalSource, 'runBatchAccountTest', '账户测试弹窗不应保留批量任务轮询编排')


const accountTestTaskHelpersSource = readFileSync(resolve(frontendRoot, 'src/views/accounts/accountTestTaskHelpers.ts'), 'utf8')
assertIncludes(accountTestTaskHelpersSource, 'export const accountTestPollIntervalMs = 3000', '任务轮询间隔应为 3000ms')
assertNotIncludes(accountTestTaskHelpersSource, 'export const accountTestPollIntervalMs = 1000', '任务轮询间隔不得回退为 1000ms')
assertIncludes(accountTestTaskHelpersSource, 'accountImageDiagnosticAttemptTimeoutsMs = [120_000]', '图片测试轮询窗口必须保留单次 120 秒')
assertIncludes(accountTestTaskHelpersSource, "testEndpointMode === 'images_json'", '图片测试必须按 Images API 请求形态选择独立轮询窗口')
assertIncludes(accountTestDialogSource, '<div v-if="result" class="test-result-meta">', '图片测试完成后必须保留完整结果 JSON 区域')
assertNotIncludes(accountTestDialogSource, '<div v-if="result && !imageTest" class="test-result-meta">', '图片测试不得隐藏完整结果 JSON 区域')
assertIncludes(accountTestDialogSource, '<a-button :disabled="!result" @click="$emit(\'copy-result\', resultJson)">复制完整结果</a-button>', '图片测试必须允许复制已脱敏的完整结果')

console.log('账户测试任务轮询回归通过：轮询循环、超时结果和任务结束清理边界保持分离，间隔 3s')

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}
