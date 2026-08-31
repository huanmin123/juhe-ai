package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// configuredModelResolution is the immutable model/family decision that must
// be copied into a Runtime Target before a probe is issued.  Keeping the
// endpoint families next to the model prevents a later caller from treating a
// cross-family mapping as a pure model rename.
type configuredModelResolution struct {
	UpstreamModel          string
	SourceEndpointFamily   modelcheckprofile.EndpointFamily
	UpstreamEndpointFamily modelcheckprofile.EndpointFamily
}

// resolveConfiguredUpstreamModelMapping is the strict J3b mapping resolver.
// It intentionally reads upstream_endpoint_family and fails closed for every
// conversion that the Node model-mapping oracle does not prove for probes.
// All model-check owner callers consume this resolution atomically.
func resolveConfiguredUpstreamModelMapping(ctx context.Context, db *sql.DB, postgres bool, accountID string, profile modelcheckprofile.ProtocolProfile, model string) (configuredModelResolution, error) {
	if db == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(model) == "" || !profileSupportsModel(profile, model) {
		return configuredModelResolution{}, nil
	}
	table := func(name string) string {
		if postgres {
			return "juhe_business." + name
		}
		return name
	}
	modelsQuery := "SELECT model FROM " + table("account_supported_models") + " WHERE account_id=?"
	if postgres {
		modelsQuery = "SELECT model FROM " + table("account_supported_models") + " WHERE account_id=$1"
	}
	rows, err := db.QueryContext(ctx, modelsQuery, accountID)
	if err != nil {
		return configuredModelResolution{}, fmt.Errorf("read J3b account supported models: %w", err)
	}
	defer rows.Close()
	supported := make(map[string]struct{})
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return configuredModelResolution{}, fmt.Errorf("scan J3b account supported model: %w", err)
		}
		if value = strings.TrimSpace(value); value != "" {
			supported[value] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return configuredModelResolution{}, fmt.Errorf("iterate J3b account supported models: %w", err)
	}
	sourceFamilies := modelcheckprofile.SourceEndpointFamilies(profile)
	defaultResolution := configuredModelResolution{UpstreamModel: model}
	if len(sourceFamilies) > 0 {
		defaultResolution.SourceEndpointFamily = sourceFamilies[0]
		defaultResolution.UpstreamEndpointFamily = sourceFamilies[0]
	}
	mappingQuery := "SELECT upstream_model,upstream_endpoint_family FROM " + table("account_model_mappings") + " WHERE account_id=? AND source_model=? AND source_endpoint_family=? AND enabled=1"
	if postgres {
		mappingQuery = "SELECT upstream_model,upstream_endpoint_family FROM " + table("account_model_mappings") + " WHERE account_id=$1 AND source_model=$2 AND source_endpoint_family=$3 AND enabled=TRUE"
	}
	for _, family := range sourceFamilies {
		var upstream, upstreamFamily string
		err := db.QueryRowContext(ctx, mappingQuery, accountID, model, string(family)).Scan(&upstream, &upstreamFamily)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return configuredModelResolution{}, fmt.Errorf("read J3b account model mapping: %w", err)
		}
		upstream = strings.TrimSpace(upstream)
		upstreamFamily = strings.TrimSpace(upstreamFamily)
		if upstream == "" || upstreamFamily == "" {
			return configuredModelResolution{}, errors.New("J3b account model mapping endpoint family is missing")
		}
		resolvedFamily := modelcheckprofile.EndpointFamily(upstreamFamily)
		if !j3bModelMappingConversionSupported(profile, family, resolvedFamily) {
			return configuredModelResolution{}, fmt.Errorf("J3b account model mapping from endpoint family %q to %q is unsupported", family, resolvedFamily)
		}
		if len(supported) > 0 {
			if _, ok := supported[upstream]; !ok {
				return configuredModelResolution{}, nil
			}
		} else if !profileSupportsModel(profile, upstream) {
			return configuredModelResolution{}, nil
		}
		return configuredModelResolution{UpstreamModel: upstream, SourceEndpointFamily: family, UpstreamEndpointFamily: resolvedFamily}, nil
	}
	_, modelIsSupported := supported[model]
	if len(supported) == 0 || modelIsSupported {
		return defaultResolution, nil
	}
	return configuredModelResolution{}, nil
}

func j3bModelMappingConversionSupported(profile modelcheckprofile.ProtocolProfile, source, upstream modelcheckprofile.EndpointFamily) bool {
	if source == upstream {
		return true
	}
	return profile.Protocol == modelcheckprofile.ProtocolOpenAIResponses && source == modelcheckprofile.EndpointResponses && upstream == modelcheckprofile.EndpointChatCompletions
}

// resolveConfiguredUpstreamModel is retained as a compatibility shim for
// package-local callers; the strict resolver remains the single source of
// mapping semantics and this wrapper cannot bypass family validation.
func resolveConfiguredUpstreamModel(ctx context.Context, db *sql.DB, postgres bool, accountID string, profile modelcheckprofile.ProtocolProfile, model string) (string, error) {
	resolution, err := resolveConfiguredUpstreamModelMapping(ctx, db, postgres, accountID, profile, model)
	if err != nil {
		return "", err
	}
	return resolution.UpstreamModel, nil
}

func profileSupportsModel(profile modelcheckprofile.ProtocolProfile, model string) bool {
	for _, candidate := range profile.Models {
		if candidate == model {
			return true
		}
	}
	return false
}
