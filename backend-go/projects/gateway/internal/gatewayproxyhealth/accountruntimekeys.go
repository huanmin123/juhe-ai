package gatewayproxyhealth

import (
	"errors"
	"fmt"
	"strings"
)

// SuppressibleGatewayAccount mirrors runtime/account-runtime-keys.ts
// SuppressibleGatewayAccount (the fields gatewayAccountRuntimeKey reads, plus
// the duck-typed display name). Empty string means the Node field is absent.
type SuppressibleGatewayAccount struct {
	ID                     string
	AccessType             string // 'owner' | 'authorized'
	AccountAccessType      string // 'owner' | 'account_authorized' | 'group_authorized'
	BindingSystemAccountID string
	BoundGroupID           string
	AccountAuthorizationID string
	Name                   string
}

// GatewayAccountRuntimeKey mirrors gatewayAccountRuntimeKey. Authorized
// instances without the full binding context throw in Node
// ('授权账户运行态键缺少绑定上下文') and return an error here.
func GatewayAccountRuntimeKey(account SuppressibleGatewayAccount) (string, error) {
	if account.AccountAccessType == "account_authorized" || account.AccessType == "authorized" {
		if account.BindingSystemAccountID != "" && account.BoundGroupID != "" && account.AccountAuthorizationID != "" {
			return fmt.Sprintf("%s:authorized:%s:%s:%s",
				account.ID, account.BindingSystemAccountID, account.BoundGroupID, account.AccountAuthorizationID), nil
		}
		return "", errors.New("授权账户运行态键缺少绑定上下文")
	}
	return account.ID, nil
}

// RuntimeAccountIDFromKey mirrors runtimeAccountIdFromKey.
func RuntimeAccountIDFromKey(runtimeKey string) string {
	first := strings.SplitN(runtimeKey, ":", 2)[0]
	if first == "" {
		return runtimeKey
	}
	return first
}
