package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type announcementPublicChangePublisher interface {
	PublishAnnouncementPublicChange(ctx context.Context, announcementID, operation string, fieldMask []string) error
}

type announcementManagementWriteOptions struct {
	operationLogs managementOperationLogOptions
	pageData      announcementPublicChangePublisher
	logger        announcementWriteLogger
	reader        announcementManagementService
}

type announcementWriteLogger interface {
	Warn(msg string, args ...any)
}

type announcementManagementWriteService interface {
	Create(ctx context.Context, input announcements.CreateInput) (port.Announcement, error)
	Update(ctx context.Context, input announcements.UpdateInput) (port.Announcement, error)
	Publish(ctx context.Context, input announcements.ActionInput) (port.Announcement, error)
	Unpublish(ctx context.Context, input announcements.ActionInput) (port.Announcement, error)
	Delete(ctx context.Context, input announcements.ActionInput) (port.Announcement, error)
}

func NewAnnouncementManagementHandlerWithOptions(
	service *announcements.Service,
	operationLogs ManagementOperationLogOptions,
	pageData announcementPublicChangePublisher,
	logger announcementWriteLogger,
) http.Handler {
	read := newAnnouncementManagementHandler(announcementManagementServiceAdapter{service: service})
	write := newAnnouncementManagementWriteHandler(
		announcementManagementWriteServiceAdapter{service: service},
		announcementManagementWriteOptions{
			operationLogs: newManagementOperationLogOptions(operationLogs),
			pageData:      pageData,
			logger:        logger,
			reader:        announcementManagementServiceAdapter{service: service},
		},
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			read.ServeHTTP(w, r)
			return
		}
		write.ServeHTTP(w, r)
	})
}

type announcementManagementWriteServiceAdapter struct{ service *announcements.Service }

func (s announcementManagementWriteServiceAdapter) Create(ctx context.Context, input announcements.CreateInput) (port.Announcement, error) {
	return s.service.Create(ctx, input)
}
func (s announcementManagementWriteServiceAdapter) Update(ctx context.Context, input announcements.UpdateInput) (port.Announcement, error) {
	return s.service.Update(ctx, input)
}
func (s announcementManagementWriteServiceAdapter) Publish(ctx context.Context, input announcements.ActionInput) (port.Announcement, error) {
	return s.service.Publish(ctx, input)
}
func (s announcementManagementWriteServiceAdapter) Unpublish(ctx context.Context, input announcements.ActionInput) (port.Announcement, error) {
	return s.service.Unpublish(ctx, input)
}
func (s announcementManagementWriteServiceAdapter) Delete(ctx context.Context, input announcements.ActionInput) (port.Announcement, error) {
	return s.service.Delete(ctx, input)
}

func newAnnouncementManagementWriteHandler(service announcementManagementWriteService, opts announcementManagementWriteOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		id := strings.TrimSpace(chi.URLParam(r, "id"))
		var err error
		switch {
		case r.Method == http.MethodPost && id == "":
			err = handleAnnouncementCreate(w, r, auth, service, opts)
		case r.Method == http.MethodPatch && id != "":
			err = handleAnnouncementUpdate(w, r, auth, id, service, opts)
		case r.Method == http.MethodPost && id != "" && strings.HasSuffix(r.URL.Path, "/publish"):
			err = handleAnnouncementStatus(w, r, auth, id, true, service, opts)
		case r.Method == http.MethodPost && id != "" && strings.HasSuffix(r.URL.Path, "/unpublish"):
			err = handleAnnouncementStatus(w, r, auth, id, false, service, opts)
		case r.Method == http.MethodDelete && id != "":
			err = handleAnnouncementDelete(w, r, auth, id, service, opts)
		default:
			writeMessageError(w, http.StatusNotFound, "接口不存在")
		}
		if err != nil && opts.logger != nil {
			opts.logger.Warn("公告管理写入处理失败", "error", err, "path", r.URL.Path)
		}
	})
}

type announcementCreateBody struct {
	Title   announcementOptionalString `json:"title"`
	Content announcementOptionalString `json:"content"`
	Level   announcementOptionalString `json:"level"`
	Status  announcementOptionalString `json:"status"`
}

type announcementUpdateBody struct {
	Title   announcementOptionalString `json:"title"`
	Content announcementOptionalString `json:"content"`
	Level   announcementOptionalString `json:"level"`
	Status  announcementOptionalString `json:"status"`
}

type announcementOptionalString struct {
	set   bool
	value string
}

func (s *announcementOptionalString) UnmarshalJSON(data []byte) error {
	s.set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("announcement field cannot be null")
	}
	return json.Unmarshal(data, &s.value)
}

func (s announcementOptionalString) pointer() *string {
	if !s.set {
		return nil
	}
	value := s.value
	return &value
}

func handleAnnouncementCreate(w http.ResponseWriter, r *http.Request, auth managementauth.Context, service announcementManagementWriteService, opts announcementManagementWriteOptions) error {
	var body announcementCreateBody
	if !decodeAnnouncementJSON(w, r, &body) {
		return errors.New("invalid announcement body")
	}
	if !body.Title.set || !body.Content.set || body.Level.set && body.Level.value == "" || body.Status.set && body.Status.value == "" {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return errors.New("invalid announcement body")
	}
	result, err := service.Create(r.Context(), announcements.CreateInput{
		ID: newAnnouncementID(), Title: body.Title.value, Content: body.Content.value, Level: body.Level.value, Status: body.Status.value, ActorID: auth.SystemAccountID,
	})
	if err != nil {
		writeAnnouncementWriteError(w, err)
		return err
	}
	recordAnnouncementOperationLog(r, auth, result, "create", nil, opts.operationLogs)
	if result.Status == "published" {
		publishAnnouncementChange(r, opts, result.ID, "upsert", []string{"title", "content", "level", "status", "publishedAt"})
	}
	writeData(w, http.StatusCreated, enrichAnnouncementWriteResult(r, opts.reader, result))
	return nil
}

func handleAnnouncementUpdate(w http.ResponseWriter, r *http.Request, auth managementauth.Context, id string, service announcementManagementWriteService, opts announcementManagementWriteOptions) error {
	var body announcementUpdateBody
	if !decodeAnnouncementJSON(w, r, &body) {
		return errors.New("invalid announcement body")
	}
	before, found, err := opts.reader.FindManagement(r.Context(), id)
	if err != nil || !found {
		if err == nil {
			writeMessageError(w, http.StatusNotFound, "公告不存在")
		} else {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		}
		return err
	}
	result, err := service.Update(r.Context(), announcements.UpdateInput{ID: id, Title: body.Title.pointer(), Content: body.Content.pointer(), Level: body.Level.pointer(), Status: body.Status.pointer(), ActorID: auth.SystemAccountID})
	if err != nil {
		writeAnnouncementWriteError(w, err)
		return err
	}
	recordAnnouncementOperationLog(r, auth, result, "update", &before, opts.operationLogs)
	if result.Status == "published" || before.Status == "published" {
		op := "delete"
		if result.Status == "published" {
			op = "upsert"
		}
		publishAnnouncementChange(r, opts, result.ID, op, []string{"title", "content", "level", "status", "publishedAt"})
	}
	writeData(w, http.StatusOK, enrichAnnouncementWriteResult(r, opts.reader, result))
	return nil
}

func handleAnnouncementStatus(w http.ResponseWriter, r *http.Request, auth managementauth.Context, id string, publish bool, service announcementManagementWriteService, opts announcementManagementWriteOptions) error {
	before, found, err := opts.reader.FindManagement(r.Context(), id)
	if err != nil || !found {
		if err == nil {
			writeMessageError(w, http.StatusNotFound, "公告不存在")
		} else {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		}
		return err
	}
	input := announcements.ActionInput{ID: id, ActorID: auth.SystemAccountID}
	var result port.Announcement
	if publish {
		result, err = service.Publish(r.Context(), input)
	} else {
		result, err = service.Unpublish(r.Context(), input)
	}
	if err != nil {
		writeAnnouncementWriteError(w, err)
		return err
	}
	action := "unpublish"
	operation := "delete"
	mask := []string{"status"}
	if publish {
		action, operation, mask = "publish", "upsert", []string{"status", "publishedAt"}
	}
	recordAnnouncementOperationLog(r, auth, result, action, &before, opts.operationLogs)
	publishAnnouncementChange(r, opts, result.ID, operation, mask)
	writeData(w, http.StatusOK, enrichAnnouncementWriteResult(r, opts.reader, result))
	return nil
}

func handleAnnouncementDelete(w http.ResponseWriter, r *http.Request, auth managementauth.Context, id string, service announcementManagementWriteService, opts announcementManagementWriteOptions) error {
	before, err := service.Delete(r.Context(), announcements.ActionInput{ID: id, ActorID: auth.SystemAccountID})
	if err != nil {
		writeAnnouncementWriteError(w, err)
		return err
	}
	recordAnnouncementOperationLog(r, auth, port.Announcement{ID: id, Title: before.Title, Status: before.Status}, "delete", &before, opts.operationLogs)
	if before.Status == "published" {
		publishAnnouncementChange(r, opts, id, "delete", nil)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func decodeAnnouncementJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	limited := http.MaxBytesReader(w, r.Body, 256<<10)
	defer limited.Close()
	raw, err := io.ReadAll(limited)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return false
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return false
	}
	strict := json.NewDecoder(bytes.NewReader(normalized))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
		return false
	}
	return true
}

func writeAnnouncementWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, announcements.ErrAnnouncementInputInvalid):
		writeMessageError(w, http.StatusBadRequest, "公告参数无效")
	case errors.Is(err, announcements.ErrAnnouncementNotFound):
		writeMessageError(w, http.StatusNotFound, "公告不存在")
	default:
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	}
}

func newAnnouncementID() string { return "ann_" + strings.ReplaceAll(uuid.NewString(), "-", "") }

func enrichAnnouncementWriteResult(r *http.Request, reader announcementManagementService, result port.Announcement) port.Announcement {
	if reader == nil || strings.TrimSpace(result.ID) == "" {
		return result
	}
	enriched, found, err := reader.FindManagement(r.Context(), result.ID)
	if err != nil || !found {
		return result
	}
	return enriched
}

func publishAnnouncementChange(r *http.Request, opts announcementManagementWriteOptions, id, operation string, fieldMask []string) {
	if opts.pageData == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := opts.pageData.PublishAnnouncementPublicChange(ctx, id, operation, fieldMask); err != nil && opts.logger != nil {
		opts.logger.Warn("公告页面数据变更发布失败", "error", err, "announcement_id", id)
	}
}

func recordAnnouncementOperationLog(r *http.Request, auth managementauth.Context, result port.Announcement, action string, before *port.Announcement, opts managementOperationLogOptions) {
	if opts.submitter == nil {
		return
	}
	statusCode := http.StatusOK
	if action == "create" {
		statusCode = http.StatusCreated
	} else if action == "delete" {
		statusCode = http.StatusNoContent
	}
	changes := announcementOperationChanges(result, action, before)
	input := port.OperationLogInput{ID: opts.newLogID(), TraceID: requestIDFromContext(r.Context()), ActorSystemAccountID: auth.SystemAccountID, ActorUsername: auth.Username, ActorDisplayName: auth.DisplayName, ActorRole: auth.Role, Mode: "admin", Module: "announcements", Action: action, OperationKey: "announcements." + action, ResourceType: "announcement", ResourceID: result.ID, ResourceName: result.Title, Summary: announcementOperationSummary(action, result.Title), DetailLevel: "summary", VisibilityScope: "all_users", Changes: changes, Method: r.Method, Path: r.URL.Path, StatusCode: &statusCode, ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(), CreatedAt: opts.now().UTC()}
	if result.Status != "published" && action != "publish" && (before == nil || before.Status != "published") {
		input.VisibilityScope, input.DetailLevel = "admin_only", "full"
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func announcementOperationChanges(result port.Announcement, action string, before *port.Announcement) []port.OperationLogChange {
	if action == "create" {
		return []port.OperationLogChange{{Field: "title", Label: "标题", After: result.Title}, {Field: "level", Label: "级别", After: result.Level}, {Field: "status", Label: "状态", After: result.Status}}
	}
	if action == "delete" {
		return []port.OperationLogChange{{Field: "deleted", Label: "删除状态", Before: false, After: true}}
	}
	if action == "publish" {
		return changedAnnouncementFields(before, result, []string{"status", "publishedAt"})
	}
	if action == "unpublish" {
		return changedAnnouncementFields(before, result, []string{"status"})
	}
	changes := []port.OperationLogChange{}
	if before == nil {
		return changes
	}
	for _, item := range []struct {
		field, label  string
		before, after any
	}{{"title", "标题", before.Title, result.Title}, {"content", "内容", before.Content, result.Content}, {"level", "级别", before.Level, result.Level}, {"status", "状态", before.Status, result.Status}} {
		if fmt.Sprint(item.before) != fmt.Sprint(item.after) {
			changes = append(changes, port.OperationLogChange{Field: item.field, Label: item.label, Before: item.before, After: item.after, Sensitive: item.field == "content"})
		}
	}
	return changes
}

func changedAnnouncementFields(before *port.Announcement, result port.Announcement, fields []string) []port.OperationLogChange {
	if before == nil {
		return nil
	}
	changes := make([]port.OperationLogChange, 0, len(fields))
	for _, field := range fields {
		var label string
		var oldValue, newValue any
		switch field {
		case "status":
			label, oldValue, newValue = "状态", before.Status, result.Status
		case "publishedAt":
			label, oldValue, newValue = "发布时间", before.PublishedAt, result.PublishedAt
		}
		if fmt.Sprint(oldValue) != fmt.Sprint(newValue) {
			changes = append(changes, port.OperationLogChange{Field: field, Label: label, Before: oldValue, After: newValue})
		}
	}
	return changes
}
func announcementOperationSummary(action, title string) string {
	names := map[string]string{"create": "创建公告：", "update": "更新公告：", "publish": "发布公告：", "unpublish": "下线公告：", "delete": "删除公告："}
	return names[action] + title
}
