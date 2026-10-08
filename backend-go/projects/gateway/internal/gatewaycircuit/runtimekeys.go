package gatewaycircuit

import (
	"errors"
	"fmt"
	"strings"
)

// SuppressibleGatewayAccount mirrors the SuppressibleGatewayAccount shape in
// account-runtime-keys.ts (only the fields the runtime key needs).
type SuppressibleGatewayAccount struct {
	ID                        string
	AccessType                string // 'owner' | 'authorized' | ''
	AccountAccessType         string // 'owner' | 'account_authorized' | 'group_authorized' | ''
	BindingSystemAccountID    string
	BoundGroupID              string
	AccountAuthorizationID    string
	CredentialSourceAccountID string
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

// RecoveryRuntimeIdentity 是恢复扫描的运行态身份解析结果（契约源
// opsjobs.RecoveryRuntimeIdentity，对齐 Node parseRecoveryRuntimeIdentity）。
type RecoveryRuntimeIdentity struct {
	Kind            string // owner | authorized
	AccountID       string
	SystemAccountID string
	GroupID         string
	AuthorizationID string
}

// ParseRecoveryRuntimeIdentity 解析恢复目标运行态键的两种形态：
// `id`（owner）与 `id:authorized:systemAccount:group:authorization`（授权绑定）。
// 契约源 opsjobs.ParseRecoveryRuntimeIdentity 逐分支对照；分隔格式与
// GatewayAccountRuntimeKey 的构造（上方 fmt.Sprintf("%s:authorized:%s:%s:%s")）
// 同源，解析是对应逆运算。trim/split 语义照抄契约源：owner 段取首个 ':' 之前
// 并 trim 空白；authorized 形态的剩余段按空白 trim 后剔空，必须恰好 3 段。
func ParseRecoveryRuntimeIdentity(runtimeKey string) (RecoveryRuntimeIdentity, bool) {
	const marker = ":authorized:"
	markerIndex := strings.Index(runtimeKey, marker)
	if markerIndex < 0 {
		accountID := strings.TrimSpace(RuntimeAccountIDFromKey(runtimeKey))
		if accountID == "" {
			return RecoveryRuntimeIdentity{}, false
		}
		return RecoveryRuntimeIdentity{Kind: "owner", AccountID: accountID}, true
	}
	accountID := strings.TrimSpace(runtimeKey[:markerIndex])
	rest := runtimeKey[markerIndex+len(marker):]
	parts := splitNonEmptyRuntimeKeyParts(rest)
	if accountID == "" || len(parts) != 3 {
		return RecoveryRuntimeIdentity{}, false
	}
	return RecoveryRuntimeIdentity{
		Kind:            "authorized",
		AccountID:       accountID,
		SystemAccountID: parts[0],
		GroupID:         parts[1],
		AuthorizationID: parts[2],
	}, true
}

// splitNonEmptyRuntimeKeyParts 对照契约源 splitNonEmpty：按 ':' 切分后逐段
// trim 空白，剔除空段。
func splitNonEmptyRuntimeKeyParts(value string) []string {
	raw := strings.Split(value, ":")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
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
