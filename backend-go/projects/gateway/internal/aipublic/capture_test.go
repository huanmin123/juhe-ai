// Tests for the aipublic capture hook (public-api-log-capture.middleware.ts
// port) and the shared Redis penalty-window driver
// (consumePenaltyWindowRateLimitAsync port): the finish/499 lifecycle, the
// source-context projection, the body parse-failed marker, the Node-shared
// Redis keyspace layout and the Redis-unavailable memory fallback.
package aipublic

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/publicapilogs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	redis "github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"
)

// recordingCapture collects the inputs the capture port emitted.
type recordingCapture struct {
	mutex  sync.Mutex
	inputs []publicapilogs.Input
}

func (c *recordingCapture) CaptureAIPublic(spec publicapilogs.CaptureSpec) {
	input := publicapilogs.BuildInput(spec)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.inputs = append(c.inputs, input)
}

func (c *recordingCapture) recorded() []publicapilogs.Input {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([]publicapilogs.Input(nil), c.inputs...)
}

// newAIPublicCaptureEnv builds the shared env with a custom Deps hook.
func newAIPublicCaptureEnv(t *testing.T, customize func(*Deps)) *aipublicEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:aipublic-capture-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range aipublicSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = service
	systemAccounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupsStore, err := groups.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	strategyStore, err := routestrategies.NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiKeyStore, err := apikeys.NewStore(db, false, "test-crypto-secret", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(db, false, "test-crypto-secret", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernelForTest()
	deps := &Deps{
		DB: db, PGDialect: false, Now: time.Now,
		SystemAccounts: systemAccounts,
		Groups:         groupsStore, Strategies: strategyStore,
		ApiKeys: apiKeyStore, AiAccounts: accountStore,
		Sink: &recordingAIPublicSink{},
	}
	if customize != nil {
		customize(deps)
	}
	deps.Mount(k)
	env := &aipublicEnv{t: t, db: db, server: httptest.NewServer(k.Handler())}
	t.Cleanup(env.server.Close)
	return env
}

// TestAIPublicCaptureLifecycle mirrors the capture middleware branches: the
// unauthenticated 401 records with a nil source, the authenticated success
// records the source context and the response snapshot, and a JSON body
// travels into the request snapshot.
func TestAIPublicCaptureLifecycle(t *testing.T) {
	capture := &recordingCapture{}
	env := newAIPublicCaptureEnv(t, func(deps *Deps) { deps.Capture = capture })
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	token := env.seedSource("extsrc_cap", "exttok_cap", "juis_token_capcapcapcapca",
		"active", "active", []string{"juhe_ai_public:group_list:read", "juhe_ai_public:group_add:write"}, "[]", "", "")

	// Unauthenticated: recorded with a nil source context (res.locals stays
	// empty in Node too).
	code, _, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=huanmin", "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", code)
	}
	inputs := capture.recorded()
	if len(inputs) != 1 {
		t.Fatalf("unauthenticated request must record exactly once, got %d", len(inputs))
	}
	missing := inputs[0]
	if missing.StatusCode != 401 || missing.Success {
		t.Fatalf("unauthenticated input mismatch: %+v", missing)
	}
	if missing.SourceRefID != "" || missing.TokenID != "" || missing.IsTestToken {
		t.Fatalf("unauthenticated input must carry no source: %+v", missing)
	}
	if missing.Method != "GET" || missing.Path != Prefix+"/group/list" {
		t.Fatalf("unauthenticated method/path mismatch: %+v", missing)
	}
	// The query string carries keys, so the request snapshot renders
	// complete (isSnapshotEmpty only fires with an absent/empty body AND an
	// empty query object).
	if missing.RequestCaptureStatus != publicapilogs.CaptureStatusComplete {
		t.Fatalf("GET with query keys must render the complete request snapshot: %+v", missing)
	}

	// Authenticated success: the source context and the response envelope are
	// captured.
	code, payload, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=huanmin&page=2&pageSize=5", "", token)
	if code != http.StatusOK {
		t.Fatalf("authenticated status: %d %v", code, payload)
	}
	inputs = capture.recorded()
	if len(inputs) != 2 {
		t.Fatalf("authenticated request must record, got %d", len(inputs))
	}
	success := inputs[1]
	if success.StatusCode != 200 || !success.Success {
		t.Fatalf("authenticated input status mismatch: %+v", success)
	}
	if success.SourceRefID != "extsrc_cap" || success.TokenID != "exttok_cap" || success.TokenPrefix != "juis_tok" {
		t.Fatalf("authenticated source context mismatch: %+v", success)
	}
	if success.QueryString != "targetUsername=huanmin&page=2&pageSize=5" {
		t.Fatalf("query string mismatch: %+v", success)
	}
	if success.ResponseCaptureStatus != publicapilogs.CaptureStatusComplete {
		t.Fatalf("response snapshot must be complete: %+v", success)
	}
	responseData := snapshotMap(t, success.ResponseData)
	if _, ok := responseData["statusCode"]; !ok {
		t.Fatalf("response snapshot must carry statusCode: %v", responseData)
	}
	if responseData["body"] == nil {
		t.Fatalf("response snapshot must carry the envelope body: %v", responseData)
	}
	if success.TraceID == "" || success.ClientIP == "" {
		t.Fatalf("trace/client ip must flow from the request context: %+v", success)
	}

	// POST with a JSON body: the parsed document lands in the request
	// snapshot (group add rejects the payload here, but the capture still
	// sees the body exactly like the Node body parser).
	code, _, _ = env.doAuth(http.MethodPost, Prefix+"/group/add", `{"name":"捕获组","targetUsername":"huanmin"}`, token)
	if code != http.StatusOK && code != http.StatusBadRequest {
		t.Fatalf("post status: %d", code)
	}
	inputs = capture.recorded()
	if len(inputs) != 3 {
		t.Fatalf("post request must record, got %d", len(inputs))
	}
	post := inputs[2]
	if post.Method != "POST" {
		t.Fatalf("post method mismatch: %+v", post)
	}
	if post.RequestCaptureStatus != publicapilogs.CaptureStatusComplete {
		t.Fatalf("post snapshot must be complete: %+v", post)
	}
	requestData := snapshotMap(t, post.RequestData)
	body, isMap := requestData["body"].(map[string]any)
	if !isMap {
		t.Fatalf("post body must decode into the snapshot: %v", requestData["body"])
	}
	if body["name"] != "捕获组" {
		t.Fatalf("post body document mismatch: %v", body)
	}
	headers, isMap := requestData["headers"].(map[string]any)
	if !isMap || headers["contentType"] != "application/json" {
		t.Fatalf("post headers snapshot mismatch: %v", requestData["headers"])
	}
}

// TestAIPublicCaptureBodyParseFailed mirrors the requestBodyRejectedReason
// marker: a malformed JSON body on a POST drops the body snapshot with the
// parse-failed reason.
func TestAIPublicCaptureBodyParseFailed(t *testing.T) {
	capture := &recordingCapture{}
	env := newAIPublicCaptureEnv(t, func(deps *Deps) { deps.Capture = capture })
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	token := env.seedSource("extsrc_cap2", "exttok_cap2", "juis_token_cap2cap2cap",
		"active", "active", []string{"juhe_ai_public:group_add:write"}, "[]", "", "")

	code, _, _ := env.doAuth(http.MethodPost, Prefix+"/group/add", `{"name": broken`, token)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed body status: %d", code)
	}
	inputs := capture.recorded()
	if len(inputs) != 1 {
		t.Fatalf("parse-failed request must record once, got %d", len(inputs))
	}
	failed := inputs[0]
	if failed.RequestCaptureStatus != publicapilogs.CaptureStatusDropped {
		t.Fatalf("parse-failed body must drop the snapshot: %+v", failed)
	}
	requestData := snapshotMap(t, failed.RequestData)
	body, isMap := requestData["body"].(map[string]any)
	if !isMap || body["dropped"] != true || body["reason"] != "request_body_parse_failed" {
		t.Fatalf("dropped body marker mismatch: %v", requestData["body"])
	}
}

// TestAIPublicCaptureNilSafe keeps the routes functional without the port.
func TestAIPublicCaptureNilSafe(t *testing.T) {
	env := newAIPublicCaptureEnv(t, nil)
	env.seedTargetUser("user_huanmin", "huanmin", "active")
	token := env.seedSource("extsrc_nil", "exttok_nil", "juis_token_nilnilnilni",
		"active", "active", []string{"juhe_ai_public:group_list:read"}, "[]", "", "")
	code, payload, _ := env.doAuth(http.MethodGet, Prefix+"/group/list?targetUsername=huanmin", "", token)
	if code != http.StatusOK {
		t.Fatalf("nil-capture status: %d %v", code, payload)
	}
}

// ---------------------------------------------------------------------------
// Redis shared penalty-window driver.
// ---------------------------------------------------------------------------

// penaltyEvalCall records one EVAL invocation.
type penaltyEvalCall struct {
	script string
	keys   []string
	args   []any
}

// penaltyFakeRedis replays canned EVAL results or errors; the other commands
// stay unused by the penalty-window driver.
type penaltyFakeRedis struct {
	mutex     sync.Mutex
	evalCalls []penaltyEvalCall
	result    []any
	err       error
}

func (c *penaltyFakeRedis) Eval(_ context.Context, script string, keys []string, args ...any) *redis.Cmd {
	c.mutex.Lock()
	c.evalCalls = append(c.evalCalls, penaltyEvalCall{script: script, keys: append([]string(nil), keys...), args: append([]any(nil), args...)})
	c.mutex.Unlock()
	cmd := redis.NewCmd(context.Background())
	if c.err != nil {
		cmd.SetErr(c.err)
		return cmd
	}
	cmd.SetVal(c.result)
	return cmd
}

func (c *penaltyFakeRedis) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringCmd(context.Background())
}

func (c *penaltyFakeRedis) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	return redis.NewStatusCmd(context.Background())
}

func (c *penaltyFakeRedis) SetNX(context.Context, string, any, time.Duration) *redis.BoolCmd {
	return redis.NewBoolCmd(context.Background())
}

func (c *penaltyFakeRedis) Del(context.Context, ...string) *redis.IntCmd {
	return redis.NewIntCmd(context.Background())
}

func (c *penaltyFakeRedis) MGet(context.Context, ...string) *redis.SliceCmd {
	return redis.NewSliceCmd(context.Background())
}

func (c *penaltyFakeRedis) calls() []penaltyEvalCall {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([]penaltyEvalCall(nil), c.evalCalls...)
}

// sharedPenaltyKey recomputes the Node redisPenaltyWindowRateLimitKey layout:
// juhe-ai:<namespace>:rate-limit:penalty:<sha256(store)>:<sha256(scope)>:<window>:<max>.
func sharedPenaltyKey(namespace, scopeKey string, windowSeconds, maxRequests int) string {
	hash := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	prefix := "juhe-ai:" + namespace + ":"
	if strings.HasPrefix("juhe-ai:rate-limit:penalty", prefix) {
		panic("unexpected namespace")
	}
	return strings.Join([]string{
		prefix + "rate-limit:penalty",
		hash(penaltyWindowStoreName),
		hash(scopeKey),
		itoa(windowSeconds),
		itoa(maxRequests),
	}, ":")
}

// TestAIPublicRateLimitRedisSharedKeyspace locks the shared-driver EVAL
// contract: the key layout matches the Node keyspace byte for byte and the
// script arguments carry the exponential-mode rule tuples.
func TestAIPublicRateLimitRedisSharedKeyspace(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	nowMs := now.UnixMilli()
	fake := &penaltyFakeRedis{result: []any{int64(1), int64(0), int64(0)}}
	deps := &Deps{
		Now:              func() time.Time { return now },
		RedisDriver:      true,
		RedisStateClient: fake,
		RedisNamespace:   "dev",
	}
	scopeKey := "src-1:tok-1:abcd1234"
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 100}, {WindowSeconds: 3600, MaxRequests: 5000}}
	decision := deps.consumeRateLimit(context.Background(), scopeKey, rules)
	if !decision.Allowed {
		t.Fatalf("first consume must be allowed: %+v", decision)
	}
	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one EVAL, got %d", len(calls))
	}
	call := calls[0]
	if !strings.Contains(call.script, "windowStartedAt") || !strings.Contains(call.script, "blockedUntilMs") {
		t.Fatalf("eval must run the shared penalty-window script: %s", call.script)
	}
	expectedKeys := []string{
		sharedPenaltyKey("dev", scopeKey, 60, 100),
		sharedPenaltyKey("dev", scopeKey, 3600, 5000),
	}
	if len(call.keys) != 2 || call.keys[0] != expectedKeys[0] || call.keys[1] != expectedKeys[1] {
		t.Fatalf("keyspace mismatch:\n got %v\nwant %v", call.keys, expectedKeys)
	}
	expectedArgs := []any{
		itoa64(nowMs), "2", "0",
		"60000", itoa64((nowMs/60000)*60000), "100", "900000", "86400000",
		"3600000", itoa64((nowMs/3600000)*3600000), "5000", "3600000", "86400000",
	}
	if len(call.args) != len(expectedArgs) {
		t.Fatalf("argument count mismatch: got %v", call.args)
	}
	for index, want := range expectedArgs {
		got, isString := call.args[index].(string)
		if !isString || got != want {
			t.Fatalf("argument %d mismatch: got %v (%T), want %s", index, call.args[index], call.args[index], want)
		}
	}
}

// TestAIPublicRateLimitRedisRejectedDecision mirrors the blocked EVAL result
// {0, retryMs, ruleIndex}: the decision projects the winning rule and the
// Retry-After seconds.
func TestAIPublicRateLimitRedisRejectedDecision(t *testing.T) {
	fake := &penaltyFakeRedis{result: []any{int64(0), int64(5000), int64(2)}}
	deps := &Deps{
		Now:              time.Now,
		RedisDriver:      true,
		RedisStateClient: fake,
		RedisNamespace:   "dev",
	}
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 100}, {WindowSeconds: 3600, MaxRequests: 5000}}
	decision := deps.consumeRateLimit(context.Background(), "scope", rules)
	if decision.Allowed {
		t.Fatalf("blocked result must deny: %+v", decision)
	}
	if decision.RetryAfterSeconds != 5 {
		t.Fatalf("retry-after mismatch: %+v", decision)
	}
	if decision.Rule != rules[1] {
		t.Fatalf("rule projection mismatch: %+v", decision.Rule)
	}
}

// TestAIPublicRateLimitRedisFallbackToMemory mirrors the migration-time
// degradation: a Redis failure warns once and the bucket degrades to the
// process-local memory model (requests keep working).
func TestAIPublicRateLimitRedisFallbackToMemory(t *testing.T) {
	fake := &penaltyFakeRedis{err: errors.New("redis down")}
	var warnings []string
	var warnMutex sync.Mutex
	deps := &Deps{
		Now:              time.Now,
		RedisDriver:      true,
		RedisStateClient: fake,
		RedisNamespace:   "dev",
		Warn: func(message string) {
			warnMutex.Lock()
			defer warnMutex.Unlock()
			warnings = append(warnings, message)
		},
	}
	scopeKey := "src-fb:tok-fb:ffff"
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 2}}
	for index := 0; index < 2; index++ {
		if decision := deps.consumeRateLimit(context.Background(), scopeKey, rules); !decision.Allowed {
			t.Fatalf("fallback consume %d must be allowed: %+v", index, decision)
		}
	}
	blocked := deps.consumeRateLimit(context.Background(), scopeKey, rules)
	if blocked.Allowed || blocked.RetryAfterSeconds < 1 || blocked.Rule != rules[0] {
		t.Fatalf("memory fallback must enforce the bucket: %+v", blocked)
	}
	warnMutex.Lock()
	defer warnMutex.Unlock()
	if len(warnings) == 0 {
		t.Fatalf("fallback must warn")
	}
	if !strings.Contains(warnings[0], "回退进程内存") || !strings.Contains(warnings[0], "redis down") {
		t.Fatalf("fallback warning mismatch: %v", warnings)
	}
	// Every failed consume falls back again (the warn is not sticky).
	if len(fake.calls()) != 3 {
		t.Fatalf("each consume must attempt Redis first: %d", len(fake.calls()))
	}
}

// TestAIPublicRateLimitMemoryModeSkipsRedis keeps the pure memory mode
// (RedisDriver off) untouched.
func TestAIPublicRateLimitMemoryModeSkipsRedis(t *testing.T) {
	fake := &penaltyFakeRedis{result: []any{int64(1), int64(0), int64(0)}}
	deps := &Deps{Now: time.Now, RedisStateClient: fake, RedisNamespace: "dev"}
	if decision := deps.consumeRateLimit(context.Background(), "scope", []RateLimitRule{{WindowSeconds: 60, MaxRequests: 1}}); !decision.Allowed {
		t.Fatalf("memory consume must be allowed: %+v", decision)
	}
	if calls := fake.calls(); len(calls) != 0 {
		t.Fatalf("memory mode must not touch redis: %v", calls)
	}
}

func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}

// snapshotMap re-reads a bounded snapshot document through its JSON form
// (the persisted representation) so assertions stay on plain maps.
func snapshotMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("snapshot marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("snapshot decode: %v (%s)", err, encoded)
	}
	return decoded
}
