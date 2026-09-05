// Package providers owns the T5 management read family over the Node-owned
// provider catalog tables (providers, provider_protocol_profiles,
// provider_protocol_profile_families, protocol_endpoint_families,
// provider_model_catalog, custom_provider_models,
// provider_default_health_check_models,
// provider_system_default_health_check_models), ported from
// backend/src/modules/providers/providers.routes.ts plus
// provider.repository.ts / provider-model-catalog.repository.ts /
// custom-provider-models.repository.ts. Node mounts the router once at
// ${systemApiPrefix}/providers (no my- surface) and distinguishes the
// management view via the viewScope=admin query; the Go gateway mirrors that
// contract directly. The model write family (custom models, built-in model
// patches, default-health-check-model preferences) depends on the C03 model
// pricing/catalog service and stays a documented 400 deferral.
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

// maxProviderDefinitions and maxProviderProtocolProfiles mirror the slice
// limits in provider.repository.ts. The two preferred profile ids mirror
// provider-protocol.ts (gemini native v1beta, glm coding openai v1).
const (
	maxProviderDefinitions      = 50
	maxProviderProtocolProfiles = 200
	preferredGeminiProfileID    = "profile_gemini_native_v1beta"
	preferredGLMCodingProfile   = "profile_glm_coding_openai_v1"
	// Provider/protocol codes from domain/provider-protocol.ts.
	geminiProviderCode       = "gemini"
	glmProviderCode          = "glm"
	hybridProviderCode       = "hybrid"
	openaiProviderCode       = "openai" // OPENAI_COMPATIBLE_PROVIDER_CODE
	openaiProtocolCode       = "openai"
	openaiProtocolVersion    = "v1"
	anthropicProtocolCode    = "anthropic"
	anthropicProtocolVersion = "v1"
	geminiProtocolCode       = "gemini"
	geminiProtocolVersion    = "v1beta"
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

// EndpointFamily mirrors ProtocolEndpointFamilyDefinition.
type EndpointFamily struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// ProtocolProfile mirrors ProviderProtocolProfileDefinition.
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

// ProviderDefinition mirrors the Node ProviderDefinition DTO (the flat
// contract of GET /providers, GET /providers/definitions and
// GET /providers/{code}); systemDefaultHealthCheckModel rides on the
// providerWithDefaultHealthCheckModelPreference overlay.
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

// ProviderListItem mirrors the catalogue list row of GET /providers/list
// (mapProviderListRow): the preferred default profile rides along without
// its id/version and without protocol profiles.
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

// ProviderOption mirrors GET /providers/options rows.
type ProviderOption struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// ListCatalogListItems mirrors listProviderListItemsAsync (GET /list rows):
// every provider with the lateral preferred default profile, ordered by name
// then code, bounded by maxProviderDefinitions. Preference overlay is applied
// by the route (listProviderListItemsForRequestAsync).
func (s *Store) ListCatalogListItems(ctx context.Context) ([]ProviderListItem, error) {
	ctx = ensureCtx(ctx)
	args := append([]any{}, preferredProfileArgs()...)
	args = append(args, maxProviderDefinitions)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT p.id, p.code, p.name, p.parent_code, p.description,
			p.enabled, p.default_supported_models_json,
			ppp.protocol_code, ppp.base_url, ppp.default_health_check_model,
			ppp.account_types_json, ppp.capabilities_json
		FROM `+s.table("providers")+` p`+s.providerJoin()+`
		ORDER BY p.name ASC, p.code ASC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ProviderListItem{}
	for rows.Next() {
		var (
			item                   ProviderListItem
			enabled                int
			parentCode, desc       sql.NullString
			defaultSupportedModels sql.NullString
			protocolCode, baseURL  sql.NullString
			healthCheckModel       sql.NullString
			accountTypes, caps     sql.NullString
		)
		if err := rows.Scan(&item.ID, &item.Code, &item.Name, &parentCode, &desc, &enabled,
			&defaultSupportedModels, &protocolCode, &baseURL, &healthCheckModel, &accountTypes, &caps); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.ParentCode = nullPtrString(parentCode)
		item.Description = nullPtrString(desc)
		item.ProtocolCode = protocolCode.String
		item.BaseURL = baseURL.String
		item.DefaultHealthCheckModel = healthCheckModel.String
		item.DefaultSupportedModels = parseJSONArray(defaultSupportedModels)
		item.AccountTypes = parseJSONArray(accountTypes)
		item.Capabilities = parseJSONArray(caps)
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListDefinitions mirrors listProvidersAsync() (no code filter): the flat
// ProviderDefinition rows with the full protocol profile list and the
// preferred default profile fields (providerDefaultProfileFields).
func (s *Store) ListDefinitions(ctx context.Context) ([]ProviderDefinition, error) {
	ctx = ensureCtx(ctx)
	args := append([]any{}, maxProviderDefinitions)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, parent_code, description, enabled,
			default_supported_models_json
		FROM `+s.table("providers")+`
		ORDER BY name ASC, code ASC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	definitions := []*ProviderDefinition{}
	for rows.Next() {
		definition, scanErr := s.scanProviderDefinition(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachProtocolProfiles(ctx, definitions); err != nil {
		return nil, err
	}
	output := make([]ProviderDefinition, 0, len(definitions))
	for _, definition := range definitions {
		output = append(output, *definition)
	}
	return output, nil
}

// FindDefinition resolves one provider by code (the Node GET /:code lookup:
// listProvidersAsync(code), code-only). Returns (nil, nil) when no row
// matches (route renders 404).
func (s *Store) FindDefinition(ctx context.Context, code string) (*ProviderDefinition, error) {
	ctx = ensureCtx(ctx)
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, nil
	}
	args := append([]any{}, trimmed, maxProviderDefinitions)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, parent_code, description, enabled,
			default_supported_models_json
		FROM `+s.table("providers")+`
		WHERE code = ?
		ORDER BY name ASC, code ASC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var definition *ProviderDefinition
	for rows.Next() {
		record, scanErr := s.scanProviderDefinition(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		definition = record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, nil
	}
	if err := s.attachProtocolProfiles(ctx, []*ProviderDefinition{definition}); err != nil {
		return nil, err
	}
	return definition, nil
}

func (s *Store) scanProviderDefinition(scan func(...any) error) (*ProviderDefinition, error) {
	var (
		definition             ProviderDefinition
		enabled                int
		parentCode, desc       sql.NullString
		defaultSupportedModels sql.NullString
	)
	if err := scan(&definition.ID, &definition.Code, &definition.Name, &parentCode, &desc, &enabled,
		&defaultSupportedModels); err != nil {
		return nil, err
	}
	definition.Enabled = enabled == 1
	definition.ParentCode = nullPtrString(parentCode)
	definition.Description = nullPtrString(desc)
	definition.DefaultSupportedModels = parseJSONArray(defaultSupportedModels)
	definition.ProtocolProfiles = []ProtocolProfile{}
	return &definition, nil
}

// attachProtocolProfiles ports providerProtocolProfilesByProviderCode plus
// providerDefaultProfileFields: one profiles query (provider_code ASC,
// updated_at DESC, id ASC, LIMIT 200), one families query, then the
// preferred default profile fields ride onto every definition.
func (s *Store) attachProtocolProfiles(ctx context.Context, definitions []*ProviderDefinition) error {
	if len(definitions) == 0 {
		return nil
	}
	codes := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		codes = append(codes, definition.Code)
	}
	profiles, err := s.listProtocolProfiles(ctx, codes)
	if err != nil {
		return err
	}
	profileIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		profileIDs = append(profileIDs, profile.ID)
	}
	families, err := s.listEndpointFamilies(ctx, profileIDs)
	if err != nil {
		return err
	}
	for index := range profiles {
		profiles[index].EndpointFamilies = families[profiles[index].ID]
		if profiles[index].EndpointFamilies == nil {
			profiles[index].EndpointFamilies = []EndpointFamily{}
		}
	}
	profilesByCode := map[string][]ProtocolProfile{}
	for _, profile := range profiles {
		profilesByCode[profile.ProviderCode] = append(profilesByCode[profile.ProviderCode], profile)
	}
	for _, definition := range definitions {
		providerProfiles := profilesByCode[definition.Code]
		if providerProfiles == nil {
			providerProfiles = []ProtocolProfile{}
		}
		definition.ProtocolProfiles = providerProfiles
		applyPreferredDefaultProfile(definition, preferredDefaultProtocolProfile(definition.Code, providerProfiles))
	}
	return nil
}

// listProtocolProfiles mirrors listProviderProtocolProfilesAsync.
func (s *Store) listProtocolProfiles(ctx context.Context, providerCodes []string) ([]ProtocolProfile, error) {
	where := ""
	args := []any{}
	if len(providerCodes) > 0 {
		where = " WHERE provider_code IN (" + placeholders(len(providerCodes)) + ")"
		for _, code := range providerCodes {
			args = append(args, code)
		}
	}
	args = append(args, maxProviderProtocolProfiles)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, name, description, enabled,
			protocol_code, protocol_version, base_url, default_health_check_model,
			account_types_json, capabilities_json
		FROM `+s.table("provider_protocol_profiles")+where+`
		ORDER BY provider_code ASC, updated_at DESC, id ASC
		LIMIT ?`), args...)
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

// listEndpointFamilies mirrors providerEndpointFamiliesByProfileIdAsync.
func (s *Store) listEndpointFamilies(ctx context.Context, profileIDs []string) (map[string][]EndpointFamily, error) {
	result := map[string][]EndpointFamily{}
	if len(profileIDs) == 0 {
		return result, nil
	}
	args := stringSliceToAny(profileIDs)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT ppf.profile_id,
			f.family_code, f.name, f.description
		FROM `+s.table("provider_protocol_profile_families")+` ppf
		INNER JOIN `+s.table("provider_protocol_profiles")+` ppp
			ON ppp.id = ppf.profile_id
		INNER JOIN `+s.table("protocol_endpoint_families")+` f
			ON f.protocol_code = ppp.protocol_code
			AND f.protocol_version = ppp.protocol_version
			AND f.family_code = ppf.family_code
		WHERE ppf.profile_id IN (`+placeholders(len(profileIDs))+`)
			AND ppf.enabled = 1
			AND f.enabled = 1
		ORDER BY ppf.profile_id ASC, f.family_code ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var profileID string
		var family EndpointFamily
		var description sql.NullString
		if err := rows.Scan(&profileID, &family.Code, &family.Name, &description); err != nil {
			return nil, err
		}
		family.Description = nullPtrString(description)
		result[profileID] = append(result[profileID], family)
	}
	return result, rows.Err()
}

// applyPreferredDefaultProfile ports providerDefaultProfileFields: the
// preferred profile fields ride onto the definition; without any profile the
// Node shape carries empty strings and empty arrays.
func applyPreferredDefaultProfile(definition *ProviderDefinition, preferred *ProtocolProfile) {
	if preferred == nil {
		definition.DefaultProtocolProfileID = ""
		definition.ProtocolCode = ""
		definition.ProtocolVersion = ""
		definition.BaseURL = ""
		definition.DefaultHealthCheckModel = ""
		definition.AccountTypes = []string{}
		definition.Capabilities = []string{}
		return
	}
	definition.DefaultProtocolProfileID = preferred.ID
	definition.ProtocolCode = preferred.ProtocolCode
	definition.ProtocolVersion = preferred.ProtocolVersion
	definition.BaseURL = preferred.BaseURL
	definition.DefaultHealthCheckModel = preferred.DefaultHealthCheckModel
	definition.AccountTypes = preferred.AccountTypes
	definition.Capabilities = preferred.Capabilities
}

// preferredDefaultProtocolProfile mirrors the in-memory
// preferredDefaultProtocolProfile pick used by listProvidersAsync: enabled
// profiles first (else all), then the gemini-native / glm-coding preferred
// ids, then the first candidate. The definition rows keep the raw profiles
// slice order (provider_code ASC, updated_at DESC, id ASC).
func preferredDefaultProtocolProfile(providerCode string, profiles []ProtocolProfile) *ProtocolProfile {
	enabledProfiles := []ProtocolProfile{}
	for _, profile := range profiles {
		if profile.Enabled {
			enabledProfiles = append(enabledProfiles, profile)
		}
	}
	candidates := enabledProfiles
	if len(candidates) == 0 {
		candidates = profiles
	}
	if len(candidates) == 0 {
		return nil
	}
	for index := range candidates {
		if candidates[index].ProviderCode == geminiProviderCode && candidates[index].ID == preferredGeminiProfileID {
			return &candidates[index]
		}
	}
	for index := range candidates {
		if candidates[index].ProviderCode == glmProviderCode && candidates[index].ID == preferredGLMCodingProfile {
			return &candidates[index]
		}
	}
	return &candidates[0]
}

// ListProviderOptions mirrors listEnabledProviderOptionsAsync: enabled rows
// only, ordered by name then code, bounded by maxProviderDefinitions.
func (s *Store) ListProviderOptions(ctx context.Context) ([]ProviderOption, error) {
	ctx = ensureCtx(ctx)
	args := append([]any{}, maxProviderDefinitions)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, enabled
		FROM `+s.table("providers")+`
		WHERE enabled = 1
		ORDER BY name ASC, code ASC LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []ProviderOption{}
	for rows.Next() {
		var option ProviderOption
		var enabled int
		if err := rows.Scan(&option.ID, &option.Code, &option.Name, &enabled); err != nil {
			return nil, err
		}
		option.Enabled = enabled == 1
		options = append(options, option)
	}
	return options, rows.Err()
}

// FindProviderOption mirrors findProviderOptionByCodeAsync (code lookup, the
// enabled flag rides along for the route-level 404 forks).
func (s *Store) FindProviderOption(ctx context.Context, code string) (*ProviderOption, error) {
	ctx = ensureCtx(ctx)
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, code, name, enabled
		FROM `+s.table("providers")+`
		WHERE code = ?
		LIMIT 1`), trimmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var option ProviderOption
		var enabled int
		if err := rows.Scan(&option.ID, &option.Code, &option.Name, &enabled); err != nil {
			return nil, err
		}
		option.Enabled = enabled == 1
		return &option, rows.Err()
	}
	return nil, rows.Err()
}

// ProtocolProviderCodes mirrors listProtocolProviderCodesAsync: distinct
// provider codes carrying an enabled profile of the protocol, provider row
// enabled, ordered by code, bounded by maxProviderDefinitions.
func (s *Store) ProtocolProviderCodes(ctx context.Context, protocolCode, protocolVersion string) ([]string, error) {
	ctx = ensureCtx(ctx)
	args := append([]any{}, protocolCode, protocolVersion, maxProviderDefinitions)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT p.code
		FROM `+s.table("provider_protocol_profiles")+` ppp
		INNER JOIN `+s.table("providers")+` p
			ON p.code = ppp.provider_code
		WHERE p.enabled = 1
			AND ppp.enabled = 1
			AND ppp.protocol_code = ?
			AND ppp.protocol_version = ?
		ORDER BY p.code ASC
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes := []string{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// EnabledNonHybridProviderCodes mirrors the /models/options fallback source
// pick: listProvidersAsync() filtered to enabled non-hybrid providers
// (already ordered by name then code, LIMIT 50).
func (s *Store) EnabledNonHybridProviderCodes(ctx context.Context) ([]string, error) {
	definitions, err := s.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, definition := range definitions {
		if !definition.Enabled || isHybridProviderCode(definition.Code) {
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

// ListDefaultHealthCheckModelPreferences mirrors
// listProviderDefaultHealthCheckModelPreferencesAsync: the personal
// per-system-account preference rows; empty systemAccountID yields no rows.
func (s *Store) ListDefaultHealthCheckModelPreferences(ctx context.Context, systemAccountID string, providerCodes []string) (map[string]string, error) {
	ctx = ensureCtx(ctx)
	result := map[string]string{}
	trimmedAccount := strings.TrimSpace(systemAccountID)
	if trimmedAccount == "" {
		return result, nil
	}
	codes := normalizeProviderCodeList(providerCodes)
	where := " WHERE system_account_id = ?"
	args := append([]any{}, trimmedAccount)
	if len(codes) > 0 {
		where += " AND provider_code IN (" + placeholders(len(codes)) + ")"
		for _, code := range codes {
			args = append(args, code)
		}
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model
		FROM `+s.table("provider_default_health_check_models")+where+`
		ORDER BY provider_code ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, model string
		if err := rows.Scan(&code, &model); err != nil {
			return nil, err
		}
		code = strings.TrimSpace(code)
		model = strings.TrimSpace(model)
		if code == "" || model == "" {
			continue
		}
		result[code] = model
	}
	return result, rows.Err()
}

// ListSystemDefaultHealthCheckModels mirrors
// listProviderSystemDefaultHealthCheckModelsAsync.
func (s *Store) ListSystemDefaultHealthCheckModels(ctx context.Context, providerCodes []string) (map[string]string, error) {
	ctx = ensureCtx(ctx)
	result := map[string]string{}
	codes := normalizeProviderCodeList(providerCodes)
	where := ""
	args := []any{}
	if len(codes) > 0 {
		where = " WHERE provider_code IN (" + placeholders(len(codes)) + ")"
		for _, code := range codes {
			args = append(args, code)
		}
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model
		FROM `+s.table("provider_system_default_health_check_models")+where+`
		ORDER BY provider_code ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, model string
		if err := rows.Scan(&code, &model); err != nil {
			return nil, err
		}
		code = strings.TrimSpace(code)
		model = strings.TrimSpace(model)
		if code == "" || model == "" {
			continue
		}
		result[code] = model
	}
	return result, rows.Err()
}

// UpsertDefaultHealthCheckModelPreference ports
// upsertProviderDefaultHealthCheckModelPreferenceAsync (personal rows).
func (s *Store) UpsertDefaultHealthCheckModelPreference(ctx context.Context, systemAccountID, providerCode, model string) error {
	ctx = ensureCtx(ctx)
	now := s.nowUTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("provider_default_health_check_models")+`
		(system_account_id, provider_code, model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(system_account_id, provider_code) DO UPDATE SET
			model = excluded.model,
			updated_at = excluded.updated_at`),
		strings.TrimSpace(systemAccountID), strings.TrimSpace(providerCode), strings.TrimSpace(model), now, now)
	return err
}

// UpsertSystemDefaultHealthCheckModel ports
// upsertProviderSystemDefaultHealthCheckModelAsync.
func (s *Store) UpsertSystemDefaultHealthCheckModel(ctx context.Context, providerCode, model string) error {
	ctx = ensureCtx(ctx)
	now := s.nowUTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("provider_system_default_health_check_models")+`
		(provider_code, model, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(provider_code) DO UPDATE SET
			model = excluded.model,
			updated_at = excluded.updated_at`),
		strings.TrimSpace(providerCode), strings.TrimSpace(model), now, now)
	return err
}

// overlayListItemHealthCheckModels ports the /list overlay
// (listProviderListItemsForRequestAsync): personal preference wins, then the
// system default, then the profile default.
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

// overlayDefinitionHealthCheckModels ports
// providerWithDefaultHealthCheckModelPreference for ProviderDefinition rows:
// personal || system || original, plus the systemDefaultHealthCheckModel key
// (present only when a system default row exists).
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

// providerJoin mirrors the Node listProviderListItems lateral: one preferred
// default profile per provider.
func (s *Store) providerJoin() string {
	return ` LEFT JOIN ` + s.table("provider_protocol_profiles") + ` ppp ON ppp.id = (
		SELECT candidate.id FROM ` + s.table("provider_protocol_profiles") + ` candidate
		WHERE candidate.provider_code = p.code
		ORDER BY ` + preferredProfileOrder + ` LIMIT 1
	)`
}

// preferredProfileOrder mirrors the Node default-profile pick in SQL: enabled
// first, then the gemini-native / glm-coding preferred ids, then recency.
const preferredProfileOrder = `candidate.enabled DESC,
		CASE WHEN candidate.id IN (?, ?) THEN 0 ELSE 1 END,
		candidate.updated_at DESC, candidate.id ASC`

// preferredProfileArgs feeds the CASE WHEN candidate.id IN (?, ?) pair of the
// preferred-profile pick.
func preferredProfileArgs() []any {
	return []any{preferredGeminiProfileID, preferredGLMCodingProfile}
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// normalizeProviderCodeList mirrors the normalized provider code lists of the
// preference repositories (trim, drop empties, dedupe).
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
