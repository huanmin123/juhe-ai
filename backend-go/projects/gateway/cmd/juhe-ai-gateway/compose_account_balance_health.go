package main

// Port of the archived Node production hotfix "accountBalanceGoOwnerHealth
// ownerMode blue/green semantics" (migration-backup/node/final-archive/
// backend/src/modules/system-api/system-api-app.ts). The DB-service health
// endpoint resolves the account-balance dependency against the Go jobs /health
// endpoint, and blue-green standby slots are judged by reachability plus the
// peer's ownerMode instead of the owner-only readiness flags.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// accountBalanceJobsOwnerEnv / accountBalanceJobsHTTPURLEnv keep the archived
// Node env names verbatim (JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER is read by
// accountBalanceGoOwnerEnabled; the manual-bridge origin doubles as the health
// origin exactly like Node `new URL('/health', endpoint)`).
const (
	accountBalanceJobsOwnerEnv   = "JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER"
	accountBalanceJobsHTTPURLEnv = "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL"
	accountBalanceHealthTimeout  = 2 * time.Second
)

// accountBalanceDependencyHealth mirrors the archived Node
// SystemApiDependencyHealth: projectorReady only appears on the Go-owner
// branches and ownerMode only appears for standby/drain (active omits the
// field), so the pointer + omitempty combination preserves the exact JSON
// shape including explicit `false` values.
type accountBalanceDependencyHealth struct {
	Enabled        bool   `json:"enabled"`
	Ready          bool   `json:"ready"`
	ProjectorReady *bool  `json:"projectorReady,omitempty"`
	OwnerMode      string `json:"ownerMode,omitempty"`
}

// accountBalanceHealthDeps carries the injectable seams: the projection
// readiness gate (Node accountBalanceJobsOutcomeProjectionRuntimeReady) and
// the HTTP transport used to probe the Go jobs /health endpoint. Tests inject
// both; production keeps the defaults.
type accountBalanceHealthDeps struct {
	ProjectorReady func() bool
	Fetch          func(*http.Request) (*http.Response, error)
}

// accountBalanceGoOwnerEnabled mirrors the archived Node
// account-balance-handover.accountBalanceGoOwnerEnabled: the gateway reports
// the account-balance dependency only when the J2 owner handover names Go.
func accountBalanceGoOwnerEnabled(getenv func(string) string) bool {
	return strings.ToLower(strings.TrimSpace(getenv(accountBalanceJobsOwnerEnv))) == "go"
}

// resolveAccountBalanceOwnerMode keeps the archived Node parse semantics
// verbatim: a trimmed JUHE_AI_BLUE_GREEN_OWNER_MODE of exactly "standby" or
// "drain" selects that mode, and every other value (empty, unknown, wrong
// case) falls back to active — unlike the fail-closed process-level
// ownermode.Load gate in main.go.
func resolveAccountBalanceOwnerMode(getenv func(string) string) ownermode.Mode {
	configured := strings.TrimSpace(getenv(ownermode.EnvironmentKey))
	switch ownermode.Mode(configured) {
	case ownermode.Standby, ownermode.Drain:
		return ownermode.Mode(configured)
	}
	return ownermode.Active
}

// accountBalanceGoOwnerHealth ports the archived Node hotfix one-to-one:
//
//   - non-Go owner: {enabled:false, ready:true} with no other fields;
//   - missing endpoint or cold projector: ready=false without probing jobs,
//     still reporting ownerMode for standby/drain;
//   - standby: ready requires only HTTP reachability, peer ownerMode=standby
//     and a fresh projector (the standby slot holds no J2 owner work);
//   - active/drain: ready keeps the original owner flags contract
//     (ready && accountBalanceEnabled && accountBalanceReady).
func accountBalanceGoOwnerHealth(getenv func(string) string, deps accountBalanceHealthDeps) accountBalanceDependencyHealth {
	if !accountBalanceGoOwnerEnabled(getenv) {
		return accountBalanceDependencyHealth{Enabled: false, Ready: true}
	}
	endpoint := strings.TrimSpace(getenv(accountBalanceJobsHTTPURLEnv))
	ownerMode := resolveAccountBalanceOwnerMode(getenv)
	projectorReady := deps.ProjectorReady
	if projectorReady == nil {
		// The Go gateway has no in-process projection bridge (the archived
		// Node accountBalanceJobsOutcomeProjectionRuntimeReady reader); the
		// outcome freshness rides the shared DB reads, so the default gate is
		// always ready until a Go-side runtime lands.
		projectorReady = func() bool { return true }
	}
	projectorReadyNow := projectorReady()
	health := accountBalanceDependencyHealth{
		Enabled:        true,
		Ready:          false,
		ProjectorReady: &projectorReadyNow,
	}
	if ownerMode != ownermode.Active {
		health.OwnerMode = string(ownerMode)
	}
	notReady := func() accountBalanceDependencyHealth { return health }
	if endpoint == "" || !projectorReadyNow {
		return notReady()
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return notReady()
	}
	healthURL := *base
	healthURL.Path = "/health"
	healthURL.RawQuery = ""
	healthURL.Fragment = ""
	request, err := http.NewRequest(http.MethodGet, healthURL.String(), nil)
	if err != nil {
		return notReady()
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountBalanceHealthTimeout)
	defer cancel()
	fetch := deps.Fetch
	if fetch == nil {
		fetch = http.DefaultClient.Do
	}
	response, err := fetch(request.WithContext(ctx))
	if err != nil {
		return notReady()
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return notReady()
	}
	ok := response.StatusCode >= 200 && response.StatusCode < 300
	ready := false
	if ok {
		if ownerMode == ownermode.Standby {
			peerMode, _ := payload["ownerMode"].(string)
			ready = peerMode == string(ownermode.Standby) && projectorReadyNow
		} else {
			peerReady, _ := payload["ready"].(bool)
			peerEnabled, _ := payload["accountBalanceEnabled"].(bool)
			peerBalanceReady, _ := payload["accountBalanceReady"].(bool)
			ready = peerReady && peerEnabled && peerBalanceReady
		}
	}
	health.Ready = ready
	return health
}

// accountBalanceSystemHealthStatus mirrors the archived Node
// resolveSystemApiHealth: the DB-service health answer stays 200 and degrades
// only inside the body (`status: "degraded"`) so a transient J2 dependency
// never turns the gateway process readiness into a 503.
func accountBalanceSystemHealthStatus(ready bool) string {
	if ready {
		return "ok"
	}
	return "degraded"
}
