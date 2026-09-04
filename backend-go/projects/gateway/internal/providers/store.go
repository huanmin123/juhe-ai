// Package providers owns the M11 vertical slice: the management-plane read
// family over the Node-owned provider catalog tables (providers,
// provider_protocol_profiles, provider_model_catalog), ported from
// backend/src/modules/providers/providers.routes.ts plus
// provider.repository.ts / provider-model-catalog.repository.ts. Node mounts
// the router once and distinguishes the management view via the viewScope
// query; the Go gateway mirrors the groups slice instead: an admin surface on
// /providers and a force-self surface on /my-providers serving the same
// global catalog rows. The model write family (custom models, built-in model
// patches, default-health-check-model preferences) depends on the C03 model
// pricing/catalog service and is mounted as a documented 400 deferral.
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

// Limits mirror the slice pagination bounds. The two preferred profile ids
// mirror provider-protocol.ts (gemini native v1beta, glm coding openai v1).
const (
	maxProviderListPageSize   = 200
	defaultProviderListPage   = 50
	preferredGeminiProfileID  = "profile_gemini_native_v1beta"
	preferredGLMCodingProfile = "profile_glm_coding_openai_v1"
)

// Store is the dual-mode provider catalog persistence (SQLite + PostgreSQL).
// The catalog tables keep INTEGER 0/1 flags in both modes (Node
// providerEnabledPredicate), so enabled checks compare against 1.
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
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

// ProtocolProfile mirrors ProviderProtocolProfileDefinition.
type ProtocolProfile struct {
	ID                      string   `json:"id"`
	ProviderCode            string   `json:"providerCode"`
	Name                    string   `json:"name"`
	Description             *string  `json:"description,omitempty"`
	Enabled                 bool     `json:"enabled"`
	ProtocolCode            string   `json:"protocolCode"`
	ProtocolVersion         string   `json:"protocolVersion"`
	BaseURL                 string   `json:"baseUrl"`
	DefaultHealthCheckModel string   `json:"defaultHealthCheckModel"`
	AccountTypes            []string `json:"accountTypes"`
	Capabilities            []string `json:"capabilities"`
}

// CatalogModel mirrors the ProviderModelCatalogItem projection the detail
// view serves (a subset of provider_model_catalog columns).
type CatalogModel struct {
	ID                    string   `json:"id"`
	ProviderCode          string   `json:"providerCode"`
	Model                 string   `json:"model"`
	Status                string   `json:"status"`
	Mode                  *string  `json:"mode,omitempty"`
	CatalogOrder          *int64   `json:"catalogOrder,omitempty"`
	ReleaseDate           *string  `json:"releaseDate,omitempty"`
	ShutdownDate          *string  `json:"shutdownDate,omitempty"`
	SupportedAPIProtocols []string `json:"supportedApiProtocols"`
	ContextWindowTokens   *int64   `json:"contextWindowTokens,omitempty"`
	MaxInputTokens        *int64   `json:"maxInputTokens,omitempty"`
	MaxOutputTokens       *int64   `json:"maxOutputTokens,omitempty"`
	InputUsdPer1M         *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M        *float64 `json:"outputUsdPer1M,omitempty"`
	SupportsPromptCaching bool     `json:"supportsPromptCaching"`
	CatalogVisible        bool     `json:"catalogVisible"`
	UpdatedAt             string   `json:"updatedAt"`
}

// ListItem mirrors the management provider list row: the providers table
// row, the preferred default protocol profile (Node listProviderListItems
// lateral pick) and the provider_model_catalog count.
type ListItem struct {
	ID                       string   `json:"id"`
	Code                     string   `json:"code"`
	Name                     string   `json:"name"`
	ParentCode               *string  `json:"parentCode,omitempty"`
	Description              *string  `json:"description,omitempty"`
	Enabled                  bool     `json:"enabled"`
	DefaultSupportedModels   []string `json:"defaultSupportedModels"`
	DefaultProtocolProfileID string   `json:"defaultProtocolProfileId"`
	ProtocolCode             string   `json:"protocolCode"`
	ProtocolVersion          string   `json:"protocolVersion"`
	BaseURL                  string   `json:"baseUrl"`
	DefaultHealthCheckModel  string   `json:"defaultHealthCheckModel"`
	AccountTypes             []string `json:"accountTypes"`
	Capabilities             []string `json:"capabilities"`
	ModelCatalogCount        int      `json:"modelCatalogCount"`
	CreatedAt                string   `json:"createdAt"`
	UpdatedAt                string   `json:"updatedAt"`
}

// Detail mirrors the provider detail: the list row plus every protocol
// profile and the model catalog entries.
type Detail struct {
	ListItem
	ProtocolProfiles []ProtocolProfile `json:"protocolProfiles"`
	Models           []CatalogModel    `json:"models"`
}

// ListPageResult mirrors the slice pagination envelope (groups shape).
type ListPageResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type providerRow struct {
	id                      string
	code                    string
	name                    string
	parentCode              sql.NullString
	description             sql.NullString
	enabled                 bool
	defaultSupportedModels  sql.NullString
	defaultProfileID        sql.NullString
	protocolCode            sql.NullString
	protocolVersion         sql.NullString
	baseURL                 sql.NullString
	defaultHealthCheckModel sql.NullString
	accountTypes            sql.NullString
	capabilities            sql.NullString
	defaultProfileEnabled   sql.NullInt64
	createdAt               string
	updatedAt               string
	modelCatalogCount       int
}

// preferredProfileOrder mirrors the Node default-profile pick: enabled first,
// then the gemini-native / glm-coding preferred ids, then recency.
const preferredProfileOrder = `candidate.enabled DESC,
		CASE WHEN candidate.id IN (?, ?) THEN 0 ELSE 1 END,
		candidate.updated_at DESC, candidate.id ASC`

func (s *Store) providerColumns() string {
	return `p.id, p.code, p.name, p.parent_code, p.description, p.enabled,
		p.default_supported_models_json, p.created_at, p.updated_at,
		ppp.id, ppp.protocol_code, ppp.protocol_version, ppp.base_url,
		ppp.default_health_check_model, ppp.account_types_json, ppp.capabilities_json,
		ppp.enabled,
		(SELECT COUNT(*) FROM ` + s.table("provider_model_catalog") + ` catalog_count
			WHERE catalog_count.provider_code = p.code) AS model_catalog_count`
}

// providerJoin mirrors the Node listProviderListItems lateral: one preferred
// default profile per provider.
func (s *Store) providerJoin() string {
	return ` LEFT JOIN ` + s.table("provider_protocol_profiles") + ` ppp ON ppp.id = (
		SELECT candidate.id FROM ` + s.table("provider_protocol_profiles") + ` candidate
		WHERE candidate.provider_code = p.code
		ORDER BY ` + preferredProfileOrder + ` LIMIT 1
	)`
}

func scanProviderRow(scan func(...any) error) (providerRow, error) {
	var row providerRow
	var enabled int
	err := scan(&row.id, &row.code, &row.name, &row.parentCode, &row.description, &enabled,
		&row.defaultSupportedModels, &row.createdAt, &row.updatedAt,
		&row.defaultProfileID, &row.protocolCode, &row.protocolVersion, &row.baseURL,
		&row.defaultHealthCheckModel, &row.accountTypes, &row.capabilities,
		&row.defaultProfileEnabled, &row.modelCatalogCount)
	if err != nil {
		return providerRow{}, err
	}
	row.enabled = enabled == 1
	return row, nil
}

func (s *Store) newListItem(row providerRow) ListItem {
	item := ListItem{
		ID:                     row.id,
		Code:                   row.code,
		Name:                   row.name,
		ParentCode:             nullPtrString(row.parentCode),
		Description:            nullPtrString(row.description),
		Enabled:                row.enabled,
		DefaultSupportedModels: parseJSONArray(row.defaultSupportedModels),
		AccountTypes:           parseJSONArray(row.accountTypes),
		Capabilities:           parseJSONArray(row.capabilities),
		ModelCatalogCount:      row.modelCatalogCount,
		CreatedAt:              row.createdAt,
		UpdatedAt:              row.updatedAt,
	}
	if row.defaultProfileID.Valid {
		item.DefaultProtocolProfileID = row.defaultProfileID.String
		item.ProtocolCode = row.protocolCode.String
		item.ProtocolVersion = row.protocolVersion.String
		item.BaseURL = row.baseURL.String
		item.DefaultHealthCheckModel = row.defaultHealthCheckModel.String
	}
	return item
}

// ListPage mirrors the management list: every provider row (the Node
// management view shows disabled providers too) with the preferred default
// profile and the model catalog count, ordered by name then code, pageSize+1
// probe and the paged total upper bound.
func (s *Store) ListPage(ctx context.Context, page, pageSize int, keyword string) (*ListPageResult, error) {
	ctx = ensureCtx(ctx)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultProviderListPage
	}
	pageSize = minInt(maxProviderListPageSize, pageSize)
	// The providerJoin carries the first two bind values (the preferred
	// profile id pair), so they lead the argument list.
	args := append([]any{}, preferredProfileArgs()...)
	clauses := []string{}
	text := strings.TrimSpace(keyword)
	if text != "" {
		clauses = append(clauses, "(p.name >= ? AND p.name < ? OR p.code >= ? AND p.code < ?)")
		args = append(args, text, textPrefixUpperBound(text), text, textPrefixUpperBound(text))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, pageSize+1, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+s.providerColumns()+`
		FROM `+s.table("providers")+` p`+s.providerJoin()+where+`
		ORDER BY p.name ASC, p.code ASC
		LIMIT ? OFFSET ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []providerRow{}
	for rows.Next() {
		row, scanErr := scanProviderRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(records) > pageSize
	if hasMore {
		records = records[:pageSize]
	}
	items := make([]ListItem, 0, len(records))
	for _, row := range records {
		items = append(items, s.newListItem(row))
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	return &ListPageResult{Items: items, Total: total, HasMore: hasMore, Page: page, PageSize: pageSize}, nil
}

// FindDetail resolves one provider by code first (the Node :code routes) and
// falls back to the row id (the slice {id} contract), with the full protocol
// profile list and the model catalog ordered by catalog_order then model.
// Returns (nil, nil) when no row matches (route renders 404).
func (s *Store) FindDetail(ctx context.Context, key string) (*Detail, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(key)
	if id == "" {
		return nil, nil
	}
	row, err := s.findProviderRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	profiles, err := s.listProfiles(ctx, row.code)
	if err != nil {
		return nil, err
	}
	models, err := s.listCatalogModels(ctx, row.code)
	if err != nil {
		return nil, err
	}
	return &Detail{ListItem: s.newListItem(*row), ProtocolProfiles: profiles, Models: models}, nil
}

func (s *Store) findProviderRow(ctx context.Context, key string) (*providerRow, error) {
	row, err := s.findProviderRowBy(ctx, "p.code = ?", key)
	if err != nil {
		return nil, err
	}
	if row == nil {
		row, err = s.findProviderRowBy(ctx, "p.id = ?", key)
	}
	return row, err
}

func (s *Store) findProviderRowBy(ctx context.Context, clause, value string) (*providerRow, error) {
	args := append([]any{}, preferredProfileArgs()...)
	args = append(args, value)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+s.providerColumns()+`
		FROM `+s.table("providers")+` p`+s.providerJoin()+`
		WHERE `+clause+`
		LIMIT 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanProviderRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return &record, rows.Err()
	}
	return nil, rows.Err()
}

// preferredProfileArgs feeds the CASE WHEN candidate.id IN (?, ?) pair of the
// preferred-profile pick.
func preferredProfileArgs() []any {
	return []any{preferredGeminiProfileID, preferredGLMCodingProfile}
}

// listProfiles mirrors listProviderProtocolProfiles (all profiles of the
// provider, enabled first is not applied here: the detail view is the raw
// management read).
func (s *Store) listProfiles(ctx context.Context, providerCode string) ([]ProtocolProfile, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, name, description, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model,
		account_types_json, capabilities_json
		FROM `+s.table("provider_protocol_profiles")+`
		WHERE provider_code = ?
		ORDER BY updated_at DESC, id ASC`), providerCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []ProtocolProfile{}
	for rows.Next() {
		var profile ProtocolProfile
		var enabled int
		var description sql.NullString
		var accountTypes, capabilities sql.NullString
		if err := rows.Scan(&profile.ID, &profile.ProviderCode, &profile.Name, &description, &enabled,
			&profile.ProtocolCode, &profile.ProtocolVersion, &profile.BaseURL, &profile.DefaultHealthCheckModel,
			&accountTypes, &capabilities); err != nil {
			return nil, err
		}
		profile.Enabled = enabled == 1
		profile.Description = nullPtrString(description)
		profile.AccountTypes = parseJSONArray(accountTypes)
		profile.Capabilities = parseJSONArray(capabilities)
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// listCatalogModels reads the provider_model_catalog rows of the provider.
// The boolean flags stay INTEGER 0/1 in both modes (Node
// providerEnabledPredicate); the CASE normalizes the PG boolean variant.
func (s *Store) listCatalogModels(ctx context.Context, providerCode string) ([]CatalogModel, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, model, status, mode, catalog_order,
		release_date, shutdown_date, supported_api_protocols_json,
		context_window_tokens, max_input_tokens, max_output_tokens,
		input_usd_per_1m, output_usd_per_1m,
		CASE WHEN supports_prompt_caching THEN 1 ELSE 0 END,
		CASE WHEN catalog_visible THEN 1 ELSE 0 END,
		updated_at
		FROM `+s.table("provider_model_catalog")+`
		WHERE provider_code = ?
		ORDER BY (catalog_order IS NULL) ASC, catalog_order ASC, model ASC`), providerCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []CatalogModel{}
	for rows.Next() {
		var model CatalogModel
		var mode, releaseDate, shutdownDate, supportedProtocols sql.NullString
		var catalogOrder, contextWindow, maxInput, maxOutput sql.NullInt64
		var inputUsd, outputUsd sql.NullFloat64
		var promptCaching, catalogVisible int
		if err := rows.Scan(&model.ID, &model.ProviderCode, &model.Model, &model.Status, &mode, &catalogOrder,
			&releaseDate, &shutdownDate, &supportedProtocols,
			&contextWindow, &maxInput, &maxOutput, &inputUsd, &outputUsd,
			&promptCaching, &catalogVisible, &model.UpdatedAt); err != nil {
			return nil, err
		}
		model.Mode = nullPtrString(mode)
		model.ReleaseDate = nullPtrString(releaseDate)
		model.ShutdownDate = nullPtrString(shutdownDate)
		model.SupportedAPIProtocols = parseJSONArray(supportedProtocols)
		model.CatalogOrder = nullInt64Ptr(catalogOrder)
		model.ContextWindowTokens = nullInt64Ptr(contextWindow)
		model.MaxInputTokens = nullInt64Ptr(maxInput)
		model.MaxOutputTokens = nullInt64Ptr(maxOutput)
		model.InputUsdPer1M = nullFloat64Ptr(inputUsd)
		model.OutputUsdPer1M = nullFloat64Ptr(outputUsd)
		model.SupportsPromptCaching = promptCaching == 1
		model.CatalogVisible = catalogVisible == 1
		models = append(models, model)
	}
	return models, rows.Err()
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}

func nullFloat64Ptr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	copied := value.Float64
	return &copied
}

// textPrefixUpperBound mirrors the shared prefix upper bound (codePoint + 1).
func textPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10ffff {
			runes[index]++
			return string(runes[:index+1])
		}
	}
	return value + "\uffff"
}
