// w14d_final_gaps_test.go closes the remaining oauthmgmt gaps: per-provider
// refreshStored token guards, the crypto base64 arms, the credentials-patch
// parser branches, the grok SSO transport-error stages, the store rotation
// CAS conflict and the profile/group 500 arms plus the SSO duplicate-name
// failure loop.
package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW14dPlansRefreshStoredTokenGuard(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	store := env.store
	empty := &rotationAccount{Credentials: map[string]any{"access_token": "a"}}

	for _, plan := range providerPlans() {
		if _, err := plan.refreshStored(ctx, store, empty, ""); err == nil {
			t.Fatalf("%s refreshStored must demand a refresh token", plan.slug)
		}
	}
	// The openai stored refresh surfaces upstream failures verbatim.
	openai := openAIPlan()
	if _, err := openai.refreshStored(ctx, store, &rotationAccount{Credentials: map[string]any{"refresh_token": "rt"}}, ""); err == nil {
		t.Fatal("openai stored refresh without upstream success must fail")
	}
}

func TestW14dCryptoBase64Arms(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	var out map[string]any
	// iv / tag / ciphertext base64 arms are checked in order.
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":!!!:!!!", &out); err == nil {
		t.Fatal("bad tag base64 must fail")
	}
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":"+parts[2]+":!!!"+",", &out); err == nil {
		t.Fatal("bad ciphertext base64 must fail")
	}
	// A gcm-size tag mismatch fails before the open.
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":QUJD:"+parts[3], &out); err == nil {
		t.Fatal("short tag must fail")
	}
	// encryptJSON rejects values JSON cannot represent.
	if _, err := encryptJSON(testSecret, map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
}

func TestW14dPatchParserResidualArms(t *testing.T) {
	// endpointModes via the openai dialect: nil passes through as empty list
	// rejection, strings fail, a valid list passes.
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"supported_endpoint_modes": nil}); ok {
		t.Fatal("nil modes must fail")
	}
	if _, ok := parseAnthropicCredentialsPatch(map[string]any{"supported_endpoint_modes": []any{"chat", " responses "}}); !ok {
		t.Fatal("valid modes must pass")
	}
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"supported_endpoint_modes": []any{"chat"}}); !ok {
		t.Fatal("single mode must pass")
	}
	// Policy keys ride every dialect untouched.
	for _, parse := range []func(map[string]any) (map[string]any, bool){
		parseOpenAICredentialsPatch, parseAnthropicCredentialsPatch,
		parseGeminiCredentialsPatch, parseGrokCredentialsPatch,
	} {
		if patch, ok := parse(map[string]any{"response_inspection_rules": map[string]any{"a": 1}}); !ok {
			t.Fatal("policy key must pass")
		} else if patch["response_inspection_rules"] == nil {
			t.Fatalf("policy passthrough: %v", patch)
		}
	}
}

// errDevice fails every transport call with the scripted error.
type errDevice struct{ err error }

func (e *errDevice) Do(context.Context, SSODeviceRequest) (SSODeviceResponse, error) {
	return SSODeviceResponse{}, e.err
}

func TestW14dGrokSSOTransportErrorStages(t *testing.T) {
	boom := errors.New("w14d transport down")
	deps := SSODeviceDeps{Request: &errDevice{err: boom}, Now: func() time.Time { return time.UnixMilli(1) }}
	// The very first flow stage fails before anything else.
	if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", deps); !errors.Is(err, boom) {
		t.Fatalf("accounts stage transport error: %v", err)
	}
	// Later stages fail on their own transport calls once the earlier answers
	// are scripted to succeed.
	stageSteps := [][]SSODeviceResponse{
		{
			ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
			{StatusCode: http.StatusOK},
			{StatusCode: http.StatusOK},
			{StatusCode: http.StatusOK},
			{StatusCode: http.StatusOK},
		},
	}
	for _, steps := range stageSteps {
		device := &wcScriptedDevice{steps: steps}
		if _, err := convertGrokSSOToOAuth(context.Background(), "sso-token", SSODeviceDeps{
			Request: device, Sleep: func(context.Context, time.Duration) error { return nil },
			Now: func() time.Time { return time.UnixMilli(1) },
		}); err == nil {
			t.Fatal("later stage transport failure must fail the flow")
		}
	}
}

func TestW14dRotationCASConflictAtStore(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-cas", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	adminID := env.w14dLoginAdmin(t)

	// The store-level CAS: a stale config revision renders the revision
	// conflict error even when the caller believes the row is fresh.
	_, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                         "w14d-cas",
		ExpectedConfigRevision:            7,
		ExpectedProviderCode:              "gpt",
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		Credentials:            map[string]any{"access_token": "new"},
		Access:                 AccessScope{ViewerID: adminID, IsAdmin: true},
	})
	if err == nil || err.Error() != "账户配置版本冲突" {
		t.Fatalf("store CAS conflict: %v", err)
	}
	// A wrong provider code misses the row: the store answers (nil, nil),
	// which the route renders as the 404 arm.
	missing, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                         "w14d-cas",
		ExpectedConfigRevision:            1,
		ExpectedProviderCode:              "anthropic",
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		Credentials:                       map[string]any{"access_token": "new"},
		Access:                            AccessScope{ViewerID: adminID, IsAdmin: true},
	})
	if err != nil || missing != nil {
		t.Fatalf("wrong provider must miss: %v %v", missing, err)
	}
}

func TestW14dCreateServerErrorArms(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Dropping the profile table turns the profile resolve into a 500.
	env.exec(t, `DROP TABLE provider_protocol_profiles`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("profile 500: %d %v", code, payload)
	}
}

func TestW14dCreateGroupServerErrorArm(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Dropping the groups table turns the group check into a 500.
	env.exec(t, `DROP TABLE groups`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","groupId":"grp-x","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("group 500: %d %v", code, payload)
	}
}

func TestW14dSSOImportDuplicateNameFails(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// Pre-create the account name the import would use; the create then fails
	// per item while the response stays 200.
	successSteps := wcDeviceSuccessSteps(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`)
	env.sso.steps = append(append([]SSODeviceResponse{}, successSteps...), successSteps...)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('w14d-sso-dup', (SELECT id FROM system_accounts WHERE username='root'), 'xai', 'profile_xai_openai_v1',
		'openai', 'v1', 'w14d-sso-name', 'oauth', 'active', '{}', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["tok-one"],"name":"w14d-sso-name","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusOK {
		t.Fatalf("duplicate import: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["createdCount"] != float64(0) {
		t.Fatalf("created: %v", data)
	}
}
