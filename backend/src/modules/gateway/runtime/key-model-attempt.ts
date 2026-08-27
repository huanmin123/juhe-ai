import type { Request } from 'express'
import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { dispatchAccountHealthCheck } from '../../internal-api/account-health-check-dispatch.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { resolveGatewayKeyModelCapability, type GatewayKeyModelCapability } from './key-model-capability.js'
import {
  RedisKeyModelRuntimeStore,
  type KeyModelFenceReference,
  type KeyModelAdmissionResult,
  type KeyModelForegroundPermit
} from './key-model-redis-store.js'
import { capabilityHash, keyModelForegroundLeaseRenewMs, type KeyModelOutcome } from './key-model-runtime.js'

const keyModelFailureIntentLimit = 8
let runtimeStore: RedisKeyModelRuntimeStore | undefined

export class GatewayKeyModelFailureBudget {
  private readonly submitted = new Set<string>()

  claim(hash: string): boolean {
    if (this.submitted.has(hash)) return false
    if (this.submitted.size >= keyModelFailureIntentLimit) return false
    this.submitted.add(hash)
    return true
  }
}

export type GatewayKeyModelAttemptPreparation =
  | { status: 'disabled' }
  | { status: 'busy' | 'blocked'; wakeSequence: number; capabilityHash: string }
  | { status: 'admitted'; attempt: GatewayKeyModelAttempt }

export interface PrepareGatewayKeyModelAttemptInput {
  req: Request
  account: UpstreamAccount
  requestId: string
  attemptId: string
  failureBudget: GatewayKeyModelFailureBudget
}

export async function prepareGatewayKeyModelAttempt(
  input: PrepareGatewayKeyModelAttemptInput
): Promise<GatewayKeyModelAttemptPreparation> {
  // Standalone mode has no Redis state backend, so the Redis-backed guard is
  // unavailable there. Performance/dev profiles use Redis and enter directly.
  if (runtimeConfig.runtimeStateDriver !== 'redis') return { status: 'disabled' }
  const route = resolveGatewayKeyModelCapability(input.req, input.account)
  if (!route) return { status: 'disabled' }
  const store = getKeyModelRuntimeStore()
  const admission = await store.admitForeground(route.capability, input.attemptId)
  if (admission.status !== 'admitted') {
    return { status: admission.status, wakeSequence: admission.wakeSequence, capabilityHash: capabilityHash(route.capability) }
  }
  return {
    status: 'admitted',
    attempt: new GatewayKeyModelAttempt(store, route, admission, input.requestId, input.attemptId, input.failureBudget)
  }
}

export class GatewayKeyModelAttempt {
  private permit: KeyModelForegroundPermit
  private readonly renewalAbort = new AbortController()
  private renewalTimer: NodeJS.Timeout | undefined
  private released = false
  private permitLost = false
  private terminal: Promise<void> | undefined

  constructor(
    private readonly store: RedisKeyModelRuntimeStore,
    readonly route: GatewayKeyModelCapability,
    admission: Extract<KeyModelAdmissionResult, { status: 'admitted' }>,
    private readonly requestId: string,
    readonly attemptId: string,
    private readonly failureBudget: GatewayKeyModelFailureBudget
  ) {
    this.permit = admission.permit
    this.scheduleRenewal()
  }

  get capabilityHash(): string {
    return this.permit.capabilityHash
  }

  transportSignal(parent?: AbortSignal): AbortSignal {
    return parent ? AbortSignal.any([parent, this.renewalAbort.signal]) : this.renewalAbort.signal
  }

  markPrecommit(): void {
    this.stopRenewal()
    void this.release().catch((error) => this.logFailure('foreground_precommit_release_failed', error))
  }

  reportCompleteSuccess(): Promise<void> {
    return this.settle('complete_success')
  }

  reportUpstreamNotComplete(): Promise<void> {
    return this.settle('upstream_not_complete')
  }

  reportUnknown(): Promise<void> {
    return this.settle('unknown')
  }

  private settle(outcome: KeyModelOutcome): Promise<void> {
    this.terminal ??= this.settleOnce(this.permitLost ? 'unknown' : outcome)
    return this.terminal
  }

  private async settleOnce(outcome: KeyModelOutcome): Promise<void> {
    this.stopRenewal()
    if (outcome !== 'upstream_not_complete') {
      await this.releaseSafely(outcome)
      return
    }
    if (this.route.isMainProbe) {
      try {
        await this.store.recordMainProbeFailure(this.route.capability, this.permit)
        this.released = true
        dispatchAccountHealthCheck(
          this.route.accountId,
          'request_failure',
          undefined,
          {
            capabilityHash: this.capabilityHash,
            keyFingerprint: this.route.capability.keyFingerprint,
            dispatchRevision: this.route.capability.dispatchRevision,
            ownerId: this.attemptId
          } satisfies KeyModelFenceReference
        )
      } catch (error) {
        await this.releaseSafely('unknown')
        this.logFailure('main_probe_fence_write_failed', error)
      }
      return
    }
    if (!this.failureBudget.claim(this.capabilityHash)) {
      await this.releaseSafely('unknown')
      return
    }
    try {
      const result = await this.store.recordFailure({
        intentId: `${this.requestId}:${this.attemptId}`,
        requestId: this.requestId,
        attemptId: this.attemptId,
        capability: this.route.capability,
        observedAtMs: Date.now(),
        outcome: 'upstream_not_complete',
        sourceFence: sourceFence(this.route),
        permit: this.permit
      })
      if (result.status === 'applied' && await this.store.claimJ1Confirmation(
        this.route.capability.credentialSourceAccountId,
        this.route.capability.dispatchRevision
      )) {
        dispatchAccountHealthCheck(this.route.capability.credentialSourceAccountId, 'request_failure')
      }
      this.released = true
    } catch (error) {
      await this.releaseSafely('unknown')
      this.logFailure('key_model_failure_intent_write_failed', error)
    }
  }

  private scheduleRenewal(): void {
    this.renewalTimer = setTimeout(() => void this.renew(), keyModelForegroundLeaseRenewMs)
    this.renewalTimer.unref?.()
  }

  private async renew(): Promise<void> {
    if (this.released || this.terminal) return
    try {
      const renewed = await this.store.renewForeground(this.permit)
      if (!renewed) {
        this.losePermit()
        return
      }
      this.permit = renewed
      this.scheduleRenewal()
    } catch (error) {
      this.logFailure('foreground_permit_renew_failed', error)
      this.losePermit()
    }
  }

  private losePermit(): void {
    this.permitLost = true
    this.stopRenewal()
    this.renewalAbort.abort(new Error('Key-model foreground permit 已失租'))
  }

  private stopRenewal(): void {
    if (this.renewalTimer) clearTimeout(this.renewalTimer)
    this.renewalTimer = undefined
  }

  private async release(): Promise<void> {
    if (this.released) return
    await this.store.releaseForeground(this.permit)
    this.released = true
  }

  private async releaseSafely(outcome: KeyModelOutcome): Promise<void> {
    try {
      await this.release()
    } catch (error) {
      this.logFailure('foreground_permit_release_failed', error, outcome)
    }
  }

  private logFailure(event: string, error: unknown, outcome?: KeyModelOutcome): void {
    logger.warn(errorLogFields(error, {
      event,
      requestId: this.requestId,
      attemptId: this.attemptId,
      capabilityHash: this.capabilityHash,
      dispatchRevision: this.route.capability.dispatchRevision,
      outcome
    }), 'Key-model runtime guard 操作失败')
  }
}

function getKeyModelRuntimeStore(): RedisKeyModelRuntimeStore {
  runtimeStore ??= new RedisKeyModelRuntimeStore()
  return runtimeStore
}

function sourceFence(route: GatewayKeyModelCapability): string {
  return createHash('sha256').update(
    `${route.capability.credentialSourceAccountId}:${route.capability.dispatchRevision}`
  ).digest('hex')
}
