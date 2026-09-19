// external_opslog.go owns external integration source operation-log recording
// and the human-readable change formatting helpers.
package policyreads

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func (d *ExternalDeps) recordSourceOperation(r *http.Request, action, operationKey, sourceRefID, sourceName, summary string, changes []authsys.OperationLogChange) {
	if d.Sink == nil {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auth.SystemAccountID,
		ActorUsername:        auth.Username,
		ActorDisplayName:     auth.DisplayName,
		ActorRole:            auth.Role,
		Mode:                 "admin",
		Module:               "external_integration_sources",
		Action:               action,
		OperationKey:         operationKey,
		ResourceType:         "external_integration_source",
		ResourceID:           sourceRefID,
		ResourceName:         sourceName,
		Summary:              summary,
		Changes:              changes,
	}, r)
}

func formatExternalRateLimits(rules []ExternalRateLimitRule) string {
	if len(rules) == 0 {
		return "不限制"
	}
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strconv.Itoa(rule.WindowSeconds)+"s/"+strconv.Itoa(rule.MaxRequests)+"次")
	}
	return strings.Join(parts, ", ")
}

// externalSourceOperationChanges mirrors sourcePatchOperationChanges.
func externalSourceOperationChanges(changes []ExternalPatchChange) []authsys.OperationLogChange {
	out := make([]authsys.OperationLogChange, 0, len(changes))
	for _, change := range changes {
		switch change.Field {
		case "rateLimits":
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: "限频规则",
				Before: formatExternalRateLimits(asRateLimitRules(change.Before)),
				After:  formatExternalRateLimits(asRateLimitRules(change.After)),
			})
		case "scopes":
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: "接口资源授权",
				Before: formatScopes(change.Before), After: formatScopes(change.After),
			})
		default:
			label := "备注"
			switch change.Field {
			case "name":
				label = "名称"
			case "status":
				label = "状态"
			case "expiresAt":
				label = "到期时间"
			}
			out = append(out, authsys.OperationLogChange{
				Field: change.Field, Label: label,
				Before: safeChangeText(change.Before), After: safeChangeText(change.After),
			})
		}
	}
	return out
}

// externalTokenOperationChanges mirrors tokenPatchOperationChanges.
func externalTokenOperationChanges(changes []ExternalPatchChange) []authsys.OperationLogChange {
	out := make([]authsys.OperationLogChange, 0, len(changes))
	for _, change := range changes {
		field := "token" + strings.ToUpper(change.Field[:1]) + change.Field[1:]
		label := "Token 到期时间"
		switch change.Field {
		case "name":
			label = "Token 名称"
		case "status":
			label = "Token 状态"
		case "scopes":
			label = "Token 接口资源授权"
		}
		before := safeChangeText(change.Before)
		after := safeChangeText(change.After)
		if change.Field == "scopes" {
			before = formatScopes(change.Before)
			after = formatScopes(change.After)
		}
		out = append(out, authsys.OperationLogChange{Field: field, Label: label, Before: before, After: after})
	}
	return out
}

func asRateLimitRules(value any) []ExternalRateLimitRule {
	items, ok := value.([]ExternalRateLimitRule)
	if ok {
		return items
	}
	list, ok := value.([]any)
	if !ok {
		return []ExternalRateLimitRule{}
	}
	out := []ExternalRateLimitRule{}
	for _, item := range list {
		record, isObject := item.(map[string]any)
		if !isObject {
			continue
		}
		window, windowOK := record["windowSeconds"].(float64)
		maxRequests, maxOK := record["maxRequests"].(float64)
		if windowOK && maxOK {
			out = append(out, ExternalRateLimitRule{WindowSeconds: int(window), MaxRequests: int(maxRequests)})
		}
	}
	return out
}

func formatScopes(value any) string {
	items, ok := asStringList(value)
	if !ok {
		return ""
	}
	return strings.Join(items, ", ")
}
