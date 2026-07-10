package httpapi

import (
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

var managementGroupListDecimalNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

type managementGroupListService interface {
	List(r *http.Request, input managementgroups.ListInput) (managementgroups.ListResult, error)
}

type managementGroupListServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupListServiceAdapter) List(
	r *http.Request,
	input managementgroups.ListInput,
) (managementgroups.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementGroupListHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupListHandler(managementGroupListServiceFrom(service), managementGroupScopeAdmin)
}

func NewManagementMyGroupListHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupListHandler(managementGroupListServiceFrom(service), managementGroupScopeSelf)
}

func managementGroupListServiceFrom(service *managementgroups.Service) managementGroupListService {
	if service == nil {
		return nil
	}
	return managementGroupListServiceAdapter{service: service}
}

func newManagementGroupListHandler(
	service managementGroupListService,
	scope managementGroupOptionScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementGroupScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		result, err := service.List(r, managementGroupListInput(authContext, r.URL.Query(), scope))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementGroupListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementGroupOptionScope,
) managementgroups.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	input := managementgroups.ListInput{
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorRole:            authContext.Role,
		Page:                 page,
		PageSize:             pageSize,
		PageSizeProvided:     pageSizeProvided,
	}
	switch scope {
	case managementGroupScopeAdmin:
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementGroupScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.SelfOnly = true
	}
	return input
}

func managementGroupListIntegerQueryValue(values url.Values, key string) (int, bool) {
	items := values[key]
	if len(items) == 0 {
		return 0, false
	}
	text := strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
	if text == "" {
		return 0, false
	}
	value, ok := managementGroupListNumber(text)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}

	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if value >= float64(maxInt) {
		return maxInt, true
	}
	if value <= float64(minInt) {
		return minInt, true
	}
	return int(value), true
}

func managementGroupListECMAScriptWhitespace(r rune) bool {
	switch r {
	case '\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020',
		'\u00A0', '\u1680', '\u2028', '\u2029', '\u202F', '\u205F',
		'\u3000', '\uFEFF':
		return true
	default:
		return r >= '\u2000' && r <= '\u200A'
	}
}

func managementGroupListNumber(text string) (float64, bool) {
	lower := strings.ToLower(text)
	for prefix, base := range map[string]int{"0x": 16, "0b": 2, "0o": 8} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		digits := lower[len(prefix):]
		if !managementGroupListDigitsValid(digits, base) {
			return 0, false
		}
		integer := new(big.Int)
		if _, ok := integer.SetString(digits, base); !ok {
			return 0, false
		}
		value, _ := integer.Float64()
		return value, true
	}
	if !managementGroupListDecimalNumberPattern.MatchString(text) {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil && (math.IsInf(value, 0) || math.IsNaN(value)) {
		return 0, false
	}
	return value, true
}

func managementGroupListDigitsValid(text string, base int) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		value := -1
		switch {
		case char >= '0' && char <= '9':
			value = int(char - '0')
		case char >= 'a' && char <= 'f':
			value = int(char-'a') + 10
		}
		if value < 0 || value >= base {
			return false
		}
	}
	return true
}
