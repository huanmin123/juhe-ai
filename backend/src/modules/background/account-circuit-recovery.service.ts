import { createHash, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { AccessScope } from '../../storage/access-scope.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'
import {
  transportProbeOutcomeFromAccountTestResult,
  type TransportProbeOutcome
} from '../accounts/automatic-account-probe-outcome.js'
import { observeGatewayRouting } from '../gateway/observability/routing-observability.service.js'
import type { GatewayRoutingCircuitOperation } from '../gateway/observability/routing-observability-store.js'
import { gatewayAccountRuntimeKey, runtimeAccountIdFromKey } from '../gateway/runtime/account-runtime-keys.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  ensureGatewayAccountCircuitRuntimeStateReady,
  getGatewayAccountCircuitStore,
  projectGatewayAccountCircuitRuntimeMutation
} from '../gateway/runtime/account-circuit.service.js'
import type {
  AccountCircuitMutationResult,
  AccountCircuitState,
  AccountCircuitStore
} from '../gateway/runtime/account-circuit-store.js'
import type { WorkerScheduledJobTaskResult } from './worker-scheduler.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs } from './account-probe-limits.js'

export interface AccountCircuitRecoveryProbeTarget {
  dispatchRevision: string
  probe(signal: AbortSignal): Promise<TransportProbeOutcome>
}

export type AccountCircuitRecoveryTargetResolver = (
  state: AccountCircuitState,
  signal: AbortSignal
) => Promise<AccountCircuitRecoveryProbeTarget | undefined>

export interface AccountCircuitRecoveryResolverDependencies {
  findAccountForTest(
    accountId: string,
    access?: AccessScope
  ): Promise<AccountSummary | undefined>
  findOpenAIAccountForGroup(
    groupId: string,
    accountId: string,
    systemAccountId: string
  ): Promise<OpenAIAccountSecret | undefined>
  probe(input: {
    account: AccountSummary
    candidateAccount: OpenAIAccountSecret
    groupId: string
    systemAccountId: string
    model?: string
    signal: AbortSignal
  }): Promise<TransportProbeOutcome>
}

export interface AccountCircuitRecoveryServiceOptions {
  batchSize?: number
  concurrency?: number
  leaseDurationMs?: number
  now?: () => number
  createId?: () => string
  onMutation?: (input: {
    scope: AccountCircuitState['scope']
    state: AccountCircuitState
    status: AccountCircuitMutationResult['status']
  }) => Promise<void> | void
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

const defaultRecoveryBatchSize = 200
const defaultRecoveryConcurrency = 1
const defaultRecoveryLeaseDurationMs = 180_000

export class AccountCircuitRecoveryService {
  private readonly batchSize: number
  private readonly leaseDurationMs: number
  private readonly concurrency: number
  private readonly now: () => number
  private readonly createId: () => string
  private readonly onMutation?: AccountCircuitRecoveryServiceOptions['onMutation']

  constructor(
    private readonly store: AccountCircuitStore,
    private readonly resolveTarget: AccountCircuitRecoveryTargetResolver,
    options: AccountCircuitRecoveryServiceOptions = {}
  ) {
    this.batchSize = positiveInteger(options.batchSize ?? defaultRecoveryBatchSize, 'batchSize')
    this.concurrency = positiveInteger(options.concurrency ?? defaultRecoveryConcurrency, 'concurrency')
    this.leaseDurationMs = positiveInteger(options.leaseDurationMs ?? defaultRecoveryLeaseDurationMs, 'leaseDurationMs')
    this.now = options.now ?? Date.now
    this.createId = options.createId ?? randomUUID
    this.onMutation = options.onMutation
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
    await forEachConcurrent(due, this.concurrency, async (state) => {
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
    })
    if (errors.length > 0) {
      throw new AggregateError(errors, `账户电路后台恢复失败：${errors.length} 个作用域未完成`)
    }
    return result
  }

  private async recover(
    dueState: AccountCircuitState,
    sweep: AccountCircuitRecoverySweepResult
  ): Promise<AccountCircuitRecoveryItemOutcome> {
    if (dueState.phase !== 'SUSPECT' && dueState.phase !== 'OPEN' && dueState.phase !== 'RECOVERING') return 'skipped'
    const nowMs = this.now()
    const leaseId = this.createId()
    const acquired = dueState.phase === 'SUSPECT'
      ? await this.store.acquireConfirmationLease({
          scope: dueState.scope,
          generation: dueState.generation,
          dispatchRevision: dueState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          leaseUntilMs: nowMs + this.leaseDurationMs,
          nowMs
        })
      : await this.store.acquireCanaryLease({
          scope: dueState.scope,
          generation: dueState.generation,
          dispatchRevision: dueState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          leaseUntilMs: nowMs + this.leaseDurationMs,
          nowMs
        })
    await this.observeMutation(
      dueState.phase === 'SUSPECT' ? 'acquire_confirmation' : 'acquire_canary',
      acquired,
      dueState.phase
    )
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
      await this.observeMutation('replace_revision', replaced, acquired.state.phase)
      return isAppliedOrIdempotent(replaced) ? 'fenced' : 'skipped'
    }

    let outcome: TransportProbeOutcome
    try {
      outcome = await runProbeWithinLease(target, controller, this.leaseDurationMs)
    } catch (error) {
      await this.releaseUnknown(acquired.state, leaseId, controller)
      throw error
    }
    const completed = dueState.phase === 'SUSPECT'
      ? await this.store.completeConfirmation({
          scope: dueState.scope,
          generation: dueState.generation,
          dispatchRevision: dueState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          outcome: circuitOutcome(outcome),
          reason: circuitFailureReason(outcome),
          ...(outcome.kind === 'transport_incomplete'
            ? { failureEvidenceKey: backgroundConfirmationEvidenceKey(dueState, leaseId) }
            : {}),
          framingCompleteDisposition: 'closed',
          nowMs: this.now()
        })
      : await this.store.completeCanary({
          scope: dueState.scope,
          generation: dueState.generation,
          dispatchRevision: dueState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          outcome: circuitOutcome(outcome),
          reason: circuitFailureReason(outcome),
          nowMs: this.now()
        })
    await this.observeMutation(
      dueState.phase === 'SUSPECT' ? 'complete_confirmation' : 'complete_canary',
      completed,
      acquired.state.phase
    )
    if (!isAppliedOrIdempotent(completed)) {
      return isFencingResult(completed) ? 'fenced' : 'skipped'
    }
    if (outcome.kind === 'framing_complete' && dueState.scope.kind === 'protocol_model') {
      await this.store.clearAccountEscalationEvidence({
        accountRuntimeKey: dueState.scope.accountRuntimeKey,
        dispatchRevision: dueState.dispatchRevision,
        evidenceId: this.createId(),
        nowMs: this.now()
      })
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
    const released = leasedState.phase === 'SUSPECT'
      ? await this.store.completeConfirmation({
          scope: leasedState.scope,
          generation: leasedState.generation,
          dispatchRevision: leasedState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          outcome: 'unknown',
          nowMs: this.now()
        })
      : await this.store.completeCanary({
          scope: leasedState.scope,
          generation: leasedState.generation,
          dispatchRevision: leasedState.dispatchRevision,
          transitionId: this.createId(),
          leaseId,
          outcome: 'unknown',
          nowMs: this.now()
        })
    await this.observeMutation(
      leasedState.phase === 'SUSPECT' ? 'complete_confirmation' : 'complete_canary',
      released,
      leasedState.phase
    )
    if (!isAppliedOrIdempotent(released) && !isFencingResult(released)) {
      throw new Error(`账户电路未知探针结果释放失败：${released.status}`)
    }
  }

  private async observeMutation(
    operation: GatewayRoutingCircuitOperation,
    result: AccountCircuitMutationResult,
    previousPhase: AccountCircuitState['phase']
  ): Promise<void> {
    observeRecoveryCircuitMutation(operation, result, previousPhase)
    if (result.status !== 'not_found') {
      await this.onMutation?.({
        scope: result.state.scope,
        state: result.state,
        status: result.status
      })
    }
  }
}

export function createScheduledAccountCircuitRecoveryResolver(
  dependencies: AccountCircuitRecoveryResolverDependencies = defaultAccountCircuitRecoveryResolverDependencies()
): AccountCircuitRecoveryTargetResolver {
  return async (state, signal) => {
    if (signal.aborted) return undefined
    const identity = parseRecoveryRuntimeIdentity(state.scope.accountRuntimeKey)
    if (!identity) return undefined
    const access: AccessScope | undefined = identity.kind === 'authorized'
      ? { systemAccountId: identity.systemAccountId, role: 'user' }
      : undefined
    const account = await dependencies.findAccountForTest(identity.accountId, access)
    if (!account || signal.aborted) return undefined
    const groupId = identity.kind === 'authorized' ? identity.groupId : account.boundGroupId
    const systemAccountId = identity.kind === 'authorized' ? identity.systemAccountId : account.systemAccountId
    if (!groupId || !systemAccountId) return undefined
    const candidateAccount = await dependencies.findOpenAIAccountForGroup(groupId, identity.accountId, systemAccountId)
    if (!candidateAccount || signal.aborted) return undefined
    if (gatewayAccountRuntimeKey(candidateAccount) !== state.scope.accountRuntimeKey) return undefined
    const dispatchRevision = currentDispatchRevision(candidateAccount)
    if (!dispatchRevision) return undefined
    return {
      dispatchRevision,
      probe: async (probeSignal) => {
        if (probeSignal.aborted) return { kind: 'unknown', failureKind: 'canceled' }
        return await dependencies.probe({
          account,
          candidateAccount,
          groupId,
          systemAccountId,
          model: state.scope.kind === 'protocol_model' && state.scope.modelBucket !== 'unknown'
            ? state.scope.modelBucket
            : undefined,
          signal: probeSignal
        })
      }
    }
  }
}

export function installDefaultScheduledAccountCircuitRecoveryResolver(): void {
  installScheduledAccountCircuitRecoveryResolver(createScheduledAccountCircuitRecoveryResolver())
}

function defaultAccountCircuitRecoveryResolverDependencies(): AccountCircuitRecoveryResolverDependencies {
  return {
    findAccountForTest: async (accountId, access) => await requestBackgroundWorkerDbService({
      type: 'find_account_for_test',
      accountId,
      access
    }, backgroundProbeDbServiceTimeoutMs),
    findOpenAIAccountForGroup: async (groupId, accountId, systemAccountId) => await requestBackgroundWorkerDbService({
      type: 'find_openai_account_for_group',
      groupId,
      accountId,
      systemAccountId,
      includeUnavailable: true,
      ignoreAvailability: true
    }, backgroundProbeDbServiceTimeoutMs),
    probe: runAccountCircuitRecoveryTransportProbe
  }
}

async function runAccountCircuitRecoveryTransportProbe(input: {
  account: AccountSummary
  candidateAccount: OpenAIAccountSecret
  groupId: string
  systemAccountId: string
  model?: string
  signal: AbortSignal
}): Promise<TransportProbeOutcome> {
  let upstreamAttempt: UpstreamAttempt | undefined
  try {
    const result = await testOpenAIAccount(input.account, {
      diagnostics: 'limited',
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      model: input.model,
      signal: input.signal,
      trafficSource: 'runtime_recovery_probe',
      testEndpointMode: input.account.healthCheckEndpointMode,
      candidateAccount: input.candidateAccount,
      disableAccountStateMutation: true,
      onUpstreamAttempt: (attempt) => {
        upstreamAttempt = attempt
      }
    })
    return transportProbeOutcomeFromAccountTestResult(result, {
      upstreamAttempt,
      canceled: input.signal.aborted
    })
  } catch (error) {
    if (input.signal.aborted) return { kind: 'unknown', failureKind: 'canceled' }
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_circuit_recovery_probe_task_failed',
      accountId: input.account.id,
      groupId: input.groupId
    }), '账户电路后台恢复探针任务未形成可判定传输结果')
    return { kind: 'unknown', failureKind: 'task_failure' }
  }
}

type RecoveryRuntimeIdentity =
  | { kind: 'owner'; accountId: string }
  | {
      kind: 'authorized'
      accountId: string
      systemAccountId: string
      groupId: string
      authorizationId: string
    }

function parseRecoveryRuntimeIdentity(runtimeKey: string): RecoveryRuntimeIdentity | undefined {
  const marker = ':authorized:'
  const markerIndex = runtimeKey.indexOf(marker)
  if (markerIndex < 0) {
    const accountId = runtimeAccountIdFromKey(runtimeKey).trim()
    return accountId ? { kind: 'owner', accountId } : undefined
  }
  const accountId = runtimeKey.slice(0, markerIndex).trim()
  const parts = runtimeKey.slice(markerIndex + marker.length).split(':')
  if (!accountId || parts.length !== 3 || parts.some((part) => !part.trim())) return undefined
  return {
    kind: 'authorized',
    accountId,
    systemAccountId: parts[0]!.trim(),
    groupId: parts[1]!.trim(),
    authorizationId: parts[2]!.trim()
  }
}

function currentDispatchRevision(account: OpenAIAccountSecret): string | undefined {
  return Number.isSafeInteger(account.dispatchRevision) && (account.dispatchRevision ?? 0) > 0
    ? String(account.dispatchRevision)
    : undefined
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
  // A partial/capacity-limited rebuild may already contain OPEN incidents.
  // Continue sweeping those loaded entries so recovery can release capacity;
  // otherwise the readiness gate and the full store can deadlock each other.
  const runtimeStateReady = await ensureGatewayAccountCircuitRuntimeStateReady()
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
    scheduledRecoveryResolver,
    {
      batchSize: runtimeConfig.gateway.accountCircuitRecoveryBatchSize,
      concurrency: runtimeConfig.gateway.accountCircuitRecoveryConcurrency,
      onMutation: projectGatewayAccountCircuitRuntimeMutation
    }
  ).sweep()
  if (result.dueCount > 0) {
    logger.info({
      event: 'gateway_account_circuit_recovery_sweep_completed',
      ...result
    }, '账户电路后台恢复扫描完成')
  }
  if (!runtimeStateReady) {
    return {
      outcome: 'partial',
      warning: '账户电路运行态仅部分重建，已对当前加载的到期事故执行恢复探针'
    }
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

async function forEachConcurrent<T>(
  values: T[],
  concurrency: number,
  task: (value: T) => Promise<void>
): Promise<void> {
  let nextIndex = 0
  const workers = Array.from({ length: Math.min(concurrency, values.length) }, async () => {
    for (;;) {
      const index = nextIndex++
      if (index >= values.length) return
      await task(values[index]!)
    }
  })
  await Promise.all(workers)
}

function observeRecoveryCircuitMutation(
  operation: GatewayRoutingCircuitOperation,
  result: AccountCircuitMutationResult,
  previousPhase: AccountCircuitState['phase']
): void {
  observeGatewayRouting({
    kind: 'circuit_mutation',
    operation,
    status: result.status,
    ...(result.state.lease?.kind ? { leaseKind: result.state.lease.kind } : {})
  })
  if (result.status === 'applied' && previousPhase !== result.state.phase) {
    observeGatewayRouting({
      kind: 'circuit_transition',
      from: previousPhase,
      to: result.state.phase,
      source: operation === 'replace_revision' ? 'configuration' : 'recovery'
    })
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

function backgroundConfirmationEvidenceKey(state: AccountCircuitState, leaseId: string): string {
  return createHash('sha256')
    .update(`background_confirmation:${state.scopeKey}:${state.generation}:${leaseId}`)
    .digest('hex')
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
