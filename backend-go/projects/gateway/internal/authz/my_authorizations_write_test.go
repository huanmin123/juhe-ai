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

// D-215 regression: the user-side write surfaces follow the recipient-consent
// model — the viewer is the owner scope, the receiver must explicitly accept
// the pending source, and inbound grants resolve as not_found for grantees.
// The operation logs degrade to mode 'self' with the owner scope.
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

	// POST /my-authorizations: the viewer creates an outbound offer. It starts
	// pending until the receiver accepts the source.
	createReq := identity(jsonRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations",
		`{"resourceType":"group","resourceId":"grp_my","granteeType":"system_account","granteeUsername":"grantee"}`), "owner")
	createRec := httptest.NewRecorder()
	deps.create(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("my-authorizations create = %d %s", createRec.Code, createRec.Body.String())
	}
	grantID := decodeJSONPath(t, createRec.Body.String(), "data", "grantId")
	sourceID := decodeJSONPath(t, createRec.Body.String(), "data", "sourceId")
	if grantID == "" || sourceID == "" {
		t.Fatalf("create response must carry grantId/sourceId: %s", createRec.Body.String())
	}
	if status := decodeJSONPath(t, createRec.Body.String(), "data", "status"); status != StatusPending {
		t.Fatalf("create status = %q, want pending", status)
	}

	// The grantee cannot mutate the inbound offer: the owner scope pins the
	// viewer, so the mutation resolves as not_found.
	granteePatch := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+grantID,
		`{"remark":"hijack"}`), "grantee")
	granteePatch.SetPathValue("id", grantID)
	granteePatchRec := httptest.NewRecorder()
	deps.patchSelf(granteePatchRec, granteePatch, false)
	if granteePatchRec.Code != http.StatusBadRequest || !strings.Contains(granteePatchRec.Body.String(), "授权不存在") {
		t.Fatalf("grantee inbound patch must be hidden as 授权不存在 = %d %s", granteePatchRec.Code, granteePatchRec.Body.String())
	}
	granteeRevoke := identity(jsonRequest(t, http.MethodDelete, "/__aisys__/api/my-authorizations/"+grantID,
		`{}`), "grantee")
	granteeRevoke.SetPathValue("id", grantID)
	granteeRevokeRec := httptest.NewRecorder()
	deps.revokeSelf(granteeRevokeRec, granteeRevoke)
	if granteeRevokeRec.Code != http.StatusBadRequest || !strings.Contains(granteeRevokeRec.Body.String(), "授权不存在") {
		t.Fatalf("grantee inbound revoke must be hidden as 授权不存在 = %d %s", granteeRevokeRec.Code, granteeRevokeRec.Body.String())
	}

	// The receiver accepts the source, which activates the runtime（领取体只收
	// targetGroupId，无版本预读位）。
	acceptReq := identity(jsonRequest(t, http.MethodPost, "/__aisys__/api/my-authorization-sources/"+sourceID+"/accept",
		`{}`), "grantee")
	acceptReq.SetPathValue("sourceId", sourceID)
	acceptRec := httptest.NewRecorder()
	deps.acceptSource(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK || !strings.Contains(acceptRec.Body.String(), `"status":"active"`) {
		t.Fatalf("source accept = %d %s", acceptRec.Code, acceptRec.Body.String())
	}

	// PATCH /my-authorizations/{id}: the owner edits the remark（额度域已退役）。
	patchReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+grantID,
		`{"remark":"updated"}`), "owner")
	patchReq.SetPathValue("id", grantID)
	patchRec := httptest.NewRecorder()
	deps.patchSelf(patchRec, patchReq, false)
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"updated":true`) {
		t.Fatalf("my-authorizations patch = %d %s", patchRec.Code, patchRec.Body.String())
	}

	// PATCH /my-authorizations/{id}/expire: the owner shortens the expiry.
	expireReq := identity(jsonRequest(t, http.MethodPatch, "/__aisys__/api/my-authorizations/"+grantID+"/expire",
		`{"expiresAt":"2030-01-01T00:00:00Z"}`), "owner")
	expireReq.SetPathValue("id", grantID)
	expireRec := httptest.NewRecorder()
	deps.patchSelf(expireRec, expireReq, true)
	if expireRec.Code != http.StatusOK {
		t.Fatalf("my-authorizations expire set = %d %s", expireRec.Code, expireRec.Body.String())
	}
	// Moving the expiry further out is extension and must use /extend.
	extendReq := identity(jsonRequest(t, http.MethodPost, "/__aisys__/api/my-authorizations/"+grantID+"/extend",
		`{"expiresAt":"2031-01-01T00:00:00Z"}`), "owner")
	extendReq.SetPathValue("id", grantID)
	extendRec := httptest.NewRecorder()
	deps.extendOffer(extendRec, extendReq)
	if extendRec.Code != http.StatusOK {
		t.Fatalf("my-authorizations extend = %d %s", extendRec.Code, extendRec.Body.String())
	}

	// …while the owner revokes the outbound grant.
	revokeReq := identity(jsonRequest(t, http.MethodDelete, "/__aisys__/api/my-authorizations/"+grantID,
		`{}`), "owner")
	revokeReq.SetPathValue("id", grantID)
	revokeRec := httptest.NewRecorder()
	deps.revokeSelf(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("my-authorizations revoke = %d %s", revokeRec.Code, revokeRec.Body.String())
	}

	// Operation log contract: mode 'self' and the owner scope on every
	// user-side mutation; create is the offer-creation action.
	if len(sink.entries) != 4 {
		t.Fatalf("operation log entries = %d, want 4 (create/update/expire+extend/revoke): %#v", len(sink.entries), sink.entries)
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
	wantActions := map[string]int{"create_offer": 1, "update": 2, "revoke": 1}
	for _, entry := range sink.entries {
		if wantActions[entry.Action] == 0 {
			t.Fatalf("unexpected log action %q", entry.Action)
		}
		wantActions[entry.Action]--
	}
	for action, remaining := range wantActions {
		if remaining != 0 {
			t.Fatalf("missing %s operation log (%d)", action, remaining)
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
		`{"resourceType":"group","resourceId":"grp_blank","granteeType":"system_account","granteeUsername":"grantee"}`), "owner"))
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
