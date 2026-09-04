package oauthmgmt

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// operationMode mirrors operation-log.service.ts operationMode.
func operationMode(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "self"
}

// recordCreateLog mirrors buildOAuthCreateLog for the create-from-*/sso routes
// (credentials never appear in clear text; the change entry is sensitive).
func (d *Deps) recordCreateLog(r *http.Request, plan providerPlan, access AccessScope, operationKey, summaryPrefix, resultID, ownerID, name string, groupID *string) {
	if d.Sink == nil {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return
	}
	changes := []authsys.OperationLogChange{
		{Field: "name", Label: "名称", After: name},
		{Field: "type", Label: "账户类型", After: plan.accountType},
		{Field: "credentials", Label: "OAuth 凭据", After: "已写入", Sensitive: true},
	}
	if groupID != nil && *groupID != "" {
		changes = append(changes, authsys.OperationLogChange{Field: "groupId", Label: "绑定分组", After: *groupID})
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID:          auth.SystemAccountID,
		ActorUsername:                 auth.Username,
		ActorDisplayName:              auth.DisplayName,
		ActorRole:                     auth.Role,
		OperationScopeSystemAccountID: ownerID,
		Mode:                          operationMode(access),
		Module:                        plan.module,
		Action:                        "create_account",
		OperationKey:                  operationKey,
		ResourceType:                  "account",
		ResourceID:                    resultID,
		ResourceName:                  name,
		Summary:                       summaryPrefix + "：" + name,
		Changes:                       changes,
		Viewers: []authsys.OperationLogViewer{
			{SystemAccountID: ownerID, Reason: "resource_owner"},
		},
	}, r)
}

// recordUpdateLog mirrors buildOAuthUpdateLog for the refresh/reauthorize
// routes.
func (d *Deps) recordUpdateLog(r *http.Request, plan providerPlan, access AccessScope, auth *authsys.AuthContext, before *rotationAccount, updated *RotationResult, action, summaryPrefix string) {
	if d.Sink == nil || auth == nil {
		return
	}
	changes := []authsys.OperationLogChange{
		{Field: "credentials", Label: "OAuth 凭据", Before: "已设置", After: "已更新", Sensitive: true},
		{Field: "status", Label: "状态", Before: before.Status, After: before.Status},
	}
	if before.LastErrorCode != "" {
		changes = append(changes, authsys.OperationLogChange{
			Field: "lastErrorCode", Label: "异常类型", Before: before.LastErrorCode, After: before.LastErrorCode,
		})
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID:          auth.SystemAccountID,
		ActorUsername:                 auth.Username,
		ActorDisplayName:              auth.DisplayName,
		ActorRole:                     auth.Role,
		OperationScopeSystemAccountID: before.SystemAccountID,
		Mode:                          operationMode(access),
		Module:                        plan.module,
		Action:                        action,
		OperationKey:                  plan.module + "." + action,
		ResourceType:                  "account",
		ResourceID:                    updated.ID,
		ResourceName:                  before.Name,
		Summary:                       summaryPrefix + "：" + before.Name,
		Changes:                       changes,
		Viewers: []authsys.OperationLogViewer{
			{SystemAccountID: before.SystemAccountID, Reason: "resource_owner"},
		},
	}, r)
}
