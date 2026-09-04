package authz

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

const authorizationsProcessingTTL = 120 * time.Second

// Deps bundles the authz slice collaborators.
type Deps struct {
	Store *Store
	Sink  authsys.OperationLogSink
	Auth  *authsys.Deps
}

func (d *Deps) RequireAdmin(next http.Handler) http.Handler { return d.Auth.RequireAdmin(next) }

func (d *Deps) RequireSession(touch bool) func(http.Handler) http.Handler {
	return d.Auth.RequireSession(touch)
}

func parseIntOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

// accessFor resolves the request access scope: admin with an explicit
// ?systemAccountId= filter narrows; my-* requests force self.
type accessInfo struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

func (d *Deps) accessFor(r *http.Request, selfOnly bool) accessInfo {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return accessInfo{}
	}
	if !selfOnly && authsys.IsAdminRole(auth.Role) {
		filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		if filter == "all" {
			filter = ""
		}
		return accessInfo{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
	}
	return accessInfo{ViewerID: auth.SystemAccountID}
}

// Mount wires the authorizations route family onto both prefixes.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/my-authorizations", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, true)
	})))
	k.Register("GET "+prefix+"/my-authorizations/{id}", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, true)
	})))
	k.Register("DELETE "+prefix+"/my-authorizations/{id}/return", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.returnValue(w, r)
	})))
	k.Register("GET "+prefix+"/my-authorizations/{id}/usage", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.usage(w, r)
	})))

	k.Register("GET "+prefix+"/authorizations", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, false)
	})))
	k.Register("GET "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, false)
	})))
	k.Register("POST "+prefix+"/authorizations", d.RequireAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey:  "authorizations.create",
			SucceededTTL:  0,
			FailedTTL:     0,
			ProcessingTTL: authorizationsProcessingTTL,
			Scope: func(r *http.Request) (any, error) {
				return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
			},
			Fingerprint: func(r *http.Request) (any, error) {
				return map[string]any{
					"owner":         strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
					"resourceType":  kernel.TextField(kernel.BodyField(r, "resourceType")),
					"resourceId":    kernel.TextField(kernel.BodyField(r, "resourceId")),
					"granteeType":   kernel.TextField(kernel.BodyField(r, "granteeType")),
					"granteeId":     kernel.TextField(kernel.BodyField(r, "granteeId")),
					"targetGroupId": kernel.TextField(kernel.BodyField(r, "targetGroupId")),
					"remark":        kernel.TextField(kernel.BodyField(r, "remark")),
					"expiresAt":     kernel.TextField(kernel.BodyField(r, "expiresAt")),
					"limits":        kernel.BodyField(r, "limits"),
				}, nil
			},
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.create(w, r)
		})),
	))
	k.Register("DELETE "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.revoke(w, r)
	})))
	k.Register("PATCH "+prefix+"/authorizations/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, false)
	})))
	k.Register("PATCH "+prefix+"/authorizations/{id}/expire", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r, true)
	})))
}

// RequireAdminAuthz combines session + admin role (authsys.RequireAdmin).
func (d *Deps) RequireAdminAuthz(next http.Handler) http.Handler {
	return d.RequireAdmin(next)
}

// RequireSelf mirrors forceSelfAccessScope + session.
func (d *Deps) RequireSelf(next http.Handler) http.Handler {
	return d.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// my-* semantics: drop any systemAccountId query (self enforced).
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	access := d.accessFor(r, selfOnly)
	if !selfOnly && access.FilterID != "" {
		// Admin narrowing by ?systemAccountId: rows touching that account.
	}
	filters := Filters{
		ResourceType:                 r.URL.Query().Get("resourceType"),
		ResourceID:                   r.URL.Query().Get("resourceId"),
		ResourceOwnerSystemAccountID: r.URL.Query().Get("resourceOwnerSystemAccountId"),
		GranteeSystemAccountID:       r.URL.Query().Get("granteeSystemAccountId"),
		TeamID:                       r.URL.Query().Get("teamId"),
		Status:                       orDefault(r.URL.Query().Get("status"), "all"),
		SourceType:                   orDefault(r.URL.Query().Get("sourceType"), "all"),
		Keyword:                      r.URL.Query().Get("keyword"),
		IsAdmin:                      access.IsAdmin,
	}
	if selfOnly {
		filters.ViewerSystemAccountID = access.ViewerID
		if access.FilterID != "" {
			filters.ViewerSystemAccountID = access.FilterID
		}
	} else if access.FilterID != "" {
		// Admin scope filter: viewer acts as that account.
		filters.ViewerSystemAccountID = access.FilterID
	}
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("pageSize"), 50)
	items, total, hasMore, err := d.Store.ListPage(r.Context(), filters, page, pageSize)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": total, "hasMore": hasMore, "page": page, "pageSize": pageSize,
	}, "")
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	access := d.accessFor(r, selfOnly)
	summary, err := d.Store.Find(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if summary == nil {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	if selfOnly && !d.visibleTo(summary, access.ViewerID) {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	kernel.WriteOK(w, summary, "")
}

func (d *Deps) visibleTo(summary *Summary, viewerID string) bool {
	if summary.OwnerID == viewerID {
		return true
	}
	if summary.GranteeUserID != nil && *summary.GranteeUserID == viewerID {
		return true
	}
	return false
}

func (d *Deps) create(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	selfOnly := strings.HasSuffix(r.URL.Path, "/my-authorizations")
	scopeAccount := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if !selfOnly && authsys.IsAdminRole(auth.Role) && scopeAccount == "" {
		kernel.WriteBadRequest(w, "管理员新增授权时必须指定授权人")
		return
	}
	actor := scopeAccount
	if actor == "" || selfOnly {
		actor = auth.SystemAccountID
	}
	var body struct {
		ResourceType  *string        `json:"resourceType"`
		ResourceID    *string        `json:"resourceId"`
		GranteeType   *string        `json:"granteeType"`
		GranteeID     *string        `json:"granteeId"`
		TargetGroupID *string        `json:"targetGroupId"`
		Remark        *string        `json:"remark"`
		ExpiresAt     *string        `json:"expiresAt"`
		Limits        map[string]any `json:"limits"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if body.ResourceType == nil || body.ResourceID == nil || body.GranteeType == nil || body.GranteeID == nil ||
		strings.TrimSpace(*body.ResourceID) == "" || strings.TrimSpace(*body.GranteeID) == "" ||
		(body.ResourceType != nil && *body.ResourceType != "account" && *body.ResourceType != "group") ||
		(body.GranteeType != nil && *body.GranteeType != "system_account" && *body.GranteeType != "team") {
		kernel.WriteBadRequest(w, "授权参数不合法")
		return
	}
	// superRefine rules.
	if *body.ResourceType == "account" && *body.GranteeType == "system_account" &&
		(body.TargetGroupID == nil || strings.TrimSpace(*body.TargetGroupID) == "") {
		kernel.WriteBadRequest(w, "授权 AI 账户给个人时必须选择目标分组")
		return
	}
	if body.TargetGroupID != nil && strings.TrimSpace(*body.TargetGroupID) != "" &&
		!(*body.ResourceType == "account" && *body.GranteeType == "system_account") {
		kernel.WriteBadRequest(w, "只有授权 AI 账户给个人时可以指定目标分组")
		return
	}
	// Admin creating on behalf: the resource must belong to the scope account.
	input := CreateInput{
		ResourceType: strings.TrimSpace(*body.ResourceType),
		ResourceID:   strings.TrimSpace(*body.ResourceID),
		GranteeType:  strings.TrimSpace(*body.GranteeType),
		GranteeID:    strings.TrimSpace(*body.GranteeID),
		Remark:       body.Remark,
		ExpiresAt:    body.ExpiresAt,
	}
	if body.TargetGroupID != nil && strings.TrimSpace(*body.TargetGroupID) != "" {
		input.TargetGroupID = body.TargetGroupID
	}
	if body.Limits != nil {
		encoded, err := jsonMarshal(body.Limits)
		if err == nil {
			text := string(encoded)
			input.LimitsJSON = &text
		}
	}
	result, err := d.Store.Create(r.Context(), input, actor)
	if err != nil {
		var fail *Fail
		var conflict *Conflict
		if errorsAsFail(err, &fail) {
			kernel.WriteBadRequest(w, fail.Message)
			return
		}
		if errorsAsConflict(err, &conflict) {
			kernel.WriteError(w, http.StatusConflict, conflict.Error())
			return
		}
		kernel.WriteBadRequest(w, "创建授权失败")
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	if result.Created || result.PreviousStatus != nil {
		if d.Sink != nil {
			actionSummary := "创建资源授权："
			if result.PreviousStatus != nil {
				actionSummary = "重新激活资源授权："
			}
			summary := actionSummary + result.Item.ResourceID
			if auth != nil {
				d.Sink.Record(authsys.OperationLogEntry{
					ActorSystemAccountID:          auth.SystemAccountID,
					ActorUsername:                 auth.Username,
					ActorDisplayName:              auth.DisplayName,
					ActorRole:                     auth.Role,
					OperationScopeSystemAccountID: actor,
					Mode:                          "admin",
					Module:                        "authorizations",
					Action:                        "create",
					OperationKey:                  "authorizations.create",
					ResourceType:                  "authorization",
					ResourceID:                    result.Item.ID,
					Summary:                       summary,
					Changes: []authsys.OperationLogChange{
						{Field: "resourceType", Label: "资源类型", After: result.Item.ResourceType},
						{Field: "resourceId", Label: "授权资源", After: result.Item.ResourceID},
						{Field: "grantee", Label: "被授权目标", After: orText(result.Item.GranteeTeamID, orText(result.Item.GranteeUserID, ""))},
						{Field: "status", Label: "状态", After: result.Item.Status},
					},
				}, r)
			}
		}
	}
	data := map[string]any{"item": result.Item, "created": result.Created}
	if result.PreviousStatus != nil {
		data["previousStatus"] = *result.PreviousStatus
	}
	kernel.WriteJSON(w, status, map[string]any{"data": data})
}

func (d *Deps) revoke(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ExpectedUpdatedAt) == "" {
		kernel.WriteBadRequest(w, "回收授权参数不合法")
		return
	}
	mutation, err := d.Store.Revoke(r.Context(), r.PathValue("id"), body.ExpectedUpdatedAt, auth.SystemAccountID)
	if err != nil {
		kernel.WriteBadRequest(w, "回收授权失败")
		return
	}
	switch mutation.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{
			"message": "授权配置已被其他操作更新，请刷新后重试", "currentUpdatedAt": mutation.CurrentUpdatedAt,
		})
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			Mode: "admin", Module: "authorizations", Action: "revoke",
			OperationKey: "authorizations.revoke", ResourceType: "authorization",
			ResourceID: mutation.Result.ID,
			Summary:    "回收资源授权：" + mutation.Result.ResourceID,
			Changes: []authsys.OperationLogChange{
				{Field: "status", Label: "状态", Before: StatusActive, After: StatusRevoked},
			},
		}, r)
	}
	kernel.WriteOK(w, map[string]any{
		"id": mutation.Result.ID, "status": mutation.Result.Status, "updatedAt": mutation.Result.UpdatedAt,
	}, "")
}

func (d *Deps) returnValue(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ExpectedUpdatedAt) == "" {
		kernel.WriteBadRequest(w, "归还授权参数不合法")
		return
	}
	mutation, err := d.Store.Return(r.Context(), r.PathValue("id"), body.ExpectedUpdatedAt, auth.SystemAccountID)
	if err != nil {
		kernel.WriteBadRequest(w, "归还授权使用权失败")
		return
	}
	switch mutation.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{
			"message": "授权配置已被其他操作更新，请刷新后重试", "currentUpdatedAt": mutation.CurrentUpdatedAt,
		})
		return
	case "updated":
		if d.Sink != nil {
			d.Sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
				Mode: "self", Module: "authorizations", Action: "return",
				OperationKey: "authorizations.return", ResourceType: "authorization",
				ResourceID: mutation.Result.ID,
				Summary:    "归还授权使用权：" + mutation.Result.ResourceID,
				Changes: []authsys.OperationLogChange{
					{Field: "returned", Label: "归还授权", Before: "false", After: "true"},
				},
			}, r)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) patch(w http.ResponseWriter, r *http.Request, expireOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		ExpectedUpdatedAt string          `json:"expectedUpdatedAt"`
		Status            *string         `json:"status"`
		ExpiresAt         json.RawMessage `json:"expiresAt"`
		Limits            map[string]any  `json:"limits"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ExpectedUpdatedAt) == "" {
		kernel.WriteBadRequest(w, "修改授权参数不合法")
		return
	}
	expiresAtSet := body.ExpiresAt != nil
	var expiresAt *string
	if expiresAtSet && string(body.ExpiresAt) != "null" {
		var value string
		if err := json.Unmarshal(body.ExpiresAt, &value); err != nil {
			kernel.WriteBadRequest(w, "修改授权参数不合法")
			return
		}
		expiresAt = &value
	}
	if body.Status == nil && !expiresAtSet && body.Limits == nil {
		kernel.WriteBadRequest(w, "请提供要修改的授权内容")
		return
	}
	if expireOnly && body.Status != nil {
		kernel.WriteBadRequest(w, "修改授权参数不合法")
		return
	}
	input := PatchInput{Status: body.Status, ExpiresAt: expiresAt, ExpiresAtSet: expiresAtSet}
	if body.Limits != nil {
		encoded, err := jsonMarshal(body.Limits)
		if err == nil {
			text := string(encoded)
			input.LimitsJSON = &text
		}
	}
	access := d.accessFor(r, false)
	outcome, err := d.Store.PatchForOwner(r.Context(), r.PathValue("id"), input, body.ExpectedUpdatedAt,
		auth.SystemAccountID, access.FilterID)
	if err != nil {
		kernel.WriteBadRequest(w, "修改授权失败")
		return
	}
	switch outcome.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]any{
			"message": "授权配置已被其他操作更新，请刷新后重试",
		})
		return
	}
	action := "update"
	summary := "更新资源授权"
	if expireOnly {
		action = "update_expire"
		summary = "更新授权有效期"
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			Mode: "admin", Module: "authorizations", Action: action,
			OperationKey: "authorizations." + action, ResourceType: "authorization",
			ResourceID: outcome.Result.ID,
			Summary:    summary + "：" + outcome.Result.ResourceID,
		}, r)
	}
	kernel.WriteOK(w, map[string]any{
		"id": outcome.Result.ID, "status": outcome.Result.Status,
		"expiresAt": outcome.Result.ExpiresAt, "limits": body.Limits,
		"updatedAt": outcome.Result.UpdatedAt,
	}, "")
}

// usage is the J5-dependent read; the shape is served with usage omitted
// until the J5 stats slice wires the window loaders (documented deferral).
func (d *Deps) usage(w http.ResponseWriter, r *http.Request) {
	summary, err := d.Store.Find(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if summary == nil {
		kernel.WriteError(w, http.StatusNotFound, "授权记录不存在")
		return
	}
	kernel.WriteJSON(w, http.StatusOK, map[string]any{"data": summary})
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func orText(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

func errorsAsFail(err error, target **Fail) bool {
	if fail, ok := err.(*Fail); ok {
		*target = fail
		return true
	}
	return false
}

func errorsAsConflict(err error, target **Conflict) bool {
	if conflict, ok := err.(*Conflict); ok {
		*target = conflict
		return true
	}
	return false
}

// MountAuthz is the canonical mount entry used by gateway wiring.
func MountAuthz(k *kernel.Kernel, deps *Deps) { deps.Mount(k) }
