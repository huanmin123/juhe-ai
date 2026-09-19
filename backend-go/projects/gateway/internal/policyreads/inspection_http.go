// inspection_http.go owns the /response-inspection-policies route family:
// dependency bundle, route mounting, handlers and operation-log recording.
package policyreads

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// M16a route family (mounted behind requireAdmin, like the Node
// `app.use(prefix + "/response-inspection-policies", requireAdmin, router)`).
// ---------------------------------------------------------------------------

// InspectionDeps bundles the M16a collaborators.
type InspectionDeps struct {
	Store *InspectionStore
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the response-inspection-policies route family.
func (d *InspectionDeps) Mount(k *kernel.Kernel) {
	k.Register("GET "+inspectionPrefix, d.Auth.RequireAdmin(http.HandlerFunc(d.list)))
	k.Register("GET "+inspectionPrefix+"/provider-options", d.Auth.RequireAdmin(http.HandlerFunc(d.providerOptions)))
	k.Register("GET "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(http.HandlerFunc(d.detail)))
	k.Register("POST "+inspectionPrefix, d.Auth.RequireAdmin(d.guardedCreate()))
	k.Register("PATCH "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedPatch()))
	k.Register("DELETE "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedDelete()))
}

func (d *InspectionDeps) list(w http.ResponseWriter, r *http.Request) {
	result, err := d.Store.ListPage(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *InspectionDeps) providerOptions(w http.ResponseWriter, r *http.Request) {
	protocolCode, scopeType, keyword, message := parseInspectionProviderOptionsQuery(r.URL.Query())
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	options, err := d.Store.ProviderOptions(r.Context(), protocolCode, scopeType, keyword)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, options, "")
}

func (d *InspectionDeps) detail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

func (d *InspectionDeps) guardedCreate() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.create",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"payload": kernel.ParsedBody(r)}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseInspectionCreateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		policy, err := d.Store.Create(r.Context(), input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "响应检查策略创建失败"))
			return
		}
		d.recordPolicyOperation(r, "create", policy.ID, policy.Name, []authsys.OperationLogChange{
			{Field: "name", Label: "规则名称", After: policy.Name},
			{Field: "protocolCode", Label: "协议", After: policy.ProtocolCode},
			{Field: "scopeType", Label: "作用层级", After: policy.ScopeType},
			{Field: "providerCode", Label: "供应商", After: safeChangeText(policy.ProviderCode)},
			{Field: "enabled", Label: "启用状态", After: safeChangeText(policy.Enabled)},
			{Field: "priority", Label: "优先级", After: safeChangeText(policy.Priority)},
		})
		writeCreatedOK(w, overviewFromDetail(policy))
	}))
	return handler
}

func (d *InspectionDeps) guardedPatch() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.update",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"id": r.PathValue("id"), "payload": kernel.ParsedBody(r)}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		patch, message := parseInspectionPatchBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.Patch(r.Context(), id, patch)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "响应检查策略更新失败"))
			return
		}
		switch outcome.Status {
		case "not_found":
			kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
			return
		case "conflict":
			kernel.WriteError(w, http.StatusConflict, "响应检查策略已被其他操作更新，请刷新后重试")
			return
		case "updated":
			d.recordPolicyOperation(r, "update", outcome.Policy.ID, outcome.Policy.Name,
				inspectionOperationChanges(outcome.Current, outcome.Policy, outcome.ChangedFields))
		}
		kernel.WriteOK(w, overviewFromDetail(outcome.Policy), "")
	}))
	return handler
}

func (d *InspectionDeps) guardedDelete() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.delete",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"id": r.PathValue("id")}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		deleted, err := d.Store.Delete(r.Context(), id)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !deleted {
			kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
			return
		}
		d.recordPolicyOperation(r, "delete", id, id, []authsys.OperationLogChange{
			{Field: "deleted", Label: "删除", After: "true"},
		})
		kernel.WriteOK(w, map[string]any{"deleted": true}, "")
	}))
	return handler
}

func (d *InspectionDeps) recordPolicyOperation(r *http.Request, action, policyID, policyName string, changes []authsys.OperationLogChange) {
	if d.Sink == nil {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return
	}
	actionText := "删除"
	if action == "create" {
		actionText = "创建"
	} else if action == "update" {
		actionText = "更新"
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auth.SystemAccountID,
		ActorUsername:        auth.Username,
		ActorDisplayName:     auth.DisplayName,
		ActorRole:            auth.Role,
		Mode:                 "admin",
		Module:               "response_inspection_policies",
		Action:               action,
		OperationKey:         "response_inspection_policies." + action,
		ResourceType:         "response_inspection_policy",
		ResourceID:           policyID,
		ResourceName:         policyName,
		Summary:              actionText + "响应检查策略：" + policyName,
		Changes:              changes,
	}, r)
}

var inspectionChangeLabels = map[string]string{
	"name": "规则名称", "enabled": "启用状态", "priority": "优先级", "scopeType": "作用层级",
	"protocolCode": "协议", "providerCode": "供应商", "match": "匹配条件", "action": "处置动作", "notes": "备注",
}

// inspectionOperationChanges mirrors policyOperationChanges + safeChange.
func inspectionOperationChanges(current, policy *InspectionDetail, fields []string) []authsys.OperationLogChange {
	changes := make([]authsys.OperationLogChange, 0, len(fields))
	for _, field := range fields {
		label := inspectionChangeLabels[field]
		changes = append(changes, authsys.OperationLogChange{
			Field:  field,
			Label:  label,
			Before: inspectionFieldValue(field, current),
			After:  inspectionFieldValue(field, policy),
		})
	}
	return changes
}

func inspectionFieldValue(field string, detail *InspectionDetail) string {
	switch field {
	case "name":
		return safeChangeText(detail.Name)
	case "enabled":
		return safeChangeText(detail.Enabled)
	case "priority":
		return safeChangeText(detail.Priority)
	case "scopeType":
		return safeChangeText(detail.ScopeType)
	case "protocolCode":
		return safeChangeText(detail.ProtocolCode)
	case "providerCode":
		return safeChangeText(detail.ProviderCode)
	case "match":
		return safeChangeText(detail.Match)
	case "action":
		return safeChangeText(detail.Action)
	case "notes":
		return safeChangeText(detail.Notes)
	default:
		return ""
	}
}
