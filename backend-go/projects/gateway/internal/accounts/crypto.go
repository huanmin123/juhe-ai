// Package accounts owns the M08 vertical slice: the /accounts (admin) +
// /my-accounts (self) route family ported from
// backend/src/modules/accounts/accounts.routes.ts plus the account.*
// repositories under backend/src/storage/. The slice covers the paged
// management list, the options dropdown, the owner-scoped edit-basic detail,
// guarded create with AES-GCM sealed credentials, the basic-config patch with
// config_revision optimistic locking, the lock/unlock/lock-config family, the
// soft delete with related cleanup and the account tag endpoints. The M09
// companion files add the batch-edit context/update, the CCS import
// preview/confirm pipeline and the native export document. The clone
// context, upstream model catalog sync, credential normalization services and
// the balance health probes remain companion slices.
package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// EncryptJSON delegates to the shared platform accountcrypto envelope (the
// single AES-256-GCM v1 copy shared with the apikeys slice and jobs
// oauthrefresh). Existing Node accounts.credentials_encrypted rows decrypt
// byte-for-byte through DecryptJSON.
func EncryptJSON(secret string, value any) (string, error) {
	return accountcrypto.EncryptJSON(secret, value)
}

// DecryptJSON delegates to the shared platform accountcrypto envelope: only
// the v1 envelope is accepted and the GCM tag is verified before JSON
// decoding.
func DecryptJSON(secret string, envelope string, target any) error {
	return accountcrypto.DecryptJSON(secret, envelope, target)
}

// HashSecret mirrors hashSecret: sha256 hex digest (credential fingerprint
// material, shared with the api_keys slice).
func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// MaskSecret mirrors maskSecret: short secrets keep head+tail pairs, longer
// ones keep a 6/4 split. The masked column and every log/response surface use
// this shape so credential material never appears in clear text.
func MaskSecret(value any) string {
	text, ok := value.(string)
	if !ok || len(text) == 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) == 1 {
		// Node slice(-2) is safe on a single rune; Go slicing would panic.
		return string(runes[:1]) + "***"
	}
	if len(runes) <= 10 {
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
	return string(runes[:6]) + "***" + string(runes[len(runes)-4:])
}

// NewAccountID mirrors Node newId('acc'):
// "acc_{Date.now()}_{8 hex chars}".
func NewAccountID() string {
	return newID("acc")
}

// NewTagID mirrors Node newId('acctag').
func NewTagID() string { return newID("acctag") }

func newID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" +
		hex.EncodeToString(buf)[:8]
}
