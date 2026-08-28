package announcements

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultHTTPMaxBody       int64 = 64 << 10
	anonymousPublicAccountID       = "__juhe_ai_anonymous_public__"
)

// ErrUnauthenticated lets an injected ActorResolver distinguish an absent or
// invalid credential from an unavailable authentication dependency.
var ErrUnauthenticated = errors.New("announcement actor is not authenticated")

// ActorResolver is owned by the listener that mounts HTTPHandler. It returns
// ErrUnauthenticated (or a zero Actor) when the request has no actor; public
// reads allow that state, while mark-read and management routes reject it.
type ActorResolver func(context.Context, *http.Request) (Actor, error)

// HTTPService is the narrow transport-facing subset of Port. Keeping the
// adapter independent from compatibility aliases makes lightweight test and
// alternate owner implementations possible without widening the contract.
type HTTPService interface {
	ListPublicAnnouncements(context.Context, Actor, int) ([]PublicListItem, error)
	FindPublicAnnouncement(context.Context, Actor, string) (PublicDetail, error)
	MarkPublicAnnouncementsRead(context.Context, Actor, []string) (ReadResult, error)
	ListAnnouncements(context.Context, Actor, ListOptions) (ListResult, error)
	FindAnnouncement(context.Context, Actor, string) (AdminDetail, error)
	CreateAnnouncement(context.Context, Actor, CreateInput) (MutationOutcome, error)
	PatchAnnouncement(context.Context, Actor, string, string, PatchInput) (*MutationOutcome, error)
	PublishAnnouncement(context.Context, Actor, string, string) (*MutationOutcome, error)
	UnpublishAnnouncement(context.Context, Actor, string, string) (*MutationOutcome, error)
	DeleteAnnouncement(context.Context, Actor, string, string) (DeleteResult, error)
}

// HTTPHandler adapts the announcement owner port to the Node-compatible route
// family. It deliberately has no dependency on Gateway main, Node, IPC,
// session-touch, request deduplication, or operation logging.
type HTTPHandler struct {
	Service      HTTPService
	ResolveActor ActorResolver
	MaxBody      int64
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil || h.ResolveActor == nil {
		writeAnnouncementError(w, http.StatusServiceUnavailable, "公告服务 owner 未完成接线")
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && path == "/public":
		h.listPublic(w, r)
	case r.Method == http.MethodPost && path == "/public/read":
		h.markRead(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/public/"):
		h.getPublic(w, r, strings.TrimPrefix(path, "/public/"))
	case r.Method == http.MethodGet && path == "":
		h.listAdmin(w, r)
	case r.Method == http.MethodPost && path == "":
		h.create(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/publish"):
		h.publish(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/publish"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/unpublish"):
		h.unpublish(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/unpublish"))
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/"):
		h.patch(w, r, strings.TrimPrefix(path, "/"))
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/"):
		h.delete(w, r, strings.TrimPrefix(path, "/"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/"):
		h.getAdmin(w, r, strings.TrimPrefix(path, "/"))
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) listPublic(w http.ResponseWriter, r *http.Request) {
	actor, err := h.publicActor(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	limit, err := parseOptionalPositive(r.URL.Query().Get("limit"), PublicLimit)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告查询参数无效")
		return
	}
	items, err := h.Service.ListPublicAnnouncements(r.Context(), actor, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, items)
}

func (h *HTTPHandler) getPublic(w http.ResponseWriter, r *http.Request, id string) {
	if strings.TrimSpace(id) == "" || strings.Contains(id, "/") {
		writeAnnouncementError(w, http.StatusNotFound, "公告不存在")
		return
	}
	actor, err := h.publicActor(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	item, err := h.Service.FindPublicAnnouncement(r.Context(), actor, id)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, item)
}

func (h *HTTPHandler) markRead(w http.ResponseWriter, r *http.Request) {
	actor, err := h.requireActor(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	var input struct {
		AnnouncementIDs []string `json:"announcementIds"`
	}
	raw, err := decodeObject(h.limitBody(w, r))
	if err != nil || decodeStrictObject(raw, &input) != nil || input.AnnouncementIDs == nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告已读参数无效")
		return
	}
	result, err := h.Service.MarkPublicAnnouncementsRead(r.Context(), actor, input.AnnouncementIDs)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) listAdmin(w http.ResponseWriter, r *http.Request) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	page, err := parseOptionalPositive(r.URL.Query().Get("page"), 0)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告查询参数无效")
		return
	}
	pageSize, err := parseOptionalPositive(r.URL.Query().Get("pageSize"), MaxPageSize)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告查询参数无效")
		return
	}
	result, err := h.Service.ListAnnouncements(r.Context(), actor, ListOptions{Page: page, PageSize: pageSize})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) getAdmin(w http.ResponseWriter, r *http.Request, id string) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	item, err := h.Service.FindAnnouncement(r.Context(), actor, id)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, item)
}

func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	reader := h.limitBody(w, r)
	input, err := DecodeCreateInput(reader)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告参数无效")
		return
	}
	outcome, err := h.Service.CreateAnnouncement(r.Context(), actor, input)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeAnnouncementJSON(w, http.StatusCreated, outcome.Receipt)
}

func (h *HTTPHandler) patch(w http.ResponseWriter, r *http.Request, id string) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	input, err := DecodePatchRequest(h.limitBody(w, r))
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告参数无效")
		return
	}
	outcome, err := h.Service.PatchAnnouncement(r.Context(), actor, id, input.ExpectedRevision, PatchInput{Title: input.Title, Content: input.Content, Level: input.Level, Status: input.Status})
	if err != nil {
		h.writeError(w, err)
		return
	}
	if outcome == nil {
		writeAnnouncementError(w, http.StatusNotFound, "公告不存在")
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, outcome.Receipt)
}

func (h *HTTPHandler) publish(w http.ResponseWriter, r *http.Request, id string) {
	h.versionMutation(w, r, id, "publish")
}

func (h *HTTPHandler) unpublish(w http.ResponseWriter, r *http.Request, id string) {
	h.versionMutation(w, r, id, "unpublish")
}

func (h *HTTPHandler) versionMutation(w http.ResponseWriter, r *http.Request, id, action string) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	expectedRevision, err := h.decodeExpectedRevision(w, r)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告版本参数无效")
		return
	}
	var outcome *MutationOutcome
	if action == "publish" {
		outcome, err = h.Service.PublishAnnouncement(r.Context(), actor, id, expectedRevision)
	} else {
		outcome, err = h.Service.UnpublishAnnouncement(r.Context(), actor, id, expectedRevision)
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	if outcome == nil {
		writeAnnouncementError(w, http.StatusNotFound, "公告不存在")
		return
	}
	writeAnnouncementJSON(w, http.StatusOK, outcome.Receipt)
}

func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request, id string) {
	actor, err := h.requireAdmin(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	expectedRevision, err := h.decodeExpectedRevision(w, r)
	if err != nil {
		writeAnnouncementError(w, http.StatusBadRequest, "公告版本参数无效")
		return
	}
	result, err := h.Service.DeleteAnnouncement(r.Context(), actor, id, expectedRevision)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if !result.Deleted {
		writeAnnouncementError(w, http.StatusNotFound, "公告不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) publicActor(r *http.Request) (Actor, error) {
	actor, err := h.ResolveActor(r.Context(), r)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return Actor{SystemAccountID: anonymousPublicAccountID}, nil
		}
		return Actor{}, err
	}
	if strings.TrimSpace(actor.SystemAccountID) == "" {
		return Actor{SystemAccountID: anonymousPublicAccountID}, nil
	}
	return actor, nil
}

func (h *HTTPHandler) requireActor(r *http.Request) (Actor, error) {
	if h.ResolveActor == nil {
		return Actor{}, ErrOwnerGate
	}
	actor, err := h.ResolveActor(r.Context(), r)
	if err != nil {
		return Actor{}, err
	}
	if strings.TrimSpace(actor.SystemAccountID) == "" {
		return Actor{}, ErrUnauthenticated
	}
	return actor, nil
}

func (h *HTTPHandler) requireAdmin(r *http.Request) (Actor, error) {
	actor, err := h.requireActor(r)
	if err != nil {
		return Actor{}, err
	}
	if !actor.Admin() {
		return Actor{}, ErrForbidden
	}
	return actor, nil
}

func (h *HTTPHandler) decodeExpectedRevision(w http.ResponseWriter, r *http.Request) (string, error) {
	var input struct {
		ExpectedRevision string `json:"expectedRevision"`
	}
	raw, err := decodeObject(h.limitBody(w, r))
	if err != nil {
		return "", err
	}
	if strictErr := decodeStrictObject(raw, &input); strictErr != nil {
		return "", strictErr
	}
	input.ExpectedRevision = strings.TrimSpace(input.ExpectedRevision)
	if input.ExpectedRevision == "" {
		return "", ErrInvalidInput
	}
	return input.ExpectedRevision, nil
}

func (h *HTTPHandler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(h.limitBody(w, r))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

func (h *HTTPHandler) limitBody(w http.ResponseWriter, r *http.Request) io.Reader {
	return http.MaxBytesReader(w, r.Body, h.maxBody())
}

func (h *HTTPHandler) maxBody() int64 {
	if h.MaxBody > 0 {
		return h.MaxBody
	}
	return defaultHTTPMaxBody
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		writeAnnouncementError(w, http.StatusUnauthorized, "请先登录")
	case errors.Is(err, ErrForbidden):
		writeAnnouncementError(w, http.StatusForbidden, "需要管理员权限")
	case errors.Is(err, ErrInvalidInput):
		writeAnnouncementError(w, http.StatusBadRequest, "公告参数无效")
	case errors.Is(err, ErrNotFound):
		writeAnnouncementError(w, http.StatusNotFound, "公告不存在")
	case errors.Is(err, ErrRevisionConflict):
		response := map[string]any{"message": err.Error()}
		var conflict *RevisionConflictError
		if errors.As(err, &conflict) {
			response["currentRevision"] = conflict.CurrentRevision
		}
		writeAnnouncementValue(w, http.StatusConflict, response)
	default:
		writeAnnouncementError(w, http.StatusServiceUnavailable, "公告服务暂不可用")
	}
}

func parseOptionalPositive(raw string, maximum int) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || (maximum > 0 && value > maximum) {
		return 0, ErrInvalidInput
	}
	return value, nil
}

func writeAnnouncementJSON(w http.ResponseWriter, status int, data any) {
	writeAnnouncementValue(w, status, map[string]any{"data": data})
}

func writeAnnouncementError(w http.ResponseWriter, status int, message string) {
	writeAnnouncementValue(w, status, map[string]any{"message": message})
}

func writeAnnouncementValue(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ http.Handler = (*HTTPHandler)(nil)
var _ HTTPService = (*Service)(nil)
