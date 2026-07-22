import assert from 'node:assert/strict'
import {
  transportProbeMeetsFirstByteTarget,
  type TransportProbeOutcome
} from '../../modules/accounts/automatic-account-probe-outcome.js'

const framingComplete: TransportProbeOutcome = {
  kind: 'framing_complete',
  statusCode: 503
}

assert.equal(transportProbeMeetsFirstByteTarget({
  success: false,
  firstTokenMs: 800
}, framingComplete, 1_000), true, '速度恢复只依赖 framing 和首字，不得读取业务 success')

assert.equal(transportProbeMeetsFirstByteTarget({
  success: true,
  firstTokenMs: 800
}, {
  kind: 'transport_incomplete',
  failureKind: 'read',
  statusCode: 200
}, 1_000), false, '读取中断不得因业务 success 恢复速度状态')

assert.equal(transportProbeMeetsFirstByteTarget({
  success: true,
  firstTokenMs: 1_200
}, framingComplete, 1_000), false, '超过首字目标不得恢复速度状态')

console.log('NORMAL_ROUTE_SPEED_FIRST_RECOVERY_PROBE_OK')
