package proxyprofiles

// w9f 覆盖收尾：本文件只补既有测试未触达的分支（错误路径、方言分支、
// 纯函数分类与解析边界），不改变既有契约断言。生产逻辑零改动。

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// init 在任何测试调用 proxyTestDeadline 前注入 env，让 sync.OnceValue 走
// 解析+上限钳制分支（601000ms > 600000ms 上限）。默认 25s 只影响失败路径。
func init() {
	_ = os.Setenv(proxyTestDeadlineEnv, "601000")
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// w9fBrokenSchema: port/updated_at 列故意放宽为可空/文本，用于构造扫描错误
// 与 JOIN 列 NULL 的混合目标行。providers.code 不加 UNIQUE 以便构造重复目标。
const w9fBrokenSchema = `
	CREATE TABLE providers (
		id TEXT PRIMARY KEY, code TEXT, name TEXT, enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE TABLE provider_protocol_profiles (
		id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
		base_url TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE TABLE proxy_profiles (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, host TEXT NOT NULL,
		port TEXT NOT NULL, username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1,
		test_status TEXT NOT NULL DEFAULT 'unknown', latency_ms INTEGER,
		outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, updated_at TEXT);
`

func newW9FBrokenFixture(t *testing.T) *proxyFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(w9fBrokenSchema); err != nil {
		t.Fatalf("broken schema: %v", err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewStore(Deps{DB: db, PGDialect: false, Secret: "test-secret",
		Now:   func() time.Time { return now },
		NewID: func(prefix string) string { return prefix + "_w9f_seq" }})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return &proxyFixture{store: store, db: db, sink: &recordingSink{}, now: now}
}

func w9fInsertBrokenProxy(t *testing.T, db *sql.DB, id, name string, updatedAt any) {
	t.Helper()
	if updatedAt == nil {
		updatedAt = "2026-09-04T12:00:00.000Z"
	}
	if _, err := db.Exec(
		`INSERT INTO proxy_profiles (id, name, type, host, port, test_status, updated_at) VALUES (?, ?, 'http', '127.0.0.1', 'not-a-port', 'unknown', ?)`,
		id, name, updatedAt); err != nil {
		t.Fatalf("insert broken proxy: %v", err)
	}
}

func w9fInsertProviderTarget(t *testing.T, db *sql.DB, id, code string, name any, profileID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, '2026-09-04T00:00:00Z', '2026-09-04T00:00:00Z')`,
		id, code, name); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, enabled, base_url, updated_at) VALUES (?, ?, 1, 'http://example.test/v1', '2026-09-04T00:00:00Z')`,
		profileID, code); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
}

// w9fRawStore 直接构造 Store，绕过 NewStore 的必填校验。
func w9fRawStore(db *sql.DB, pg bool, secret string) *Store {
	return &Store{db: db, pg: pg, secret: secret,
		now:   func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
		newID: func(prefix string) string { return prefix + "_w9f" }}
}

const w9fSystemAccountsSchema = `CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`

// ---------------------------------------------------------------------------
// proxyTestDeadline env 解析分支（sync.OnceValue 全包只解析一次）
// ---------------------------------------------------------------------------

func TestW9FProxyTestDeadlineClampsAboveMax(t *testing.T) {
	// init 已写入 601000ms；一旦 Once 被解析，结果必须钳到 10min 上限。
	deadline := proxyTestDeadline()
	if deadline != proxyTestMaxDeadline {
		t.Fatalf("deadline = %v, want max %v", deadline, proxyTestMaxDeadline)
	}
}

// ---------------------------------------------------------------------------
// LoadProxyTestSnapshot：PG 方言列、扫描错误、混合 NULL 目标、重复 provider
// ---------------------------------------------------------------------------

func TestW9FLoadSnapshotPGDialectSelectsTextifiedColumn(t *testing.T) {
	fixture := newProxyTestFixture(t)
	// pg=true 时 SQL 使用 to_char，sqlite 必然报错；覆盖方言分支本身。
	_, err := w9fRawStore(fixture.db, true, "secret").LoadProxyTestSnapshot(context.Background(), "any")
	if err == nil {
		t.Fatal("pg dialect query on sqlite should fail")
	}
}

func TestW9FLoadSnapshotScanErrorOnBadPort(t *testing.T) {
	fixture := newW9FBrokenFixture(t)
	w9fInsertBrokenProxy(t, fixture.db, "px_bad", "坏端口", nil)
	snapshot, err := fixture.store.LoadProxyTestSnapshot(context.Background(), "px_bad")
	if err == nil {
		t.Fatalf("scan error expected, got snapshot %+v", snapshot)
	}
}

func TestW9FLoadSnapshotRejectsMixedNullTargetIdentifiers(t *testing.T) {
	fixture := newW9FBrokenFixture(t)
	// provider.name 为 NULL → JOIN 目标行三标识混合 NULL → 必须报错。
	w9fInsertProviderTarget(t, fixture.db, "pv1", "openai", nil, "prof1")
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, name, type, host, port, test_status, updated_at) VALUES ('px1', '代理', 'http', '127.0.0.1', '1', 'unknown', '2026-09-04T12:00:00.000Z')`); err != nil {
		t.Fatalf("insert proxy: %v", err)
	}
	_, err := fixture.store.LoadProxyTestSnapshot(context.Background(), "px1")
	if err == nil || !strings.Contains(err.Error(), "标识无效") {
		t.Fatalf("err = %v, want provider target 标识无效", err)
	}
}

func TestW9FProxyTestExistsSurfacesQueryError(t *testing.T) {
	fixture := newProxyTestFixture(t)
	db := fixture.db
	_ = db.Close()
	_, err := fixture.store.ProxyTestExists(context.Background(), "px1")
	if err == nil {
		t.Fatal("closed db should fail ProxyTestExists")
	}
}

// ---------------------------------------------------------------------------
// proxyTestURL / transport / 目标解析
// ---------------------------------------------------------------------------

func TestW9FProxyTestURLRejectsUnparseableFinalURL(t *testing.T) {
	fixture := newProxyFixture(t)
	// 主机带空格：类型/端口校验通过，最终 ParseProxyURL 失败。
	snapshot := &proxyTestSnapshot{ProxyType: "http", ProxyHost: "a b", ProxyPort: 8080}
	_, err := fixture.store.proxyTestURL(snapshot)
	if err == nil || !strings.Contains(err.Error(), "代理 URL 无效") {
		t.Fatalf("err = %v, want 代理 URL 无效", err)
	}
}

func TestW9FProxyTestURLValidatesTypeHostPortAndPassword(t *testing.T) {
	fixture := newProxyFixture(t)
	cases := []struct {
		name     string
		snapshot *proxyTestSnapshot
		want     string
	}{
		{"类型无效", &proxyTestSnapshot{ProxyType: "ftp", ProxyHost: "h", ProxyPort: 1}, "代理类型无效"},
		{"空主机", &proxyTestSnapshot{ProxyType: "http", ProxyHost: " ", ProxyPort: 1}, "代理主机或端口无效"},
		{"端口为零", &proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 0}, "代理主机或端口无效"},
		{"端口超界", &proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 65536}, "代理主机或端口无效"},
		{"密码缺用户名", &proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 1, PasswordEncrypted: "x"}, "代理密码缺少用户名"},
	}
	for _, item := range cases {
		if _, err := fixture.store.proxyTestURL(item.snapshot); err == nil || !strings.Contains(err.Error(), item.want) {
			t.Fatalf("%s: err = %v, want %s", item.name, err, item.want)
		}
	}
	// 密码信封解密失败（密文非法）。
	bad := &proxyTestSnapshot{ProxyType: "http", ProxyHost: "h", ProxyPort: 1, ProxyUsername: "u", PasswordEncrypted: "not-an-envelope"}
	if _, err := fixture.store.proxyTestURL(bad); err == nil {
		t.Fatal("bad envelope should fail")
	}
	// 信封里密码为空。
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
		VALUES ('px_env', 'acc', 'envelope', 'http', 'h', 1, 'u', '', 1, 'unknown', '2026-09-04T12:00:00.000Z', '2026-09-04T12:00:00.000Z')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 密码字段缺失/空串路径：username-only URL。
	snapshot := &proxyTestSnapshot{ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 1, ProxyUsername: "u"}
	got, err := fixture.store.proxyTestURL(snapshot)
	if err != nil || !strings.HasPrefix(got, "http://u@127.0.0.1:1") {
		t.Fatalf("url = %q, err = %v", got, err)
	}
}

func TestW9FNewProxyTestTransportRequiresProxy(t *testing.T) {
	if _, err := newProxyTestTransport("   ", time.Second); err == nil || !strings.Contains(err.Error(), "proxy required") {
		t.Fatalf("err = %v, want proxy required", err)
	}
	if _, err := newProxyTestTransport("ftp://h", time.Second); err == nil || !strings.Contains(err.Error(), "proxy URL invalid") {
		t.Fatalf("err = %v, want proxy URL invalid", err)
	}
}

func TestW9FProbeProxyTargetInvalidProxyConfiguration(t *testing.T) {
	// proxy required / scheme unsupported → configuration code 分类。
	result := probeProxyTarget(context.Background(), "http://127.0.0.1:9/", "   ", time.Second)
	if result.Status != proxyTestItemUnknown || result.ErrorCode != "proxy_required" {
		t.Fatalf("result = %+v, want proxy_required", result)
	}
	result = probeProxyTarget(context.Background(), "http://127.0.0.1:9/", "ftp://h", time.Second)
	if result.ErrorCode != "proxy_invalid" {
		t.Fatalf("result = %+v, want proxy_invalid", result)
	}
	// 非法目标 URL。
	result = probeProxyTarget(context.Background(), "::::", "http://127.0.0.1:9", time.Second)
	if result.ErrorCode != proxyTestTargetErrorInvalidURL {
		t.Fatalf("result = %+v, want target_url_invalid", result)
	}
	// 非法超时。
	result = probeProxyTarget(context.Background(), "http://127.0.0.1:9/", "http://127.0.0.1:9", 0)
	if result.ErrorCode != "timeout_invalid" {
		t.Fatalf("result = %+v, want timeout_invalid", result)
	}
	// 已取消的 ctx 在入口被拒。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = probeProxyTarget(ctx, "http://127.0.0.1:9/", "http://127.0.0.1:9", time.Second)
	if result.ErrorCode != "probe_cancelled" {
		t.Fatalf("result = %+v, want probe_cancelled", result)
	}
}

func TestW9FTransportFailureClassification(t *testing.T) {
	timeoutErr := &w9fTimeoutNetError{}
	dnsErr := &net.DNSError{Err: "no such host", Name: "h.test"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"net timeout", timeoutErr, "timeout"},
		{"dns", dnsErr, "dns"},
		{"eof", io.EOF, "early_eof"},
		{"unexpected eof", io.ErrUnexpectedEOF, "early_eof"},
		{"other", errors.New("boom"), "transport"},
	}
	for _, item := range cases {
		if got := proxyTestTransportFailureResult(item.err).ErrorCode; got != item.want {
			t.Fatalf("%s: code = %s, want %s", item.name, got, item.want)
		}
	}
	// 响应读取失败分类：只有 timeout 与 early_eof 两类。
	if got := proxyTestResponseReadFailureResult(context.DeadlineExceeded).ErrorCode; got != "timeout" {
		t.Fatalf("read deadline code = %s", got)
	}
	if got := proxyTestResponseReadFailureResult(timeoutErr).ErrorCode; got != "timeout" {
		t.Fatalf("read net-timeout code = %s", got)
	}
	if got := proxyTestResponseReadFailureResult(errors.New("x")).ErrorCode; got != "early_eof" {
		t.Fatalf("read default code = %s", got)
	}
	if got := proxyTestConfigurationCode(errors.New("proxy required")); got != "proxy_required" {
		t.Fatalf("config code = %s", got)
	}
	if got := proxyTestConfigurationCode(errors.New("proxy URL invalid")); got != "proxy_invalid" {
		t.Fatalf("config code = %s", got)
	}
}

// w9fTimeoutNetError 同时满足 net.Error 且 Timeout()=true。
type w9fTimeoutNetError struct{}

func (w9fTimeoutNetError) Error() string   { return "i/o timeout" }
func (w9fTimeoutNetError) Timeout() bool   { return true }
func (w9fTimeoutNetError) Temporary() bool { return true }

// w9fInsertRealProxy 在真实 schema 的 fixture 中插入一行可探测代理。
func w9fInsertRealProxy(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at) VALUES ('px1', 'acc', '代理', 'http', '127.0.0.1', '1', 1, 'unknown', '2026-09-04T12:00:00.000Z', '2026-09-04T12:00:00.000Z')`); err != nil {
		t.Fatalf("insert proxy: %v", err)
	}
}

// TestW9FRunProxyTestCancelledBeforeTargets 覆盖循环入口的 ctx 检查。
func TestW9FRunProxyTestCancelledBeforeTargets(t *testing.T) {
	fixture := newProxyTestFixture(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	w9fInsertProviderTarget(t, fixture.db, "pv1", "openai", "OpenAI", "prof1")
	w9fInsertRealProxy(t, fixture.db)
	snapshot, err := fixture.store.LoadProxyTestSnapshot(context.Background(), "px1")
	if err != nil || snapshot == nil {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
	snapshot.Targets[0].URL = target.URL + "/"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.store.runProxyTest(ctx, snapshot, fixture.now); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestW9FRunProxyTestCancelledMidProbe：目标服务端在探测中途取消 ctx →
// client.Do 报 canceled，循环结束后 ctx.Err() 终止整个 run。
func TestW9FRunProxyTestCancelledMidProbe(t *testing.T) {
	fixture := newProxyTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer target.Close()
	w9fInsertProviderTarget(t, fixture.db, "pv1", "openai", "OpenAI", "prof1")
	w9fInsertRealProxy(t, fixture.db)
	snapshot, err := fixture.store.LoadProxyTestSnapshot(context.Background(), "px1")
	if err != nil || snapshot == nil || len(snapshot.Targets) == 0 {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
	snapshot.Targets[0].URL = target.URL + "/"
	// 代理指向本地正向代理桩，确保探测真实抵达目标后再被取消。
	proxyServer, _ := forwardProxyStub(t)
	host, port := proxyHostPort(proxyServer.URL)
	snapshot.ProxyHost = host
	snapshot.ProxyPort = port
	report, runErr := fixture.store.runProxyTest(ctx, snapshot, fixture.now)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("err = %v, report = %+v, want context.Canceled", runErr, report)
	}
}

// ---------------------------------------------------------------------------
// 纯函数：报告聚合 / 消息 / 目标 URL 解析
// ---------------------------------------------------------------------------

func TestW9FSummarizeCoversAllGradesAndClamp(t *testing.T) {
	passed := func(latency int64) proxyTestReportItem {
		return proxyTestReportItem{Name: "x", Status: proxyTestItemPassed, LatencyMS: &latency}
	}
	latency := int64(100)
	if _, score, p, w, f, grade := proxyTestSummarize([]proxyTestReportItem{passed(100)}); score != 100 || grade != "A" || p != 1 || w != 0 || f != 0 {
		t.Fatalf("A: score=%d grade=%s p=%d w=%d f=%d", score, grade, p, w, f)
	}
	warningItem := proxyTestReportItem{Name: "x", Status: proxyTestItemWarning}
	if _, score, _, _, _, grade := proxyTestSummarize([]proxyTestReportItem{passed(100), warningItem}); score != 90 || grade != "A" {
		t.Fatalf("A-with-warning: score=%d grade=%s", score, grade)
	}
	if _, score, _, _, _, grade := proxyTestSummarize([]proxyTestReportItem{passed(100), warningItem, warningItem}); score != 80 || grade != "B" {
		t.Fatalf("B: score=%d grade=%s", score, grade)
	}
	if _, score, _, _, _, grade := proxyTestSummarize([]proxyTestReportItem{passed(100), warningItem, warningItem, warningItem}); score != 70 || grade != "C" {
		t.Fatalf("C: score=%d grade=%s", score, grade)
	}
	failed := proxyTestReportItem{Name: "x", Status: proxyTestItemFailed}
	if _, score, _, _, _, grade := proxyTestSummarize([]proxyTestReportItem{passed(100), {Name: "x", Status: proxyTestItemFailed, LatencyMS: &latency}, failed, failed}); score != 0 || grade != "D" {
		t.Fatalf("clamp D: score=%d grade=%s", score, grade)
	}
	unknownItem := proxyTestReportItem{Name: "x", Status: proxyTestItemUnknown}
	if status, score, _, _, _, _ := proxyTestSummarize([]proxyTestReportItem{unknownItem}); status != proxyTestItemUnknown || score != 0 {
		t.Fatalf("unknown: status=%s score=%d", status, score)
	}
	if status, _, _, _, _, _ := proxyTestSummarize(nil); status != proxyTestItemUnknown {
		t.Fatalf("empty status = %s", status)
	}
	// passed+unknown 混合 → warning。
	if status, _, _, _, _, _ := proxyTestSummarize([]proxyTestReportItem{passed(100), unknownItem}); status != proxyTestItemWarning {
		t.Fatalf("mixed status = %s", status)
	}
}

func TestW9FSyntheticBaseAveragesLatencyAndMessages(t *testing.T) {
	// 无 provider → unknown 基础项。
	base := proxyTestSyntheticBase(nil, 0)
	if base.Status != proxyTestItemUnknown || !strings.Contains(base.Message, "没有启用") {
		t.Fatalf("base = %+v", base)
	}
	latency := int64(90)
	items := []proxyTestReportItem{
		{Name: "a", Status: proxyTestItemPassed, LatencyMS: &latency},
		{Name: "b", Status: proxyTestItemFailed},
	}
	base = proxyTestSyntheticBase(items, 2)
	if base.Status != proxyTestItemWarning || base.LatencyMS == nil || *base.LatencyMS != 90 {
		t.Fatalf("warning base = %+v", base)
	}
	if !strings.Contains(base.Message, "部分供应商") {
		t.Fatalf("warning message = %q", base.Message)
	}
	// 全 warning 项不计入任何计数：failed/unknown 均为 0 → passed。
	base = proxyTestSyntheticBase([]proxyTestReportItem{{Name: "w", Status: proxyTestItemWarning}}, 1)
	if base.Status != proxyTestItemPassed {
		t.Fatalf("warning-only base = %+v", base)
	}
	// 全部 passed → 全可达消息。
	base = proxyTestSyntheticBase(items[:1], 1)
	if base.Status != proxyTestItemPassed || !strings.Contains(base.Message, "全部") {
		t.Fatalf("passed base = %+v", base)
	}
	// 全部 failed → 失败消息。
	failedItems := []proxyTestReportItem{{Name: "f", Status: proxyTestItemFailed}}
	base = proxyTestSyntheticBase(failedItems, 1)
	if base.Status != proxyTestItemFailed || !strings.Contains(base.Message, "传输失败") {
		t.Fatalf("failed base = %+v", base)
	}
	// 全部 unknown → 未知消息。
	base = proxyTestSyntheticBase([]proxyTestReportItem{{Name: "u", Status: proxyTestItemUnknown}}, 1)
	if base.Status != proxyTestItemUnknown || !strings.Contains(base.Message, "未形成真实传输") {
		t.Fatalf("unknown base = %+v", base)
	}
}

func TestW9FReportAndItemMessages(t *testing.T) {
	if got := proxyTestReportMessage(proxyTestItemPassed, 0, 0); got != "代理质量检测通过" {
		t.Fatalf("passed message = %q", got)
	}
	if got := proxyTestReportMessage(proxyTestItemWarning, 0, 2); !strings.Contains(got, "2") {
		t.Fatalf("warning message = %q", got)
	}
	if got := proxyTestReportMessage(proxyTestItemFailed, 3, 0); !strings.Contains(got, "3") {
		t.Fatalf("failed message = %q", got)
	}
	if got := proxyTestReportMessage(proxyTestItemUnknown, 0, 0); !strings.Contains(got, "未形成") {
		t.Fatalf("unknown message = %q", got)
	}
	status := 200
	if got := proxyTestItemMessage(probeItemResult{HTTPStatus: status}, "http://x"); !strings.Contains(got, "200") {
		t.Fatalf("http message = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{ErrorCode: proxyTestTargetErrorInvalidURL}, "ftp:"); !strings.Contains(got, "Invalid URL") {
		t.Fatalf("legacy message = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{ErrorCode: "timeout"}, "http://x"); got != "timeout" {
		t.Fatalf("code message = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemFailed}, "http://x"); got != "代理传输失败" {
		t.Fatalf("failed message = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemUnknown}, "http://x"); got != "未形成真实代理检测请求" {
		t.Fatalf("unknown message = %q", got)
	}
	if got := proxyTestItemMessage(probeItemResult{Status: proxyTestItemPassed}, "http://x"); got != "代理目标检测完成" {
		t.Fatalf("passed message = %q", got)
	}
	// legacy 协议消息：mailto 有 opaque → 报协议名；ftp: 无 authority → Invalid URL。
	if got := proxyTestItemMessage(probeItemResult{ErrorCode: proxyTestTargetErrorInvalidURL}, "mailto:a@b"); !strings.Contains(got, "mailto") {
		t.Fatalf("mailto message = %q", got)
	}
	if protocol, ok := legacyProxyTestUnsupportedProtocol("http://x"); ok {
		t.Fatalf("http protocol = %q, want none", protocol)
	}
}

func TestW9FLegacyUnsupportedProtocolEdges(t *testing.T) {
	// url.Parse 失败。
	if _, ok := legacyProxyTestUnsupportedProtocol("://bad"); ok {
		t.Fatal("parse failure must not be reported as protocol")
	}
	// 需要 authority 但缺失（ftp:）→ 不报协议。
	if _, ok := legacyProxyTestUnsupportedProtocol("ftp:"); ok {
		t.Fatal("authority-less ftp must not be reported")
	}
	// ws 需要 authority，路径存在则报协议。
	if protocol, ok := legacyProxyTestUnsupportedProtocol("ws:/path"); !ok || protocol != "ws" {
		t.Fatalf("ws = %q, %v", protocol, ok)
	}
}

func TestW9FParseTargetURLAcceptsCanonicalForms(t *testing.T) {
	parsed, err := parseProxyTestTargetURL("HTTP://Example.COM:80")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if parsed.String() != "http://example.com/" {
		t.Fatalf("canonical = %q", parsed.String())
	}
	if _, err := parseProxyTestTargetURL("https://example.com:443/a"); err != nil {
		t.Fatalf("https default port: %v", err)
	}
	if _, err := parseProxyTestTargetURL("http://127.0.0.1:8080/x"); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if _, err := parseProxyTestTargetURL("http://[::1]:9000/"); err != nil {
		t.Fatalf("ipv6: %v", err)
	}
}

func TestW9FParseTargetURLRejectsInvalidForms(t *testing.T) {
	cases := []string{
		"",
		`http://a\\b`,
		"http://a/\x01",
		"   ",
		"://bad",
		"ftp://example.com",
		"http:/example.com",
		"http:example.com",
		"http://u@example.com",
		"http://example.com/?a=1",
		"http://example.com#frag",
		"http://exa%20mple.com",
		"http://bad..host",
		"http://.leading.dot",
		"http://host..name",
		"http://-label.example.com",
		"http://label-.example.com",
		"http://label_.example.com",
		"http://example.com:99999",
		"http://example.com:0",
		"http://example.com:abc",
		"http://[::1",
		"http://[::1]:",
		"http://[::1]x",
		"http://a::1",
		"http://a:",
		"http://a/x//y",
		"http://a/%2f",
		"http://a/%5C",
		"http://a/../b",
		"http://a/./b",
		"http://a/%2e/b",
		strings.Repeat("http://a.b.c.d.e.f.g.h.i.j.k.l.m.n.o.p.q.r.s.t.u.v.w.x.y.z.", 6) + "com",
	}
	for _, raw := range cases {
		if got, err := parseProxyTestTargetURL(raw); err == nil {
			t.Fatalf("parseProxyTestTargetURL(%q) = %v, want error", raw, got)
		}
	}
}

// ---------------------------------------------------------------------------
// reads.go：分页钳制、扫描错误、选项合并排序
// ---------------------------------------------------------------------------

func TestW9FListPageClampsPageAndSize(t *testing.T) {
	fixture := newProxyTestFixture(t)
	result, err := fixture.store.ListPage(context.Background(), -5, 0, "")
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if result.Page != 1 || result.PageSize != 1 {
		t.Fatalf("page=%d size=%d, want 1/1", result.Page, result.PageSize)
	}
	result, err = fixture.store.ListPage(context.Background(), 99999, 500, "")
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if result.PageSize != 200 || result.Page != 5 {
		t.Fatalf("page=%d size=%d, want 5/200", result.Page, result.PageSize)
	}
}

func TestW9FListPageScanErrorOnBadPort(t *testing.T) {
	fixture := newW9FBrokenFixture(t)
	w9fInsertBrokenProxy(t, fixture.db, "px_bad", "坏端口", nil)
	if _, err := fixture.store.ListPage(context.Background(), 1, 20, ""); err == nil {
		t.Fatal("expected scan error from non-numeric port")
	}
}

func TestW9FScanProfileSurfacesScanError(t *testing.T) {
	fixture := newProxyFixture(t)
	rows, err := fixture.db.Query(`SELECT 'id','name',NULL,'http','h',1,'u',1,'unknown',NULL,NULL,NULL,NULL,NULL,NULL`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	covered := false
	for rows.Next() {
		if _, scanErr := fixture.store.scanProfile(rows); scanErr != nil {
			covered = true
		}
	}
	if !covered {
		t.Fatal("expected a scan error from NULL updated_at")
	}
}

func TestW9FListOptionsClampsLimitAndScans(t *testing.T) {
	fixture := newProxyFixture(t)
	for _, name := range []string{"alpha", "beta"} {
		if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at) VALUES (?, 'acc', ?, 'http', 'h', 1, 1, 'unknown', '2026-09-04T12:00:00.000Z', '2026-09-04T12:00:00.000Z')`, "px_"+name, name); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	few, err := fixture.store.ListOptions(context.Background(), "", 0, nil)
	if err != nil || len(few) != 1 {
		t.Fatalf("limit clamp min (0 -> 1): n=%d err=%v", len(few), err)
	}
	many, err := fixture.store.ListOptions(context.Background(), "", 100, []string{"px_alpha"})
	if err != nil || len(many) != 2 {
		t.Fatalf("limit clamp max + selected merge: n=%d err=%v", len(many), err)
	}
	empty, err := fixture.store.ListOptions(context.Background(), "", 10, []string{"missing"})
	if err != nil || len(empty) != 2 {
		t.Fatalf("selected miss: n=%d err=%v", len(empty), err)
	}
}

func TestW9FListOptionsScanErrorOnNullUpdatedAt(t *testing.T) {
	fixture := newW9FBrokenFixture(t)
	// 窗口扫描错误：updated_at NULL。
	if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, name, type, host, port, test_status, updated_at) VALUES ('px1', 'a', 'http', 'h', 1, 'unknown', NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := fixture.store.ListOptions(context.Background(), "", 10, nil); err == nil {
		t.Fatal("window scan error expected")
	}
	// 选中扫描错误：窗口（按关键字）不命中该行，selectedIds 命中。
	if _, err := fixture.store.ListOptions(context.Background(), "zzz", 10, []string{"px1"}); err == nil {
		t.Fatal("selected scan error expected")
	}
}

func TestW9FMergeOptionRowsSortsNameThenRevisionThenID(t *testing.T) {
	out := mergeProxyOptionRows(
		[]optionRow{{id: "2", name: "b", typeCode: "http", updatedAt: "2026-01-01"}},
		[]optionRow{{id: "1", name: "a", typeCode: "http", updatedAt: "2026-01-02"}})
	if len(out) != 2 || out[0].ID != "1" || out[1].ID != "2" {
		t.Fatalf("name order = %+v", out)
	}
	// 同名按 updated_at DESC。
	out = mergeProxyOptionRows(
		[]optionRow{{id: "old", name: "a", typeCode: "http", updatedAt: "2026-01-01"}},
		[]optionRow{{id: "new", name: "a", typeCode: "http", updatedAt: "2026-01-02"}})
	if out[0].ID != "new" {
		t.Fatalf("revision order = %+v", out)
	}
	// 同名同版本按 id ASC。
	out = mergeProxyOptionRows(
		[]optionRow{{id: "z", name: "a", typeCode: "http", updatedAt: "2026-01-01"}},
		[]optionRow{{id: "y", name: "a", typeCode: "http", updatedAt: "2026-01-01"}})
	if out[0].ID != "y" {
		t.Fatalf("id order = %+v", out)
	}
}

func TestW9FClampAndTextHelpers(t *testing.T) {
	if clampInt(-1, 1, 200) != 1 || clampInt(999, 1, 200) != 200 || clampInt(5, 1, 200) != 5 {
		t.Fatal("clampInt edges")
	}
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("prefix bound = %q", got)
	}
	if got := textPrefixUpperBound("\U0010ffff"); got != "\U0010ffff\U0010ffff" {
		t.Fatalf("max rune bound = %q", got)
	}
	if got := textPrefixUpperBound(""); got != "\U0010ffff" {
		t.Fatalf("empty bound = %q", got)
	}
	if got := placeholders(0); got != "?" {
		t.Fatalf("placeholders(0) = %q", got)
	}
	if got := placeholders(3); got != "?,?,?" {
		t.Fatalf("placeholders(3) = %q", got)
	}
	if len(idsToAny([]string{"a", "b"})) != 2 {
		t.Fatal("idsToAny length")
	}
}

// ---------------------------------------------------------------------------
// store.go：构造器 / 小工具
// ---------------------------------------------------------------------------

func TestW9FNewStoreValidation(t *testing.T) {
	if _, err := NewStore(Deps{}); err == nil {
		t.Fatal("nil db must fail")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(Deps{DB: db, NewID: func(string) string { return "x" }})
	if err != nil || store == nil {
		t.Fatalf("default Now: err=%v", err)
	}
	if _, err := NewStore(Deps{DB: db}); err == nil || !strings.Contains(err.Error(), "id generator") {
		t.Fatalf("missing NewID: err=%v", err)
	}
}

func TestW9FStoreSmallHelpers(t *testing.T) {
	if itoa(0) != "0" || itoa(57) != "57" {
		t.Fatal("itoa")
	}
	if isProxyNameDuplicate(nil) {
		t.Fatal("nil error is not duplicate")
	}
	if !isProxyNameDuplicate(errors.New("UNIQUE constraint failed: proxy_profiles.name")) {
		t.Fatal("modernc duplicate shape")
	}
	if !isProxyNameDuplicate(errors.New("idx_proxy_profiles_name_unique failed")) {
		t.Fatal("index duplicate shape")
	}
	// InUseError：lower bound 与 名称截断 后缀。
	err := &InUseError{AccountCount: 9, AccountNames: []string{"a", "b"}, AccountCountIsLowerBound: true}
	text := err.Error()
	if !strings.Contains(text, "至少 9") || !strings.Contains(text, "a、b 等") {
		t.Fatalf("InUseError = %q", text)
	}
	err = &InUseError{AccountCount: 5, AccountNames: []string{"a"}}
	if !strings.Contains(err.Error(), "5 个账户使用") {
		t.Fatalf("InUseError plain = %q", err.Error())
	}
	if (&InUseError{AccountCount: 1}).Error() == "" {
		t.Fatal("empty names message")
	}
}

// ---------------------------------------------------------------------------
// writes.go：创建/更新错误分支
// ---------------------------------------------------------------------------

func TestW9FCreateSurfacesNonDuplicateError(t *testing.T) {
	fixture := newProxyFixture(t)
	db := fixture.db
	_ = db.Close()
	_, err := fixture.store.Create(context.Background(), proxyInput{Name: strPtr("x"), Type: strPtr("http"), Host: strPtr("h"), Port: intPtr(1)}, "acc")
	if err == nil || isProxyNameDuplicate(err) {
		t.Fatalf("err = %v, want closed-db non-duplicate error", err)
	}
}

func TestW9FPatchLoadErrorAndDescriptionUsernameOverExisting(t *testing.T) {
	fixture := newProxyFixture(t)
	ctx := context.Background()
	description := "原始说明"
	username := "user1"
	created, err := fixture.store.Create(ctx, proxyInput{Name: strPtr("p1"), Type: strPtr("http"), Host: strPtr("127.0.0.1"), Port: intPtr(1), HasDescription: true, Description: &description, HasUsername: true, Username: &username}, "acc")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newDescription := "新说明"
	newUsername := "user2"
	outcome, err := fixture.store.Patch(ctx, created.ID, proxyInput{HasDescription: true, Description: &newDescription, HasUsername: true, Username: &newUsername, ExpectedUpdatedAt: created.UpdatedAt})
	if err != nil || outcome == nil || !outcome.Mutation.Changed {
		t.Fatalf("patch = %+v, err = %v", outcome, err)
	}
	if got := outcome.After["description"]; got != "新说明" {
		t.Fatalf("after description = %v", got)
	}
	// 当前密码为空时 before.password 必须是 nil。
	secretStore := fixture.store
	outcome, err = secretStore.Patch(ctx, created.ID, proxyInput{HasPassword: true, Password: strPtr("pw1"), ExpectedUpdatedAt: outcome.Mutation.UpdatedAt})
	if err != nil || outcome == nil || !outcome.PasswordChanged {
		t.Fatalf("password patch = %+v, err = %v", outcome, err)
	}
	if before, ok := outcome.Before["password"]; !ok || before != nil {
		t.Fatalf("before password = %v, want nil", before)
	}
	// 已有密码时 before.password 必须是 [已设置]。
	outcome, err = secretStore.Patch(ctx, created.ID, proxyInput{HasPassword: true, Password: strPtr("pw2"), ExpectedUpdatedAt: outcome.Mutation.UpdatedAt})
	if err != nil || outcome.Before["password"] != "[已设置]" {
		t.Fatalf("second password patch = %+v, err = %v", outcome, err)
	}
	_ = outcome
	db := fixture.db
	_ = db.Close()
	if _, err := fixture.store.Patch(ctx, created.ID, proxyInput{ExpectedUpdatedAt: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("closed-db patch load should fail")
	}
}

// ---------------------------------------------------------------------------
// routes.go：请求值解析 / 输入解析 / 直驱 handler 分支
// ---------------------------------------------------------------------------

func TestW9FIntegerQueryValueJSGrammar(t *testing.T) {
	if value, ok := jsStringToNumber("-Infinity"); !ok || !math.IsInf(value, -1) {
		t.Fatalf("-Infinity = %v, %v", value, ok)
	}
	if value, ok := jsStringToNumber("Infinity"); !ok || !math.IsInf(value, 1) {
		t.Fatalf("Infinity = %v, %v", value, ok)
	}
	if _, ok := jsStringToNumber("-"); ok {
		t.Fatal("lone sign must be absent")
	}
	if _, ok := jsStringToNumber("-Infinityx"); ok {
		t.Fatal("Infinityx must be NaN")
	}
	if _, ok := integerQueryValue("-Infinity"); ok {
		t.Fatal("integer query must reject Infinity")
	}
	if value, ok := integerQueryValue("1e-400"); !ok || value != 0 {
		t.Fatalf("1e-400 = %v, %v", value, ok)
	}
	if _, ok := integerQueryValue("1e400"); ok {
		t.Fatal("1e400 overflows to Inf and must be absent")
	}
	if isDecimalNumericLiteral("1e+") || isDecimalNumericLiteral("1e") || isDecimalNumericLiteral(".") || isDecimalNumericLiteral("0x1") {
		t.Fatal("decimal literal gate failed")
	}
	if !isDecimalNumericLiteral("1.5e-3") {
		t.Fatal("valid exponent rejected")
	}
	if got := intFromQuery(1e300, true, 7); got != int(^uint(0)>>1) {
		t.Fatalf("saturate max = %d", got)
	}
	if got := intFromQuery(-1e300, true, 7); got != -int(^uint(0)>>1)-1 {
		t.Fatalf("saturate min = %d", got)
	}
	if got := intFromQuery(0, false, 7); got != 7 {
		t.Fatalf("absent fallback = %d", got)
	}
	if got := normalizeSafeValue(make(chan int)); got != "" {
		t.Fatalf("unmarshalable value = %q", got)
	}
	if got := normalizeSafeValue(strings.Repeat("x", 300)); len(got) != 200 {
		t.Fatalf("truncate = %d", len(got))
	}
}

func TestW9FParseSelectedProxyOptionIdsEdges(t *testing.T) {
	if _, bad := parseSelectedProxyOptionIds(url.Values{"selectedIds[0]": {"x"}}); bad == "" {
		t.Fatal("indexed key must fail")
	}
	ids, bad := parseSelectedProxyOptionIds(url.Values{"selectedIds": {"", " b "}, "selectedIds[]": {"b"}})
	if bad != "" || len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("ids = %v, bad = %q", ids, bad)
	}
	if _, bad = parseSelectedProxyOptionIds(url.Values{"selectedIds": {"a,b"}}); bad == "" {
		t.Fatal("comma must fail")
	}
	if _, bad = parseSelectedProxyOptionIds(url.Values{"selectedIds": {"[x]"}}); bad == "" {
		t.Fatal("bracket prefix must fail")
	}
	many := url.Values{}
	for i := 0; i < 21; i++ {
		many.Add("selectedIds", strings.Repeat("x", 1)+string(rune('a'+i)))
	}
	if _, bad = parseSelectedProxyOptionIds(many); bad == "" {
		t.Fatal(">20 ids must fail")
	}
	if _, bad = parseSelectedProxyOptionIds(url.Values{"selectedIds": {strings.Repeat("x", 121)}}); bad == "" {
		t.Fatal(">120 chars must fail")
	}
}

func TestW9FParseProxyInputRejectsBadPasswordAndNormalizer(t *testing.T) {
	if _, bad := parseProxyInput(map[string]any{"password": 5}, false); bad == "" {
		t.Fatal("non-string password must fail")
	}
	// 空白名称在路由层前置检查被拒。
	if _, bad := parseProxyInput(map[string]any{"name": "   ", "type": "http", "host": "h", "port": float64(1)}, false); bad == "" {
		t.Fatal("blank name must fail")
	}
	// 合法前置 + normalize 失败（描述超 200 rune）→ 通用 400。
	if _, bad := parseProxyInput(map[string]any{"name": "x", "type": "http", "host": "h", "port": float64(1), "description": strings.Repeat("长", 201)}, false); bad == "" {
		t.Fatal("long description via normalizer must fail")
	}
	// update 变体：expectedUpdatedAt 缺失 / 非 RFC3339 / 纯版本无载荷。
	if _, bad := parseProxyInput(map[string]any{"name": "x"}, true); bad == "" {
		t.Fatal("missing expectedUpdatedAt must fail")
	}
	if _, bad := parseProxyInput(map[string]any{"name": "x", "expectedUpdatedAt": "not-rfc3339"}, true); bad == "" {
		t.Fatal("bad expectedUpdatedAt must fail")
	}
	if _, bad := parseProxyInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z"}, true); bad == "" {
		t.Fatal("version-only patch must fail")
	}
}

func TestW9FCreateAndPatchHandlerDirectBranches(t *testing.T) {
	fixture := newProxyTestFixture(t)
	// 未认证 create → 401。
	recorder := httptest.NewRecorder()
	createHandler(recorder, httptest.NewRequest(http.MethodPost, "/x", nil), fixture.store, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("create auth = %d", recorder.Code)
	}
	// 已认证 + 非法补丁体 → 400。
	authed := func(r *http.Request) *http.Request {
		return r.WithContext(authsys.WithAuthContext(r.Context(), fixture.auth("admin")))
	}
	// 直驱 handler 需手动补 Content-Length（kernel.hasRequestBody 读的是 header）。
	jsonBody := func(method, body string) *http.Request {
		request := httptest.NewRequest(method, "/x", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return request
	}
	request := authed(jsonBody(http.MethodPatch, `{"unknownKey":1}`))
	request.SetPathValue("id", "px1")
	recorder = httptest.NewRecorder()
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch bad body = %d", recorder.Code)
	}
	// 非法 JSON → DecodeJSON 失败分支。
	malformed := authed(jsonBody(http.MethodPatch, `{`))
	malformed.SetPathValue("id", "px1")
	recorder = httptest.NewRecorder()
	patchHandler(recorder, malformed, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed patch = %d", recorder.Code)
	}
	// 创建。
	request = authed(jsonBody(http.MethodPost, `{"name":"代理","type":"http","host":"127.0.0.1","port":8080}`))
	recorder = httptest.NewRecorder()
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// 重名创建 → store 层 DuplicateNameError → writeMutationError 409。
	request = authed(jsonBody(http.MethodPost, `{"name":"代理","type":"http","host":"127.0.0.1","port":8080}`))
	recorder = httptest.NewRecorder()
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	created := fixture.store
	summary, err := created.ListPage(context.Background(), 1, 20, "")
	if err != nil || len(summary.Items) != 1 {
		t.Fatalf("list = %+v, err = %v", summary, err)
	}
	body := `{"expectedUpdatedAt":"` + summary.Items[0].UpdatedAt + `","password":"pw9"}`
	request = authed(jsonBody(http.MethodPatch, body))
	request.SetPathValue("id", summary.Items[0].ID)
	recorder = httptest.NewRecorder()
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	found := false
	for _, entry := range fixture.sink.entries {
		for _, change := range entry.Changes {
			if change.Field == "password" && change.Sensitive {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("password sensitive change missing in sink")
	}
}

// ---------------------------------------------------------------------------
// Mount：真实内核 + 开发自动登录，逐端点打穿注册闭包
// ---------------------------------------------------------------------------

func TestW9FMountServesRouteFamilyViaAutoLogin(t *testing.T) {
	fixture := newProxyTestFixture(t)
	if _, err := fixture.db.Exec(w9fSystemAccountsSchema); err != nil {
		t.Fatalf("system accounts schema: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at) VALUES ('acc1','admin','管理员','admin','active','x','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	accountStore, err := authsys.NewAccountStore(fixture.db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatalf("account store: %v", err)
	}
	deps := &authsys.Deps{Accounts: accountStore, DevAutoLoginUsername: "admin"}
	kern := kernel.New(kernel.Options{CompressionDisabled: true})
	Mount(kern, deps, fixture.store, fixture.sink)
	server := httptest.NewServer(kern.Handler())
	t.Cleanup(server.Close)

	do := func(method, path, body string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}

	if response := do(http.MethodGet, "/__aisys__/api/proxies/options", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("options = %d", response.StatusCode)
	}
	if response := do(http.MethodGet, "/__aisys__/api/proxies?page=1&pageSize=20", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("list = %d", response.StatusCode)
	}
	if response := do(http.MethodPost, "/__aisys__/api/proxies", `{"name":"代理","type":"http","host":"127.0.0.1","port":8080}`); response.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", response.StatusCode)
	}
	// 取 created id / updatedAt 再 PATCH、test、DELETE。
	result, err := fixture.store.ListPage(context.Background(), 1, 20, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list = %+v, err = %v", result, err)
	}
	id := result.Items[0].ID
	if response := do(http.MethodPatch, "/__aisys__/api/proxies/"+id, `{"expectedUpdatedAt":"`+result.Items[0].UpdatedAt+`","description":"d"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", response.StatusCode)
	}
	updated, err := fixture.store.ListPage(context.Background(), 1, 20, "")
	if err != nil || len(updated.Items) != 1 {
		t.Fatalf("relist = %+v, err = %v", updated, err)
	}
	if response := do(http.MethodPost, "/__aisys__/api/proxies/"+id+"/test", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("test = %d", response.StatusCode)
	}
	if response := do(http.MethodDelete, "/__aisys__/api/proxies/"+id, ""); response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", response.StatusCode)
	}
	if len(fixture.sink.entries) == 0 {
		t.Fatal("operation log entries missing")
	}
}

func strPtr(value string) *string { return &value }
func intPtr(value int) *int       { return &value }
func boolPtr(value bool) *bool    { return &value }
