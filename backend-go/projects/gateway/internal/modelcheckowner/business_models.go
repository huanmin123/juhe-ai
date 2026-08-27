package modelcheckowner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// resolveConfiguredUpstreamModel preserves the Business account's effective
// model set and returns the actual model that must be sent upstream. An empty
// account_supported_models set means the profile catalog applies; a non-empty
// set restricts the catalog, except for an enabled source model mapping whose
// upstream model is explicitly supported.
func resolveConfiguredUpstreamModel(ctx context.Context, db *sql.DB, postgres bool, accountID string, profile modelcheckprofile.ProtocolProfile, model string) (string, error) {
	if db == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(model) == "" || !profileSupportsModel(profile, model) {
		return "", nil
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
		return "", fmt.Errorf("read J3b account supported models: %w", err)
	}
	defer rows.Close()
	supported := make(map[string]struct{})
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return "", fmt.Errorf("scan J3b account supported model: %w", err)
		}
		if value = strings.TrimSpace(value); value != "" {
			supported[value] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate J3b account supported models: %w", err)
	}
	if len(supported) == 0 {
		return model, nil
	}
	if _, ok := supported[model]; ok {
		return model, nil
	}
	mappingQuery := "SELECT upstream_model FROM " + table("account_model_mappings") + " WHERE account_id=? AND source_model=? AND source_endpoint_family=? AND enabled=1"
	if postgres {
		mappingQuery = "SELECT upstream_model FROM " + table("account_model_mappings") + " WHERE account_id=$1 AND source_model=$2 AND source_endpoint_family=$3 AND enabled=TRUE"
	}
	for _, family := range modelcheckprofile.SourceEndpointFamilies(profile) {
		var upstream string
		err := db.QueryRowContext(ctx, mappingQuery, accountID, model, string(family)).Scan(&upstream)
		if err == nil {
			upstream = strings.TrimSpace(upstream)
			if _, ok := supported[upstream]; ok {
				return upstream, nil
			}
			continue
		}
		if err != sql.ErrNoRows {
			return "", fmt.Errorf("read J3b account model mapping: %w", err)
		}
	}
	return "", nil
}

func profileSupportsModel(profile modelcheckprofile.ProtocolProfile, model string) bool {
	for _, candidate := range profile.Models {
		if candidate == model {
			return true
		}
	}
	return false
}
