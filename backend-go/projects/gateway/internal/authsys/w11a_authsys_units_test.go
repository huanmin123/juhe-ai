package authsys

// w11a coverage arms (part 1): pure helpers, parsePatchInput arms, route
// handler error paths, shared captcha/login-guard/runtime-state drivers, and
// the operation-log sink corners. IDs use the w11a- prefix where persistent.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// ---------------------------------------------------------------------------
// Pure helpers.
// ---------------------------------------------------------------------------

func TestW11APureHelperArms(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q", got)
	}
	if got := itoaText(0); got != "0" {
		t.Fatalf("itoaText(0) = %q", got)
	}
	if got := normalizeNullableDescription(strPtrW11A("   ")); got != nil {
		t.Fatalf("blank description = %v", got)
	}
	if seconds := retryAfterSeconds(time.Unix(0, 400_000_000), time.Unix(0, 0)); seconds != 1 {
		t.Fatalf("retryAfterSeconds sub-second = %d", seconds)
	}
	negative := -1
	if _, err := marshalRequestLimits(&UserRequestLimits{PerWeek: &negative}); err == nil {
		t.Fatal("negative window must fail")
	}
	huge := 1_000_000_001
	if _, err := marshalRequestLimits(&UserRequestLimits{PerMonth: &huge}); err == nil {
		t.Fatal("oversized window must fail")
	}
	if limits := parseUserRequestLimits(`{"perWeek":7,"perMonth":8}`); limits == nil || *limits.PerWeek != 7 || *limits.PerMonth != 8 {
		t.Fatalf("weekly/monthly limits = %+v", limits)
	}
	if limits := parseUserRequestLimits(`{"perMinute":5,"expiresOn":123}`); limits != nil {
		t.Fatalf("non-string expiresOn must drop the override: %+v", limits)
	}
	if limits := parseUserRequestLimits(`{"perMinute":5,"expiresOn":"not-a-date"}`); limits != nil {
		t.Fatalf("invalid expiresOn must drop the override: %+v", limits)
	}
	if _, err := userRequestLimitOverrideActive(&UserRequestLimits{}, "UTC", time.Now()); err != nil {
		t.Fatalf("blank expiresOn override: %v", err)
	}
	if _, err := userRequestLimitOverrideActive(&UserRequestLimits{ExpiresOn: strPtrW11A("bad-date")}, "UTC", time.Now()); err == nil {
		t.Fatal("invalid expiresOn must error")
	}
	// changeValue marshal fallback for composite values json cannot encode.
	if got := changeValue(map[string]any{"c": make(chan int)}, ""); got == nil {
		t.Fatal("marshal fallback must render a placeholder")
	}
	var nilStore *AccountStore
	if err := nilStore.CheckContract(context.Background()); err == nil {
		t.Fatal("nil store CheckContract must fail")
	}
	db := newContractTestDB(t)
	if _, err := NewAccountStore(db, modelcheckauth.SQLite, nil); err != nil {
		t.Fatalf("nil clock must fall back to time.Now: %v", err)
	}
}

func strPtrW11A(value string) *string { return &value }

// ---------------------------------------------------------------------------
// parsePatchInput arms (app.go).
// ---------------------------------------------------------------------------

func TestW11AParsePatchInputArms(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	base := func(extra string) map[string]any {
		body := map[string]any{"expectedUpdatedAt": stamp}
		if extra != "" {
			if err := json.Unmarshal([]byte(extra), &body); err != nil {
				t.Fatal(err)
			}
		}
		return body
	}
	if _, err := parsePatchInput(base(`{"unknownKey":1}`)); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("unknown key = %v", err)
	}
	if _, err := parsePatchInput(base(`{"expectedUpdatedAt":5}`)); err == nil {
		t.Fatal("non-string expectedUpdatedAt must fail")
	}
	if _, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "bad-time"}); err == nil {
		t.Fatal("malformed timestamp must fail")
	}
	if _, err := parsePatchInput(base(`{"displayName":""}`)); err == nil {
		t.Fatal("blank displayName must fail")
	}
	if _, err := parsePatchInput(base(`{"displayName":5}`)); err == nil {
		t.Fatal("non-string displayName must fail")
	}
	if _, err := parsePatchInput(base(`{"description":5}`)); err == nil {
		t.Fatal("non-string description must fail")
	}
	long := strings.Repeat("x", 201)
	if _, err := parsePatchInput(base(`{"description":"` + long + `"}`)); err == nil {
		t.Fatal("oversized description must fail")
	}
	if _, err := parsePatchInput(base(`{"password":5}`)); err == nil {
		t.Fatal("non-string password must fail")
	}
	if _, err := parsePatchInput(base(`{"password":"abc"}`)); err == nil {
		t.Fatal("short password must fail")
	}
	if _, err := parsePatchInput(base(`{"password":"a bcd"}`)); err == nil {
		t.Fatal("whitespace password must fail as bad request")
	}
	if _, err := parsePatchInput(base(`{"role":5}`)); err == nil {
		t.Fatal("non-string role must fail")
	}
	if _, err := parsePatchInput(base(`{"role":"super_admin"}`)); err == nil {
		t.Fatal("super_admin role must fail at parse layer")
	}
	if _, err := parsePatchInput(base(`{"status":5}`)); err == nil {
		t.Fatal("non-string status must fail")
	}
	if _, err := parsePatchInput(base(`{"mustChangePassword":"yes"}`)); err == nil {
		t.Fatal("non-bool mustChangePassword must fail")
	}
	if _, err := parsePatchInput(base(`{"imageGenerationEnabled":1}`)); err == nil {
		t.Fatal("non-bool imageGenerationEnabled must fail")
	}
	if _, err := parsePatchInput(base(`{"aiAccountLimit":"many"}`)); err == nil {
		t.Fatal("non-number aiAccountLimit must fail")
	}
	if _, err := parsePatchInput(base(`{"requestLimits":[]}`)); err == nil {
		t.Fatal("array requestLimits must fail")
	}
	if _, err := parsePatchInput(base(`{"requestLimits":{"perMinute":"fast"}}`)); err == nil {
		t.Fatal("string perMinute must fail")
	}
	input, err := parsePatchInput(base(`{"mustChangePassword":true,"imageGenerationEnabled":false,"aiAccountLimit":null,"requestLimits":null,"description":"  x  ","status":"disabled","role":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.MustChangePassword == nil || !*input.MustChangePassword ||
		input.ImageGenerationEnabled == nil || *input.ImageGenerationEnabled ||
		!input.AIAccountLimitPresent || input.AIAccountLimit != nil ||
		!input.RequestLimitsPresent || input.RequestLimits != nil ||
		input.Description == nil || *input.Description != "x" {
		t.Fatalf("input = %+v", input)
	}
}

// ---------------------------------------------------------------------------
// createAccount / patchAccount handler arms.
// ---------------------------------------------------------------------------

func w11aHandlerRequest(method, target, body string, auth *AuthContext) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var request *http.Request
	if reader != nil {
		request = httptest.NewRequest(method, target, reader)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	} else {
		request = httptest.NewRequest(method, target, nil)
	}
	if auth != nil {
		request = request.WithContext(WithAuthContext(request.Context(), auth))
	}
	return request
}

func TestW11ACreateAccountHandlerArms(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	admin := &AuthContext{SystemAccountID: "w11a-admin", Username: "w11a-admin", Role: "admin"}

	rec := httptest.NewRecorder()
	deps.createAccount(rec, w11aHandlerRequest(http.MethodPost, "/accounts", "{bad json", admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	deps.createAccount(rec, w11aHandlerRequest(http.MethodPost, "/accounts", `{"username":"x","displayName":"d","password":"abcd"}`, admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short username = %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestW11APatchAccountHandlerArms(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	admin := &AuthContext{SystemAccountID: "w11a-admin", Username: "w11a-admin", Role: "admin"}

	created, err := deps.Accounts.Create(nil, CreateInput{Username: "w11a-user", DisplayName: "w11a_user", Password: "w11a-pass"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := deps.Accounts.Create(nil, CreateInput{Username: "w11a-user2", DisplayName: "w11a_user2", Password: "w11a-pass"})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	deps.patchAccount(rec, w11aHandlerRequest(http.MethodPatch, "/accounts/"+created.ID, "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	badJSON := w11aHandlerRequest(http.MethodPatch, "/accounts/"+created.ID, "{bad json", admin)
	badJSON.SetPathValue("id", created.ID)
	deps.patchAccount(rec, badJSON)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	usernamePatch := w11aHandlerRequest(http.MethodPatch, "/accounts/"+created.ID,
		`{"username":"new"}`, admin)
	usernamePatch.SetPathValue("id", created.ID)
	deps.patchAccount(rec, usernamePatch)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("username patch = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	missingRequest := w11aHandlerRequest(http.MethodPatch, "/accounts/w11a-missing",
		`{"expectedUpdatedAt":"`+created.EditVersion+`","status":"disabled"}`, admin)
	missingRequest.SetPathValue("id", "w11a-missing")
	deps.patchAccount(rec, missingRequest)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id = %d (%s)", rec.Code, rec.Body.String())
	}

	// Duplicate display name → ConflictError 409 arm.
	body := `{"expectedUpdatedAt":"` + second.EditVersion + `","displayName":"w11a_user"}`
	rec = httptest.NewRecorder()
	duplicateRequest := w11aHandlerRequest(http.MethodPatch, "/accounts/"+second.ID, body, admin)
	duplicateRequest.SetPathValue("id", second.ID)
	deps.patchAccount(rec, duplicateRequest)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate display name = %d (%s)", rec.Code, rec.Body.String())
	}

	// Duplicate display name → ConflictError 409 arm is above; the broken
	// schema 500 lives in the store-level test file.
}

// ---------------------------------------------------------------------------
// Route handler arms (getProfile / patchMe / postChangePassword / captcha /
// temporary tokens / requireRole / authenticated rate limit).
// ---------------------------------------------------------------------------

type w11aFakeCaptcha struct {
	blocked bool
	err     error
}

func (c *w11aFakeCaptcha) Issue(string) (modelcheckauth.CaptchaIssueResult, error) {
	return modelcheckauth.CaptchaIssueResult{Blocked: c.blocked, RetryAfter: 30}, c.err
}
func (c *w11aFakeCaptcha) Verify(string, string) bool { return true }

func TestW11ARouteHandlerArms(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	user := &AuthContext{SystemAccountID: "w11a-self", Username: "w11a-self", Role: "user"}

	// getCaptcha: issue failure and blocked-with-blank-message arms.
	deps.CaptchaDisabled = false
	deps.Captcha = &w11aFakeCaptcha{err: errors.New("w11a captcha down")}
	rec := httptest.NewRecorder()
	deps.getCaptcha(rec, httptest.NewRequest(http.MethodGet, "/captcha", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("captcha issue failure = %d (%s)", rec.Code, rec.Body.String())
	}
	deps.Captcha = &w11aFakeCaptcha{blocked: true}
	rec = httptest.NewRecorder()
	deps.getCaptcha(rec, httptest.NewRequest(http.MethodGet, "/captcha", nil))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "稍后再试") {
		t.Fatalf("blocked captcha = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	deps.CaptchaDisabled = true

	// getProfile without auth.
	rec = httptest.NewRecorder()
	deps.getProfile(rec, w11aHandlerRequest(http.MethodGet, "/profile", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("getProfile no auth = %d", rec.Code)
	}

	// patchMe arms.
	rec = httptest.NewRecorder()
	deps.patchMe(rec, w11aHandlerRequest(http.MethodPatch, "/me", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("patchMe no auth = %d", rec.Code)
	}
	mustChange := &AuthContext{SystemAccountID: "w11a-self", Username: "w11a-self", Role: "user", MustChangePassword: true}
	rec = httptest.NewRecorder()
	deps.patchMe(rec, w11aHandlerRequest(http.MethodPatch, "/me", `{"displayName":"x"}`, mustChange))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("patchMe must-change gate = %d (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	deps.patchMe(rec, w11aHandlerRequest(http.MethodPatch, "/me", "{bad json", user))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patchMe bad json = %d", rec.Code)
	}

	// postChangePassword arms.
	rec = httptest.NewRecorder()
	deps.postChangePassword(rec, w11aHandlerRequest(http.MethodPost, "/change-password", "{bad json", user))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("change password bad json = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	deps.postChangePassword(rec, w11aHandlerRequest(http.MethodPost, "/change-password", `{"oldPassword":"w11a-old"}`, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("change password no auth = %d", rec.Code)
	}

	// requireRole without auth.
	guarded := requireRole("admin", "需要管理员权限")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("requireRole no auth = %d", rec.Code)
	}

	// Authenticated rate limit writing the 429 itself.
	limited := &Deps{AuthenticatedRateLimit: func(w http.ResponseWriter, _ *http.Request, _ string) bool {
		w.WriteHeader(http.StatusTooManyRequests)
		return false
	}}
	if limited.rateLimitOrWrite(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), "w11a-user") {
		t.Fatal("rate limit must report false")
	}
}

type w11aTempTokenRejectPort struct{ businessauth.Port }

func (w11aTempTokenRejectPort) VerifyCredentials(context.Context, string, string) (modelcheckauth.VerifiedCredentials, bool, error) {
	return modelcheckauth.VerifiedCredentials{SystemAccountID: "w11a-admin", Username: "w11a-admin", Role: "admin", CredentialRevision: "rev-1"}, true, nil
}
func (w11aTempTokenRejectPort) CreateTemporaryToken(context.Context, string, string, int) (modelcheckauth.IssuedSession, bool, error) {
	return modelcheckauth.IssuedSession{}, false, nil
}

func TestW11ATemporaryTokenRejectedArm(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	deps.Port = w11aTempTokenRejectPort{deps.Port}
	deps.TemporaryAccessIPAllowlist = []string{"127.0.0.1"}

	rec := httptest.NewRecorder()
	request := w11aHandlerRequest(http.MethodPost, "/temporary-access-tokens",
		`{"username":"w11a-admin","password":"w11a-pass","ttlSeconds":120}`, nil)
	request.RemoteAddr = "127.0.0.1:55555"
	// The kernel request context (client IP) is attached by the middleware in
	// production; wrap the direct call to reproduce it.
	kernel.RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, inner *http.Request) {
		deps.postTemporaryAccessToken(w, inner)
	})).ServeHTTP(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("temporary token rejected = %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Shared drivers: captcha issue failure, login-guard lock arms, state store.
// ---------------------------------------------------------------------------

type w11aFakeStateStore struct {
	RedisStateStore
	counters   map[string]int64
	setJSONErr error
}

func (s *w11aFakeStateStore) Incr(_ context.Context, key string, _ int64, _ int64) (int64, error) {
	if s.counters == nil {
		s.counters = map[string]int64{}
	}
	s.counters[key]++
	return s.counters[key], nil
}
func (s *w11aFakeStateStore) SetJSON(context.Context, string, any, int64) error {
	return s.setJSONErr
}
func (s *w11aFakeStateStore) GetJSON(context.Context, string, any) (bool, error) { return false, nil }
func (s *w11aFakeStateStore) GetDeleteJSON(context.Context, string, any) (bool, error) {
	return false, nil
}
func (s *w11aFakeStateStore) DeleteJSON(context.Context, string) error { return nil }

func TestW11ASharedDriverArms(t *testing.T) {
	// Issue fails when the challenge write fails.
	failing := NewSharedCaptchaService(&w11aFakeStateStore{setJSONErr: errors.New("w11a write down")}, time.Now)
	if _, err := failing.Issue("203.0.113.9"); err == nil {
		t.Fatal("SetJSON failure must fail Issue")
	}

	// Login guard: username locked, fresh IP → the user-lock arm wins.
	guard := NewSharedLoginGuard(&w11aFakeStateStore{}, time.Now)
	for i := 0; i < 9; i++ {
		if blocked, _, _, err := guard.Failed("198.51.100.1", "w11a-user"); blocked || err != nil {
			t.Fatalf("locked too early at %d: blocked=%v err=%v", i, blocked, err)
		}
	}
	blocked, retry, message, err := guard.Failed("198.51.100.2", "w11a-user")
	if err != nil || !blocked || retry <= 0 || !strings.Contains(message, "账号暂时锁定") {
		t.Fatalf("user lock arm err=%v blocked=%v retry=%d message=%q", err, blocked, retry, message)
	}
	// The 10th attempt on the original IP already locks it too: the IP lock
	// arm now short-circuits the username arm.
	blocked, _, message, err = guard.Failed("198.51.100.1", "w11a-user")
	if err != nil || !blocked || !strings.Contains(message, "尝试过于频繁") {
		t.Fatalf("ip lock arm err=%v blocked=%v message=%q", err, blocked, message)
	}

	// SetJSON failure during the lock write fails closed (D8, 2026-09-20):
	// the 10th attempt surfaces the store error instead of staying
	// permissive.
	fragile := NewSharedLoginGuard(&w11aFakeStateStore{setJSONErr: errors.New("w11a lock down")}, time.Now)
	for i := 0; i < 9; i++ {
		blocked, _, _, err := fragile.Failed("198.51.100.3", "w11a-user2")
		if blocked || err != nil {
			t.Fatalf("pre-threshold attempts must stay clean: attempt %d blocked=%v err=%v", i, blocked, err)
		}
	}
	blocked, _, _, err = fragile.Failed("198.51.100.3", "w11a-user2")
	if blocked || err == nil {
		t.Fatalf("lock-write failure must fail closed: blocked=%v err=%v", blocked, err)
	}
}

func TestW11ASharedStateStoreArms(t *testing.T) {
	store, server := newMiniredisStore(t)
	// Malformed stored JSON decodes as a miss.
	key := "juhe-ai:dev:state:test_store:w11a-bad-json"
	if err := server.Set(key, "{not-json"); err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	if ok, err := store.GetDeleteJSON(nil, "w11a-bad-json", &target); ok || err != nil {
		t.Fatalf("malformed json = %v, %v", ok, err)
	}
	// Unmarshalable value fails SetJSON at the marshal layer.
	if err := store.SetJSON(nil, "w11a-chan", make(chan int), 1000); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
	// Connection failure propagates from GetDeleteJSON.
	server.Close()
	if _, err := store.GetDeleteJSON(nil, "w11a-gone", &target); err == nil {
		t.Fatal("closed server must fail reads")
	}
}

// ---------------------------------------------------------------------------
// Operation-log sink corners.
// ---------------------------------------------------------------------------

func TestW11AOperationLogSinkArms(t *testing.T) {
	var nilSink *OperationLogProducerSink
	nilSink.Record(OperationLogEntry{Module: "w11a", Action: "create"}, nil) // must not panic

	empty := &OperationLogProducerSink{}
	empty.Record(OperationLogEntry{Module: "w11a", Action: "create"}, nil) // nil producer short-circuit

	// Invalid MaxChanges with a nil Logger falls back to slog.Default().
	dropping := &sinkFakeStore{}
	invalid := &OperationLogProducerSink{MaxChanges: -1, Producer: operationlog.NewProducer(dropping, operationlog.OwnerLease{}, operationlog.Config{InstanceID: "w11a"}, nil)}
	invalid.Record(OperationLogEntry{
		Module: "w11a", Action: "update",
		Changes: []OperationLogChange{{Field: "f"}},
		Viewers: []OperationLogViewer{{SystemAccountID: "w11a-v", Reason: "w11a-reason"}},
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	if len(dropping.inputs) != 0 {
		t.Fatal("invalid max changes must drop the entry")
	}

	// User-Agent + viewers reach the producer on the happy path.
	recording := &sinkFakeStore{}
	sink := &OperationLogProducerSink{Producer: operationlog.NewProducer(recording, operationlog.OwnerLease{}, operationlog.Config{InstanceID: "w11a"}, nil)}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("User-Agent", "w11a-agent/1.0")
	sink.Record(OperationLogEntry{
		Module: "w11a", Action: "update",
		Changes: []OperationLogChange{{Field: "f", After: "v", BeforeValue: map[string]any{"k": float64(1)}}},
		Viewers: []OperationLogViewer{{SystemAccountID: "w11a-v", Reason: "w11a-reason"}},
	}, request)
	inputs := waitForSinkInputs(t, recording, 1)
	if len(inputs) == 0 {
		t.Fatal("entry must reach the producer")
	}
	input := inputs[len(inputs)-1]
	if input.UserAgent != "w11a-agent/1.0" || len(input.Viewers) != 1 || len(input.Changes) != 1 {
		t.Fatalf("input = %+v", input)
	}
}
