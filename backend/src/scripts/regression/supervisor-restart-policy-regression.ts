import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { Writable } from 'node:stream'

import {
  createSupervisorRestartState,
  recordSupervisorChildReady,
  recordSupervisorChildStopped,
  supervisorRestartDelayMs
} from '../../shared/supervisor-restart-policy.js'
import { forwardSupervisorOutput } from '../../shared/supervisor-output.js'

const expectedDelays = [1_000, 5_000, 15_000, 30_000, 60_000, 120_000, 300_000, 600_000, 600_000]
assert.deepEqual(
  expectedDelays.map((_, index) => supervisorRestartDelayMs(index + 1)),
  expectedDelays,
  'worker/DB supervisor 必须使用统一的有界退避档位'
)

let state = createSupervisorRestartState()
state = recordSupervisorChildStopped(state, 1_000)
assert.equal(state.restartAttempts, 1)
state = recordSupervisorChildReady(state, 2_000)
state = recordSupervisorChildStopped(state, 2_001)
assert.equal(state.restartAttempts, 2, '仅 ready 不能清空短时崩溃历史')

state = recordSupervisorChildReady(state, 3_000)
state = recordSupervisorChildStopped(state, 3_000 + 10 * 60_000 - 1)
assert.equal(state.restartAttempts, 3, '稳定不足十分钟时不能清零退避')

state = recordSupervisorChildReady(state, 4_000)
state = recordSupervisorChildStopped(state, 4_000 + 10 * 60_000)
assert.equal(state.restartAttempts, 1, '连续稳定十分钟后下一次退出应从首档重新开始')

const closedOutput = new Writable({
  write(_chunk, _encoding, callback) {
    const error = Object.assign(new Error('broken pipe'), { code: 'EPIPE' })
    callback(error)
  }
})
closedOutput.on('error', () => undefined)
assert.doesNotThrow(() => forwardSupervisorOutput(closedOutput, Buffer.from('test')))

const rawDebugMessages: string[] = []
const processWithRawDebug = process as typeof process & { _rawDebug?: (message: string) => void }
const originalRawDebug = processWithRawDebug._rawDebug
processWithRawDebug._rawDebug = (message: string) => rawDebugMessages.push(message)
try {
  const brokenOutput = new Writable({
    write() {
      throw Object.assign(new Error('unexpected output failure'), { code: 'EIO' })
    }
  })
  assert.equal(forwardSupervisorOutput(brokenOutput, Buffer.from('test')), false)
  assert.match(rawDebugMessages.join('\n'), /unexpected output failure/, '非 EPIPE 输出异常必须保留诊断')
} finally {
  processWithRawDebug._rawDebug = originalRawDebug
}

const maintenanceQueueSource = readFileSync(
  new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url),
  'utf8'
)
assert.match(maintenanceQueueSource, /forwardSupervisorOutput\(process\.stdout, chunk\)/, '临时 maintenance worker stdout 必须使用安全输出转发')
assert.match(maintenanceQueueSource, /forwardSupervisorOutput\(process\.stderr, chunk\)/, '临时 maintenance worker stderr 必须使用安全输出转发')

console.log('SUPERVISOR_RESTART_POLICY_REGRESSION_OK')
