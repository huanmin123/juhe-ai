package httpapi

import (
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

type managementGroupStatusSnapshotService interface {
	StatusSnapshot(*http.Request, managementgroups.StatusSnapshotInput) (managementgroups.StatusSnapshotResult, error)
}

type managementGroupStatusSnapshotServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupStatusSnapshotServiceAdapter) StatusSnapshot(r *http.Request, input managementgroups.StatusSnapshotInput) (managementgroups.StatusSnapshotResult, error) {
	return s.service.StatusSnapshot(r.Context(), input)
}

func NewManagementGroupStatusSnapshotHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupStatusSnapshotHandler(managementGroupStatusSnapshotServiceFrom(service), managementGroupScopeAdmin)
}

func NewManagementMyGroupStatusSnapshotHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupStatusSnapshotHandler(managementGroupStatusSnapshotServiceFrom(service), managementGroupScopeSelf)
}

func managementGroupStatusSnapshotServiceFrom(service *managementgroups.Service) managementGroupStatusSnapshotService {
	if service == nil {
		return nil
	}
	return managementGroupStatusSnapshotServiceAdapter{service: service}
}

func newManagementGroupStatusSnapshotHandler(service managementGroupStatusSnapshotService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementGroupScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		groupIDs, err := managementgroups.ParseStatusSnapshotGroupIDs(r.URL.Query().Get("groupIds"))
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		input := managementgroups.StatusSnapshotInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			GroupIDs:             groupIDs,
			SelfOnly:             scope == managementGroupScopeSelf,
		}
		if scope == managementGroupScopeSelf {
			input.SystemAccountID = authContext.SystemAccountID
		} else {
			input.SystemAccountID = firstManagementQueryText(r.URL.Query(), "systemAccountId")
			if input.SystemAccountID == "all" {
				input.SystemAccountID = ""
			}
		}
		result, err := service.StatusSnapshot(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}
