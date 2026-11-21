// Governance helpers for the simplified send-and-claim model: the inbox
// badge counts. Cooldown, offer limits, suspensions and the notification
// audit trail were removed with the confirmation checkpoints.
package authz

import (
	"context"
)

// InboxSummary returns the badge counts for the authorization center.
type InboxSummary struct {
	PendingSources int `json:"pendingSources"`
	ActiveSources  int `json:"activeSources"`
}

func (s *Store) InboxSummary(ctx context.Context, systemAccountID string, now string) (InboxSummary, error) {
	var out InboxSummary
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = $1 AND s.status = 'pending'`), systemAccountID).Scan(&out.PendingSources); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT count(*) FROM `+s.table("resource_authorization_sources")+` s
		JOIN `+s.table("resource_authorizations")+` ra ON ra.id = s.authorization_id
		WHERE ra.grantee_system_account_id = $1 AND s.status = 'active'`), systemAccountID).Scan(&out.ActiveSources); err != nil {
		return out, err
	}
	return out, nil
}

