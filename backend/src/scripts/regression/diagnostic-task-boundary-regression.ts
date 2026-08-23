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

for (const relativePath of [
  'src/modules/model-checks/model-checks.routes.ts',
  'src/modules/proxies/proxies.routes.ts'
]) {
  const source = readFileSync(resolve(relativePath), 'utf8')
  assert.match(source, /tryAcquireDiagnosticTaskSlot/, `${relativePath} 必须接入诊断任务并发闸门`)
  assert.match(source, /diagnosticTaskBusyMessage/, `${relativePath} 过载时必须快速返回繁忙提示`)
  assert.match(source, /Retry-After/, `${relativePath} 过载响应必须带 Retry-After`)
}

const proxyRoutesSource = readFileSync(resolve('src/modules/proxies/proxies.routes.ts'), 'utf8')
const proxyContractSource = readFileSync(resolve('src/modules/proxies/proxy-test.contract.ts'), 'utf8')
const proxyHandoverSource = readFileSync(resolve('src/modules/background/proxy-latency-handover.ts'), 'utf8')
const modelChecksRoutesSource = readFileSync(resolve('src/modules/model-checks/model-checks.routes.ts'), 'utf8')
const serverSource = readFileSync(resolve('src/server.ts'), 'utf8')
assert.match(proxyContractSource, /export const manualProxyTestDeadlineMs = runtimeConfig\.background\.proxyManualTestDeadlineMs/, '手动代理测试总耗时必须受配置保护')
assert.match(proxyRoutesSource, /runGoProxyManualExecution\(req\.params\.id\)/, '手动代理测试路由必须只调用 Go manual adapter')
assert.match(proxyHandoverSource, /deadline_ms:/, 'Go 手动 bridge 必须携带总 deadline')
assert.doesNotMatch(proxyRoutesSource, /testProxyById|update_proxy_test_state/, 'J3a 手动路由不得保留 Node executor 或 Node business writer')
assert.doesNotMatch(modelChecksRoutesSource, /modelCheckHttpRunDeadlineMs|AbortSignal\.timeout\(modelCheckHttpRunDeadlineMs\)/, '模型检测不能用固定总时限提前终止未完成探针')
assert.match(serverSource, /modelCheckHttpProxy = createDbServiceHttpProxy\(\{[\s\S]*chatTimeoutMs[\s\S]*\}\)/, '模型检测必须使用长时 DB service 代理预算')
assert.match(serverSource, /app\.use\(`\$\{systemApiPrefix\}\/my-model-checks`, modelCheckHttpProxy\)/, '用户模型检测必须绕过通用短代理超时')
assert.match(serverSource, /app\.use\(`\$\{systemApiPrefix\}\/model-checks`, modelCheckHttpProxy\)/, '管理模型检测必须绕过通用短代理超时')
assert.match(modelChecksRoutesSource, /export const modelCheckStreamHeartbeatMs = 10_000/, '流式模型检测必须定期输出心跳，避免 DB service HTTP proxy 空闲超时')
assert.match(modelChecksRoutesSource, /res\.write\(': connected\\n\\n'\)/, '流式模型检测必须立即输出首个 SSE 事件建立响应')
assert.match(modelChecksRoutesSource, /res\.write\(': heartbeat\\n\\n'\)/, '流式模型检测必须输出 SSE 心跳')

console.log('诊断任务并发边界回归通过：模型检测和代理检测超过上限时快速拒绝，手动代理检测受总耗时保护；账户测试由后台 worker 独立异步消费')
