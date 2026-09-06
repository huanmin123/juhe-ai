package main

// Regression matrix for the ported archived Node hotfix
// "accountBalanceGoOwnerHealth ownerMode blue/green semantics". The cases
// mirror the archived regression script
// (migration-backup/node/final-archive/backend/src/scripts/regression/
// account-balance-jobs-health-regression.ts) and extend it with the drain
// row of the ownerMode matrix: active keeps the owner-flag contract,
// standby judges by jobs reachability plus the peer ownerMode, drain keeps
// the owner-flag contract while still reporting ownerMode.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func getenvWith(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func goOwnerEnv(values map[string]string) map[string]string {
	merged := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":    "go",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL": "http://127.0.0.1:3305/account-balance/manual",
	}
	for key, value := range values {
		merged[key] = value
	}
	return merged
}

func mustJSONString(t *testing.T, health accountBalanceDependencyHealth) string {
	t.Helper()
	encoded, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func fetchJSON(status int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
}

func fetchError(*http.Request) (*http.Response, error) {
	return nil, http.ErrHandlerTimeout
}

// TestAccountBalanceGoOwnerHealthDisabled mirrors the archived assertion:
// a non-Go owner must not pull J2 into the DB-service health, and the JSON
// shape carries neither projectorReady nor ownerMode.
func TestAccountBalanceGoOwnerHealthDisabled(t *testing.T) {
	health := accountBalanceGoOwnerHealth(getenvWith(map[string]string{}), accountBalanceHealthDeps{
		ProjectorReady: func() bool { return true },
		Fetch: func(*http.Request) (*http.Response, error) {
			t.Fatal("non-Go owner must not probe the jobs health endpoint")
			return nil, nil
		},
	})
	payload := mustJSONString(t, health)
	if payload != `{"enabled":false,"ready":true}` {
		t.Fatalf("non-Go owner health shape wrong: %s", payload)
	}
}

// TestAccountBalanceGoOwnerHealthProjectorGate mirrors the archived
// assertion: a Go owner with a cold projector must refuse the DB-service
// health without probing the jobs endpoint, and the active mode omits
// ownerMode from the answer.
func TestAccountBalanceGoOwnerHealthProjectorGate(t *testing.T) {
	health := accountBalanceGoOwnerHealth(getenvWith(goOwnerEnv(nil)), accountBalanceHealthDeps{
		ProjectorReady: func() bool { return false },
		Fetch: func(*http.Request) (*http.Response, error) {
			t.Fatal("cold projector must not probe the jobs health endpoint")
			return nil, nil
		},
	})
	payload := mustJSONString(t, health)
	if payload != `{"enabled":true,"ready":false,"projectorReady":false}` {
		t.Fatalf("projector-gated health shape wrong: %s", payload)
	}
}

// TestAccountBalanceGoOwnerHealthMatrix walks the ownerMode x jobs-response
// matrix: the jobs payload shapes come straight from the archived regression
// script, and the drain rows extend it per the archived ternary (only
// standby takes the peer-ownerMode branch).
func TestAccountBalanceGoOwnerHealthMatrix(t *testing.T) {
	readyPayload := `{"ready":true,"accountBalanceEnabled":true,"accountBalanceReady":true}`
	standbyPayload := `{"ready":false,"ownerMode":"standby","accountBalanceEnabled":false,"accountBalanceReady":false}`
	activeOwnerPayload := `{"ready":true,"ownerMode":"active","accountBalanceEnabled":true,"accountBalanceReady":true}`

	cases := []struct {
		name          string
		ownerModeEnv  string
		projectorOK   bool
		status        int
		body          string
		failFetch     bool
		wantReady     bool
		wantOwnerMode string
	}{
		{name: "active ready payload", ownerModeEnv: "", projectorOK: true, status: 200, body: readyPayload, wantReady: true},
		{name: "active peer standby payload keeps owner contract", ownerModeEnv: "", projectorOK: true, status: 200, body: standbyPayload, wantReady: false},
		{name: "active http 500", ownerModeEnv: "", projectorOK: true, status: 500, body: readyPayload, wantReady: false},
		{name: "active invalid json", ownerModeEnv: "", projectorOK: true, status: 200, body: "not-json", wantReady: false},
		{name: "active fetch error", ownerModeEnv: "", projectorOK: true, failFetch: true, wantReady: false},
		{name: "standby peer standby payload ready", ownerModeEnv: "standby", projectorOK: true, status: 200, body: standbyPayload, wantReady: true, wantOwnerMode: "standby"},
		{name: "standby wrong peer ownerMode refuses ready", ownerModeEnv: "standby", projectorOK: true, status: 200, body: activeOwnerPayload, wantReady: false, wantOwnerMode: "standby"},
		{name: "standby ready payload without peer standby stays strict", ownerModeEnv: "standby", projectorOK: true, status: 200, body: readyPayload, wantReady: false, wantOwnerMode: "standby"},
		{name: "standby http 500", ownerModeEnv: "standby", projectorOK: true, status: 500, body: standbyPayload, wantReady: false, wantOwnerMode: "standby"},
		{name: "standby fetch error", ownerModeEnv: "standby", projectorOK: true, failFetch: true, wantReady: false, wantOwnerMode: "standby"},
		{name: "standby cold projector", ownerModeEnv: "standby", projectorOK: false, status: 200, body: standbyPayload, wantReady: false, wantOwnerMode: "standby"},
		{name: "drain keeps owner contract and reports mode", ownerModeEnv: "drain", projectorOK: true, status: 200, body: readyPayload, wantReady: true, wantOwnerMode: "drain"},
		{name: "drain unready payload refuses ready", ownerModeEnv: "drain", projectorOK: true, status: 200, body: standbyPayload, wantReady: false, wantOwnerMode: "drain"},
		{name: "drain peer active ownerMode irrelevant", ownerModeEnv: "drain", projectorOK: true, status: 200, body: activeOwnerPayload, wantReady: true, wantOwnerMode: "drain"},
		{name: "drain fetch error", ownerModeEnv: "drain", projectorOK: true, failFetch: true, wantReady: false, wantOwnerMode: "drain"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deps := accountBalanceHealthDeps{ProjectorReady: func() bool { return testCase.projectorOK }}
			if testCase.failFetch {
				deps.Fetch = fetchError
			} else {
				deps.Fetch = fetchJSON(testCase.status, testCase.body)
			}
			health := accountBalanceGoOwnerHealth(getenvWith(goOwnerEnv(map[string]string{
				"JUHE_AI_BLUE_GREEN_OWNER_MODE": testCase.ownerModeEnv,
			})), deps)
			if health.Ready != testCase.wantReady {
				t.Fatalf("ready=%v want %v (%s)", health.Ready, testCase.wantReady, mustJSONString(t, health))
			}
			if health.OwnerMode != testCase.wantOwnerMode {
				t.Fatalf("ownerMode=%q want %q (%s)", health.OwnerMode, testCase.wantOwnerMode, mustJSONString(t, health))
			}
			if health.ProjectorReady == nil || *health.ProjectorReady != testCase.projectorOK {
				t.Fatalf("projectorReady missing or wrong: %s", mustJSONString(t, health))
			}
			if !health.Enabled {
				t.Fatalf("enabled=%v want true (%s)", health.Enabled, mustJSONString(t, health))
			}
		})
	}
}

// TestAccountBalanceGoOwnerHealthEarlyExitReportsOwnerMode mirrors the
// archived early-exit branch: a missing endpoint degrades with ready=false
// and still reports ownerMode for standby/drain.
func TestAccountBalanceGoOwnerHealthEarlyExitReportsOwnerMode(t *testing.T) {
	for ownerModeEnv, wantOwnerMode := range map[string]string{
		"":        "",
		"standby": "standby",
		"drain":   "drain",
		"bogus":   "",
	} {
		health := accountBalanceGoOwnerHealth(getenvWith(goOwnerEnv(map[string]string{
			"JUHE_AI_BLUE_GREEN_OWNER_MODE":         ownerModeEnv,
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL": " ",
		})), accountBalanceHealthDeps{
			ProjectorReady: func() bool { return true },
			Fetch: func(*http.Request) (*http.Response, error) {
				t.Fatal("missing endpoint must not probe the jobs health endpoint")
				return nil, nil
			},
		})
		if health.Ready || !health.Enabled || health.OwnerMode != wantOwnerMode {
			t.Fatalf("ownerMode env %q: health=%s", ownerModeEnv, mustJSONString(t, health))
		}
	}
}

// TestResolveAccountBalanceOwnerMode pins the archived parse semantics:
// only the exact trimmed lowercase standby/drain strings select the mode,
// every other value falls back to active (unlike the fail-closed process
// ownermode.Load gate).
func TestResolveAccountBalanceOwnerMode(t *testing.T) {
	cases := map[string]ownermode.Mode{
		"":           ownermode.Active,
		"active":     ownermode.Active,
		"standby":    ownermode.Standby,
		"drain":      ownermode.Drain,
		" standby ":  ownermode.Standby,
		"STANDBY":    ownermode.Active,
		"weird":      ownermode.Active,
	}
	for value, want := range cases {
		if got := resolveAccountBalanceOwnerMode(getenvWith(map[string]string{
			"JUHE_AI_BLUE_GREEN_OWNER_MODE": value,
		})); got != want {
			t.Fatalf("owner mode %q resolved %q want %q", value, got, want)
		}
	}
}

// TestAccountBalanceGoOwnerHealthProbesJobsOrigin mirrors the archived URL
// assertion: the probe reads the Go jobs /health from the manual-bridge
// origin (path replaced, not appended) through the default transport.
func TestAccountBalanceGoOwnerHealthProbesJobsOrigin(t *testing.T) {
	var probedPath string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		probedPath = request.URL.Path
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"ready":true,"accountBalanceEnabled":true,"accountBalanceReady":true}`))
	}))
	defer server.Close()
	health := accountBalanceGoOwnerHealth(getenvWith(goOwnerEnv(map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_URL": server.URL + "/account-balance/manual",
	})), accountBalanceHealthDeps{})
	if !health.Ready {
		t.Fatalf("jobs origin probe not ready: %s", mustJSONString(t, health))
	}
	if probedPath != "/health" {
		t.Fatalf("probe path=%q want /health", probedPath)
	}
}

// TestAccountBalanceGoOwnerHealthUnusablePayloads: the archived Node branch
// treats a JSON null / non-object body as an unusable health document
// (health=undefined) and reports ready=false without changing the shape.
func TestAccountBalanceGoOwnerHealthUnusablePayloads(t *testing.T) {
	for _, body := range []string{"null", `[1,2]`, `"text"`, `{}`} {
		health := accountBalanceGoOwnerHealth(getenvWith(goOwnerEnv(nil)), accountBalanceHealthDeps{
			Fetch: fetchJSON(200, body),
		})
		if health.Ready {
			t.Fatalf("body %q must not be ready: %s", body, mustJSONString(t, health))
		}
	}
}

// TestAccountBalanceSystemHealthStatus mirrors the archived
// resolveSystemApiHealth: the body degrades while the endpoint stays 200.
func TestAccountBalanceSystemHealthStatus(t *testing.T) {
	if accountBalanceSystemHealthStatus(true) != "ok" {
		t.Fatal("ready health must report ok")
	}
	if accountBalanceSystemHealthStatus(false) != "degraded" {
		t.Fatal("unready health must degrade in the body only")
	}
}
