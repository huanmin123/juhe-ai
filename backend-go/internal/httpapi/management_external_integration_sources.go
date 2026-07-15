package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
)

type managementExternalIntegrationSourceListService interface {
	List(context.Context, managementexternalintegrationsources.ListInput) (managementexternalintegrationsources.ListResult, error)
}

type managementExternalIntegrationSourceDetailService interface {
	Get(context.Context, string) (*managementexternalintegrationsources.Detail, error)
}

func NewManagementExternalIntegrationSourceListHandler(
	service *managementexternalintegrationsources.Service,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceListHandler(nil)
	}
	return newManagementExternalIntegrationSourceListHandler(service)
}

func NewManagementExternalIntegrationSourceDetailHandler(
	service *managementexternalintegrationsources.Service,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceDetailHandler(nil)
	}
	return newManagementExternalIntegrationSourceDetailHandler(service)
}

func NewManagementExternalIntegrationSourceScopesHandler() http.Handler {
	return newManagementExternalIntegrationSourceScopesHandler()
}

func NewManagementExternalIntegrationSourceAPIDocsHandler() http.Handler {
	return newManagementExternalIntegrationSourceAPIDocsHandler()
}

func newManagementExternalIntegrationSourceListHandler(
	service managementExternalIntegrationSourceListService,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		input, validationMessage := managementExternalIntegrationSourceListInput(r.URL.Query())
		if validationMessage != "" {
			writeMessageError(w, http.StatusBadRequest, validationMessage)
			return
		}
		result, err := service.List(r.Context(), input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementExternalIntegrationSourceDetailHandler(
	service managementExternalIntegrationSourceDetailService,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		sourceID := strings.TrimFunc(chi.URLParam(r, "id"), managementGroupListECMAScriptWhitespace)
		if sourceID == "" {
			writeMessageError(w, http.StatusBadRequest, "来源系统不存在")
			return
		}
		detail, err := service.Get(r.Context(), sourceID)
		switch {
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		case detail == nil:
			writeMessageError(w, http.StatusNotFound, "来源系统不存在")
		default:
			writeData(w, http.StatusOK, detail)
		}
	})
}

func managementExternalIntegrationSourceListInput(
	values url.Values,
) (managementexternalintegrationsources.ListInput, string) {
	page, _, message := managementExternalIntegrationSourceListIntegerQueryValue(values, "page", 0)
	if message != "" {
		return managementexternalintegrationsources.ListInput{}, message
	}
	pageSize, pageSizeProvided, message := managementExternalIntegrationSourceListIntegerQueryValue(values, "pageSize", 100)
	if message != "" {
		return managementexternalintegrationsources.ListInput{}, message
	}
	keyword, message := managementExternalIntegrationSourceListStringQueryValue(values, "keyword", true)
	if message != "" {
		return managementexternalintegrationsources.ListInput{}, message
	}
	status, message := managementExternalIntegrationSourceListStatusQueryValue(values)
	if message != "" {
		return managementexternalintegrationsources.ListInput{}, message
	}
	return managementexternalintegrationsources.ListInput{
		Page:             page,
		PageSize:         pageSize,
		PageSizeProvided: pageSizeProvided,
		Keyword:          keyword,
		Status:           status,
	}, ""
}

func managementExternalIntegrationSourceListIntegerQueryValue(
	values url.Values,
	key string,
	maximum int,
) (int, bool, string) {
	queryValue := managementExternalIntegrationSourceListQueryValueForKey(values, key)
	if queryValue.kind == managementExternalIntegrationSourceListQueryMissing {
		return 0, false, ""
	}
	if queryValue.kind == managementExternalIntegrationSourceListQueryObject ||
		len(queryValue.items) != 1 ||
		(queryValue.kind == managementExternalIntegrationSourceListQueryArray && !queryValue.numberCoercible) {
		return 0, false, "Expected number, received nan"
	}
	text := strings.TrimFunc(queryValue.items[0], managementGroupListECMAScriptWhitespace)
	value, ok := managementExternalIntegrationSourceListNumber(text)
	if !ok || math.IsNaN(value) {
		return 0, false, "Expected number, received nan"
	}
	if math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false, "Expected integer, received float"
	}
	if value < 1 {
		return 0, false, "Number must be greater than or equal to 1"
	}
	if maximum > 0 && value > float64(maximum) {
		return 0, false, fmt.Sprintf("Number must be less than or equal to %d", maximum)
	}

	maxInt := int(^uint(0) >> 1)
	if value >= float64(maxInt) {
		return maxInt, true, ""
	}
	return int(value), true, ""
}

type managementExternalIntegrationSourceListQueryKind uint8

const (
	managementExternalIntegrationSourceListQueryMissing managementExternalIntegrationSourceListQueryKind = iota
	managementExternalIntegrationSourceListQueryScalar
	managementExternalIntegrationSourceListQueryArray
	managementExternalIntegrationSourceListQueryObject
)

type managementExternalIntegrationSourceListQueryValue struct {
	kind            managementExternalIntegrationSourceListQueryKind
	items           []string
	numberCoercible bool
}

const managementExternalIntegrationSourceListQueryMaximumDepth = 5

func managementExternalIntegrationSourceListQueryValueForKey(
	values url.Values,
	key string,
) managementExternalIntegrationSourceListQueryValue {
	directItems, directExists := values[key]
	items := append([]string(nil), directItems...)
	bracketExists := false
	outerObject := false
	numberCoercible := true

	for rawKey, bracketItems := range values {
		segments, ok := managementExternalIntegrationSourceListBracketSegments(rawKey, key)
		if !ok {
			continue
		}
		bracketExists = true
		items = append(items, bracketItems...)
		if !managementExternalIntegrationSourceListArraySegment(segments[0]) {
			outerObject = true
		}
		if len(segments) > managementExternalIntegrationSourceListQueryMaximumDepth {
			numberCoercible = false
		}
		for _, segment := range segments {
			if !managementExternalIntegrationSourceListArraySegment(segment) {
				numberCoercible = false
			}
		}
	}

	if !directExists && !bracketExists {
		return managementExternalIntegrationSourceListQueryValue{
			kind: managementExternalIntegrationSourceListQueryMissing,
		}
	}
	if directExists && !bracketExists {
		kind := managementExternalIntegrationSourceListQueryScalar
		if len(directItems) != 1 {
			kind = managementExternalIntegrationSourceListQueryArray
		}
		return managementExternalIntegrationSourceListQueryValue{
			kind:            kind,
			items:           items,
			numberCoercible: false,
		}
	}
	if directExists {
		return managementExternalIntegrationSourceListQueryValue{
			kind:            managementExternalIntegrationSourceListQueryArray,
			items:           items,
			numberCoercible: false,
		}
	}
	if outerObject {
		return managementExternalIntegrationSourceListQueryValue{
			kind:            managementExternalIntegrationSourceListQueryObject,
			items:           items,
			numberCoercible: false,
		}
	}
	return managementExternalIntegrationSourceListQueryValue{
		kind:            managementExternalIntegrationSourceListQueryArray,
		items:           items,
		numberCoercible: numberCoercible,
	}
}

func managementExternalIntegrationSourceListBracketSegments(rawKey string, key string) ([]string, bool) {
	if !strings.HasPrefix(rawKey, key+"[") {
		return nil, false
	}
	rest := rawKey[len(key):]
	segments := make([]string, 0, 2)
	for {
		start := strings.IndexByte(rest, '[')
		if start < 0 {
			break
		}
		endOffset := strings.IndexByte(rest[start+1:], ']')
		if endOffset < 0 {
			break
		}
		end := start + 1 + endOffset
		segments = append(segments, rest[start+1:end])
		rest = rest[end+1:]
	}
	return segments, len(segments) > 0
}

func managementExternalIntegrationSourceListArraySegment(segment string) bool {
	if segment == "" || segment == "0" {
		return true
	}
	if segment[0] == '0' {
		return false
	}
	value, err := strconv.Atoi(segment)
	return err == nil && value > 0 && value < 20
}

func managementExternalIntegrationSourceListNumber(text string) (float64, bool) {
	if text == "" {
		return 0, true
	}
	switch text {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if value, ok := managementGroupListNumber(text); ok {
		return value, true
	}
	if managementGroupListDecimalNumberPattern.MatchString(text) {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil && math.IsInf(value, 0) {
			return value, true
		}
	}
	return 0, false
}

func managementExternalIntegrationSourceListStringQueryValue(
	values url.Values,
	key string,
	trim bool,
) (string, string) {
	queryValue := managementExternalIntegrationSourceListQueryValueForKey(values, key)
	switch queryValue.kind {
	case managementExternalIntegrationSourceListQueryMissing:
		return "", ""
	case managementExternalIntegrationSourceListQueryArray:
		return "", "Expected string, received array"
	case managementExternalIntegrationSourceListQueryObject:
		return "", "Expected string, received object"
	}
	if trim {
		return strings.TrimFunc(queryValue.items[0], managementGroupListECMAScriptWhitespace), ""
	}
	return queryValue.items[0], ""
}

func managementExternalIntegrationSourceListStatusQueryValue(values url.Values) (string, string) {
	queryValue := managementExternalIntegrationSourceListQueryValueForKey(values, "status")
	switch queryValue.kind {
	case managementExternalIntegrationSourceListQueryMissing:
		return "", ""
	case managementExternalIntegrationSourceListQueryArray:
		return "", "Expected 'all' | 'active' | 'disabled', received array"
	case managementExternalIntegrationSourceListQueryObject:
		return "", "Expected 'all' | 'active' | 'disabled', received object"
	}
	status := queryValue.items[0]
	switch status {
	case "all", "active", "disabled":
		return status, ""
	default:
		return "", fmt.Sprintf("Invalid enum value. Expected 'all' | 'active' | 'disabled', received '%s'", status)
	}
}

func newManagementExternalIntegrationSourceScopesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		writeData(w, http.StatusOK, publicapi.ScopeOptions())
	})
}

func newManagementExternalIntegrationSourceAPIDocsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		writeData(w, http.StatusOK, publicapi.APIDocsCatalog())
	})
}
