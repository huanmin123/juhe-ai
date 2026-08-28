// Package businessauth exposes the Gateway-owned Business SQLite session
// lifecycle. It is deliberately a thin in-process adapter over the existing
// modelcheckauth implementation: credentials are verified and fenced there,
// while this package adds the explicit Business-owner handoff gate required
// before any session/account write is allowed.
package businessauth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var (
	ErrOwnerGate = errors.New("Business SQLite owner handoff gate is not satisfied")
)

// OwnerGate is the immutable evidence captured when Gateway becomes the
// Business SQLite writer. Every mutating operation requires all fields to be
// true; a partial handoff fails closed.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// Service owns the Business account/session lifecycle in-process. It has no
// HTTP, IPC, queue, or Node dependency.
type Service struct {
	auth *modelcheckauth.Authenticator
	gate OwnerGate
}

// Port is the dependency-injection boundary consumed by a future Gateway
// management handler. Keeping this interface local prevents callers from
// reaching through to modelcheckauth or constructing ad-hoc SQL writes.
type Port interface {
	Login(context.Context, string, string, int) (modelcheckauth.IssuedSession, modelcheckauth.VerifiedCredentials, bool, error)
	CreateSession(context.Context, string, string, int) (modelcheckauth.IssuedSession, bool, error)
	Authenticate(context.Context, string, bool, bool) (modelcheckauth.Actor, error)
	Touch(context.Context, string) (modelcheckauth.Actor, error)
	RevokeToken(context.Context, string) error
	Logout(context.Context, string) error
	RevokeSession(context.Context, string) error
	RevokeOtherSessions(context.Context, string, string) error
	ChangePassword(context.Context, string, string, string, string) (bool, error)
	CleanupExpiredSessions(context.Context, time.Time, int) (int64, error)
	VerifyCredentials(context.Context, string, string) (modelcheckauth.VerifiedCredentials, bool, error)
	CurrentCredentialRevision(context.Context, string) (string, error)
}

var _ Port = (*Service)(nil)

func New(db *sql.DB, mode modelcheckauth.Mode, now func() time.Time, gate OwnerGate) (*Service, error) {
	if db == nil {
		return nil, errors.New("business auth database is required")
	}
	auth, err := modelcheckauth.New(db, mode, now)
	if err != nil {
		return nil, err
	}
	return &Service{auth: auth, gate: gate}, nil
}

func (s *Service) requireOwner() error {
	if s == nil || s.auth == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

// CheckContract verifies the required Business relations without writing.
func (s *Service) CheckContract(ctx context.Context) error {
	if s == nil || s.auth == nil {
		return ErrOwnerGate
	}
	return s.auth.CheckContract(ctx)
}

func (s *Service) Login(ctx context.Context, username, password string, ttlDays int) (modelcheckauth.IssuedSession, modelcheckauth.VerifiedCredentials, bool, error) {
	if err := s.requireOwner(); err != nil {
		return modelcheckauth.IssuedSession{}, modelcheckauth.VerifiedCredentials{}, false, err
	}
	return s.auth.Login(ctx, strings.TrimSpace(username), password, ttlDays)
}

func (s *Service) CreateSession(ctx context.Context, systemAccountID, credentialRevision string, ttlDays int) (modelcheckauth.IssuedSession, bool, error) {
	if err := s.requireOwner(); err != nil {
		return modelcheckauth.IssuedSession{}, false, err
	}
	return s.auth.CreateAuthenticatedSession(ctx, systemAccountID, credentialRevision, ttlDays)
}

// Authenticate validates a token and optionally updates last_seen_at using
// the modelcheckauth CAS touch fence. rejectMustChange controls whether an
// account flagged for initial password change is rejected.
func (s *Service) Authenticate(ctx context.Context, token string, rejectMustChange bool, touch bool) (modelcheckauth.Actor, error) {
	if err := s.requireOwner(); err != nil {
		return modelcheckauth.Actor{}, err
	}
	if rejectMustChange {
		var actor modelcheckauth.Actor
		var err error
		if touch {
			actor, err = s.auth.AuthenticateToken(ctx, token)
		} else {
			actor, err = s.auth.AuthenticateTokenForSessionNoTouch(ctx, token)
			if err == nil && actor.MustChangePassword {
				return modelcheckauth.Actor{}, modelcheckauth.ErrMustChange
			}
		}
		return actor, err
	}
	if touch {
		return s.auth.AuthenticateTokenForSession(ctx, token)
	}
	return s.auth.AuthenticateTokenForSessionNoTouch(ctx, token)
}

func (s *Service) Touch(ctx context.Context, token string) (modelcheckauth.Actor, error) {
	return s.Authenticate(ctx, token, false, true)
}

func (s *Service) RevokeToken(ctx context.Context, token string) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	return s.auth.RevokeToken(ctx, token)
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.RevokeToken(ctx, token)
}

func (s *Service) RevokeSession(ctx context.Context, sessionID string) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	return s.auth.RevokeSession(ctx, sessionID)
}

func (s *Service) RevokeOtherSessions(ctx context.Context, systemAccountID, keepSessionID string) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	return s.auth.RevokeOtherSessions(ctx, systemAccountID, keepSessionID)
}

func (s *Service) ChangePassword(ctx context.Context, systemAccountID, expectedRevision, newPassword, keepSessionID string) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	return s.auth.ChangePassword(ctx, systemAccountID, expectedRevision, newPassword, keepSessionID)
}

func (s *Service) CleanupExpiredSessions(ctx context.Context, now time.Time, limit int) (int64, error) {
	if err := s.requireOwner(); err != nil {
		return 0, err
	}
	return s.auth.CleanupExpiredSessions(ctx, now, limit)
}

// VerifyCredentials is intentionally exposed only as a non-secret result;
// submitted passwords never leave the in-process call and are not persisted.
func (s *Service) VerifyCredentials(ctx context.Context, username, password string) (modelcheckauth.VerifiedCredentials, bool, error) {
	if err := s.requireOwner(); err != nil {
		return modelcheckauth.VerifiedCredentials{}, false, err
	}
	return s.auth.VerifySystemAccountCredentials(ctx, strings.TrimSpace(username), password)
}

func (s *Service) CurrentCredentialRevision(ctx context.Context, systemAccountID string) (string, error) {
	if err := s.requireOwner(); err != nil {
		return "", err
	}
	return s.auth.CurrentCredentialRevision(ctx, systemAccountID)
}
