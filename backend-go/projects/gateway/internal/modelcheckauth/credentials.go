package modelcheckauth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"strings"
)

// VerifiedCredentials is the non-secret result of checking a Node-format
// system-account password. CredentialRevision is the session fence derived
// from the stored hash, never from the submitted password.
type VerifiedCredentials struct {
	SystemAccountID    string
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
	CredentialRevision string
}

// VerifySystemAccountCredentials directly verifies the existing Node PBKDF2
// representation. Node passes the base64url salt text itself to PBKDF2, so it
// must be treated as UTF-8 bytes rather than decoded before derivation.
func (a *Authenticator) VerifySystemAccountCredentials(ctx context.Context, username, password string) (VerifiedCredentials, bool, error) {
	if a == nil || a.db == nil || strings.TrimSpace(username) == "" || password == "" {
		return VerifiedCredentials{}, false, nil
	}
	var verified VerifiedCredentials
	var status, passwordHash string
	query := `SELECT id,username,COALESCE(display_name,''),role,status,password_hash,must_change_password FROM ` + a.table("system_accounts") + ` WHERE lower(username)=lower(?) LIMIT 1`
	if err := a.db.QueryRowContext(ctx, a.bind(query), username).Scan(&verified.SystemAccountID, &verified.Username, &verified.DisplayName, &verified.Role, &status, &passwordHash, &verified.MustChangePassword); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return VerifiedCredentials{}, false, nil
		}
		return VerifiedCredentials{}, false, err
	}
	if status != "active" || !verifyNodePBKDF2Password(password, passwordHash) {
		return VerifiedCredentials{}, false, nil
	}
	verified.CredentialRevision = hashString(passwordHash)
	return verified, true, nil
}

// Login verifies a submitted password and then rechecks its derived revision
// inside CreateAuthenticatedSession's transaction. A password change between
// the read and session insert therefore fails closed instead of issuing a
// session for stale credentials.
func (a *Authenticator) Login(ctx context.Context, username, password string, ttlDays int) (IssuedSession, VerifiedCredentials, bool, error) {
	verified, ok, err := a.VerifySystemAccountCredentials(ctx, username, password)
	if err != nil || !ok {
		return IssuedSession{}, VerifiedCredentials{}, false, err
	}
	issued, issuedOK, err := a.CreateAuthenticatedSession(ctx, verified.SystemAccountID, verified.CredentialRevision, ttlDays)
	if err != nil || !issuedOK {
		return IssuedSession{}, VerifiedCredentials{}, false, err
	}
	return issued, verified, true, nil
}

func verifyNodePBKDF2Password(password, passwordHash string) bool {
	parts := strings.Split(passwordHash, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha512" {
		return false
	}
	iterationsValue, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil || math.IsNaN(iterationsValue) || math.IsInf(iterationsValue, 0) || iterationsValue < 1 || iterationsValue > float64(maxIntValue()) {
		return false
	}
	iterations := int(math.Trunc(iterationsValue))
	if iterations < 1 {
		return false
	}
	expected, err := decodeNodeBase64URL(parts[4])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual, err := pbkdf2.Key(sha512.New, password, []byte(parts[3]), iterations, len(expected))
	if err != nil || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func decodeNodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty base64url value")
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64url value")
}

func maxIntValue() int {
	return int(^uint(0) >> 1)
}
