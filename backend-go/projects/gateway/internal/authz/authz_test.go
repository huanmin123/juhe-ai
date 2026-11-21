package authz

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

const testRevisionSecret = "test-authz-revision-secret"

// sealedTestCredentials seals fixture credentials with the same secret the
// fixture store uses for material revisions; authzrevision.Load fails closed on
// a non-envelope value.
func sealedTestCredentials(t *testing.T, values map[string]any) string {
	t.Helper()
	sealed, err := accountcrypto.EncryptJSON(testRevisionSecret, values)
	if err != nil {
		t.Fatalf("seal fixture credentials: %v", err)
	}
	return sealed
}

type fixture struct {
	store *Store
	db    *sql.DB
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := newAuthzPGDatabase(t)
	now := time.Unix(1_750_000_000, 0).UTC()
	f := &fixture{db: db, now: now}
	// The clock reads through the fixture pointer so tests can advance time
	// for expiry state-machine coverage.
	store, err := NewStore(db, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	// 新来源模型在创建/接受/重确认时计算 material revision HMAC；生产由
	// gateway 装配注入，测试夹具提供稳定密钥。
	store.AttachRevisionSecret(testRevisionSecret)
	f.store = store
	return f
}

func (f *fixture) seedAccount(t *testing.T, id, status string) {
	t.Helper()
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', ?, 'pbkdf2$sha512$120000$abc$def', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`), id, id, id, status); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) seedTeamWithMember(t *testing.T, teamID, memberID string) {
	t.Helper()
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES (?, ?, 'active', 'owner', ?, ?) ON CONFLICT(id) DO NOTHING`), teamID, "Team "+teamID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`), "teammem_"+memberID, teamID, memberID, now, now, now); err != nil {
		t.Fatal(err)
	}
}

// createAcceptedDirectOffer drives the direct-offer happy path (owner offer →
// grantee claim) and returns the grant/source ids. Tests that exercise
// post-claim behavior must not seed grants through any retired path.
func (f *fixture) createAcceptedDirectOffer(t *testing.T, resourceType, resourceID, granteeID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	offer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: resourceType, ResourceID: resourceID,
		GranteeType: "system_account", GranteeUsername: granteeID,
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	if _, err := f.store.AcceptSource(ctx, granteeID, offer.SourceID, AcceptInput{},
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("accept source: %v", err)
	}
	return offer.GrantID, offer.SourceID
}

// claimAccountOffer drives the direct account offer → one-step claim: the
// offer is claimed with the grantee-selected target group (empty falls back to
// the grantee's enabled default group). Returns the grant id.
func (f *fixture) claimAccountOffer(t *testing.T, resourceID, granteeID, targetGroupID string) string {
	t.Helper()
	ctx := context.Background()
	offer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "account", ResourceID: resourceID,
		GranteeType: "system_account", GranteeUsername: granteeID,
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	result, err := f.store.AcceptSource(ctx, granteeID, offer.SourceID, AcceptInput{TargetGroupID: targetGroupID},
		f.now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("claim source: %v", err)
	}
	if !result.Bound || result.InstanceID == "" {
		t.Fatalf("account claim must provision and bind the instance: %+v", result)
	}
	return offer.GrantID
}

// acceptSourceForTest accepts the pending source as the given receiver,
// returning the store result/error for callers that assert the consent
// outcome.
func (f *fixture) acceptSourceForTest(t *testing.T, actorID, sourceID string) (*AcceptResult, error) {
	t.Helper()
	return f.store.AcceptSource(context.Background(), actorID, sourceID, AcceptInput{},
		f.now.UTC().Format(time.RFC3339Nano))
}

func (f *fixture) seedGroup(t *testing.T, groupID, ownerID string) {
	t.Helper()
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO groups (id, name, system_account_id, status) VALUES (?, ?, ?, 'active')`), groupID, "Group "+groupID, ownerID); err != nil {
		t.Fatal(err)
	}
}

// exec runs a fixture SQL statement with the fixtureBind placeholder rewrite.
func (f *fixture) exec(t *testing.T, query string) {
	t.Helper()
	if _, err := f.db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

// seedPhysicalAccount inserts the grantable physical account row backing an
// 'account' resource (resolveResource requires authorization_instance_
// authorization_id IS NULL).
func seedPhysicalAccount(t *testing.T, f *fixture, id, ownerID string) {
	t.Helper()
	vendor, driver := provisionVendorIdentity("minimax")
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, connection_driver_code, type, status,
		config_revision, dispatch_revision, deleted_at)
		VALUES (?, ?, ?, ?, 'oauth', 'active', 1, 1, NULL)`), id, ownerID, vendor, driver); err != nil {
		t.Fatal(err)
	}
}

// seedGranteeDefaultGroup satisfies the one-step claim default-group fallback
// for account grants claimed without a target group: the binding requires an
// enabled default group owned by the grantee（组无 provider 维度后，provider
// 入参仅为调用点兼容保留）.
func seedGranteeDefaultGroup(t *testing.T, f *fixture, granteeID, _ string) {
	t.Helper()
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO groups (id, name, system_account_id, status, enabled, is_default, updated_at)
		VALUES (?, 'default', ?, 'active', 1, 1, '2026-01-01T00:00:00Z')
		ON CONFLICT(id) DO NOTHING`), "grp-default-"+granteeID, granteeID); err != nil {
		t.Fatal(err)
	}
}

// accountRuntimeIDFor resolves the runtime (resource_authorizations) id for a
// resource/grantee pair.
func accountRuntimeIDFor(t *testing.T, f *fixture, resourceID, granteeID string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ?`), resourceID, granteeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAuthorizationsProcessingTTLMatchesNode(t *testing.T) {
	if authorizationsProcessingTTL != 120*time.Second {
		t.Fatalf("authorizations processing TTL = %s, want 120s", authorizationsProcessingTTL)
	}
}

// TestMyAuthorizationsDirectionRouteContract: /my-authorizations resolves
// direction — invalid values are rejected (query enum), outbound/inbound
// refine the self scope, and the admin list still ignores the parameter.
func TestMyAuthorizationsDirectionRouteContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_dir", "owner")
	if _, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_dir",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner"); err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: f.store}
	request := func(url string, accountID, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(authsys.WithAuthContext(req.Context(), &authsys.AuthContext{SystemAccountID: accountID, Role: role}))
		rec := httptest.NewRecorder()
		deps.list(rec, req, !strings.Contains(url, "/authorizations?") && strings.Contains(url, "my-authorizations"))
		return rec
	}
	if rec := request("/my-authorizations?direction=sideways", "grantee", "user"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid direction status = %d", rec.Code)
	}
	inbound := request("/my-authorizations?direction=inbound", "grantee", "user")
	if inbound.Code != http.StatusOK {
		t.Fatalf("inbound status = %d body=%s", inbound.Code, inbound.Body.String())
	}
	if items := decodeListItems(t, inbound); len(items) != 1 {
		t.Fatalf("grantee inbound items = %d, want 1", len(items))
	}
	outbound := request("/my-authorizations?direction=outbound", "grantee", "user")
	if items := decodeListItems(t, outbound); len(items) != 0 {
		t.Fatalf("grantee outbound items = %d, want 0", len(items))
	}
	ownerOutbound := request("/my-authorizations?direction=outbound", "owner", "user")
	if items := decodeListItems(t, ownerOutbound); len(items) != 1 {
		t.Fatalf("owner outbound items = %d, want 1", len(items))
	}
	// The admin surface validates the enum but does not apply the filter.
	adminFiltered := request("/authorizations?direction=inbound", "owner", "admin")
	if adminFiltered.Code != http.StatusOK {
		t.Fatalf("admin filtered status = %d", adminFiltered.Code)
	}
	if items := decodeListItems(t, adminFiltered); len(items) != 1 {
		t.Fatalf("admin list must ignore direction: items = %d", len(items))
	}
}

// TestAuthorizationRouteInputContract: strict-JSON route validation for the
// write surface runs before the store — unknown keys are rejected, an empty
// body is rejected, a malformed expiry is rejected verbatim, a valid remark +
// null-expiry patch lands, and the DELETE revoke reports revoked.
func TestAuthorizationRouteInputContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_route", "owner")
	// A claimed grant is active, the only state UpdateOffer accepts.
	grantID, _ := f.createAcceptedDirectOffer(t, "group", "grp_route", "grantee")
	deps := &Deps{Store: f.store}
	adminContext := authsys.WithAuthContext(context.Background(), &authsys.AuthContext{SystemAccountID: "owner", Role: "admin"})
	patchRequest := func(expireOnly bool, body string) *httptest.ResponseRecorder {
		url := "/authorizations/" + grantID
		if expireOnly {
			url += "/expire"
		}
		req := httptest.NewRequest(http.MethodPatch, url, strings.NewReader(body))
		// The kernel localize chain only parses bodies announced by content-length;
		// a direct ServeHTTP call must declare the length the wire would carry.
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", grantID)
		req = req.WithContext(adminContext)
		rec := newUpstreamRecorder()
		deps.patchScoped(rec, req, expireOnly, false)
		return rec.ResponseRecorder
	}
	// 更新面只接受 remark/expiresAt；status 字段属于未知键，strict JSON 直接拒绝。
	if rec := patchRequest(false, `{"status":"paused"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Unrecognized key(s) in object: 'status'") {
		t.Fatalf("status-key response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchRequest(false, `{}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "请提供要修改的授权内容") {
		t.Fatalf("no-content response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patchRequest(false, `{"expiresAt":"not-a-time"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "过期时间格式不正确") {
		t.Fatalf("invalid-expiry response = %d %s", rec.Code, rec.Body.String())
	}
	// explicit null is accepted when the grant has no expiry: clearing an unset
	// value is not an extension.
	body := `{"remark":"新备注","expiresAt":null}`
	if rec := patchRequest(false, body); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"updated":true`) {
		t.Fatalf("remark/null-expiry patch response = %d %s", rec.Code, rec.Body.String())
	}
	// 回收按 grantId 执行，无需客户端提交旧版本号。
	revokeReq := httptest.NewRequest(http.MethodDelete, "/authorizations/"+grantID, strings.NewReader(`{}`))
	revokeReq.Header.Set("Content-Length", strconv.Itoa(len(`{}`)))
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeReq.SetPathValue("id", grantID)
	revokeReq = revokeReq.WithContext(adminContext)
	revokeRec := httptest.NewRecorder()
	deps.revoke(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke response = %d %s", revokeRec.Code, revokeRec.Body.String())
	}
}

// upstreamRecorder mimics the kernel localizeWriter: it carries the
// UpstreamMarker so handler code that preserves verbatim zod messages behaves
// exactly as behind the real kernel chain.
type upstreamRecorder struct {
	*httptest.ResponseRecorder
	marked bool
}

func (r *upstreamRecorder) MarkUpstream()        { r.marked = true }
func (r *upstreamRecorder) MarkedUpstream() bool { return r.marked }

func newUpstreamRecorder() *upstreamRecorder {
	return &upstreamRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func decodeListItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data.Items
}

// TestSummaryUsageProjectionKey locks in the BUG-0175 W4-C D-126 授权面
// fix: the grant summary always renders the usage summary object — the
// all-zero AccountUsageSummary with lastUsedAt omitted — exactly like the
// Node includeUsage:false routes.
func TestSummaryUsageProjectionKey(t *testing.T) {
	summary := grantRow{ID: "auth_1", ResourceType: "account", ResourceID: "acc_1",
		OwnerID: "owner_1", GranteeType: "system_account", Status: "active"}.summary()
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage key missing from summary: %s", encoded)
	}
	if usage["requestCount"] != float64(0) || usage["totalTokens"] != float64(0) || usage["totalCost"] != float64(0) {
		t.Fatalf("usage must render the zero summary: %v", usage)
	}
	if _, present := usage["lastUsedAt"]; present {
		t.Fatalf("zero usage must omit lastUsedAt: %v", usage)
	}
	if _, present := payload["lastUsedAt"]; present {
		t.Fatalf("includeUsage:false must omit the summary lastUsedAt: %s", encoded)
	}
}
