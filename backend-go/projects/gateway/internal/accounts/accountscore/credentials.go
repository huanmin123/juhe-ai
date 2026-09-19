package accountscore

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// Credentials mirrors AccountCredentials: an open record of credential fields.
type Credentials map[string]any

// ModelMapping mirrors AccountModelMapping.
type ModelMapping struct {
	SourceModel            string `json:"sourceModel"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily"`
	UpstreamModel          string `json:"upstreamModel"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily"`
	Enabled                *bool  `json:"enabled,omitempty"`
}

// AuthorizedAccountReader is the narrow cross-package port of the authz
// slice's authorized-instance projection
// (authz.Store.AuthorizedReadableAccountIDs). It maps the readable
// authorization instance account ids for one viewer.
type AuthorizedAccountReader interface {
	AuthorizedReadableAccountIDs(ctx context.Context, viewerSystemAccountID string) (map[string]bool, error)
}

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
