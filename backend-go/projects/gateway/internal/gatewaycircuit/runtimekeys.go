package gatewaycircuit

import (
	"errors"
	"fmt"
	"strings"
)

// SuppressibleGatewayAccount mirrors the SuppressibleGatewayAccount shape in
// account-runtime-keys.ts (only the fields the runtime key needs).
type SuppressibleGatewayAccount struct {
	ID                         string
	AccessType                 string // 'owner' | 'authorized' | ''
	AccountAccessType          string // 'owner' | 'account_authorized' | 'group_authorized' | ''
	BindingSystemAccountID     string
	BoundGroupID               string
	AccountAuthorizationID     string
	CredentialSourceAccountID  string
}

// GatewayAccountRuntimeKeyString mirrors gatewayAccountRuntimeKey(string).
func GatewayAccountRuntimeKeyString(runtimeKey string) string { return runtimeKey }

// GatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey(account).
func GatewayAccountRuntimeKey(account SuppressibleGatewayAccount) (string, error) {
	if account.AccountAccessType == "account_authorized" || account.AccessType == "authorized" {
		systemAccountID := account.BindingSystemAccountID
		groupID := account.BoundGroupID
		authorizationID := account.AccountAuthorizationID
		if systemAccountID != "" && groupID != "" && authorizationID != "" {
			return fmt.Sprintf("%s:authorized:%s:%s:%s", account.ID, systemAccountID, groupID, authorizationID), nil
		}
		return "", errors.New("授权账户运行态键缺少绑定上下文")
	}
	return account.ID, nil
}

// RuntimeAccountIDFromKey mirrors runtimeAccountIdFromKey.
func RuntimeAccountIDFromKey(runtimeKey string) string {
	if index := strings.Index(runtimeKey, ":"); index >= 0 {
		return runtimeKey[:index]
	}
	return runtimeKey
}

// GatewayAccountConcurrencyAccountID mirrors
// gatewayAccountConcurrencyAccountId (dispatch/account-concurrency-identity.ts).
func GatewayAccountConcurrencyAccountID(accountID, credentialSourceAccountID string) string {
	normalized := strings.TrimSpace(credentialSourceAccountID)
	if normalized != "" {
		return normalized
	}
	return accountID
}
