package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const gatewayCandidateAccountFactsSQL = `
WITH requested(account_id) AS (
  SELECT unnest($1::text[])
)
SELECT
  requested.account_id,
  COALESCE((
    SELECT jsonb_agg(models.model ORDER BY models.model)
    FROM juhe_business.account_supported_models AS models
    WHERE models.account_id = requested.account_id
  ), '[]'::jsonb)::text,
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'providerCode', mappings.provider_code,
      'sourceModel', mappings.source_model,
      'sourceEndpointFamily', mappings.source_endpoint_family,
      'upstreamModel', mappings.upstream_model,
      'upstreamEndpointFamily', mappings.upstream_endpoint_family,
      'enabled', mappings.enabled
    ) ORDER BY mappings.source_model, mappings.source_endpoint_family, mappings.upstream_model)
    FROM juhe_business.account_model_mappings AS mappings
    WHERE mappings.account_id = requested.account_id
      AND mappings.enabled = true
  ), '[]'::jsonb)::text
FROM requested
ORDER BY array_position($1::text[], requested.account_id)`

const gatewayCandidateProxyFactsSQL = `
SELECT id, type, host, port, COALESCE(username, ''), COALESCE(password_encrypted, ''), enabled
FROM juhe_business.proxy_profiles
WHERE id = ANY($1::text[])
ORDER BY array_position($1::text[], id)`

const gatewayCandidateFreshQualitySQL = `
SELECT account_id, quality_score, quality_state, ewma_first_token_ms
FROM juhe_stats.account_quality_scores
WHERE account_id = ANY($1::text[])
  AND last_sample_at >= $2::text
ORDER BY array_position($1::text[], account_id)`

func (s *Store) LoadGatewayCandidateHydrationFacts(ctx context.Context, input port.GatewayCandidateHydrationInput) (port.GatewayCandidateHydrationFacts, error) {
	accountIDs := normalizedGatewayHydrationIDs(input.AccountIDs, 256)
	proxyIDs := normalizedGatewayHydrationIDs(input.ProxyIDs, 256)
	facts := port.GatewayCandidateHydrationFacts{
		Accounts: make(map[string]port.GatewayCandidateAccountFacts, len(accountIDs)),
		Proxies:  make(map[string]port.GatewayCandidateProxyFacts, len(proxyIDs)),
	}
	if len(accountIDs) > 0 {
		if err := s.loadGatewayCandidateAccountFacts(ctx, accountIDs, facts.Accounts); err != nil {
			return port.GatewayCandidateHydrationFacts{}, err
		}
	}
	if len(proxyIDs) > 0 {
		if err := s.loadGatewayCandidateProxyFacts(ctx, proxyIDs, facts.Proxies); err != nil {
			return port.GatewayCandidateHydrationFacts{}, err
		}
	}
	return facts, nil
}

func (s *Store) LoadGatewayCandidateQualityFacts(ctx context.Context, accountIDs []string, freshAfter time.Time) (map[string]port.GatewayCandidateQualityFacts, error) {
	ids := normalizedGatewayHydrationIDs(accountIDs, port.GatewayAccountCandidateScanLimit)
	result := make(map[string]port.GatewayCandidateQualityFacts, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if err := s.loadGatewayCandidateFreshQuality(ctx, ids, freshAfter, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) loadGatewayCandidateAccountFacts(ctx context.Context, ids []string, output map[string]port.GatewayCandidateAccountFacts) error {
	rows, err := s.pool.Query(ctx, gatewayCandidateAccountFactsSQL, ids)
	if err != nil {
		return fmt.Errorf("load gateway candidate account facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, modelsJSON, mappingsJSON string
		if err := rows.Scan(&accountID, &modelsJSON, &mappingsJSON); err != nil {
			return fmt.Errorf("scan gateway candidate account facts: %w", err)
		}
		var models []string
		var mappings []port.GatewayCandidateModelMapping
		if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
			return fmt.Errorf("decode gateway candidate supported models: %w", err)
		}
		if err := json.Unmarshal([]byte(mappingsJSON), &mappings); err != nil {
			return fmt.Errorf("decode gateway candidate model mappings: %w", err)
		}
		facts := output[accountID]
		facts.SupportedModels = models
		facts.ModelMappings = mappings
		output[accountID] = facts
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate gateway candidate account facts: %w", err)
	}
	return nil
}

func (s *Store) loadGatewayCandidateFreshQuality(ctx context.Context, ids []string, freshAfterTime time.Time, output map[string]port.GatewayCandidateQualityFacts) error {
	rows, err := s.pool.Query(ctx, gatewayCandidateFreshQualitySQL, ids, freshAfterTime.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if gatewayCandidateQualityUnavailable(err) {
			return nil
		}
		return fmt.Errorf("load gateway candidate fresh quality: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, qualityState string
		var score pgtype.Int8
		var ewma pgtype.Float8
		if err := rows.Scan(&accountID, &score, &qualityState, &ewma); err != nil {
			return fmt.Errorf("scan gateway candidate fresh quality: %w", err)
		}
		facts := output[accountID]
		if score.Valid {
			value := score.Int64
			facts.QualityScore = &value
		}
		facts.QualityState = qualityState
		if ewma.Valid {
			value := ewma.Float64
			facts.QualityEWMAFirstTokenMS = &value
		}
		output[accountID] = facts
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate gateway candidate fresh quality: %w", err)
	}
	return nil
}

func gatewayCandidateQualityUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703")
}

func (s *Store) loadGatewayCandidateProxyFacts(ctx context.Context, ids []string, output map[string]port.GatewayCandidateProxyFacts) error {
	rows, err := s.pool.Query(ctx, gatewayCandidateProxyFactsSQL, ids)
	if err != nil {
		return fmt.Errorf("load gateway candidate proxy facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var proxy port.GatewayCandidateProxyFacts
		if err := rows.Scan(&proxy.ID, &proxy.Type, &proxy.Host, &proxy.Port, &proxy.Username, &proxy.PasswordEncrypted, &proxy.Enabled); err != nil {
			return fmt.Errorf("scan gateway candidate proxy facts: %w", err)
		}
		output[proxy.ID] = proxy
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate gateway candidate proxy facts: %w", err)
	}
	return nil
}

func normalizedGatewayHydrationIDs(values []string, limit int) []string {
	result := make([]string, 0, min(len(values), limit))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

var _ port.GatewayCandidateHydrationReader = (*Store)(nil)
var _ port.GatewayCandidateQualityReader = (*Store)(nil)
