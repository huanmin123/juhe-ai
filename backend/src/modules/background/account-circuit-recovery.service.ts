import { randomUUID } from 'node:crypto'

import { errorLogFields, logger } from '../../shared/logger.js'
import type { TransportProbeOutcome } from '../accounts/automatic-account-probe-outcome.js'
import { getGatewayAccountCircuitStore } from '../gateway/runtime/account-circuit.service.js'
import type {
  AccountCircuitMutationResult,
  AccountCircuitState,
  AccountCircuitStore
} from '../gateway/runtime/account-circuit-store.js'
import type { WorkerScheduledJobTaskResult } from './worker-scheduler.js'

export interface AccountCircuitRecoveryProbeTarget {
  dispatchRevision: string
  probe(signal: AbortSignal): Promise<TransportProbeOutcome>
}

export type AccountCircuitRecoveryTargetResolver = (
  state: AccountCircuitState,
  signal: AbortSignal
) => Promise<AccountCircuitRecoveryProbeTarget | undefined>

export interface AccountCircuitRecoveryServiceOptions {
  batchSize?: number
  leaseDurationMs?: number
  now?: () => number
  createId?: () => string
}

export interface AccountCircuitRecoverySweepResult {
  dueCount: number
  leasedCount: number
  framingCompleteCount: number
  transportIncompleteCount: number
  unknownCount: number
  fencedCount: number
  skippedCount: number
}

type AccountCircuitRecoveryItemOutcome =
  | 'framing_complete'
  | 'transport_incomplete'
  | 'unknown'
  | 'fenced'
  | 'skipped'

const defaultRecoveryBatchSize = 10
const defaultRecoveryLeaseDurationMs = 180_000

export class AccountCircuitRecoveryService {
  private readonly batchSize: number
  private readonly leaseDurationMs: number
  private readonly now: () => number
  private readonly createId: () => string

  constructor(
    private readonly store: AccountCircuitStore,
    private readonly resolveTarget: AccountCircuitRecoveryTargetResolver,
    options: AccountCircuitRecoveryServiceOptions = {}
  ) {
    this.batchSize = positiveInteger(options.batchSize ?? defaultRecoveryBatchSize, 'batchSize')
    this.leaseDurationMs = positiveInteger(options.leaseDurationMs ?? defaultRecoveryLeaseDurationMs, 'leaseDurationMs')
    this.now = options.now ?? Date.now
    this.createId = options.createId ?? randomUUID
  }

  async sweep(): Promise<AccountCircuitRecoverySweepResult> {
    const due = await this.store.listDue(this.now(), this.batchSize)
    const result: AccountCircuitRecoverySweepResult = {
      dueCount: due.length,
      leasedCount: 0,
      framingCompleteCount: 0,
      transportIncompleteCount: 0,
      unknownCount: 0,
      fencedCount: 0,
      skippedCount: 0
    }
    const errors: unknown[] = []
    for (const state of due) {
      try {
        const outcome = await this.recover(state, result)
        incrementSweepOutcome(result, outcome)
      } catch (error) {
        errors.push(error)
        logger.error(errorLogFields(error, {
          event: 'gateway_account_circuit_recovery_item_failed',
          scopeKey: state.scopeKey,
          generation: state.generation,
          dispatchRevision: state.dispatchRevision,
          phase: state.phase
        }), '账户电路后台恢复探针执行失败')
      }
    }
    if (errors.length > 0) {
      throw new AggregateError(errors, `账户电路后台恢复失败：${errors.length} 个作用域未完成`)
    }
    return result
  }

  private async recover(
    dueState: AccountCircuitState,
    sweep: AccountCircuitRecoverySweepResult
  ): Promise<AccountCircuitRecoveryItemOutcome> {
    if (dueState.phase !== 'OPEN' && dueState.phase !== 'RECOVERING') return 'skipped'
    const nowMs = this.now()
    const leaseId = this.createId()
    const acquired = await this.store.acquireCanaryLease({
      scope: dueState.scope,
      generation: dueState.generation,
      dispatchRevision: dueState.dispatchRevision,
      transitionId: this.createId(),
      leaseId,
      leaseUntilMs: nowMs + this.leaseDurationMs,
      nowMs
    })
    if (acquired.status !== 'applied') {
      return isFencingResult(acquired) ? 'fenced' : 'skipped'
    }
    sweep.leasedCount += 1

    const controller = new AbortController()
    let target: AccountCircuitRecoveryProbeTarget | undefined
    try {
      target = await this.resolveTarget(dueState, controller.signal)
    } catch (error) {
      await this.releaseUnknown(acquired.state, leaseId, controller)
      throw error
    }
    if (!target) {
      await this.releaseUnknown(acquired.state, leaseId, controller)
      logger.warn({
        event: 'gateway_account_circuit_recovery_target_missing',
        scopeKey: dueState.scopeKey,
        generation: dueState.generation,
        dispatchRevision: dueState.dispatchRevision
      }, '账户电路后台恢复目标无法解析，已保守释放半开租约')
      return 'unknown'
    }
    if (target.dispatchRevision !== dueState.dispatchRevision) {
      controller.abort('dispatch_revision_changed')
      const replaced = await this.store.replaceDispatchRevision({
        scope: dueState.scope,
        dispatchRevision: target.dispatchRevision,
        transitionId: this.createId(),
        nowMs: this.now()
      })
      return isAppliedOrIdempotent(replaced) ? 'fenced' : 'skipped'
    }

    let outcome: TransportProbeOutcome
    try {
      outcome = await runProbeWithinLease(target, controller, this.leaseDurationMs)
    } catch (error) {
      await this.releaseUnknown(acquired.state, leaseId, controller)
      throw error
    }
    const completed = await this.store.completeCanary({
      scope: dueState.scope,
      generation: dueState.generation,
      dispatchRevision: dueState.dispatchRevision,
      transitionId: this.createId(),
      leaseId,
      outcome: circuitOutcome(outcome),
      reason: circuitFailureReason(outcome),
      nowMs: this.now()
    })
    if (!isAppliedOrIdempotent(completed)) {
      return isFencingResult(completed) ? 'fenced' : 'skipped'
    }
    if (outcome.kind === 'framing_complete') return 'framing_complete'
    if (outcome.kind === 'transport_incomplete') return 'transport_incomplete'
    return 'unknown'
  }

  private async releaseUnknown(
    leasedState: AccountCircuitState,
    leaseId: string,
    controller: AbortController
  ): Promise<void> {
    controller.abort('account_circuit_probe_unknown')
    const released = await this.store.completeCanary({
      scope: leasedState.scope,
      generation: leasedState.generation,
      dispatchRevision: leasedState.dispatchRevision,
      transitionId: this.createId(),
      leaseId,
      outcome: 'unknown',
      nowMs: this.now()
    })
    if (!isAppliedOrIdempotent(released) && !isFencingResult(released)) {
      throw new Error(`账户电路未知探针结果释放失败：${released.status}`)
    }
  }
}

let scheduledRecoveryResolver: AccountCircuitRecoveryTargetResolver | undefined
let missingResolverWarningLogged = false

export function installScheduledAccountCircuitRecoveryResolver(
  resolver: AccountCircuitRecoveryTargetResolver | undefined
): void {
  scheduledRecoveryResolver = resolver
  missingResolverWarningLogged = false
}

export async function runScheduledAccountCircuitRecovery(): Promise<WorkerScheduledJobTaskResult | void> {
  if (!scheduledRecoveryResolver) {
    if (!missingResolverWarningLogged) {
      missingResolverWarningLogged = true
      logger.warn({
        event: 'gateway_account_circuit_recovery_resolver_missing'
      }, '账户电路后台恢复 resolver 尚未安装，本轮未执行探针')
    }
    return { outcome: 'partial', warning: '账户电路后台恢复 resolver 尚未安装' }
  }
  const result = await new AccountCircuitRecoveryService(
    getGatewayAccountCircuitStore(),
    scheduledRecoveryResolver
  ).sweep()
  if (result.dueCount > 0) {
    logger.info({
      event: 'gateway_account_circuit_recovery_sweep_completed',
      ...result
    }, '账户电路后台恢复扫描完成')
  }
}

async function runProbeWithinLease(
  target: AccountCircuitRecoveryProbeTarget,
  controller: AbortController,
  leaseDurationMs: number
): Promise<TransportProbeOutcome> {
  let timer: NodeJS.Timeout | undefined
  const deadline = new Promise<TransportProbeOutcome>((resolve) => {
    timer = setTimeout(() => {
      controller.abort('account_circuit_probe_lease_deadline')
      resolve({ kind: 'unknown', failureKind: 'canceled' })
    }, leaseDurationMs)
    timer.unref()
  })
  try {
    return await Promise.race([target.probe(controller.signal), deadline])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

function circuitOutcome(outcome: TransportProbeOutcome): 'framing_complete' | 'transport_failure' | 'unknown' {
  if (outcome.kind === 'framing_complete') return 'framing_complete'
  if (outcome.kind === 'transport_incomplete') return 'transport_failure'
  return 'unknown'
}

function circuitFailureReason(outcome: TransportProbeOutcome): string | undefined {
  if (outcome.kind !== 'transport_incomplete') return undefined
  const status = outcome.statusCode === undefined ? '' : `:http_${outcome.statusCode}`
  return `background_probe:${outcome.failureKind}${status}`
}

function isAppliedOrIdempotent(result: AccountCircuitMutationResult): boolean {
  return result.status === 'applied' || result.status === 'idempotent'
}

function isFencingResult(result: AccountCircuitMutationResult): boolean {
  return result.status === 'stale_generation'
    || result.status === 'stale_dispatch_revision'
    || result.status === 'lease_mismatch'
}

function incrementSweepOutcome(
  result: AccountCircuitRecoverySweepResult,
  outcome: AccountCircuitRecoveryItemOutcome
): void {
  if (outcome === 'framing_complete') result.framingCompleteCount += 1
  else if (outcome === 'transport_incomplete') result.transportIncompleteCount += 1
  else if (outcome === 'unknown') result.unknownCount += 1
  else if (outcome === 'fenced') result.fencedCount += 1
  else result.skippedCount += 1
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isFinite(value) || value < 1) throw new Error(`账户电路恢复 ${name} 必须是正整数`)
  return Math.trunc(value)
}
