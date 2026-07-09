package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	geminiNativeProfileID = "profile_gemini_native_v1beta"
	glmCodingProfileID    = "profile_glm_coding_openai_v1"
)

type managementProviderRow struct {
	ID                         string
	Code                       string
	Name                       string
	ParentCode                 pgtype.Text
	Description                pgtype.Text
	Enabled                    bool
	DefaultSupportedModelsJson string
}

func (s *Store) ListManagementProviders(ctx context.Context, input port.ManagementProviderListInput) ([]port.ManagementProviderOption, error) {
	return listManagementProviders(ctx, s.queries(), input)
}

func (s *Store) ListManagementProviderOptions(ctx context.Context, input port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error) {
	return listManagementProviderOptions(ctx, s.queries(), input)
}

func listManagementProviders(ctx context.Context, q *postgresqueries.Queries, input port.ManagementProviderListInput) ([]port.ManagementProviderOption, error) {
	rows, err := q.ListManagementProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list management providers: %w", err)
	}
	return managementProviderOptionsFromRows(ctx, q, managementProviderRowsFromProviderRows(rows), strings.TrimSpace(input.SystemAccountID))
}

func listManagementProviderOptions(ctx context.Context, q *postgresqueries.Queries, input port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error) {
	providerRows, err := q.ListManagementProviderOptionProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list management provider options: %w", err)
	}
	return managementProviderOptionsFromRows(ctx, q, managementProviderRowsFromOptionRows(providerRows), strings.TrimSpace(input.SystemAccountID))
}

func managementProviderOptionsFromRows(ctx context.Context, q *postgresqueries.Queries, providerRows []managementProviderRow, systemAccountID string) ([]port.ManagementProviderOption, error) {
	providerCodes := make([]string, 0, len(providerRows))
	for _, row := range providerRows {
		providerCodes = append(providerCodes, row.Code)
	}
	if len(providerCodes) == 0 {
		return []port.ManagementProviderOption{}, nil
	}

	profileRows, err := q.ListManagementProviderOptionProfiles(ctx, providerCodes)
	if err != nil {
		return nil, fmt.Errorf("list management provider option profiles: %w", err)
	}
	profileIDs := make([]string, 0, len(profileRows))
	for _, row := range profileRows {
		profileIDs = append(profileIDs, row.ID)
	}

	familiesByProfile := map[string][]port.ManagementProviderEndpointFamily{}
	if len(profileIDs) > 0 {
		familyRows, err := q.ListManagementProviderOptionEndpointFamilies(ctx, profileIDs)
		if err != nil {
			return nil, fmt.Errorf("list management provider option endpoint families: %w", err)
		}
		for _, row := range familyRows {
			familiesByProfile[row.ProfileID] = append(familiesByProfile[row.ProfileID], port.ManagementProviderEndpointFamily{
				Code:        row.FamilyCode,
				Name:        row.Name,
				Description: providerTextValue(row.Description),
			})
		}
	}

	preferences := map[string]string{}
	if systemAccountID != "" {
		preferenceRows, err := q.ListManagementProviderDefaultTestModelPreferences(ctx, postgresqueries.ListManagementProviderDefaultTestModelPreferencesParams{
			SystemAccountID: systemAccountID,
			ProviderCodes:   providerCodes,
		})
		if err != nil {
			return nil, fmt.Errorf("list management provider default test model preferences: %w", err)
		}
		for _, row := range preferenceRows {
			preferences[row.ProviderCode] = row.Model
		}
	}

	profilesByProvider, err := managementProviderProfilesByProvider(profileRows, familiesByProfile)
	if err != nil {
		return nil, err
	}
	options := make([]port.ManagementProviderOption, 0, len(providerRows))
	for _, row := range providerRows {
		option, err := managementProviderOptionFromRow(row, profilesByProvider[row.Code], preferences[row.Code])
		if err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, nil
}

func managementProviderRowsFromProviderRows(rows []postgresqueries.ListManagementProvidersRow) []managementProviderRow {
	items := make([]managementProviderRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementProviderRow{
			ID:                         row.ID,
			Code:                       row.Code,
			Name:                       row.Name,
			ParentCode:                 row.ParentCode,
			Description:                row.Description,
			Enabled:                    row.Enabled,
			DefaultSupportedModelsJson: row.DefaultSupportedModelsJson,
		})
	}
	return items
}

func managementProviderRowsFromOptionRows(rows []postgresqueries.ListManagementProviderOptionProvidersRow) []managementProviderRow {
	items := make([]managementProviderRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementProviderRow{
			ID:                         row.ID,
			Code:                       row.Code,
			Name:                       row.Name,
			ParentCode:                 row.ParentCode,
			Description:                row.Description,
			Enabled:                    row.Enabled,
			DefaultSupportedModelsJson: row.DefaultSupportedModelsJson,
		})
	}
	return items
}

func managementProviderProfilesByProvider(
	rows []postgresqueries.ListManagementProviderOptionProfilesRow,
	familiesByProfile map[string][]port.ManagementProviderEndpointFamily,
) (map[string][]port.ManagementProviderProtocolProfile, error) {
	result := map[string][]port.ManagementProviderProtocolProfile{}
	for _, row := range rows {
		accountTypes, err := decodeProviderStringArray(row.AccountTypesJson, "provider profile account_types_json")
		if err != nil {
			return nil, err
		}
		capabilities, err := decodeProviderStringArray(row.CapabilitiesJson, "provider profile capabilities_json")
		if err != nil {
			return nil, err
		}
		profile := port.ManagementProviderProtocolProfile{
			ID:               row.ID,
			ProviderCode:     row.ProviderCode,
			Name:             row.Name,
			Description:      providerTextValue(row.Description),
			Enabled:          row.Enabled,
			ProtocolCode:     row.ProtocolCode,
			ProtocolVersion:  row.ProtocolVersion,
			BaseURL:          row.BaseUrl,
			DefaultTestModel: row.DefaultTestModel,
			AccountTypes:     accountTypes,
			Capabilities:     capabilities,
			EndpointFamilies: append([]port.ManagementProviderEndpointFamily(nil), familiesByProfile[row.ID]...),
		}
		result[row.ProviderCode] = append(result[row.ProviderCode], profile)
	}
	return result, nil
}

func managementProviderOptionFromRow(
	row managementProviderRow,
	profiles []port.ManagementProviderProtocolProfile,
	preferredModel string,
) (port.ManagementProviderOption, error) {
	defaultSupportedModels, err := decodeProviderStringArray(row.DefaultSupportedModelsJson, "provider default_supported_models_json")
	if err != nil {
		return port.ManagementProviderOption{}, err
	}
	profiles = append([]port.ManagementProviderProtocolProfile(nil), profiles...)
	defaultProfile := preferredManagementProviderDefaultProfile(profiles)
	if preferredModel != "" && defaultProfile != nil {
		for index := range profiles {
			if profiles[index].ID == defaultProfile.ID {
				profiles[index].DefaultTestModel = preferredModel
				defaultProfile = &profiles[index]
				break
			}
		}
	}
	option := port.ManagementProviderOption{
		ID:                       row.ID,
		Code:                     row.Code,
		Name:                     row.Name,
		ParentCode:               providerTextValue(row.ParentCode),
		Description:              providerTextValue(row.Description),
		Enabled:                  row.Enabled,
		DefaultSupportedModels:   defaultSupportedModels,
		ProtocolProfiles:         profiles,
		DefaultProtocolProfileID: "",
		ProtocolCode:             "",
		ProtocolVersion:          "",
		BaseURL:                  "",
		DefaultTestModel:         strings.TrimSpace(preferredModel),
		AccountTypes:             []string{},
		Capabilities:             []string{},
	}
	if defaultProfile != nil {
		option.DefaultProtocolProfileID = defaultProfile.ID
		option.ProtocolCode = defaultProfile.ProtocolCode
		option.ProtocolVersion = defaultProfile.ProtocolVersion
		option.BaseURL = defaultProfile.BaseURL
		if option.DefaultTestModel == "" {
			option.DefaultTestModel = defaultProfile.DefaultTestModel
		}
		option.AccountTypes = append([]string(nil), defaultProfile.AccountTypes...)
		option.Capabilities = append([]string(nil), defaultProfile.Capabilities...)
	}
	return option, nil
}

func preferredManagementProviderDefaultProfile(profiles []port.ManagementProviderProtocolProfile) *port.ManagementProviderProtocolProfile {
	if len(profiles) == 0 {
		return nil
	}
	candidates := make([]port.ManagementProviderProtocolProfile, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Enabled {
			candidates = append(candidates, profile)
		}
	}
	if len(candidates) == 0 {
		candidates = profiles
	}
	for index := range candidates {
		if candidates[index].ProviderCode == "gemini" && candidates[index].ID == geminiNativeProfileID {
			return &candidates[index]
		}
	}
	for index := range candidates {
		if candidates[index].ProviderCode == "glm" && candidates[index].ID == glmCodingProfileID {
			return &candidates[index]
		}
	}
	return &candidates[0]
}

func decodeProviderStringArray(raw string, label string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
	}
	return output, nil
}

func providerTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

var _ port.ManagementProviderReader = (*Store)(nil)
var _ port.ManagementProviderOptionReader = (*Store)(nil)
