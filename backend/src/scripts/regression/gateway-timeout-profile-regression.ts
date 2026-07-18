import { strict as assert } from 'node:assert'

import {
  gatewayTimeoutProfileForLane,
  type GatewayTimeoutSettings
} from '../../modules/gateway/policy/timeout-profile.js'

const settings: GatewayTimeoutSettings = {
  textFirstResponseTimeoutSeconds: 120,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  noAvailableAccountWaitTimeoutSeconds: 270
}

assert.deepEqual(gatewayTimeoutProfileForLane(settings, 'text'), {
  firstResponseTimeoutMs: 120_000,
  firstByteTimeoutMs: 120_000,
  idleTimeoutMs: 30_000,
  uncommittedAttemptMaxLifetimeMs: 1_800_000,
  noAvailableAccountWaitMs: 270_000
})

assert.deepEqual(gatewayTimeoutProfileForLane(settings, 'image'), {
  firstResponseTimeoutMs: 600_000,
  firstByteTimeoutMs: 600_000,
  idleTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 3_600_000,
  noAvailableAccountWaitMs: 270_000
})

console.log('gateway timeout profile regression passed')
