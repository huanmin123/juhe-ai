// default_cleanup.go ports
// backend/src/storage/provider-model-default-reference-cleanup.repository.ts:
// when a provider model stops being usable as a default health-check model
// (patch transition or delete), the stale personal preferences and system
// defaults that point at it are removed inside the caller's transaction,
// unless another still-usable catalog row (built-in or custom) serves the
// same model for the target provider.
package providers

import (
	"context"
	"database/sql"
	"strings"
)

// defaultReferenceCleanupTarget mirrors ProviderModelDefaultReferenceCleanupTarget.
type defaultReferenceCleanupTarget struct {
	ProviderCode               string
	BuiltInSourceProviderCodes []string
	CustomSourceProviderCodes  []string
}

// defaultReferenceCleanupInput mirrors ProviderModelDefaultReferenceCleanupInput.
type defaultReferenceCleanupInput struct {
	Model              string
	Targets            []defaultReferenceCleanupTarget
	SystemAccountID    string
	ClearSystemDefault bool
}

// clearUnavailableProviderModelDefaultReferences ports
// clearUnavailableProviderModelDefaultReferencesInTransaction and returns the
// provider codes whose preference rows changed (target order, deduped).
func clearUnavailableProviderModelDefaultReferences(ctx context.Context, tx *sql.Tx, s *Store, input *defaultReferenceCleanupInput) ([]string, error) {
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return []string{}, nil
	}
	cleared := map[string]bool{}
	for _, target := range normalizeCleanupTargets(input.Targets) {
		personalChanges, err := clearPersonalDefaultReferences(ctx, tx, s, model, target, input.SystemAccountID)
		if err != nil {
			return nil, err
		}
		systemChanges := int64(0)
		if input.ClearSystemDefault {
			systemChanges, err = clearSystemDefaultReference(ctx, tx, s, model, target)
			if err != nil {
				return nil, err
			}
		}
		if personalChanges+systemChanges > 0 {
			cleared[target.ProviderCode] = true
		}
	}
	codes := make([]string, 0, len(cleared))
	for _, target := range normalizeCleanupTargets(input.Targets) {
		if cleared[target.ProviderCode] {
			codes = append(codes, target.ProviderCode)
		}
	}
	return codes, nil
}

func clearPersonalDefaultReferences(ctx context.Context, tx *sql.Tx, s *Store, model string, target defaultReferenceCleanupTarget, systemAccountID string) (int64, error) {
	owner := strings.TrimSpace(systemAccountID)
	ownerPredicate := ""
	args := []any{target.ProviderCode, model}
	if owner != "" {
		ownerPredicate = "AND preference.system_account_id = ?"
		args = append(args, owner)
	}
	availability := availableModelExistsSQL(s, target.BuiltInSourceProviderCodes, target.CustomSourceProviderCodes, "preference.system_account_id")
	args = append(args, availability.params(model)...)
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("provider_default_health_check_models")+` AS preference
		WHERE preference.provider_code = ?
			AND preference.model = ?
			`+ownerPredicate+`
			AND NOT (`+availability.sql+`)`), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func clearSystemDefaultReference(ctx context.Context, tx *sql.Tx, s *Store, model string, target defaultReferenceCleanupTarget) (int64, error) {
	availability := availableModelExistsSQL(s, target.BuiltInSourceProviderCodes, target.CustomSourceProviderCodes, "")
	args := append([]any{target.ProviderCode, model}, availability.params(model)...)
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("provider_system_default_health_check_models")+` AS system_default
		WHERE system_default.provider_code = ?
			AND system_default.model = ?
			AND NOT (`+availability.sql+`)`), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type availabilityExists struct {
	sql    string
	params func(model string) []any
}

// availableModelExistsSQL ports availableModelExistsSql: an OR of EXISTS
// clauses over the built-in and custom source tables proving the model is
// still served by an active, non-expired, text-capable row.
func availableModelExistsSQL(s *Store, builtInSourceProviderCodes, customSourceProviderCodes []string, personalOwnerExpression string) availabilityExists {
	builtInCodes := normalizeProviderCodeList(builtInSourceProviderCodes)
	customCodes := normalizeProviderCodeList(customSourceProviderCodes)
	noop := availabilityExists{sql: "0 = 1", params: func(string) []any { return nil }}
	if len(builtInCodes) == 0 && len(customCodes) == 0 {
		return noop
	}
	trimFn := "trim"
	if s.pg {
		trimFn = "btrim"
	}
	visiblePredicate := "built_in.catalog_visible = 1"
	if s.pg {
		visiblePredicate = "built_in.catalog_visible = TRUE"
	}
	customScopePredicate := "custom.scope = 'global' AND custom.system_account_id IS NULL"
	if personalOwnerExpression != "" {
		customScopePredicate = `(custom.scope = 'global' AND custom.system_account_id IS NULL)
			OR (custom.scope = 'personal' AND custom.system_account_id = ` + personalOwnerExpression + `)`
	}
	clauses := []string{}
	params := func(model string) []any {
		values := []any{}
		if len(builtInCodes) > 0 {
			values = append(values, stringSliceToAny(builtInCodes)...)
			values = append(values, model)
		}
		if len(customCodes) > 0 {
			values = append(values, stringSliceToAny(customCodes)...)
			values = append(values, model)
		}
		return values
	}
	if len(builtInCodes) > 0 {
		clauses = append(clauses, `EXISTS (
			SELECT 1
			FROM `+s.table("provider_model_catalog")+` AS built_in
			WHERE built_in.provider_code IN (`+placeholders(len(builtInCodes))+`)
				AND built_in.model = ?
				AND built_in.status = 'active'
				AND `+visiblePredicate+`
				AND (built_in.shutdown_date IS NULL OR `+trimFn+`(built_in.shutdown_date) = '' OR built_in.shutdown_date > `+s.todayText()+`)
				AND (built_in.mode IS NULL OR lower(`+trimFn+`(built_in.mode)) NOT IN ('image', 'audio'))
				AND `+textProtocolPredicate(s, "built_in.supported_api_protocols_json")+`
		)`)
	}
	if len(customCodes) > 0 {
		clauses = append(clauses, `EXISTS (
			SELECT 1
			FROM `+s.table("custom_provider_models")+` AS custom
			WHERE custom.provider_code IN (`+placeholders(len(customCodes))+`)
				AND custom.model = ?
				AND custom.status = 'active'
				AND (custom.shutdown_date IS NULL OR `+trimFn+`(custom.shutdown_date) = '' OR custom.shutdown_date > `+s.todayText()+`)
				AND (custom.mode IS NULL OR lower(`+trimFn+`(custom.mode)) NOT IN ('image', 'audio'))
				AND `+textProtocolPredicate(s, "custom.supported_api_protocols_json")+`
				AND (`+customScopePredicate+`)
		)`)
	}
	joined := clauses[0]
	for _, clause := range clauses[1:] {
		joined += " OR " + clause
	}
	return availabilityExists{sql: "(" + joined + ")", params: params}
}

// textProtocolPredicate ports textProtocolPredicate: empty protocol arrays
// count as text-capable, otherwise one of the five text protocols must be
// present.
func textProtocolPredicate(s *Store, column string) string {
	if s.pg {
		return `(jsonb_array_length(COALESCE(` + column + `::jsonb, '[]'::jsonb)) = 0
			OR COALESCE(` + column + `::jsonb, '[]'::jsonb) ?| ARRAY['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content'])`
	}
	return `(json_array_length(COALESCE(` + column + `, '[]')) = 0
		OR EXISTS (
			SELECT 1
			FROM json_each(COALESCE(` + column + `, '[]')) AS protocol
			WHERE protocol.value IN ('chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content')
		))`
}

// normalizeCleanupTargets mirrors normalizeTargets: dedupe per provider code
// with merged source-code sets, preserving first-seen order.
func normalizeCleanupTargets(targets []defaultReferenceCleanupTarget) []defaultReferenceCleanupTarget {
	type merged struct {
		builtIn map[string]bool
		custom  map[string]bool
	}
	ordered := []string{}
	byCode := map[string]*merged{}
	for _, target := range targets {
		providerCode := strings.TrimSpace(target.ProviderCode)
		if providerCode == "" {
			continue
		}
		entry, ok := byCode[providerCode]
		if !ok {
			entry = &merged{builtIn: map[string]bool{}, custom: map[string]bool{}}
			byCode[providerCode] = entry
			ordered = append(ordered, providerCode)
		}
		for _, code := range target.BuiltInSourceProviderCodes {
			if trimmed := strings.TrimSpace(code); trimmed != "" {
				entry.builtIn[trimmed] = true
			}
		}
		for _, code := range target.CustomSourceProviderCodes {
			if trimmed := strings.TrimSpace(code); trimmed != "" {
				entry.custom[trimmed] = true
			}
		}
	}
	output := make([]defaultReferenceCleanupTarget, 0, len(ordered))
	for _, providerCode := range ordered {
		entry := byCode[providerCode]
		output = append(output, defaultReferenceCleanupTarget{
			ProviderCode:               providerCode,
			BuiltInSourceProviderCodes: mapKeysSorted(entry.builtIn),
			CustomSourceProviderCodes:  mapKeysSorted(entry.custom),
		})
	}
	return output
}

func mapKeysSorted(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for index := 1; index < len(keys); index++ {
		for position := index; position > 0 && keys[position] < keys[position-1]; position-- {
			keys[position], keys[position-1] = keys[position-1], keys[position]
		}
	}
	return keys
}
