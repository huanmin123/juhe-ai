package oauthrefresh

import (
	"context"
	"database/sql"
	"testing"
)

// newKeepaliveJobForTest shares the refresh fixture but wires the keepalive
// job over the same store/clock/exchanger.
func newKeepaliveJobForTest(t *testing.T) (*KeepaliveJob, *sql.DB, *fixedClock, *recordingExchanger) {
	t.Helper()
	_, store, db, clock, exchanger := newRefreshJobForTest(t)
	job := NewKeepaliveJob(store, exchanger, WithKeepaliveClock(clock))
	return job, db, clock, exchanger
}

func TestKeepaliveDueWindowPerProvider(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	// anthropic: 60s lead — 59s is due, 61s is not.
	seedAccountRow(t, db, accountRowSeed{ID: "an-59", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(59_000)}, Now: clock.Now()})
	seedAccountRow(t, db, accountRowSeed{ID: "an-61", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(61_000)}, Now: clock.Now()})
	// grok: 300s lead — 299s is due, 301s is not.
	seedAccountRow(t, db, accountRowSeed{ID: "xai-299", ProviderCode: "xai", ProfileID: ProfileXAIOpenAIV1, Type: "oauth",
		Credentials: map[string]any{"access_token": "at-x", "refresh_token": "rt-x", "expires_at": expiresInMillis(299_000)}, Now: clock.Now()})
	seedAccountRow(t, db, accountRowSeed{ID: "xai-301", ProviderCode: "xai", ProfileID: ProfileXAIOpenAIV1, Type: "oauth",
		Credentials: map[string]any{"access_token": "at-x", "refresh_token": "rt-x", "expires_at": expiresInMillis(301_000)}, Now: clock.Now()})
	// grok with the wrong profile pin is invisible to the keepalive query.
	seedAccountRow(t, db, accountRowSeed{ID: "xai-wrong-profile", ProviderCode: "xai", ProfileID: "profile_gpt_openai_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-x", "refresh_token": "rt-x", "expires_at": expiresInMillis(0)}, Now: clock.Now()})

	plans := KeepalivePlans()
	anthropicPlan, grokPlan := plans[0], plans[2]

	anthropicResult, err := job.RunOnce(context.Background(), anthropicPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if anthropicResult.Scanned != 1 || anthropicResult.Due != 1 || anthropicResult.Refreshed != 1 {
		t.Fatalf("anthropic=%+v", anthropicResult)
	}
	grokResult, err := job.RunOnce(context.Background(), grokPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if grokResult.Scanned != 1 || grokResult.Refreshed != 1 {
		t.Fatalf("grok=%+v", grokResult)
	}

	// Boundary: exactly at the lead window counts as due (<= lead).
	seedAccountRow(t, db, accountRowSeed{ID: "an-60", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(60_000)}, Now: clock.Now()})
	result, err := job.RunOnce(context.Background(), anthropicPlan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Refreshed != 1 {
		t.Fatalf("boundary=%+v", result)
	}
}

func TestKeepalivePreservesStoredBaseURL(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-base", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{
			"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0),
			"base_url": "https://anthropic-proxy.example", "client_id": "client-kept",
		}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600,
			"account":{"email_address":"a@b.c","uuid":"acc-uuid"}}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-base")
	// The operator-managed base_url survives the merge.
	if credentials["base_url"] != "https://anthropic-proxy.example" {
		t.Fatalf("base_url=%v", credentials["base_url"])
	}
	// The stored client id feeds the refresh request and the token builder
	// echoes the token info's client id (Node dispatch parity).
	if credentials["client_id"] != "client-kept" {
		t.Fatalf("client_id=%v", credentials["client_id"])
	}
	_, _, revision, _, _ := readAccountRow(t, db, "an-base")
	if revision != 2 {
		t.Fatalf("revision=%d", revision)
	}
}

func TestKeepaliveGeminiAccountTypeAndMerge(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "gm-1", ProviderCode: "gemini", ProfileID: "profile_gemini_native_v1beta", Type: "google_oauth",
		Credentials: map[string]any{
			"access_token": "at-g", "refresh_token": "rt-g", "expires_at": expiresInMillis(0),
			"oauth_type": "ai_studio", "client_id": "cid", "client_secret": "sec",
			"base_url": "https://gemini-proxy.example", "project_id": "proj",
		}, Now: clock.Now()})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-g2","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[1], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "gm-1")
	if credentials["base_url"] != "https://gemini-proxy.example" || credentials["access_token"] != "at-g2" || credentials["oauth_type"] != "ai_studio" {
		t.Fatalf("credentials=%v", credentials)
	}
	// A missing rotated refresh token keeps the stored one.
	if credentials["refresh_token"] != "rt-g" {
		t.Fatalf("fallback refresh token lost: %v", credentials["refresh_token"])
	}
}

func TestKeepaliveMissingRefreshTokenDoesNotWriteState(t *testing.T) {
	job, db, clock, _ := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-nort", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// Corrupt the row into the flag/credential inconsistency the dispatch
	// guard defends against: derived present flag stays 1, the decrypted
	// credentials lost their refresh token.
	sealed, err := EncryptJSON(cryptoTestSecret, map[string]any{"access_token": "at-a", "expires_at": expiresInMillis(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = 'an-nort'`, sealed); err != nil {
		t.Fatal(err)
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	// The keepalive family owns no terminal state writes; the failure is
	// reported to the scheduler log only.
	var lastMessage string
	if err := db.QueryRow(`SELECT last_error_message FROM accounts WHERE id = 'an-nort'`).Scan(&lastMessage); err == nil && lastMessage != "" {
		t.Fatalf("keepalive must not write terminal state: %q", lastMessage)
	}
}

func TestKeepaliveConflictAdoptsFreshConcurrentRefresh(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-race", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// A concurrent writer completes a refresh before our persist: the first
	// CAS conflicts and the re-read finds fresh credentials, so the keepalive
	// must report "skipped fresh" instead of writing.
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	persistCalls := 0
	job.updateCredentials = func(ctx context.Context, accountID string, credentials map[string]any, expected int64) (*RotationAccount, error) {
		persistCalls++
		// The concurrent writer rotated the refresh token too, which is the
		// Node adopt-fresh precondition.
		fresh := map[string]any{"access_token": "at-concurrent", "refresh_token": "rt-concurrent", "expires_at": expiresInMillis(400_000)}
		sealed, err := EncryptJSON(cryptoTestSecret, fresh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET config_revision = config_revision + 3,
			credentials_encrypted = ?, oauth_access_token_expires_at = ? WHERE id = 'an-race'`,
			sealed, expiresInMillis(400_000)); err != nil {
			t.Fatal(err)
		}
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 0 || result.Failed != 0 || result.SkippedFresh != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-race")
	if credentials["access_token"] != "at-concurrent" || credentials["refresh_token"] != "rt-concurrent" {
		t.Fatalf("concurrent credentials must stay: %v", credentials)
	}
	if persistCalls != 1 {
		t.Fatalf("persist calls=%d", persistCalls)
	}
}

func TestKeepaliveConflictExhaustionFails(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{ID: "an-exhaust", ProviderCode: "anthropic", ProfileID: "profile_anthropic_anthropic_v1", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	// The concurrent writer keeps winning the CAS; after 3 attempts the
	// keepalive gives up with the conflict and the stored credentials stay.
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a","expires_in":3600}`}, nil
	}
	job.updateCredentials = func(ctx context.Context, accountID string, credentials map[string]any, expected int64) (*RotationAccount, error) {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || result.Refreshed != 0 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "an-exhaust")
	if credentials["access_token"] != "at-a" {
		t.Fatalf("stored credentials must be untouched: %v", credentials)
	}
}
