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
assert.equal(result.action, 'recover', '宽限后连续 3 次失败必须定向恢复 DB service')
assert.equal(result.state.consecutiveFailures, 0)

state = result.state
for (const offset of [225_000, 240_000, 255_000]) {
  result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + offset, healthy: false })
  state = result.state
}
assert.equal(result.action, 'suppressed_cooldown', '上次恢复后 60 秒内必须阻断重复恢复')

result = recordDbServiceHealthProbe(state, { nowMs: startedAtMs + 270_000, healthy: true })
assert.equal(result.action, 'none')
assert.equal(result.state.consecutiveFailures, 0, '一次成功探测必须清空连续失败')

state = createDbServiceHealthRecoveryState(startedAtMs)
for (const recoveryStartedAt of [180_000, 240_000, 300_000]) {
  for (let index = 0; index < 3; index += 1) {
    result = recordDbServiceHealthProbe(state, {
      nowMs: startedAtMs + recoveryStartedAt + index * 15_000,
      healthy: false
    })
    state = result.state
  }
  assert.equal(result.action, 'recover')
}
for (let index = 0; index < 3; index += 1) {
  result = recordDbServiceHealthProbe(state, {
    nowMs: startedAtMs + 360_000 + index * 15_000,
    healthy: false
  })
  state = result.state
}
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
