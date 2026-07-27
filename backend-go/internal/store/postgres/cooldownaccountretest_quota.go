package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const loadCooldownAccountRetestQuotaSubjectsSQL = `
SELECT a.id,
  CASE
    WHEN a.authorization_instance_authorization_id IS NULL
      AND a.authorization_instance_source_account_id IS NULL
      AND a.authorization_instance_owner_system_account_id IS NULL THEN 'owner'
    WHEN a.authorization_instance_authorization_id IS NOT NULL
      AND a.authorization_instance_source_account_id IS NOT NULL
      AND a.authorization_instance_owner_system_account_id IS NOT NULL THEN 'authorized'
    ELSE 'invalid'
  END AS access_type,
  COALESCE(a.authorization_instance_authorization_id, '') AS authorization_id,
  a.system_account_id,
  COALESCE(ra.effective_source_team_id, '') AS effective_source_team_id,
  CASE
    WHEN a.authorization_instance_authorization_id IS NULL
      AND a.authorization_instance_source_account_id IS NULL
      AND a.authorization_instance_owner_system_account_id IS NULL THEN true
    WHEN ra.id IS NOT NULL
      AND source_accounts.id IS NOT NULL
      AND (ra.effective_source_team_id IS NULL OR team_grant.id IS NOT NULL) THEN true
    ELSE false
  END AS authorization_valid,
  ra.limits_json,
  team_grant.limits_json
FROM juhe_business.accounts AS a
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = a.authorization_instance_source_account_id
  AND source_accounts.deleted_at IS NULL
LEFT JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = a.authorization_instance_authorization_id
  AND ra.resource_type = 'account'
  AND ra.resource_id = a.authorization_instance_source_account_id
  AND ra.resource_owner_system_account_id = source_accounts.system_account_id
  AND ra.grantee_system_account_id = a.system_account_id
  AND a.authorization_instance_owner_system_account_id = source_accounts.system_account_id
  AND ra.status = 'active'
  AND (ra.expires_at IS NULL OR ra.expires_at > $2)
LEFT JOIN LATERAL (
  SELECT grant_rows.id, grant_rows.limits_json
  FROM juhe_business.resource_authorization_grants AS grant_rows
  WHERE grant_rows.resource_type = ra.resource_type
    AND grant_rows.resource_id = ra.resource_id
    AND grant_rows.grantee_type = 'team'
    AND grant_rows.grantee_team_id = ra.effective_source_team_id
    AND grant_rows.status = 'active'
    AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > $2)
  ORDER BY grant_rows.updated_at DESC, grant_rows.id ASC
  LIMIT 1
) AS team_grant ON true
WHERE a.id = ANY($1::text[])
  AND a.deleted_at IS NULL
ORDER BY a.id ASC`

func (s *Store) LoadCooldownAccountRetestQuotaSubjects(
	ctx context.Context,
	accountIDs []string,
	now time.Time,
) ([]port.CooldownAccountRetestQuotaSubject, error) {
	ids := uniqueCooldownAccountRetestQuotaAccountIDs(accountIDs)
	if len(ids) == 0 {
		return []port.CooldownAccountRetestQuotaSubject{}, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := s.pool.Query(ctx, loadCooldownAccountRetestQuotaSubjectsSQL, ids, now)
	if err != nil {
		return nil, fmt.Errorf("query cooldown account retest quota subjects: %w", err)
	}
	defer rows.Close()

	subjects := make([]port.CooldownAccountRetestQuotaSubject, 0, len(ids))
	for rows.Next() {
		var subject port.CooldownAccountRetestQuotaSubject
		var directLimitsJSON, teamLimitsJSON pgtype.Text
		if err := rows.Scan(
			&subject.AccountID,
			&subject.AccessType,
			&subject.AuthorizationID,
			&subject.SystemAccountID,
			&subject.EffectiveSourceTeamID,
			&subject.AuthorizationValid,
			&directLimitsJSON,
			&teamLimitsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan cooldown account retest quota subject: %w", err)
		}
		directLimits, err := parseCooldownAccountRetestQuotaLimits(directLimitsJSON)
		if err != nil {
			return nil, fmt.Errorf("parse cooldown account retest direct quota for %s: %w", subject.AccountID, err)
		}
		teamLimits, err := parseCooldownAccountRetestQuotaLimits(teamLimitsJSON)
		if err != nil {
			return nil, fmt.Errorf("parse cooldown account retest team quota for %s: %w", subject.AccountID, err)
		}
		subject.DirectLimits = directLimits
		subject.TeamLimits = teamLimits
		subjects = append(subjects, subject)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cooldown account retest quota subjects: %w", err)
	}
	return subjects, nil
}

func parseCooldownAccountRetestQuotaLimits(value pgtype.Text) (port.ManagementRequestQuotaLimits, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return port.ManagementRequestQuotaLimits{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value.String))
	decoder.DisallowUnknownFields()
	var limits port.ManagementRequestQuotaLimits
	if err := decoder.Decode(&limits); err != nil {
		return port.ManagementRequestQuotaLimits{}, fmt.Errorf("authorization limits json is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("contains multiple JSON values")
		}
		return port.ManagementRequestQuotaLimits{}, fmt.Errorf("authorization limits json is invalid: %w", err)
	}
	var rawLimits map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value.String), &rawLimits); err != nil {
		return port.ManagementRequestQuotaLimits{}, fmt.Errorf("authorization limits json is invalid: %w", err)
	}
	for _, name := range []string{"hourly", "daily", "weekly", "monthly", "total"} {
		if raw, present := rawLimits[name]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return port.ManagementRequestQuotaLimits{}, fmt.Errorf("authorization %s quota limit must not be null", name)
		}
	}
	if err := validateCooldownAccountRetestQuotaLimits(limits); err != nil {
		return port.ManagementRequestQuotaLimits{}, err
	}
	return limits, nil
}

func validateCooldownAccountRetestQuotaLimits(limits port.ManagementRequestQuotaLimits) error {
	if limits.Hourly != nil {
		if !limits.Hourly.Enabled || limits.Hourly.Hours < 1 || limits.Hourly.Hours > 24*30 || !validCooldownAccountRetestQuotaAmount(limits.Hourly.Limit) {
			return fmt.Errorf("authorization hourly quota limit is invalid")
		}
	}
	for name, limit := range map[string]*port.ManagementRequestQuotaLimit{
		"daily": limits.Daily, "weekly": limits.Weekly, "monthly": limits.Monthly, "total": limits.Total,
	} {
		if limit != nil && (!limit.Enabled || !validCooldownAccountRetestQuotaAmount(limit.Limit)) {
			return fmt.Errorf("authorization %s quota limit is invalid", name)
		}
	}
	return nil
}

func validCooldownAccountRetestQuotaAmount(value float64) bool {
	if value <= 0 || value > float64(1<<53-1) || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	scaled := value * 1_000_000
	return math.Round(scaled) == scaled
}

func uniqueCooldownAccountRetestQuotaAccountIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	output := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	return output
}

var _ port.CooldownAccountRetestQuotaSubjectReader = (*Store)(nil)
