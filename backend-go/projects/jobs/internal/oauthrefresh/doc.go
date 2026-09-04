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
//     the account is mutated.
//   - keepalive window: anthropic 60s, gemini 60s, grok 300s
//     (oauth-dispatch-preparation lead constants) with a 3-attempt
//     config-revision CAS retry per refresh.
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
