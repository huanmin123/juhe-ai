// external_http_write.go owns the guarded mutation handlers for external
// integration sources (source create/patch/delete, token create/patch and the
// built-in test token reset) plus their error mapping.
package policyreads

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func (d *ExternalDeps) guardedResetBuiltInTestToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.reset_builtin_test_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(*http.Request) (any, error) {
			return map[string]any{"target": "built_in_test_token"}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := d.Store.ResetBuiltInTestToken(r.Context())
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "重置内置测试 Token 失败"))
			return
		}
		sourceName := "内置测试 Token"
		if source, findErr := d.Store.FindSource(r.Context(), builtInExternalTestSourceID); findErr == nil && source != nil {
			sourceName = source.Name
		}
		d.recordSourceOperation(r, "reset_builtin_test_token", "external_integration_sources.reset_builtin_test_token",
			builtInExternalTestSourceID, sourceName, "重置内置测试 Token", []authsys.OperationLogChange{{
				Field: "tokenPreview", Label: "Token 标识",
				After: token.TokenPrefix + "..." + token.TokenSuffix,
			}})
		setNoStoreHeaders(w)
		kernel.WriteOK(w, map[string]any{"token": token}, "")
	}))
}

func (d *ExternalDeps) guardedCreate() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.create",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"name":       kernel.BodyField(r, "name"),
				"status":     kernel.BodyField(r, "status"),
				"scopes":     kernel.BodyField(r, "scopes"),
				"rateLimits": kernel.BodyField(r, "rateLimits"),
				"expiresAt":  kernel.BodyField(r, "expiresAt"),
				"notes":      kernel.BodyField(r, "notes"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalSourceBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		source, token, err := d.Store.CreateAuthorization(r.Context(), input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "来源系统创建失败"))
			return
		}
		d.recordSourceOperation(r, "create", "external_integration_sources.create",
			source.ID, source.Name, "创建外部来源系统："+source.Name, []authsys.OperationLogChange{
				{Field: "name", Label: "名称", After: source.Name},
				{Field: "status", Label: "状态", After: source.Status},
				{Field: "expiresAt", Label: "到期时间", After: safeChangeText(source.ExpiresAt)},
				{Field: "rateLimits", Label: "限频规则", After: formatExternalRateLimits(source.RateLimits)},
			})
		setNoStoreHeaders(w)
		writeCreatedOK(w, map[string]any{
			"item":  externalCreatedListItem(source, token),
			"token": token,
		})
	}))
}

func externalCreatedListItem(source *ExternalSourceRecord, token *CreatedExternalToken) ExternalSourceListItem {
	return ExternalSourceListItem{
		ID: source.ID, Name: source.Name, Status: source.Status, Scopes: source.Scopes,
		RateLimits: source.RateLimits, ExpiresAt: source.ExpiresAt, Notes: source.Notes,
		LastUsedAt: source.LastUsedAt, UpdatedAt: source.UpdatedAt,
		PrimaryToken: &ExternalPrimaryToken{ID: token.ID, TokenPrefix: token.TokenPrefix, TokenSuffix: token.TokenSuffix},
		IsBuiltIn:    source.IsBuiltIn,
	}
}

func (d *ExternalDeps) guardedPatchSource() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.update",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"name":              kernel.BodyField(r, "name"),
				"status":            kernel.BodyField(r, "status"),
				"scopes":            kernel.BodyField(r, "scopes"),
				"expiresAt":         kernel.BodyField(r, "expiresAt"),
				"rateLimits":        kernel.BodyField(r, "rateLimits"),
				"notes":             kernel.BodyField(r, "notes"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalSourceUpdateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.UpdateSource(r.Context(), id, input)
		if err != nil {
			d.writeSourceMutationError(w, err)
			return
		}
		if outcome == nil {
			kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
			return
		}
		if len(outcome.Changes) > 0 {
			d.recordSourceOperation(r, "update", "external_integration_sources.update",
				outcome.Mutation.ID, outcome.SourceName, "更新外部来源系统："+outcome.SourceName,
				externalSourceOperationChanges(outcome.Changes))
		}
		kernel.WriteOK(w, outcome.Mutation, "")
	}))
}

func (d *ExternalDeps) guardedDeleteSource() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.delete",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		expected, message := parseExternalDeleteBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		receipt, err := d.Store.DeleteSource(r.Context(), id, expected)
		if err != nil {
			d.writeSourceMutationError(w, err, "删除来源授权失败")
			return
		}
		if receipt == nil {
			kernel.WriteError(w, http.StatusNotFound, "来源系统不存在")
			return
		}
		d.recordSourceOperation(r, "delete", "external_integration_sources.delete",
			receipt.ID, receipt.Name, "删除外部来源系统："+receipt.Name, []authsys.OperationLogChange{
				{Field: "deleted", Label: "删除状态", Before: "false", After: "true"},
			})
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (d *ExternalDeps) guardedCreateToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.create_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":        r.PathValue("id"),
				"name":      kernel.BodyField(r, "name"),
				"status":    kernel.BodyField(r, "status"),
				"scopes":    kernel.BodyField(r, "scopes"),
				"expiresAt": kernel.BodyField(r, "expiresAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalTokenBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		token, err := d.Store.CreateToken(r.Context(), id, input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "Token 创建失败"))
			return
		}
		sourceName := id
		if source, findErr := d.Store.FindSource(r.Context(), id); findErr == nil && source != nil {
			sourceName = source.Name
		}
		d.recordSourceOperation(r, "create_token", "external_integration_sources.create_token",
			id, sourceName, "生成外部来源系统 Token："+sourceName, []authsys.OperationLogChange{
				{Field: "tokenName", Label: "Token 名称", After: token.Name},
				{Field: "tokenPreview", Label: "Token 标识", After: token.TokenPrefix + "..." + token.TokenSuffix},
				{Field: "expiresAt", Label: "到期时间", After: safeChangeText(token.ExpiresAt)},
			})
		setNoStoreHeaders(w)
		writeCreatedOK(w, map[string]any{"token": token})
	}))
}

func (d *ExternalDeps) guardedPatchToken() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "external_integration_sources.update_token",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"id":                r.PathValue("id"),
				"tokenId":           r.PathValue("tokenId"),
				"name":              kernel.BodyField(r, "name"),
				"status":            kernel.BodyField(r, "status"),
				"scopes":            kernel.BodyField(r, "scopes"),
				"expiresAt":         kernel.BodyField(r, "expiresAt"),
				"expectedUpdatedAt": kernel.BodyField(r, "expectedUpdatedAt"),
			}, nil
		},
	})
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		tokenID := strings.TrimSpace(r.PathValue("tokenId"))
		if id == "" {
			kernel.WriteBadRequest(w, "来源系统不存在")
			return
		}
		if tokenID == "" {
			kernel.WriteBadRequest(w, "Token 不存在")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseExternalTokenUpdateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.UpdateToken(r.Context(), id, tokenID, input)
		if err != nil {
			d.writeSourceMutationError(w, err)
			return
		}
		if outcome == nil {
			kernel.WriteError(w, http.StatusNotFound, "Token 不存在")
			return
		}
		if len(outcome.Changes) > 0 {
			d.recordSourceOperation(r, "update_token", "external_integration_sources.update_token",
				id, outcome.SourceName, "更新外部来源系统 Token："+outcome.TokenName,
				externalTokenOperationChanges(outcome.Changes))
		}
		kernel.WriteOK(w, outcome.Mutation, "")
	}))
}

// writeSourceMutationError maps store errors onto the Node contract:
// conflicts → 409, everything else → 400.
func (d *ExternalDeps) writeSourceMutationError(w http.ResponseWriter, err error, fallback ...string) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	message := "来源系统更新失败"
	if len(fallback) > 0 && fallback[0] != "" {
		message = fallback[0]
	}
	kernel.WriteBadRequest(w, storeErrorMessage(err, message))
}
