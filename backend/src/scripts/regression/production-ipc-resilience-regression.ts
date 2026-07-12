import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const accountTestQueueSource = readFileSync(
  resolve('src/modules/accounts/account-test-task-queue.service.ts'),
  'utf8'
)
const backgroundJobsSource = readFileSync(
  resolve('src/modules/background/background-jobs.ts'),
  'utf8'
)

const sweepBody = functionBody(accountTestQueueSource, 'sweepManualAccountTestQueue')
assert.match(
  sweepBody,
  /void runAccountTestTaskMaintenance\('sweep'\)[\s\S]*\.catch\(\(error\) => \{[\s\S]*event:\s*'manual_account_test_sweep_failed'/,
  '账号测试定时维护必须显式收口 DB service 超时，禁止形成 unhandled rejection'
)

const aggregationSafetyBody = functionBody(backgroundJobsSource, 'usageStatsAggregationSafety')
assert.match(
  aggregationSafetyBody,
  /requestIngestWorkerDrainStatus\(5000\)/,
  '统计聚合安全检查必须给 ingest-worker 快照完整 IPC 容错窗口，避免事件循环抖动时误判不可用'
)

console.log('生产 IPC 韧性回归通过：账号测试维护拒绝已收口，统计安全快照使用 5 秒容错窗口')

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const parametersStart = sourceText.indexOf('(', start)
  assert(parametersStart >= 0, `函数 ${functionName} 缺少参数列表`)
  let parameterDepth = 0
  let parametersEnd = -1
  for (let index = parametersStart; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parameterDepth += 1
    if (char === ')') {
      parameterDepth -= 1
      if (parameterDepth === 0) {
        parametersEnd = index
        break
      }
    }
  }
  assert(parametersEnd >= 0, `函数 ${functionName} 参数列表未闭合`)
  const openBrace = sourceText.indexOf('{', parametersEnd)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return sourceText.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}
