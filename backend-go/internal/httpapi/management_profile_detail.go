package httpapi

import (
	"errors"
	"net/http"

	"juhe-ai/backend-go/internal/modules/managementauth"
)

func NewManagementProfileDetailHandler(authenticator ManagementCurrentUserAuthenticator, service *managementauth.ProfileDetailService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		authContext, err := authenticator.AuthenticateCookieForCurrentUser(r.Context(), r.Header.Get("Cookie"))
		if err != nil {
			writeManagementAuthError(w, err)
			return
		}
		account, err := service.GetProfile(r.Context(), authContext)
		if errors.Is(err, managementauth.ErrProfileNotFound) {
			writeMessageError(w, http.StatusNotFound, "系统账户不存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, managementPasswordChangeResponseFromSummary(account))
	})
}
