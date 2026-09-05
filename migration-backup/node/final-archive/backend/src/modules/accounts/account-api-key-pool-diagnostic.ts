import type { AccountApiKeyEntry } from '../../storage/account-api-key-rotation.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import {
  fixedAccountApiKeyPoolCandidate,
  isCandidateAccountApiKeyPoolTestable
} from './account-api-key-pool-runtime.js'
import { accountDiagnosticRetryTimeoutMs } from './account-diagnostic-retry-policy.js'

export function accountApiKeyPoolKeySetFingerprint(entries: readonly Pick<AccountApiKeyEntry, 'fingerprint'>[]): string {
  let hash = 2166136261
  for (const fingerprint of entries.map((entry) => entry.fingerprint).sort()) {
    for (const character of fingerprint) {
      hash ^= character.charCodeAt(0)
      hash = Math.imul(hash, 16777619)
    }
    hash ^= 0
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(16).padStart(8, '0')
}

export function orderAccountApiKeyPoolEntries<T extends Pick<AccountApiKeyEntry, 'fingerprint'>>(
  entries: readonly T[],
  startAfterFingerprint?: string
): T[] {
  if (!startAfterFingerprint) return [...entries]
  const index = entries.findIndex((entry) => entry.fingerprint === startAfterFingerprint)
  if (index < 0) return [...entries]
  return [...entries.slice(index + 1), ...entries.slice(0, index + 1)]
}

export interface AccountApiKeyPoolDiagnosticAttempt<T> {
  entry: AccountApiKeyEntry
  value: T
}

export interface AccountApiKeyPoolDiagnosticResult<T> {
  attempts: AccountApiKeyPoolDiagnosticAttempt<T>[]
  winner?: AccountApiKeyPoolDiagnosticAttempt<T>
  /**
   * The last Key in the ordered scan prefix that finished conclusively. This
   * is safe to persist as the next-round cursor even when workers finish out
   * of order.
   */
  lastCompletedFingerprint?: string
  completed: boolean
  errors: Array<{ entry: AccountApiKeyEntry; error: unknown }>
}

export interface AccountApiKeyPoolDiagnosticAttemptResult<T> {
  value: T
  success: boolean
  timedOutAfterRealUpstreamAttempt: boolean
}

/**
 * Shared API Key pool diagnostic scheduler. Callers own result interpretation
 * and all state mutations; this runner only controls fixed-Key attempts.
 */
export async function runAccountApiKeyPoolDiagnostic<T>(
  candidate: OpenAIAccountSecret,
  entries: readonly AccountApiKeyEntry[],
  attempt: (input: {
    entry: AccountApiKeyEntry
    candidate: OpenAIAccountSecret
    timeoutMs: number
    signal: AbortSignal
  }) => Promise<AccountApiKeyPoolDiagnosticAttemptResult<T> | undefined>,
  options: {
    signal?: AbortSignal
    allowSingleEntry?: boolean
    maxConcurrentAttempts?: number
    maxStages?: number
    timeoutSchedule?: readonly number[]
    onKeyAttempt?: (entry: AccountApiKeyEntry) => void
    onEntryComplete?: (attempt: AccountApiKeyPoolDiagnosticAttempt<T>) => void
  } = {}
): Promise<AccountApiKeyPoolDiagnosticResult<T> | undefined> {
  if (entries.length === 0) return undefined
  if (!options.allowSingleEntry && !isCandidateAccountApiKeyPoolTestable(candidate, [...entries])) return undefined

  const runtimeStatusByFingerprint = new Map(
    (candidate.apiKeyRuntimeStates ?? []).map((state) => [state.keyFingerprint, state.status])
  )
  const pending = entries
    .filter((entry) => runtimeStatusByFingerprint.get(entry.fingerprint) !== 'disabled')
    .map((entry) => ({ entry, nextStage: 0, completed: false }))
  const results = new Map<number, AccountApiKeyPoolDiagnosticAttempt<T>>()
  const errors: Array<{ entry: AccountApiKeyEntry; error: unknown }> = []
  const stopController = new AbortController()
  let winner: AccountApiKeyPoolDiagnosticAttempt<T> | undefined
  let completedPrefixLength = 0
  let lastCompletedFingerprint: string | undefined
  const timeoutSchedule = options.timeoutSchedule?.length
    ? options.timeoutSchedule
    : accountDiagnosticRetryTimeoutMs

  const recordCompletedAttempt = (
    current: { entry: AccountApiKeyEntry; nextStage: number; completed: boolean },
    item: AccountApiKeyPoolDiagnosticAttempt<T>
  ): void => {
    results.set(current.entry.index, item)
    current.completed = true
    options.onEntryComplete?.(item)
    while (pending[completedPrefixLength]?.completed) {
      lastCompletedFingerprint = pending[completedPrefixLength]!.entry.fingerprint
      completedPrefixLength += 1
    }
  }

  const stageCount = Math.min(timeoutSchedule.length, Math.max(1, options.maxStages ?? timeoutSchedule.length))
  for (let stage = 0; stage < stageCount; stage += 1) {
    if (options.signal?.aborted || stopController.signal.aborted) break
    const stageEntries = pending.filter((item) => !item.completed && item.nextStage === stage)
    if (stageEntries.length === 0) continue
    let nextIndex = 0
    const worker = async (): Promise<void> => {
      while (nextIndex < stageEntries.length && !options.signal?.aborted && !stopController.signal.aborted) {
        const current = stageEntries[nextIndex++]
        if (!current) return
        options.onKeyAttempt?.(current.entry)
        let attemptResult: AccountApiKeyPoolDiagnosticAttemptResult<T> | undefined
        try {
          attemptResult = await attempt({
            entry: current.entry,
            candidate: fixedAccountApiKeyPoolCandidate(candidate, current.entry, { apiKeyRuntimeStateDisabled: true }),
            timeoutMs: timeoutSchedule[stage] ?? timeoutSchedule[timeoutSchedule.length - 1]!,
            signal: AbortSignal.any([options.signal ?? new AbortController().signal, stopController.signal])
          })
        } catch (error) {
          errors.push({ entry: current.entry, error })
          continue
        }
        if (!attemptResult) continue
        const item = { entry: current.entry, value: attemptResult.value }
        // A result returned after either cancellation may be a locally
        // interrupted request. Do not advance a durable cursor from it.
        if (options.signal?.aborted || stopController.signal.aborted) return
        if (attemptResult.success) {
          winner = item
          recordCompletedAttempt(current, item)
          stopController.abort('account_api_key_pool_success')
          return
        }
        if (attemptResult.timedOutAfterRealUpstreamAttempt && stage + 1 < stageCount) {
          current.nextStage = stage + 1
          continue
        }
        recordCompletedAttempt(current, item)
      }
    }
    const maxConcurrentAttempts = Number.isFinite(options.maxConcurrentAttempts)
      ? Math.max(1, Math.trunc(options.maxConcurrentAttempts!))
      : stageEntries.length
    await Promise.all(Array.from({ length: Math.min(stageEntries.length, maxConcurrentAttempts) }, () => worker()))
  }

  return {
    attempts: [...results.values()].sort((left, right) => left.entry.index - right.entry.index),
    winner,
    lastCompletedFingerprint,
    completed: !options.signal?.aborted && pending.every((item) => item.completed),
    errors
  }
}
