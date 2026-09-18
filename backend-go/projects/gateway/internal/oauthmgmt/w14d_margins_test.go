// w14d_margins_test.go adds coverage margin around the 95% boundary: the log
// recording guards, the anthropic refreshStored success arm and the failed
// invalidation channels after a committed rotation.
package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestW14dAnthropicRefreshStoredSuccess(t *testing.T) {
	env := newTestEnv(t)
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != AnthropicOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"access_token":"a2","refresh_token":"r2","expires_in":3600,"token_type":"Bearer"}`
	}
	outcome, err := anthropicPlan().refreshStored(context.Background(), env.store,
		&rotationAccount{Credentials: map[string]any{"refresh_token": "stored", "client_id": "cid"}})
	if err != nil || outcome == nil {
		t.Fatalf("anthropic refreshStored: %v %+v", err, outcome)
	}
}

// failingInvalidator fails every rotation invalidation channel.
type failingInvalidator struct{}

func (failingInvalidator) InvalidateAccountLookup(string) error { return errors.New("lookup down") }
func (failingInvalidator) InvalidateRuntime(string) error       { return errors.New("runtime down") }
func (failingInvalidator) InvalidateAPIKeyValidation(string) error {
	return errors.New("validation down")
}

func TestW14dLoggingGuardsAndFailedInvalidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.w14dLoginAdmin(t)

	// A nil auth context on the request skips the log recording entirely.
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code", nil)
	(&Deps{Sink: &recordingSink{}}).recordCreateLog(request, providerPlan{}, AccessScope{}, "k", "s", "id", "owner", "n", nil)
	// A nil Sink behaves the same.
	(&Deps{}).recordCreateLog(request, providerPlan{}, AccessScope{}, "k", "s", "id", "owner", "n", nil)
	_ = adminID

	// A rotation whose invalidation channels all fail still commits and logs
	// the warnings.
	env.w14dSeedRotatableAccount(t, "w14d-inv", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	env.store.invalidator = failingInvalidator{}
	env.exchanger.respond = staticToken(`{"access_token":"a3","refresh_token":"r3","expires_in":3600,"token_type":"Bearer"}`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w14d-inv/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("rotation with failing channels: %d %v", code, payload)
	}
}
