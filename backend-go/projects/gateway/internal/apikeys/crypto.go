// Package apikeys owns the M07 vertical slice: the /api-keys (admin) +
// /my-api-keys (self) route family ported from
// backend/src/modules/api-keys/api-keys.routes.ts plus the api-key.*
// repositories under backend/src/storage/. The slice covers the paged list
// (masked keys only), the owner-scoped detail, the one-shot secret reveal,
// guarded create with AES-GCM sealed plaintext, secret refresh with triple
// cache invalidation and the atomic hard delete that enqueues a
// api_key_record_cleanup_targets row. PATCH /:id (revision-locked update),
// the request-quota hourly window worker and the J5 usage summaries are
// companion slices; the usage projections here render the shared zero value.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// EncryptJSON delegates to the shared platform accountcrypto envelope (the
// single AES-256-GCM v1 copy shared with the accounts slice and jobs
// oauthrefresh). Existing Node rows decrypt byte-for-byte through DecryptJSON.
func EncryptJSON(secret string, value any) (string, error) {
	return accountcrypto.EncryptJSON(secret, value)
}

// DecryptJSON delegates to the shared platform accountcrypto envelope: only
// the v1 envelope is accepted and the GCM tag is verified before JSON
// decoding.
func DecryptJSON(secret string, envelope string, target any) error {
	return accountcrypto.DecryptJSON(secret, envelope, target)
}

// HashSecret mirrors hashSecret: sha256 hex digest of the plaintext key
// (api_keys.key_hash lookup material).
func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey mirrors createApiKey: "sk-" + 32 random bytes hex (67 chars).
func NewAPIKey() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return "sk-" + hex.EncodeToString(buf)
}

// secretPayload mirrors the encryptJson({ key }) envelope Node stores in
// api_keys.key_secret_encrypted.
type secretPayload struct {
	Key string `json:"key"`
}
