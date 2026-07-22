package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountTestOptionsScope int

const (
	managementAccountTestOptionsScopeAdmin managementAccountTestOptionsScope = iota
	managementAccountTestOptionsScopeSelf
)

type managementAccountTestOptionsService interface {
	Options(r *http.Request, input managementaccounttestoptions.OptionsInput) ([]managementaccounttestoptions.SelectionOption, bool, error)
	ModelCapabilities(r *http.Request, input managementaccounttestoptions.ModelCapabilitiesInput) (managementaccounttestoptions.ModelCapabilities, bool, error)
}

type managementAccountTestOptionsServiceAdapter struct {
	service *managementaccounttestoptions.Service
}

func (s managementAccountTestOptionsServiceAdapter) Options(
	r *http.Request,
	input managementaccounttestoptions.OptionsInput,
) ([]managementaccounttestoptions.SelectionOption, bool, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementAccountTestOptionsServiceAdapter) ModelCapabilities(
	r *http.Request,
	input managementaccounttestoptions.ModelCapabilitiesInput,
) (managementaccounttestoptions.ModelCapabilities, bool, error) {
	return s.service.ModelCapabilities(r.Context(), input)
}

func NewManagementAccountTestOptionsHandler(service *managementaccounttestoptions.Service) http.Handler {
	return newManagementAccountTestOptionsHandler(
		managementAccountTestOptionsServiceFrom(service),
		managementAccountTestOptionsScopeAdmin,
	)
}

func NewManagementMyAccountTestOptionsHandler(service *managementaccounttestoptions.Service) http.Handler {
	return newManagementAccountTestOptionsHandler(
		managementAccountTestOptionsServiceFrom(service),
		managementAccountTestOptionsScopeSelf,
	)
}

func managementAccountTestOptionsServiceFrom(service *managementaccounttestoptions.Service) managementAccountTestOptionsService {
	if service == nil {
		return nil
	}
	return managementAccountTestOptionsServiceAdapter{service: service}
}

func newManagementAccountTestOptionsHandler(
	service managementAccountTestOptionsService,
	scope managementAccountTestOptionsScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountTestOptionsInput(
			authContext,
			r.URL.Query(),
			scope,
			chi.URLParam(r, "id"),
		)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		modelID := strings.TrimFunc(chi.URLParam(r, "modelId"), managementGroupListECMAScriptWhitespace)
		if modelID != "" {
			decodedModelID, decodeErr := url.PathUnescape(modelID)
			if decodeErr != nil || strings.TrimFunc(decodedModelID, managementGroupListECMAScriptWhitespace) == "" {
				writeMessageError(w, http.StatusBadRequest, "请选择测试模型")
				return
			}
			modelID = strings.TrimFunc(decodedModelID, managementGroupListECMAScriptWhitespace)
			result, found, err := service.ModelCapabilities(r, managementaccounttestoptions.ModelCapabilitiesInput{
				AccountID: input.AccountID, SystemAccountID: input.SystemAccountID, Model: modelID,
			})
			writeManagementAccountTestOptionsResult(w, result, found, err)
			return
		}
		limit := 50
		if value := firstManagementAccountTestQueryText(r.URL.Query(), "limit"); value != "" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > 50 {
				writeMessageError(w, http.StatusBadRequest, "limit 必须是 1 到 50 的整数")
				return
			}
			limit = parsed
		}
		result, found, err := service.Options(r, managementaccounttestoptions.OptionsInput{
			AccountID:       input.AccountID,
			SystemAccountID: input.SystemAccountID,
			Keyword:         firstManagementAccountTestQueryText(r.URL.Query(), "keyword"),
			Limit:           limit,
			SelectedIDs:     append([]string(nil), r.URL.Query()["selectedIds"]...),
		})
		writeManagementAccountTestOptionsResult(w, result, found, err)
	})
}

func writeManagementAccountTestOptionsResult(w http.ResponseWriter, result any, found bool, err error) {
	if message, validation := managementaccounttestoptions.ValidationMessage(err); validation {
		writeMessageError(w, http.StatusBadRequest, message)
		return
	}
	if err != nil {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if !found {
		writeMessageError(w, http.StatusNotFound, "账户不存在")
		return
	}
	writeData(w, http.StatusOK, result)
}

func managementAccountTestOptionsInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountTestOptionsScope,
	accountID string,
) (managementaccounttestoptions.Input, bool) {
	input := managementaccounttestoptions.Input{AccountID: accountID}
	switch scope {
	case managementAccountTestOptionsScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounttestoptions.Input{}, false
		}
		systemAccountID := firstManagementAccountTestQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountTestOptionsScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}

func firstManagementAccountTestQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
}
