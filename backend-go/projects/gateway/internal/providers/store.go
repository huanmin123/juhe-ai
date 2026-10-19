// Package providers owns the T5 management read family over the 供应商解耦
// target schema（供应商与上游协议面重构设计 §8）：vendors（9 个真实厂商，
// 只读种子）与 models 单表（原 provider_model_catalog +
// custom_provider_models 合并，见 catalog.go / custom_models.go /
// builtin_patch.go）。旧 providers/provider_protocol_profiles/
// provider_protocol_profile_families/protocol_endpoint_families 四表与
// provider_default_health_check_models/provider_system_default_health_check_models
// 两张默认检查模型偏好表已从 fresh DDL 删除（设计 §8.11）：供应商读侧改
// vendors 投影，协议档案维度随表退役（DTO 键保留、恒空/空数组），偏好表读写
// 按 groups 管理面「入参忽略不落库」同一先例收敛（overlay 恒空、保存不落库）。
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
// The profile tables are retired (设计 §8.11), so every produced value is the
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
// parent and no protocol profiles any more: parentCode is always absent, the
// protocol/base-url/default-health-check keys render the empty shape and
// protocolProfiles stays []（键名保留，前端未迁移）。
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
// archived mapProviderListRow shape; the protocol/profile keys render empty).
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
	return items, rows.Err()
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
	return definitions, rows.Err()
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
	for rows.Next() {
		definition, scanErr := scanVendorDefinition(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return &definition, rows.Err()
	}
	return nil, rows.Err()
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

// ListDefaultHealthCheckModelPreferences renders the personal per-provider
// default health-check model overlay. The storage table
// (provider_default_health_check_models) is retired by 设计 §8.11，overlay 恒
// 空（与 groups 管理面 providerCode 入参忽略不落库同一先例；键名保留）。
func (s *Store) ListDefaultHealthCheckModelPreferences(ctx context.Context, systemAccountID string, providerCodes []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// ListSystemDefaultHealthCheckModels renders the system default overlay; the
// storage table (provider_system_default_health_check_models) is retired by
// 设计 §8.11，overlay 恒空.
func (s *Store) ListSystemDefaultHealthCheckModels(ctx context.Context, providerCodes []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// UpsertDefaultHealthCheckModelPreference keeps the PUT contract shape but no
// longer persists：偏好表随供应商解耦退役（设计 §8.11），保存按 groups 管理面
// 「忽略不落库」先例收敛，路由仍返回校验后的成功体.
func (s *Store) UpsertDefaultHealthCheckModelPreference(ctx context.Context, systemAccountID, providerCode, model string) error {
	return nil
}

// UpsertSystemDefaultHealthCheckModel keeps the PUT contract shape but no
// longer persists（同上：偏好表退役，忽略不落库）.
func (s *Store) UpsertSystemDefaultHealthCheckModel(ctx context.Context, providerCode, model string) error {
	return nil
}

// overlayListItemHealthCheckModels keeps the /list overlay composition
// (personal preference wins, then the system default, then the row default);
// with both preference maps retired-empty the overlay is a no-op.
func overlayListItemHealthCheckModels(items []ProviderListItem, preferences, systemDefaults map[string]string) {
	for index := range items {
		code := items[index].Code
		if model := preferences[code]; model != "" {
			items[index].DefaultHealthCheckModel = model
			continue
		}
		if model := systemDefaults[code]; model != "" {
			items[index].DefaultHealthCheckModel = model
		}
	}
}

// overlayDefinitionHealthCheckModels keeps the ProviderDefinition overlay
// composition; with both preference maps retired-empty it is a no-op and the
// systemDefaultHealthCheckModel key stays absent.
func overlayDefinitionHealthCheckModels(definitions []ProviderDefinition, preferences, systemDefaults map[string]string) {
	for index := range definitions {
		definition := &definitions[index]
		personal := preferences[definition.Code]
		system := systemDefaults[definition.Code]
		if personal != "" {
			definition.DefaultHealthCheckModel = personal
		} else if system != "" {
			definition.DefaultHealthCheckModel = system
		}
		if system != "" {
			copied := system
			definition.SystemDefaultHealthCheckModel = &copied
		}
	}
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// normalizeProviderCodeList keeps the normalized code lists of the retired
// preference repositories (trim, drop empties, dedupe) for the catalog-side
// vendor windows.
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
