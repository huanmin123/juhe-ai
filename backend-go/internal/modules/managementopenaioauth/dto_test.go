package managementopenaioauth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicDTOJSONUsesExistingClientFieldNamesAndNeverIncludesLeaseSecrets(t *testing.T) {
	resultBody, err := json.Marshal(AuthURLResult{AuthURL: "https://auth.example", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if string(resultBody) != `{"authUrl":"https://auth.example","sessionId":"session"}` {
		t.Fatalf("auth URL result = %s", resultBody)
	}

	errorBody, err := json.Marshal(ErrorResponse{Code: ErrorCodeSessionExpired, Message: "OAuth 会话不存在或已过期"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(errorBody), "lease") || !strings.Contains(string(errorBody), `"code":"oauth_session_expired"`) {
		t.Fatalf("error response = %s", errorBody)
	}
}

func TestOAuthGrantDTOsRemainNarrow(t *testing.T) {
	codeBody, err := json.Marshal(CodeGrant{SessionID: "session", CallbackURL: "http://localhost/callback?code=x&state=y"})
	if err != nil {
		t.Fatal(err)
	}
	if string(codeBody) != `{"sessionId":"session","callbackUrl":"http://localhost/callback?code=x\u0026state=y"}` {
		t.Fatalf("code grant = %s", codeBody)
	}
	refreshBody, err := json.Marshal(RefreshGrant{RefreshToken: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if string(refreshBody) != `{"refreshToken":"refresh"}` {
		t.Fatalf("refresh grant = %s", refreshBody)
	}
}
