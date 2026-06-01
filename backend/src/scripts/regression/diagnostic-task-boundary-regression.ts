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

for (const relativePath of [
  'src/modules/accounts/accounts.routes.ts',
  'src/modules/model-checks/model-checks.routes.ts',
  'src/modules/proxies/proxies.routes.ts'
]) {
  const source = readFileSync(resolve(relativePath), 'utf8')
  assert.match(source, /tryAcquireDiagnosticTaskSlot/, `${relativePath} 必须接入诊断任务并发闸门`)
  assert.match(source, /diagnosticTaskBusyMessage/, `${relativePath} 过载时必须快速返回繁忙提示`)
  assert.match(source, /Retry-After/, `${relativePath} 过载响应必须带 Retry-After`)
}

console.log('诊断任务并发边界回归通过：账户测试、模型检测和代理检测超过上限时快速拒绝，不在 DB service 事件循环排队')
