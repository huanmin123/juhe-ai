package oauthrefresh

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// The AES v1 credential envelope implementation lives in the shared
// backend-go-platform/accountcrypto package (the single copy shared with the
// gateway accounts/apikeys slices). These wrappers keep the oauthrefresh call
// surface stable; envelopes are byte-for-byte interoperable with
// backend/src/storage/crypto.ts.

// EncryptJSON seals a JSON-serializable value with the runtime secret.
func EncryptJSON(secret string, value any) (string, error) {
	return accountcrypto.EncryptJSON(secret, value)
}

// DecryptJSON opens a v1 envelope into target. Only the v1 envelope is
// accepted; GCM authentication failures surface as the Node error copy.
func DecryptJSON(secret string, envelope string, target any) error {
	return accountcrypto.DecryptJSON(secret, envelope, target)
}

// hashSecret mirrors storage/crypto.ts hashSecret (sha256 hex of the trimmed
// credential source; accounts.credential_fingerprint material).
func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// maskSecret mirrors storage/crypto.ts maskSecret: short secrets keep head+tail
// pairs, longer ones a 6/4 split (accounts.credential_mask material).
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 10 {
		return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
	}
	return string(runes[:6]) + "***" + string(runes[len(runes)-4:])
}
