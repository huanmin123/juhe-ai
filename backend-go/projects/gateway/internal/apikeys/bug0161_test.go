package apikeys

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// BUG-0161 review-and-fix pairings. Every test below locks one adjudicated
// claim against the Node archive (migration-backup/node/final-archive).

// --- Claim 4: integerQueryValue uses JS Number + Number.isInteger. ---

func TestJSQueryIntegerSemantics(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantOK  bool
		comment string
	}{
		{"1", 1, true, "plain integer"},
		{" 7 ", 7, true, "JS trim accepts surrounding whitespace"},
		{"1e2", 100, true, "scientific notation is an integer"},
		{"1.0", 1, true, "fraction-zero decimal is an integer"},
		{"5.", 5, true, "trailing dot decimal"},
		{"+3", 3, true, "explicit plus sign"},
		{"0x10", 16, true, "hex radix prefix"},
		{"0b11", 3, true, "binary radix prefix"},
		{"0o17", 15, true, "octal radix prefix"},
		{"1e-3", 0, false, "fractional result is not an integer"},
		{".5", 0, false, "fractional result is not an integer"},
		{"Infinity", 0, false, "Infinity is not an integer"},
		{"1e400", 0, false, "overflow rounds to Infinity"},
		{"inf", 0, false, "strconv-only spelling must stay absent"},
		{"nan", 0, false, "strconv-only spelling must stay absent"},
		{"1_000", 0, false, "numeric separators are not Number() input"},
		{"abc", 0, false, "NaN input"},
		{"", 0, false, "blank is absent"},
		{"   ", 0, false, "whitespace-only is absent"},
		{"0x", 0, false, "bare prefix is NaN"},
		{"-0x10", 0, false, "signed radix literals are NaN in JS"},
	}
	for _, testCase := range cases {
		got, ok := jsQueryInteger(testCase.raw)
		if ok != testCase.wantOK || (ok && got != testCase.want) {
			t.Fatalf("jsQueryInteger(%q) = %d,%v want %d,%v (%s)",
				testCase.raw, got, ok, testCase.want, testCase.wantOK, testCase.comment)
		}
	}
}

func TestListPageQueryAcceptsJSNumberForms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-page")
	for _, name := range []string{"p1", "p2", "p3"} {
		if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"`+name+`"}`); code != http.StatusCreated {
			t.Fatalf("seed create: %d %v", code, payload)
		}
	}
	// pageSize=1e2 clamps into the window like Node's integerQueryValue.
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?pageSize=1e2&page=1.0", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["pageSize"] != float64(100) || data["page"] != float64(1) {
		t.Fatalf("JS number forms must drive pagination: %v", data)
	}
}

// --- Claims 2+3: optional-but-not-nullable sub-fields reject explicit null
// with the zod route messages; only top-level null clears. ---

const scheduleBaseTail = `"windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]`

func TestAPIKeyScheduleNullSubFieldsRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	schedule := func(name, extra string) string {
		return `{"name":"` + name + `","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows",` +
			scheduleBaseTail + extra + `}}`
	}
	// ASCII zod messages are unobservable through HTTP: Node wraps res.json
	// with localizeSystemErrorPayload and the Go kernel mirrors it, so CJK-less
	// 400 bodies render the status default 请求参数无效; CJK messages pass
	// through verbatim. Names stay unique per case: the mutation guard
	// deduplicates repeated (owner, name) fingerprints with a 409.
	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"dateRange-null", schedule("s-datenull", `,"dateRange":null`), "请求参数无效"},
		{"exceptions-null", schedule("s-excnull", `,"exceptions":null`), "请求参数无效"},
		{"deny-windows-null", schedule("s-denynull", `,"exceptions":[{"date":"2030-01-01","action":"deny","windows":null}]`), "请求参数无效"},
		{"allow-windows-null", schedule("s-allownull", `,"exceptions":[{"date":"2030-01-01","action":"allow","windows":null}]`), "请求参数无效"},
		{"allow-windows-missing", schedule("s-allowmiss", `,"exceptions":[{"date":"2030-01-01","action":"allow"}]`), "API Key 时间计划允许例外至少需要一个允许时段"},
		{"dateRange-startDate-null", schedule("s-startnull", `,"dateRange":{"startDate":null,"endDate":"2030-01-02"}`), "请求参数无效"},
		{"timezone-null", `{"name":"s-tznull","availabilitySchedule":{"enabled":true,"timezone":null,"mode":"allow_windows",` +
			scheduleBaseTail + `}}`, "请求参数无效"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", testCase.body)
		if code != http.StatusBadRequest || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v want 400 %q", testCase.name, code, payload, testCase.message)
		}
	}
	// Absent sub-fields stay legal (undefined semantics unchanged).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", schedule("s-absent", ""))
	if code != http.StatusCreated {
		t.Fatalf("absent sub-fields must stay legal: %d %v", code, payload)
	}
}
func TestAPIKeyQuotaNullSubItemsRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	// Create: {"daily":null} is a zod invalid_type, not a silent clear (the
	// ASCII zod message localizes to the 400 status default, like Node).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"quota-null","quotaLimits":{"daily":null}}`)
	if code != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("create quota null sub-item: %d %v", code, payload)
	}

	// PATCH: the invalid request is rejected and the stored limits survive.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"quota-keep","quotaLimits":{"daily":{"enabled":true,"limit":40}}}`)
	if code != http.StatusCreated {
		t.Fatalf("seed create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+id,
		`{"expectedRevision":"`+revision+`","quotaLimits":{"daily":null}}`)
	if code != http.StatusBadRequest || patched["message"] != "请求参数无效" {
		t.Fatalf("patch quota null sub-item: %d %v", code, patched)
	}
	if stored := env.queryCell(t, `SELECT quota_limits_json FROM api_keys WHERE id = ?`, id); !strings.Contains(stored, `"daily"`) {
		t.Fatalf("rejected patch must retain the stored quota: %v", stored)
	}
	// Top-level null keeps the clear-all semantics on PATCH.
	code, current := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("detail for revision: %d %v", code, current)
	}
	currentRevision := dataMap(t, current)["revision"].(string)
	code, cleared := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+id,
		`{"expectedRevision":"`+currentRevision+`","quotaLimits":null}`)
	if code != http.StatusOK {
		t.Fatalf("patch quotaLimits null must clear: %d %v", code, cleared)
	}
	if stored := env.queryCell(t, `SELECT COALESCE(quota_limits_json,'') FROM api_keys WHERE id = ?`, id); stored != "" {
		t.Fatalf("quotaLimits:null must clear the column: %v", stored)
	}
}

// --- Claim 7: the 200-char description cap counts UTF-16 code units. ---

func TestAPIKeyDescriptionUTF16Cap(t *testing.T) {
	if utf16Length("\U0001F600") != 2 || utf16Length("ab") != 2 {
		t.Fatal("utf16Length must count code units")
	}
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	emoji := "\U0001F600" // one emoji = two UTF-16 code units
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"emoji-101","description":"`+strings.Repeat(emoji, 101)+`"}`)
	// The ASCII zod message localizes to the status default, exactly like Node.
	if code != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("101 emoji must fail: %d %v", code, payload)
	}
	code, ok := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"emoji-100","description":"`+strings.Repeat(emoji, 100)+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("100 emoji (200 units) must pass: %d %v", code, ok)
	}
}

// --- Claim 17: only the exact empty expiresAt clears; whitespace is 400. ---

func TestAPIKeyExpiresAtWhitespaceRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"blank-expiry","expiresAt":"   "}`)
	if code != http.StatusBadRequest || payload["message"] != "API Key 过期时间必须是有效时间字符串" {
		t.Fatalf("whitespace expiresAt must 400: %d %v", code, payload)
	}
	// The exact empty string stays the clear/unset value.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"empty-expiry","expiresAt":""}`)
	if code != http.StatusCreated {
		t.Fatalf("empty expiresAt must pass: %d %v", code, created)
	}
	if stored := env.queryCell(t, `SELECT COALESCE(expires_at,'') FROM api_keys WHERE id = ?`, dataMap(t, created)["id"].(string)); stored != "" {
		t.Fatalf("empty expiresAt must not set a value: %v", stored)
	}
	// PATCH carries the same contract.
	id := dataMap(t, created)["id"].(string)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+id,
		`{"expectedRevision":"`+dataMap(t, created)["revision"].(string)+`","expiresAt":"  "}`)
	if code != http.StatusBadRequest || patched["message"] != "API Key 过期时间必须是有效时间字符串" {
		t.Fatalf("whitespace expiresAt on patch must 400: %d %v", code, patched)
	}
}

// --- Claim 6: whitespace-only stored schedule JSON is a read failure. ---

func TestAPIKeyStoredWhitespaceScheduleFailsReads(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := EncryptJSON(testSecret, secretPayload{Key: "sk-dirty"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, availability_schedule_json, created_at, updated_at)
		VALUES ('key-dirty', ?, 'rs-default', 'dirty', 'hash-dirty', 'sk-dirty', 'ty-xxxx', ?, 'active', 0, 'general', '   ', ?, ?)`,
		adminID, sealed, now, now)

	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", ""); code != http.StatusInternalServerError {
		t.Fatalf("list with whitespace schedule must 500: %d %v", code, payload)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/key-dirty", ""); code != http.StatusInternalServerError {
		t.Fatal("detail with whitespace schedule must 500")
	}
	revision := env.queryCell(t, `SELECT updated_at FROM api_keys WHERE id = 'key-dirty'`)
	code, _ := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-dirty",
		`{"expectedRevision":"`+revision+`","availabilitySchedule":null}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("patch touching a corrupt stored schedule must 500: %d", code)
	}
}

// --- Claim 1: the default schedule timezone comes from system_settings. ---

func TestAPIKeyDefaultScheduleTimezoneFromSettings(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	env.exec(t, `CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT, updated_at TEXT)`)

	// Valid business setting wins over the process timezone.
	env.exec(t, `INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"Asia/Shanghai"', '')`)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"tz-setting","availabilitySchedule":{"enabled":true,"mode":"allow_windows",
		"windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("create without timezone: %d %v", code, created)
	}
	stored := env.queryCell(t, `SELECT availability_schedule_json FROM api_keys WHERE id = ?`, dataMap(t, created)["id"].(string))
	if !strings.Contains(stored, `"timezone":"Asia/Shanghai"`) {
		t.Fatalf("default timezone must come from the setting: %v", stored)
	}

	// Invalid setting → deployment fallback (Node catches the throw).
	env.exec(t, `UPDATE system_settings SET value_json = '"Mars/Phobos"' WHERE key = 'usageStatsTimezone'`)
	env.store.tzMu.Lock()
	env.store.tzResolved = false
	env.store.tzMu.Unlock()
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"tz-invalid","availabilitySchedule":{"enabled":true,"mode":"allow_windows",
		"windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}}`)
	if code != http.StatusCreated {
		t.Fatalf("create with invalid setting: %d %v", code, created)
	}
	stored = env.queryCell(t, `SELECT availability_schedule_json FROM api_keys WHERE id = ?`, dataMap(t, created)["id"].(string))
	if !strings.Contains(stored, `"timezone":"`+fallbackScheduleTimezone()+`"`) {
		t.Fatalf("invalid setting must fall back to the deployment timezone: %v", stored)
	}
}

// --- Claim 15: unknown route strategy mode fails the read; empty omits. ---

func TestAPIKeyRouteStrategyModeNormalization(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := EncryptJSON(testSecret, secretPayload{Key: "sk-mode"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-mode', ?, 'rs-default', 'mode-key', 'hash-mode', 'sk-modex', 'e-keyxx', ?, 'active', 0, 'general', ?, ?)`,
		adminID, sealed, now, now)

	env.exec(t, `UPDATE route_strategies SET mode = 'bogus' WHERE id = 'rs-default'`)
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", ""); code != http.StatusInternalServerError {
		t.Fatal("unknown mode must fail the list")
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/key-mode", ""); code != http.StatusInternalServerError {
		t.Fatal("unknown mode must fail the detail")
	}
	env.exec(t, `UPDATE route_strategies SET mode = '' WHERE id = 'rs-default'`)
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if code != http.StatusOK {
		t.Fatalf("empty mode stays omitted: %d %v", code, payload)
	}
	item := dataMap(t, payload)["items"].([]any)[0].(map[string]any)
	if _, hasMode := item["routeStrategyMode"]; hasMode {
		t.Fatalf("empty mode must be omitted: %v", item)
	}
}

// --- Claim 5: PostgreSQL keyword filter uses COLLATE "C" + starts_with. ---

func TestPGDialectQueryContracts(t *testing.T) {
	pgClause, pgArgs := apiKeyKeywordClause(true, "al")
	if !strings.Contains(pgClause, `COLLATE "C"`) || !strings.Contains(pgClause, "starts_with(api_keys.name, ?)") {
		t.Fatalf("PG keyword clause must keep the C-collation + prefix guard: %v", pgClause)
	}
	if len(pgArgs) != 3 || pgArgs[0] != "al" || pgArgs[2] != "al" {
		t.Fatalf("PG keyword clause binds keyword, upper bound, keyword: %v", pgArgs)
	}
	sqliteClause, sqliteArgs := apiKeyKeywordClause(false, "al")
	if strings.Contains(sqliteClause, "COLLATE") || strings.Contains(sqliteClause, "starts_with") {
		t.Fatalf("SQLite keyword clause keeps the plain range: %v", sqliteClause)
	}
	if len(sqliteArgs) != 2 {
		t.Fatalf("SQLite keyword clause arity: %v", sqliteArgs)
	}
	if rowLockClause(true) != " FOR UPDATE" || rowLockClause(false) != "" {
		t.Fatalf("row lock clause: %q %q", rowLockClause(true), rowLockClause(false))
	}
	if strategyJoinLockClause(true) != " FOR UPDATE OF route_strategies, route_strategy_groups, groups" ||
		strategyJoinLockClause(false) != "" {
		t.Fatalf("strategy join lock clause: %q %q", strategyJoinLockClause(true), strategyJoinLockClause(false))
	}
}

// --- Claims 8+11: SQLite cleanup targets live in the dataset database and
// the maintenance submission runs after commit. ---

func TestAPIKeyDeleteSQLiteCleanupTargetPlacement(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"cleanup-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)

	dataset, err := sql.Open("sqlite", "file:apikeys-dataset-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer dataset.Close()
	if _, err := dataset.Exec(`CREATE TABLE api_key_record_cleanup_targets (
		api_key_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	env.store.SetDatasetDB(dataset)
	submitted := &recordingSubmitter{}
	env.store.SetCleanupSubmitter(submitted)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`) != 0 {
		t.Fatal("SQLite deletes must not write the cleanup target into the business database")
	}
	var owner string
	if err := dataset.QueryRow(`SELECT system_account_id FROM api_key_record_cleanup_targets WHERE api_key_id = ?`, id).Scan(&owner); err != nil {
		t.Fatalf("cleanup target must land in the dataset database: %v", err)
	}
	if owner != adminID {
		t.Fatalf("cleanup target owner: %v", owner)
	}
	if !submitted.has(id, adminID) {
		t.Fatal("maintenance submission must fire after commit")
	}
}

func TestAPIKeyDeleteWithoutDatasetDBKeepsSuccess(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"no-dataset"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	// Node keeps the delete successful when the cleanup side effects fail.
	result, err := env.store.Delete(context.Background(), id, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || !result.Deleted {
		t.Fatalf("delete must succeed without dataset wiring: %v %v", result, err)
	}
	if result.CleanupRegisterError == nil {
		t.Fatal("the unwired dataset handle must stay observable as CleanupRegisterError")
	}
}

type recordingSubmitter struct {
	targets [][2]string
}

func (r *recordingSubmitter) SubmitAPIKeyRelatedCleanup(_ context.Context, apiKeyID, systemAccountID string) error {
	r.targets = append(r.targets, [2]string{apiKeyID, systemAccountID})
	return nil
}

func (r *recordingSubmitter) has(apiKeyID, systemAccountID string) bool {
	for _, target := range r.targets {
		if target[0] == apiKeyID && target[1] == systemAccountID {
			return true
		}
	}
	return false
}
