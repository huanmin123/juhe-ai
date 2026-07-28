package port

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOAuthCredentialRefreshCASInputFormattingRedactsCredentials(t *testing.T) {
	input := OAuthCredentialRefreshCASInput{
		AccountID:              "acct-1",
		SystemAccountID:        "system-1",
		ExpectedAccountType:    "oauth",
		ExpectedConfigRevision: 7,
		Secrets:                NewOAuthCredentialRefreshSecrets("encrypted-secret-value", "secret-fingerprint", "secret-mask"),
		UpdatedAt:              time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC),
	}
	for name, rendered := range map[string]string{
		"string": input.String(),
		"go":     fmt.Sprintf("%#v", input),
		"detail": fmt.Sprintf("%+v", input),
		"json": func() string {
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			return string(encoded)
		}(),
	} {
		for _, secret := range []string{"encrypted-secret-value", "secret-fingerprint", "secret-mask"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s formatting leaked %q in %q", name, secret, rendered)
			}
		}
	}
}
