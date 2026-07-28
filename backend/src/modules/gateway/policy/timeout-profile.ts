import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'

export interface GatewayTimeoutSettings {
  textFirstResponseTimeoutSeconds: number
  textStreamIdleTimeoutSeconds: number
  textUncommittedAttemptMaxLifetimeSeconds: number
  imageFirstResponseTimeoutSeconds: number
  imageStreamIdleTimeoutSeconds: number
  imageUncommittedAttemptMaxLifetimeSeconds: number
  noAvailableAccountWaitTimeoutSeconds: number
}

export interface GatewayTimeoutProfile {
  timeoutsDisabled?: true
  firstResponseTimeoutMs: number
  firstByteTimeoutMs: number
  idleTimeoutMs: number
  uncommittedAttemptMaxLifetimeMs: number
  noAvailableAccountWaitMs: number
}

export function gatewayTimeoutProfileForLane(
  settings: GatewayTimeoutSettings,
  lane: OpenAIGatewayRequestLane,
  options: { disableTimeouts?: boolean } = {}
): GatewayTimeoutProfile {
  const firstResponseTimeoutSeconds = lane === 'image'
    ? settings.imageFirstResponseTimeoutSeconds
    : settings.textFirstResponseTimeoutSeconds
  const idleTimeoutSeconds = lane === 'image'
    ? settings.imageStreamIdleTimeoutSeconds
    : settings.textStreamIdleTimeoutSeconds
  const uncommittedAttemptMaxLifetimeSeconds = lane === 'image'
    ? settings.imageUncommittedAttemptMaxLifetimeSeconds
    : settings.textUncommittedAttemptMaxLifetimeSeconds

  return {
    ...(options.disableTimeouts === true ? { timeoutsDisabled: true as const } : {}),
    firstResponseTimeoutMs: secondsToMilliseconds(firstResponseTimeoutSeconds),
    firstByteTimeoutMs: secondsToMilliseconds(firstResponseTimeoutSeconds),
    idleTimeoutMs: secondsToMilliseconds(idleTimeoutSeconds),
    uncommittedAttemptMaxLifetimeMs: secondsToMilliseconds(uncommittedAttemptMaxLifetimeSeconds),
    noAvailableAccountWaitMs: secondsToMilliseconds(settings.noAvailableAccountWaitTimeoutSeconds)
  }
}

function secondsToMilliseconds(seconds: number): number {
  return Math.max(1, seconds) * 1000
}
