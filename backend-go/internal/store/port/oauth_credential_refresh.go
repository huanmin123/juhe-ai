package port

import (
	"context"
	"fmt"
	"time"
)

type OAuthCredentialRefreshSecrets struct {
	credentialsEncrypted  string
	credentialFingerprint string
	credentialMask        string
}

func NewOAuthCredentialRefreshSecrets(credentialsEncrypted, credentialFingerprint, credentialMask string) OAuthCredentialRefreshSecrets {
	return OAuthCredentialRefreshSecrets{
		credentialsEncrypted:  credentialsEncrypted,
		credentialFingerprint: credentialFingerprint,
		credentialMask:        credentialMask,
	}
}

func (secrets OAuthCredentialRefreshSecrets) CredentialsEncrypted() string {
	return secrets.credentialsEncrypted
}

func (secrets OAuthCredentialRefreshSecrets) CredentialFingerprint() string {
	return secrets.credentialFingerprint
}

func (secrets OAuthCredentialRefreshSecrets) CredentialMask() string {
	return secrets.credentialMask
}

func (OAuthCredentialRefreshSecrets) String() string {
	return "OAuthCredentialRefreshSecrets{redacted}"
}

func (secrets OAuthCredentialRefreshSecrets) GoString() string {
	return secrets.String()
}

// OAuthCredentialRefreshCASInput contains already encrypted and normalized
// credential material. String methods intentionally omit every credential field.
type OAuthCredentialRefreshCASInput struct {
	AccountID                        string
	SystemAccountID                  string
	ExpectedAccountType              string
	ExpectedConfigRevision           int
	Secrets                          OAuthCredentialRefreshSecrets
	AccessTokenExpiresAt             *time.Time
	RefreshTokenPresent              bool
	CircuitOwnerConfigurationChanged bool
	UpdatedAt                        time.Time
}

func (input OAuthCredentialRefreshCASInput) String() string {
	return fmt.Sprintf(
		"OAuthCredentialRefreshCASInput{AccountID:%q SystemAccountID:%q ExpectedAccountType:%q ExpectedConfigRevision:%d RefreshTokenPresent:%t CircuitOwnerConfigurationChanged:%t UpdatedAt:%s}",
		input.AccountID,
		input.SystemAccountID,
		input.ExpectedAccountType,
		input.ExpectedConfigRevision,
		input.RefreshTokenPresent,
		input.CircuitOwnerConfigurationChanged,
		input.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
}

func (input OAuthCredentialRefreshCASInput) GoString() string {
	return input.String()
}

type OAuthCredentialRefreshCASResult struct {
	ConfigRevision   int
	DispatchRevision int64
}

type OAuthCredentialRefreshStore interface {
	CompareAndSwapOAuthCredentials(context.Context, OAuthCredentialRefreshCASInput) (OAuthCredentialRefreshCASResult, bool, error)
}
