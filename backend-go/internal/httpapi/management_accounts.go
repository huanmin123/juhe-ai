package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountOptionScope int

const (
	managementAccountScopeAdmin managementAccountOptionScope = iota
	managementAccountScopeSelf
)

type managementAccountOptionService interface {
	Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error)
}

type managementAccountTagService interface {
	Tags(r *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error)
	DeleteTag(r *http.Request, input managementaccounts.TagDeleteInput) (bool, error)
}

type managementAccountOptionServiceAdapter struct {
	service *managementaccounts.Service
}

func (s managementAccountOptionServiceAdapter) Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementAccountOptionServiceAdapter) Tags(r *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error) {
	return s.service.Tags(r.Context(), input)
}

func (s managementAccountOptionServiceAdapter) DeleteTag(r *http.Request, input managementaccounts.TagDeleteInput) (bool, error) {
	return s.service.DeleteTag(r.Context(), input)
}

func NewManagementAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountTagsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagDeleteHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagDeleteHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountTagDeleteHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagDeleteHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func newManagementAccountOptionsHandler(service managementAccountOptionService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementAccountTagsHandler(service managementAccountTagService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountTagListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		tags, err := service.Tags(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, tags)
	})
}

func newManagementAccountTagDeleteHandler(service managementAccountTagService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		scopeInput, allowed := managementAccountTagListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		deleted, err := service.DeleteTag(r, managementaccounts.TagDeleteInput{
			ID:              chi.URLParam(r, "tagId"),
			SystemAccountID: scopeInput.SystemAccountID,
		})
		if errors.Is(err, managementaccounts.ErrAccountTagInUse) {
			writeMessageError(w, http.StatusBadRequest, "标签已绑定账户，不能删除")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !deleted {
			writeMessageError(w, http.StatusNotFound, "标签不存在")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func managementAccountOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountOptionScope,
) (managementaccounts.OptionListInput, bool) {
	input := parseManagementAccountOptionListQuery(values)
	switch scope {
	case managementAccountScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounts.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func managementAccountTagListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountOptionScope,
) (managementaccounts.TagListInput, bool) {
	input := managementaccounts.TagListInput{SystemAccountID: authContext.SystemAccountID}
	switch scope {
	case managementAccountScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounts.TagListInput{}, false
		}
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}

func parseManagementAccountOptionListQuery(values url.Values) managementaccounts.OptionListInput {
	return managementaccounts.OptionListInput{
		IDs:          managementTextListQueryValue(values, "ids", 50),
		Page:         managementIntegerQueryValue(values, "page"),
		Limit:        managementIntegerQueryValue(values, "limit"),
		Keyword:      firstManagementQueryText(values, "keyword"),
		ProviderCode: firstManagementQueryText(values, "providerCode"),
		GroupID:      firstManagementQueryText(values, "groupId"),
		TagIDs:       managementTextListQueryValue(values, "tagIds", 100),
		Type:         firstManagementQueryText(values, "type"),
		Status:       strings.Join(managementTextListQueryValue(values, "status", 100), ","),
		Schedulable:  firstManagementQueryText(values, "schedulable"),
	}
}
