package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementaccountcreate"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountCreateService interface {
	Create(*http.Request, managementaccountcreate.Input) (map[string]any, error)
}
type managementAccountCreateServiceAdapter struct {
	service *managementaccountcreate.Service
}

func (s managementAccountCreateServiceAdapter) Create(r *http.Request, input managementaccountcreate.Input) (map[string]any, error) {
	return s.service.Create(r.Context(), input)
}

func NewManagementAccountCreateHandler(service *managementaccountcreate.Service) http.Handler {
	return newManagementAccountCreateHandler(managementAccountCreateServiceAdapter{service: service}, false)
}
func NewManagementMyAccountCreateHandler(service *managementaccountcreate.Service) http.Handler {
	return newManagementAccountCreateHandler(managementAccountCreateServiceAdapter{service: service}, true)
}

func newManagementAccountCreateHandler(service managementAccountCreateService, selfOnly bool, logOptions ...managementOperationLogOptions) http.Handler {
	op := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !selfOnly && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		body, ok := decodeManagementAccountCreateBody(w, r)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "账户参数无效")
			return
		}
		owner := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		if selfOnly || owner == "" {
			owner = auth.SystemAccountID
		}
		result, err := service.Create(r, managementaccountcreate.Input{ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role, SystemAccountID: owner, SelfOnly: selfOnly, ProviderCode: textField(body, "providerCode"), ProviderProtocolProfileID: textField(body, "providerProtocolProfileId"), Name: textField(body, "name"), Type: textField(body, "type"), Credentials: mapField(body, "credentials"), SupportedModels: stringSliceField(body, "supportedModels"), HealthCheckModel: textField(body, "healthCheckModel"), HealthCheckEndpointMode: textField(body, "healthCheckEndpointMode"), Status: textField(body, "status"), ConcurrencyLimit: intField(body, "concurrencyLimit"), Priority: intField(body, "priority"), SuperPriorityEnabled: boolField(body, "superPriorityEnabled"), FallbackEnabled: boolField(body, "fallbackEnabled"), ProxyProfileID: textField(body, "proxyProfileId"), Schedulable: boolPointerField(body, "schedulable"), GroupID: textField(body, "groupId"), AccountExpiresAt: timeField(body, "accountExpiresAt"), AvailabilitySchedule: mapField(body, "availabilitySchedule"), TemporaryUnavailableContinuousProbeEnabled: boolField(body, "temporaryUnavailableContinuousProbeEnabled"), Notes: textField(body, "notes")})
		if err != nil {
			writeManagementAccountCreateError(w, err)
			return
		}
		recordAccountCreateOperationLog(r, auth, result, op)
		writeData(w, http.StatusCreated, result)
	})
}

func decodeManagementAccountCreateBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return body, true
}
func textField(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}
func mapField(body map[string]any, key string) map[string]any {
	value, _ := body[key].(map[string]any)
	return value
}
func boolField(body map[string]any, key string) bool { value, _ := body[key].(bool); return value }
func boolPointerField(body map[string]any, key string) *bool {
	value, exists := body[key]
	if !exists {
		return nil
	}
	typed, ok := value.(bool)
	if !ok {
		return nil
	}
	return &typed
}
func intField(body map[string]any, key string) int {
	value, _ := body[key].(json.Number)
	number, _ := strconv.Atoi(string(value))
	return number
}
func stringSliceField(body map[string]any, key string) []string {
	values, _ := body[key].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}
func timeField(body map[string]any, key string) *time.Time {
	text := textField(body, key)
	if text == "" {
		return nil
	}
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &value
}

func writeManagementAccountCreateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, managementaccountcreate.ErrInvalid):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, managementaccountcreate.ErrProviderInvalid), errors.Is(err, managementaccountcreate.ErrGroupInvalid):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	default:
		writeMessageError(w, http.StatusBadRequest, err.Error())
	}
}

func recordAccountCreateOperationLog(r *http.Request, auth managementauth.Context, account map[string]any, opts managementOperationLogOptions) {
	if opts.client == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newID := opts.newLogID
	if newID == nil {
		newID = func() string { return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "") }
	}
	text := func(key string) string { value, _ := account[key].(string); return value }
	statusCode := http.StatusCreated
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{ID: newID(), TraceID: requestIDFromContext(r.Context()), ActorSystemAccountID: auth.SystemAccountID, ActorUsername: auth.Username, ActorDisplayName: auth.DisplayName, ActorRole: auth.Role, OperationScopeSystemAccountID: text("systemAccountId"), Mode: "admin", Module: "accounts", Action: "create", OperationKey: "accounts.create", ResourceType: "account", ResourceID: text("id"), ResourceName: text("name"), Summary: "创建 AI 账户：" + text("name"), DetailLevel: "full", VisibilityScope: "targeted", Changes: []port.OperationLogChange{{Field: "name", Label: "名称", After: text("name")}, {Field: "providerCode", Label: "供应商", After: text("providerCode")}, {Field: "providerProtocolProfileId", Label: "协议档案", After: text("providerProtocolProfileId")}, {Field: "type", Label: "账户类型", After: text("type")}, {Field: "status", Label: "状态", After: text("status")}}, Method: r.Method, Path: r.URL.Path, StatusCode: &statusCode, ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(), Viewers: []port.OperationLogViewerInput{{SystemAccountID: text("systemAccountId"), VisibilityReason: "resource_owner", DetailLevel: "full"}}, CreatedAt: now().UTC()})
}
