package gatewayaccounteffects

import (
	"errors"
	"strings"
)

// errAuthorizationRuntimeKeyMissingBinding mirrors
// throw new Error('授权账户运行态键缺少绑定上下文').
var errAuthorizationRuntimeKeyMissingBinding = errors.New("授权账户运行态键缺少绑定上下文")

// SuppressibleGatewayAccount mirrors SuppressibleGatewayAccount
// (runtime/account-runtime-keys.ts). Empty optional fields behave like the
// Node undefined values; empty string is falsy there too.
type SuppressibleGatewayAccount struct {
	ID                        string
	AccessType                string // 'owner' | 'authorized'
	AccountAccessType         string // 'owner' | 'account_authorized' | 'group_authorized'
	BindingSystemAccountID    string
	BoundGroupID              string
	AccountAuthorizationID    string
	CredentialSourceAccountID string
}

// AuthorizedBinding mirrors GatewayAccountRuntimeClearTarget['authorizedBinding'].
type AuthorizedBinding struct {
	SystemAccountID         string
	GroupID                 string
	AccountAuthorizationID  string
}

// GatewayAccountRuntimeClearTarget mirrors GatewayAccountRuntimeClearTarget.
// IncludeBaseAccountKey follows the Node default: nil and true both include
// the bare account key; only an explicit false drops it.
type GatewayAccountRuntimeClearTarget struct {
	AccountID         string
	AuthorizedBinding *AuthorizedBinding
	// IncludeBaseAccountKey: manual authorized-instance resets must not touch
	// the bare account key.
	IncludeBaseAccountKey             *bool
	PreserveConfiguredPolicyAvoidance bool
}

// GatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey.
func GatewayAccountRuntimeKey(account SuppressibleGatewayAccount) (string, error) {
	if account.AccountAccessType == "account_authorized" || account.AccessType == "authorized" {
		systemAccountID := account.BindingSystemAccountID
		groupID := account.BoundGroupID
		authorizationID := account.AccountAuthorizationID
		if systemAccountID != "" && groupID != "" && authorizationID != "" {
			return account.ID + ":authorized:" + systemAccountID + ":" + groupID + ":" + authorizationID, nil
		}
		return "", errAuthorizationRuntimeKeyMissingBinding
	}
	return account.ID, nil
}

// MustGatewayAccountRuntimeKey is the variant for call sites that already
// validated the authorized binding; invalid input panics like the Node throw.
func MustGatewayAccountRuntimeKey(account SuppressibleGatewayAccount) string {
	key, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		panic(err)
	}
	return key
}

// GatewayAccountID mirrors gatewayAccountId.
func GatewayAccountID(account SuppressibleGatewayAccount) string {
	return account.ID
}

// RuntimeAccountIDFromKey mirrors runtimeAccountIdFromKey.
func RuntimeAccountIDFromKey(runtimeKey string) string {
	if index := strings.Index(runtimeKey, ":"); index >= 0 {
		return runtimeKey[:index]
	}
	return runtimeKey
}

// AccountRuntimeClearTargetFromAccount builds the clear target for a
// suppressible account, mirroring the non-ClearTarget branch of
// gatewayAccountRuntimeClearKeys.
func AccountRuntimeClearTargetFromAccount(account SuppressibleGatewayAccount) GatewayAccountRuntimeClearTarget {
	target := GatewayAccountRuntimeClearTarget{AccountID: account.ID}
	if account.AccountAccessType == "account_authorized" || account.AccessType == "authorized" {
		target.AuthorizedBinding = &AuthorizedBinding{
			SystemAccountID:        account.BindingSystemAccountID,
			GroupID:                account.BoundGroupID,
			AccountAuthorizationID: account.AccountAuthorizationID,
		}
	}
	return target
}

// ClearKeys mirrors gatewayAccountRuntimeClearKeys for a clear target. Node
// accumulates into a Set (insertion order): base account key first, then the
// authorized binding key.
func (t GatewayAccountRuntimeClearTarget) ClearKeys() []string {
	accountID := strings.TrimSpace(t.AccountID)
	if accountID == "" {
		return []string{}
	}
	keys := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(key string) {
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if t.IncludeBaseAccountKey == nil || *t.IncludeBaseAccountKey {
		add(accountID)
	}
	if binding := t.AuthorizedBinding; binding != nil {
		systemAccountID := strings.TrimSpace(binding.SystemAccountID)
		groupID := strings.TrimSpace(binding.GroupID)
		authorizationID := strings.TrimSpace(binding.AccountAuthorizationID)
		if systemAccountID != "" && groupID != "" && authorizationID != "" {
			add(accountID + ":authorized:" + systemAccountID + ":" + groupID + ":" + authorizationID)
		}
	}
	return keys
}

// ClearKeysForAccount mirrors gatewayAccountRuntimeClearKeys for a plain
// suppressible account.
func ClearKeysForAccount(account SuppressibleGatewayAccount) []string {
	if account.ID != "" {
		return AccountRuntimeClearTargetFromAccount(account).ClearKeys()
	}
	return []string{}
}

// ClearKeysFromString mirrors gatewayAccountRuntimeClearKeys('runtime-key').
func ClearKeysFromString(runtimeKey string) []string {
	normalized := strings.TrimSpace(runtimeKey)
	if normalized == "" {
		return []string{}
	}
	return []string{normalized}
}
