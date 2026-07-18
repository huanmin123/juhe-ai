import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  createDbServiceHealthRecoveryState,
  recordDbServiceHealthProbe,
  resetDbServiceHealthRecoveryState
} from '../../modules/db-service/db-service-health-recovery.js'

const startedAtMs = 1_000_000
let state = createDbServiceHealthRecoveryState(startedAtMs)

let result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + 179_999, healthy: false })
assert.equal(result.action, 'ignored_grace', '180 秒启动宽限内失败不得累计或恢复')
assert.equal(result.state.consecutiveFailures, 0)

result = recordDbServiceHealthProbe(result.state, { nowMs: startedAtMs + 180_000, healthy: false })
assert.equal(result.action, 'none')
assert.equal(result.state.consecutiveFailures, 1)
result = recordDbServiceHealthProbe(result.state, { nowMs: startedAtMs + 195_000, healthy: false })
assert.equal(result.action, 'none')
result = recordDbServiceHealthProbe(result.state, { nowMs: startedAtMs + 210_000, healthy: false })
assert.equal(result.action, 'none', '短于五分钟的 DB health 异常不得终止子进程')
result = recordDbServiceHealthProbe(result.state, { nowMs: startedAtMs + 479_999, healthy: false })
assert.equal(result.action, 'none')
result = recordDbServiceHealthProbe(result.state, { nowMs: startedAtMs + 480_000, healthy: false })
assert.equal(result.action, 'recover', '宽限后持续失败五分钟且最终复核仍失败才能定向恢复 DB service')
assert.equal(result.state.consecutiveFailures, 0)

state = result.state
result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + 495_000, healthy: true })
assert.equal(result.action, 'none')
assert.equal(result.state.consecutiveFailures, 0, '一次成功探测必须清空连续失败')

state = {
  ...createDbServiceHealthRecoveryState(startedAtMs),
  consecutiveFailures: 20,
  failureStartedAtMs: startedAtMs + 180_000,
  recoveryAttemptsMs: [startedAtMs + 300_000, startedAtMs + 360_000, startedAtMs + 420_000]
}
result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + 480_000, healthy: false })
assert.equal(result.action, 'suppressed_budget', '15 分钟内第 4 次恢复必须只告警，不再终止 child')

state = resetDbServiceHealthRecoveryState(state, startedAtMs + 400_000)
result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + 400_001, healthy: false })
assert.equal(result.action, 'ignored_grace', '新 child 必须重新获得完整 180 秒宽限')
assert.equal(result.state.recoveryAttemptsMs.length, 3, '恢复预算必须跨 child 保留，避免连续重启绕过 15 分钟上限')

const supervisorSource = readFileSync(
  new URL('../../modules/db-service/db-service-supervisor.ts', import.meta.url),
  'utf8'
).replace(/\r\n/g, '\n')

assert.match(supervisorSource, /getDbServiceState\(\)/, 'supervisor 必须读取 DB service ready 消息中的 health host/port')
assert.match(supervisorSource, /AbortSignal\.timeout\(dbServiceHealthProbeTimeoutMs\)/, 'health 探测必须有 5 秒超时')
assert.match(supervisorSource, /await response\.arrayBuffer\(\)/, 'health 响应体必须消费，避免长期探测占用连接资源')
assert.match(supervisorSource, /child\.kill\('SIGTERM'\)/, '定向恢复必须先 TERM 当前 child')
assert.match(supervisorSource, /child\.kill\('SIGKILL'\)/, '10 秒未退出必须能 KILL 同一 child')
assert.match(supervisorSource, /dbServiceProcess === child[\s\S]*child\.pid === childPid/, 'KILL 前必须再次核对 child 对象和 PID')
assert.match(supervisorSource, /child\.signalCode === null/, 'KILL 前必须确认 child 尚未以信号退出')
assert.match(supervisorSource, /clearDbServiceHealthMonitor\(\)/, 'shutdown 必须停止 health 与 kill timer')

console.log('DB service health 定向恢复回归通过')
