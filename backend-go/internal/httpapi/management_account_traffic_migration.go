package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounttrafficmigration"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountTrafficMigrationScope int

const (
	managementAccountTrafficMigrationScopeAdmin managementAccountTrafficMigrationScope = iota
	managementAccountTrafficMigrationScopeSelf
)

type managementAccountTrafficMigrationService interface {
	Migrate(*http.Request, managementaccounttrafficmigration.Input) (managementaccounttrafficmigration.Result, error)
}

type managementAccountTrafficMigrationServiceAdapter struct {
	service *managementaccounttrafficmigration.Service
}

func (s managementAccountTrafficMigrationServiceAdapter) Migrate(r *http.Request, input managementaccounttrafficmigration.Input) (managementaccounttrafficmigration.Result, error) {
	return s.service.Migrate(r.Context(), input)
}

func NewManagementAccountTrafficMigrationHandler(service *managementaccounttrafficmigration.Service) http.Handler {
	return newManagementAccountTrafficMigrationHandler(managementAccountTrafficMigrationServiceAdapter{service: service}, managementAccountTrafficMigrationScopeAdmin)
}

func NewManagementMyAccountTrafficMigrationHandler(service *managementaccounttrafficmigration.Service) http.Handler {
	return newManagementAccountTrafficMigrationHandler(managementAccountTrafficMigrationServiceAdapter{service: service}, managementAccountTrafficMigrationScopeSelf)
}

func newManagementAccountTrafficMigrationHandler(service managementAccountTrafficMigrationService, scope managementAccountTrafficMigrationScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountTrafficMigrationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		systemAccountID, valid := managementAccountTrafficMigrationSystemAccountID(r, scope)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		var payload struct {
			TargetAccountID *string `json:"targetAccountId"`
			SourceStatus    *string `json:"sourceStatus"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || payload.TargetAccountID == nil || strings.TrimSpace(*payload.TargetAccountID) == "" {
			writeMessageError(w, http.StatusBadRequest, "迁移流量参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "迁移流量参数无效")
			return
		}
		status := managementaccounttrafficmigration.SourceStatusTemporaryUnavailable
		if payload.SourceStatus != nil {
			status = managementaccounttrafficmigration.SourceStatus(strings.TrimSpace(*payload.SourceStatus))
			if status != managementaccounttrafficmigration.SourceStatusTemporaryUnavailable && status != managementaccounttrafficmigration.SourceStatusDisabled && status != managementaccounttrafficmigration.SourceStatusUnchanged {
				writeMessageError(w, http.StatusBadRequest, "迁移流量参数无效")
				return
			}
		}
		result, err := service.Migrate(r, managementaccounttrafficmigration.Input{
			ActorSystemAccountID: authContext.SystemAccountID, ActorRole: authContext.Role,
			SystemAccountID: systemAccountID, SelfOnly: scope == managementAccountTrafficMigrationScopeSelf,
			SourceAccountID: chi.URLParam(r, "id"), TargetAccountID: *payload.TargetAccountID, SourceStatus: status,
		})
		if err != nil {
			if errors.Is(err, managementaccounttrafficmigration.ErrNotFound) {
				writeMessageError(w, http.StatusNotFound, err.Error())
			} else {
				writeMessageError(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementAccountTrafficMigrationSystemAccountID(r *http.Request, scope managementAccountTrafficMigrationScope) (string, bool) {
	if scope == managementAccountTrafficMigrationScopeSelf {
		return "", true
	}
	values, exists := r.URL.Query()["systemAccountId"]
	if !exists {
		return "", true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "all" {
		return "", true
	}
	return value, true
}
