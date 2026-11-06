package modelcheckauth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha512"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
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

// NodePasswordHashIterations is the PBKDF2-SHA512 iteration count for every
// newly written system-account password hash. T169 raised it from the
// historical Node value (120000); stored hashes below this target are
// transparently re-hashed with this count on the next successful login.
const NodePasswordHashIterations = 600000

// verifySystemAccountPassword is the PBKDF2 checker used for both the real
// row and the missing-user dummy arm below. Package-level so tests can count
// calls without touching the Authenticator wiring.
var verifySystemAccountPassword = verifyNodePBKDF2Password

// dummySystemAccountPasswordHash is a well-formed Node-format hash carrying
// NodePasswordHashIterations (sha512), generated the same way as the stored
// hashes. The missing-user arm runs the checker against it so both arms cost
// the same PBKDF2 work (SEC-07 timing alignment).
const dummySystemAccountPasswordHash = "pbkdf2$sha512$600000$MDEyMzQ1Njc4OWFiY2RlZg$aKla08LzVb4jj_YVGoMh4KaFYWxld_rnynZitVkmTNw"

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
			// Timing-enumeration guard: the existing-user arm runs a full
			// PBKDF2 verification, so a missing username must cost the same
			// work or response latency reveals which usernames exist. Run the
			// checker once against the fixed dummy hash and discard the
			// result; the response stays unverified either way.
			verifySystemAccountPassword(password, dummySystemAccountPasswordHash)
			return VerifiedCredentials{}, false, nil
		}
		return VerifiedCredentials{}, false, err
	}
	// Inactive accounts must still pay the full PBKDF2 cost before the status
	// rejection: skipping the hash work would make "known username but
	// disabled" measurably faster than both an active account and the
	// missing-user arm (SEC-07). The stored hash is not exercised.
	if status != "active" {
		verifySystemAccountPassword(password, dummySystemAccountPasswordHash)
		return VerifiedCredentials{}, false, nil
	}
	if !verifySystemAccountPassword(password, passwordHash) {
		return VerifiedCredentials{}, false, nil
	}
	// Transparent upgrade of pre-T169 hashes (120000 iterations -> the target).
	// The password has just been proven, so the plaintext can re-derive the
	// stronger hash; a lost CAS or write failure keeps the old hash and
	// revision and must never fail the login.
	passwordHash = a.upgradeSystemAccountPasswordHash(ctx, verified.SystemAccountID, password, passwordHash)
	verified.MustChangePassword = verified.MustChangePassword && !a.initialPasswordGateDisabled
	verified.CredentialRevision = hashString(passwordHash)
	return verified, true, nil
}

// upgradeSystemAccountPasswordHash rewrites a stored hash whose iteration count
// is below NodePasswordHashIterations. The UPDATE is a compare-and-swap on the
// old hash text so a concurrent password change is never clobbered; only an
// affected row count of 1 makes the upgraded hash (and therefore the returned
// credential revision) authoritative. Any error or lost CAS leaves the caller
// on the old hash; the password verification result still stands, and the
// subsequent session insert remains fail-closed if a concurrent change moved
// the fence.
func (a *Authenticator) upgradeSystemAccountPasswordHash(ctx context.Context, systemAccountID, password, passwordHash string) string {
	iterations, ok := parseNodePasswordHashIterations(passwordHash)
	if !ok || iterations >= NodePasswordHashIterations {
		return passwordHash
	}
	upgraded, err := newNodePasswordHash(password)
	if err != nil {
		return passwordHash
	}
	result, err := a.db.ExecContext(ctx, a.bind(`UPDATE `+a.table("system_accounts")+` SET password_hash=?, updated_at=? WHERE id=? AND password_hash=?`),
		upgraded, nodeISOTime(a.now().UTC()), systemAccountID, passwordHash)
	if err != nil {
		return passwordHash
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return passwordHash
	}
	return upgraded
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

// parseNodePasswordHashIterations extracts the iteration count from a
// pbkdf2$sha512$<iterations>$salt$digest envelope. Non-numeric, zero,
// negative and out-of-range counts are rejected instead of being coerced.
func parseNodePasswordHashIterations(passwordHash string) (int, bool) {
	parts := strings.Split(passwordHash, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha512" {
		return 0, false
	}
	iterations, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil || iterations < 1 {
		return 0, false
	}
	return iterations, true
}

var (
	// legacyVerificationPadIterations records the extra PBKDF2 work burned on
	// failed legacy-hash verification. It exists so tests can pin the SEC-07
	// timing alignment without wall-clock assertions.
	legacyVerificationPadIterations atomic.Int64
)

func verifyNodePBKDF2Password(password, passwordHash string) bool {
	iterations, ok := parseNodePasswordHashIterations(passwordHash)
	if !ok {
		return false
	}
	parts := strings.Split(passwordHash, "$")
	expected, err := decodeNodeBase64URL(parts[4])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual, err := pbkdf2.Key(sha512.New, password, []byte(parts[3]), iterations, len(expected))
	if err != nil || len(actual) != len(expected) {
		return false
	}
	matched := subtle.ConstantTimeCompare(expected, actual) == 1
	if !matched && iterations < NodePasswordHashIterations {
		// SEC-07 timing alignment: missing/inactive accounts and current
		// 600000-iteration hashes spend the full target cost on failure. A
		// legacy 120000 hash must not fail ~5x faster and leak account
		// existence through response latency; burn the difference with a
		// discarded derivation. The password was not proven, so no rehash.
		pad := NodePasswordHashIterations - iterations
		legacyVerificationPadIterations.Add(int64(pad))
		_, _ = pbkdf2.Key(sha512.New, password, []byte(parts[3]), pad, len(expected))
	}
	return matched
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
