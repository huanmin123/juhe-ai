package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

type managementGroupOptionScope int

const (
	managementGroupScopeAdmin managementGroupOptionScope = iota
	managementGroupScopeSelf

	managementGroupCreateMaxBodyBytes = 256 << 10
)

type managementGroupOptionService interface {
	Options(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error)
}

type managementGroupAccountOptionService interface {
	AccountOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error)
}

type managementGroupAuthorizationOptionService interface {
	AuthorizationOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AuthorizationOption, error)
}

type managementGroupCreateService interface {
	Create(r *http.Request, input managementgroups.CreateInput) (managementgroups.CreateResult, error)
}

func managementGroupCreateJSONBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isJSON, charsetSupported := managementGroupCreateJSONContentType(r.Header.Get("Content-Type"))
		if !charsetSupported {
			writeMessageError(w, http.StatusUnsupportedMediaType, "请求体无效")
			return
		}
		if !isJSON {
			_ = r.Body.Close()
			r.Body = io.NopCloser(strings.NewReader("{}"))
			r.ContentLength = 2
			next.ServeHTTP(w, r)
			return
		}
		limited := http.MaxBytesReader(w, r.Body, managementGroupCreateMaxBodyBytes)
		body, err := io.ReadAll(limited)
		if err != nil {
			writeManagementGroupCreateBodyError(w, err)
			return
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			body = []byte("{}")
		} else if (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid(body) {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

func managementGroupCreateJSONContentType(value string) (isJSON bool, charsetSupported bool) {
	if strings.TrimSpace(value) == "" {
		return false, true
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false, true
	}
	charset := strings.ToLower(strings.TrimSpace(params["charset"]))
	return true, charset == "" || charset == "utf-8"
}

func managementGroupAdminRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type managementGroupOptionServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupOptionServiceAdapter) Options(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementGroupOptionServiceAdapter) AccountOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error) {
	return s.service.AccountOptions(r.Context(), input)
}

func (s managementGroupOptionServiceAdapter) AuthorizationOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AuthorizationOption, error) {
	return s.service.AuthorizationOptions(r.Context(), input)
}

func (s managementGroupOptionServiceAdapter) Create(r *http.Request, input managementgroups.CreateInput) (managementgroups.CreateResult, error) {
	return s.service.Create(r.Context(), input)
}

func NewManagementGroupCreateHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupCreateHandler(
		managementGroupOptionServiceAdapter{service: service},
		managementGroupScopeAdmin,
		managementOperationLogOptions{},
	)
}

func NewManagementMyGroupCreateHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupCreateHandler(
		managementGroupOptionServiceAdapter{service: service},
		managementGroupScopeSelf,
		managementOperationLogOptions{},
	)
}

func NewManagementGroupCreateHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupCreateHandler(
		managementGroupOptionServiceAdapter{service: service},
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyGroupCreateHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupCreateHandler(
		managementGroupOptionServiceAdapter{service: service},
		managementGroupScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementGroupOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeAdmin)
}

func NewManagementMyGroupOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeSelf)
}

func NewManagementGroupAuthorizationOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAuthorizationOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeAdmin)
}

func NewManagementMyGroupAuthorizationOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAuthorizationOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeSelf)
}

func NewManagementGroupAccountOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAccountOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeAdmin)
}

func NewManagementMyGroupAccountOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAccountOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeSelf)
}

func newManagementGroupCreateHandler(
	service managementGroupCreateService,
	scope managementGroupOptionScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementGroupScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		ownerSystemAccountID, includeSystemAccountFields, validScope := managementGroupCreateScope(
			authContext,
			r.URL.Query(),
			scope,
		)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		payload, ok := decodeManagementGroupCreatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Create(r, managementgroups.CreateInput{
			SystemAccountID:            ownerSystemAccountID,
			IncludeSystemAccountFields: includeSystemAccountFields,
			Name:                       payload.Name,
			ProviderCode:               payload.ProviderCode,
			Description:                payload.Description,
			Enabled:                    payload.Enabled,
			GroupType:                  payload.GroupType,
			SchedulingPolicy:           payload.SchedulingPolicy,
		})
		if !writeManagementGroupCreateError(w, err) {
			return
		}
		recordManagementGroupCreateOperationLog(
			r,
			authContext,
			scope,
			ownerSystemAccountID,
			result,
			logOptions,
		)
		writeData(w, http.StatusCreated, result)
	})
}

func newManagementGroupOptionsHandler(service managementGroupOptionService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementGroupOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementGroupAccountOptionsHandler(service managementGroupAccountOptionService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementGroupOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.AccountOptions(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementGroupAuthorizationOptionsHandler(service managementGroupAuthorizationOptionService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementGroupOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.AuthorizationOptions(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

type managementGroupCreatePayload struct {
	Name             string
	ProviderCode     string
	Description      *string
	Enabled          *bool
	GroupType        string
	SchedulingPolicy *managementgroups.SchedulingPolicyInput
}

func managementGroupCreateScope(
	authContext managementauth.Context,
	values url.Values,
	scope managementGroupOptionScope,
) (string, bool, bool) {
	ownerSystemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	switch scope {
	case managementGroupScopeSelf:
		return ownerSystemAccountID, false, true
	case managementGroupScopeAdmin:
		rawValues, exists := values["systemAccountId"]
		if !exists {
			return ownerSystemAccountID, true, true
		}
		if len(rawValues) != 1 {
			return "", false, false
		}
		selectedSystemAccountID := strings.TrimSpace(rawValues[0])
		if selectedSystemAccountID == "" {
			return "", false, false
		}
		if selectedSystemAccountID == "all" {
			return ownerSystemAccountID, true, true
		}
		return selectedSystemAccountID, true, true
	default:
		return "", false, false
	}
}

func decodeManagementGroupCreatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementGroupCreatePayload, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, managementGroupCreateMaxBodyBytes))
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementGroupCreatePayload{}, false
	}
	if !json.Valid(body) {
		writeManagementGroupCreateBodyError(w, nil)
		return managementGroupCreatePayload{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupCreatePayload{}, false
	}
	for field := range raw {
		switch field {
		case "name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy":
		default:
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupCreatePayload{}, false
		}
	}

	name, ok := requiredManagementGroupString(raw, "name")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupCreatePayload{}, false
	}
	providerCode, ok := requiredManagementGroupString(raw, "providerCode")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupCreatePayload{}, false
	}
	payload := managementGroupCreatePayload{
		Name:         name,
		ProviderCode: providerCode,
	}
	if value, exists := raw["description"]; exists {
		var description string
		if isManagementGroupJSONNull(value) || json.Unmarshal(value, &description) != nil {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupCreatePayload{}, false
		}
		description = strings.TrimSpace(description)
		payload.Description = &description
	}
	if value, exists := raw["enabled"]; exists {
		var enabled bool
		if isManagementGroupJSONNull(value) || json.Unmarshal(value, &enabled) != nil {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupCreatePayload{}, false
		}
		payload.Enabled = &enabled
	}
	if value, exists := raw["groupType"]; exists {
		var groupType string
		if err := json.Unmarshal(value, &groupType); err != nil {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupCreatePayload{}, false
		}
		if groupType != "personal" && groupType != "high_concurrency" {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupCreatePayload{}, false
		}
		payload.GroupType = groupType
	}
	if value, exists := raw["schedulingPolicy"]; exists {
		policy, ok := decodeManagementGroupSchedulingPolicy(w, value)
		if !ok {
			return managementGroupCreatePayload{}, false
		}
		payload.SchedulingPolicy = policy
	}
	return payload, true
}

func decodeManagementGroupSchedulingPolicy(
	w http.ResponseWriter,
	value json.RawMessage,
) (*managementgroups.SchedulingPolicyInput, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(value, &raw); err != nil || raw == nil {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return nil, false
	}
	for field := range raw {
		switch field {
		case "defaultSoftConcurrency",
			"maxQueueWaitMs",
			"clientIpConcurrencyLimit",
			"clientIpConcurrencyOverflowMode",
			"imageLaneMaxConcurrency":
		default:
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
	}
	policy := &managementgroups.SchedulingPolicyInput{}
	var ok bool
	if value, exists := raw["defaultSoftConcurrency"]; exists {
		policy.DefaultSoftConcurrency, ok = managementGroupPolicyInteger(value, 1, 1000000)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
	}
	if value, exists := raw["maxQueueWaitMs"]; exists {
		policy.MaxQueueWaitMs, ok = managementGroupPolicyInteger(value, 1, 3600000)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
	}
	if value, exists := raw["clientIpConcurrencyLimit"]; exists {
		policy.ClientIPConcurrencyLimit, ok = managementGroupPolicyInteger(value, 0, 1000000)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
	}
	if value, exists := raw["clientIpConcurrencyOverflowMode"]; exists {
		var mode string
		if err := json.Unmarshal(value, &mode); err != nil || (mode != "reject" && mode != "queue") {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
		policy.ClientIPConcurrencyOverflowMode = &mode
	}
	if value, exists := raw["imageLaneMaxConcurrency"]; exists {
		policy.ImageLaneMaxConcurrency, ok = managementGroupPolicyInteger(value, 0, 1000000)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return nil, false
		}
	}
	return policy, true
}

func managementGroupPolicyInteger(raw json.RawMessage, minimum int, maximum int) (*int, bool) {
	var value float64
	if isManagementGroupJSONNull(raw) ||
		json.Unmarshal(raw, &value) != nil ||
		math.IsNaN(value) ||
		math.IsInf(value, 0) ||
		value != math.Trunc(value) ||
		value < float64(minimum) ||
		value > float64(maximum) {
		return nil, false
	}
	result := int(value)
	return &result, true
}

func isManagementGroupJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func requiredManagementGroupString(raw map[string]json.RawMessage, field string) (string, bool) {
	value, exists := raw[field]
	if !exists {
		return "", false
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

func writeManagementGroupCreateBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "请求体无效")
}

func writeManagementGroupCreateError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, managementgroups.ErrSystemAccountNotFound) {
		writeMessageError(w, http.StatusBadRequest, "目标系统账户不存在")
		return false
	}
	if message, ok := managementgroups.ProviderNotFoundMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return false
	}
	if message, ok := managementgroups.ProviderDisabledMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return false
	}
	if message, ok := managementgroups.NameExistsMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return false
	}
	if _, ok := managementgroups.ValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return false
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	return false
}

func recordManagementGroupCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementGroupOptionScope,
	ownerSystemAccountID string,
	result managementgroups.CreateResult,
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
	mode := "self"
	if scope == managementGroupScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusCreated
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: ownerSystemAccountID,
		Mode:                          mode,
		Module:                        "groups",
		Action:                        "create",
		OperationKey:                  "groups.create",
		ResourceType:                  "group",
		ResourceID:                    result.ID,
		ResourceName:                  result.Name,
		Summary:                       "创建分组：" + result.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "名称", Before: nil, After: result.Name},
			{Field: "providerCode", Label: "供应商", Before: nil, After: result.ProviderCode},
			{Field: "groupType", Label: "分组类型", Before: nil, After: result.GroupType},
			{Field: "enabled", Label: "启用状态", Before: nil, After: result.Enabled},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  ownerSystemAccountID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementGroupOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementGroupOptionScope,
) (managementgroups.OptionListInput, bool) {
	input := parseManagementGroupOptionListQuery(values)
	switch scope {
	case managementGroupScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementgroups.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementGroupScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func parseManagementGroupOptionListQuery(values url.Values) managementgroups.OptionListInput {
	manageableOnly := false
	if value, ok := managementBooleanQueryValue(values, "manageableOnly"); ok {
		manageableOnly = value
	}
	preferDefault := false
	if value, ok := managementBooleanQueryValue(values, "preferDefault"); ok {
		preferDefault = value
	}
	return managementgroups.OptionListInput{
		IDs:            managementTextListQueryValue(values, "ids", 50),
		Keyword:        firstManagementQueryText(values, "keyword"),
		ProviderCode:   firstManagementQueryText(values, "providerCode"),
		Limit:          managementIntegerQueryValue(values, "limit"),
		ManageableOnly: manageableOnly,
		PreferDefault:  preferDefault,
	}
}
