package announcements

// w11f 覆盖波次：脚本化 sqlite 连接层故障注入 + 纯函数/直连分支测试。
// 连接器默认直通真实 sqlite，按查询子串注入 Query/Exec/Begin/Commit 故障、
// 受影响行数覆盖与罐装行（Scan 错误 / rows.Err 错误臂）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	execErr  error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	affected int64
	hasAff   bool
	limit    int
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w11fScript struct {
	mu        sync.Mutex
	t         *testing.T
	rules     []*w11fRule
	beginErr  error
	commitErr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w11fRule) allowUnfired() *w11fRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注入规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w11fScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w11f 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w11fScript) rule(r *w11fRule) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) failQuery(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom})
}

func (s *w11fScript) failQueryAlways(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom, limit: -1})
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	return s.rule(&w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr})
}

func (s *w11fScript) failExec(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fScript) execAffected(substr string, affected int64) *w11fRule {
	return s.rule(&w11fRule{substr: substr, affected: affected, hasAff: true})
}

func (s *w11fScript) failBegin() {
	s.mu.Lock()
	s.beginErr = w11fBoom
	s.mu.Unlock()
}

// failCommit 是一次性的。
func (s *w11fScript) failCommit() {
	s.mu.Lock()
	s.commitErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) take(query string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

// beginFailure 是一次性的。
func (s *w11fScript) beginFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.beginErr
	s.beginErr = nil
	return err
}

// commitFailure 是一次性的。
func (s *w11fScript) commitFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitErr
	s.commitErr = nil
	return err
}

type w11fConnector struct {
	base   driver.Connector
	script *w11fScript
}

func (c w11fConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fConn{base: conn, script: c.script}, nil
}

func (c w11fConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fConn struct {
	base   driver.Conn
	script *w11fScript
}

func (c *w11fConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fConn) Close() error                              { return c.base.Close() }

func (c *w11fConn) Begin() (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w11fTx{base: tx, script: c.script}, nil
}

func (c *w11fConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &w11fTx{base: tx, script: c.script}, nil
	}
	return c.Begin()
}

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.execErr != nil {
			return nil, rule.execErr
		}
		if rule.hasAff {
			return w11fResult{affected: rule.affected}, nil
		}
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w11fConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.queryErr != nil {
			return nil, rule.queryErr
		}
		if rule.cols != nil {
			return &w11fRows{cols: rule.cols, values: rule.rows, nextErr: rule.nextErr}, nil
		}
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w11fTx struct {
	base   driver.Tx
	script *w11fScript
}

func (t *w11fTx) Commit() error {
	if err := t.script.commitFailure(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w11fTx) Rollback() error { return t.base.Rollback() }

type w11fResult struct{ affected int64 }

func (r w11fResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11fResult) RowsAffected() (int64, error) { return r.affected, nil }

type w11fRows struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
	closed  bool
}

func (r *w11fRows) Columns() []string { return r.cols }
func (r *w11fRows) Close() error      { r.closed = true; return nil }
func (r *w11fRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.values[r.next]
	r.next++
	copy(dest, row)
	return nil
}

// ---------------------------------------------------------------------------
// 环境
// ---------------------------------------------------------------------------

var w11fDDL = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS announcements (id TEXT PRIMARY KEY, title TEXT NOT NULL, content TEXT NOT NULL, level TEXT NOT NULL, status TEXT NOT NULL, created_by TEXT NOT NULL, updated_by TEXT NOT NULL, published_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS announcement_reads (announcement_id TEXT NOT NULL, system_account_id TEXT NOT NULL, read_at TEXT NOT NULL, PRIMARY KEY (announcement_id, system_account_id), FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE)`,
}

type w11fEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	sink   *recordingSink
	store  *Store
	db     *sql.DB
	script *w11fScript
	jar    map[string]string
	mu     sync.Mutex
}

func newW11FEnv(t *testing.T) *w11fEnv {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range w11fDDL {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	var sequence atomic.Int64
	store, err := NewStore(db, false, nil, func(prefix string) string {
		return fmt.Sprintf("%s_w11f_%06d", prefix, sequence.Add(1))
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	sink := &recordingSink{}
	deps.MountAuth(k, "lax", false)
	Mount(k, deps, store, sink)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &w11fEnv{deps: deps, k: k, server: server, sink: sink, store: store, db: db, script: script, jar: map[string]string{}}
}

func (e *w11fEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *w11fEnv) login(t *testing.T, username, password, role string) {
	t.Helper()
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
}

func w11fExec(t *testing.T, env *w11fEnv, query string, args ...any) {
	t.Helper()
	if _, err := env.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func w11fQueryString(t *testing.T, env *w11fEnv, query string, args ...any) string {
	t.Helper()
	var value string
	if err := env.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func w11fSeedAnnouncement(t *testing.T, env *w11fEnv, id, status string) string {
	t.Helper()
	revision := "2024-01-01T00:00:00Z"
	var publishedAt any
	if status == "published" {
		publishedAt = revision
	}
	w11fExec(t, env, `INSERT INTO announcements (id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at)
		VALUES (?, 'w11f-标题', 'w11f-内容', 'info', ?, 'w11f-actor', 'w11f-actor', ?, ?, ?)`,
		id, status, publishedAt, revision, revision)
	return revision
}

// ---------------------------------------------------------------------------
// 纯函数分支
// ---------------------------------------------------------------------------

func TestW11FHelperBranches(t *testing.T) {
	// parseIntOr: 空 / 非数字 / 小于 1 / 合法。
	if parseIntOr("", 0) != 0 || parseIntOr("x", 0) != 0 || parseIntOr("0", 0) != 0 || parseIntOr("4", 0) != 4 {
		t.Fatal("parseIntOr drift")
	}
	// queryIntOption: 缺省 / 重复键 / 非法 / 越界 / 科学计数法。
	single := func(raw string) url.Values { return url.Values{"limit": {raw}} }
	if value, ok := queryIntOption(url.Values{}, "limit", 1, 30); value != nil || !ok {
		t.Fatalf("absent = %v/%v", value, ok)
	}
	if _, ok := queryIntOption(url.Values{"limit": {"1", "2"}}, "limit", 1, 30); ok {
		t.Fatal("repeated keys must fail")
	}
	if _, ok := queryIntOption(single("abc"), "limit", 1, 30); ok {
		t.Fatal("garbage must fail")
	}
	if _, ok := queryIntOption(single("0"), "limit", 1, 30); ok {
		t.Fatal("below min must fail")
	}
	if _, ok := queryIntOption(single("31"), "limit", 1, 30); ok {
		t.Fatal("above max must fail")
	}
	if value, ok := queryIntOption(single("1e1"), "limit", 1, 30); !ok || *value != 10 {
		t.Fatalf("scientific = %v/%v", value, ok)
	}
	// coercedInt: Inf / 小数。
	if _, ok := coercedInt("1e400"); ok {
		t.Fatal("inf must fail")
	}
	if _, ok := coercedInt("1.5"); ok {
		t.Fatal("fraction must fail")
	}
	// jsNumber: 下划线 / 十六进制 / 空串 / 尾部垃圾。
	if _, ok := jsNumber("1_0"); ok {
		t.Fatal("underscore must fail")
	}
	if value, ok := jsNumber("0x10"); !ok || value != 16 {
		t.Fatalf("hex = %v/%v", value, ok)
	}
	if value, ok := jsNumber(""); !ok || value != 0 {
		t.Fatalf("empty = %v/%v", value, ok)
	}
	if _, ok := jsNumber("10abc"); ok {
		t.Fatal("trailing garbage must fail")
	}
	// intValue nil。
	if intValue(nil, 7) != 7 {
		t.Fatal("intValue nil drift")
	}
	// utf16Length astral。
	if utf16Length("😀") != 2 || utf16Length("ab") != 2 {
		t.Fatal("utf16Length drift")
	}
	// truncateUTF16: 截断 / 星面截断 / 短串原样。
	if got := truncateUTF16("abcd", 3); got != "abc..." {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncateUTF16("a😀", 2); got != "a..." {
		t.Fatalf("astral truncate = %q", got)
	}
	if got := truncateUTF16("ab", 5); got != "ab" {
		t.Fatalf("short = %q", got)
	}
	// comparableText: nil / 不可序列化。
	if comparableText(nil) != "null" {
		t.Fatal("comparableText nil drift")
	}
	if comparableText(make(chan int)) != "null" {
		t.Fatal("comparableText chan drift")
	}
	// safeChangeText: nil / 字符串 / 非字符串 / 不可序列化。
	if safeChangeText(nil) != "" {
		t.Fatal("safeChangeText nil drift")
	}
	if safeChangeText("text") != "text" {
		t.Fatal("safeChangeText string drift")
	}
	if got := safeChangeText(42); got != "42" {
		t.Fatalf("safeChangeText int = %q", got)
	}
	if safeChangeText(make(chan int)) != "" {
		t.Fatal("safeChangeText chan drift")
	}
	if got := safeChangeText(strings.Repeat("a", 250)); got != strings.Repeat("a", 200)+"..." {
		t.Fatalf("safeChangeText clamp = %d", len(got))
	}
	// valueOrText。
	if valueOrText(nil, "fb") != "fb" || valueOrText(strPtr("  "), "fb") != "fb" || valueOrText(strPtr("v"), "fb") != "v" {
		t.Fatal("valueOrText drift")
	}
	// stateFieldValue: content nil / publishedAt nil / 未知字段。
	if stateFieldValue(MutationState{}, "content") != nil {
		t.Fatal("stateFieldValue content nil drift")
	}
	if stateFieldValue(MutationState{}, "publishedAt") != nil {
		t.Fatal("stateFieldValue publishedAt nil drift")
	}
	if stateFieldValue(MutationState{}, "unknown") != nil {
		t.Fatal("stateFieldValue unknown drift")
	}
	content := "c"
	published := "p"
	state := MutationState{Content: &content, PublishedAt: &published}
	if stateFieldValue(state, "content") != "c" || stateFieldValue(state, "publishedAt") != "p" {
		t.Fatal("stateFieldValue values drift")
	}
	// nextRevision 非法当前版本 → 以 now 为准（定宽毫秒，非 RFC3339Nano 变长）。
	if got := nextRevision("not-a-time", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)); got != "2024-01-02T03:04:05.000Z" {
		t.Fatalf("nextRevision bad = %q", got)
	}
	// 旧变长 revision + now 落在同毫秒 → floor 兜底仍产出定宽更晚值。
	if got := nextRevision("2024-01-02T03:04:05.123456789Z", time.Date(2024, 1, 2, 3, 4, 5, 123000000, time.UTC)); got != "2024-01-02T03:04:05.124Z" {
		t.Fatalf("nextRevision legacy floor = %q", got)
	}
	// normalizeLevel / normalizeStatus 非法值。
	bad := "bogus"
	if _, err := normalizeLevel(&bad, "info"); err == nil {
		t.Fatal("normalizeLevel bogus must fail")
	}
	if _, err := normalizeStatus(&bad, "draft"); err == nil {
		t.Fatal("normalizeStatus bogus must fail")
	}
	// NewStore: nil DB / 默认 now 与 newID。
	if _, err := NewStore(nil, false, nil, nil); err == nil {
		t.Fatal("NewStore(nil) must fail")
	}
	// PG 方言: table 与 bind。
	pgStore := &Store{pg: true}
	if pgStore.table("x") != "juhe_business.x" {
		t.Fatal("pg table drift")
	}
	if got := pgStore.bind("UPDATE t SET a = ?, b = ?"); got != "UPDATE t SET a = $1, b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	if itoa(0) != "0" || itoa(52) != "52" {
		t.Fatal("itoa drift")
	}
	// 分页窗口工具。
	if pageUpperBoundForWindow(0) != 1000 {
		t.Fatal("pageUpperBoundForWindow min drift")
	}
	if pageUpperBoundForWindow(2000) != 1 {
		t.Fatal("pageUpperBoundForWindow clamp drift")
	}
	// normalizeListOptions: size<1 / size>100。
	small, big := 0, 101
	page, size := normalizeListOptions(nil, &small)
	if page != 1 || size != 1 {
		t.Fatalf("normalizeListOptions small = %d/%d", page, size)
	}
	page, size = normalizeListOptions(nil, &big)
	if page != 1 || size != 100 {
		t.Fatalf("normalizeListOptions big = %d/%d", page, size)
	}
	// pagedTotalUpperBound: page/pageSize/itemCount 负值钳制。
	if got := pagedTotalUpperBound(-1, -1, -1, false); got != 0 {
		t.Fatalf("pagedTotalUpperBound clamps = %d", got)
	}
	if got := pagedTotalUpperBound(2, 10, 5, true); got != 16 {
		t.Fatalf("pagedTotalUpperBound hasMore = %d", got)
	}
	// writeMutationError: 四条臂。
	recorder := httptest.NewRecorder()
	if !writeMutationError(recorder, nil, false) {
		t.Fatal("writeMutationError nil must pass through")
	}
	recorder = httptest.NewRecorder()
	if writeMutationError(recorder, nil, true) || recorder.Code != http.StatusNotFound {
		t.Fatal("writeMutationError missing must 404")
	}
	recorder = httptest.NewRecorder()
	conflict := &ConflictError{Message: "冲突", CurrentRevision: "r1"}
	if writeMutationError(recorder, conflict, false) || recorder.Code != http.StatusConflict {
		t.Fatal("writeMutationError conflict must 409")
	}
	recorder = httptest.NewRecorder()
	if writeMutationError(recorder, &ValidationError{Message: "校验"}, false) || recorder.Code != http.StatusConflict {
		t.Fatal("writeMutationError validation must 409")
	}
	recorder = httptest.NewRecorder()
	if writeMutationError(recorder, w11fBoom, false) || recorder.Code != http.StatusInternalServerError {
		t.Fatal("writeMutationError generic must 500")
	}
	// 冲突错误与校验错误的 Error()。
	if (&ConflictError{Message: "m"}).Error() != "m" || (&ValidationError{Message: "m"}).Error() != "m" {
		t.Fatal("Error() drift")
	}
}

func strPtr(v string) *string { return &v }

// ---------------------------------------------------------------------------
// 请求体解析分支
// ---------------------------------------------------------------------------

func w11fBodyRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/x", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return request
}

func TestW11FBodyParsingBranches(t *testing.T) {
	// bodyObject: 非对象 JSON。
	recorder := httptest.NewRecorder()
	if _, ok := bodyObject(recorder, w11fBodyRequest(`[1,2]`), "无效"); ok || recorder.Code != http.StatusBadRequest {
		t.Fatal("bodyObject array must fail")
	}
	recorder = httptest.NewRecorder()
	if _, ok := bodyObject(recorder, w11fBodyRequest(`"str"`), "无效"); ok {
		t.Fatal("bodyObject string must fail")
	}
	recorder = httptest.NewRecorder()
	if body, ok := bodyObject(recorder, w11fBodyRequest(`{"a":1}`), "无效"); !ok || body["a"] != float64(1) {
		t.Fatal("bodyObject object must pass")
	}
	// 非法 JSON → DecodeJSON 已写 400。
	recorder = httptest.NewRecorder()
	if _, ok := bodyObject(recorder, w11fBodyRequest(`{bad`), "无效"); ok || recorder.Code != http.StatusBadRequest {
		t.Fatal("bodyObject malformed must fail")
	}

	// readAnnouncementIDs: 非对象 / 缺 announcementIds / 多余键 / 非数组 / 超限 / 非字符串项。
	cases := []string{
		`[1]`,
		`{}`,
		`{"announcementIds":["a"],"extra":1}`,
		`{"announcementIds":"a"}`,
		`{"announcementIds":[` + strings.Repeat(`"a",`, 30) + `"a"]}`,
		`{"announcementIds":[1]}`,
		`{"announcementIds":["  "]}`,
	}
	for _, body := range cases {
		recorder := httptest.NewRecorder()
		if ids, ok := readAnnouncementIDs(recorder, w11fBodyRequest(body)); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("readAnnouncementIDs(%s) = %v/%d", body, ids, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	if ids, ok := readAnnouncementIDs(recorder, w11fBodyRequest(`{"announcementIds":[" a ","a"," b "]}`)); !ok || len(ids) != 3 || ids[0] != "a" || ids[2] != "b" {
		t.Fatalf("readAnnouncementIDs trim = %v", ids)
	}

	// strictRevisionBody: 多键 / 缺键 / 非字符串 / 空白。
	for _, body := range []string{`{}`, `{"a":1,"expectedRevision":"r"}`, `{"expectedRevision":1}`, `{"expectedRevision":"  "}`} {
		recorder := httptest.NewRecorder()
		if revision, ok := strictRevisionBody(recorder, mustObject(t, body), "无效"); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("strictRevisionBody(%s) = %q/%d", body, revision, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	if revision, ok := strictRevisionBody(recorder, mustObject(t, `{"expectedRevision":" r "}`), "无效"); !ok || revision != "r" {
		t.Fatalf("strictRevisionBody valid = %q", revision)
	}

	// required/optional/enum 字段校验。
	body := mustObject(t, `{"title":"t","content":"c","level":"info","status":"draft"}`)
	if text, ok := requiredTextField(body, "title", 120); !ok || text != "t" {
		t.Fatalf("requiredTextField = %q", text)
	}
	if _, ok := requiredTextField(body, "missing", 120); ok {
		t.Fatal("requiredTextField missing must fail")
	}
	if _, ok := requiredTextField(mustObject(t, `{"title":""}`), "title", 120); ok {
		t.Fatal("requiredTextField empty must fail")
	}
	if _, ok := requiredTextField(mustObject(t, `{"title":1}`), "title", 120); ok {
		t.Fatal("requiredTextField non-string must fail")
	}
	if _, ok := requiredTextField(mustObject(t, `{"title":"`+strings.Repeat("a", 121)+`"}`), "title", 120); ok {
		t.Fatal("requiredTextField overflow must fail")
	}
	if value, ok := optionalTextField(body, "content", 5000); !ok || *value != "c" {
		t.Fatal("optionalTextField drift")
	}
	if value, ok := optionalTextField(body, "missing", 5000); !ok || value != nil {
		t.Fatal("optionalTextField absent drift")
	}
	if _, ok := optionalTextField(mustObject(t, `{"content":2}`), "content", 5000); ok {
		t.Fatal("optionalTextField non-string must fail")
	}
	if value, ok := optionalEnumField(body, "level", levels); !ok || *value != "info" {
		t.Fatal("optionalEnumField drift")
	}
	if _, ok := optionalEnumField(body, "missing", levels); !ok {
		t.Fatal("optionalEnumField absent drift")
	}
	if _, ok := optionalEnumField(mustObject(t, `{"level":"bogus"}`), "level", levels); ok {
		t.Fatal("optionalEnumField bogus must fail")
	}
	// revisionField: 缺失 / 非字符串 / 空白。
	if _, ok := revisionField(mustObject(t, `{}`)); ok {
		t.Fatal("revisionField missing must fail")
	}
	if _, ok := revisionField(mustObject(t, `{"expectedRevision":2}`)); ok {
		t.Fatal("revisionField non-string must fail")
	}
	// hasUnknownField。
	if hasUnknownField(body, "title", "content", "level", "status") {
		t.Fatal("known fields must pass")
	}
	if !hasUnknownField(body, "title") {
		t.Fatal("unknown field must fail")
	}
}

func mustObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.(map[string]any)
}

// ---------------------------------------------------------------------------
// store 层故障
// ---------------------------------------------------------------------------

func TestW11FStoreReadFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	w11fSeedAnnouncement(t, env, "w11f-ann1", "published")

	// ListPublic: limit 钳制（0 与 31）。
	items, err := store.ListPublic(context.Background(), "w11f-user", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListPublic limit0 = %v/%v", items, err)
	}
	if items, err = store.ListPublic(nil, "w11f-user", 31); err != nil || len(items) != 1 {
		t.Fatalf("ListPublic limit31 = %v/%v", items, err)
	}
	// 查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("FROM announcements a")
	if _, err := store.ListPublic(context.Background(), "w11f-user", 5); err == nil {
		t.Fatal("ListPublic query fault must fail")
	}
	env.script.canned("FROM announcements a", []string{"id", "title", "level", "published_at", "read_at"},
		[][]driver.Value{{nil, "t", "l", "p", nil}}, nil)
	if _, err := store.ListPublic(context.Background(), "w11f-user", 5); err == nil {
		t.Fatal("ListPublic scan fault must fail")
	}
	env.script.canned("FROM announcements a", []string{"id", "title", "level", "published_at", "read_at"},
		[][]driver.Value{{"i", "t", "l", "p", nil}}, w11fBoom)
	if _, err := store.ListPublic(context.Background(), "w11f-user", 5); err == nil {
		t.Fatal("ListPublic rows.Err fault must fail")
	}

	// FindPublic: 通用错误 / 未命中（nil）/ 命中。
	env.script.failQuery("WHERE id = ? AND status = 'published'")
	if _, err := store.FindPublic(context.Background(), "w11f-ann1"); err == nil {
		t.Fatal("FindPublic query fault must fail")
	}
	if detail, err := store.FindPublic(context.Background(), "w11f-missing"); err != nil || detail != nil {
		t.Fatalf("FindPublic missing = %v/%v", detail, err)
	}
	detail, err := store.FindPublic(context.Background(), "w11f-ann1")
	if err != nil || detail == nil || detail.Title != "w11f-标题" {
		t.Fatalf("FindPublic hit = %+v/%v", detail, err)
	}

	// MarkRead: 空白/重复 ID 跳过 + 上限截断; exec 错误。
	result, err := store.MarkRead(context.Background(), "w11f-user", []string{"  ", "w11f-ann1", "w11f-ann1"})
	if err != nil || result.Count != 1 {
		t.Fatalf("MarkRead dedupe = %+v/%v", result, err)
	}
	many := make([]string, 40)
	for i := range many {
		many[i] = "w11f-ann1"
	}
	if _, err := store.MarkRead(context.Background(), "w11f-user", many); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRead(context.Background(), "w11f-user", []string{" ", " "}); err != nil {
		t.Fatalf("MarkRead empty ids = %v", err)
	}
	env.script.failExec("INSERT INTO announcement_reads")
	if _, err := store.MarkRead(context.Background(), "w11f-user", []string{"w11f-ann1"}); err == nil {
		t.Fatal("MarkRead exec fault must fail")
	}

	// ListPage: 查询错误 / Scan 错误 / rows.Err / hasMore 截断。
	env.script.failQuery("ORDER BY a.updated_at DESC")
	if _, err := store.ListPage(context.Background(), nil, nil); err == nil {
		t.Fatal("ListPage query fault must fail")
	}
	env.script.canned("ORDER BY a.updated_at DESC", []string{"id", "title", "content_preview", "content_truncated", "level", "status", "updated_by_name", "published_at", "updated_at"},
		[][]driver.Value{{nil, "t", "p", int64(0), "l", "s", nil, nil, "r"}}, nil)
	if _, err := store.ListPage(context.Background(), nil, nil); err == nil {
		t.Fatal("ListPage scan fault must fail")
	}
	env.script.canned("ORDER BY a.updated_at DESC", []string{"id", "title", "content_preview", "content_truncated", "level", "status", "updated_by_name", "published_at", "updated_at"},
		[][]driver.Value{{"i", "t", "p", int64(0), "l", "s", nil, nil, "r"}}, w11fBoom)
	if _, err := store.ListPage(context.Background(), nil, nil); err == nil {
		t.Fatal("ListPage rows.Err fault must fail")
	}
	for i := 0; i < 3; i++ {
		w11fExec(t, env, `INSERT INTO announcements (id, title, content, level, status, created_by, updated_by, created_at, updated_at)
			VALUES (?, 'w11f-bulk', 'c', 'info', 'draft', 'a', 'a', '2024-01-0`+strconv.Itoa(i+2)+`T00:00:00Z', '2024-01-0`+strconv.Itoa(i+2)+`T00:00:00Z')`,
			"w11f-bulk"+strconv.Itoa(i))
	}
	sizeTwo := 2
	pageResult, err := store.ListPage(context.Background(), nil, &sizeTwo)
	if err != nil || !pageResult.HasMore || len(pageResult.Items) != 2 || pageResult.Total != 3 {
		t.Fatalf("ListPage hasMore = %+v/%v", pageResult, err)
	}

	// FindEditDetail: 通用错误 / 未命中。
	env.script.failQuery("SELECT id, title, content, level, status, updated_at")
	if _, err := store.FindEditDetail(context.Background(), "w11f-ann1"); err == nil {
		t.Fatal("FindEditDetail query fault must fail")
	}
	if detail, err := store.FindEditDetail(context.Background(), "w11f-missing"); err != nil || detail != nil {
		t.Fatalf("FindEditDetail missing = %v/%v", detail, err)
	}
}

func TestW11FStoreWriteFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	revision := w11fSeedAnnouncement(t, env, "w11f-ann2", "draft")
	title := "w11f-标题"
	content := "w11f-内容"
	input := MutationInput{Title: &title, Content: &content}

	// Create: 标题缺失 / 空标题 / 内容缺失 / 空内容 / 非法级别 / 非法状态 / exec 错误。
	if _, err := store.Create(context.Background(), MutationInput{}, "w11f-actor"); err == nil {
		t.Fatal("Create no title must fail")
	}
	empty := " "
	if _, err := store.Create(context.Background(), MutationInput{Title: &empty}, "w11f-actor"); err == nil {
		t.Fatal("Create blank title must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Title: &title}, "w11f-actor"); err == nil {
		t.Fatal("Create no content must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Title: &title, Content: &empty}, "w11f-actor"); err == nil {
		t.Fatal("Create blank content must fail")
	}
	bad := "bogus"
	if _, err := store.Create(context.Background(), MutationInput{Title: &title, Content: &content, Level: &bad}, "w11f-actor"); err == nil {
		t.Fatal("Create bogus level must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Title: &title, Content: &content, Status: &bad}, "w11f-actor"); err == nil {
		t.Fatal("Create bogus status must fail")
	}
	env.script.failExec("INSERT INTO announcements")
	if _, err := store.Create(context.Background(), input, "w11f-actor"); err == nil {
		t.Fatal("Create exec fault must fail")
	}
	// 默认 ID 生成器（NewStore 传入 nil newID）。
	defaultStore, err := NewStore(env.db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	published := "published"
	receipt, err := defaultStore.Create(context.Background(), MutationInput{Title: &title, Content: &content, Status: &published}, "w11f-actor")
	if err != nil || !strings.HasPrefix(receipt.ID, "ann_") {
		t.Fatalf("default newID create = %+v/%v", receipt, err)
	}

	// Patch: begin 错误 / 行扫描错误 / 缺失（nil）/ 空 revision 冲突 /
	// 标题校验 / 内容校验 / 级别校验 / 状态校验 / UPDATE 错误 /
	// 受影响行 0 / 发布清空已读失败 / commit 错误。
	env.script.failBegin()
	if _, err := store.Patch(context.Background(), "w11f-ann2", input, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch begin fault must fail")
	}
	env.script.failQuery("SELECT id, title, level, status")
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch row fault must fail")
	}
	if outcome, err := store.Patch(context.Background(), "w11f-missing", MutationInput{}, revision, "w11f-actor"); err != nil || outcome != nil {
		t.Fatalf("Patch missing = %v/%v", outcome, err)
	}
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Title: &title}, "  ", "w11f-actor"); err == nil {
		t.Fatal("Patch blank revision must conflict")
	}
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Title: &empty}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch blank title must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Content: &empty}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch blank content must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Level: &bad}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch bogus level must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Status: &bad}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch bogus status must fail")
	}
	newTitle := "w11f-新标题"
	env.script.failExec("UPDATE announcements")
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Title: &newTitle}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch update fault must fail")
	}
	env.script.execAffected("UPDATE announcements", 0)
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Title: &newTitle}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch affected0 must conflict")
	}
	// 发布转换: 清空已读失败。
	publishStatus := "published"
	env.script.failExec("DELETE FROM announcement_reads")
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Status: &publishStatus}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch reads-clear fault must fail")
	}
	// commit 失败。
	env.script.failCommit()
	if _, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{Title: &newTitle}, revision, "w11f-actor"); err == nil {
		t.Fatal("Patch commit fault must fail")
	}
	// noop（无变更字段）。
	outcome, err := store.Patch(context.Background(), "w11f-ann2", MutationInput{}, revision, "w11f-actor")
	if err != nil || outcome.Changed || outcome.Receipt.Revision != revision {
		t.Fatalf("Patch noop = %+v/%v", outcome, err)
	}
	// 内容相 同 → no-op 列。
	sameContent := "w11f-内容"
	current := w11fQueryString(t, env, `SELECT updated_at FROM announcements WHERE id = 'w11f-ann2'`)
	if outcome, err = store.Patch(context.Background(), "w11f-ann2", MutationInput{Content: &sameContent}, current, "w11f-actor"); err != nil || outcome.Changed {
		t.Fatalf("Patch same content noop = %+v/%v", outcome, err)
	}

	// Delete: begin 错误 / 行扫描错误 / 缺失 / 冲突 / exec 错误 / affected 0 / commit 错误。
	env.script.failBegin()
	if _, err := store.Delete(context.Background(), "w11f-ann2", revision); err == nil {
		t.Fatal("Delete begin fault must fail")
	}
	env.script.failQuery("SELECT id, title, level, status")
	if _, err := store.Delete(context.Background(), "w11f-ann2", revision); err == nil {
		t.Fatal("Delete row fault must fail")
	}
	if outcome, err := store.Delete(context.Background(), "w11f-missing", revision); err != nil || outcome != nil {
		t.Fatalf("Delete missing = %v/%v", outcome, err)
	}
	if _, err := store.Delete(context.Background(), "w11f-ann2", " "); err == nil {
		t.Fatal("Delete blank revision must conflict")
	}
	env.script.failExec("DELETE FROM announcements")
	if _, err := store.Delete(context.Background(), "w11f-ann2", revision); err == nil {
		t.Fatal("Delete exec fault must fail")
	}
	env.script.execAffected("DELETE FROM announcements", 0)
	if _, err := store.Delete(context.Background(), "w11f-ann2", revision); err == nil {
		t.Fatal("Delete affected0 must conflict")
	}
	env.script.failCommit()
	if _, err := store.Delete(context.Background(), "w11f-ann2", revision); err == nil {
		t.Fatal("Delete commit fault must fail")
	}
	// 正常删除。
	current = w11fQueryString(t, env, `SELECT updated_at FROM announcements WHERE id = 'w11f-ann2'`)
	deleted, err := store.Delete(context.Background(), "w11f-ann2", current)
	if err != nil || deleted.Receipt.ID != "w11f-ann2" {
		t.Fatalf("Delete hit = %+v/%v", deleted, err)
	}
}

// ---------------------------------------------------------------------------
// 路由层（HTTP）
// ---------------------------------------------------------------------------

func TestW11FRouteFaults(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11fadmin", "w11f-pass", "super_admin")
	w11fSeedAnnouncement(t, env, "w11f-ann3", "published")

	// 公共列表: 非法 limit → 400。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/announcements/public?limit=0", "")
	if code != http.StatusBadRequest || payload["message"] != "公告查询参数无效" {
		t.Fatalf("public limit invalid = %d %v", code, payload)
	}
	// 公共读取: 非法请求体 → 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read", `[1]`)
	if code != http.StatusBadRequest || payload["message"] != "公告已读参数无效" {
		t.Fatalf("public read invalid body = %d %v", code, payload)
	}
	// 公共详情: 未命中 → 404。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements/public/w11f-missing", "")
	if code != http.StatusNotFound || payload["message"] != "公告不存在" {
		t.Fatalf("public detail missing = %d %v", code, payload)
	}
	// 兼容路径: my-announcements?limit 非法 → 回落 0。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-announcements?limit=abc", "")
	if code != http.StatusOK {
		t.Fatalf("my-announcements lenient limit = %d %v", code, payload)
	}
	// my-announcements/read: 超过 30 个 ID → 400。
	ids := strings.Repeat(`"a",`, 31)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-announcements/read", `{"announcementIds":[`+ids[:len(ids)-1]+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "公告已读参数无效" {
		t.Fatalf("my-announcements read overflow = %d %v", code, payload)
	}
	// 管理列表: 非法 page / pageSize → 400。
	for _, query := range []string{"page=0", "pageSize=abc", "pageSize=101"} {
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements?"+query, "")
		if code != http.StatusBadRequest || payload["message"] != "公告查询参数无效" {
			t.Fatalf("admin list %s = %d %v", query, code, payload)
		}
	}
	// 管理详情: 未命中 → 404。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements/w11f-missing", "")
	if code != http.StatusNotFound || payload["message"] != "公告不存在" {
		t.Fatalf("admin detail missing = %d %v", code, payload)
	}
	// 更新: 非法 JSON / 未知字段 / 缺 revision / 无变更字段 / 未命中 404。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-ann3", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("update malformed = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-ann3", `{"expectedRevision":"r","extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("update unknown field = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-ann3", `{"title":"t"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("update no revision = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-ann3", `{"expectedRevision":"r"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("update no change field = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-missing", `{"expectedRevision":"r","title":"t"}`)
	if code != http.StatusNotFound || payload["message"] != "公告不存在" {
		t.Fatalf("update missing = %d %v", code, payload)
	}
	// 发布/下线: 非法 JSON / 多键 / 缺 revision / 未命中。
	for _, target := range []string{"publish", "unpublish"} {
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/w11f-ann3/"+target, `{bad`)
		if code != http.StatusBadRequest {
			t.Fatalf("%s malformed = %d %v", target, code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/w11f-ann3/"+target, `{"expectedRevision":"r","extra":1}`)
		if code != http.StatusBadRequest || payload["message"] != "公告版本参数无效" {
			t.Fatalf("%s multi key = %d %v", target, code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/w11f-ann3/"+target, `{"x":"y"}`)
		if code != http.StatusBadRequest || payload["message"] != "公告版本参数无效" {
			t.Fatalf("%s no revision = %d %v", target, code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/w11f-missing/"+target, `{"expectedRevision":"r"}`)
		if code != http.StatusNotFound || payload["message"] != "公告不存在" {
			t.Fatalf("%s missing = %d %v", target, code, payload)
		}
	}
	// 删除: 非法 JSON / 多键 / 未命中。
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/w11f-ann3", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("delete malformed = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/w11f-ann3", `{"expectedRevision":"r","x":1}`)
	if code != http.StatusBadRequest || payload["message"] != "公告版本参数无效" {
		t.Fatalf("delete multi key = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/w11f-missing", `{"expectedRevision":"r"}`)
	if code != http.StatusNotFound || payload["message"] != "公告不存在" {
		t.Fatalf("delete missing = %d %v", code, payload)
	}
	// 创建: 未知字段 / 缺标题 / 缺内容 → 400。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"title":"t","content":"c","extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("create unknown field = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"content":"c"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("create no title = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"title":"t"}`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("create no content = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements", `[1]`)
	if code != http.StatusBadRequest || payload["message"] != "公告参数无效" {
		t.Fatalf("create non object = %d %v", code, payload)
	}
}

func TestW11FRouteStoreFaults(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11fadmin", "w11f-pass", "super_admin")
	revision := w11fSeedAnnouncement(t, env, "w11f-ann4", "draft")

	// 公共列表 500。
	env.script.failQuery("FROM announcements a")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/announcements/public", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("public list fault = %d %v", code, payload)
	}
	// 公共读取 500。
	env.script.failExec("INSERT INTO announcement_reads")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/public/read", `{"announcementIds":["w11f-ann4"]}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("public read fault = %d %v", code, payload)
	}
	// 公共详情 500。
	env.script.failQuery("WHERE id = ? AND status = 'published'")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements/public/w11f-ann4", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("public detail fault = %d %v", code, payload)
	}
	// my-announcements 列表 / 读取 500。
	env.script.failQuery("FROM announcements a")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-announcements", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("my-announcements fault = %d %v", code, payload)
	}
	env.script.failExec("INSERT INTO announcement_reads")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-announcements/read", `{"announcementIds":["w11f-ann4"]}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("my-announcements read fault = %d %v", code, payload)
	}
	// 管理列表 / 详情 500。
	env.script.failQuery("ORDER BY a.updated_at DESC")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("admin list fault = %d %v", code, payload)
	}
	env.script.failQuery("SELECT id, title, content, level, status, updated_at")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/announcements/w11f-ann4", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("admin detail fault = %d %v", code, payload)
	}
	// 创建 500（exec 错误）。
	env.script.failExec("INSERT INTO announcements")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements", `{"title":"w11f-fault-title","content":"w11f-fault-content"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("create fault = %d %v", code, payload)
	}
	// 更新 500。
	env.script.failQuery("SELECT id, title, level, status")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/announcements/w11f-ann4", `{"expectedRevision":"`+revision+`","title":"新"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("update fault = %d %v", code, payload)
	}
	// 发布 500。
	env.script.failExec("UPDATE announcements")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/w11f-ann4/publish", `{"expectedRevision":"`+revision+`"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("publish fault = %d %v", code, payload)
	}
	// 删除 500。
	env.script.failExec("DELETE FROM announcements")
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/announcements/w11f-ann4", `{"expectedRevision":"`+revision+`"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("delete fault = %d %v", code, payload)
	}
}

// TestW11FSinkVisibility 补 sink 可见性分支: 更新 published 公告、下线已发布、
// 删除已发布公告 → all_users/summary；草稿操作 → admin_only/full。
func TestW11FSinkVisibility(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11fadmin", "w11f-pass", "super_admin")

	// 草稿创建 + 删除 → admin_only/full。
	draftID, draftRevision := env.w11fCreateAnnouncement(t, "w11f-草稿", "内容", "")
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+draftID, `{"expectedRevision":"`+draftRevision+`"}`); code != http.StatusNoContent {
		t.Fatalf("delete draft = %d", code)
	}

	// 已发布创建 + 删除 → all_users/summary。
	id, revision := env.w11fCreateAnnouncement(t, "w11f-发布创建", "内容", "published")
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/announcements/"+id, `{"expectedRevision":"`+revision+`"}`); code != http.StatusNoContent {
		t.Fatalf("delete published = %d", code)
	}

	// 更新 published 公告 → all_users/summary，随后下线 → all_users/summary。
	id, revision = env.w11fCreateAnnouncement(t, "w11f-发布更新", "内容", "published")
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/announcements/"+id, `{"expectedRevision":"`+revision+`","title":"w11f-发布更新2"}`)
	if code != http.StatusOK {
		t.Fatalf("patch published = %d %v", code, payload)
	}
	current := dataObject(t, payload)["revision"].(string)
	if code, payload = env.do(t, http.MethodPost, "/__aisys__/api/announcements/"+id+"/unpublish", `{"expectedRevision":"`+current+`"}`); code != http.StatusOK {
		t.Fatalf("unpublish = %d %v", code, payload)
	}

	entries := env.sink.snapshot()
	scopes := map[string]int{}
	for _, entry := range entries {
		scopes[entry.Action+"/"+entry.VisibilityScope+"/"+entry.DetailLevel]++
	}
	if scopes["create/admin_only/full"] == 0 {
		t.Fatalf("draft create visibility missing: %v", scopes)
	}
	if scopes["delete/admin_only/full"] == 0 {
		t.Fatalf("draft delete visibility missing: %v", scopes)
	}
	if scopes["create/all_users/summary"] == 0 {
		t.Fatalf("published create visibility missing: %v", scopes)
	}
	if scopes["update/all_users/summary"] == 0 {
		t.Fatalf("published update visibility missing: %v", scopes)
	}
	if scopes["unpublish/all_users/summary"] == 0 {
		t.Fatalf("unpublish visibility missing: %v", scopes)
	}
	if scopes["delete/all_users/summary"] == 0 {
		t.Fatalf("published delete visibility missing: %v", scopes)
	}
}

// w11fCreateAnnouncement 通过路由创建公告，返回 (id, revision)。
func (e *w11fEnv) w11fCreateAnnouncement(t *testing.T, title, content, status string) (string, string) {
	t.Helper()
	body := `{"title":"` + title + `","content":"` + content + `"`
	if status != "" {
		body += `,"status":"` + status + `"`
	}
	body += `}`
	code, created := e.do(t, http.MethodPost, "/__aisys__/api/announcements", body)
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", title, code, created)
	}
	data := created["data"].(map[string]any)
	return data["id"].(string), data["revision"].(string)
}
