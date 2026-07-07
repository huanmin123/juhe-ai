package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementproxies"
)

type managementProxyOptionService interface {
	Options(r *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error)
}

type managementProxyOptionServiceAdapter struct {
	service *managementproxies.Service
}

func (s managementProxyOptionServiceAdapter) Options(r *http.Request, input managementproxies.OptionListInput) ([]managementproxies.Option, error) {
	return s.service.Options(r.Context(), input)
}

func NewManagementProxyOptionsHandler(service *managementproxies.Service) http.Handler {
	return newManagementProxyOptionsHandler(managementProxyOptionServiceAdapter{service: service})
}

func newManagementProxyOptionsHandler(service managementProxyOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := parseManagementProxyOptionListQuery(r.URL.Query())
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func parseManagementProxyOptionListQuery(values url.Values) managementproxies.OptionListInput {
	return managementproxies.OptionListInput{
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}

func firstManagementQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func managementIntegerQueryValue(values url.Values, key string) int {
	text := firstManagementQueryText(values, key)
	if text == "" {
		return 0
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}
