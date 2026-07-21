import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestTaskPollingPath = resolve(frontendRoot, 'src/views/accounts/accountTestTaskPolling.ts')

const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestTaskPollingSource = readFileSync(accountTestTaskPollingPath, 'utf8')
const waitForSubmittedAccountTestResultSource = accountTestModalSource.slice(
  accountTestModalSource.indexOf('function waitForSubmittedAccountTestResult'),
  accountTestModalSource.indexOf('function persistAccountTestRunSession')
)

assertIncludes(accountTestModalSource, "import { waitForAccountTestResult } from './accountTestTaskPolling'", '账户测试弹窗应通过 task polling helper 等待后台任务结果')
assertIncludes(accountTestTaskPollingSource, 'export async function waitForAccountTestResult', '任务轮询 helper 应导出后台测试任务结果等待函数')
assertIncludes(accountTestTaskPollingSource, 'export function accountTestTaskTimeoutResult', '任务轮询 helper 应负责测试任务超时结果构造')
assertIncludes(accountTestTaskPollingSource, 'waitForPollDelay', '任务轮询 helper 应负责 poll delay 等待')
assertIncludes(accountTestTaskPollingSource, 'accountTestTaskRemainingWaitMs(task)', '任务轮询 helper 应按任务剩余等待窗口控制轮询延迟')
assertIncludes(accountTestTaskPollingSource, 'accountTestTaskMaxWaitMs', '任务轮询 helper 应负责最大等待时间判断')
assertIncludes(accountTestTaskPollingSource, 'failedAccountTestResult', '任务轮询 helper 应负责无结果任务和超时任务的失败结果兜底')
assertIncludes(accountTestTaskPollingSource, 'fetchTask(task.id, account, signal)', '任务轮询 helper 应通过调用方传入的 fetchTask 拉取最新任务')
assertIncludes(accountTestTaskPollingSource, 'cancelTask(task.id, account)', '任务轮询 helper 应通过调用方传入的 cancelTask 停止超时任务')
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
