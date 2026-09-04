package announcements

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Mount wires the announcement route family. Public routes run unauthed;
// admin routes require requireAdmin. Mirrors announcements.routes.ts.
func Mount(k *kernel.Kernel, deps *authsys.Deps, store *Store, sink authsys.OperationLogSink) {
	prefix := "/__aisys__/api"
	// Public surface (self scope via the caller's session).
	k.Register("GET "+prefix+"/my-announcements", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		limit := parseIntOr(r.URL.Query().Get("limit"), 0)
		items, err := store.ListPublic(r.Context(), auth.SystemAccountID, limit)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, items, "")
	})))
	k.Register("POST "+prefix+"/my-announcements/read", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var body struct {
			AnnouncementIDs []string `json:"announcementIds"`
		}
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if len(body.AnnouncementIDs) > 30 {
			kernel.WriteBadRequest(w, "公告已读参数无效")
			return
		}
		result, err := store.MarkRead(r.Context(), auth.SystemAccountID, body.AnnouncementIDs)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
	})))

	// Admin surface.
	k.Register("GET "+prefix+"/announcements", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := parseIntOr(r.URL.Query().Get("page"), 1)
		pageSize := parseIntOr(r.URL.Query().Get("pageSize"), 20)
		items, hasMore, err := store.ListPage(r.Context(), page, pageSize)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		total := page * pageSize
		if !hasMore {
			total = (page-1)*pageSize + len(items)
		}
		kernel.WriteOK(w, map[string]any{
			"items": items, "total": total, "hasMore": hasMore, "page": page, "pageSize": pageSize,
		}, "")
	})))
	k.Register("GET "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detail, err := store.FindEditDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if detail == nil {
			kernel.WriteError(w, http.StatusNotFound, "公告不存在")
			return
		}
		kernel.WriteOK(w, detail, "")
	})))
	k.Register("POST "+prefix+"/announcements", mountGuardedCreate(deps, store, sink))
	k.Register("PATCH "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementPatch(w, r, store, sink, "update")
	})))
	k.Register("POST "+prefix+"/announcements/{id}/publish", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementPatch(w, r, store, sink, "publish")
	})))
	k.Register("POST "+prefix+"/announcements/{id}/unpublish", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementPatch(w, r, store, sink, "unpublish")
	})))
	k.Register("DELETE "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementDelete(w, r, store, sink)
	})))
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

func trimPtr(v string) *string { return &v }

func announcementPatch(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink, action string) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	expected, _ := body["expectedRevision"].(string)
	if strings.TrimSpace(expected) == "" {
		kernel.WriteBadRequest(w, "公告版本参数无效")
		return
	}
	input := MutationInput{}
	if value, exists := body["title"]; exists {
		if text, isString := value.(string); isString {
			input.Title = trimPtr(text)
		}
	}
	if value, exists := body["content"]; exists {
		if text, isString := value.(string); isString {
			input.Content = trimPtr(text)
		}
	}
	if value, exists := body["level"]; exists {
		if text, isString := value.(string); isString {
			input.Level = trimPtr(text)
		}
	}
	if value, exists := body["status"]; exists {
		if text, isString := value.(string); isString {
			input.Status = trimPtr(text)
		}
	}

	id := r.PathValue("id")
	var receipt *MutationReceipt
	var err error
	switch action {
	case "publish":
		input = MutationInput{Status: trimPtr("published")}
	case "unpublish":
		input = MutationInput{Status: trimPtr("archived")}
	}
	receipt, err = store.Patch(r.Context(), id, input, expected, auth.SystemAccountID)
	if receipt == nil && err == nil {
		kernel.WriteError(w, http.StatusNotFound, "公告不存在")
		return
	}
	if err != nil {
		var conflict *ConflictError
		var validation *ValidationError
		if errors.As(err, &conflict) {
			writeConflict(w, conflict)
			return
		}
		if errors.As(err, &validation) {
			kernel.WriteError(w, http.StatusConflict, validation.Message)
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "announcements", Action: action, OperationKey: "announcements." + action,
			ResourceType: "announcement", ResourceID: receipt.ID,
			Summary: summaryFor(action, receipt),
		}, r)
	}
	kernel.WriteOK(w, receipt, "")
}

func announcementDelete(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		ExpectedRevision string `json:"expectedRevision"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ExpectedRevision) == "" {
		kernel.WriteBadRequest(w, "公告版本参数无效")
		return
	}
	receipt, err := store.Delete(r.Context(), r.PathValue("id"), body.ExpectedRevision)
	if receipt == nil && err == nil {
		kernel.WriteError(w, http.StatusNotFound, "公告不存在")
		return
	}
	if err != nil {
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			writeConflict(w, conflict)
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "announcements", Action: "delete", OperationKey: "announcements.delete",
			ResourceType: "announcement", ResourceID: receipt.ID,
			Summary: "删除公告",
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeConflict(w http.ResponseWriter, conflict *ConflictError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	body := `{"message":` + jsonString(conflict.Message) + `,"currentRevision":` + jsonString(conflict.CurrentRevision) + `}`
	_, _ = w.Write([]byte(body))
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func summaryFor(action string, receipt *MutationReceipt) string {
	switch action {
	case "create":
		return "创建公告"
	case "publish":
		return "发布公告"
	case "unpublish":
		return "下线公告"
	default:
		return "更新公告"
	}
}

// mountGuardedCreate mounts the guarded create route.
func mountGuardedCreate(d *authsys.Deps, store *Store, sink authsys.OperationLogSink) http.Handler {
	return d.RequireAdmin(kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "announcements.create",
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"title":   kernel.TextField(kernel.BodyField(r, "title")),
				"content": kernel.HashStableValue(kernel.BodyField(r, "content")),
				"level":   kernel.TextField(kernel.BodyField(r, "level")),
				"status":  kernel.TextField(kernel.BodyField(r, "status")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var body struct {
			Title   *string `json:"title"`
			Content *string `json:"content"`
			Level   *string `json:"level"`
			Status  *string `json:"status"`
		}
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body.Title == nil || body.Content == nil ||
			strings.TrimSpace(*body.Title) == "" || strings.TrimSpace(*body.Content) == "" ||
			len(*body.Title) > 120 || len(*body.Content) > 5000 {
			kernel.WriteBadRequest(w, "公告参数无效")
			return
		}
		receipt, err := store.Create(r.Context(), MutationInput{
			Title: body.Title, Content: body.Content, Level: body.Level, Status: body.Status,
		}, auth.SystemAccountID)
		if err != nil {
			var validation *ValidationError
			if errors.As(err, &validation) {
				kernel.WriteError(w, http.StatusConflict, validation.Message)
				return
			}
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if sink != nil {
			sink.Record(authsys.OperationLogEntry{
				OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
				Module: "announcements", Action: "create", OperationKey: "announcements.create",
				ResourceType: "announcement", ResourceID: receipt.ID,
				ResourceName: strings.TrimSpace(*body.Title),
				Summary:      "创建公告：" + strings.TrimSpace(*body.Title),

				Changes: []authsys.OperationLogChange{
					{Field: "title", Label: "标题", After: strings.TrimSpace(*body.Title)},
					{Field: "level", Label: "级别", After: valueOrText(body.Level, "info")},
					{Field: "status", Label: "状态", After: valueOrText(body.Status, "draft")},
				},
			}, r)
		}
		w.WriteHeader(http.StatusCreated)
		kernel.WriteOK(w, receipt, "")
	})))
}

func valueOrText(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}
