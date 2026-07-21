package httpapi

import (
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountstatussnapshot"
)

func NewManagementAccountStatusSnapshotHandler(service *managementaccountstatussnapshot.Service) http.Handler {
	return newManagementAccountStatusSnapshotHandler(service, false)
}
func NewManagementMyAccountStatusSnapshotHandler(service *managementaccountstatussnapshot.Service) http.Handler {
	return newManagementAccountStatusSnapshotHandler(service, true)
}

func newManagementAccountStatusSnapshotHandler(service *managementaccountstatussnapshot.Service, selfOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		raw := r.URL.Query().Get("accountIds")
		ids, err := managementaccountstatussnapshot.ParseAccountIDs(raw)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		systemAccountID := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		result, err := service.Get(r.Context(), managementaccountstatussnapshot.Input{ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role, SystemAccountID: systemAccountID, SelfOnly: selfOnly, AccountIDs: ids})
		if err != nil {
			if strings.Contains(err.Error(), "管理员") || strings.Contains(err.Error(), "未登录") {
				writeMessageError(w, http.StatusForbidden, err.Error())
			} else {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			}
			return
		}
		writeData(w, http.StatusOK, result)
	})
}
