// Package providers owns the T5 management read family over the 供应商解耦
// target schema（供应商与上游协议面重构设计 §8）：vendors（9 个真实厂商，
// 只读种子）与 models 单表（原 provider_model_catalog +
// custom_provider_models 合并，见 catalog.go / custom_models.go /
// builtin_patch.go）。旧 providers/provider_protocol_profiles/
// provider_protocol_profile_families/protocol_endpoint_families 四表与
// provider_default_health_check_models/provider_system_default_health_check_models
// 两张默认检查模型偏好表已从 fresh DDL 删除（设计 §9.11）：供应商读侧改
// vendors 投影，协议档案维度随表退役；capabilities 与 defaultHealthCheckModel
// 改由 models 单表内置目录行只读派生（协议并集、最新发布时间文本模型）。
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxProviderDefinitions mirrors the slice limit in the archived provider
// repository; the catalog list/definitions/options queries keep the same
// ceiling over vendors.
const maxProviderDefinitions = 50

// Store is the dual-mode vendor catalog persistence (SQLite + PostgreSQL).
// The vendors.enabled flag keeps INTEGER 0/1 in both modes (the fresh DDL
// declares it integer on PostgreSQL too), so enabled checks compare against 1.
type Store struct {
	db  *sql.DB
	pg  bool
	now func() time.Time
}

// NewStore builds the store.
func NewStore(db *sql.DB, postgres bool, now func() time.Time) (*Store, error) {
	if db == nil {
		return nil, errors.New("providers store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, pg: postgres, now: now}, nil
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

// bind rewrites ? placeholders into $N for PostgreSQL.
func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func textPtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value.String)
	return &trimmed
}

func parseJSONArray(value sql.NullString) []string {
	output := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return output
	}
	var raw []any
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return output
	}
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			output = append(output, text)
		}
	}
	return output
}

// ProtocolProfile mirrors the archived ProviderProtocolProfileDefinition DTO.
// The profile tables are retired (设计 §9.11), so every produced value is the
// empty shape; the type stays because ProviderDefinition.ProtocolProfiles and
// the /:code payloads keep the key for the not-yet-migrated frontend.
type ProtocolProfile struct {
	ID                      string           `json:"id"`
	ProviderCode            string           `json:"providerCode"`
	Name                    string           `json:"name"`
	Description             *string          `json:"description,omitempty"`
	Enabled                 bool             `json:"enabled"`
	ProtocolCode            string           `json:"protocolCode"`
	ProtocolVersion         string           `json:"protocolVersion"`
	BaseURL                 string           `json:"baseUrl"`
	DefaultHealthCheckModel string           `json:"defaultHealthCheckModel"`
	AccountTypes            []string         `json:"accountTypes"`
	Capabilities            []string         `json:"capabilities"`
	EndpointFamilies        []EndpointFamily `json:"endpointFamilies"`
}

// EndpointFamily mirrors the archived ProtocolEndpointFamilyDefinition DTO
// (retired with the profile tables; kept for the ProtocolProfile shape).
type EndpointFamily struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// ProviderDefinition mirrors the flat provider DTO of GET /providers,
// GET /providers/definitions and GET /providers/{code}. Vendor rows carry no
// parent and no protocol profiles any more: parentCode/baseUrl/accountTypes
// stay empty, protocolProfiles stays [], while capabilities and
// defaultHealthCheckModel are derived from the vendor's built-in models rows.
type ProviderDefinition struct {
	ID                            string            `json:"id"`
	Code                          string            `json:"code"`
	Name                          string            `json:"name"`
	ParentCode                    *string           `json:"parentCode,omitempty"`
	Description                   *string           `json:"description,omitempty"`
	Enabled                       bool              `json:"enabled"`
	DefaultProtocolProfileID      string            `json:"defaultProtocolProfileId"`
	ProtocolCode                  string            `json:"protocolCode"`
	ProtocolVersion               string            `json:"protocolVersion"`
	BaseURL                       string            `json:"baseUrl"`
	DefaultHealthCheckModel       string            `json:"defaultHealthCheckModel"`
	SystemDefaultHealthCheckModel *string           `json:"systemDefaultHealthCheckModel,omitempty"`
	DefaultSupportedModels        []string          `json:"defaultSupportedModels"`
	AccountTypes                  []string          `json:"accountTypes"`
	Capabilities                  []string          `json:"capabilities"`
	ProtocolProfiles              []ProtocolProfile `json:"protocolProfiles"`
}

// ProviderListItem mirrors the catalogue list row of GET /providers/list (the
// archived mapProviderListRow shape; capabilities/defaultHealthCheckModel are
// derived from the vendor's built-in models rows).
type ProviderListItem struct {
	ID                      string   `json:"id"`
	Code                    string   `json:"code"`
	Name                    string   `json:"name"`
	ParentCode              *string  `json:"parentCode,omitempty"`
	Description             *string  `json:"description,omitempty"`
	Enabled                 bool     `json:"enabled"`
	ProtocolCode            string   `json:"protocolCode"`
	BaseURL                 string   `json:"baseUrl"`
	DefaultHealthCheckModel string   `json:"defaultHealthCheckModel"`
	DefaultSupportedModels  []string `json:"defaultSupportedModels"`
	AccountTypes            []string `json:"accountTypes"`
	Capabilities            []string `json:"capabilities"`
}

// ProviderOption mirrors GET /providers/options rows (vendors projection).
type ProviderOption struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// vendorRowColumns is the shared vendors projection for the list/definitions
// reads (name ASC, code ASC, bounded by maxProviderDefinitions like the
// archived provider list).
const vendorRowColumns = `id, code, name, description, enabled, default_supported_models_json`

// ListCatalogListItems serves the GET /list rows over vendors（旧 providers 行
// 经 protocol profile lateral 的协议字段随表退役，恒渲染空形状）.
func (s *Store) ListCatalogListItems(ctx context.Context) ([]ProviderListItem, error) {
	ctx = ensureCtx(ctx)
	items := []ProviderListItem{}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vendorRowColumns+`
		FROM `+s.table("vendors")+`
		ORDER BY name ASC, code ASC LIMIT ?`), maxProviderDefinitions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanVendorListItem(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 关闭行集后再查 models：测试与生产都可能有单连接池，不能在打开的
	// rows 上再发查询。
	rows.Close()
	if err := s.applyListItemCatalogSummaries(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

func scanVendorListItem(scan func(...any) error) (ProviderListItem, error) {
	var (
		item                   ProviderListItem
		enabled                int
		description            sql.NullString
		defaultSupportedModels sql.NullString
	)
	if err := scan(&item.ID, &item.Code, &item.Name, &description, &enabled, &defaultSupportedModels); err != nil {
		return ProviderListItem{}, err
	}
	item.Enabled = enabled == 1
	item.Description = nullPtrString(description)
	item.DefaultSupportedModels = parseJSONArray(defaultSupportedModels)
	item.AccountTypes = []string{}
	item.Capabilities = []string{}
	return item, nil
}

// ListDefinitions serves GET /providers and GET /providers/definitions: the
// flat vendors rows with the retired protocol keys rendered empty.
func (s *Store) ListDefinitions(ctx context.Context) ([]ProviderDefinition, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vendorRowColumns+`
		FROM `+s.table("vendors")+`
		ORDER BY name ASC, code ASC LIMIT ?`), maxProviderDefinitions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	definitions := []ProviderDefinition{}
	for rows.Next() {
		definition, scanErr := scanVendorDefinition(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := s.applyDefinitionCatalogSummaries(ctx, definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

// FindDefinition resolves one vendor by code (the GET /providers/{code}
// lookup). Returns (nil, nil) when no row matches (route renders 404).
func (s *Store) FindDefinition(ctx context.Context, code string) (*ProviderDefinition, error) {
	ctx = ensureCtx(ctx)
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+vendorRowColumns+`
		FROM `+s.table("vendors")+`
		WHERE code = ?
		ORDER BY name ASC, code ASC LIMIT 1`), trimmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found *ProviderDefinition
	for rows.Next() {
		definition, scanErr := scanVendorDefinition(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		found = &definition
		break
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if found == nil {
		return nil, nil
	}
	summaries, err := s.vendorCatalogSummaries(ctx, []string{found.Code})
	if err != nil {
		return nil, err
	}
	if summary, ok := summaries[found.Code]; ok {
		found.Capabilities = summary.Capabilities
		found.DefaultHealthCheckModel = summary.DefaultHealthCheckModel
	}
	return found, nil
}

func scanVendorDefinition(scan func(...any) error) (ProviderDefinition, error) {
	var (
		definition             ProviderDefinition
		enabled                int
		description            sql.NullString
		defaultSupportedModels sql.NullString
	)
	if err := scan(&definition.ID, &definition.Code, &definition.Name, &description, &enabled, &defaultSupportedModels); err != nil {
		return ProviderDefinition{}, err
	}
	definition.Enabled = enabled == 1
	definition.Description = nullPtrString(description)
	definition.DefaultSupportedModels = parseJSONArray(defaultSupportedModels)
	definition.AccountTypes = []string{}
	definition.Capabilities = []string{}
	definition.ProtocolProfiles = []ProtocolProfile{}
	return definition, nil
}

// ListProviderOptions serves GET /providers/options: enabled vendor rows only.
func (s *Store) ListProviderOptions(ctx context.Context) ([]ProviderOption, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, enabled
		FROM `+s.table("vendors")+`
		WHERE enabled = 1
		ORDER BY name ASC, code ASC LIMIT ?`), maxProviderDefinitions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []ProviderOption{}
	for rows.Next() {
		option, scanErr := scanVendorOption(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

// FindProviderOption mirrors the code lookup; the enabled flag rides along for
// the route-level 404 forks.
func (s *Store) FindProviderOption(ctx context.Context, code string) (*ProviderOption, error) {
	ctx = ensureCtx(ctx)
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, enabled
		FROM `+s.table("vendors")+`
		WHERE code = ?
		LIMIT 1`), trimmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		option, scanErr := scanVendorOption(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return &option, rows.Err()
	}
	return nil, rows.Err()
}

func scanVendorOption(scan func(...any) error) (ProviderOption, error) {
	var (
		option  ProviderOption
		enabled int
	)
	if err := scan(&option.ID, &option.Code, &option.Name, &enabled); err != nil {
		return ProviderOption{}, err
	}
	option.Enabled = enabled == 1
	return option, nil
}

// EnabledNonHybridProviderCodes serves the /models/options fallback source
// pick: enabled vendors, already ordered by name then code. B4 ④：hybrid 门
// 拆除——ListDefinitions 读 vendors（设计 §439 只保留 §1.1 九真实厂商，hybrid
// 不在值域），过滤无真实语义（拆前恒 false，行为不变）。
func (s *Store) EnabledNonHybridProviderCodes(ctx context.Context) ([]string, error) {
	definitions, err := s.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, definition := range definitions {
		if !definition.Enabled {
			continue
		}
		trimmed := strings.TrimSpace(definition.Code)
		if trimmed == "" {
			continue
		}
		codes = append(codes, trimmed)
	}
	return codes, nil
}

// vendorCatalogSummary 是供应商摘要里由 models 单表只读派生的两项事实：
// 该 vendor 内置目录声明的协议并集，以及最新发布时间、可用于健康检查的
// 文本生成模型。供应商级默认检查模型配置表已随重构删除（设计 §9.11），
// 这里必须是纯派生，不能再引入第二份可写状态。
type vendorCatalogSummary struct {
	Capabilities            []string
	DefaultHealthCheckModel string
}

// healthCheckTextProtocols 是可用于协议最小检查的文本生成协议集合，与
// isProviderModelUsableForAccountTest 同源；媒体协议（images/video/audio/
// realtime/embedding）不参与默认检查模型选择。
var healthCheckTextProtocols = map[string]bool{
	"chat_completions":        true,
	"responses":               true,
	"messages":                true,
	"generate_content":        true,
	"stream_generate_content": true,
}

// vendorCatalogSummaries 按 vendor code 汇总 models 单表的内置目录行：
// capabilities 取 SupportedAPIProtocols 并集，defaultHealthCheckModel 取
// 最新发布且文本可用的模型（纯媒体 vendor 回落最新模型）。
func (s *Store) vendorCatalogSummaries(ctx context.Context, codes []string) (map[string]vendorCatalogSummary, error) {
	summaries := map[string]vendorCatalogSummary{}
	normalized := normalizeProviderCodeList(codes)
	if len(normalized) == 0 {
		return summaries, nil
	}
	items, err := s.listBuiltInCatalogModels(ctx, normalized, false)
	if err != nil {
		return nil, err
	}
	byVendor := map[string][]ModelCatalogItem{}
	for _, item := range items {
		byVendor[item.ProviderCode] = append(byVendor[item.ProviderCode], item)
	}
	for code, models := range byVendor {
		protocols := map[string]bool{}
		for _, model := range models {
			for _, protocol := range model.SupportedAPIProtocols {
				protocols[protocol] = true
			}
		}
		capabilities := make([]string, 0, len(protocols))
		for protocol := range protocols {
			capabilities = append(capabilities, protocol)
		}
		sort.Strings(capabilities)
		summaries[code] = vendorCatalogSummary{
			Capabilities:            capabilities,
			DefaultHealthCheckModel: latestHealthCheckModel(models),
		}
	}
	return summaries, nil
}

func (s *Store) applyListItemCatalogSummaries(ctx context.Context, items []ProviderListItem) error {
	codes := make([]string, 0, len(items))
	for index := range items {
		codes = append(codes, items[index].Code)
	}
	summaries, err := s.vendorCatalogSummaries(ctx, codes)
	if err != nil {
		return err
	}
	for index := range items {
		summary, ok := summaries[items[index].Code]
		if !ok {
			items[index].Capabilities = []string{}
			continue
		}
		items[index].Capabilities = summary.Capabilities
		items[index].DefaultHealthCheckModel = summary.DefaultHealthCheckModel
	}
	return nil
}

func (s *Store) applyDefinitionCatalogSummaries(ctx context.Context, definitions []ProviderDefinition) error {
	codes := make([]string, 0, len(definitions))
	for index := range definitions {
		codes = append(codes, definitions[index].Code)
	}
	summaries, err := s.vendorCatalogSummaries(ctx, codes)
	if err != nil {
		return err
	}
	for index := range definitions {
		summary, ok := summaries[definitions[index].Code]
		if !ok {
			definitions[index].Capabilities = []string{}
			continue
		}
		definitions[index].Capabilities = summary.Capabilities
		definitions[index].DefaultHealthCheckModel = summary.DefaultHealthCheckModel
	}
	return nil
}

// latestHealthCheckModel 返回目录内最新发布且可用于文本生成的模型；没有
// 文本模型时回落到最新目录模型。release_date 为空视为最旧，同日按
// catalog_order 升序（空值最后）和 model 名升序稳定兜底。
func latestHealthCheckModel(models []ModelCatalogItem) string {
	if name := latestCatalogModelName(models, true); name != "" {
		return name
	}
	return latestCatalogModelName(models, false)
}

func latestCatalogModelName(models []ModelCatalogItem, textOnly bool) string {
	candidates := make([]ModelCatalogItem, 0, len(models))
	for _, model := range models {
		if textOnly && !isTextGenerationModel(model) {
			continue
		}
		candidates = append(candidates, model)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return catalogModelNewer(candidates[left], candidates[right])
	})
	return candidates[0].Model
}

func isTextGenerationModel(model ModelCatalogItem) bool {
	if len(model.SupportedAPIProtocols) == 0 {
		return true
	}
	for _, protocol := range model.SupportedAPIProtocols {
		if healthCheckTextProtocols[protocol] {
			return true
		}
	}
	return false
}

func catalogModelNewer(left, right ModelCatalogItem) bool {
	leftRelease := releaseDateText(left.ReleaseDate)
	rightRelease := releaseDateText(right.ReleaseDate)
	if leftRelease != rightRelease {
		if leftRelease == "" {
			return false
		}
		if rightRelease == "" {
			return true
		}
		return leftRelease > rightRelease
	}
	leftOrder, leftHasOrder := catalogOrderValue(left.CatalogOrder)
	rightOrder, rightHasOrder := catalogOrderValue(right.CatalogOrder)
	if leftHasOrder != rightHasOrder {
		return leftHasOrder
	}
	if leftHasOrder && leftOrder != rightOrder {
		return leftOrder < rightOrder
	}
	return left.Model < right.Model
}

func releaseDateText(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func catalogOrderValue(value *int64) (int64, bool) {
	if value == nil {
		return 0, false
	}
	return *value, true
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// normalizeProviderCodeList normalizes code lists (trim, drop empties,
// dedupe) for the catalog-side vendor windows.
func normalizeProviderCodeList(codes []string) []string {
	seen := map[string]bool{}
	output := []string{}
	for _, code := range codes {
		trimmed := strings.TrimSpace(code)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		output = append(output, trimmed)
	}
	return output
}
