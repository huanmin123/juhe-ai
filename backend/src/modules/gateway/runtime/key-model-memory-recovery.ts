import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import type { AccountSupportedEndpointMode } from '../../../domain/types.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../../shared/concurrency-governor.js'
import { accountApiKeyEntries } from '../../../storage/account-api-key-rotation.js'
import {
  findAccountForTestAsync,
  findOpenAIAccountForGroupAsync,
  type OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { testOpenAIAccount } from '../../accounts/account-test.service.js'
import type { UpstreamAttempt } from '../upstream/attempt.js'
import { transportProbeOutcomeFromAccountTestResult, type TransportProbeOutcome } from '../../accounts/automatic-account-probe-outcome.js'
import {
  getInMemoryKeyModelRuntimeStore,
  type InMemoryKeyModelRecoveryStore,
  type KeyModelRecoveryTarget
} from './key-model-redis-store.js'
import {
  keyModelProbeLeaseMs,
  keyModelProbeLeaseRenewMs,
  keyModelProbeTimeoutMs,
  type KeyModelOutcome,
  type KeyModelState
} from './key-model-runtime.js'
import type { WorkerScheduledJobTaskResult } from '../../background/worker-scheduler.js'

export const keyModelRecoveryScanIntervalMs = 1_000
const keyModelRecoveryBatchSize = 32
const keyModelRecoveryConcurrency = 32
const keyModelRecoveryContinuationSlots = 8
const keyModelRecoveryContinuationSourceLimit = 2
const keyModelRecoveryOpenSourceLimit = 2

export interface KeyModelRecoveryProbeInput {
  state: KeyModelState
  target: KeyModelRecoveryTarget
  signal: AbortSignal
}

export type KeyModelRecoveryProbe = (input: KeyModelRecoveryProbeInput) => Promise<KeyModelOutcome>

export interface KeyModelMemoryRecoveryRunnerOptions {
  store?: InMemoryKeyModelRecoveryStore
  probe?: KeyModelRecoveryProbe
  now?: () => number
  createId?: () => string
  concurrency?: number
}

/** Process-local counterpart of the Go Redis model-recovery runner. */
export class KeyModelMemoryRecoveryRunner {
  private readonly store: InMemoryKeyModelRecoveryStore
  private readonly probe: KeyModelRecoveryProbe
  private readonly now: () => number
  private readonly createId: () => string
  private readonly concurrency: number
  private readonly running = new Set<string>()
  private readonly runningSources = new Map<string, number>()

  constructor(options: KeyModelMemoryRecoveryRunnerOptions = {}) {
    this.store = options.store ?? getInMemoryKeyModelRuntimeStore()
    this.probe = options.probe ?? defaultKeyModelRecoveryProbe
    this.now = options.now ?? Date.now
    this.createId = options.createId ?? randomUUID
    this.concurrency = Math.max(1, Math.min(keyModelRecoveryConcurrency, Math.trunc(options.concurrency ?? keyModelRecoveryConcurrency)))
  }

  async sweep(signal?: AbortSignal): Promise<{ dueCount: number; startedCount: number; settledCount: number }> {
    const nowMs = this.now()
    const due = await this.store.listDue(nowMs, keyModelRecoveryBatchSize)
    const continuationWaiting = due.some((state) => state.phase === 'RECOVERING')
    const selected: KeyModelState[] = []
    const selectedOpenSources = new Map<string, number>()
    let continuationCount = 0
    let openCount = 0
    for (const state of due) {
      if (signal?.aborted) break
      if (this.running.has(state.capabilityHash)) continue
      const source = state.credentialSourceAccountId
      const runningSourceCount = this.runningSources.get(source) ?? 0
      const sourceLimit = state.phase === 'RECOVERING' ? keyModelRecoveryContinuationSourceLimit : keyModelRecoveryOpenSourceLimit
      if (runningSourceCount >= sourceLimit) continue
      if (state.phase === 'RECOVERING') {
        if (continuationCount >= keyModelRecoveryContinuationSlots) continue
        continuationCount += 1
      } else {
        const selectedSourceCount = selectedOpenSources.get(source) ?? 0
        if (selectedSourceCount + runningSourceCount >= sourceLimit) continue
        if (continuationWaiting && openCount >= keyModelRecoveryConcurrency - keyModelRecoveryContinuationSlots) continue
        selectedOpenSources.set(source, selectedSourceCount + 1)
        openCount += 1
      }
      if (selected.length >= this.concurrency) break
      selected.push(state)
    }
    let startedCount = 0
    let settledCount = 0
    await Promise.all(selected.map(async (state) => {
      this.running.add(state.capabilityHash)
      this.runningSources.set(state.credentialSourceAccountId, (this.runningSources.get(state.credentialSourceAccountId) ?? 0) + 1)
      try {
        const target = this.store.getRecoveryTarget(state)
        if (!target) return
        const leaseId = this.createId()
        const acquired = await this.store.acquireRecoveryLease({
          capability: state,
          generation: state.generation,
          dispatchRevision: state.dispatchRevision,
          leaseId,
          nowMs: this.now()
        })
        if (acquired.status !== 'applied') return
        startedCount += 1
        const outcome = await this.runProbeWithLease(acquired.state, target, leaseId, signal)
        const settled = await this.store.settleRecovery({
          capability: acquired.state,
          generation: acquired.state.generation,
          dispatchRevision: acquired.state.dispatchRevision,
          leaseId,
          outcome,
          nowMs: this.now()
        })
        if (settled.status === 'applied') settledCount += 1
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'gateway_key_model_memory_recovery_failed',
          capabilityHash: state.capabilityHash,
          generation: state.generation
        }), '单机 Key-model 恢复探针执行失败，按 unknown 处理')
      } finally {
        this.running.delete(state.capabilityHash)
        const remaining = (this.runningSources.get(state.credentialSourceAccountId) ?? 1) - 1
        if (remaining > 0) this.runningSources.set(state.credentialSourceAccountId, remaining)
        else this.runningSources.delete(state.credentialSourceAccountId)
      }
    }))
    return { dueCount: due.length, startedCount, settledCount }
  }

  private async runProbeWithLease(state: KeyModelState, target: KeyModelRecoveryTarget, leaseId: string, parentSignal?: AbortSignal): Promise<KeyModelOutcome> {
    const controller = new AbortController()
    const signal = parentSignal ? AbortSignal.any([parentSignal, controller.signal]) : controller.signal
    let timeout: NodeJS.Timeout | undefined
    let renew: NodeJS.Timeout | undefined
    const renewLease = async (): Promise<void> => {
      if (signal.aborted) return
      const ok = await this.store.renewRecoveryLease({
        capabilityHash: state.capabilityHash,
        generation: state.generation,
        dispatchRevision: state.dispatchRevision,
        leaseId,
        nowMs: this.now()
      })
      if (!ok) controller.abort('key_model_recovery_lease_lost')
    }
    try {
      const deadline = new Promise<KeyModelOutcome>((resolve) => {
        timeout = setTimeout(() => {
          controller.abort('key_model_recovery_probe_timeout')
          resolve('unknown')
        }, keyModelProbeTimeoutMs)
        timeout.unref?.()
      })
      const task = this.probe({ state, target, signal }).catch(() => 'unknown' as const)
      renew = setInterval(() => void renewLease(), keyModelProbeLeaseRenewMs)
      renew.unref?.()
      return await Promise.race([task, deadline])
    } finally {
      if (timeout) clearTimeout(timeout)
      if (renew) clearInterval(renew)
      controller.abort('key_model_recovery_probe_finished')
    }
  }
}

const defaultRunner = new KeyModelMemoryRecoveryRunner()

export async function runScheduledKeyModelMemoryRecovery(signal?: AbortSignal): Promise<WorkerScheduledJobTaskResult | void> {
  if (runtimeConfig.runtimeStateDriver === 'redis') return
  const result = await runWithGlobalBackgroundConcurrencySlot(() => defaultRunner.sweep(signal))
  if (result.dueCount > 0) {
    logger.info({ event: 'gateway_key_model_memory_recovery_sweep_completed', ...result }, '单机 Key-model 恢复扫描完成')
  }
  return { outcome: 'success' }
}

async function defaultKeyModelRecoveryProbe(input: KeyModelRecoveryProbeInput): Promise<KeyModelOutcome> {
  const account = await findAccountForTestAsync(input.target.accountId, { systemAccountId: input.target.systemAccountId, role: 'user' })
  if (!account || input.signal.aborted) return 'unknown'
  const candidate = await findOpenAIAccountForGroupAsync(input.target.groupId, input.target.accountId, input.target.systemAccountId, { includeUnavailable: true, ignoreAvailability: true })
  if (!candidate || input.signal.aborted) return 'unknown'
  const selected = selectExactApiKey(candidate, input.state.keyFingerprint)
  if (!selected) return 'unknown'
  let upstreamAttempt: UpstreamAttempt | undefined
  const result = await testOpenAIAccount(account, {
    model: input.state.clientModel,
    groupId: input.target.groupId,
    systemAccountId: input.target.systemAccountId,
    testEndpointMode: endpointMode(input.state.clientEndpointFamily, input.state.upstreamEndpointMode),
    candidateAccount: selected,
    signal: input.signal,
    trafficSource: 'runtime_recovery_probe',
    disableAccountStateMutation: true,
    bypassKeyModelAdmission: true,
    onUpstreamAttempt: (attempt) => { upstreamAttempt = attempt }
  })
  const transport = transportProbeOutcomeFromAccountTestResult(result, {
    upstreamAttempt,
    canceled: input.signal.aborted
  })
  return outcomeFromTransport(transport)
}

function selectExactApiKey(account: OpenAIAccountSecret, fingerprint: string): OpenAIAccountSecret | undefined {
  const entry = accountApiKeyEntries(account.credentials).find((candidate) => candidate.fingerprint === fingerprint)
  if (!entry) return undefined
  return {
    ...account,
    apiKey: entry.key,
    selectedApiKeyFingerprint: entry.fingerprint,
    selectedApiKeyIndex: entry.index,
    credentials: { ...account.credentials, api_key: entry.key }
  }
}

function endpointMode(family: string, mode: string): AccountSupportedEndpointMode {
  const stream = mode.endsWith('_sse')
  if (family === 'chat_completions') return stream ? 'chat_sse' : 'chat_json'
  if (family === 'responses') return stream ? 'responses_sse' : 'responses_json'
  if (family === 'messages') return stream ? 'messages_sse' : 'messages_json'
  if (family === 'generate_content' || family === 'stream_generate_content') return stream ? 'generate_content_sse' : 'generate_content_json'
  if (family === 'interactions') return stream ? 'interactions_sse' : 'interactions_json'
  return 'chat_json'
}

function outcomeFromTransport(outcome: TransportProbeOutcome): KeyModelOutcome {
  if (outcome.kind === 'framing_complete' && outcome.semanticSuccess !== false) return 'complete_success'
  if (outcome.kind === 'transport_incomplete') return 'upstream_not_complete'
  return 'unknown'
}
