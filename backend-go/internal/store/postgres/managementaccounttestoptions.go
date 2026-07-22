package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAccountTestOptionsQueries interface {
	GetManagementAccountTestOptionsSource(
		ctx context.Context,
		arg postgresqueries.GetManagementAccountTestOptionsSourceParams,
	) (postgresqueries.GetManagementAccountTestOptionsSourceRow, error)
	ListManagementAccountTestOptionModelMappings(
		ctx context.Context,
		accountID string,
	) ([]postgresqueries.ListManagementAccountTestOptionModelMappingsRow, error)
}

type managementAccountTestOptionQueries interface {
	GetManagementAccountTestOptionListSource(
		ctx context.Context,
		arg postgresqueries.GetManagementAccountTestOptionListSourceParams,
	) (postgresqueries.GetManagementAccountTestOptionListSourceRow, error)
	GetManagementAccountTestModelCapabilitiesSource(
		ctx context.Context,
		arg postgresqueries.GetManagementAccountTestModelCapabilitiesSourceParams,
	) (postgresqueries.GetManagementAccountTestModelCapabilitiesSourceRow, error)
	ListManagementAccountTestModelCatalog(
		ctx context.Context,
		arg postgresqueries.ListManagementAccountTestModelCatalogParams,
	) ([]postgresqueries.ListManagementAccountTestModelCatalogRow, error)
	ListManagementAccountTestOptionModelMappingsBySourceModel(
		ctx context.Context,
		arg postgresqueries.ListManagementAccountTestOptionModelMappingsBySourceModelParams,
	) ([]postgresqueries.ListManagementAccountTestOptionModelMappingsBySourceModelRow, error)
}

func (s *Store) GetManagementAccountTestOptionsSource(
	ctx context.Context,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	return getManagementAccountTestOptionsSource(ctx, s.queries(), input)
}

func (s *Store) GetManagementAccountTestOptionListSource(
	ctx context.Context,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionListSource, bool, error) {
	return getManagementAccountTestOptionListSource(ctx, s.queries(), input)
}

func (s *Store) ListManagementAccountTestModelCatalog(
	ctx context.Context,
	input port.ManagementAccountTestModelCatalogInput,
) ([]port.ManagementAccountTestModelCatalogItem, error) {
	return listManagementAccountTestModelCatalog(ctx, s.queries(), input)
}

func (s *Store) GetManagementAccountTestModelCapabilitiesSource(
	ctx context.Context,
	input port.ManagementAccountTestModelCapabilitiesSourceInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	return getManagementAccountTestModelCapabilitiesSource(ctx, s.queries(), input)
}

func getManagementAccountTestOptionListSource(
	ctx context.Context,
	q managementAccountTestOptionQueries,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionListSource, bool, error) {
	row, err := q.GetManagementAccountTestOptionListSource(ctx, postgresqueries.GetManagementAccountTestOptionListSourceParams{
		AccountID:       trimManagementAccountTestText(input.AccountID),
		SystemAccountID: trimManagementAccountTestText(input.SystemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestOptionListSource{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestOptionListSource{}, false, fmt.Errorf(
			"get management account test option list source: %w",
			err,
		)
	}
	return port.ManagementAccountTestOptionListSource{
		ID:                        row.ID,
		OwnerSystemAccountID:      row.OwnerSystemAccountID,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		ClientCompatibility:       row.ClientCompatibility,
		HealthCheckModel:          row.HealthCheckModel,
	}, true, nil
}

func listManagementAccountTestModelCatalog(
	ctx context.Context,
	q managementAccountTestOptionQueries,
	input port.ManagementAccountTestModelCatalogInput,
) ([]port.ManagementAccountTestModelCatalogItem, error) {
	rows, err := q.ListManagementAccountTestModelCatalog(ctx, postgresqueries.ListManagementAccountTestModelCatalogParams{
		SelectedIds:          append([]string(nil), input.SelectedIDs...),
		ResultLimit:          int32(input.Limit),
		ProviderCode:         trimManagementAccountTestText(input.ProviderCode),
		ModelIds:             append([]string(nil), input.ModelIDs...),
		Keyword:              trimManagementAccountTestText(input.Keyword),
		OwnerSystemAccountID: trimManagementAccountTestText(input.SystemAccountID),
	})
	if err != nil {
		return nil, fmt.Errorf("list management account test model catalog: %w", err)
	}
	items := make([]port.ManagementAccountTestModelCatalogItem, 0, len(rows))
	for _, row := range rows {
		var protocols []string
		if err := json.Unmarshal([]byte(row.SupportedApiProtocolsJson), &protocols); err != nil {
			return nil, fmt.Errorf("decode management account test model protocols: %w", err)
		}
		items = append(items, port.ManagementAccountTestModelCatalogItem{
			ID:                    row.ID,
			ProviderCode:          row.ProviderCode,
			Model:                 row.Model,
			Scope:                 row.Scope,
			Mode:                  row.Mode,
			SupportedAPIProtocols: protocols,
		})
	}
	return items, nil
}

func getManagementAccountTestModelCapabilitiesSource(
	ctx context.Context,
	q managementAccountTestOptionQueries,
	input port.ManagementAccountTestModelCapabilitiesSourceInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	row, err := q.GetManagementAccountTestModelCapabilitiesSource(ctx, postgresqueries.GetManagementAccountTestModelCapabilitiesSourceParams{
		AccountID:       trimManagementAccountTestText(input.AccountID),
		SystemAccountID: trimManagementAccountTestText(input.SystemAccountID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestOptionsSource{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestOptionsSource{}, false, fmt.Errorf(
			"get management account test model capabilities source: %w",
			err,
		)
	}
	mappingRows, err := q.ListManagementAccountTestOptionModelMappingsBySourceModel(
		ctx,
		postgresqueries.ListManagementAccountTestOptionModelMappingsBySourceModelParams{
			AccountID:   row.ModelMappingAccountID,
			SourceModel: trimManagementAccountTestText(input.Model),
		},
	)
	if err != nil {
		return port.ManagementAccountTestOptionsSource{}, false, fmt.Errorf(
			"list management account test option model mappings by source model: %w",
			err,
		)
	}
	modelMappings := make([]port.ManagementAccountTestModelMapping, 0, len(mappingRows))
	for _, mappingRow := range mappingRows {
		modelMappings = append(modelMappings, port.ManagementAccountTestModelMapping{
			SourceModel:            mappingRow.SourceModel,
			SourceEndpointFamily:   mappingRow.SourceEndpointFamily,
			UpstreamModel:          mappingRow.UpstreamModel,
			UpstreamEndpointFamily: mappingRow.UpstreamEndpointFamily,
			Enabled:                mappingRow.Enabled,
		})
	}
	return port.ManagementAccountTestOptionsSource{
		ID:                        row.ID,
		OwnerSystemAccountID:      row.OwnerSystemAccountID,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		ClientCompatibility:       row.ClientCompatibility,
		HealthCheckModel:          row.HealthCheckModel,
		HealthCheckEndpointMode:   row.HealthCheckEndpointMode,
		CredentialsEncrypted:      row.CredentialsEncrypted,
		ModelMappings:             modelMappings,
	}, true, nil
}

func getManagementAccountTestOptionsSource(
	ctx context.Context,
	q managementAccountTestOptionsQueries,
	input port.ManagementAccountTestOptionsInput,
) (port.ManagementAccountTestOptionsSource, bool, error) {
	row, err := q.GetManagementAccountTestOptionsSource(
		ctx,
		postgresqueries.GetManagementAccountTestOptionsSourceParams{
			AccountID:       strings.TrimSpace(input.AccountID),
			SystemAccountID: strings.TrimSpace(input.SystemAccountID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestOptionsSource{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestOptionsSource{}, false, fmt.Errorf(
			"get management account test options source: %w",
			err,
		)
	}
	mappingRows, err := q.ListManagementAccountTestOptionModelMappings(ctx, row.ModelMappingAccountID)
	if err != nil {
		return port.ManagementAccountTestOptionsSource{}, false, fmt.Errorf(
			"list management account test option model mappings: %w",
			err,
		)
	}
	modelMappings := make([]port.ManagementAccountTestModelMapping, 0, len(mappingRows))
	for _, mappingRow := range mappingRows {
		modelMappings = append(modelMappings, port.ManagementAccountTestModelMapping{
			SourceModel:            mappingRow.SourceModel,
			SourceEndpointFamily:   mappingRow.SourceEndpointFamily,
			UpstreamModel:          mappingRow.UpstreamModel,
			UpstreamEndpointFamily: mappingRow.UpstreamEndpointFamily,
			Enabled:                mappingRow.Enabled,
		})
	}

	return port.ManagementAccountTestOptionsSource{
		ID:                        row.ID,
		OwnerSystemAccountID:      row.OwnerSystemAccountID,
		ProviderCode:              row.ProviderCode,
		ProviderProtocolProfileID: row.ProviderProtocolProfileID,
		ProtocolCode:              row.ProtocolCode,
		ProtocolVersion:           row.ProtocolVersion,
		Type:                      row.Type,
		ClientCompatibility:       row.ClientCompatibility,
		HealthCheckModel:          row.HealthCheckModel,
		HealthCheckEndpointMode:   row.HealthCheckEndpointMode,
		CredentialsEncrypted:      row.CredentialsEncrypted,
		ModelMappings:             modelMappings,
	}, true, nil
}

var _ port.ManagementAccountTestOptionsReader = (*Store)(nil)
var _ port.ManagementAccountTestOptionReader = (*Store)(nil)

func trimManagementAccountTestText(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028', '\u2029':
			return true
		default:
			return false
		}
	})
}
