package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountimport"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementAccountImportMaxBodyBytes = 1 << 20

type managementAccountImportScope int

const (
	managementAccountImportScopeAdmin managementAccountImportScope = iota
	managementAccountImportScopeSelf
)

type managementAccountImportService interface {
	Preview(context.Context, []byte, managementaccountimport.OptionsInput) (managementaccountimport.Result, error)
	Confirm(context.Context, []byte, managementaccountimport.OptionsInput, string) (managementaccountimport.Result, error)
}

func NewManagementAccountImportPreviewHandler(service *managementaccountimport.Service) http.Handler {
	return newManagementAccountImportHandler(service, managementAccountImportScopeAdmin, false)
}

func NewManagementMyAccountImportPreviewHandler(service *managementaccountimport.Service) http.Handler {
	return newManagementAccountImportHandler(service, managementAccountImportScopeSelf, false)
}

func NewManagementAccountImportConfirmHandler(service *managementaccountimport.Service) http.Handler {
	return newManagementAccountImportHandler(service, managementAccountImportScopeAdmin, true)
}

func NewManagementMyAccountImportConfirmHandler(service *managementaccountimport.Service) http.Handler {
	return newManagementAccountImportHandler(service, managementAccountImportScopeSelf, true)
}

func newManagementAccountImportHandler(service managementAccountImportService, scope managementAccountImportScope, confirm bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountImportScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var request struct {
			Data    json.RawMessage                      `json:"data"`
			Options managementaccountimport.OptionsInput `json:"options,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementAccountImportMaxBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || len(request.Data) == 0 {
			writeMessageError(w, http.StatusBadRequest, "账户导入参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "账户导入参数无效")
			return
		}
		if !confirm {
			result, err := service.Preview(r.Context(), request.Data, request.Options)
			if err != nil {
				writeMessageError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeData(w, http.StatusOK, result)
			return
		}
		owner := auth.SystemAccountID
		if scope == managementAccountImportScopeAdmin {
			owner = strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
			if owner == "" || owner == "all" {
				owner = auth.SystemAccountID
			}
		}
		result, err := service.Confirm(r.Context(), request.Data, request.Options, owner)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeData(w, http.StatusOK, result)
	})
}
