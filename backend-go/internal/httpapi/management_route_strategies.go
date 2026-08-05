package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

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

type managementRouteStrategyListService interface {
	List(r *http.Request, input managementroutestrategies.ListInput) (managementroutestrategies.ListResult, error)
}

type managementRouteStrategyListServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyListServiceAdapter) List(
	r *http.Request,
	input managementroutestrategies.ListInput,
) (managementroutestrategies.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementRouteStrategyListHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyListHandler(
		managementRouteStrategyListServiceFrom(service),
		managementRouteStrategyScopeAdmin,
	)
}

func NewManagementMyRouteStrategyListHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyListHandler(
		managementRouteStrategyListServiceFrom(service),
		managementRouteStrategyScopeSelf,
	)
}

func managementRouteStrategyListServiceFrom(service *managementroutestrategies.Service) managementRouteStrategyListService {
	if service == nil {
		return nil
	}
	return managementRouteStrategyListServiceAdapter{service: service}
}

type managementRouteStrategyDetailService interface {
	Detail(r *http.Request, input managementroutestrategies.DetailInput) (managementroutestrategies.DetailResult, error)
}

// managementRouteStrategyEditBasicResponse matches the Node edit form projection.
// Keep runtime counters and creation metadata out of this response.
type managementRouteStrategyEditBasicResponse struct {
	ID                  string                                          `json:"id"`
	SystemAccountID     string                                          `json:"systemAccountId,omitempty"`
	Name                string                                          `json:"name"`
	Description         *string                                         `json:"description,omitempty"`
	Mode                string                                          `json:"mode"`
	Status              string                                          `json:"status"`
	IsDefault           bool                                            `json:"isDefault"`
	NormalRoutingConfig *managementroutestrategies.NormalRoutingConfig  `json:"normalRoutingConfig,omitempty"`
	HybridRoutingConfig map[string]any                                  `json:"hybridRoutingConfig,omitempty"`
	GroupBindings       []managementroutestrategies.GroupBindingSummary `json:"groupBindings"`
	UpdatedAt           string                                          `json:"updatedAt"`
}

type managementRouteStrategyDetailServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyDetailServiceAdapter) Detail(
	r *http.Request,
	input managementroutestrategies.DetailInput,
) (managementroutestrategies.DetailResult, error) {
	return s.service.Detail(r.Context(), input)
}

func NewManagementRouteStrategyDetailHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyDetailHandler(
		managementRouteStrategyDetailServiceFrom(service),
		managementRouteStrategyScopeAdmin,
	)
}

func NewManagementMyRouteStrategyDetailHandler(service *managementroutestrategies.Service) http.Handler {
	return newManagementRouteStrategyDetailHandler(
		managementRouteStrategyDetailServiceFrom(service),
		managementRouteStrategyScopeSelf,
	)
}

func managementRouteStrategyDetailServiceFrom(service *managementroutestrategies.Service) managementRouteStrategyDetailService {
	if service == nil {
		return nil
	}
	return managementRouteStrategyDetailServiceAdapter{service: service}
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

func newManagementRouteStrategyListHandler(
	service managementRouteStrategyListService,
	scope managementRouteStrategyOptionScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementRouteStrategyScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		result, err := service.List(r, managementRouteStrategyListInput(authContext, r.URL.Query(), scope))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementRouteStrategyListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementRouteStrategyOptionScope,
) managementroutestrategies.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	input := managementroutestrategies.ListInput{
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorRole:            authContext.Role,
		Page:                 page,
		PageSize:             pageSize,
		PageSizeProvided:     pageSizeProvided,
		Keyword:              firstManagementRouteStrategyListQueryText(values, "keyword"),
		Mode:                 firstManagementRouteStrategyListQueryText(values, "mode"),
		Status:               firstManagementRouteStrategyListQueryText(values, "status"),
	}
	switch scope {
	case managementRouteStrategyScopeAdmin:
		systemAccountID := firstManagementRouteStrategyListQueryText(values, "systemAccountId")
		if systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementRouteStrategyScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.SelfOnly = true
	}
	return input
}

func firstManagementRouteStrategyListQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
}

func newManagementRouteStrategyDetailHandler(
	service managementRouteStrategyDetailService,
	scope managementRouteStrategyOptionScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementRouteStrategyScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		systemAccountID := ""
		selfOnly := scope == managementRouteStrategyScopeSelf
		if !selfOnly {
			var message string
			systemAccountID, message, ok = managementGroupDetailSystemAccountID(r.URL.Query())
			if !ok {
				writeMessageError(w, http.StatusBadRequest, message)
				return
			}
		}
		result, err := service.Detail(r, managementroutestrategies.DetailInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			RouteStrategyID:      chi.URLParam(r, "id"),
		})
		switch {
		case errors.Is(err, managementroutestrategies.ErrRouteStrategyNotFound):
			writeMessageError(w, http.StatusNotFound, "策略路由不存在")
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		default:
			if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/edit-basic") {
				writeData(w, http.StatusOK, managementRouteStrategyEditBasicResponse{
					ID:                  result.ID,
					SystemAccountID:     result.SystemAccountID,
					Name:                result.Name,
					Description:         result.Description,
					Mode:                result.Mode,
					Status:              result.Status,
					IsDefault:           result.IsDefault,
					NormalRoutingConfig: result.NormalRoutingConfig,
					HybridRoutingConfig: result.HybridRoutingConfig,
					GroupBindings:       result.GroupBindings,
					UpdatedAt:           result.UpdatedAt,
				})
				return
			}
			writeData(w, http.StatusOK, result)
		}
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
