package oauthrefresh

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock freezes time for deterministic due-window and backoff asserts.
type fixedClock struct{ current time.Time }

func (c *fixedClock) Now() time.Time { return c.current }

func defaultNow() time.Time { return time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC) }

func expiresInMillis(milliseconds int64) string {
	return isoMillis(defaultNow().Add(time.Duration(milliseconds) * time.Millisecond))
}

func openAICredentials(expiresAt string) map[string]any {
	return map[string]any{
		"access_token":  "at-current",
		"refresh_token": "rt-current",
		"expires_at":    expiresAt,
		"client_id":     OpenAIOAuthClientID,
		"base_url":      "https://api.openai.com/v1",
	}
}

func newRefreshJobForTest(t *testing.T) (*RefreshJob, *Store, *sql.DB, *fixedClock, *recordingExchanger) {
	t.Helper()
	store, db, _ := newTestStore(t)
	seedProviderProfiles(t, db)
	clock := &fixedClock{current: defaultNow()}
	exchanger := &recordingExchanger{}
	job := NewRefreshJob(store, exchanger, WithClock(clock))
	job.store = store.WithClock(func() time.Time { return clock.Now() })
	return job, store, db, clock, exchanger
}

func TestRefreshJobSuccessGolden(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	// Expires in 60s (< lead 300s) → due.
	seedOpenAIOAuthAccount(t, db, "acc-1", openAICredentials(expiresInMillis(60_000)), clock.Now())
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","id_token":"","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertRefreshResult(t, result, map[string]int{"Scanned": 1, "Due": 1, "Refreshed": 1, "Started": 1})

	status, _, revision, oauthExpiresAt, refreshPresent := readAccountRow(t, db, "acc-1")
	if status != "active" || revision != 2 {
		t.Fatalf("status=%q revision=%d", status, revision)
	}
	if oauthExpiresAt.String != expiresInMillis(3_600_000) {
		t.Fatalf("derived oauth expires=%v", oauthExpiresAt)
	}
	if refreshPresent != 1 {
		t.Fatalf("refresh present=%d", refreshPresent)
	}
	credentials := readAccountCredentials(t, db, "acc-1")
	if credentials["access_token"] != "at-new" || credentials["refresh_token"] != "rt-new" || credentials["expires_at"] != expiresInMillis(3_600_000) {
		t.Fatalf("credentials=%v", credentials)
	}
}

func TestRefreshJobNotDueSkipped(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	// Expires in 400s (> lead 300s) → not a candidate.
	seedOpenAIOAuthAccount(t, db, "acc-1", openAICredentials(expiresInMillis(400_000)), clock.Now())
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 || result.Refreshed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 0 {
		t.Fatal("upstream must not be called")
	}
	// Boundary: exactly at the lead window (300s) counts as due.
	seedOpenAIOAuthAccount(t, db, "acc-2", openAICredentials(expiresInMillis(300_000)), clock.Now())
	result, err = job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Refreshed != 1 {
		t.Fatalf("boundary result=%+v", result)
	}
}

func TestRefreshJobStoppedAccountExcluded(t *testing.T) {
	job, _, db, clock, _ := newRefreshJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{
		ID: "acc-stopped", ProviderCode: "gpt", ProfileID: ProfileGPTOpenAIV1, Type: "oauth",
		Status: "error", LastErrorCode: OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode,
		Credentials: openAICredentials(expiresInMillis(0)), Now: clock.Now(),
	})
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The stopped account is filtered by the status guard but still listed by
	// SQL (status <> 'error' OR last_error_code <> stopped) → the guard keeps
	// it out only when the code matches; the stopped row carries the stopped
	// code, so SQL itself excludes it and the result stays empty.
	if result.Scanned != 0 || result.Failed != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRefreshJobTableDriven(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, db *sql.DB, clock *fixedClock)
		respond func(call int, request TokenHTTPRequest) (TokenHTTPResponse, error)
		runs    int
		// restart mirrors a process kill/restart between runs: a fresh job
		// (and in-memory failure store) while the DB state carries over.
		restart bool
		// options mutates the per-run options (backoff=0 for the local
		// failure threshold paths).
		options func(o *RefreshOptions)
		assert  func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger)
	}{
		{
			name: "401 upstream failure backs off without terminal state",
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				seedOpenAIOAuthAccount(t, db, "acc-401", openAICredentials(expiresInMillis(0)), clock.Now())
			},
			respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
				return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant"}`}, nil
			},
			runs: 4,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				for index, result := range results {
					want := 1
					if index > 0 {
						want = 0 // backoff keeps the account out of the batch
					}
					if result.Failed != want {
						t.Fatalf("run %d failed=%d want %d (%+v)", index, result.Failed, want, result)
					}
					if index > 0 && result.SkippedBackoff != 1 {
						t.Fatalf("run %d skippedBackoff=%d (%+v)", index, result.SkippedBackoff, result)
					}
				}
				status, lastError, _, _, _ := readAccountRow(t, db, "acc-401")
				if status != "active" || lastError != "" {
					t.Fatalf("upstream failures must not stop the account: %q %q", status, lastError)
				}
			},
		},
		{
			name: "429 rate limit failure recorded as runtime failure",
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				seedOpenAIOAuthAccount(t, db, "acc-429", openAICredentials(expiresInMillis(0)), clock.Now())
			},
			respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
				return TokenHTTPResponse{StatusCode: 429, Body: `{"error":"rate_limit_exceeded"}`}, nil
			},
			runs: 1,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				if results[0].Failed != 1 {
					t.Fatalf("result=%+v", results[0])
				}
			},
		},
		{
			name: "network failure surfaces through failed counter",
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				seedOpenAIOAuthAccount(t, db, "acc-net", openAICredentials(expiresInMillis(0)), clock.Now())
			},
			respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
				return TokenHTTPResponse{}, errors.New("dial tcp: i/o timeout")
			},
			runs: 1,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				if results[0].Failed != 1 || results[0].Exceptioned != 0 {
					t.Fatalf("result=%+v", results[0])
				}
			},
		},
		{
			name:    "missing refresh token stops the account after 3 local failures",
			options: func(o *RefreshOptions) { o.RetryBackoffSeconds = intPtr(0) },
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				seedOpenAIOAuthAccount(t, db, "acc-local", map[string]any{
					"access_token": "at-x",
					"expires_at":   expiresInMillis(0),
				}, clock.Now())
			},
			runs: 3,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				total := 0
				for _, result := range results {
					total += result.Exceptioned
				}
				if total != 1 {
					t.Fatalf("exceptioned total=%d results=%+v", total, results)
				}
				status, lastError, _, _, _ := readAccountRow(t, db, "acc-local")
				if status != "error" || lastError != OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode {
					t.Fatalf("status=%q code=%q", status, lastError)
				}
				var reason string
				if err := db.QueryRow(`SELECT last_error_message FROM accounts WHERE id = ?`, "acc-local").Scan(&reason); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(reason, "OpenAI OAuth 访问令牌连续 3 次因本地配置错误无法启动刷新，已停止自动刷新。") {
					t.Fatalf("reason=%q", reason)
				}
				if !strings.Contains(reason, "最后本地错误：OpenAI OAuth 账户缺少刷新令牌") {
					t.Fatalf("reason=%q", reason)
				}
			},
		},
		{
			name: "decrypt failure without a decryptable sibling stays unclassified",
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				// Sealed with a foreign secret → decrypt failure at read time.
				foreign, err := EncryptJSON("other-secret", openAICredentials(expiresInMillis(0)))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, name, type, status,
					credentials_encrypted, config_revision, updated_at)
					VALUES ('acc-decrypt', 'gpt', 'profile_gpt_openai_v1', '账户-decrypt', 'oauth', 'active', ?, 1, ?)`,
					foreign, isoMillis(clock.Now())); err != nil {
					t.Fatal(err)
				}
			},
			runs: 1,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				// No sibling decrypted OK → the batch cannot prove the keyring is
				// healthy, so the failure must not count toward the local
				// configuration terminal state.
				if results[0].Failed != 1 || results[0].Exceptioned != 0 {
					t.Fatalf("result=%+v", results[0])
				}
			},
		},
		{
			name:    "decrypt failure with a decryptable sibling counts local",
			options: func(o *RefreshOptions) { o.RetryBackoffSeconds = intPtr(0) },
			// The sibling's own refresh fails (401) so its stored credentials
			// stay due and decryptable across runs, keeping the local keyring
			// evidence alive for the decrypt-failure row.
			respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
				return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant"}`}, nil
			},
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				// The sibling must itself be due so it lands in the listed batch
				// and proves the keyring can decrypt this pool.
				seedOpenAIOAuthAccount(t, db, "acc-fresh-sibling", openAICredentials(expiresInMillis(0)), clock.Now())
				foreign, err := EncryptJSON("other-secret", openAICredentials(expiresInMillis(0)))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO accounts (id, provider_code, provider_protocol_profile_id, name, type, status,
					credentials_encrypted, config_revision, updated_at)
					VALUES ('acc-decrypt-sibling', 'gpt', 'profile_gpt_openai_v1', '账户-decrypt-sibling', 'oauth', 'active', ?, 1, ?)`,
					foreign, isoMillis(clock.Now())); err != nil {
					t.Fatal(err)
				}
			},
			runs: 3,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				total := 0
				for _, result := range results {
					total += result.Exceptioned
				}
				if total != 1 {
					t.Fatalf("exceptioned total=%d results=%+v", total, results)
				}
				status, lastError, _, _, _ := readAccountRow(t, db, "acc-decrypt-sibling")
				if status != "error" || lastError != OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode {
					t.Fatalf("status=%q code=%q", status, lastError)
				}
			},
		},
		{
			name:    "kill-restart idempotency: fresh job instance resumes backoff safely",
			restart: true,
			seed: func(t *testing.T, db *sql.DB, clock *fixedClock) {
				seedOpenAIOAuthAccount(t, db, "acc-restart", openAICredentials(expiresInMillis(0)), clock.Now())
			},
			respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
				return TokenHTTPResponse{StatusCode: 500, Body: `{"error":"server_error"}`}, nil
			},
			runs: 2,
			assert: func(t *testing.T, results []RefreshResult, db *sql.DB, exchanger *recordingExchanger) {
				// Run 1 fails; run 2 (new job, empty in-memory failure store) must
				// hit the upstream again — the refresh itself is idempotent
				// because persistence is revision-CAS based.
				if results[0].Failed != 1 || results[1].Failed != 1 {
					t.Fatalf("results=%+v", results)
				}
				if exchanger.callCount() < 2 {
					t.Fatalf("upstream calls=%d", exchanger.callCount())
				}
				_, _, revision, _, _ := readAccountRow(t, db, "acc-restart")
				if revision != 1 {
					t.Fatalf("failed refreshes must not bump config_revision: %d", revision)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			job, _, db, clock, exchanger := newRefreshJobForTest(t)
			testCase.seed(t, db, clock)
			if testCase.respond != nil {
				exchanger.respond = testCase.respond
			}
			results := make([]RefreshResult, 0, testCase.runs)
			runOptions := RefreshOptions{}
			if testCase.options != nil {
				testCase.options(&runOptions)
			}
			for run := 0; run < testCase.runs; run++ {
				if testCase.restart {
					// Restart semantics: a fresh job per run; the DB state
					// carries over while the in-memory failure store resets.
					job = NewRefreshJob(job.store, exchanger, WithClock(clock))
				}
				result, err := job.RunOnce(context.Background(), runOptions)
				if err != nil {
					t.Fatal(err)
				}
				results = append(results, result)
			}
			testCase.assert(t, results, db, exchanger)
		})
	}
}

func TestRefreshJobRestoreAfterRecovery(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedAccountRow(t, db, accountRowSeed{
		ID: "acc-restore", ProviderCode: "gpt", ProfileID: ProfileGPTOpenAIV1, Type: "oauth",
		Status: "error", LastErrorCode: OpenAIOAuthTokenRefreshFailedErrorCode,
		Credentials: openAICredentials(expiresInMillis(0)), Now: clock.Now(),
	})
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 || result.Cooldowned != 1 {
		t.Fatalf("result=%+v", result)
	}
	status, lastError, _, _, _ := readAccountRow(t, db, "acc-restore")
	if status != "active" || lastError != "" {
		t.Fatalf("restored account=%q %q", status, lastError)
	}
}

func TestRefreshJobRaceRecoveryKeepsFreshCredentials(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	seedOpenAIOAuthAccount(t, db, "acc-race", openAICredentials(expiresInMillis(0)), clock.Now())
	// On the upstream call, an out-of-band writer completes a refresh of the
	// same account (fresh credentials + bumped revision). The job's own CAS
	// then conflicts; the race recovery must adopt the fresh credentials.
	exchanger.respond = func(call int, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		if call == 1 {
			fresh := openAICredentials(expiresInMillis(400_000))
			fresh["access_token"] = "at-other-writer"
			sealed, err := EncryptJSON(cryptoTestSecret, fresh)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE accounts SET config_revision = config_revision + 1,
				credentials_encrypted = ?, oauth_access_token_expires_at = ? WHERE id = 'acc-race'`,
				sealed, expiresInMillis(400_000)); err != nil {
				t.Fatal(err)
			}
		}
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-job","refresh_token":"rt-current","expires_in":3600}`}, nil
	}
	_ = clock
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 || result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	credentials := readAccountCredentials(t, db, "acc-race")
	if credentials["access_token"] != "at-other-writer" {
		t.Fatalf("fresh credentials must win: %v", credentials)
	}
	_, _, revision, _, _ := readAccountRow(t, db, "acc-race")
	if revision != 2 {
		t.Fatalf("revision=%d", revision)
	}
}

func TestRotateCredentialsNoopReceiptKeepsRevision(t *testing.T) {
	store, db, _ := newTestStore(t)
	now := defaultNow()
	seedOpenAIOAuthAccount(t, db, "acc-noop", openAICredentials(expiresInMillis(3_600_000)), now)
	account, err := store.FindRotationAccount(context.Background(), "acc-noop")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	first, err := store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID: "acc-noop", ExpectedConfigRevision: account.ConfigRevision,
		ExpectedProviderCode: ProviderGPT, ExpectedAccountType: AccountTypeOAuth,
		ExpectedProviderProtocolProfileID: ProfileGPTOpenAIV1,
		Credentials:                       account.Credentials,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Changed {
		t.Fatalf("identical credentials reported Changed: %+v", first)
	}
	_, _, revision, _, _ := readAccountRow(t, db, "acc-noop")
	if revision != 1 {
		t.Fatalf("no-op receipt must keep config_revision: %d", revision)
	}
}

func intPtr(value int) *int { return &value }

func assertRefreshResult(t *testing.T, got RefreshResult, want map[string]int) {
	t.Helper()
	value := map[string]int{
		"Scanned": got.Scanned, "Due": got.Due, "Refreshed": got.Refreshed,
		"Failed": got.Failed, "Exceptioned": got.Exceptioned, "Cooldowned": got.Cooldowned,
		"SkippedBackoff": got.SkippedBackoff, "Started": got.Started,
		"SkippedLocked": got.SkippedLocked, "DeferredBudget": got.DeferredBudget,
	}
	for key, wantValue := range want {
		if value[key] != wantValue {
			t.Fatalf("%s=%d want %d (%+v)", key, value[key], wantValue, got)
		}
	}
}
