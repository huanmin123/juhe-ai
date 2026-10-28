// Package oauthrefresh owns the J4 background job family: OpenAI OAuth access
// token refresh, the three-vendor keepalive refresh (anthropic/gemini/grok),
// the resource authorization expiry sweep and the two availability-schedule
// status syncs (api keys + accounts).
//
// The slice mirrors backend/src/modules/openai-oauth/
// openai-oauth-access-token-refresh.service.ts, the per-provider refresh
// services under backend/src/modules/{openai,anthropic,gemini,grok}-oauth, the
// on-dispatch refresh conditions in backend/src/modules/providers/drivers/
// {anthropic,gemini,xai}/oauth-dispatch-preparation.ts, the sweep in
// backend/src/storage/resource-authorization-write(-state).repository.ts and
// the two syncs in backend/src/storage/{api-key-schedule,account-availability-
// schedule}-status-sync.repository.ts.
//
// Semantics contract (byte-for-byte where observable):
//   - refresh trigger: oauth_access_token_expires_at IS NULL OR <= now + lead
//     (lead default 300s, clamped 60..86400), refresh_token missing → local
//     configuration failure, threshold 3 consecutive local failures + active
//     status → terminal error with lastErrorCode
//     oauth_token_refresh_local_configuration_invalid.
//   - backoff: failed refreshes record a backoffUntil = now +
//     oauthAccessTokenRefreshRetryBackoffSeconds (default 300s, 0..86400);
//     backoff state is guarded by the account config_revision and dropped when
//     the account is mutated. Accounts already terminal (status=error with a
//     managed refresh error code) use a 24h backoff instead (2026-10-02:
//     dead refresh tokens otherwise retry ~288x/day forever); upstream
//     responses never mark an account terminal (untrusted in relay
//     environments) — only local-configuration failures do.
//   - keepalive window: anthropic 60s, gemini 60s, grok 300s
//     (oauth-dispatch-preparation lead constants) with a 3-attempt
//     config-revision CAS retry per refresh. Keepalive failure bookkeeping
//     mirrors the OpenAI refresh family exactly (T65-③, 2026-10-28): both
//     failure classes — IsLocalConfigurationError (missing refresh token,
//     unusable proxy, missing source row) and everything else including
//     upstream responses — record a default 300s backoff in the failure state
//     store; 3 applied local-configuration failures on an active account write
//     the shared terminal code oauth_token_refresh_local_configuration_invalid
//     and then clear the failure state (the due scan excludes terminal
//     accounts, so the family stops refreshing); a successful refresh clears
//     the recorded state and restores an account stopped by a managed refresh
//     error code; accounts with a live backoff are filtered out of the batch
//     (SkippedBackoff) and an unreadable failure state skips the account for
//     the round. Upstream failures never mark a keepalive account terminal.
//     Redis failure-state keys use a family-isolated prefix
//     (juhe-ai:state:oauth-keepalive-failure:{hash}) while the OpenAI family
//     keeps its historical openai-oauth-refresh-failure prefix byte-for-byte,
//     so existing records stay readable and the two families never alias.
//   - sweep threshold: grants with status active/paused and expires_at <= now
//     become expired, batch 20, ordered expires_at/updated_at/id.
//   - sync semantics: due schedule rows are evaluated for boundary events,
//     applied exactly once through the *_schedule_status_events tables.
//
// Upstream token endpoints are reached only through the injected TokenExchanger
// and the credential envelope is the Node-compatible AES v1 format, so rows
// written by Node stay refreshable here and vice versa. The packages
// gateway/internal/oauthmgmt (M17) and this package intentionally duplicate the
// refresh protocol helpers: the jobs and gateway Go modules must not import
// each other.
package oauthrefresh
