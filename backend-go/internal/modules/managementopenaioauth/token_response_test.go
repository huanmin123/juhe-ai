package managementopenaioauth

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeTokenResponseAcceptsBoundedNodeCompatiblePayload(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	info, err := DecodeTokenResponse(strings.NewReader(`{
		"access_token":"access",
		"refresh_token":"refresh",
		"id_token":"id",
		"token_type":"Bearer",
		"expires_in":"3600",
		"ignored_by_newer_upstream":true
	}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if info.AccessToken != "access" || info.RefreshToken != "refresh" || info.IDToken != "id" || info.TokenType != "Bearer" || info.ExpiresIn != 3600 {
		t.Fatalf("token info = %#v", info)
	}
	if !info.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires at = %s", info.ExpiresAt)
	}
}

func TestDecodeTokenResponseRejectsUnsafeOrMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing access token", body: `{"expires_in":3600}`},
		{name: "empty access token", body: `{"access_token":"  ","expires_in":3600}`},
		{name: "missing expires", body: `{"access_token":"access"}`},
		{name: "zero expires", body: `{"access_token":"access","expires_in":0}`},
		{name: "fractional expires", body: `{"access_token":"access","expires_in":1.5}`},
		{name: "invalid json", body: `{"access_token":`},
		{name: "trailing json", body: `{"access_token":"a","expires_in":1}{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeTokenResponse(strings.NewReader(tc.body), time.Now().UTC())
			if ErrorCodeOf(err) != ErrorCodeUpstreamUnavailable {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), tc.body) {
				t.Fatal("error leaked token response body")
			}
		})
	}
}

func TestDecodeTokenResponseEnforcesGoldenByteLimit(t *testing.T) {
	body := `{"access_token":"` + strings.Repeat("x", TokenResponseMaxBytes) + `","expires_in":1}`
	_, err := DecodeTokenResponse(strings.NewReader(body), time.Now().UTC())
	if ErrorCodeOf(err) != ErrorCodeUpstreamUnavailable {
		t.Fatalf("error = %v", err)
	}
}
