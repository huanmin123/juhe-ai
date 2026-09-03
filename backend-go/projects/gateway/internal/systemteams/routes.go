package systemteams

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the system-teams slice collaborators.
type Deps struct {
	Store *Store
	Sink  authsys.OperationLogSink
	Auth  *authsys.Deps
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

func scopeFor(r *http.Request, auth *authsys.AuthContext, selfOnly bool) AccessScope {
	if selfOnly || !authsys.IsAdminRole(auth.Role) {
		return AccessScope{ViewerID: auth.SystemAccountID}
	}
	filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if filter == "all" {
		filter = ""
	}
	return AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
}

// Mount wires both prefixes (my-teams self scope; system-teams admin).
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/my-teams", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, true)
	})))
	k.Register("GET "+prefix+"/my-teams/{id}", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, true)
	})))
	k.Register("GET "+prefix+"/my-teams/{id}/members", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.members(w, r, true)
	})))
	k.Register("GET "+prefix+"/my-teams/{id}/members/history", d.RequireSelf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.history(w, r, true)
	})))

	k.Register("GET "+prefix+"/system-teams", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.list(w, r, false)
	})))
	k.Register("GET "+prefix+"/system-teams/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.find(w, r, false)
	})))
	k.Register("GET "+prefix+"/system-teams/{id}/members", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.members(w, r, false)
	})))
	k.Register("GET "+prefix+"/system-teams/{id}/members/history", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.history(w, r, false)
	})))
	k.Register("POST "+prefix+"/system-teams", d.RequireAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "system_teams.create",
			Scope: func(r *http.Request) (any, error) {
				return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
			},
			Fingerprint: func(r *http.Request) (any, error) {
				return map[string]any{
					"owner":       strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
					"name":        kernel.TextField(kernel.BodyField(r, "name")),
					"description": kernel.TextField(kernel.BodyField(r, "description")),
					"status":      orText(kernel.TextField(kernel.BodyField(r, "status")), "active"),
				}, nil
			},
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.create(w, r)
		})),
	))
	k.Register("PATCH "+prefix+"/system-teams/{id}", d.RequireAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.patch(w, r)
	})))
	k.Register("POST "+prefix+"/system-teams/{id}/members", d.RequireAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "system_teams.add_members",
			Scope: func(r *http.Request) (any, error) {
				return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
			},
			Fingerprint: func(r *http.Request) (any, error) {
				return map[string]any{
					"teamId":            r.PathValue("id"),
					"memberIds":         kernel.SortedTextValues(kernel.BodyField(r, "systemAccountIds")),
					"expectedUpdatedAt": kernel.TextField(kernel.BodyField(r, "expectedUpdatedAt")),
				}, nil
			},
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.addMembers(w, r)
		})),
	))
	k.Register("DELETE "+prefix+"/system-teams/{id}/members/{memberId}", d.RequireAdmin(
		kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
			OperationKey: "system_teams.remove_member",
			Scope: func(r *http.Request) (any, error) {
				return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
			},
			Fingerprint: func(r *http.Request) (any, error) {
				return map[string]any{
					"teamId":            r.PathValue("id"),
					"memberId":          r.PathValue("memberId"),
					"expectedUpdatedAt": kernel.TextField(kernel.BodyField(r, "expectedUpdatedAt")),
				}, nil
			},
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.removeMember(w, r)
		})),
	))
}

// RequireAdminAuthz combines session + admin (delegated to authsys).
func (d *Deps) RequireAdminAuthz(next http.Handler) http.Handler {
	return d.Auth.RequireAdmin(next)
}

// RequireAdmin delegates to authsys (alias used by guard mounts).
func (d *Deps) RequireAdmin(next http.Handler) http.Handler {
	return d.Auth.RequireAdmin(next)
}

func orText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// RequireSelf wraps session + self enforcement.
func (d *Deps) RequireSelf(next http.Handler) http.Handler {
	return d.Auth.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := authsys.AuthContextFrom(r); auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (d *Deps) list(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	access := scopeFor(r, auth, selfOnly)
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("pageSize"), 20)
	items, hasMore, err := d.Store.ListPage(r.Context(), access, page, pageSize, strings.TrimSpace(r.URL.Query().Get("keyword")))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": total, "hasMore": hasMore, "page": page, "pageSize": pageSize,
	}, "")
}

func (d *Deps) find(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"), scopeFor(r, auth, selfOnly))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "团队不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

func (d *Deps) members(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"), scopeFor(r, auth, selfOnly))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "团队不存在")
		return
	}
	kernel.WriteOK(w, map[string]any{
		"items": detail.Members, "total": len(detail.Members),
		"hasMore": false, "page": 1, "pageSize": len(detail.Members),
	}, "")
}

func (d *Deps) history(w http.ResponseWriter, r *http.Request, selfOnly bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("pageSize"), 20)
	entries, hasMore, err := d.Store.ListHistory(r.Context(), r.PathValue("id"), scopeFor(r, auth, selfOnly), page, pageSize)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if entries == nil {
		kernel.WriteError(w, http.StatusNotFound, "团队不存在")
		return
	}
	total := (page-1)*pageSize + len(entries)
	if hasMore {
		total++
	}
	kernel.WriteOK(w, map[string]any{
		"items": entries, "total": total, "hasMore": hasMore, "page": page, "pageSize": pageSize,
	}, "")
}

func (d *Deps) create(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	item, err := d.Store.Create(r.Context(), valueOr(body.Name), body.Description, body.Status, auth.SystemAccountID)
	if err != nil {
		var validation *ValidationError
		if errorsAs(err, &validation) {
			kernel.WriteBadRequest(w, validation.Message)
			return
		}
		kernel.WriteBadRequest(w, "创建团队失败")
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorUsername: auth.Username,
			ActorDisplayName: auth.DisplayName, ActorRole: auth.Role,
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "system_teams", Action: "create", OperationKey: "system_teams.create",
			ResourceType: "system_team", ResourceID: item.ID, ResourceName: item.Name,
			Summary: "创建系统团队：" + item.Name,
			Changes: []authsys.OperationLogChange{
				{Field: "name", Label: "团队名称", After: item.Name},
				{Field: "status", Label: "状态", After: item.Status},
			},
		}, r)
	}
	w.WriteHeader(http.StatusCreated)
	kernel.WriteOK(w, item, "")
}

func (d *Deps) patch(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		Name              *string `json:"name"`
		Description       *string `json:"description"`
		Status            *string `json:"status"`
		ExpectedUpdatedAt string  `json:"expectedUpdatedAt"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ExpectedUpdatedAt) == "" {
		kernel.WriteBadRequest(w, "团队参数不合法")
		return
	}
	if body.Name == nil && body.Description == nil && body.Status == nil {
		kernel.WriteBadRequest(w, "请至少提交一个团队变更字段")
		return
	}
	outcome, err := d.Store.Patch(r.Context(), r.PathValue("id"), PatchInput{
		Name: body.Name, Description: body.Description, Status: body.Status,
		ExpectedUpdatedAt: body.ExpectedUpdatedAt,
	}, scopeFor(r, auth, false))
	if err != nil {
		var validation *ValidationError
		if errorsAs(err, &validation) {
			kernel.WriteBadRequest(w, validation.Message)
			return
		}
		kernel.WriteBadRequest(w, "更新团队失败")
		return
	}
	switch outcome.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "团队不存在")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": "团队已被其他操作更新，请刷新后重试"})
		return
	}
	if d.Sink != nil && outcome.Status == "updated" {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			Mode: "admin", Module: "system_teams", Action: "update",
			OperationKey: "system_teams.update", ResourceType: "system_team",
			ResourceID: r.PathValue("id"),
			Summary:    "更新系统团队",
		}, r)
	}
	kernel.WriteOK(w, map[string]any{"status": outcome.Status}, "")
}

func (d *Deps) addMembers(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		SystemAccountIDs  []string `json:"systemAccountIds"`
		ExpectedUpdatedAt string   `json:"expectedUpdatedAt"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if len(body.SystemAccountIDs) < 1 || strings.TrimSpace(body.ExpectedUpdatedAt) == "" {
		kernel.WriteBadRequest(w, "团队成员参数不合法")
		return
	}
	outcome, added, err := d.Store.AddMembers(r.Context(), r.PathValue("id"), body.SystemAccountIDs,
		body.ExpectedUpdatedAt, scopeFor(r, auth, false))
	if err != nil {
		var validation *ValidationError
		if errorsAs(err, &validation) {
			kernel.WriteBadRequest(w, validation.Message)
			return
		}
		kernel.WriteBadRequest(w, "添加团队成员失败")
		return
	}
	switch outcome.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "团队不存在或已停用")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": "团队已被其他操作更新，请刷新后重试"})
		return
	}
	if d.Sink != nil && outcome.Status == "updated" && added != nil {
		names := []string{}
		for _, member := range *added {
			names = append(names, member.DisplayName)
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			Mode: "admin", Module: "system_teams", Action: "add_members",
			OperationKey: "system_teams.add_members", ResourceType: "system_team",
			ResourceID: r.PathValue("id"), ResourceName: "团队",
			Summary: "添加团队成员", Changes: []authsys.OperationLogChange{
				{Field: "members", Label: "新增成员", After: strings.Join(names, "、")},
			},
		}, r)
	}
	kernel.WriteOK(w, map[string]any{"id": r.PathValue("id"), "addedMembers": added, "status": outcome.Status}, "")
}

func (d *Deps) removeMember(w http.ResponseWriter, r *http.Request) {
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
		kernel.WriteBadRequest(w, "团队成员参数不合法")
		return
	}
	outcome, change, err := d.Store.RemoveMember(r.Context(), r.PathValue("id"), r.PathValue("memberId"),
		body.ExpectedUpdatedAt, scopeFor(r, auth, false))
	if err != nil {
		kernel.WriteBadRequest(w, "移除团队成员失败")
		return
	}
	switch outcome.Status {
	case "not_found":
		kernel.WriteError(w, http.StatusNotFound, "团队成员不存在")
		return
	case "conflict":
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": "团队已被其他操作更新，请刷新后重试"})
		return
	}
	if d.Sink != nil && change != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			Mode: "admin", Module: "system_teams", Action: "remove_member",
			OperationKey: "system_teams.remove_member", ResourceType: "system_team",
			ResourceID: r.PathValue("id"),
			Summary:    "移除团队成员",
			Changes: []authsys.OperationLogChange{
				{Field: "member", Label: "移除成员", Before: change.TargetName},
			},
		}, r)
	}
	kernel.WriteOK(w, map[string]any{"id": r.PathValue("id"), "status": outcome.Status}, "")
}

func valueOr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func errorsAs(err error, target **ValidationError) bool {
	if validation, ok := err.(*ValidationError); ok {
		*target = validation
		return true
	}
	return false
}
