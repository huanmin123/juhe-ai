import assert from 'node:assert/strict'
import {
  transportProbeMeetsFirstByteTarget,
  type TransportProbeOutcome
} from '../../modules/accounts/automatic-account-probe-outcome.js'
import { normalRouteSpeedFirstRecoveryProbeRequiresWindowReset } from '../../modules/background/normal-route-speed-first-recovery-probe.service.js'

const framingComplete: TransportProbeOutcome = {
  kind: 'framing_complete',
  statusCode: 503
}

assert.equal(transportProbeMeetsFirstByteTarget({
  success: false,
  firstTokenMs: 800
}, framingComplete, 1_000), false, '完整但业务无效的快速响应不得恢复共享速度排名')

assert.equal(transportProbeMeetsFirstByteTarget({
  success: true,
  firstTokenMs: 800
}, framingComplete, 1_000), true, '协议成功且首字达标才可作为速度恢复证据')

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

assert.equal(
  normalRouteSpeedFirstRecoveryProbeRequiresWindowReset({ success: false }, {
    kind: 'transport_incomplete',
    failureKind: 'timeout'
  }),
  true,
  'transport_incomplete 必须作废当前两次窗口，不能作为 FF 的第二次失败'
)
assert.equal(
  normalRouteSpeedFirstRecoveryProbeRequiresWindowReset({ success: false }, framingComplete),
  true,
  '完整但业务失败的探针必须作废当前两次窗口'
)
assert.equal(
  normalRouteSpeedFirstRecoveryProbeRequiresWindowReset({ success: true }, framingComplete),
  false,
  '完整业务成功但首字慢应进入失败计数，而不是作废窗口'
)

console.log('NORMAL_ROUTE_SPEED_FIRST_RECOVERY_PROBE_OK')
