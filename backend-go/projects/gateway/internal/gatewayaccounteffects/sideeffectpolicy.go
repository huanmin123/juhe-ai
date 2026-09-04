package gatewayaccounteffects

// AccountErrorHandlingOperationRuntimeKey mirrors
// accountErrorHandlingOperationRuntimeKey.
func AccountErrorHandlingOperationRuntimeKey(operation AccountSideEffectOperation) (string, error) {
	return GatewayAccountRuntimeKeyForSecret(operation.Account)
}

// MustAccountErrorHandlingOperationRuntimeKey panics on invalid authorized
// bindings, matching the Node throw propagation.
func MustAccountErrorHandlingOperationRuntimeKey(operation AccountSideEffectOperation) string {
	key, err := GatewayAccountRuntimeKeyForSecret(operation.Account)
	if err != nil {
		panic(err)
	}
	return key
}

// ShouldCoalesceQueuedAccountErrorHandlingSideEffect mirrors
// shouldCoalesceQueuedAccountErrorHandlingSideEffect.
func ShouldCoalesceQueuedAccountErrorHandlingSideEffect(item *QueuedAccountSideEffect, operation AccountSideEffectOperation) bool {
	if operation.Input.Success {
		return false
	}
	runtimeKey, err := AccountErrorHandlingOperationRuntimeKey(operation)
	if err != nil {
		return false
	}
	return isQueuedAccountErrorHandlingForRuntimeKey(item, runtimeKey)
}

// ShouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess mirrors
// shouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess.
func ShouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess(item *QueuedAccountSideEffect, runtimeKey string) bool {
	return isQueuedAccountErrorHandlingForRuntimeKey(item, runtimeKey)
}

// ShouldSkipHealthySuccessfulAccountSideEffect mirrors
// shouldSkipHealthySuccessfulAccountSideEffect.
func ShouldSkipHealthySuccessfulAccountSideEffect(operation AccountSideEffectOperation) bool {
	if !operation.Input.Success {
		return false
	}
	account := operation.Account
	return account.Status == "active" &&
		account.CooldownUntil == nil &&
		account.LastErrorMessage == nil &&
		maxInt64(0, int64(account.StreamFailureCount)) == 0 &&
		account.StreamFailureWindowStartedAt == nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func isQueuedAccountErrorHandlingForRuntimeKey(item *QueuedAccountSideEffect, runtimeKey string) bool {
	if item.Operation.Type != AccountSideEffectOperationType {
		return false
	}
	key, err := GatewayAccountRuntimeKeyForSecret(item.Operation.Account)
	if err != nil {
		return false
	}
	return key == runtimeKey
}
