package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproxies"
	"juhe-ai/backend-go/internal/store/port"
)

type managementProxyOptionService interface {
	List(r *http.Request, input managementproxies.ListInput) (managementproxies.ListResult, error)
	Options(r *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error)
}

type managementProxyMutationService interface {
	Create(r *http.Request, input managementproxies.CreateInput) (managementproxies.CreateResult, error)
	Update(r *http.Request, input managementproxies.UpdateInput) (managementproxies.UpdateResult, error)
	Delete(r *http.Request, input managementproxies.DeleteInput) (managementproxies.DeleteResult, error)
}

type managementProxyTestService interface {
	Find(r *http.Request, id string) (managementproxies.Summary, bool, error)
	Test(r *http.Request, input managementproxies.TestInput) (managementproxies.TestResult, error)
}

type managementProxyOptionServiceAdapter struct {
	service *managementproxies.Service
}

func (s managementProxyOptionServiceAdapter) Options(r *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementProxyOptionServiceAdapter) List(r *http.Request, input managementproxies.ListInput) (managementproxies.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementProxyOptionServiceAdapter) Create(r *http.Request, input managementproxies.CreateInput) (managementproxies.CreateResult, error) {
	return s.service.Create(r.Context(), input)
}

func (s managementProxyOptionServiceAdapter) Update(r *http.Request, input managementproxies.UpdateInput) (managementproxies.UpdateResult, error) {
	return s.service.Update(r.Context(), input)
}

func (s managementProxyOptionServiceAdapter) Delete(r *http.Request, input managementproxies.DeleteInput) (managementproxies.DeleteResult, error) {
	return s.service.Delete(r.Context(), input)
}

func (s managementProxyOptionServiceAdapter) Find(r *http.Request, id string) (managementproxies.Summary, bool, error) {
	return s.service.Find(r.Context(), id)
}

func (s managementProxyOptionServiceAdapter) Test(r *http.Request, input managementproxies.TestInput) (managementproxies.TestResult, error) {
	return s.service.Test(r.Context(), input)
}

func NewManagementProxiesHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxiesHandler(managementProxyOptionServiceAdapter{service: service})
}

func NewManagementProxyOptionsHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyOptionsHandler(managementProxyOptionServiceAdapter{service: service})
}

func NewManagementProxyCreateHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyCreateHandler(managementProxyOptionServiceAdapter{service: service}, managementOperationLogOptions{})
}

func NewManagementProxyCreateHandlerWithOperationLog(service *managementproxies.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProxyCreateHandler(managementProxyOptionServiceAdapter{service: service}, newManagementOperationLogOptions(opts))
}

func NewManagementProxyUpdateHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyUpdateHandler(managementProxyOptionServiceAdapter{service: service}, managementOperationLogOptions{})
}

func NewManagementProxyUpdateHandlerWithOperationLog(service *managementproxies.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProxyUpdateHandler(managementProxyOptionServiceAdapter{service: service}, newManagementOperationLogOptions(opts))
}

func NewManagementProxyDeleteHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyDeleteHandler(managementProxyOptionServiceAdapter{service: service}, managementOperationLogOptions{})
}

func NewManagementProxyDeleteHandlerWithOperationLog(service *managementproxies.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProxyDeleteHandler(managementProxyOptionServiceAdapter{service: service}, newManagementOperationLogOptions(opts))
}

func NewManagementProxyTestHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyTestHandler(managementProxyOptionServiceAdapter{service: service}, managementOperationLogOptions{}, sharedManagementDiagnosticTaskLimiter())
}

func NewManagementProxyTestHandlerWithOperationLog(service *managementproxies.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProxyTestHandler(managementProxyOptionServiceAdapter{service: service}, newManagementOperationLogOptions(opts), sharedManagementDiagnosticTaskLimiter())
}

func newManagementProxiesHandler(service managementProxyOptionService) http.Handler {
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
		result, err := service.List(r, parseManagementProxyListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementProxyOptionsHandler(service managementProxyOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := parseManagementProxyOptionListQuery(r.URL.Query())
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementProxyCreateHandler(service managementProxyMutationService, logOptions managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, allowed := requireManagementProxyAdmin(w, r)
		if !allowed {
			return
		}
		input, ok := decodeManagementProxyCreateInput(w, r, authContext.SystemAccountID)
		if !ok {
			return
		}
		result, err := service.Create(r, input)
		if !writeManagementProxyMutationError(w, err, http.StatusCreated) {
			return
		}
		recordProxyCreateOperationLog(r, authContext, result, logOptions)
		writeData(w, http.StatusCreated, result.Proxy)
	})
}

func newManagementProxyUpdateHandler(service managementProxyMutationService, logOptions managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, allowed := requireManagementProxyAdmin(w, r)
		if !allowed {
			return
		}
		input, ok := decodeManagementProxyUpdateInput(w, r, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		result, err := service.Update(r, input)
		if !writeManagementProxyMutationError(w, err, http.StatusOK) {
			return
		}
		recordProxyUpdateOperationLog(r, authContext, result, logOptions)
		writeData(w, http.StatusOK, result.Proxy)
	})
}

func newManagementProxyDeleteHandler(service managementProxyMutationService, logOptions managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, allowed := requireManagementProxyAdmin(w, r)
		if !allowed {
			return
		}
		result, err := service.Delete(r, managementproxies.DeleteInput{ID: chi.URLParam(r, "id")})
		if !writeManagementProxyMutationError(w, err, http.StatusNoContent) {
			return
		}
		recordProxyDeleteOperationLog(r, authContext, result, logOptions)
		w.WriteHeader(http.StatusNoContent)
	})
}

func newManagementProxyTestHandler(service managementProxyTestService, logOptions managementOperationLogOptions, limiter diagnosticTaskLimiter) http.Handler {
	if limiter == nil {
		limiter = sharedManagementDiagnosticTaskLimiter()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, allowed := requireManagementProxyAdmin(w, r)
		if !allowed {
			return
		}
		if !validateOptionalManagementProxyTestJSONBody(w, r) {
			return
		}
		proxyID := chi.URLParam(r, "id")
		before, found, err := service.Find(r, proxyID)
		if err != nil {
			writeManagementProxyTestError(w, err)
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "代理不存在")
			return
		}
		release, acquired := limiter.TryAcquire()
		if !acquired {
			w.Header().Set("Retry-After", "1")
			writeMessageError(w, http.StatusServiceUnavailable, "诊断任务繁忙，请稍后重试")
			return
		}
		defer release()
		result, err := service.Test(r, managementproxies.TestInput{ID: proxyID})
		if err != nil {
			if errors.Is(err, managementproxies.ErrProxyNotFound) {
				writeMessageError(w, http.StatusNotFound, "代理不存在")
				return
			}
			writeManagementProxyTestError(w, err)
			return
		}
		result.Before = before
		recordProxyTestOperationLog(r, authContext, result, logOptions)
		writeData(w, http.StatusOK, result.Report)
	})
}

func validateOptionalManagementProxyTestJSONBody(w http.ResponseWriter, r *http.Request) bool {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return true
	}
	reader := http.MaxBytesReader(w, r.Body, 256*1024)
	body, err := io.ReadAll(reader)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return false
		}
		writeMessageError(w, http.StatusBadRequest, "请求参数无效")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value any
	if err := decoder.Decode(&value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求参数无效")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求参数无效")
		return false
	}
	return true
}

func writeManagementProxyTestError(w http.ResponseWriter, err error) {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "代理检测失败"
	}
	writeMessageError(w, http.StatusBadGateway, message)
}

func requireManagementProxyAdmin(w http.ResponseWriter, r *http.Request) (managementauth.Context, bool) {
	authContext, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return managementauth.Context{}, false
	}
	if !managementauth.IsAdminRole(authContext.Role) {
		writeMessageError(w, http.StatusForbidden, "需要管理员权限")
		return managementauth.Context{}, false
	}
	return authContext, true
}

func writeManagementProxyMutationError(w http.ResponseWriter, err error, successStatus int) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, managementproxies.ErrProxyNotFound) {
		writeMessageError(w, http.StatusNotFound, "代理不存在")
		return false
	}
	if message, ok := managementproxies.NameExistsMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return false
	}
	if message, ok := managementproxies.InUseMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return false
	}
	if _, ok := managementproxies.ValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return false
	}
	if errors.Is(err, managementproxies.ErrProxyCredentialCodecUnusable) {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return false
	}
	if successStatus == http.StatusNoContent {
		writeMessageError(w, http.StatusBadRequest, "删除代理失败")
		return false
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	return false
}

func parseManagementProxyListQuery(values url.Values) managementproxies.ListInput {
	return managementproxies.ListInput{
		Keyword:  firstManagementQueryText(values, "keyword"),
		Page:     managementIntegerQueryValue(values, "page"),
		PageSize: managementIntegerQueryValue(values, "pageSize"),
	}
}

func parseManagementProxyOptionListQuery(values url.Values) managementproxies.OptionListInput {
	return managementproxies.OptionListInput{
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}

func firstManagementQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func managementIntegerQueryValue(values url.Values, key string) int {
	text := firstManagementQueryText(values, key)
	if text == "" {
		return 0
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

func decodeManagementProxyCreateInput(w http.ResponseWriter, r *http.Request, systemAccountID string) (managementproxies.CreateInput, bool) {
	fields, ok := decodeManagementProxyJSONFields(w, r)
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	name, ok := requiredManagementProxyStringField(w, fields, "name")
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	proxyType, ok := requiredManagementProxyStringField(w, fields, "type")
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	host, ok := requiredManagementProxyStringField(w, fields, "host")
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	port, ok := requiredManagementProxyIntField(w, fields, "port")
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	description, ok := optionalManagementProxyStringField(w, fields, "description", true)
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	username, ok := optionalManagementProxyStringField(w, fields, "username", false)
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	password, ok := optionalManagementProxyStringField(w, fields, "password", false)
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	enabled, ok := optionalManagementProxyBoolField(w, fields, "enabled")
	if !ok {
		return managementproxies.CreateInput{}, false
	}
	return managementproxies.CreateInput{
		SystemAccountID: systemAccountID,
		Name:            *name,
		Description:     description,
		Type:            *proxyType,
		Host:            *host,
		Port:            *port,
		Username:        username,
		Password:        password,
		Enabled:         enabled,
	}, true
}

func decodeManagementProxyUpdateInput(w http.ResponseWriter, r *http.Request, proxyID string) (managementproxies.UpdateInput, bool) {
	fields, ok := decodeManagementProxyJSONFields(w, r)
	if !ok {
		return managementproxies.UpdateInput{}, false
	}
	input := managementproxies.UpdateInput{ID: proxyID}
	if _, present := fields["name"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "name", false)
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Name = value
	}
	if _, present := fields["description"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "description", true)
		if !ok {
			return managementproxies.UpdateInput{}, false
		}
		input.Description = managementproxies.OptionalText{Set: true, Value: value}
	}
	if _, present := fields["type"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "type", false)
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Type = value
	}
	if _, present := fields["host"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "host", false)
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Host = value
	}
	if _, present := fields["port"]; present {
		value, ok := optionalManagementProxyIntField(w, fields, "port")
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Port = value
	}
	if _, present := fields["username"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "username", false)
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Username = managementproxies.OptionalText{Set: true, Value: value}
	}
	if _, present := fields["password"]; present {
		value, ok := optionalManagementProxyStringField(w, fields, "password", false)
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Password = value
	}
	if _, present := fields["enabled"]; present {
		value, ok := optionalManagementProxyBoolField(w, fields, "enabled")
		if !ok || value == nil {
			return managementproxies.UpdateInput{}, false
		}
		input.Enabled = value
	}
	return input, true
}

func decodeManagementProxyJSONFields(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	for key := range fields {
		switch key {
		case "name", "description", "type", "host", "port", "username", "password", "enabled":
		default:
			writeMessageError(w, http.StatusBadRequest, "代理参数无效")
			return nil, false
		}
	}
	return fields, true
}

func requiredManagementProxyStringField(w http.ResponseWriter, fields map[string]json.RawMessage, key string) (*string, bool) {
	value, ok := optionalManagementProxyStringField(w, fields, key, false)
	if !ok || value == nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	return value, true
}

func requiredManagementProxyIntField(w http.ResponseWriter, fields map[string]json.RawMessage, key string) (*int, bool) {
	value, ok := optionalManagementProxyIntField(w, fields, key)
	if !ok || value == nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	return value, true
}

func optionalManagementProxyStringField(w http.ResponseWriter, fields map[string]json.RawMessage, key string, nullable bool) (*string, bool) {
	raw, present := fields[key]
	if !present {
		return nil, true
	}
	if isJSONNull(raw) {
		if nullable {
			return nil, true
		}
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	return &value, true
}

func optionalManagementProxyIntField(w http.ResponseWriter, fields map[string]json.RawMessage, key string) (*int, bool) {
	raw, present := fields[key]
	if !present {
		return nil, true
	}
	if isJSONNull(raw) {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	return &value, true
}

func optionalManagementProxyBoolField(w http.ResponseWriter, fields map[string]json.RawMessage, key string) (*bool, bool) {
	raw, present := fields[key]
	if !present {
		return nil, true
	}
	if isJSONNull(raw) {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "代理参数无效")
		return nil, false
	}
	return &value, true
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.EqualFold(strings.TrimSpace(string(raw)), "null")
}

func recordProxyCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementproxies.CreateResult,
	opts managementOperationLogOptions,
) {
	changes := []port.OperationLogChange{
		{Field: "name", Label: "名称", Before: nil, After: result.Proxy.Name},
		{Field: "type", Label: "类型", Before: nil, After: result.Proxy.Type},
		{Field: "host", Label: "主机", Before: nil, After: result.Proxy.Host},
		{Field: "port", Label: "端口", Before: nil, After: result.Proxy.Port},
		{Field: "username", Label: "用户名", Before: nil, After: proxyOperationLogTextValue(result.Proxy.Username)},
		{Field: "enabled", Label: "启用状态", Before: nil, After: result.Proxy.Enabled},
	}
	if result.PasswordSet {
		changes = append(changes, port.OperationLogChange{
			Field:     "password",
			Label:     "密码",
			Before:    nil,
			After:     "已设置",
			Sensitive: true,
		})
	}
	recordProxyOperationLog(r, authContext, opts, proxyOperationLogRecord{
		statusCode:   http.StatusCreated,
		action:       "create",
		operationKey: "proxies.create",
		resourceID:   result.Proxy.ID,
		resourceName: result.Proxy.Name,
		summary:      "创建代理：" + result.Proxy.Name,
		changes:      changes,
	})
}

func recordProxyUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementproxies.UpdateResult,
	opts managementOperationLogOptions,
) {
	changes := make([]port.OperationLogChange, 0, 8)
	changes = appendProxyChange(changes, "name", "名称", result.Before.Name, result.Proxy.Name)
	changes = appendProxyChange(changes, "description", "说明", proxyOperationLogTextValue(result.Before.Description), proxyOperationLogTextValue(result.Proxy.Description))
	changes = appendProxyChange(changes, "type", "类型", result.Before.Type, result.Proxy.Type)
	changes = appendProxyChange(changes, "host", "主机", result.Before.Host, result.Proxy.Host)
	changes = appendProxyChange(changes, "port", "端口", result.Before.Port, result.Proxy.Port)
	changes = appendProxyChange(changes, "username", "用户名", proxyOperationLogTextValue(result.Before.Username), proxyOperationLogTextValue(result.Proxy.Username))
	changes = appendProxyChange(changes, "enabled", "启用状态", result.Before.Enabled, result.Proxy.Enabled)
	if result.PasswordChanged {
		changes = append(changes, port.OperationLogChange{
			Field:     "password",
			Label:     "密码",
			Before:    nil,
			After:     "已更新",
			Sensitive: true,
		})
	}
	recordProxyOperationLog(r, authContext, opts, proxyOperationLogRecord{
		statusCode:   http.StatusOK,
		action:       "update",
		operationKey: "proxies.update",
		resourceID:   result.Proxy.ID,
		resourceName: result.Proxy.Name,
		summary:      "更新代理：" + result.Proxy.Name,
		changes:      changes,
	})
}

func recordProxyDeleteOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementproxies.DeleteResult,
	opts managementOperationLogOptions,
) {
	recordProxyOperationLog(r, authContext, opts, proxyOperationLogRecord{
		statusCode:   http.StatusNoContent,
		action:       "delete",
		operationKey: "proxies.delete",
		resourceID:   result.Before.ID,
		resourceName: result.Before.Name,
		summary:      "删除代理：" + result.Before.Name,
		changes: []port.OperationLogChange{{
			Field:  "deleted",
			Label:  "删除状态",
			Before: false,
			After:  true,
		}},
	})
}

func recordProxyTestOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementproxies.TestResult,
	opts managementOperationLogOptions,
) {
	changes := make([]port.OperationLogChange, 0, 6)
	changes = appendProxyChange(changes, "testStatus", "检测状态", result.Before.TestStatus, result.Proxy.TestStatus)
	changes = appendProxyChange(changes, "latencyMs", "延迟", proxyOperationLogIntValue(result.Before.LatencyMs), proxyOperationLogIntValue(result.Proxy.LatencyMs))
	changes = appendProxyChange(changes, "outboundIp", "出口 IP", proxyOperationLogTextValue(result.Before.OutboundIP), proxyOperationLogTextValue(result.Proxy.OutboundIP))
	changes = appendProxyChange(changes, "outboundRegion", "出口地区", proxyOperationLogTextValue(result.Before.OutboundRegion), proxyOperationLogTextValue(result.Proxy.OutboundRegion))
	changes = appendProxyChange(changes, "lastTestMessage", "检测消息", proxyOperationLogTextValue(result.Before.LastTestMessage), proxyOperationLogTextValue(result.Proxy.LastTestMessage))
	changes = appendProxyChange(changes, "lastTestedAt", "检测时间", proxyOperationLogTimeValue(result.Before.LastTestedAt), proxyOperationLogTimeValue(result.Proxy.LastTestedAt))
	recordProxyOperationLog(r, authContext, opts, proxyOperationLogRecord{
		statusCode:   http.StatusOK,
		action:       "test",
		operationKey: "proxies.test",
		resourceID:   result.Report.ProxyID,
		resourceName: result.Report.ProxyName,
		summary:      "检测代理：" + result.Report.ProxyName,
		changes:      changes,
	})
}

type proxyOperationLogRecord struct {
	statusCode   int
	action       string
	operationKey string
	resourceID   string
	resourceName string
	summary      string
	changes      []port.OperationLogChange
}

func recordProxyOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	opts managementOperationLogOptions,
	record proxyOperationLogRecord,
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
	statusCode := record.statusCode
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: authContext.SystemAccountID,
		Mode:                          "admin",
		Module:                        "proxies",
		Action:                        record.action,
		OperationKey:                  record.operationKey,
		ResourceType:                  "proxy",
		ResourceID:                    record.resourceID,
		ResourceName:                  record.resourceName,
		Summary:                       record.summary,
		DetailLevel:                   "full",
		VisibilityScope:               "admin_only",
		Changes:                       record.changes,
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		CreatedAt:                     now().UTC(),
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if _, err := operationlogjob.EnqueueWrite(enqueueCtx, opts.client, input); err != nil && opts.logger != nil {
		opts.logger.Warn("管理端操作日志入队失败",
			slog.String("event", "operation_log_enqueue_failed"),
			slog.String("operation_key", input.OperationKey),
			slog.String("resource_id", input.ResourceID),
			slog.String("request_id", input.TraceID),
			slog.Any("error", err),
		)
	}
}

func defaultManagementOperationLogID() string {
	return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func appendProxyChange(changes []port.OperationLogChange, field string, label string, before any, after any) []port.OperationLogChange {
	if operationLogValuesEqual(before, after) {
		return changes
	}
	return append(changes, port.OperationLogChange{
		Field:  field,
		Label:  label,
		Before: before,
		After:  after,
	})
}

func operationLogValuesEqual(before any, after any) bool {
	return reflect.DeepEqual(before, after)
}

func proxyOperationLogTextValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func proxyOperationLogIntValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func proxyOperationLogTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
