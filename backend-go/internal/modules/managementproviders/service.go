package managementproviders

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

type Service struct {
	store port.ManagementProviderReader
}

type ListInput struct {
	SystemAccountID string
}

type OptionListInput struct {
	SystemAccountID string
}

type EndpointFamily struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ProtocolProfile struct {
	ID                      string           `json:"id"`
	ProviderCode            string           `json:"providerCode"`
	Name                    string           `json:"name"`
	Description             string           `json:"description,omitempty"`
	Enabled                 bool             `json:"enabled"`
	ProtocolCode            string           `json:"protocolCode"`
	ProtocolVersion         string           `json:"protocolVersion"`
	BaseURL                 string           `json:"baseUrl"`
	DefaultHealthCheckModel string           `json:"defaultHealthCheckModel"`
	AccountTypes            []string         `json:"accountTypes"`
	Capabilities            []string         `json:"capabilities"`
	EndpointFamilies        []EndpointFamily `json:"endpointFamilies"`
}

type Option struct {
	ID                            string            `json:"id"`
	Code                          string            `json:"code"`
	Name                          string            `json:"name"`
	ParentCode                    string            `json:"parentCode,omitempty"`
	Description                   string            `json:"description,omitempty"`
	Enabled                       bool              `json:"enabled"`
	DefaultProtocolProfileID      string            `json:"defaultProtocolProfileId"`
	ProtocolCode                  string            `json:"protocolCode"`
	ProtocolVersion               string            `json:"protocolVersion"`
	BaseURL                       string            `json:"baseUrl"`
	DefaultHealthCheckModel       string            `json:"defaultHealthCheckModel"`
	SystemDefaultHealthCheckModel string            `json:"systemDefaultHealthCheckModel"`
	DefaultSupportedModels        []string          `json:"defaultSupportedModels"`
	AccountTypes                  []string          `json:"accountTypes"`
	Capabilities                  []string          `json:"capabilities"`
	ProtocolProfiles              []ProtocolProfile `json:"protocolProfiles"`
}

func NewService(store port.ManagementProviderReader) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, input ListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management provider store is required")
	}
	rows, err := s.store.ListManagementProviders(ctx, port.ManagementProviderListInput{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil {
		return nil, err
	}
	return providerOptionsFromPort(rows), nil
}

func (s *Service) Options(ctx context.Context, input OptionListInput) ([]Option, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management provider option store is required")
	}
	rows, err := s.store.ListManagementProviderOptions(ctx, port.ManagementProviderOptionListInput{
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil {
		return nil, err
	}
	return providerOptionsFromPort(rows), nil
}

func providerOptionsFromPort(rows []port.ManagementProviderOption) []Option {
	items := make([]Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, providerOptionFromPort(row))
	}
	return items
}

func providerOptionFromPort(row port.ManagementProviderOption) Option {
	return Option{
		ID:                            row.ID,
		Code:                          row.Code,
		Name:                          row.Name,
		ParentCode:                    row.ParentCode,
		Description:                   row.Description,
		Enabled:                       row.Enabled,
		DefaultProtocolProfileID:      row.DefaultProtocolProfileID,
		ProtocolCode:                  row.ProtocolCode,
		ProtocolVersion:               row.ProtocolVersion,
		BaseURL:                       row.BaseURL,
		DefaultHealthCheckModel:       row.DefaultHealthCheckModel,
		SystemDefaultHealthCheckModel: row.SystemDefaultHealthCheckModel,
		DefaultSupportedModels:        append([]string(nil), row.DefaultSupportedModels...),
		AccountTypes:                  append([]string(nil), row.AccountTypes...),
		Capabilities:                  append([]string(nil), row.Capabilities...),
		ProtocolProfiles:              providerProfilesFromPort(row.ProtocolProfiles),
	}
}

func providerProfilesFromPort(rows []port.ManagementProviderProtocolProfile) []ProtocolProfile {
	items := make([]ProtocolProfile, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProtocolProfile{
			ID:                      row.ID,
			ProviderCode:            row.ProviderCode,
			Name:                    row.Name,
			Description:             row.Description,
			Enabled:                 row.Enabled,
			ProtocolCode:            row.ProtocolCode,
			ProtocolVersion:         row.ProtocolVersion,
			BaseURL:                 row.BaseURL,
			DefaultHealthCheckModel: row.DefaultHealthCheckModel,
			AccountTypes:            append([]string(nil), row.AccountTypes...),
			Capabilities:            append([]string(nil), row.Capabilities...),
			EndpointFamilies:        endpointFamiliesFromPort(row.EndpointFamilies),
		})
	}
	return items
}

func endpointFamiliesFromPort(rows []port.ManagementProviderEndpointFamily) []EndpointFamily {
	items := make([]EndpointFamily, 0, len(rows))
	for _, row := range rows {
		items = append(items, EndpointFamily{
			Code:        row.Code,
			Name:        row.Name,
			Description: row.Description,
		})
	}
	return items
}
