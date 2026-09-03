import type { AccountSummary } from '../../domain/types.js'
import { isExplicitAccountErrorPolicyCooldown } from '../../domain/account-runtime-provenance.js'
import { clearServerAccountRuntimeAvailability } from '../db-service/db-service-ipc.js'
import { clearNormalRouteLatencyDegradationForAccountAsync } from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { dispatchAccountHealthCheck } from './account-health-check-dispatch.service.js'
import { isAccountExpired } from '../../storage/account-runtime-mutation-helpers.js'
import { isResourceAuthorizationExpired } from '../../storage/resource-authorization-helpers.js'
import { advanceAccountCircuitDispatchRevision } from '../../storage/account-circuit-control-plane.repository.js'
import { newId } from '../../storage/database.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import {
  clearGatewayAccountApiKeyFailureGuard,
  clearGatewayAccountApiKeyTransientFailure,
  loadGatewayAccountApiKeyTransientStatesForDispatch
} from '../gateway/runtime/account-api-key-failure-guard.service.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import {
  AuthorizedAccountDispatchRevisionConflictError,
  updateAuthorizedAccountBindingDispatchAsync
} from '../../storage/account-authorized-dispatch.repository.js'
import {
  AccountManagementPatchRevisionConflictError,
  patchAccountManagementAsync
} from '../../storage/account-management-patch.repository.js'
import { revalidateAccountApiKeyRuntimePoolAsync } from '../../storage/account-api-key-runtime-state.repository.js'
import { findAccountSummaryAsync } from '../../storage/account-summary.repository.js'
import { findAccountLockStateAsync } from '../../storage/account-lock.repository.js'
import type { RequestAccessScope } from '../auth/request-context.js'
import { operationMode, viewer } from '../operation-logs/operation-log.service.js'

export interface AccountRuntimeResetResult {
  id: string
  configRevision: number
  dispatchRevision?: number
  changed: boolean
  status: AccountSummary['status']
  schedulable: boolean
  dispatchEligible: boolean
  gatewayRuntime: 'cleared' | 'unchanged' | 'unavailable'
  latencyDegradationCleared: number
  apiKeyRuntimeRevalidated: number
  apiKeyTransientCleared: number
  cleared: string[]
  skipped: string[]
  failed: string[]
}

export interface AccountRuntimeResetOutcome {
  result: AccountRuntimeResetResult
  log: {
    operationScopeSystemAccountId?: string
    mode: ReturnType<typeof operationMode>
    module: string
    action: string
    operationKey: string
    resourceType: string
    resourceId: string
    resourceName: string
    summary: string
    changes: Array<{ field: string; label: string; before: unknown; after: unknown }>
    viewers: ReturnType<typeof viewer>
  }
}

export async function resetAccountRuntimeStateAsync(input: {
  accountId: string
  expectedConfigRevision: number
  access?: RequestAccessScope
}): Promise<AccountRuntimeResetOutcome | undefined> {
  const before = await findAccountSummaryAsync(input.accountId, input.access)
  if (!before) return undefined
  if (before.configRevision !== input.expectedConfigRevision) {
    throw new AccountManagementPatchRevisionConflictError(input.accountId, input.expectedConfigRevision, before.configRevision)
  }

  let id = before.id
  let configRevision = before.configRevision ?? input.expectedConfigRevision
  let changedFields: string[] = []
  let name = before.name
  let ownerSystemAccountId = before.ownerSystemAccountId ?? before.systemAccountId
  let status = before.status
  let schedulable = before.schedulable
  let authorizedBinding: { systemAccountId: string; groupId: string; accountAuthorizationId: string } | undefined
  let healthCheckRequired = false
  let healthCheckReason: 'activation' | 'configuration' | undefined
  const failed: string[] = []
  let apiKeyRuntimeRevalidated = 0
  let apiKeyTransientCleared = 0
  let dispatchRevision: number | undefined
  let dispatchFenceAdvanced = false
  let preserveConfiguredPolicyAvoidance = isExplicitAccountErrorPolicyCooldown(before.lastErrorCode, before.lastErrorMessage)
  const cleared = new Set<string>()
  const skipped = new Set<string>()
  // Lock incidents are a separate hard boundary. The reset action may clear
  // unrelated runtime observations, but it must never turn a lock-policy
  // outage back into a dispatchable account.
  let lockState: Awaited<ReturnType<typeof findAccountLockStateAsync>>
  try {
    lockState = await findAccountLockStateAsync(before.id)
  } catch {
    throw new Error('账户锁死状态读取失败，请稍后重试')
  }
  const lockBlocksPersistentReset = Boolean(
    lockState?.enabled
    && (lockState.lockState === 'ENGAGED' || lockState.lockState === 'DEAD_CONFIRMED')
  )
  if (lockBlocksPersistentReset) skipped.add('lock_state')

  if (before.accessType === 'authorized') {
    const manualUnschedulable = before.status === 'active' && before.schedulable === false
    authorizedBinding = before.bindingSystemAccountId && before.boundGroupId && before.accountAuthorizationId
      ? {
          systemAccountId: before.bindingSystemAccountId,
          groupId: before.boundGroupId,
          accountAuthorizationId: before.accountAuthorizationId
        }
      : undefined
    const sourceExplicitPolicyCooldown = isExplicitAccountErrorPolicyCooldown(
      before.authorizationInstanceSourceAccountLastErrorCode,
      before.authorizationInstanceSourceAccountLastErrorMessage
    )
    preserveConfiguredPolicyAvoidance = preserveConfiguredPolicyAvoidance || sourceExplicitPolicyCooldown
    const sourceBlocked = Boolean(
      // A deleted or incompletely projected source is a hard availability
      // boundary.  Do not rewrite the local authorized-instance failure state
      // while the source account itself cannot be resolved.
      !before.authorizationInstanceSourceAccountId
      || (before.authorizationStatus && before.authorizationStatus !== 'active')
      || isResourceAuthorizationExpired(before.authorizationExpiresAt)
      || before.groupBindStatus === 'authorization_unavailable'
      || Boolean(before.authorizationInstanceSourceAccountId && !before.authorizationInstanceSourceAccountStatus)
      || (before.authorizationInstanceSourceAccountStatus && before.authorizationInstanceSourceAccountStatus !== 'active')
      || before.authorizationInstanceSourceAccountSchedulable === false
      || isAccountExpired(before.authorizationInstanceSourceAccountExpiresAt)
      || before.authorizationInstanceSourceAccountLastErrorCode === 'account_expired'
      || isAccountExpired(before.accountExpiresAt)
      || before.lastErrorCode === 'account_expired'
      || before.status === 'quality_isolated'
      || sourceExplicitPolicyCooldown
      || isFutureTimestamp(before.authorizationInstanceSourceAccountCooldownUntil)
      || before.authorizationQuotaExceeded
      || isExplicitAccountErrorPolicyCooldown(before.lastErrorCode, before.lastErrorMessage)
      || lockBlocksPersistentReset
      || manualUnschedulable
    )
    if (before.status === 'pending_test') skipped.add('pending_test')
    if (before.status === 'error') skipped.add('health_check_gate')
    if (before.status === 'disabled') skipped.add('disabled')
    if (before.status === 'quality_isolated') skipped.add('quality_isolated')
    if (isAccountExpired(before.accountExpiresAt) || before.lastErrorCode === 'account_expired') skipped.add('expired')
    if (manualUnschedulable) skipped.add('manual_unschedulable')
    if (before.authorizationQuotaExceeded) skipped.add('authorization_quota')
    if (sourceBlocked && !before.authorizationQuotaExceeded) skipped.add('authorization_source_blocked')
    if (sourceExplicitPolicyCooldown || isExplicitAccountErrorPolicyCooldown(before.lastErrorCode, before.lastErrorMessage)) skipped.add('explicit_policy_cooldown')
    if (before.status !== 'pending_test' && before.status !== 'error' && before.status !== 'disabled' && !sourceBlocked && !manualUnschedulable) {
      const patched = await updateAuthorizedAccountBindingDispatchAsync(input.accountId, {
        expectedConfigRevision: input.expectedConfigRevision,
        clearFailureState: true
      }, input.access, { runtimeResetRequireUnlocked: true })
      if (!patched) return undefined
      id = patched.id
      configRevision = patched.configRevision
      changedFields = patched.changedFields
      name = patched.name
      ownerSystemAccountId = patched.ownerSystemAccountId
      status = patched.patch.status ?? before.status
      schedulable = patched.patch.schedulable ?? before.schedulable
      authorizedBinding = patched.authorizedBinding
      if (patched.changedFields.length > 0) cleared.add('account_persistent')
      // The existing failure-state transaction advances the circuit fence only
      // when it restores an account directly to active. Error/pending_test is
      // deliberately left behind the health-check gate, so the manual fence
      // below must still run for that changed state.
      dispatchFenceAdvanced = patched.changedFields.length > 0 && patched.runtimeRestoreRequired && status === 'active'
      if (dispatchFenceAdvanced) cleared.add('dispatch_revision')
    }
  } else {
    const manualUnschedulable = before.status === 'active' && before.schedulable === false
    const pendingHealthCheckWithoutFailure = before.status === 'pending_test'
      && !(before.lastHealthCheckAt && (before.lastHealthCheckErrorCode || before.lastHealthCheckErrorMessage))
    const skipPersistentClear = pendingHealthCheckWithoutFailure
      || before.status === 'disabled'
      || manualUnschedulable
      || before.lastErrorCode === 'account_expired'
      || isAccountExpired(before.accountExpiresAt)
      || before.status === 'quality_isolated'
      || !hasPersistentFailureState(before)
      || isExplicitAccountErrorPolicyCooldown(before.lastErrorCode, before.lastErrorMessage)
      || lockBlocksPersistentReset
    if (before.status === 'pending_test') skipped.add('pending_test')
    if (before.status === 'disabled') skipped.add('disabled')
    if (before.status === 'quality_isolated') skipped.add('quality_isolated')
    if (before.lastErrorCode === 'account_expired' || isAccountExpired(before.accountExpiresAt)) skipped.add('expired')
    if (manualUnschedulable) skipped.add('manual_unschedulable')
    if (isExplicitAccountErrorPolicyCooldown(before.lastErrorCode, before.lastErrorMessage)) skipped.add('explicit_policy_cooldown')
    if (!skipPersistentClear) {
      const patched = await patchAccountManagementAsync(input.accountId, {
        expectedConfigRevision: input.expectedConfigRevision,
        clearFailureState: true,
        runtimeResetRequireUnlocked: true
      }, input.access)
      if (!patched) return undefined
      id = patched.id
      configRevision = patched.configRevision
      changedFields = patched.changedFields
      name = patched.name
      ownerSystemAccountId = patched.ownerSystemAccountId
      status = patched.status
      healthCheckRequired = patched.healthCheckRequired
      healthCheckReason = patched.healthCheckReason
      authorizedBinding = patched.authorizedBinding
      if (patched.changedFields.length > 0) cleared.add('account_persistent')
      dispatchFenceAdvanced = patched.changedFields.length > 0 && patched.runtimeRestoreRequired && status === 'active'
      if (dispatchFenceAdvanced) cleared.add('dispatch_revision')
    }
  }

  let runtimeClear: Awaited<ReturnType<typeof clearServerAccountRuntimeAvailability>>
  try {
    runtimeClear = await clearServerAccountRuntimeAvailability({
      accountId: id,
      authorizedBinding,
      includeBaseAccountKey: before.accessType !== 'authorized',
      preserveConfiguredPolicyAvoidance
    })
    if (!runtimeClear || (runtimeClear.failedKeys?.length ?? 0) > 0) failed.push('gateway_runtime')
    if (runtimeClear?.cleared) cleared.add('gateway_runtime')
  } catch {
    failed.push('gateway_runtime')
  }

  const systemAccountId = before.accessType === 'authorized'
    ? before.bindingSystemAccountId
    : before.ownerSystemAccountId ?? before.systemAccountId
  let latencyDegradationCleared = 0
  try {
    latencyDegradationCleared = systemAccountId
      ? await clearNormalRouteLatencyDegradationForAccountAsync({ systemAccountId, accountId: before.id })
      : 0
    if (latencyDegradationCleared > 0) cleared.add('speed_first_latency')
  } catch {
    failed.push('speed_first_latency')
  }

  // A runtime-only reset may not change any persistent account columns. Advance
  // the circuit dispatch fence when a runtime state was actually cleared, so
  // stale open/half-open incidents and asynchronous results cannot keep the
  // account out of scheduling. A normal account is left untouched.
  if (!dispatchFenceAdvanced && (changedFields.length > 0 || runtimeClear?.cleared || latencyDegradationCleared > 0)) {
    try {
      const fenced = await advanceAccountCircuitDispatchRevision({
        accountId: id,
        accountRuntimeKey: id,
        transitionId: newId('dispatch'),
        nowMs: Date.now()
      })
      dispatchRevision = fenced.dispatchRevision
      dispatchFenceAdvanced = fenced.status === 'applied' || fenced.status === 'idempotent'
      if (dispatchFenceAdvanced) cleared.add('dispatch_revision')
    } catch {
      failed.push('dispatch_revision')
    }
  }

  if (healthCheckRequired && healthCheckReason) dispatchAccountHealthCheck(id, healthCheckReason)

  const current = await findAccountSummaryAsync(id, input.access)
  if (current) {
    configRevision = current.configRevision ?? configRevision
    dispatchRevision = current.dispatchRevision ?? dispatchRevision
    status = current.status
    schedulable = current.schedulable
  }
  if (before.accessType !== 'authorized' && before.type === 'api_key') {
    try {
      const revalidated = await revalidateAccountApiKeyRuntimePoolAsync({ accountId: id, expectedConfigRevision: configRevision })
      if (revalidated.eligible) apiKeyRuntimeRevalidated = revalidated.changed
      if (apiKeyRuntimeRevalidated > 0) cleared.add('api_key_runtime')
    } catch {
      failed.push('api_key_runtime')
    }
  }

  if (before.accessType !== 'authorized' && before.type === 'api_key') {
    try {
      const entries = accountApiKeyEntries(before.credentials)
      const transientStates = await loadGatewayAccountApiKeyTransientStatesForDispatch(
        id,
        entries.map((entry) => entry.fingerprint)
      )
      const stateByFingerprint = new Map(transientStates.map((state) => [state.keyFingerprint, state]))
      for (const entry of entries) {
        const state = stateByFingerprint.get(entry.fingerprint)
        const target = {
          id,
          selectedApiKeyFingerprint: entry.fingerprint,
          selectedApiKeyTransientGeneration: state?.transientGeneration
        } as unknown as OpenAIAccountSecret
        let cleared = clearGatewayAccountApiKeyFailureGuard(target)
        // A Redis transient record may already have expired from the dispatch
        // projection and therefore be reported as `active`; the generation is
        // still the authoritative CAS fence and should be tombstoned when it
        // exists, so stale failure counters cannot reappear on the next load.
        if (state?.transientGeneration) {
          cleared = await clearGatewayAccountApiKeyTransientFailure(target) || cleared
        }
        if (cleared) apiKeyTransientCleared += 1
      }
      if (apiKeyTransientCleared > 0) cleared.add('api_key_transient')
    } catch {
      failed.push('api_key_transient')
    }
  }

  const final = await findAccountSummaryAsync(id, input.access)
  let finalLockState: Awaited<ReturnType<typeof findAccountLockStateAsync>>
  let finalLockStateReadFailed = false
  try {
    finalLockState = await findAccountLockStateAsync(id)
  } catch {
    finalLockStateReadFailed = true
    failed.push('lock_state')
  }
  const finalLockBlocked = Boolean(
    finalLockStateReadFailed
    || (finalLockState?.enabled
    && (finalLockState.lockState === 'ENGAGED' || finalLockState.lockState === 'DEAD_CONFIRMED')
    )
  )
  if (finalLockBlocked) skipped.add('lock_state')
  const dispatchEligible = Boolean(!finalLockBlocked && final?.effectiveAvailability.available && final.schedulable)
  if (final) {
    configRevision = final.configRevision ?? configRevision
    dispatchRevision = final.dispatchRevision ?? dispatchRevision
    status = final.status
    schedulable = final.schedulable
  }

  return {
    result: {
      id,
      configRevision,
      dispatchRevision,
      changed: changedFields.length > 0 || Boolean(runtimeClear?.cleared) || latencyDegradationCleared > 0 || apiKeyRuntimeRevalidated > 0 || apiKeyTransientCleared > 0 || dispatchFenceAdvanced,
      status,
      schedulable,
      dispatchEligible,
      gatewayRuntime: runtimeClear ? (runtimeClear.cleared ? 'cleared' : 'unchanged') : 'unavailable',
      latencyDegradationCleared,
      apiKeyRuntimeRevalidated,
      apiKeyTransientCleared,
      cleared: [...cleared],
      skipped: [...skipped],
      failed
    },
    log: {
      operationScopeSystemAccountId: ownerSystemAccountId,
      mode: operationMode(input.access),
      module: 'accounts',
      action: 'runtime_reset',
      operationKey: 'accounts.runtime_reset',
      resourceType: 'account',
      resourceId: id,
      resourceName: name,
      summary: `清理 AI 账户运行状态：${name}`,
      changes: [{ field: 'runtimeState', label: '运行状态', before: before.status, after: status }],
      viewers: viewer(ownerSystemAccountId, 'resource_owner')
    }
  }
}

function isFutureTimestamp(value: string | undefined): boolean {
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp > Date.now()
}

function hasPersistentFailureState(account: AccountSummary): boolean {
  return account.status === 'error'
    || account.status === 'rate_limited'
    || account.status === 'temporary_unavailable'
    || Boolean(
      account.cooldownUntil
      || account.lastErrorCode
      || account.lastErrorMessage
      || account.lastErrorTraceId
      || (account.cooldownRetestFailureCount ?? 0) > 0
      || account.cooldownRetestObservationStartedAt
      || account.cooldownRetestGeneration
      || account.cooldownRetestLastAt
      || (account.healthCheckFailureCount ?? 0) > 0
      || account.healthCheckFailureStartedAt
      || account.lastHealthCheckErrorCode
      || account.lastHealthCheckErrorMessage
      || (account.streamFailureCount ?? 0) > 0
      || account.streamFailureWindowStartedAt
    )
}

export { AccountManagementPatchRevisionConflictError, AuthorizedAccountDispatchRevisionConflictError }
