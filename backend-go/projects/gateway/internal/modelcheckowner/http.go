package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// RunService is the narrow runtime dependency of the Gateway management
// handler. Runtime construction, authentication and source resolution stay
// outside this transport package and are injected by the owner process.
type RunService interface {
	Run(context.Context, RunRequest) (RunResult, error)
	RunStream(context.Context, RunRequest, func(ProgressEvent)) (RunResult, error)
	ListRuns(context.Context, RunListQuery) (any, error)
	GetRun(context.Context, string) (any, bool, error)
}

type RunRequest struct {
	TargetType, TargetID, Model, Profile                         string
	SystemAccountID, ActorSystemAccountID                        string
	TriggerKind, ScheduleID                                      string
	ProviderCode                                                 string
	Threshold                                                    int
	PenaltyAction                                                string
	IdentityKey, ConfigRevision, PolicyRevision, ProbeSetVersion string
	Endpoint, Prompt                                             string
	Protocol                                                     string
	Headers                                                      http.Header
}

type RunResult struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
}

type ProgressEvent struct {
	Kind string `json:"kind"`
	Data any    `json:"data,omitempty"`
}

type RunListQuery struct {
	SystemAccountID string
	Page, PageSize  int
}

type Authorize func(context.Context, *http.Request) (string, error)

// NewAdminAuthorize adapts the Gateway-owned session contract to the J3b
// handler. Authentication and administrator scope checks stay in-process;
// this adapter never proxies to Node or another Go service.
func NewAdminAuthorize(auth *modelcheckauth.Authenticator) Authorize {
	return func(ctx context.Context, request *http.Request) (string, error) {
		if auth == nil || request == nil {
			return "", errors.New("J3b Gateway authenticator is not initialized")
		}
		actor, err := auth.RequireAdmin(ctx, request.Header.Get("Authorization"), request.Header.Get("Cookie"))
		if err != nil {
			return "", err
		}
		return actor.SystemAccountID, nil
	}
}

type BuildRequest func(context.Context, string, RunCommand) (RunRequest, error)

type RunCommand struct {
	TargetType          string `json:"targetType"`
	TargetID            string `json:"targetId"`
	Model               string `json:"model"`
	Profile             string `json:"profile,omitempty"`
	TrustedComparison   bool   `json:"trustedComparison,omitempty"`
	TrustedComparisonID string `json:"trustedComparisonAccountId,omitempty"`
}

type HTTPHandler struct {
	Service   RunService
	Active    *modelcheckactive.Registry
	Authorize Authorize
	Build     BuildRequest
	MaxBody   int64
	Heartbeat time.Duration
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil || h.Authorize == nil || h.Build == nil || h.Active == nil {
		writeOwnerError(w, http.StatusServiceUnavailable, "J3b Gateway 管理入口未完成 owner 接线")
		return
	}
	systemAccountID, err := h.Authorize(r.Context(), r)
	if err != nil || strings.TrimSpace(systemAccountID) == "" {
		status := http.StatusUnauthorized
		if errors.Is(err, modelcheckauth.ErrForbidden) {
			status = http.StatusForbidden
		}
		writeOwnerError(w, status, "模型检测管理请求未授权")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPost && path == "/run":
		h.serveRun(w, r, systemAccountID)
	case r.Method == http.MethodPost && path == "/run/stream":
		h.serveStream(w, r, systemAccountID)
	case r.Method == http.MethodGet && path == "/run/active":
		h.serveActive(w, systemAccountID)
	case r.Method == http.MethodPost && path == "/run/stop":
		h.serveStop(w, systemAccountID)
	case r.Method == http.MethodGet && path == "/runs":
		h.serveList(w, r, systemAccountID)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/runs/"):
		h.serveDetail(w, r, strings.TrimPrefix(path, "/runs/"), systemAccountID)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) serveRun(w http.ResponseWriter, r *http.Request, accountID string) {
	command, err := decodeRunCommand(r, h.maxBody())
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	runRequest, err := h.Build(r.Context(), accountID, command)
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateBuiltRequest(accountID, command, runRequest); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := "system-account:" + accountID
	handle, acquired, current := h.Active.TryStart(r.Context(), key, modelcheckactive.Summary{TargetID: runRequest.TargetID, Model: runRequest.Model, Profile: runRequest.Profile, StartedAt: time.Now().UTC()})
	if !acquired {
		w.Header().Set("Retry-After", "1")
		writeOwnerJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "active", "active": current}})
		return
	}
	defer handle.Finish()
	result, err := h.Service.Run(handle.Context(), runRequest)
	if result.RunID != "" {
		handle.Update(modelcheckactive.Summary{RunID: result.RunID})
	}
	if err != nil {
		writeOwnerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (h *HTTPHandler) serveStream(w http.ResponseWriter, r *http.Request, accountID string) {
	command, err := decodeRunCommand(r, h.maxBody())
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	runRequest, err := h.Build(r.Context(), accountID, command)
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateBuiltRequest(accountID, command, runRequest); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := "system-account:" + accountID
	handle, acquired, current := h.Active.TryStart(r.Context(), key, modelcheckactive.Summary{TargetID: runRequest.TargetID, Model: runRequest.Model, Profile: runRequest.Profile, StartedAt: time.Now().UTC()})
	if !acquired {
		w.Header().Set("Retry-After", "1")
		writeOwnerJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "active", "active": current}})
		return
	}
	defer handle.Finish()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOwnerError(w, http.StatusInternalServerError, "J3b SSE 不受当前 HTTP 服务器支持")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	type streamResult struct {
		result RunResult
		err    error
	}
	events := make(chan ProgressEvent, 32)
	results := make(chan streamResult, 1)
	go func() {
		result, err := h.Service.RunStream(handle.Context(), runRequest, func(event ProgressEvent) {
			if event.Kind == "run_started" {
				if data, ok := event.Data.(map[string]any); ok {
					if runID, ok := data["runId"].(string); ok {
						handle.Update(modelcheckactive.Summary{RunID: runID})
					}
				}
			}
			select {
			case events <- event:
			case <-handle.Context().Done():
			}
		})
		results <- streamResult{result: result, err: err}
	}()
	heartbeat := h.heartbeat()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	var writeMu sync.Mutex
	writeEvent := func(event string, value any) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeSSE(w, flusher, event, value) == nil
	}
	for {
		select {
		case event := <-events:
			if !writeEvent("progress", event) {
				return
			}
		case <-ticker.C:
			writeMu.Lock()
			_, writeErr := io.WriteString(w, ": heartbeat\n\n")
			if writeErr == nil {
				flusher.Flush()
			}
			writeMu.Unlock()
			if writeErr != nil {
				return
			}
		case outcome := <-results:
			if outcome.err != nil {
				_ = writeEvent("error", map[string]any{"message": outcome.err.Error()})
				return
			}
			_ = writeEvent("complete", outcome.result)
			return
		case <-handle.Context().Done():
			return
		}
	}
}

func (h *HTTPHandler) serveActive(w http.ResponseWriter, accountID string) {
	if summary, ok := h.Active.Get("system-account:" + accountID); ok {
		writeOwnerJSON(w, http.StatusOK, map[string]any{"data": summary})
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": nil})
}

func (h *HTTPHandler) serveStop(w http.ResponseWriter, accountID string) {
	summary, stopped := h.Active.Stop("system-account:" + accountID)
	var active any
	if stopped {
		active = summary
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"stopped": stopped, "active": active}})
}

func (h *HTTPHandler) serveList(w http.ResponseWriter, r *http.Request, accountID string) {
	page, pageSize := parsePage(r)
	result, err := h.Service.ListRuns(r.Context(), RunListQuery{SystemAccountID: accountID, Page: page, PageSize: pageSize})
	if err != nil {
		writeOwnerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (h *HTTPHandler) serveDetail(w http.ResponseWriter, r *http.Request, runID, accountID string) {
	if strings.TrimSpace(runID) == "" {
		http.NotFound(w, r)
		return
	}
	result, found, err := h.Service.GetRun(r.Context(), runID)
	if err != nil {
		writeOwnerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if view, ok := result.(RunView); ok && view.SystemAccountID != accountID {
		http.NotFound(w, r)
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": result})
}

func parsePage(r *http.Request) (int, int) {
	page, pageSize := 1, 50
	if value := r.URL.Query().Get("page"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if value := r.URL.Query().Get("pageSize"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 1000 {
			pageSize = parsed
		}
	}
	return page, pageSize
}

func decodeRunCommand(r *http.Request, maxBody int64) (RunCommand, error) {
	if r.Body == nil {
		return RunCommand{}, errors.New("模型检测请求体不能为空")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	decoder.DisallowUnknownFields()
	var command RunCommand
	if err := decoder.Decode(&command); err != nil {
		return RunCommand{}, errors.New("模型检测请求体无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RunCommand{}, errors.New("模型检测请求体必须是单个 JSON 对象")
	}
	if command.TargetType == "" || command.TargetID == "" || command.Model == "" {
		return RunCommand{}, errors.New("targetType、targetId、model 为必填")
	}
	if command.Profile != "" && command.Profile != "quick" && command.Profile != "full" {
		return RunCommand{}, errors.New("profile 必须为 quick 或 full")
	}
	if command.TrustedComparisonID != "" && !command.TrustedComparison {
		return RunCommand{}, errors.New("trustedComparisonAccountId 需要 trustedComparison=true")
	}
	if command.TrustedComparison && command.TrustedComparisonID == "" {
		return RunCommand{}, errors.New("trustedComparison=true 需要 trustedComparisonAccountId")
	}
	return command, nil
}

func validateBuiltRequest(systemAccountID string, command RunCommand, request RunRequest) error {
	if strings.TrimSpace(request.SystemAccountID) != strings.TrimSpace(systemAccountID) {
		return errors.New("模型检测请求 scope 与认证账号不一致")
	}
	if strings.TrimSpace(request.ActorSystemAccountID) == "" {
		return errors.New("模型检测请求缺少 actor scope")
	}
	if request.TargetType != "account" || request.TargetID != command.TargetID || request.Model != command.Model {
		return errors.New("模型检测请求目标快照与原始命令不一致")
	}
	if request.Profile != "quick" && request.Profile != "full" {
		return errors.New("模型检测请求 profile 快照无效")
	}
	return nil
}

func (h *HTTPHandler) maxBody() int64 {
	if h.MaxBody > 0 {
		return h.MaxBody
	}
	return 512 << 10
}

func (h *HTTPHandler) heartbeat() time.Duration {
	if h.Heartbeat > 0 {
		return h.Heartbeat
	}
	return 10 * time.Second
}

func writeSSE(w io.Writer, flusher http.Flusher, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: "+event+"\ndata: "+string(payload)+"\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeOwnerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeOwnerError(w http.ResponseWriter, status int, message string) {
	writeOwnerJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}
