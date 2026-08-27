// Package modelcheckhttp provides the Go-owned management transport boundary
// for J3b. Account/configuration resolution is injected by the jobs adapter;
// this package never calls Node, DB-service, or another process.
package modelcheckhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
)

const (
	DefaultMaxBodyBytes = 512 << 10
	DefaultHeartbeat    = 10 * time.Second
)

var (
	ErrUnauthorized = errors.New("model check management request is unauthorized")
	ErrInvalidBody  = errors.New("model check management request body is invalid")
)

type Scope struct {
	SystemAccountID       string
	ActorSystemAccountID  string
	ActorRole             string
	SystemAccountFilterID string
}

type Command struct {
	TargetType          string `json:"targetType"`
	TargetID            string `json:"targetId"`
	Model               string `json:"model"`
	Profile             string `json:"profile,omitempty"`
	TrustedComparison   bool   `json:"trustedComparison,omitempty"`
	TrustedComparisonID string `json:"trustedComparisonAccountId,omitempty"`
}

type AuthorizeFunc func(context.Context, *http.Request) (Scope, error)
type BuildRequestFunc func(context.Context, Scope, Command) (modelcheckruntime.RunRequest, error)
type ResolveScopeFunc func(context.Context, Scope, Command) (Scope, error)
type RunReader interface {
	ListRuns(context.Context, modelcheckstore.RunListOptions) (modelcheckstore.RunListResult, error)
	GetRun(context.Context, string, string) (modelcheckstore.RunDetail, bool, error)
}

type Handler struct {
	Service       *modelcheckruntime.Service
	Active        *modelcheckactive.Registry
	Authorize     AuthorizeFunc
	BuildRequest  BuildRequestFunc
	ResolveScope  ResolveScopeFunc
	Reader        RunReader
	MaxBodyBytes  int64
	Heartbeat     time.Duration
	RetryAfterSec int
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if h == nil || h.Service == nil || h.Authorize == nil || h.BuildRequest == nil {
		writeJSONError(response, http.StatusServiceUnavailable, "模型检测 Go 管理入口未初始化")
		return
	}
	scope, err := h.Authorize(request.Context(), request)
	if err != nil {
		status, message := http.StatusUnauthorized, ErrUnauthorized.Error()
		code := ""
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			status, message, code = httpErr.Status, httpErr.Message, httpErr.Code
		}
		writeJSONErrorCode(response, status, message, code)
		return
	}
	if strings.TrimSpace(scope.SystemAccountID) == "" {
		writeJSONError(response, http.StatusUnauthorized, ErrUnauthorized.Error())
		return
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case request.Method == http.MethodPost && path == "/run":
		h.serveJSONRun(response, request, scope)
	case request.Method == http.MethodPost && path == "/run/stream":
		h.serveStreamRun(response, request, scope)
	case request.Method == http.MethodGet && path == "/run/active":
		h.serveActive(response, scope)
	case request.Method == http.MethodPost && path == "/run/stop":
		h.serveStop(response, scope)
	case request.Method == http.MethodGet && path == "/runs":
		h.serveRunList(response, request, scope)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/runs/"):
		h.serveRunDetail(response, request, scope, strings.TrimPrefix(path, "/runs/"))
	default:
		http.NotFound(response, request)
	}
}

type HTTPError struct {
	Status  int
	Message string
	Code    string
}

func (e *HTTPError) Error() string { return e.Message }

func (h *Handler) serveJSONRun(response http.ResponseWriter, request *http.Request, scope Scope) {
	command, err := decodeCommand(response, request, h.maxBodyBytes())
	if err != nil {
		writeJSONError(response, http.StatusBadRequest, err.Error())
		return
	}
	scope, err = h.resolveScope(request.Context(), scope, command)
	if err != nil {
		h.writeBuildError(response, err)
		return
	}
	runRequest, err := h.BuildRequest(request.Context(), scope, command)
	if err != nil {
		h.writeBuildError(response, err)
		return
	}
	result, err := h.run(request.Context(), scope, runRequest)
	if err != nil {
		h.writeRunError(response, scope, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"data": result})
}

func (h *Handler) serveStreamRun(response http.ResponseWriter, request *http.Request, scope Scope) {
	command, err := decodeCommand(response, request, h.maxBodyBytes())
	if err != nil {
		writeJSONError(response, http.StatusBadRequest, err.Error())
		return
	}
	scope, err = h.resolveScope(request.Context(), scope, command)
	if err != nil {
		h.writeBuildError(response, err)
		return
	}
	runRequest, err := h.BuildRequest(request.Context(), scope, command)
	if err != nil {
		h.writeBuildError(response, err)
		return
	}
	runCtx, cancel := context.WithCancel(request.Context())
	defer cancel()
	runRequest, releaseLease, err := h.reserveActive(runCtx, scope, runRequest)
	if err != nil {
		h.writeRunError(response, scope, err)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		releaseLease()
		writeJSONError(response, http.StatusInternalServerError, "模型检测 SSE 不受当前 HTTP 服务器支持")
		return
	}
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache, no-transform")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	if _, err := io.WriteString(response, ": connected\n\n"); err != nil {
		releaseLease()
		return
	}
	flusher.Flush()

	resultCh := make(chan runResult, 1)
	progress := make(chan modelcheckruntime.ProgressEvent, 128)
	go func() {
		result, runErr := h.Service.RunWithProgress(runCtx, runRequest, func(event modelcheckruntime.ProgressEvent) {
			select {
			case progress <- event:
			case <-runCtx.Done():
			}
		})
		resultCh <- runResult{result: result, err: runErr}
	}()
	heartbeat := time.NewTicker(h.heartbeat())
	defer heartbeat.Stop()
	for {
		select {
		case result := <-resultCh:
			for {
				select {
				case event := <-progress:
					if !h.writeSSE(response, flusher, "progress", event) {
						cancel()
						return
					}
				default:
					goto resultReady
				}
			}
		resultReady:
			if result.err != nil {
				if runCtx.Err() != nil {
					return
				}
				h.writeSSE(response, flusher, "error", sseErrorPayload(result.err))
				return
			}
			h.writeSSE(response, flusher, "complete", result.result)
			return
		case event := <-progress:
			if !h.writeSSE(response, flusher, "progress", event) {
				cancel()
				return
			}
		case <-heartbeat.C:
			if request.Context().Err() != nil {
				return
			}
			if !h.writeSSE(response, flusher, "heartbeat", nil) {
				cancel()
				return
			}
		}
	}
}

func sseErrorPayload(err error) map[string]any {
	payload := map[string]any{"message": err.Error()}
	if errors.Is(err, modelcheckruntime.ErrInvalidRequest) {
		payload["statusCode"] = http.StatusBadRequest
	}
	return payload
}

type runResult struct {
	result modelcheckruntime.Result
	err    error
}

func (h *Handler) serveActive(response http.ResponseWriter, scope Scope) {
	registry := h.activeRegistry()
	if registry == nil || strings.TrimSpace(scope.SystemAccountID) == "" {
		writeJSON(response, http.StatusOK, map[string]any{"data": nil})
		return
	}
	summary, ok := registry.Get(activeKey(scope))
	if !ok {
		writeJSON(response, http.StatusOK, map[string]any{"data": nil})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"data": summary})
}

func (h *Handler) serveStop(response http.ResponseWriter, scope Scope) {
	registry := h.activeRegistry()
	if registry == nil {
		writeJSON(response, http.StatusOK, map[string]any{"data": map[string]any{"stopped": false, "active": nil}})
		return
	}
	summary, stopped := registry.Stop(activeKey(scope))
	var active any
	if stopped {
		active = summary
	}
	writeJSON(response, http.StatusOK, map[string]any{"data": map[string]any{"stopped": stopped, "active": active}})
}

func (h *Handler) serveRunList(response http.ResponseWriter, request *http.Request, scope Scope) {
	if h.Reader == nil {
		writeJSONError(response, http.StatusServiceUnavailable, "模型检测管理读服务未初始化")
		return
	}
	page, pageSize, err := parsePage(request)
	if err != nil {
		writeJSONError(response, http.StatusBadRequest, err.Error())
		return
	}
	query := request.URL.Query()
	result, err := h.Reader.ListRuns(request.Context(), modelcheckstore.RunListOptions{
		SystemAccountID: scope.SystemAccountID,
		// Node /runs 固定只返回 account 目标；不接受调用方改写目标类型。
		TargetType: "account", TargetID: query.Get("targetId"), Model: query.Get("model"),
		Level: query.Get("level"), Status: query.Get("status"), TriggerKind: query.Get("triggerKind"),
		StartAt: query.Get("startAt"), EndAt: query.Get("endAt"), Page: page, PageSize: pageSize,
	})
	if err != nil {
		writeJSONError(response, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"data": result})
}

func (h *Handler) serveRunDetail(response http.ResponseWriter, request *http.Request, scope Scope, runID string) {
	if h.Reader == nil {
		writeJSONError(response, http.StatusServiceUnavailable, "模型检测管理读服务未初始化")
		return
	}
	runID, err := url.PathUnescape(runID)
	if err != nil || strings.TrimSpace(runID) == "" || strings.Contains(runID, "/") {
		writeJSONError(response, http.StatusNotFound, "模型检测记录不存在")
		return
	}
	detail, found, err := h.Reader.GetRun(request.Context(), runID, scope.SystemAccountID)
	if err != nil {
		writeJSONError(response, http.StatusBadGateway, err.Error())
		return
	}
	if !found {
		writeJSONError(response, http.StatusNotFound, "模型检测记录不存在")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"data": detail})
}

func parsePage(request *http.Request) (int, int, error) {
	page, pageSize := 1, 20
	if raw := request.URL.Query().Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return 0, 0, errors.New("分页参数无效")
		}
		page = value
	}
	if raw := request.URL.Query().Get("pageSize"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, 0, errors.New("分页参数无效")
		}
		pageSize = value
	}
	return page, pageSize, nil
}

func (h *Handler) writeBuildError(response http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		status = httpErr.Status
	}
	writeJSONError(response, status, err.Error())
}

func (h *Handler) writeRunError(response http.ResponseWriter, scope Scope, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, modelcheckruntime.ErrActiveRun) {
		status = http.StatusConflict
		response.Header().Set("Retry-After", fmt.Sprint(h.retryAfter()))
		var active any
		if registry := h.activeRegistry(); registry != nil {
			if summary, ok := registry.Get(activeKey(scope)); ok {
				active = summary
			}
		}
		writeJSON(response, status, map[string]any{"message": err.Error(), "active": active})
		return
	}
	if errors.Is(err, modelcheckruntime.ErrInvalidRequest) {
		status = http.StatusBadRequest
	}
	writeJSONError(response, status, err.Error())
}

func (h *Handler) writeSSE(response http.ResponseWriter, flusher http.Flusher, event string, value any) bool {
	if event == "heartbeat" {
		if _, err := io.WriteString(response, ": heartbeat\n\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if value == nil {
		if _, err := io.WriteString(response, "event: "+event+"\n\n"); err != nil {
			return false
		}
	} else {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err := io.WriteString(response, "event: "+event+"\ndata: "+string(data)+"\n\n"); err != nil {
			return false
		}
	}
	flusher.Flush()
	return true
}

func decodeCommand(response http.ResponseWriter, request *http.Request, maxBytes int64) (Command, error) {
	request.Body = http.MaxBytesReader(response, request.Body, maxBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var command Command
	if err := decoder.Decode(&command); err != nil {
		return Command{}, fmt.Errorf("%w: %v", ErrInvalidBody, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Command{}, fmt.Errorf("%w: 不得包含尾随 JSON", ErrInvalidBody)
	}
	command.TargetID = strings.TrimSpace(command.TargetID)
	command.Model = strings.TrimSpace(command.Model)
	command.TrustedComparisonID = strings.TrimSpace(command.TrustedComparisonID)
	if command.Profile == "" {
		command.Profile = modelcheckprofile.DefaultProfile
	}
	if command.TrustedComparisonID != "" {
		command.TrustedComparison = true
	}
	if command.TrustedComparison && command.TrustedComparisonID == "" {
		return Command{}, fmt.Errorf("%w: 可信对比账户不能为空", ErrInvalidBody)
	}
	if command.TargetType != "account" || command.TargetID == "" || command.Model == "" || (command.Profile != "quick" && command.Profile != "full") {
		return Command{}, fmt.Errorf("%w: targetType、targetId、model 或 profile 无效", ErrInvalidBody)
	}
	return command, nil
}

func (h *Handler) activeRegistry() *modelcheckactive.Registry {
	if h != nil && h.Service != nil && h.Service.Active != nil {
		return h.Service.Active
	}
	if h != nil {
		return h.Active
	}
	return nil
}

func (h *Handler) run(ctx context.Context, scope Scope, request modelcheckruntime.RunRequest) (modelcheckruntime.Result, error) {
	request, _, err := h.reserveActive(ctx, scope, request)
	if err != nil {
		return modelcheckruntime.Result{}, err
	}
	return h.Service.Run(ctx, request)
}

func (h *Handler) reserveActive(ctx context.Context, scope Scope, request modelcheckruntime.RunRequest) (modelcheckruntime.RunRequest, func(), error) {
	registry := h.activeRegistry()
	if registry == nil {
		return request, func() {}, nil
	}
	handle, acquired, _ := registry.TryStart(ctx, activeKey(scope), activeSummary(request))
	if !acquired {
		return modelcheckruntime.RunRequest{}, func() {}, modelcheckruntime.ErrActiveRun
	}
	request.ActiveLease = &handle
	return request, handle.Finish, nil
}

func activeSummary(request modelcheckruntime.RunRequest) modelcheckactive.Summary {
	return modelcheckactive.Summary{RunID: request.RunID, TargetID: request.Target.ID, TargetName: request.TargetName, Model: request.Model, Profile: request.Profile, StartedAt: request.StartedAt.UTC()}
}

func (h *Handler) maxBodyBytes() int64 {
	if h.MaxBodyBytes <= 0 {
		return DefaultMaxBodyBytes
	}
	return h.MaxBodyBytes
}

func (h *Handler) heartbeat() time.Duration {
	if h.Heartbeat <= 0 {
		return DefaultHeartbeat
	}
	return h.Heartbeat
}

func (h *Handler) retryAfter() int {
	if h.RetryAfterSec <= 0 {
		return 1
	}
	return h.RetryAfterSec
}

func activeKey(scope Scope) string {
	actorID := strings.TrimSpace(scope.ActorSystemAccountID)
	if actorID == "" {
		actorID = strings.TrimSpace(scope.SystemAccountID)
	}
	return "system-account:" + actorID
}

func (h *Handler) resolveScope(ctx context.Context, scope Scope, command Command) (Scope, error) {
	if h == nil || h.ResolveScope == nil {
		return scope, nil
	}
	return h.ResolveScope(ctx, scope, command)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeJSONError(response http.ResponseWriter, status int, message string) {
	writeJSONErrorCode(response, status, message, "")
}

func writeJSONErrorCode(response http.ResponseWriter, status int, message, code string) {
	payload := map[string]any{"message": message}
	if code != "" {
		payload["code"] = code
	}
	writeJSON(response, status, payload)
}
