// Send-and-claim authorization model. Offer (grant), source and runtime rows
// share the seven-value status set; confirmation checkpoints and material
// review no longer exist.
package authz

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/authzrevision"
)

// offerColumns is the grant projection used by the routes.
const offerColumns = `g.id, g.resource_type, g.resource_id, g.resource_owner_system_account_id,
	g.grantee_type, COALESCE(g.grantee_system_account_id, ''), COALESCE(g.grantee_team_id, ''),
	g.status, COALESCE(g.remark, ''), COALESCE(g.expires_at, ''),
	g.created_by,
	COALESCE(g.source_revision, ''), g.expiry_cycle,
	COALESCE(g.ended_at, ''), COALESCE(g.ended_reason, ''),
	g.created_at, g.updated_at`

type offerRecord struct {
	ID             string
	ResourceType   string
	ResourceID     string
	OwnerID        string
	GranteeType    string
	GranteeUserID  string
	GranteeTeamID  string
	Status         string
	Remark         string
	ExpiresAt      string
	CreatedBy      string
	SourceRevision string
	ExpiryCycle    int
	EndedAt        string
	EndedReason    string
	CreatedAt      string
	UpdatedAt      string
}

func scanOffer(scanner interface{ Scan(...any) error }) (offerRecord, error) {
	var row offerRecord
	err := scanner.Scan(&row.ID, &row.ResourceType, &row.ResourceID, &row.OwnerID,
		&row.GranteeType, &row.GranteeUserID, &row.GranteeTeamID,
		&row.Status, &row.Remark, &row.ExpiresAt,
		&row.CreatedBy,
		&row.SourceRevision, &row.ExpiryCycle,
		&row.EndedAt, &row.EndedReason,
		&row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func (s *Store) getOfferForUpdate(ctx context.Context, tx *sql.Tx, id string) (*offerRecord, error) {
	row, err := scanOffer(tx.QueryRowContext(ctx, s.bind(`SELECT `+offerColumns+`
		FROM `+s.table("resource_authorization_grants")+` g WHERE g.id = ? FOR UPDATE`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) getOffer(ctx context.Context, id string) (*offerRecord, error) {
	row, err := scanOffer(s.db.QueryRowContext(ctx, s.bind(`SELECT `+offerColumns+`
		FROM `+s.table("resource_authorization_grants")+` g WHERE g.id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

const sourceColumns = `s.id, COALESCE(s.grant_id, ''), s.authorization_id, s.source_type,
	COALESCE(s.source_team_id, ''), s.status, COALESCE(s.activated_at, ''),
	COALESCE(s.accepted_at, ''), COALESCE(s.accepted_by, ''),
	COALESCE(s.declined_at, ''), COALESCE(s.declined_by, ''),
	COALESCE(s.returned_at, ''), COALESCE(s.returned_by, ''),
	COALESCE(s.ended_at, ''), COALESCE(s.ended_reason, ''),
	s.expiry_cycle,
	s.created_by, s.created_at, s.updated_at`

type sourceRecord struct {
	ID             string
	GrantID        string
	AuthorizationID string
	SourceType     string
	SourceTeamID   string
	Status         string
	ActivatedAt    string
	AcceptedAt     string
	AcceptedBy     string
	DeclinedAt     string
	DeclinedBy     string
	ReturnedAt     string
	ReturnedBy     string
	EndedAt        string
	EndedReason    string
	ExpiryCycle    int
	CreatedBy      string
	CreatedAt      string
	UpdatedAt      string
}

func scanSource(scanner interface{ Scan(...any) error }) (sourceRecord, error) {
	var row sourceRecord
	err := scanner.Scan(&row.ID, &row.GrantID, &row.AuthorizationID, &row.SourceType,
		&row.SourceTeamID, &row.Status, &row.ActivatedAt,
		&row.AcceptedAt, &row.AcceptedBy, &row.DeclinedAt, &row.DeclinedBy,
		&row.ReturnedAt, &row.ReturnedBy, &row.EndedAt, &row.EndedReason,
		&row.ExpiryCycle,
		&row.CreatedBy, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func (s *Store) getSourceForUpdate(ctx context.Context, tx *sql.Tx, id string) (*sourceRecord, error) {
	row, err := scanSource(tx.QueryRowContext(ctx, s.bind(`SELECT `+sourceColumns+`
		FROM `+s.table("resource_authorization_sources")+` s WHERE s.id = ? FOR UPDATE`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) listSourcesForRuntime(ctx context.Context, tx *sql.Tx, runtimeID string) ([]sourceRecord, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT `+sourceColumns+`
		FROM `+s.table("resource_authorization_sources")+` s
		WHERE s.authorization_id = ? ORDER BY s.created_at ASC, s.id ASC`), runtimeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sourceRecord{}
	for rows.Next() {
		row, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type runtimeRecord struct {
	ID              string
	ResourceType    string
	ResourceID      string
	OwnerID         string
	GranteeID       string
	Status          string
	EffectiveType   string
	EffectiveTeamID string
	ExpiresAt       string
	CreatedAt       string
	UpdatedAt       string
}

func (s *Store) getRuntimeForUpdate(ctx context.Context, tx *sql.Tx, id string) (*runtimeRecord, error) {
	var row runtimeRecord
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id, resource_type, resource_id,
			resource_owner_system_account_id, grantee_system_account_id, status,
			COALESCE(effective_source_type, ''), COALESCE(effective_source_team_id, ''),
			COALESCE(expires_at, ''),
			created_at, updated_at
		FROM `+s.table("resource_authorizations")+` WHERE id = ? FOR UPDATE`), id).
		Scan(&row.ID, &row.ResourceType, &row.ResourceID, &row.OwnerID, &row.GranteeID, &row.Status,
			&row.EffectiveType, &row.EffectiveTeamID, &row.ExpiresAt,
			&row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) findRuntimeByResourceGrantee(ctx context.Context, tx *sql.Tx, resourceType, resourceID, granteeID string) (*runtimeRecord, error) {
	var id string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("resource_authorizations")+`
		WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1`),
		resourceType, resourceID, granteeID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.getRuntimeForUpdate(ctx, tx, id)
}

func (s *Store) ensureRuntime(ctx context.Context, tx *sql.Tx, resourceType, resourceID, ownerID, granteeID, actor, now string) (*runtimeRecord, error) {
	runtime, err := s.findRuntimeByResourceGrantee(ctx, tx, resourceType, resourceID, granteeID)
	if err != nil {
		return nil, err
	}
	if runtime != nil {
		return runtime, nil
	}
	id := newRuntimeID(s.now)
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("resource_authorizations")+`
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 scope, status, activated_at, last_source_changed_at,
		 remark, expires_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'use', 'pending', NULL, ?, NULL, NULL, ?, ?, ?)`),
		id, resourceType, resourceID, ownerID, granteeID, now, actor, now, now); err != nil {
		return nil, err
	}
	return s.getRuntimeForUpdate(ctx, tx, id)
}

func isLiveStatus(status string) bool {
	return status == StatusPending || status == StatusActive
}

func isTerminalStatus(status string) bool {
	return status == StatusExpired || status == StatusRevoked || status == StatusReturned || status == StatusDeclined
}

func isOpenOfferStatus(status string) bool {
	switch status {
	case StatusPending, StatusActive, StatusPaused:
		return true
	default:
		return false
	}
}

func isPersonalGrant(grant *offerRecord) bool { return grant.GranteeType == "system_account" }

func grantTargetID(grant *offerRecord) string {
	if grant.GranteeType == "team" {
		return grant.GranteeTeamID
	}
	return grant.GranteeUserID
}

func (s *Store) revisionSecretOrFail() (string, error) {
	if strings.TrimSpace(s.revisionSecret) == "" {
		return "", errors.New("授权来源修订密钥未配置")
	}
	return s.revisionSecret, nil
}

type revisionQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) accountRevision(ctx context.Context, q revisionQueryer, accountID string) (string, int, error) {
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return "", 0, err
	}
	revision, version, _, err := authzrevision.Compute(ctx, q, accountID, secret)
	return revision, version, err
}

func (s *Store) groupRevision(resourceID, ownerID string) (string, int, error) {
	secret, err := s.revisionSecretOrFail()
	if err != nil {
		return "", 0, err
	}
	return authzrevision.Group(secret, resourceID, ownerID), authzrevision.Version, nil
}

func (s *Store) sourceCurrentRevision(ctx context.Context, q revisionQueryer, resourceType, resourceID, ownerID string) (string, int, error) {
	if resourceType == "account" {
		return s.accountRevision(ctx, q, resourceID)
	}
	return s.groupRevision(resourceID, ownerID)
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nowPlus(now time.Time, d time.Duration) string { return formatTime(now.Add(d)) }

var errNoRestorable = &Fail{Message: "没有可恢复的本次周期来源，请重新发起授权", Code: "no_restorable_source"}

func failCode(code, message string) *Fail { return &Fail{Code: code, Message: message} }

func requireString(value, message string) error {
	if strings.TrimSpace(value) == "" {
		return failf("%s", message)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, query, args...)
}

func (s *Store) invalidateGovernance(ctx context.Context, reason string) {
	s.invalidateAfterBusinessWrite(ctx, reason)
}

func newGrantID(now func() time.Time) string {
	return "rauthgrant_" + randomSuffix()
}

// randomSuffix mirrors Node newId: 16 random bytes hex.
func randomSuffix() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}

func newRuntimeID(now func() time.Time) string {
	return "rauth_" + randomSuffix()
}

func newSourceID(now func() time.Time) string {
	return "rauthsrc_" + randomSuffix()
}
