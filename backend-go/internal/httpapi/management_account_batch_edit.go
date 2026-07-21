package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountbatchedit"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

func NewManagementAccountBatchEditHandler(service *managementaccountbatchedit.Service) http.Handler {
	return batchEditHandler(service, false)
}
func NewManagementMyAccountBatchEditHandler(service *managementaccountbatchedit.Service) http.Handler {
	return batchEditHandler(service, true)
}

func batchEditHandler(service *managementaccountbatchedit.Service, self bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !self && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		systemID := auth.SystemAccountID
		if !self {
			systemID = ""
		}
		if !self && strings.TrimSpace(r.URL.Query().Get("systemAccountId")) != "" {
			systemID = strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		}
		var payload struct {
			AccountIDs []string                                `json:"accountIds"`
			Targets    []port.ManagementAccountBatchEditTarget `json:"targets"`
			Updates    map[string]any                          `json:"updates"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
			writeMessageError(w, http.StatusBadRequest, "批量编辑参数无效")
			return
		}
		if r.URL.Path != "" && strings.HasSuffix(r.URL.Path, "/batch-edit-context") {
			accounts, err := service.Context(r.Context(), systemID, payload.AccountIDs)
			if err != nil {
				writeMessageError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeData(w, http.StatusOK, accounts)
			return
		}
		result, err := service.Update(r.Context(), port.ManagementAccountBatchEditInput{SystemAccountID: systemID, Targets: payload.Targets, Updates: normalizeBatchUpdates(payload.Updates)})
		if err != nil {
			status := http.StatusBadRequest
			if err == managementaccountbatchedit.ErrVersionConflict {
				status = http.StatusConflict
			}
			writeMessageError(w, status, err.Error())
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func normalizeBatchUpdates(input map[string]any) map[string]any {
	output := map[string]any{}
	for key, value := range input {
		if item, ok := value.(map[string]any); ok {
			if enabled, ok := item["enabled"].(bool); ok && enabled {
				field, supported := batchNodeField(key)
				if !supported {
					continue
				}
				value := item["value"]
				if field == "availability_schedule_json" && value != nil {
					encoded, err := json.Marshal(value)
					if err != nil {
						continue
					}
					value = string(encoded)
				}
				output[field] = value
			}
		}
	}
	return output
}
func batchNodeField(key string) (string, bool) {
	fields := map[string]string{
		"concurrencyLimit": "concurrency_limit", "priority": "priority",
		"superPriorityEnabled": "super_priority_enabled", "fallbackEnabled": "fallback_enabled",
		"accountExpiresAt": "account_expires_at", "availabilitySchedule": "availability_schedule_json",
		"notes": "notes", "healthCheckModel": "health_check_model",
		"healthCheckEndpointMode": "health_check_endpoint_mode",
	}
	field, ok := fields[key]
	return field, ok
}
