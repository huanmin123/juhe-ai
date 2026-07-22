package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementresponseinspectionpolicies"
	"juhe-ai/backend-go/internal/store/port"
)

const responseInspectionPolicyMaxBodyBytes int64 = 256 << 10

type managementResponseInspectionPolicyService interface {
	List(context.Context) (managementresponseinspectionpolicies.ListResult, error)
	Create(context.Context, managementresponseinspectionpolicies.Input) (port.ResponseInspectionPolicy, error)
	Update(context.Context, string, managementresponseinspectionpolicies.Input) (port.ResponseInspectionPolicy, error)
	Delete(context.Context, string) (port.ResponseInspectionPolicy, error)
}

func NewManagementResponseInspectionPoliciesHandlerWithOperationLog(
	service *managementresponseinspectionpolicies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementResponseInspectionPoliciesHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementResponseInspectionPoliciesHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementResponseInspectionPoliciesHandler(
	service managementResponseInspectionPolicyService,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		switch r.Method {
		case http.MethodGet:
			result, err := service.List(r.Context())
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			writeData(w, http.StatusOK, result)
		case http.MethodPost:
			input, decoded := decodeManagementResponseInspectionPolicyPayload(w, r)
			if !decoded {
				return
			}
			policy, err := service.Create(r.Context(), input)
			if writeManagementResponseInspectionPolicyError(w, err) {
				return
			}
			w.Header().Set("Pragma", "no-cache")
			writeData(w, http.StatusCreated, policy)
			recordManagementResponseInspectionPolicyOperation(r, authContext, "create", policy, http.StatusCreated, logOptions)
		case http.MethodPut:
			input, decoded := decodeManagementResponseInspectionPolicyPayload(w, r)
			if !decoded {
				return
			}
			policy, err := service.Update(r.Context(), chi.URLParam(r, "id"), input)
			if writeManagementResponseInspectionPolicyError(w, err) {
				return
			}
			w.Header().Set("Pragma", "no-cache")
			writeData(w, http.StatusOK, policy)
			recordManagementResponseInspectionPolicyOperation(r, authContext, "update", policy, http.StatusOK, logOptions)
		case http.MethodDelete:
			policy, err := service.Delete(r.Context(), chi.URLParam(r, "id"))
			if writeManagementResponseInspectionPolicyError(w, err) {
				return
			}
			w.Header().Set("Pragma", "no-cache")
			writeData(w, http.StatusOK, struct {
				Deleted bool `json:"deleted"`
			}{Deleted: true})
			recordManagementResponseInspectionPolicyOperation(r, authContext, "delete", policy, http.StatusOK, logOptions)
		default:
			writeMessageError(w, http.StatusMethodNotAllowed, "请求方法无效")
		}
	})
}

type managementResponseInspectionPolicyPayload struct {
	Name         *string         `json:"name"`
	Enabled      json.RawMessage `json:"enabled"`
	Priority     json.RawMessage `json:"priority"`
	ScopeType    *string         `json:"scopeType"`
	ProtocolCode *string         `json:"protocolCode"`
	ProviderCode *string         `json:"providerCode"`
	Match        json.RawMessage `json:"match"`
	Action       *string         `json:"action"`
	Notes        *string         `json:"notes"`
}

type managementResponseInspectionPolicyMatchPayload struct {
	ClientProfiles       strictResponseInspectionPolicyStringList `json:"clientProfiles"`
	OutputTextIncludes   strictResponseInspectionPolicyStringList `json:"outputTextIncludes"`
	OutputTextExcludes   strictResponseInspectionPolicyStringList `json:"outputTextExcludes"`
	ErrorCodes           strictResponseInspectionPolicyStringList `json:"errorCodes"`
	ErrorTypes           strictResponseInspectionPolicyStringList `json:"errorTypes"`
	ErrorMessageIncludes strictResponseInspectionPolicyStringList `json:"errorMessageIncludes"`
	FinishReasons        strictResponseInspectionPolicyStringList `json:"finishReasons"`
	JSONPathsExists      strictResponseInspectionPolicyStringList `json:"jsonPathsExists"`
	RawTextIncludes      strictResponseInspectionPolicyStringList `json:"rawTextIncludes"`
}

type strictResponseInspectionPolicyStringList []string

func (values *strictResponseInspectionPolicyStringList) UnmarshalJSON(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return errors.New("string list cannot be null")
	}
	var decoded []string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*values = decoded
	return nil
}

func decodeManagementResponseInspectionPolicyPayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementresponseinspectionpolicies.Input, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, responseInspectionPolicyMaxBodyBytes))
	decoder.DisallowUnknownFields()
	var payload managementResponseInspectionPolicyPayload
	if err := decoder.Decode(&payload); err != nil {
		writeManagementResponseInspectionPolicyDecodeError(w, err)
		return managementresponseinspectionpolicies.Input{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeManagementResponseInspectionPolicyDecodeError(w, err)
		return managementresponseinspectionpolicies.Input{}, false
	}
	if payload.Name == nil || payload.ScopeType == nil || payload.ProtocolCode == nil || payload.Action == nil {
		writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
		return managementresponseinspectionpolicies.Input{}, false
	}
	enabled, ok := decodeResponseInspectionPolicyOptionalBool(w, payload.Enabled)
	if !ok {
		return managementresponseinspectionpolicies.Input{}, false
	}
	priority, ok := decodeResponseInspectionPolicyOptionalInt(w, payload.Priority)
	if !ok {
		return managementresponseinspectionpolicies.Input{}, false
	}
	match := port.ResponseInspectionPolicyMatch{}
	if len(payload.Match) > 0 {
		if strings.TrimSpace(string(payload.Match)) == "null" {
			writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
			return managementresponseinspectionpolicies.Input{}, false
		}
		var matchPayload managementResponseInspectionPolicyMatchPayload
		matchDecoder := json.NewDecoder(strings.NewReader(string(payload.Match)))
		matchDecoder.DisallowUnknownFields()
		if err := matchDecoder.Decode(&matchPayload); err != nil {
			writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
			return managementresponseinspectionpolicies.Input{}, false
		}
		var matchExtra any
		if err := matchDecoder.Decode(&matchExtra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
			return managementresponseinspectionpolicies.Input{}, false
		}
		match = port.ResponseInspectionPolicyMatch{
			ClientProfiles:       append([]string(nil), matchPayload.ClientProfiles...),
			OutputTextIncludes:   append([]string(nil), matchPayload.OutputTextIncludes...),
			OutputTextExcludes:   append([]string(nil), matchPayload.OutputTextExcludes...),
			ErrorCodes:           append([]string(nil), matchPayload.ErrorCodes...),
			ErrorTypes:           append([]string(nil), matchPayload.ErrorTypes...),
			ErrorMessageIncludes: append([]string(nil), matchPayload.ErrorMessageIncludes...),
			FinishReasons:        append([]string(nil), matchPayload.FinishReasons...),
			JSONPathsExists:      append([]string(nil), matchPayload.JSONPathsExists...),
			RawTextIncludes:      append([]string(nil), matchPayload.RawTextIncludes...),
		}
	}
	return managementresponseinspectionpolicies.Input{
		Name: *payload.Name, Enabled: enabled, Priority: priority,
		ScopeType: *payload.ScopeType, ProtocolCode: *payload.ProtocolCode,
		ProviderCode: payload.ProviderCode, Match: match, Action: *payload.Action, Notes: payload.Notes,
	}, true
}

func decodeResponseInspectionPolicyOptionalBool(w http.ResponseWriter, raw json.RawMessage) (*bool, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
		return nil, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
		return nil, false
	}
	return &value, true
}

func decodeResponseInspectionPolicyOptionalInt(w http.ResponseWriter, raw json.RawMessage) (*int, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
		return nil, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
		return nil, false
	}
	return &value, true
}

func writeManagementResponseInspectionPolicyDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "响应检查策略参数无效")
}

func writeManagementResponseInspectionPolicyError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if managementresponseinspectionpolicies.IsNotFound(err) {
		writeMessageError(w, http.StatusNotFound, "响应检查策略不存在")
		return true
	}
	if managementresponseinspectionpolicies.IsConflict(err) {
		writeMessageError(w, http.StatusConflict, managementresponseinspectionpolicies.ErrorMessage(err))
		return true
	}
	if message := managementresponseinspectionpolicies.ValidationMessage(err); message != "" {
		writeMessageError(w, http.StatusBadRequest, message)
		return true
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	return true
}

func recordManagementResponseInspectionPolicyOperation(
	r *http.Request,
	authContext managementauth.Context,
	action string,
	policy port.ResponseInspectionPolicy,
	statusCode int,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.newLogID
	if newLogID == nil {
		newLogID = defaultManagementOperationLogID
	}
	changes := []port.OperationLogChange{{Field: "deleted", Label: "删除", Before: nil, After: true}}
	if action != "delete" {
		changes = []port.OperationLogChange{
			{Field: "name", Label: "规则名称", Before: nil, After: policy.Name},
			{Field: "protocolCode", Label: "协议", Before: nil, After: policy.ProtocolCode},
			{Field: "scopeType", Label: "作用层级", Before: nil, After: policy.ScopeType},
			{Field: "providerCode", Label: "供应商", Before: nil, After: responseInspectionPolicyOptionalText(policy.ProviderCode)},
			{Field: "enabled", Label: "启用状态", Before: nil, After: policy.Enabled},
			{Field: "priority", Label: "优先级", Before: nil, After: policy.Priority},
			{Field: "action", Label: "动作", Before: nil, After: policy.Action},
			{Field: "matchSummary", Label: "匹配条件摘要", Before: nil, After: responseInspectionPolicyMatchSummary(policy.Match)},
		}
	}
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID: newLogID(), TraceID: requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID, ActorUsername: authContext.Username,
		ActorDisplayName: authContext.DisplayName, ActorRole: authContext.Role,
		// This endpoint is admin-only; classify the operation as an admin action
		// even though the actor is the same system account that owns the session.
		Mode: "admin", Module: "response_inspection_policies", Action: action,
		OperationKey: "response_inspection_policies." + action,
		ResourceType: "response_inspection_policy", ResourceID: policy.ID,
		ResourceName: policy.Name, Summary: responseInspectionPolicyOperationSummary(action, policy.Name),
		DetailLevel: "full", VisibilityScope: "admin_only", Changes: changes,
		Metadata: map[string]any{"policyId": policy.ID, "actorSystemAccountId": authContext.SystemAccountID},
		Method:   r.Method, Path: r.URL.Path, StatusCode: &statusCode,
		ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(), CreatedAt: now().UTC(),
	})
}

func responseInspectionPolicyMatchSummary(match port.ResponseInspectionPolicyMatch) map[string]int {
	return map[string]int{
		"clientProfiles":       len(match.ClientProfiles),
		"outputTextIncludes":   len(match.OutputTextIncludes),
		"outputTextExcludes":   len(match.OutputTextExcludes),
		"errorCodes":           len(match.ErrorCodes),
		"errorTypes":           len(match.ErrorTypes),
		"errorMessageIncludes": len(match.ErrorMessageIncludes),
		"finishReasons":        len(match.FinishReasons),
		"jsonPathsExists":      len(match.JSONPathsExists),
		"rawTextIncludes":      len(match.RawTextIncludes),
	}
}

func responseInspectionPolicyOptionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func responseInspectionPolicyOperationSummary(action string, name string) string {
	switch action {
	case "create":
		return "创建响应检查策略：" + name
	case "update":
		return "更新响应检查策略：" + name
	default:
		return "删除响应检查策略：" + name
	}
}

var _ managementResponseInspectionPolicyService = (*managementresponseinspectionpolicies.Service)(nil)
