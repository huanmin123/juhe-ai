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
	TargetType, TargetID, Model, Profile                                                          string
	SystemAccountID, ActorSystemAccountID                                                         string
	TriggerKind, ScheduleID                                                                       string
	ProviderCode                                                                                  string
	Threshold                                                                                     int
	PenaltyAction                                                                                 string
	RecoveryIntervalMinutes                                                                       int
	ManualEnforcementEnabled, OwnPhysicalAccount                                                  bool
	IdentityKey, ConfigRevision, SourceConfigRevision, PolicyRevision, ProbeSetVersion            string
	SourceDispatchRevision                                                                        int64
	DispatchRevision                                                                              int64
	TrustedComparison                                                                             bool
	TrustedComparisonAccountID, TrustedComparisonSystemAccountID, TrustedComparisonConfigRevision string
	TrustedComparisonDispatchRevision                                                             int64
	TrustedComparisonSourceConfigRevision                                                         string
	TrustedComparisonSourceDispatchRevision                                                       int64
	Endpoint, Prompt                                                                              string
	Protocol                                                                                      string
	SourceEndpointFamily, UpstreamEndpointFamily                                                  string
	UpstreamProtocol, UpstreamEndpointMode                                                        string
	Headers                                                                                       http.Header
}

type RunResult struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
}

// RequestError is the HTTP-safe equivalent of Node's
// ModelCheckRequestError. Runtime and target resolvers may return it when a
// request is well formed but cannot be fulfilled because of caller-visible
// state (for example an inaccessible account). Transport errors must keep
// their status code in both JSON and SSE responses.
type RequestError struct {
	StatusCode int
	Message    string
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type ProgressEvent struct {
	Kind string `json:"kind"`
	Data any    `json:"data,omitempty"`
}

type RunListQuery struct {
	SystemAccountID   string
	AllSystemAccounts bool
	Page, PageSize    int
	TargetID          string
	Model             string
	Level             string
	Status            string
	TriggerKind       string
	StartAt           string
	EndAt             string
}

// AccountOptionsQuery is the read-only selector used by the model-check
// management UI. It deliberately carries only filtering inputs; credentials
// and runtime state never cross this transport boundary.
type AccountOptionsQuery struct {
	// SystemAccountID is the selected tenant boundary. It is empty only when
	// AllSystemAccounts was explicitly authorized on the administrator mount.
	SystemAccountID   string
	AllSystemAccounts bool
	Purpose           string
	AccountID         string
	Keyword           string
	SelectedID        []string
	Limit             int
}

type AccountOption struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	ProviderCode            string   `json:"providerCode"`
	ProviderProtocolProfile string   `json:"providerProtocolProfileId"`
	ProtocolCode            string   `json:"protocolCode"`
	ProtocolVersion         string   `json:"protocolVersion"`
	ModelCheckModels        []string `json:"modelCheckModels,omitempty"`
}

type ModelCheckSupportedOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type ModelCheckOptions struct {
	SupportedModels   []ModelCheckSupportedOption `json:"supportedModels"`
	SupportedProfiles []ModelCheckSupportedOption `json:"supportedProfiles"`
	DefaultModel      string                      `json:"defaultModel"`
	DefaultProfile    string                      `json:"defaultProfile"`
	TrustedComparison map[string]any              `json:"trustedComparison"`
}

type AccountOptions interface {
	ListAccountOptions(context.Context, AccountOptionsQuery) ([]AccountOption, error)
	ModelCheckOptions() ModelCheckOptions
}

// TokenInterceptBaselineActivator is the in-process Gateway port for the
// calibration activation management command. Implementations must perform a
// durable CAS; HTTP never reaches Node, DB-service IPC, or another process.
type TokenInterceptBaselineActivator interface {
	ActivateTokenInterceptBaseline(context.Context, TokenInterceptBaselineActivation) error
}

type Authorize func(context.Context, *http.Request) (string, error)

// ManagementScope separates the authenticated actor from the target tenant.
// Node records model-check runs under the target account's tenant while
// retaining the administrator who initiated the work as actor metadata.
// An empty SelectedSystemAccountID is valid only with AllSystemAccounts.
type ManagementScope struct {
	ActorSystemAccountID    string
	SelectedSystemAccountID string
	AllSystemAccounts       bool
}

func (s ManagementScope) valid() bool {
	actor := strings.TrimSpace(s.ActorSystemAccountID)
	selected := strings.TrimSpace(s.SelectedSystemAccountID)
	return actor != "" && ((s.AllSystemAccounts && selected == "") || (!s.AllSystemAccounts && selected != ""))
}

func (s ManagementScope) activeKey() string {
	return "system-account:" + strings.TrimSpace(s.ActorSystemAccountID)
}

func (s ManagementScope) specificSystemAccountID() (string, bool) {
	selected := strings.TrimSpace(s.SelectedSystemAccountID)
	return selected, !s.AllSystemAccounts && selected != ""
}

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

// NewSelfAuthorize adapts the authenticated-user management contract used by
// /my-model-checks. It intentionally accepts non-admin actors and lets the
// handler's non-cross-account scope force all requests to the actor's own
// system account.
func NewSelfAuthorize(auth *modelcheckauth.Authenticator) Authorize {
	return func(ctx context.Context, request *http.Request) (string, error) {
		if auth == nil || request == nil {
			return "", errors.New("J3b Gateway authenticator is not initialized")
		}
		actor, err := auth.Authenticate(ctx, request.Header.Get("Authorization"), request.Header.Get("Cookie"))
		if err != nil {
			return "", err
		}
		return actor.SystemAccountID, nil
	}
}

type BuildRequest func(context.Context, string, RunCommand) (RunRequest, error)
type ScopedBuildRequest func(context.Context, ManagementScope, RunCommand) (RunRequest, error)

type RunCommand struct {
	TargetType          string `json:"targetType"`
	TargetID            string `json:"targetId"`
	Model               string `json:"model"`
	Profile             string `json:"profile,omitempty"`
	TrustedComparison   bool   `json:"trustedComparison,omitempty"`
	TrustedComparisonID string `json:"trustedComparisonAccountId,omitempty"`
}

type HTTPHandler struct {
	Service        RunService
	AccountOptions AccountOptions
	Baseline       TokenInterceptBaselineActivator
	Quality        QualityManagement
	Active         *modelcheckactive.Registry
	Authorize      Authorize
	Build          BuildRequest
	BuildScoped    ScopedBuildRequest
	MaxBody        int64
	Heartbeat      time.Duration
	// AllowCrossAccount identifies the administrator public mount. It is the
	// only mount permitted to construct a global or foreign-tenant scope.
	AllowCrossAccount bool
	// ForceActorScope is set only by the self public mount. It preserves the
	// legacy handler's fail-closed behavior for an unwired foreign scope while
	// making the self route ignore any caller-supplied systemAccountId.
	ForceActorScope bool
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil || h.Authorize == nil || (h.Build == nil && h.BuildScoped == nil) || h.Active == nil {
		writeOwnerError(w, http.StatusServiceUnavailable, "J3b Gateway 管理入口未完成 owner 接线")
		return
	}
	actorSystemAccountID, err := h.Authorize(r.Context(), r)
	if err != nil || strings.TrimSpace(actorSystemAccountID) == "" {
		status := http.StatusUnauthorized
		if errors.Is(err, modelcheckauth.ErrMustChange) {
			writeOwnerErrorCode(w, http.StatusForbidden, "must_change_password", err.Error())
			return
		}
		if errors.Is(err, modelcheckauth.ErrForbidden) {
			status = http.StatusForbidden
		}
		writeOwnerError(w, status, "模型检测管理请求未授权")
		return
	}
	scope, scopeErr := resolveManagementScope(r, actorSystemAccountID, h.AllowCrossAccount, h.ForceActorScope)
	if scopeErr != nil {
		writeOwnerError(w, http.StatusBadRequest, scopeErr.Error())
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPost && path == "/run":
		h.serveRun(w, r, scope)
	case r.Method == http.MethodPost && path == "/token-intercept-baselines/activate":
		if h.ForceActorScope {
			writeOwnerError(w, http.StatusForbidden, "模型检测 token 截距基线仅限管理员激活")
			return
		}
		h.activateTokenInterceptBaseline(w, r)
	case r.Method == http.MethodPost && path == "/run/stream":
		h.serveStream(w, r, scope)
	case r.Method == http.MethodGet && path == "/run/active":
		h.serveActive(w, scope)
	case r.Method == http.MethodPost && path == "/run/stop":
		h.serveStop(w, scope)
	case r.Method == http.MethodGet && path == "/runs":
		h.serveList(w, r, scope)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/runs/"):
		h.serveDetail(w, r, strings.TrimPrefix(path, "/runs/"), scope)
	case r.Method == http.MethodGet && path == "/quality-policy":
		h.serveScopedQualityPolicy(w, r, scope)
	case r.Method == http.MethodGet && path == "/options":
		h.serveOptions(w)
	case r.Method == http.MethodGet && (path == "/account-options" || path == "/options/accounts"):
		h.serveAccountOptions(w, r, scope)
	case r.Method == http.MethodPatch && path == "/quality-policy":
		h.patchScopedQualityPolicy(w, r, scope)
	case r.Method == http.MethodGet && path == "/quality-schedules":
		h.listScopedQualitySchedules(w, r, scope)
	case r.Method == http.MethodPost && path == "/quality-schedules":
		h.createScopedQualitySchedule(w, r, scope)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/quality-schedules/"):
		h.patchScopedQualitySchedule(w, r, scope, strings.TrimPrefix(path, "/quality-schedules/"))
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/quality-schedules/"):
		h.deleteScopedQualitySchedule(w, r, scope, strings.TrimPrefix(path, "/quality-schedules/"))
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) activateTokenInterceptBaseline(w http.ResponseWriter, r *http.Request) {
	var input TokenInterceptBaselineActivation
	if err := decodeOwnerJSON(r, h.maxBody(), &input); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	input = normalizeTokenInterceptBaselineActivation(input)
	if err := validateTokenInterceptBaselineActivation(input); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.Baseline == nil {
		writeOwnerError(w, http.StatusServiceUnavailable, "J3b Gateway 固定截距基线 owner 未完成接线")
		return
	}
	if err := h.Baseline.ActivateTokenInterceptBaseline(r.Context(), input); err != nil {
		switch {
		case errors.Is(err, ErrTokenInterceptBaselineConflict):
			writeOwnerError(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrTokenInterceptBaselineUnavailable):
			writeOwnerError(w, http.StatusServiceUnavailable, err.Error())
		default:
			// A concrete activator is still required to preserve the same
			// fail-closed service boundary for unexpected storage errors.
			writeOwnerError(w, http.StatusServiceUnavailable, err.Error())
		}
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"activated": true, "baselineVersion": input.BaselineVersion}})
}

func resolveManagementScope(r *http.Request, authenticated string, allowCrossAccount, forceActorScope bool) (ManagementScope, error) {
	actor := strings.TrimSpace(authenticated)
	if actor == "" {
		return ManagementScope{}, errors.New("J3b 管理请求缺少认证 systemAccountId")
	}
	if forceActorScope {
		return ManagementScope{ActorSystemAccountID: actor, SelectedSystemAccountID: actor}, nil
	}
	values, exists := r.URL.Query()["systemAccountId"]
	if !exists || len(values) == 0 || (len(values) == 1 && strings.TrimSpace(values[0]) == "") {
		if allowCrossAccount {
			return ManagementScope{ActorSystemAccountID: actor, AllSystemAccounts: true}, nil
		}
		return ManagementScope{ActorSystemAccountID: actor, SelectedSystemAccountID: actor}, nil
	}
	if len(values) != 1 {
		return ManagementScope{}, errors.New("J3b systemAccountId 作用域参数无效")
	}
	requested := strings.TrimSpace(values[0])
	if requested == "" {
		return ManagementScope{}, errors.New("J3b systemAccountId 作用域参数无效")
	}
	if requested == "all" {
		if !allowCrossAccount {
			return ManagementScope{ActorSystemAccountID: actor, SelectedSystemAccountID: actor}, nil
		}
		return ManagementScope{ActorSystemAccountID: actor, AllSystemAccounts: true}, nil
	}
	if !allowCrossAccount && requested != actor {
		return ManagementScope{}, errors.New("J3b 管理员跨 systemAccountId 作用域尚未完成，当前请求已拒绝")
	}
	return ManagementScope{ActorSystemAccountID: actor, SelectedSystemAccountID: requested}, nil
}

func (h *HTTPHandler) serveOptions(w http.ResponseWriter) {
	if h.AccountOptions == nil {
		writeOwnerError(w, http.StatusServiceUnavailable, "J3b Gateway 账户选项 owner 未完成接线")
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": h.AccountOptions.ModelCheckOptions()})
}

func (h *HTTPHandler) serveAccountOptions(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	if h.AccountOptions == nil {
		writeOwnerError(w, http.StatusServiceUnavailable, "J3b Gateway 账户选项 owner 未完成接线")
		return
	}
	query, err := parseAccountOptionsQuery(r)
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	query.SystemAccountID = strings.TrimSpace(scope.SelectedSystemAccountID)
	query.AllSystemAccounts = scope.AllSystemAccounts
	if query.SystemAccountID == "" && !query.AllSystemAccounts {
		writeOwnerError(w, http.StatusUnauthorized, "模型检测账户选项缺少认证 systemAccountId 作用域")
		return
	}
	items, err := h.AccountOptions.ListAccountOptions(r.Context(), query)
	if err != nil {
		writeOwnerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": items})
}

func parseAccountOptionsQuery(r *http.Request) (AccountOptionsQuery, error) {
	q := r.URL.Query()
	purpose := q.Get("purpose")
	if purpose != "run" && purpose != "history" && purpose != "schedule" {
		return AccountOptionsQuery{}, errors.New("模型检测账户选项 purpose 仅支持 run、history 或 schedule")
	}
	keyword := strings.TrimSpace(q.Get("keyword"))
	if len(keyword) > 100 {
		return AccountOptionsQuery{}, errors.New("模型检测账户选项 keyword 无效")
	}
	accountID := strings.TrimSpace(q.Get("accountId"))
	if accountID != "" && (len(accountID) > 120 || strings.ContainsAny(accountID, ",[]")) {
		return AccountOptionsQuery{}, errors.New("模型检测账户选项 accountId 无效")
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 50 {
			return AccountOptionsQuery{}, errors.New("模型检测账户选项 limit 必须是 1 到 50 的整数")
		}
		limit = parsed
	}
	selectedRaw, hasSelected := q["selectedIds"]
	_, hasBracketSelected := q["selectedIds[]"]
	if hasSelected && hasBracketSelected {
		return AccountOptionsQuery{}, errors.New("模型检测账户选项 selectedIds 无效")
	}
	if !hasSelected {
		selectedRaw = q["selectedIds[]"]
	}
	selected := make([]string, 0, len(selectedRaw))
	for _, value := range selectedRaw {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 120 || strings.ContainsAny(value, ",[]") {
			return AccountOptionsQuery{}, errors.New("模型检测账户选项 selectedIds 无效")
		}
		selected = append(selected, value)
	}
	if len(selected) > 20 {
		return AccountOptionsQuery{}, errors.New("模型检测账户选项 selectedIds 无效")
	}
	if accountID != "" && (keyword != "" || len(selected) > 0 || limit != 1) {
		return AccountOptionsQuery{}, errors.New("模型检测账户定点模型选项只接受 accountId、purpose 和 limit=1")
	}
	if accountID != "" {
		limit = 1
	}
	return AccountOptionsQuery{Purpose: purpose, AccountID: accountID, Keyword: keyword, SelectedID: selected, Limit: limit}, nil
}

func (h *HTTPHandler) quality() (QualityManagement, error) {
	if h == nil || h.Quality == nil {
		return nil, errors.New("J3b Gateway 质量管理 owner 未完成接线")
	}
	return h.Quality, nil
}
func (h *HTTPHandler) serveQualityPolicy(w http.ResponseWriter, r *http.Request, systemID string) {
	q, err := h.quality()
	if err == nil {
		var v QualityPolicyView
		v, err = q.Policy(r.Context(), systemID)
		if err == nil {
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": v})
			return
		}
	}
	writeOwnerError(w, http.StatusInternalServerError, err.Error())
}

func (h *HTTPHandler) serveScopedQualityPolicy(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.serveQualityPolicy(w, r, systemID)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func (h *HTTPHandler) patchQualityPolicy(w http.ResponseWriter, r *http.Request, systemID string) {
	var input QualityPolicyPatch
	if err := decodeOwnerJSON(r, h.maxBody(), &input); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := h.quality()
	if err == nil {
		var v QualityPolicyView
		v, err = q.PatchPolicy(r.Context(), systemID, input)
		if err == nil {
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": v})
			return
		}
	}
	writeQualityError(w, err)
}

func (h *HTTPHandler) patchScopedQualityPolicy(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.patchQualityPolicy(w, r, systemID)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func (h *HTTPHandler) listQualitySchedules(w http.ResponseWriter, r *http.Request, systemID string) {
	q, err := h.quality()
	if err == nil {
		page, size, parseErr := parsePage(r)
		if parseErr != nil {
			writeOwnerError(w, http.StatusBadRequest, parseErr.Error())
			return
		}
		var v QualityScheduleList
		v, err = q.ListSchedules(r.Context(), systemID, page, size)
		if err == nil {
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": v})
			return
		}
	}
	writeOwnerError(w, http.StatusInternalServerError, err.Error())
}

func (h *HTTPHandler) listScopedQualitySchedules(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.listQualitySchedules(w, r, systemID)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func (h *HTTPHandler) createQualitySchedule(w http.ResponseWriter, r *http.Request, systemID string) {
	var input QualityScheduleInput
	if err := decodeOwnerJSON(r, h.maxBody(), &input); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := h.quality()
	if err == nil {
		var v QualityScheduleView
		v, err = q.CreateSchedule(r.Context(), systemID, input)
		if err == nil {
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": v})
			return
		}
	}
	writeQualityError(w, err)
}

func (h *HTTPHandler) createScopedQualitySchedule(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.createQualitySchedule(w, r, systemID)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func (h *HTTPHandler) patchQualitySchedule(w http.ResponseWriter, r *http.Request, systemID, id string) {
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	var input QualitySchedulePatch
	if err := decodeOwnerJSON(r, h.maxBody(), &input); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	q, err := h.quality()
	if err == nil {
		var v QualityScheduleView
		v, err = q.PatchSchedule(r.Context(), systemID, id, input)
		if err == nil {
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": v})
			return
		}
	}
	writeQualityError(w, err)
}

func (h *HTTPHandler) patchScopedQualitySchedule(w http.ResponseWriter, r *http.Request, scope ManagementScope, id string) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.patchQualitySchedule(w, r, systemID, id)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func (h *HTTPHandler) deleteQualitySchedule(w http.ResponseWriter, r *http.Request, systemID, id string) {
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	q, err := h.quality()
	if err == nil {
		var deleted bool
		deleted, err = q.DeleteSchedule(r.Context(), systemID, id)
		if err == nil {
			if !deleted {
				http.NotFound(w, r)
				return
			}
			writeOwnerJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
			return
		}
	}
	writeOwnerError(w, http.StatusInternalServerError, err.Error())
}

func (h *HTTPHandler) deleteScopedQualitySchedule(w http.ResponseWriter, r *http.Request, scope ManagementScope, id string) {
	if systemID, ok := scope.specificSystemAccountID(); ok {
		h.deleteQualitySchedule(w, r, systemID, id)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, "请先选择具体系统账户")
}
func decodeOwnerJSON(r *http.Request, max int64, target any) error {
	if r.Body == nil {
		return errors.New("请求体不能为空")
	}
	d := json.NewDecoder(io.LimitReader(r.Body, max))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("请求体无效")
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return errors.New("请求体必须是单个 JSON 对象")
	}
	return nil
}
func writeQualityError(w http.ResponseWriter, err error) {
	if err == nil {
		err = errors.New("质量管理操作失败")
	}
	if strings.Contains(err.Error(), "已被其他操作修改") || strings.Contains(err.Error(), "已变化") {
		writeOwnerError(w, http.StatusConflict, err.Error())
		return
	}
	if strings.Contains(err.Error(), "不存在") {
		writeOwnerError(w, http.StatusNotFound, err.Error())
		return
	}
	writeOwnerError(w, http.StatusBadRequest, err.Error())
}

func (h *HTTPHandler) serveRun(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	command, err := decodeRunCommand(r, h.maxBody())
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	runRequest, err := h.buildRequest(r.Context(), scope, command)
	if err != nil {
		writeBuildError(w, err)
		return
	}
	if err := validateBuiltRequest(scope, command, runRequest); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := scope.activeKey()
	handle, acquired, current := h.Active.TryStart(r.Context(), key, modelcheckactive.Summary{TargetID: runRequest.TargetID, Model: runRequest.Model, Profile: runRequest.Profile, StartedAt: time.Now().UTC()})
	if !acquired {
		w.Header().Set("Retry-After", "1")
		writeOwnerActiveConflict(w, current)
		return
	}
	defer handle.Finish()
	result, err := h.Service.Run(handle.Context(), runRequest)
	if result.RunID != "" {
		handle.Update(modelcheckactive.Summary{RunID: result.RunID})
	}
	if err != nil {
		writeRunError(w, err)
		return
	}
	h.writeRunDetail(w, r, result, scope)
}

func (h *HTTPHandler) serveStream(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	command, err := decodeRunCommand(r, h.maxBody())
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	runRequest, err := h.buildRequest(r.Context(), scope, command)
	if err != nil {
		writeBuildError(w, err)
		return
	}
	if err := validateBuiltRequest(scope, command, runRequest); err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := scope.activeKey()
	handle, acquired, current := h.Active.TryStart(r.Context(), key, modelcheckactive.Summary{TargetID: runRequest.TargetID, Model: runRequest.Model, Profile: runRequest.Profile, StartedAt: time.Now().UTC()})
	if !acquired {
		w.Header().Set("Retry-After", "1")
		writeOwnerActiveConflict(w, current)
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
	w.Header().Set("X-Accel-Buffering", "no")
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
				_ = writeEvent("error", ownerStreamError(outcome.err))
				return
			}
			detail, detailErr := h.runDetailForStream(r.Context(), outcome.result, scope)
			if detailErr != nil {
				_ = writeEvent("error", ownerStreamError(detailErr))
				return
			}
			_ = writeEvent("complete", detail)
			return
		case <-handle.Context().Done():
			return
		}
	}
}

func (h *HTTPHandler) writeRunDetail(w http.ResponseWriter, r *http.Request, result RunResult, scope ManagementScope) {
	detail, err := h.runDetail(r.Context(), result)
	if err != nil {
		writeRunError(w, err)
		return
	}
	if !scopeAllowsRun(scope, detail) {
		writeOwnerError(w, http.StatusNotFound, "模型检测记录不存在")
		return
	}
	if detail, err = h.presentRunResult(detail); err != nil {
		writeRunError(w, err)
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": detail})
}

func (h *HTTPHandler) runDetailForStream(ctx context.Context, result RunResult, scope ManagementScope) (any, error) {
	detail, err := h.runDetail(ctx, result)
	if err != nil {
		return nil, err
	}
	if !scopeAllowsRun(scope, detail) {
		return nil, &RequestError{StatusCode: http.StatusNotFound, Message: "模型检测记录不存在"}
	}
	return h.presentRunResult(detail)
}

// presentRunResult keeps the owner-facing record intact while applying the
// same system-account field boundary as Node's self management namespace.
// Redaction is performed on a marshalled copy to cover the durable JSON
// summaries too; if a value cannot be copied safely, the self response fails
// closed instead of leaking a caller's tenant metadata.
func (h *HTTPHandler) presentRunResult(value any) (any, error) {
	if h == nil || !h.ForceActorScope {
		return value, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, &RequestError{StatusCode: http.StatusInternalServerError, Message: "模型检测 self 响应脱敏失败"}
	}
	var copied any
	if err := json.Unmarshal(payload, &copied); err != nil {
		return nil, &RequestError{StatusCode: http.StatusInternalServerError, Message: "模型检测 self 响应脱敏失败"}
	}
	redactSystemAccountFields(copied)
	return copied, nil
}

func redactSystemAccountFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"systemAccountId", "actorSystemAccountId", "targetOwnerSystemAccountId"} {
			delete(typed, key)
		}
		for _, child := range typed {
			redactSystemAccountFields(child)
		}
	case []any:
		for _, child := range typed {
			redactSystemAccountFields(child)
		}
	}
}

func (h *HTTPHandler) runDetail(ctx context.Context, result RunResult) (detail any, err error) {
	if strings.TrimSpace(result.RunID) == "" {
		return nil, &RequestError{StatusCode: http.StatusInternalServerError, Message: "模型检测完成后缺少运行 ID"}
	}
	detail, found, err := h.Service.GetRun(ctx, result.RunID)
	if err != nil {
		return nil, err
	}
	if !found || detail == nil {
		return nil, &RequestError{StatusCode: http.StatusInternalServerError, Message: "模型检测已完成但持久化报告不可读取"}
	}
	if !hasCompleteRunDetailShape(detail) {
		// A 200 response here is consumed by the UI as ModelCheckRunDetail.
		// Returning an abbreviated durable summary would therefore be a silent,
		// incompatible fallback. Keep the owner closed until the query/store
		// layer can supply Node's persisted report contract.
		return nil, &RequestError{StatusCode: http.StatusServiceUnavailable, Message: "模型检测已完成但完整持久化报告尚未就绪"}
	}
	return detail, nil
}

func hasCompleteRunDetailShape(value any) bool {
	payload, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return false
	}
	for _, key := range []string{"id", "requestSummary", "resultSummary", "checks"} {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func writeRunError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var requestError *RequestError
	if errors.As(err, &requestError) && requestError.StatusCode >= http.StatusBadRequest && requestError.StatusCode <= 599 {
		status = requestError.StatusCode
	}
	message := "模型检测失败"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	writeOwnerError(w, status, message)
}

func writeBuildError(w http.ResponseWriter, err error) {
	var requestError *RequestError
	if errors.As(err, &requestError) {
		writeRunError(w, err)
		return
	}
	writeOwnerError(w, http.StatusBadRequest, err.Error())
}

func ownerStreamError(err error) map[string]any {
	message := "模型检测失败"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	payload := map[string]any{"message": message}
	var requestError *RequestError
	if errors.As(err, &requestError) && requestError.StatusCode >= http.StatusBadRequest && requestError.StatusCode <= 599 {
		payload["statusCode"] = requestError.StatusCode
	}
	return payload
}

func (h *HTTPHandler) serveActive(w http.ResponseWriter, scope ManagementScope) {
	if summary, ok := h.Active.Get(scope.activeKey()); ok {
		writeOwnerJSON(w, http.StatusOK, map[string]any{"data": summary})
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": nil})
}

func (h *HTTPHandler) serveStop(w http.ResponseWriter, scope ManagementScope) {
	summary, stopped := h.Active.Stop(scope.activeKey())
	var active any
	if stopped {
		active = summary
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"stopped": stopped, "active": active}})
}

func (h *HTTPHandler) serveList(w http.ResponseWriter, r *http.Request, scope ManagementScope) {
	query, err := parseRunListQuery(r)
	if err != nil {
		writeOwnerError(w, http.StatusBadRequest, err.Error())
		return
	}
	query.SystemAccountID = scope.SelectedSystemAccountID
	query.AllSystemAccounts = scope.AllSystemAccounts
	result, err := h.Service.ListRuns(r.Context(), query)
	if err != nil {
		writeOwnerError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result, err = h.presentRunResult(result); err != nil {
		writeRunError(w, err)
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": result})
}

func (h *HTTPHandler) serveDetail(w http.ResponseWriter, r *http.Request, runID string, scope ManagementScope) {
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
	if !scopeAllowsRun(scope, result) {
		http.NotFound(w, r)
		return
	}
	if result, err = h.presentRunResult(result); err != nil {
		writeRunError(w, err)
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{"data": result})
}

func scopeAllowsRun(scope ManagementScope, result any) bool {
	if !scope.valid() {
		return false
	}
	systemAccountID, ok := resultSystemAccountID(result)
	return ok && (scope.AllSystemAccounts || systemAccountID == scope.SelectedSystemAccountID)
}

func resultSystemAccountID(result any) (string, bool) {
	switch value := result.(type) {
	case RunView:
		return value.SystemAccountID, true
	case RunDetail:
		return value.SystemAccountID, true
	case map[string]any:
		systemAccountID, ok := value["systemAccountId"].(string)
		return strings.TrimSpace(systemAccountID), ok && strings.TrimSpace(systemAccountID) != ""
	default:
		return "", false
	}
}

func parseRunListQuery(r *http.Request) (RunListQuery, error) {
	page, pageSize, err := parsePage(r)
	if err != nil {
		return RunListQuery{}, err
	}
	values := r.URL.Query()
	return RunListQuery{
		Page:        page,
		PageSize:    pageSize,
		TargetID:    strings.TrimSpace(values.Get("targetId")),
		Model:       strings.TrimSpace(values.Get("model")),
		Level:       strings.TrimSpace(values.Get("level")),
		Status:      strings.TrimSpace(values.Get("status")),
		TriggerKind: strings.TrimSpace(values.Get("triggerKind")),
		StartAt:     strings.TrimSpace(values.Get("startAt")),
		EndAt:       strings.TrimSpace(values.Get("endAt")),
	}, nil
}

func parsePage(r *http.Request) (int, int, error) {
	page, pageSize := 1, 20
	if value := r.URL.Query().Get("page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return 0, 0, errors.New("分页参数 page 必须是正整数")
		}
		page = parsed
	}
	if value := r.URL.Query().Get("pageSize"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > 100 {
			return 0, 0, errors.New("分页参数 pageSize 必须是 1 到 100 之间的整数")
		}
		pageSize = parsed
	}
	return page, pageSize, nil
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

func validateBuiltRequest(scope ManagementScope, command RunCommand, request RunRequest) error {
	if !scope.valid() || strings.TrimSpace(request.SystemAccountID) == "" {
		return errors.New("模型检测请求 scope 不完整")
	}
	if !scope.AllSystemAccounts && strings.TrimSpace(request.SystemAccountID) != strings.TrimSpace(scope.SelectedSystemAccountID) {
		return errors.New("模型检测请求 scope 与选中账号不一致")
	}
	if strings.TrimSpace(request.ActorSystemAccountID) != strings.TrimSpace(scope.ActorSystemAccountID) {
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

func (h *HTTPHandler) buildRequest(ctx context.Context, scope ManagementScope, command RunCommand) (RunRequest, error) {
	if h.BuildScoped != nil {
		return h.BuildScoped(ctx, scope, command)
	}
	if scope.AllSystemAccounts || strings.TrimSpace(scope.SelectedSystemAccountID) == "" {
		return RunRequest{}, errors.New("J3b 管理员全局 scope 尚未接入目标租户构建器")
	}
	return h.Build(ctx, scope.SelectedSystemAccountID, command)
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
	// Node routes expose a root message for all caller-visible failures. Keep
	// the former nested message during the local owner transition so existing
	// Gateway-only callers do not lose their error extraction path.
	writeOwnerJSON(w, status, map[string]any{
		"message": message,
		"error":   map[string]any{"message": message},
	})
}

func writeOwnerErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeOwnerJSON(w, status, map[string]any{
		"message": message,
		"code":    code,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func writeOwnerActiveConflict(w http.ResponseWriter, active modelcheckactive.Summary) {
	const message = "当前用户已有模型检测正在运行，请等待完成或先手动停止"
	writeOwnerJSON(w, http.StatusConflict, map[string]any{
		"message": message,
		"active":  active,
		// Retain the previous local shape while matching Node's root fields.
		"error": map[string]any{"code": "active", "message": message, "active": active},
	})
}
