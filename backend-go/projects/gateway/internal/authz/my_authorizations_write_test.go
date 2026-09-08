package authz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

type recordingSink struct {
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.entries = append(s.entries, entry)
}

// jsonRequest builds a handler-ready request like the Node express.json()
// wire: an explicit content length plus the exact media type.
func jsonRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// D-215 regression: the four user-side write surfaces must exist and behave
// like the Node my-* mount of authorizations.routes.ts — the viewer is the
// owner scope, inbound grants resolve as not_found for grantees, and the
// operation logs degrade to mode 'self' with the owner scope.
func TestMyAuthorizationsWriteSurfaceLifecycle(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_my", "owner")
	sink := &recordingSink{}
	deps := &Deps{Store: f.store, Sink: sink}

	identity := func(req *http.Request, accountID string) *http.Request {
		return req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: accountID, Role: "user"}))
	}

	// POST /my-authorizations: the viewer creates an outbound grant.
	createReq := identity(jsonRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_my","granteeType":"system_account","granteeId":"grantee"}`), "owner")
	createRec := httptest.NewRecorder()
	deps.create(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("my-authorizations create = %d %s", createRec.Code, createRec.Body.String())
	}
	authorizationID := decodeJSONPath(t, createRec.Body.String(), "data", "item", "id")
	if authorizationID == "" {
		t.Fatal("create response must carry the authorization id")
	}

	// PATCH /my-authorizations/{id}: the owner pauses the outbound grant.
	patchReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+authorizationID,
		`{"expectedUpdatedAt":"`+decodeJSONPath(t, createRec.Body.String(), "data", "item", "updatedAt")+`","status":"paused"}`), "owner")
	patchReq.SetPathValue("id", authorizationID)
	patchRec := httptest.NewRecorder()
	deps.patchSelf(patchRec, patchReq, false)
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"status":"paused"`) {
		t.Fatalf("my-authorizations patch = %d %s", patchRec.Code, patchRec.Body.String())
	}
	pausedVersion := decodeJSONPath(t, patchRec.Body.String(), "data", "updatedAt")

	// PATCH /my-authorizations/{id}/expire: the owner extends the expiry.
	expireReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+authorizationID+"/expire",
		`{"expectedUpdatedAt":"`+pausedVersion+`","expiresAt":"2030-01-01T00:00:00Z"}`), "owner")
	expireReq.SetPathValue("id", authorizationID)
	expireRec := httptest.NewRecorder()
	deps.patchSelf(expireRec, expireReq, true)
	if expireRec.Code != http.StatusOK || !strings.Contains(expireRec.Body.String(), "2030-01-01") {
		t.Fatalf("my-authorizations expire = %d %s", expireRec.Code, expireRec.Body.String())
	}
	expireVersion := decodeJSONPath(t, expireRec.Body.String(), "data", "updatedAt")

	// The grantee cannot mutate the inbound grant: the owner scope pins the
	// viewer, so the mutation resolves as not_found (scope boundary regression
	// :746).
	granteePatch := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+authorizationID,
		`{"expectedUpdatedAt":"`+expireVersion+`","status":"active"}`), "grantee")
	granteePatch.SetPathValue("id", authorizationID)
	granteePatchRec := httptest.NewRecorder()
	deps.patchSelf(granteePatchRec, granteePatch, false)
	if granteePatchRec.Code != http.StatusNotFound {
		t.Fatalf("grantee inbound patch must be not_found = %d %s", granteePatchRec.Code, granteePatchRec.Body.String())
	}

	// DELETE /my-authorizations/{id}: grantee revocation of the inbound grant
	// is not_found (scope boundary regression :747)…
	granteeRevoke := identity(jsonRequest(t, http.MethodDelete, "/__aisys__/api/my-authorizations/"+authorizationID,
		`{"expectedUpdatedAt":"`+expireVersion+`"}`), "grantee")
	granteeRevoke.SetPathValue("id", authorizationID)
	granteeRevokeRec := httptest.NewRecorder()
	deps.revokeSelf(granteeRevokeRec, granteeRevoke)
	if granteeRevokeRec.Code != http.StatusNotFound {
		t.Fatalf("grantee inbound revoke must be not_found = %d %s", granteeRevokeRec.Code, granteeRevokeRec.Body.String())
	}

	// …while the owner revokes the outbound grant.
	revokeReq := identity(jsonRequest(t, http.MethodDelete, "/__aisys__/api/my-authorizations/"+authorizationID,
		`{"expectedUpdatedAt":"`+expireVersion+`"}`), "owner")
	revokeReq.SetPathValue("id", authorizationID)
	revokeRec := httptest.NewRecorder()
	deps.revokeSelf(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("my-authorizations revoke = %d %s", revokeRec.Code, revokeRec.Body.String())
	}

	// Operation log contract: mode 'self' and the owner scope on every
	// user-side mutation (Node operationMode(requestAccess) +
	// outcome.context.resourceOwnerSystemAccountId).
	if len(sink.entries) != 4 {
		t.Fatalf("operation log entries = %d, want 4 (create/patch/expire/revoke)", len(sink.entries))
	}
	for _, entry := range sink.entries {
		if entry.Mode != "self" {
			t.Fatalf("%s log mode = %q, want self", entry.Action, entry.Mode)
		}
		if entry.OperationScopeSystemAccountID != "owner" {
			t.Fatalf("%s log scope = %q, want owner", entry.Action, entry.OperationScopeSystemAccountID)
		}
		if entry.Module != "authorizations" {
			t.Fatalf("%s log module = %q", entry.Action, entry.Module)
		}
	}
	wantActions := map[string]bool{"create": false, "update": false, "update_expire": false, "revoke": false}
	for _, entry := range sink.entries {
		if _, ok := wantActions[entry.Action]; !ok {
			t.Fatalf("unexpected log action %q", entry.Action)
		}
		wantActions[entry.Action] = true
	}
	for action, seen := range wantActions {
		if !seen {
			t.Fatalf("missing %s operation log", action)
		}
	}
}

// D-216 regression (present-but-empty systemAccountId): every write route
// validates the scope query before the body schema, so `?systemAccountId=`
// fails with 400 '查询参数不合法' exactly like parseRequestScopeQuery
// (request-scope-query.ts).
func TestMyAuthorizationsWriteSurfaceRejectsBlankScopeQuery(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_blank", "owner")
	deps := &Deps{Store: f.store}

	identity := func(req *http.Request, accountID string) *http.Request {
		return req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: accountID, Role: "user"}))
	}
	assertBlankScope400 := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "系统账号 ID 不能为空") {
			t.Fatalf("%s with blank systemAccountId = %d %s, want 400 系统账号 ID 不能为空 (parseOrBadRequest first issue)", name, rec.Code, rec.Body.String())
		}
	}

	createRec := httptest.NewRecorder()
	deps.create(createRec, identity(jsonRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations?systemAccountId=",
		`{"resourceType":"group","resourceId":"grp_blank","granteeType":"system_account","granteeId":"grantee"}`), "owner"))
	assertBlankScope400("POST create", createRec)

	patchRec := httptest.NewRecorder()
	patchReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/any-id?systemAccountId=",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00Z","status":"paused"}`), "owner")
	patchReq.SetPathValue("id", "any-id")
	deps.patchSelf(patchRec, patchReq, false)
	assertBlankScope400("PATCH update", patchRec)

	expireRec := httptest.NewRecorder()
	expireReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/any-id/expire?systemAccountId=",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00Z","expiresAt":null}`), "owner")
	expireReq.SetPathValue("id", "any-id")
	deps.patchSelf(expireRec, expireReq, true)
	assertBlankScope400("PATCH expire", expireRec)

	revokeRec := httptest.NewRecorder()
	revokeReq := identity(jsonRequest(t, http.MethodDelete, "/__aisys__/api/my-authorizations/any-id?systemAccountId=",
		`{"expectedUpdatedAt":"2026-01-01T00:00:00Z"}`), "owner")
	revokeReq.SetPathValue("id", "any-id")
	deps.revokeSelf(revokeRec, revokeReq)
	assertBlankScope400("DELETE revoke", revokeRec)

	// An absent query keeps working: a non-blank value on the admin surface is
	// honored, and the my-* detail route still resolves.
	findRec := httptest.NewRecorder()
	deps.find(findRec, identity(httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-authorizations/missing-id", nil), "owner"), true)
	if findRec.Code != http.StatusNotFound {
		t.Fatalf("detail route with clean query = %d, want 404 for missing id", findRec.Code)
	}
}

func decodeJSONPath(t *testing.T, body string, path ...string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("invalid JSON %q: %v", body, err)
	}
	var current any = payload
	for _, key := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %v is not an object in %s", path, key, body)
		}
		current, ok = mapping[key]
		if !ok {
			t.Fatalf("path %v: key %s missing in %s", path, key, body)
		}
	}
	text, ok := current.(string)
	if !ok {
		t.Fatalf("path %v: value is not a string in %s", path, body)
	}
	return text
}
