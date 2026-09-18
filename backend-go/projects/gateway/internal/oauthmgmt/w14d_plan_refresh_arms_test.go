// w14d_plan_refresh_arms_test.go pins the remaining per-plan refresh arms:
// the upstream failure path of refreshInput for every provider, the
// credentials-fallback picks of the gemini refresh and the openai/grok
// success builds.
package oauthmgmt

import (
	"context"
	"net/http"
	"testing"
)

func TestW14dPlanRefreshInputUpstreamFailureArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		return http.StatusInternalServerError, `{"error":"down"}`
	}

	for _, plan := range providerPlans() {
		current := &rotationAccount{Credentials: map[string]any{
			"refresh_token": "stored", "client_id": "cid", "client_secret": "secret",
			"project_id": "proj", "tier_id": "tier", "quota_project_id": "quota",
			"base_url": "https://custom", "scope": "s", "oauth_type": "code_assist",
		}}
		if _, err := plan.refreshInput(ctx, env.store, map[string]any{"refreshToken": "rt"}, current); err == nil {
			t.Fatalf("%s refreshInput upstream failure must surface", plan.slug)
		}
	}
	// The openai refreshInput prefers the body clientId over the credential.
	env.exchanger.respond = staticToken(`{"access_token":"a","refresh_token":"r","expires_in":60,"token_type":"Bearer"}`)
	outcome, err := openAIPlan().refreshInput(ctx, env.store,
		map[string]any{"refreshToken": "rt", "clientId": "body-cid"},
		&rotationAccount{Credentials: map[string]any{"client_id": "cred-cid"}})
	if err != nil || outcome == nil {
		t.Fatalf("openai refreshInput success: %v %+v", err, outcome)
	}
	// The grok refreshStored builds credentials without a refresh token echo.
	grokOutcome, err := grokPlan().refreshStored(ctx, env.store,
		&rotationAccount{Credentials: map[string]any{"refresh_token": "stored", "client_id": "cid"}})
	if err != nil || grokOutcome == nil {
		t.Fatalf("grok refreshStored: %v %+v", err, grokOutcome)
	}
}

func TestW14dGeminiRefreshInputFallbackArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// The gemini exchangeRefresh validates the body fields first.
	if _, err := geminiPlan().exchangeRefresh(ctx, env.store, map[string]any{
		"refreshToken": "rt", "oauthType": "bogus",
	}); err == nil || err.Error() != "oauthType 无效" {
		t.Fatalf("gemini exchangeRefresh validation: %v", err)
	}
	// refreshInput falls back to the stored credentials for missing fields
	// and derives the oauth type from the credentials.
	env.exchanger.respond = staticToken(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`)
	current := &rotationAccount{Credentials: map[string]any{
		"refresh_token": "stored", "client_id": "cid", "client_secret": "sec",
		"project_id": "proj", "tier_id": "pro", "quota_project_id": "quota",
		"base_url": "https://g.example", "scope": "s",
	}}
	credentials, err := geminiPlan().refreshInput(ctx, env.store, map[string]any{"refreshToken": "rt"}, current)
	if err != nil || credentials == nil {
		t.Fatalf("gemini refreshInput fallback: %v %v", credentials, err)
	}
}
