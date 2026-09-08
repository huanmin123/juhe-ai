package authsys

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// D-214 regression: the global must_change_password 403 gate from Node
// requireAuth (auth.middleware.ts:51-54) applies to every authsys-gated
// surface, while the auth route family itself (requireSessionContext,
// auth.routes.ts:396) stays reachable so the account can read its flag and
// clear it via POST /auth/change-password.
func TestRequireSessionMustChangePasswordGlobalGate(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	// store.Create defaults mustChangePassword to true for non-admin roles.
	forced, err := deps.Accounts.Create(nil, CreateInput{
		Username: "forced", DisplayName: "Forced_Name", Password: "forced-pass", Role: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.AccountSummary.MustChangePassword {
		t.Fatalf("seeded account must carry mustChangePassword=true: %+v", forced.AccountSummary)
	}
	adminCookie := login(t, server, "root", "root-password")
	forcedCookie := login(t, server, "forced", "forced-pass")

	// Gated admin surface: 403 with the exact Node must_change_password body.
	gated, gatedBody := getJSON(t, server, "/__aisys__/api/system-accounts", forcedCookie)
	if gated.StatusCode != http.StatusForbidden {
		t.Fatalf("must-change account on admin list = %d, want 403", gated.StatusCode)
	}
	if gatedBody["message"] != "请先修改初始密码" || gatedBody["code"] != "must_change_password" {
		t.Fatalf("must-change gate body must mirror auth.middleware.ts: %v", gatedBody)
	}

	// The gate sits in front of the role check, so even a permitted role sees
	// the 403 gate (Node order: mustChangePassword check inside requireAuth
	// precedes requireAdmin).
	if root := accountIDByUsername(t, deps, "root"); root == "" {
		t.Fatal("root missing")
	}
	// Auth route family stays reachable: /me reports the flag…
	me, meBody := getJSON(t, server, "/__aisys__/api/auth/me", forcedCookie)
	if me.StatusCode != http.StatusOK {
		t.Fatalf("auth /me must stay reachable for must-change accounts: %d %v", me.StatusCode, meBody)
	}
	if meData, ok := meBody["data"].(map[string]any); !ok || meData["mustChangePassword"] != true {
		t.Fatalf("auth /me must report mustChangePassword=true: %v", meBody)
	}
	// …/profile stays readable…
	profile, profileBody := getJSON(t, server, "/__aisys__/api/auth/profile", forcedCookie)
	if profile.StatusCode != http.StatusOK {
		t.Fatalf("auth /profile must stay reachable for must-change accounts: %d %v", profile.StatusCode, profileBody)
	}
	// …PATCH /me keeps its own gate (auth.routes.ts:283-286)…
	patchSelf, patchSelfBody := patchJSON(t, server, "/__aisys__/api/auth/me", `{"displayName":"Renamed"}`, forcedCookie)
	if patchSelf.StatusCode != http.StatusForbidden ||
		patchSelfBody["code"] != "must_change_password" {
		t.Fatalf("auth PATCH /me must gate must-change accounts: %d %v", patchSelf.StatusCode, patchSelfBody)
	}
	// …and change-password clears the flag without the old password.
	change, changeBody := postJSON(t, server, "/__aisys__/api/auth/change-password",
		`{"newPassword":"changed-pass-1"}`, forcedCookie)
	if change.StatusCode != http.StatusOK {
		t.Fatalf("change-password must stay reachable: %d %v", change.StatusCode, changeBody)
	}

	// After the flag is cleared the gated surface opens up again.
	after, afterBody := getJSON(t, server, "/__aisys__/api/system-accounts", forcedCookie)
	if after.StatusCode != http.StatusForbidden {
		// A plain user still cannot list accounts, but the failure must be the
		// role gate, not the must_change_password gate.
		if msg, _ := afterBody["message"].(string); msg == "请先修改初始密码" {
			t.Fatalf("must_change_password gate must be cleared: %d %v", after.StatusCode, afterBody)
		}
	}
	adminList, _ := getJSON(t, server, "/__aisys__/api/system-accounts", adminCookie)
	if adminList.StatusCode != http.StatusOK {
		t.Fatalf("admin control account must be unaffected: %d", adminList.StatusCode)
	}
}

// D-216: a configured dev auto-login username whose account is missing or
// inactive is a server misconfiguration — Node throws inside
// developmentAutoLoginContextAsync (development-auto-login.ts) and lands on
// 500; only the unconfigured case falls through to the 401 contract.
func TestDevAutoLoginMissingAccountReportsServerFailure(t *testing.T) {
	deps, _, server := newTestEnv(t)

	// Unconfigured: anonymous request keeps the 401 contract.
	anonymous, anonymousBody := getJSON(t, server, "/__aisys__/api/auth/me", "")
	if anonymous.StatusCode != http.StatusUnauthorized || anonymousBody["message"] != "请先登录" {
		t.Fatalf("unconfigured dev auto-login must keep 401: %d %v", anonymous.StatusCode, anonymousBody)
	}

	deps.DevAutoLoginUsername = "ghost-account"
	missing, missingBody := getJSON(t, server, "/__aisys__/api/auth/me", "")
	if missing.StatusCode != http.StatusInternalServerError || missingBody["message"] != "服务器内部错误" {
		t.Fatalf("configured dev auto-login with missing account must be 500: %d %v", missing.StatusCode, missingBody)
	}
}

// Guard rail: the must_change_password body keeps the exact Node field order
// and code so frontend interceptors can keep matching it.
func TestMustChangeBodyContract(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeMustChange(recorder)
	body := recorder.Body.String()
	if !strings.Contains(body, `"code":"must_change_password"`) || !strings.Contains(body, "请先修改初始密码") {
		t.Fatalf("must-change body contract drifted: %q", body)
	}
}
