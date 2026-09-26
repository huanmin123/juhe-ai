// Public provider family (list + detail), the read-only projection over the
// providers.Store definitions (providers.ListDefinitions / FindDefinition /
// ListProviderModelsForRequest). The detail route splits into the basic tab
// (provider + protocol profiles, no health-check preference overlay) and the
// models tab (the active+priced merged catalog with a server-side category
// derivation ported from the frontend providerModelCategoryRules.ts name
// rules). Both tabs page in memory behind the shared page envelope.
package aipublic

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
)

// PublicProviderSummary mirrors the public provider projection: the flat
// fields of ProviderDefinition the external sources may see.
type PublicProviderSummary struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	ParentCode  *string `json:"parentCode,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     bool    `json:"enabled"`
}

// PublicProviderEndpointFamily mirrors the {code, name} family projection of
// ProviderDefinition.ProtocolProfiles[].endpointFamilies.
type PublicProviderEndpointFamily struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// PublicProviderProtocolProfile mirrors the public protocol-profile
// projection (direct ProviderDefinition.ProtocolProfiles fields; the
// health-check preference overlays the management family applies are not
// stacked on top).
type PublicProviderProtocolProfile struct {
	ID                      string                         `json:"id"`
	Name                    string                         `json:"name"`
	Description             *string                        `json:"description,omitempty"`
	Enabled                 bool                           `json:"enabled"`
	ProtocolCode            string                         `json:"protocolCode"`
	ProtocolVersion         string                         `json:"protocolVersion"`
	BaseURL                 string                         `json:"baseUrl"`
	DefaultHealthCheckModel string                         `json:"defaultHealthCheckModel"`
	AccountTypes            []string                       `json:"accountTypes"`
	Capabilities            []string                       `json:"capabilities"`
	EndpointFamilies        []PublicProviderEndpointFamily `json:"endpointFamilies"`
}

// PublicProviderModelItem mirrors the models-tab whitelist: ModelCatalogItem
// fields re-emitted under their existing json tags plus the server-side
// category.
type PublicProviderModelItem struct {
	ID                    string   `json:"-"`
	Model                 string   `json:"model"`
	Category              string   `json:"category"`
	Status                string   `json:"status"`
	ContextWindowTokens   *int64   `json:"contextWindowTokens,omitempty"`
	MaxInputTokens        *int64   `json:"maxInputTokens,omitempty"`
	MaxOutputTokens       *int64   `json:"maxOutputTokens,omitempty"`
	InputUsdPer1M         *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M        *float64 `json:"outputUsdPer1M,omitempty"`
	CachedInputUsdPer1M   *float64 `json:"cachedInputUsdPer1M,omitempty"`
	SupportsPromptCaching bool     `json:"supportsPromptCaching"`
	SourcePricingCurrency string   `json:"sourcePricingCurrency,omitempty"`
}

// providerModelCategory mirrors the model-name category rules of the frontend
// providerModelCategoryRules.ts (image prefixes first, text fallback kept as
// the terminal rule so new name rules slot in before it).
type providerModelCategoryRule struct {
	category string
	matches  func(model string) bool
}

var providerModelNameCategoryRules = []providerModelCategoryRule{
	{
		category: "image",
		matches: func(model string) bool {
			return strings.HasPrefix(model, "gpt-image") || strings.HasPrefix(model, "dall-e")
		},
	},
	{
		category: "text",
		matches: func(model string) bool {
			return true
		},
	},
}

// publicModelCategory resolves the display category from the model name
// alone (trimmed + lowercased like the frontend rule input).
func publicModelCategory(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, rule := range providerModelNameCategoryRules {
		if rule.matches(normalized) {
			return rule.category
		}
	}
	return "text"
}

// providerListQuery mirrors the provider list schema (strict; no
// targetUsername — providers are global definitions).
type providerListQuery struct {
	Page        int
	HasPage     bool
	PageSize    int
	HasPageSize bool
}

func parseProviderListQuery(values url.Values) (*providerListQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values), "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &providerListQuery{}
	page, has, issue := parseOptionalQueryInt(values, "page", 1, 0)
	if issue != "" {
		return nil, issue
	}
	query.Page, query.HasPage = page, has
	pageSize, hasPageSize, issue := parseOptionalQueryInt(values, "pageSize", 1, 100)
	if issue != "" {
		return nil, issue
	}
	query.PageSize, query.HasPageSize = pageSize, hasPageSize
	return query, ""
}

// providerDetailTabs / providerModelCategories mirror the detail enums.
var providerDetailTabs = []string{"basic", "models"}
var providerModelCategories = []string{"text", "image"}

// providerDetailQuery mirrors the detail schema (strict). category/page/
// pageSize belong to the models tab; the basic tab rejects them like unknown
// keys (the conditional zod object).
type providerDetailQuery struct {
	Code        string
	Tab         string
	Category    string
	HasCategory bool
	Page        int
	HasPage     bool
	PageSize    int
	HasPageSize bool
}

func parseProviderDetailQuery(values url.Values) (*providerDetailQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values), "code", "tab", "category", "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &providerDetailQuery{}
	code, issue := parseQueryString(values, "code", true, 1, 60)
	if issue != "" {
		return nil, issue
	}
	query.Code = code
	tab, issue := parseQueryString(values, "tab", true, 1, 0)
	if issue != "" {
		return nil, issue
	}
	if !containsString(providerDetailTabs, tab) {
		return nil, zodEnumMessage(providerDetailTabs, tab)
	}
	query.Tab = tab
	if query.Tab != "models" {
		// The basic tab carries only code+tab: category/page/pageSize are
		// rejected like unknown keys (the conditional models schema).
		if unknown := strictObjectKeys(valuesAsMap(values), "code", "tab"); unknown != nil {
			return nil, zodUnrecognizedKeys(unknown...)
		}
		return query, ""
	}
	category, hasCategory, issue := parseOptionalQueryEnum(values, "category", providerModelCategories)
	if issue != "" {
		return nil, issue
	}
	query.Category, query.HasCategory = category, hasCategory
	page, hasPage, issue := parseOptionalQueryInt(values, "page", 1, 0)
	if issue != "" {
		return nil, issue
	}
	query.Page, query.HasPage = page, hasPage
	pageSize, hasPageSize, issue := parseOptionalQueryInt(values, "pageSize", 1, 100)
	if issue != "" {
		return nil, issue
	}
	query.PageSize, query.HasPageSize = pageSize, hasPageSize
	return query, ""
}

// listProviders mirrors GET /__aipublic__/provider/list: the enabled subset
// of providers.Store.ListDefinitions (name ASC, code ASC, capped at 50 by the
// store) paged in memory.
func (d *Deps) listProviders(w http.ResponseWriter, r *http.Request) {
	query, issue := parseProviderListQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		page, pageSize := d.mockPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
		d.mockProviderList(w, page, pageSize)
		return
	}
	page, pageSize := d.paging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	definitions, err := d.Providers.ListDefinitions(r.Context())
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	enabled := make([]providers.ProviderDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Enabled {
			enabled = append(enabled, definition)
		}
	}
	start, end, hasMore := paginateIndexes(len(enabled), page, pageSize)
	pageItems := make([]PublicProviderSummary, 0, end-start)
	for _, definition := range enabled[start:end] {
		pageItems = append(pageItems, publicProviderSummary(&definition))
	}
	d.writeStatsEnvelope(w, map[string]any{
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": pagedTotalUpperBound(page, pageSize, len(pageItems), hasMore),
		"hasMore":        hasMore,
		"items":          pageItems,
	})
}

// providerDetail mirrors GET /__aipublic__/provider/detail: the definition
// lookup rejects disabled/unknown providers with the shared 404, then the
// basic/models tabs project their own envelopes.
func (d *Deps) providerDetail(w http.ResponseWriter, r *http.Request) {
	query, issue := parseProviderDetailQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockProviderDetail(w, query)
		return
	}
	definition, err := d.Providers.FindDefinition(r.Context(), query.Code)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if definition == nil || !definition.Enabled {
		kernel.WriteNotFound(w, "供应商不存在或已停用")
		return
	}
	if query.Tab == "basic" {
		d.writeProviderBasicDetail(w, definition)
		return
	}
	d.writeProviderModelDetail(w, r, query, definition)
}

// writeProviderBasicDetail renders the basic tab: provider + profiles with no
// pagination fields.
func (d *Deps) writeProviderBasicDetail(w http.ResponseWriter, definition *providers.ProviderDefinition) {
	profiles := make([]PublicProviderProtocolProfile, 0, len(definition.ProtocolProfiles))
	for _, profile := range definition.ProtocolProfiles {
		families := make([]PublicProviderEndpointFamily, 0, len(profile.EndpointFamilies))
		for _, family := range profile.EndpointFamilies {
			families = append(families, PublicProviderEndpointFamily{Code: family.Code, Name: family.Name})
		}
		profiles = append(profiles, PublicProviderProtocolProfile{
			ID:                      profile.ID,
			Name:                    profile.Name,
			Description:             profile.Description,
			Enabled:                 profile.Enabled,
			ProtocolCode:            profile.ProtocolCode,
			ProtocolVersion:         profile.ProtocolVersion,
			BaseURL:                 profile.BaseURL,
			DefaultHealthCheckModel: profile.DefaultHealthCheckModel,
			AccountTypes:            nonNilStrings(profile.AccountTypes),
			Capabilities:            nonNilStrings(profile.Capabilities),
			EndpointFamilies:        families,
		})
	}
	d.writeStatsEnvelope(w, map[string]any{
		"provider":               publicProviderSummary(definition),
		"defaultSupportedModels": nonNilStrings(definition.DefaultSupportedModels),
		"protocolProfiles":       profiles,
	})
}

// writeProviderModelDetail renders the models tab: the active+priced merged
// catalog (hybrid expansion stays inside the store), the server-side
// category, the optional category filter and the shared page envelope.
func (d *Deps) writeProviderModelDetail(w http.ResponseWriter, r *http.Request, query *providerDetailQuery, definition *providers.ProviderDefinition) {
	catalog, err := d.Providers.ListProviderModelsForRequest(r.Context(), definition.Code, "", false, false)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	items := make([]PublicProviderModelItem, 0, len(catalog))
	for _, item := range catalog {
		category := publicModelCategory(item.Model)
		if query.HasCategory && query.Category != category {
			continue
		}
		items = append(items, PublicProviderModelItem{
			ID:                    item.ID,
			Model:                 item.Model,
			Category:              category,
			Status:                item.Status,
			ContextWindowTokens:   item.ContextWindowTokens,
			MaxInputTokens:        item.MaxInputTokens,
			MaxOutputTokens:       item.MaxOutputTokens,
			InputUsdPer1M:         item.InputUsdPer1M,
			OutputUsdPer1M:        item.OutputUsdPer1M,
			CachedInputUsdPer1M:   item.CachedInputUsdPer1M,
			SupportsPromptCaching: item.SupportsPromptCaching,
			SourcePricingCurrency: item.SourcePricingCurrency,
		})
	}
	// model ASC stable order with the unique id as the tiebreaker (the
	// release-date ordering of the store does not survive the category
	// projection; the public contract is the stable model order).
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Model != items[right].Model {
			return items[left].Model < items[right].Model
		}
		return items[left].ID < items[right].ID
	})
	page, pageSize := d.paging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	start, end, hasMore := paginateIndexes(len(items), page, pageSize)
	pageItems := items[start:end]
	payload := map[string]any{
		"provider":       map[string]any{"code": definition.Code, "name": definition.Name},
		"page":           page,
		"pageSize":       pageSize,
		"pageUpperBound": pagedTotalUpperBound(page, pageSize, len(pageItems), hasMore),
		"hasMore":        hasMore,
		"items":          pageItems,
	}
	if query.HasCategory {
		payload["category"] = query.Category
	}
	d.writeStatsEnvelope(w, payload)
}

func publicProviderSummary(definition *providers.ProviderDefinition) PublicProviderSummary {
	return PublicProviderSummary{
		Code:        definition.Code,
		Name:        definition.Name,
		ParentCode:  definition.ParentCode,
		Description: definition.Description,
		Enabled:     definition.Enabled,
	}
}

// paginateIndexes resolves the [start, end) window of an in-memory page plus
// the hasMore flag (one extra item past the window).
func paginateIndexes(total, page, pageSize int) (start, end int, hasMore bool) {
	start = (page - 1) * pageSize
	if start > total {
		start = total
	}
	end = start + pageSize
	if end > total {
		end = total
	}
	return start, end, end < total
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
