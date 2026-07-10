package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
)

type managementRouteStrategyOptionScope int

const (
	managementRouteStrategyScopeAdmin managementRouteStrategyOptionScope = iota
	managementRouteStrategyScopeSelf
)

type managementRouteStrategyOptionService interface {
	Options(r *http.Request, input managementroutestrategies.OptionListInput) ([]managementroutestrategies.Option, error)
}

type managementRouteStrategyOptionServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyOptionServiceAdapter) Options(r *http.Request, input managementroutestrategies.OptionListInput) ([]managementroutestrategies.Option, error) {
	return s.service.Options(r.Context(), input)
}

func NewManagementRouteStrategyOptionsHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyOptionsHandler(managementRouteStrategyOptionServiceAdapter{service: service}, managementRouteStrategyScopeAdmin)
}

func NewManagementMyRouteStrategyOptionsHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyOptionsHandler(managementRouteStrategyOptionServiceAdapter{service: service}, managementRouteStrategyScopeSelf)
}

func newManagementRouteStrategyOptionsHandler(service managementRouteStrategyOptionService, scope managementRouteStrategyOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementRouteStrategyOptionListInput(authContext, r.URL.Query(), scope)
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

func managementRouteStrategyOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementRouteStrategyOptionScope,
) (managementroutestrategies.OptionListInput, bool) {
	input := parseManagementRouteStrategyOptionListQuery(values)
	switch scope {
	case managementRouteStrategyScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementroutestrategies.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementRouteStrategyScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func parseManagementRouteStrategyOptionListQuery(values url.Values) managementroutestrategies.OptionListInput {
	activeOnly := true
	if value, ok := managementBooleanQueryValue(values, "activeOnly"); ok {
		activeOnly = value
	}
	return managementroutestrategies.OptionListInput{
		IDs:        managementTextListQueryValue(values, "ids", 50),
		Keyword:    firstManagementQueryText(values, "keyword"),
		Limit:      managementIntegerQueryValue(values, "limit"),
		ActiveOnly: activeOnly,
	}
}

func managementTextListQueryValue(values url.Values, key string, maxItems int) []string {
	items := values[key]
	if len(items) == 0 {
		return nil
	}
	output := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		for _, part := range strings.Split(item, ",") {
			text := strings.TrimSpace(part)
			if text == "" {
				continue
			}
			if _, exists := seen[text]; exists {
				continue
			}
			seen[text] = struct{}{}
			output = append(output, text)
			if len(output) >= maxItems {
				return output
			}
		}
	}
	return output
}

func managementBooleanQueryValue(values url.Values, key string) (bool, bool) {
	text := strings.ToLower(firstManagementQueryText(values, key))
	switch text {
	case "1", "true", "yes":
		return true, true
	case "0", "false", "no":
		return false, true
	default:
		return false, false
	}
}
