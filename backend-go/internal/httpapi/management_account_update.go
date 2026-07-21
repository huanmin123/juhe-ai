package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountupdate"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountUpdateScope int

const (
	managementAccountUpdateScopeAdmin managementAccountUpdateScope = iota
	managementAccountUpdateScopeSelf
)

type managementAccountUpdateService interface {
	Update(*http.Request, managementaccountupdate.UpdateInput) (managementaccountupdate.Result, error)
}

type managementAccountUpdateServiceAdapter struct {
	service *managementaccountupdate.Service
}

func (s managementAccountUpdateServiceAdapter) Update(r *http.Request, input managementaccountupdate.UpdateInput) (managementaccountupdate.Result, error) {
	return s.service.Update(r.Context(), input)
}

func NewManagementAccountUpdateHandler(service *managementaccountupdate.Service) http.Handler {
	return newManagementAccountUpdateHandler(managementAccountUpdateServiceAdapter{service: service}, managementAccountUpdateScopeAdmin)
}

func NewManagementMyAccountUpdateHandler(service *managementaccountupdate.Service) http.Handler {
	return newManagementAccountUpdateHandler(managementAccountUpdateServiceAdapter{service: service}, managementAccountUpdateScopeSelf)
}

func newManagementAccountUpdateHandler(service managementAccountUpdateService, scope managementAccountUpdateScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountUpdateScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		revision, fields, ok := decodeManagementAccountUpdateBody(w, r)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "账户更新参数无效")
			return
		}
		systemAccountID := auth.SystemAccountID
		if scope == managementAccountUpdateScopeAdmin {
			systemAccountID = strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
		}
		result, err := service.Update(r, managementaccountupdate.UpdateInput{
			ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role,
			SystemAccountID: systemAccountID, SelfOnly: scope == managementAccountUpdateScopeSelf,
			AccountID: chi.URLParam(r, "id"), ExpectedConfigRevision: revision, Fields: fields,
		})
		if err != nil {
			writeManagementAccountUpdateError(w, err)
			return
		}
		writeData(w, http.StatusOK, result.After)
	})
}

func decodeManagementAccountUpdateBody(w http.ResponseWriter, r *http.Request) (int, map[string]any, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return 0, nil, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return 0, nil, false
	}
	rawRevision, exists := body["configRevision"]
	if !exists {
		return 0, nil, false
	}
	revisionNumber, ok := rawRevision.(json.Number)
	if !ok {
		return 0, nil, false
	}
	revision64, err := revisionNumber.Int64()
	if err != nil || revision64 < 1 || revision64 > int64(^uint(0)>>1) {
		return 0, nil, false
	}
	delete(body, "configRevision")
	if len(body) == 0 {
		return 0, nil, false
	}
	return int(revision64), body, true
}

func writeManagementAccountUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, managementaccountupdate.ErrInvalid):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, managementaccountupdate.ErrNotFound):
		writeMessageError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, managementaccountupdate.ErrAuthorized), errors.Is(err, managementaccountupdate.ErrProviderInvalid), errors.Is(err, managementaccountupdate.ErrGroupInvalid):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, managementaccountupdate.ErrVersionConflict), errors.Is(err, managementaccountupdate.ErrNameExists):
		writeMessageError(w, http.StatusConflict, err.Error())
	default:
		writeMessageError(w, http.StatusInternalServerError, "更新账户失败")
	}
}
