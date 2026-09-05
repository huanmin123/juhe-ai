import {
  gatewayAccountRuntimeKey
} from './account-runtime-keys.js'
import type {
  AccountErrorHandlingOperation,
  QueuedAccountSideEffect
} from './account-side-effect-queue.js'

export function accountErrorHandlingOperationRuntimeKey(operation: AccountErrorHandlingOperation): string {
  return gatewayAccountRuntimeKey(operation.account)
}

export function shouldCoalesceQueuedAccountErrorHandlingSideEffect(
  item: QueuedAccountSideEffect,
  operation: AccountErrorHandlingOperation
): boolean {
  return !operation.input.success
    && isQueuedAccountErrorHandlingForRuntimeKey(item, accountErrorHandlingOperationRuntimeKey(operation))
}

export function shouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess(
  item: QueuedAccountSideEffect,
  runtimeKey: string
): boolean {
  return isQueuedAccountErrorHandlingForRuntimeKey(item, runtimeKey)
}

export function shouldSkipHealthySuccessfulAccountSideEffect(operation: AccountErrorHandlingOperation): boolean {
  if (!operation.input.success) {
    return false
  }
  const account = operation.account
  return account.status === 'active'
    && !account.cooldownUntil
    && !account.lastErrorMessage
    && Math.max(0, account.streamFailureCount ?? 0) === 0
    && !account.streamFailureWindowStartedAt
}

function isQueuedAccountErrorHandlingForRuntimeKey(
  item: QueuedAccountSideEffect,
  runtimeKey: string
): boolean {
  return item.operation.type === 'apply_account_error_handling'
    && gatewayAccountRuntimeKey(item.operation.account) === runtimeKey
}
