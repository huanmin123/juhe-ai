// Account write operation logs for the /__aipublic__ family, ported from
// recordPublicWelfareAccountWriteOperation / recordPublicWelfareAccountDeleteOperation
// (external-integrations.routes.ts). Failures never fail the request (Node
// logs a warning and continues); there is no session auth context here, the
// actor is the external source (actorSystemAccountId `external:<sourceRefId>`).
package aipublic

import (
	"encoding/json"
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func (d *Deps) recordAccountWriteLog(r *http.Request, context *AuthContext, action, operationKey, verb,
	accountID, accountName string, target *ResolvedPublicTarget, groupCreated bool, extra map[string]any) {
	if d.Sink == nil || context == nil || accountID == "" {
		return
	}
	after := func(value any) string {
		return scalarText(value)
	}
	actionValue := "updated"
	if action == "account_add" {
		actionValue = "created"
	}
	changes := []authsys.OperationLogChange{
		{Field: "action", Label: "写入动作", After: actionValue},
		{Field: "status", Label: "账户状态", After: after(extra["status"])},
		{Field: "schedulable", Label: "可调度", After: after(extra["schedulable"])},
		{Field: "targetCreated", Label: "新建目标用户", After: boolText(target.Created)},
		{Field: "groupCreated", Label: "新建目标分组", After: boolText(groupCreated)},
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID:          "external:" + context.SourceRefID,
		ActorUsername:                 context.SourceName,
		ActorDisplayName:              "外部来源：" + context.SourceName,
		ActorRole:                     "user",
		OperationScopeSystemAccountID: target.SystemAccountID,
		Mode:                          "self",
		Module:                        "external_integrations",
		Action:                        action,
		OperationKey:                  operationKey,
		ResourceType:                  "account",
		ResourceID:                    accountID,
		ResourceName:                  accountName,
		Summary:                       context.SourceName + " " + verb + "账号：" + accountName,
		DetailLevel:                   "full",
		VisibilityScope:               "admin_only",
		Metadata:                      marshalRawMessage(d.accountLogMetadata(r, context, target, accountID)),
		Changes:                       changes,
	}, r)
}

func (d *Deps) recordAccountDeleteLog(r *http.Request, context *AuthContext,
	accountID, accountName string, target *ResolvedPublicTarget, parsed *accountDeleteBody) {
	if d.Sink == nil || context == nil {
		return
	}
	metadata := d.accountLogMetadata(r, context, target, accountID)
	metadata["targetGroupName"] = parsed.TargetGroupName
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID:          "external:" + context.SourceRefID,
		ActorUsername:                 context.SourceName,
		ActorDisplayName:              "外部来源：" + context.SourceName,
		ActorRole:                     "user",
		OperationScopeSystemAccountID: target.SystemAccountID,
		Mode:                          "self",
		Module:                        "external_integrations",
		Action:                        "account_delete",
		OperationKey:                  "external_integrations.public_account_delete",
		ResourceType:                  "account",
		ResourceID:                    accountID,
		ResourceName:                  accountName,
		Summary:                       context.SourceName + " 删除账号：" + accountName,
		DetailLevel:                   "full",
		VisibilityScope:               "admin_only",
		Metadata:                      marshalRawMessage(metadata),
		Changes: []authsys.OperationLogChange{
			{Field: "deleted", Label: "删除状态", After: "true"},
		},
	}, r)
}

// accountLogMetadata carries the source/token/target identifiers (the Node
// metadata block; the request method/path/status ride along because the Go
// entry has no dedicated columns).
func (d *Deps) accountLogMetadata(r *http.Request, context *AuthContext, target *ResolvedPublicTarget, accountID string) map[string]any {
	return map[string]any{
		"sourceRefId":           context.SourceRefID,
		"sourceName":            context.SourceName,
		"tokenId":               context.TokenID,
		"tokenName":             context.TokenName,
		"tokenPrefix":           context.TokenPrefix,
		"targetSystemAccountId": target.SystemAccountID,
		"targetUsername":        target.Public.Username,
		"accountId":             accountID,
		"method":                r.Method,
		"path":                  r.URL.Path,
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func scalarText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return boolText(typed)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func marshalRawMessage(value map[string]any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}
