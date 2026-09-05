import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  diagnosticTaskMaxInFlight,
  diagnosticTaskRuntime,
  tryAcquireDiagnosticTaskSlot
} from '../../modules/diagnostics/diagnostic-task-limiter.js'

const releases: Array<() => void> = []
for (let index = 0; index < diagnosticTaskMaxInFlight; index += 1) {
  const release = tryAcquireDiagnosticTaskSlot()
  assert.ok(release, `第 ${index + 1} 个诊断任务应能获得槽位`)
  releases.push(release)
}

assert.equal(diagnosticTaskRuntime().active, diagnosticTaskMaxInFlight, '诊断任务 active 计数应达到上限')
assert.equal(tryAcquireDiagnosticTaskSlot(), undefined, '超过上限的诊断任务必须快速拒绝，不能排队')

for (const release of releases) {
  release()
}
assert.equal(diagnosticTaskRuntime().active, 0, '释放后 active 计数应归零')

const reusableRelease = tryAcquireDiagnosticTaskSlot()
assert.ok(reusableRelease, '释放槽位后应允许新的诊断任务进入')
reusableRelease()

const accountTaskQueueSource = readFileSync(resolve('src/modules/accounts/account-test-task-queue.service.ts'), 'utf8')
assert.doesNotMatch(accountTaskQueueSource, /tryAcquireDiagnosticTaskSlot/, '账户测试后台 worker 不应接入共享诊断任务并发闸门')
assert.doesNotMatch(accountTaskQueueSource, /diagnosticTaskBusyMessage/, '账户测试后台 worker 不应因为共享诊断闸门繁忙而快速失败')

console.log('诊断任务并发边界回归通过：共享诊断任务超过上限时快速拒绝；账户测试由后台 worker 独立异步消费。J3b 模型检测已由 Gateway 接管，不再属于 Node 诊断闸门。')
