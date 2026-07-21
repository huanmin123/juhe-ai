package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountexport"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountExportScope int

const (
	managementAccountExportScopeAdmin managementAccountExportScope = iota
	managementAccountExportScopeSelf
)

type managementAccountExportService interface {
	Write(*http.Request, io.Writer, managementaccountexport.Input) (managementaccountexport.Summary, error)
}

type accountExportServiceAdapter struct {
	service *managementaccountexport.Service
}

func (s accountExportServiceAdapter) Write(r *http.Request, writer io.Writer, input managementaccountexport.Input) (managementaccountexport.Summary, error) {
	return s.service.Write(r.Context(), writer, input)
}

func NewManagementAccountExportHandler(service *managementaccountexport.Service) http.Handler {
	return newManagementAccountExportHandler(exportServiceFrom(service), managementAccountExportScopeAdmin)
}
func NewManagementMyAccountExportHandler(service *managementaccountexport.Service) http.Handler {
	return newManagementAccountExportHandler(exportServiceFrom(service), managementAccountExportScopeSelf)
}
func exportServiceFrom(service *managementaccountexport.Service) managementAccountExportService {
	if service == nil {
		return nil
	}
	return accountExportServiceAdapter{service: service}
}

func newManagementAccountExportHandler(service managementAccountExportService, scope managementAccountExportScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountExportScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var request struct {
			AccountIDs []string                         `json:"accountIds"`
			Filters    *managementaccountexport.Filters `json:"filters"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeMessageError(w, http.StatusBadRequest, "账户导出参数无效")
			return
		}
		if (len(request.AccountIDs) == 0) == (request.Filters == nil) {
			writeMessageError(w, http.StatusBadRequest, "账户导出参数无效")
			return
		}
		systemID := auth.SystemAccountID
		if scope == managementAccountExportScopeAdmin {
			systemID = managementAccountExportSystemAccountID(r)
		}
		input := managementaccountexport.Input{SystemAccountID: systemID, AccountIDs: request.AccountIDs, Filters: request.Filters}
		writer := &managementAccountExportResponseWriter{ResponseWriter: w}
		_, err := service.Write(r, writer, input)
		if err != nil {
			if !writer.wrote {
				writeMessageError(w, http.StatusBadRequest, err.Error())
			}
			return
		}
	})
}

type managementAccountExportResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *managementAccountExportResponseWriter) Write(payload []byte) (int, error) {
	if !w.wrote {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.wrote = true
	}
	return w.ResponseWriter.Write(payload)
}

func managementAccountExportSystemAccountID(r *http.Request) string {
	value := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if value == "all" {
		return ""
	}
	return value
}
