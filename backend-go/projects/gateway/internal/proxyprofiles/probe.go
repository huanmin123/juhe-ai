package proxyprofiles

// Manual proxy test family (POST /__aisys__/api/proxies/{id}/test).
//
// Behavior baseline: the archived Node manual route
// migration-backup/node/j3a-proxy-latency-manual-control-cutover-20260826/
// proxies-manual-test.route.ts (+ proxy-test.contract.ts) with the frozen
// external report schema (J3a migration contract 11.2) and the current Go
// probe semantics in backend-go/projects/jobs/internal/proxylatency
// (manual.go / transport.go / manual_outbound.go / manual_admin.go).
//
// Ownership boundary (J3a contract): periodic execution, outcome projection
// and the proxy_profiles test-state CAS writeback stay exclusive to the jobs
// process. This endpoint only runs the bounded manual probe and renders the
// frozen report; it never writes proxy test state back.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// Item statuses mirror proxylatency ItemStatus (types.go:15-22).
const (
	proxyTestItemPassed  = "passed"
	proxyTestItemWarning = "warning"
	proxyTestItemFailed  = "failed"
	proxyTestItemUnknown = "unknown"
)

// Error codes mirror proxylatency transport.go classification.
const (
	proxyTestTargetErrorInvalidURL = "target_url_invalid"
)

const (
	proxyTestMaxResponseBodyBytes = 512 * 1024
	proxyTestResponseHeaderBytes  = 64 * 1024
	proxyTestUserAgent            = "juhe-ai-proxy-test/0.1"

	// Node diagnostic-task limiter (diagnostic-task-limiter.ts reads
	// runtimeConfig.background.diagnosticTaskMaxInFlight, runtime.ts:813
	// 'JUHE_AI_BACKGROUND_DIAGNOSTIC_TASK_MAX_IN_FLIGHT' default 5, bound
	// 1..1000): one global in-flight cap, fixed Retry-After hint and busy
	// message. The Go side keeps the same env name, default and bound, and
	// clamps out-of-range values instead of failing fast (the same lenient
	// reading as proxyTestDeadline below).
	proxyTestSlotRetryAfter  = "1"
	proxyTestSlotBusyMessage = "诊断任务繁忙，请稍后重试"

	proxyTestSlotCapacityEnv     = "JUHE_AI_BACKGROUND_DIAGNOSTIC_TASK_MAX_IN_FLIGHT"
	proxyTestSlotCapacityDefault = 5
	proxyTestSlotCapacityMin     = 1
	proxyTestSlotCapacityMax     = 1000

	// Node background.proxyManualTestDeadlineMs bounds the whole manual run.
	proxyTestDeadlineEnv     = "JUHE_AI_BACKGROUND_PROXY_MANUAL_TEST_DEADLINE_MS"
	proxyTestDefaultDeadline = 25 * time.Second
	proxyTestMinDeadline     = time.Second
	proxyTestMaxDeadline     = 10 * time.Minute
)

// resolveProxyTestSlotCapacity reads the diagnostic in-flight cap: an
// unparseable value keeps the default 5 and any value outside 1..1000 clamps
// to the nearest bound. It is a pure function so tests can cover the default
// and the clamp edges directly; production resolves it once at package init.
func resolveProxyTestSlotCapacity(raw string) int {
	capacity := int64(proxyTestSlotCapacityDefault)
	if value := strings.TrimSpace(raw); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			capacity = parsed
		}
	}
	if capacity < proxyTestSlotCapacityMin {
		return proxyTestSlotCapacityMin
	}
	if capacity > proxyTestSlotCapacityMax {
		return proxyTestSlotCapacityMax
	}
	return int(capacity)
}

// proxyTestProbeSlots is the in-process diagnostic slot semaphore; the
// capacity resolves once from proxyTestSlotCapacityEnv at package init.
var proxyTestProbeSlots = make(chan struct{}, resolveProxyTestSlotCapacity(os.Getenv(proxyTestSlotCapacityEnv)))

func tryAcquireProxyTestSlot() (func(), bool) {
	select {
	case proxyTestProbeSlots <- struct{}{}:
		return func() { <-proxyTestProbeSlots }, true
	default:
		return nil, false
	}
}

// proxyTestDeadline mirrors Node integerConfig JUHE_AI_BACKGROUND_PROXY_MANUAL_TEST_DEADLINE_MS
// (runtime.ts:831): default 25_000, clamp 1_000..600_000, unparseable keeps
// the default.
var proxyTestDeadline = sync.OnceValue(func() time.Duration {
	deadline := proxyTestDefaultDeadline
	if raw := strings.TrimSpace(os.Getenv(proxyTestDeadlineEnv)); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			deadline = time.Duration(parsed) * time.Millisecond
		}
	}
	if deadline < proxyTestMinDeadline {
		return proxyTestMinDeadline
	}
	if deadline > proxyTestMaxDeadline {
		return proxyTestMaxDeadline
	}
	return deadline
})

// ---------------------------------------------------------------------------
// Snapshot: proxy row + enabled-provider default targets.
// ---------------------------------------------------------------------------

type proxyTestTarget struct {
	Provider  string
	ProfileID string
	Name      string
	URL       string
}

// proxyTestBeforeState carries the persisted test columns the operation log
// diffs against the fresh report (Node runLoggedOperationAsync diff).
type proxyTestBeforeState struct {
	Status         string
	LatencyMS      *int64
	OutboundIP     *string
	OutboundRegion *string
	Message        *string
	TestedAt       *string
}

type proxyTestSnapshot struct {
	ProxyID           string
	ProxyName         string
	ProxyType         string
	ProxyHost         string
	ProxyPort         int
	ProxyUsername     string
	PasswordEncrypted string
	Before            proxyTestBeforeState
	Targets           []proxyTestTarget
}

// LoadProxyTestSnapshot mirrors the jobs management snapshot
// (manual_admin.go manualAdminSnapshotSQL): one proxy row joined against the
// best enabled protocol profile of every enabled provider (enabled first,
// the frozen gemini/glm preferences next, updated_at DESC, id ASC), targets
// ordered by provider name/code. A missing proxy returns (nil, nil). The
// snapshot never filters on proxy enabled: disabled proxies stay testable.
// Stale/deleted providers after the run are the caller's re-check, matching
// the Node route's before/after findProxyAsync pair.
func (s *Store) LoadProxyTestSnapshot(ctx context.Context, id string) (*proxyTestSnapshot, error) {
	lastTestedAtColumn := "p.last_tested_at"
	if s.pg {
		// PG stores timestamptz; textify like manualAdminSnapshotSQL so the
		// scan stays string-typed and the audit diff keeps the same text.
		lastTestedAtColumn = `CASE WHEN p.last_tested_at IS NULL THEN NULL ELSE to_char(p.last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS last_tested_at`
	}
	query := `
WITH selected_targets AS (
  SELECT provider,provider_name,profile_id,target_url
  FROM (
    SELECT p.code AS provider,p.name AS provider_name,ppp.id AS profile_id,ppp.base_url AS target_url,
      row_number() OVER (
        PARTITION BY p.code
        ORDER BY CASE WHEN ppp.enabled = 1 THEN 0 ELSE 1 END,
          CASE WHEN (p.code = 'gemini' AND ppp.id = 'profile_gemini_native_v1beta')
                    OR (p.code = 'glm' AND ppp.id = 'profile_glm_coding_openai_v1') THEN 0 ELSE 1 END,
          ppp.updated_at DESC,ppp.id ASC
      ) AS profile_rank
    FROM ` + s.table("providers") + ` p
    JOIN ` + s.table("provider_protocol_profiles") + ` ppp ON ppp.provider_code = p.code
    WHERE p.enabled = 1
  ) ranked_profiles
  WHERE profile_rank = 1
)
SELECT p.id,p.name,p.type,p.host,p.port,COALESCE(p.username,''),COALESCE(p.password_encrypted,''),
  p.test_status,p.latency_ms,p.outbound_ip,p.outbound_region,p.last_test_message,
  ` + lastTestedAtColumn + `,
  target.provider,target.provider_name,target.profile_id,target.target_url
FROM ` + s.table("proxy_profiles") + ` p
LEFT JOIN selected_targets target ON TRUE
WHERE p.id=?
ORDER BY target.provider_name ASC,target.provider ASC`
	rows, err := s.db.QueryContext(ctx, s.bind(query), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshot *proxyTestSnapshot
	seen := map[string]bool{}
	for rows.Next() {
		var (
			proxyID, name, proxyType, host               string
			port                                         int64
			username, passwordEncrypted                  string
			status                                       string
			latency                                      sql.NullInt64
			outboundIP, outboundRegion, message, tested  sql.NullString
			provider, providerName, profileID, targetURL sql.NullString
		)
		if err := rows.Scan(&proxyID, &name, &proxyType, &host, &port, &username, &passwordEncrypted,
			&status, &latency, &outboundIP, &outboundRegion, &message, &tested,
			&provider, &providerName, &profileID, &targetURL); err != nil {
			return nil, err
		}
		if snapshot == nil {
			snapshot = &proxyTestSnapshot{
				ProxyID:           proxyID,
				ProxyName:         name,
				ProxyType:         proxyType,
				ProxyHost:         host,
				ProxyPort:         int(port),
				ProxyUsername:     username,
				PasswordEncrypted: passwordEncrypted,
				Before:            proxyTestBeforeState{Status: status},
			}
			if latency.Valid {
				value := latency.Int64
				snapshot.Before.LatencyMS = &value
			}
			for _, field := range []struct {
				source sql.NullString
				target **string
			}{{outboundIP, &snapshot.Before.OutboundIP}, {outboundRegion, &snapshot.Before.OutboundRegion},
				{message, &snapshot.Before.Message}, {tested, &snapshot.Before.TestedAt}} {
				if field.source.Valid {
					value := field.source.String
					*field.target = &value
				}
			}
		}
		if !provider.Valid && !providerName.Valid && !profileID.Valid && !targetURL.Valid {
			continue
		}
		if !provider.Valid || !providerName.Valid || !profileID.Valid {
			return nil, errors.New("代理检测 provider target 标识无效")
		}
		if seen[provider.String] {
			return nil, errors.New("代理检测 provider target 重复")
		}
		seen[provider.String] = true
		snapshot.Targets = append(snapshot.Targets, proxyTestTarget{
			Provider: provider.String, ProfileID: profileID.String, Name: providerName.String, URL: targetURL.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// ProxyTestExists is the post-run existence re-check (Node re-ran
// findProxyAsync after the execution; jobs manual_admin.go Exists).
func (s *Store) ProxyTestExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM ` + s.table("proxy_profiles") + ` WHERE id = ?)`
	if err := s.db.QueryRowContext(ctx, s.bind(query), id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// proxyTestURL mirrors jobs proxyURLForIssuedInput (executor.go:186-212): the
// stored type becomes the scheme, credentials only form with a username
// present, and the password envelope decrypts in memory only.
func (s *Store) proxyTestURL(snapshot *proxyTestSnapshot) (string, error) {
	switch snapshot.ProxyType {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("代理类型无效")
	}
	if strings.TrimSpace(snapshot.ProxyHost) == "" || snapshot.ProxyPort < 1 || snapshot.ProxyPort > 65535 {
		return "", errors.New("代理主机或端口无效")
	}
	proxyURL := &url.URL{Scheme: snapshot.ProxyType, Host: net.JoinHostPort(snapshot.ProxyHost, strconv.Itoa(snapshot.ProxyPort))}
	if snapshot.PasswordEncrypted != "" {
		if strings.TrimSpace(snapshot.ProxyUsername) == "" {
			// Node's URL builder only creates credentials when a username is
			// present; never build the invalid :password@ form.
			return "", errors.New("代理密码缺少用户名")
		}
		var envelope struct {
			Password any `json:"password"`
		}
		if err := apikeys.DecryptJSON(s.secret, snapshot.PasswordEncrypted, &envelope); err != nil {
			return "", err
		}
		password, _ := envelope.Password.(string)
		proxyURL.User = url.UserPassword(snapshot.ProxyUsername, password)
	} else if snapshot.ProxyUsername != "" {
		proxyURL.User = url.User(snapshot.ProxyUsername)
	}
	value := proxyURL.String()
	if _, err := upstreamhttp.ParseProxyURL(value); err != nil {
		return "", errors.New("代理 URL 无效")
	}
	return value, nil
}

// ---------------------------------------------------------------------------
// Probe execution and report assembly.
// ---------------------------------------------------------------------------

// probeItemResult mirrors the observable projection of proxylatency
// ItemResult: status plus the optional HTTP status, latency and error code.
type probeItemResult struct {
	Status     string
	HTTPStatus int
	LatencyMS  int64
	ErrorCode  string
}

// proxyTestReport mirrors the frozen ProxyTestReport schema
// (proxy-test.contract.ts; jobs manual.go ProxyTestReport).
type proxyTestReport struct {
	ProxyID        string                `json:"proxyId"`
	ProxyName      string                `json:"proxyName"`
	Score          int                   `json:"score"`
	Grade          string                `json:"grade"`
	Status         string                `json:"status"`
	PassedCount    int                   `json:"passedCount"`
	WarningCount   int                   `json:"warningCount"`
	FailedCount    int                   `json:"failedCount"`
	OutboundIP     string                `json:"outboundIp,omitempty"`
	OutboundRegion string                `json:"outboundRegion,omitempty"`
	BaseLatencyMS  *int64                `json:"baseLatencyMs,omitempty"`
	TestedAt       string                `json:"testedAt"`
	Items          []proxyTestReportItem `json:"items"`
	Message        string                `json:"message"`
}

// proxyTestReportItem: provider items always carry targetUrl; the synthetic
// base item omits it (contract 11.2).
type proxyTestReportItem struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	HTTPStatus *int    `json:"httpStatus,omitempty"`
	LatencyMS  *int64  `json:"latencyMs,omitempty"`
	Message    string  `json:"message"`
	TargetURL  *string `json:"targetUrl,omitempty"`
}

// runProxyTest executes the manual run: sequential target probes under the
// manual deadline, then the manual-only outbound identity probe, then the
// frozen aggregation (jobs runner.go RunManual minus the jobs-store lease /
// outcome / projection machinery, which stays jobs-owned).
func (s *Store) runProxyTest(ctx context.Context, snapshot *proxyTestSnapshot, now time.Time) (proxyTestReport, error) {
	testedAt := proxyTestISOTime(now)
	// Node preserved a 200 unknown report when no enabled provider supplies a
	// target; no proxy URL is built and no outbound probe runs (jobs
	// RunManual no-targets branch, runner.go:302-312).
	if len(snapshot.Targets) == 0 {
		items := []proxyTestReportItem{proxyTestSyntheticBase(nil, 0)}
		return assembleProxyTestReport(snapshot, items, testedAt), nil
	}
	proxyURL, err := s.proxyTestURL(snapshot)
	if err != nil {
		return proxyTestReport{}, err
	}
	deadline := proxyTestDeadline()
	providerItems := make([]proxyTestReportItem, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		if err := ctx.Err(); err != nil {
			return proxyTestReport{}, err
		}
		result := probeProxyTarget(ctx, target.URL, proxyURL, deadline)
		item := proxyTestReportItem{
			Name:    target.Name,
			Status:  result.Status,
			Message: proxyTestItemMessage(result, target.URL),
		}
		if result.HTTPStatus != 0 {
			status := result.HTTPStatus
			item.HTTPStatus = &status
		}
		if result.Status == proxyTestItemPassed {
			latency := result.LatencyMS
			item.LatencyMS = &latency
		}
		itemURL := target.URL
		item.TargetURL = &itemURL
		providerItems = append(providerItems, item)
	}
	if err := ctx.Err(); err != nil {
		return proxyTestReport{}, err
	}
	items := make([]proxyTestReportItem, 0, len(providerItems)+1)
	items = append(items, proxyTestSyntheticBase(providerItems, len(snapshot.Targets)))
	items = append(items, providerItems...)
	report := assembleProxyTestReport(snapshot, items, testedAt)
	// Manual-only outbound identity fallback (jobs manual_outbound.go):
	// fixed target order, HTTP 200 JSON only, best effort.
	if info, ok := probeProxyTestOutbound(ctx, proxyURL, deadline); ok {
		report.OutboundIP = info.IP
		report.OutboundRegion = info.Region
	}
	return report, nil
}

// assembleProxyTestReport mirrors ManualRequest.Report aggregation: base
// item first, provider counts over all items, score/grade/message frozen.
func assembleProxyTestReport(snapshot *proxyTestSnapshot, items []proxyTestReportItem, testedAt string) proxyTestReport {
	status, score, passed, warning, failed, grade := proxyTestSummarize(items)
	var baseLatency *int64
	if len(items) > 0 {
		baseLatency = items[0].LatencyMS
	}
	return proxyTestReport{
		ProxyID: snapshot.ProxyID, ProxyName: snapshot.ProxyName,
		Score: score, Grade: grade, Status: status,
		PassedCount: passed, WarningCount: warning, FailedCount: failed,
		BaseLatencyMS: baseLatency, TestedAt: testedAt, Items: items,
		Message: proxyTestReportMessage(status, failed, warning),
	}
}

// proxyTestISOTime keeps Date#toISOString's fixed UTC millisecond shape
// (jobs manualNodeISOTime).
func proxyTestISOTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}

// probeProxyTarget mirrors jobs transport.go ProbeItem: one GET through the
// proxy, no redirects, Node probe headers, bounded response header budget
// with a full body drain; a complete HTTP response passes regardless of the
// status code (non-2xx/3xx stays passed and is only diagnostic).
func probeProxyTarget(ctx context.Context, rawTargetURL string, rawProxyURL string, timeout time.Duration) probeItemResult {
	if err := ctx.Err(); err != nil {
		return probeTaskFailureResult("probe_cancelled")
	}
	target, err := parseProxyTestTargetURL(rawTargetURL)
	if err != nil {
		return probeTaskFailureResult(proxyTestTargetErrorInvalidURL)
	}
	if timeout <= 0 {
		return probeTaskFailureResult("timeout_invalid")
	}
	transport, err := newProxyTestTransport(rawProxyURL, timeout)
	if err != nil {
		return probeTaskFailureResult(proxyTestConfigurationCode(err))
	}
	defer transport.CloseIdleConnections()
	proxyURL, err := url.Parse(strings.TrimSpace(rawProxyURL))
	if err != nil || proxyURL == nil {
		return probeTaskFailureResult("proxy_invalid")
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return probeTaskFailureResult("request_build_failed")
	}
	applyProxyTestHeaders(request, target, proxyURL)
	client := upstreamhttp.NewClientWithTransport(transport)
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return probeTaskFailureResult("probe_cancelled")
		}
		return proxyTestTransportFailureResult(err)
	}
	defer response.Body.Close()
	if _, err := upstreamhttp.Drain(response.Body); err != nil {
		return proxyTestResponseReadFailureResult(err)
	}
	return probeItemResult{
		Status:     proxyTestItemPassed,
		HTTPStatus: response.StatusCode,
		LatencyMS:  time.Since(started).Milliseconds(),
	}
}

func probeTaskFailureResult(code string) probeItemResult {
	return probeItemResult{Status: proxyTestItemUnknown, ErrorCode: code}
}

// proxyTestTransportFailureResult mirrors transportFailureResult.
func proxyTestTransportFailureResult(err error) probeItemResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "timeout"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "timeout"}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "dns"}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "early_eof"}
	}
	return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "transport"}
}

// proxyTestResponseReadFailureResult mirrors responseReadFailureResult.
func proxyTestResponseReadFailureResult(err error) probeItemResult {
	if errors.Is(err, context.DeadlineExceeded) {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "timeout"}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "timeout"}
	}
	return probeItemResult{Status: proxyTestItemFailed, ErrorCode: "early_eof"}
}

func proxyTestConfigurationCode(err error) string {
	if strings.Contains(err.Error(), "required") {
		return "proxy_required"
	}
	return "proxy_invalid"
}

// proxyTestItemMessage mirrors manual.go itemMessage.
func proxyTestItemMessage(result probeItemResult, targetURL string) string {
	if result.HTTPStatus != 0 {
		return fmt.Sprintf("HTTP %d（传输完整，状态码仅供诊断）", result.HTTPStatus)
	}
	if result.ErrorCode == proxyTestTargetErrorInvalidURL {
		return legacyProxyTestTargetFailureMessage(targetURL)
	}
	if result.ErrorCode != "" {
		return result.ErrorCode
	}
	switch result.Status {
	case proxyTestItemFailed:
		return "代理传输失败"
	case proxyTestItemUnknown:
		return "未形成真实代理检测请求"
	}
	return "代理目标检测完成"
}

// legacyProxyTestTargetFailureMessage restores the Node management response
// for a target that never formed a proxy request (jobs manual.go:257-280).
func legacyProxyTestTargetFailureMessage(rawTargetURL string) string {
	if protocol, ok := legacyProxyTestUnsupportedProtocol(rawTargetURL); ok {
		return fmt.Sprintf("未形成真实代理检测请求：不支持的目标协议：%s:", protocol)
	}
	return "未形成真实代理检测请求：Invalid URL"
}

func legacyProxyTestUnsupportedProtocol(rawTargetURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawTargetURL))
	if err != nil || parsed == nil {
		return "", false
	}
	protocol := strings.ToLower(parsed.Scheme)
	if protocol == "" || protocol == "http" || protocol == "https" {
		return "", false
	}
	if proxyTestTargetRequiresAuthority(protocol) && parsed.Host == "" && parsed.Opaque == "" && parsed.Path == "" {
		return "", false
	}
	return protocol, true
}

func proxyTestTargetRequiresAuthority(protocol string) bool {
	switch protocol {
	case "ftp", "ftps", "ws", "wss":
		return true
	default:
		return false
	}
}

// proxyTestSyntheticBase mirrors manual.go syntheticBase.
func proxyTestSyntheticBase(items []proxyTestReportItem, providerCount int) proxyTestReportItem {
	if providerCount <= 0 {
		return proxyTestReportItem{Name: "基础连通性", Status: proxyTestItemUnknown, Message: "没有启用的供应商默认地址，未形成代理传输检测"}
	}
	failed, unknown, reachable := 0, 0, 0
	var total int64
	latencies := 0
	for _, item := range items {
		switch item.Status {
		case proxyTestItemFailed:
			failed++
		case proxyTestItemUnknown:
			unknown++
		case proxyTestItemPassed:
			reachable++
		}
		if item.Status == proxyTestItemPassed && item.LatencyMS != nil {
			total += *item.LatencyMS
			latencies++
		}
	}
	status := proxyTestItemUnknown
	if failed == 0 && unknown == 0 {
		status = proxyTestItemPassed
	} else if reachable > 0 {
		status = proxyTestItemWarning
	} else if failed > 0 {
		status = proxyTestItemFailed
	}
	base := proxyTestReportItem{Name: "基础连通性", Status: status, Message: "供应商默认地址均未形成真实传输检测"}
	if latencies > 0 {
		average := (total + int64(latencies)/2) / int64(latencies)
		base.LatencyMS = &average
	}
	switch {
	case failed == 0 && unknown == 0:
		base.Message = "全部供应商默认地址可达"
	case reachable > 0:
		base.Message = fmt.Sprintf("部分供应商默认地址完成传输检测（%d/%d）", reachable, providerCount)
	case failed > 0:
		base.Message = "供应商默认地址全部发生传输失败"
	}
	return base
}

// proxyTestSummarize mirrors manual.go summarizeReport: failed>0 -> failed,
// else warning or passed/unknown mix -> warning, else passed>0 -> passed,
// else unknown; unknown scores 0, otherwise max(0, 100-warning*10-failed*35)
// with grades A(>=90)/B(>=75)/C(>=60)/D.
func proxyTestSummarize(items []proxyTestReportItem) (status string, score, passed, warning, failed int, grade string) {
	passed, warning, failed, unknown := 0, 0, 0, 0
	for _, item := range items {
		switch item.Status {
		case proxyTestItemPassed:
			passed++
		case proxyTestItemWarning:
			warning++
		case proxyTestItemFailed:
			failed++
		case proxyTestItemUnknown:
			unknown++
		}
	}
	switch {
	case failed > 0:
		status = proxyTestItemFailed
	case warning > 0 || (passed > 0 && unknown > 0):
		status = proxyTestItemWarning
	case unknown > 0 || len(items) == 0:
		status = proxyTestItemUnknown
	default:
		status = proxyTestItemPassed
	}
	score = 0
	if status != proxyTestItemUnknown {
		score = 100 - warning*10 - failed*35
		if score < 0 {
			score = 0
		}
	}
	grade = "D"
	if score >= 90 {
		grade = "A"
	} else if score >= 75 {
		grade = "B"
	} else if score >= 60 {
		grade = "C"
	}
	return status, score, passed, warning, failed, grade
}

// proxyTestReportMessage mirrors manual.go reportMessage.
func proxyTestReportMessage(status string, failed, warning int) string {
	switch status {
	case proxyTestItemPassed:
		return "代理质量检测通过"
	case proxyTestItemWarning:
		return fmt.Sprintf("代理可用，存在 %d 项告警", warning)
	case proxyTestItemFailed:
		return fmt.Sprintf("代理检测存在 %d 项失败", failed)
	default:
		return "代理检测未形成有效传输尝试"
	}
}

// ---------------------------------------------------------------------------
// Probe transport (jobs transport.go newProxyTransport / parseTargetURL /
// applyNodeProbeHeaders).
// ---------------------------------------------------------------------------

// newProxyTestTransport: no transparent gzip (Node http.request never
// negotiated it), remote DNS for socks5h, 64 KiB header budget, CONNECT
// close hint, and direct access independent from HTTP(S)_PROXY env state.
func newProxyTestTransport(rawProxyURL string, timeout time.Duration) (*http.Transport, error) {
	if strings.TrimSpace(rawProxyURL) == "" {
		return nil, errors.New("proxy required")
	}
	if _, err := upstreamhttp.ParseProxyURL(rawProxyURL); err != nil {
		return nil, errors.New("proxy URL invalid")
	}
	return upstreamhttp.NewTransport(rawProxyURL, upstreamhttp.TransportOptions{
		ResponseHeaderTimeout:  timeout,
		DisableCompression:     true,
		ForceRemoteSOCKS5:      true,
		MaxResponseHeaderBytes: proxyTestResponseHeaderBytes,
		ProxyConnectHeader:     http.Header{"Proxy-Connection": []string{"close"}},
	})
}

// applyProxyTestHeaders mirrors applyNodeProbeHeaders: forward HTTP proxy
// requests carry the target Host and close headers; CONNECT/SOCKS requests
// carry only the probe identity headers.
func applyProxyTestHeaders(request *http.Request, target, proxyURL *url.URL) {
	request.Header.Set("Accept", "application/json,text/plain,*/*")
	request.Header.Set("User-Agent", proxyTestUserAgent)
	if target == nil || proxyURL == nil || target.Scheme != "http" || !proxyTestForwardProxyScheme(proxyURL.Scheme) {
		return
	}
	request.Header.Set("Connection", "close")
	request.Header.Set("Proxy-Connection", "close")
	request.Host = target.Host
	request.Close = true
}

func proxyTestForwardProxyScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// parseProxyTestTargetURL mirrors jobs parseTargetURL (transport.go:76-137):
// strict http(s) authority form, canonical lowercase host/port, no query,
// fragment, userinfo, dot segments or control characters.
func parseProxyTestTargetURL(raw string) (*url.URL, error) {
	if raw == "" || strings.Contains(raw, "\\") || proxyTestHasControlCharacter(raw) {
		return nil, errors.New("target URL invalid")
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, errors.New("target URL invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return nil, errors.New("target URL invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("target URL scheme invalid")
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon+2 >= len(value) || !strings.EqualFold(value[:colon], scheme) || value[colon+1:colon+3] != "//" {
		return nil, errors.New("target URL authority invalid")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(value, "#") {
		return nil, errors.New("target URL invalid")
	}
	if strings.Contains(parsed.Host, "%") {
		return nil, errors.New("target URL host invalid")
	}
	hostname := parsed.Hostname()
	if !proxyTestValidHostname(hostname) {
		return nil, errors.New("target URL host invalid")
	}
	port, err := proxyTestCanonicalTargetPort(parsed.Host)
	if err != nil {
		return nil, err
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	if proxyTestHasControlCharacter(path) || strings.Contains(path, "//") || proxyTestHasEncodedByte(parsed.EscapedPath(), "2f") || proxyTestHasEncodedByte(parsed.EscapedPath(), "5c") {
		return nil, errors.New("target URL path invalid")
	}
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") || proxyTestHasControlCharacter(decoded) {
			return nil, errors.New("target URL path dot segment invalid")
		}
	}
	parsed.Scheme = scheme
	canonicalHost := strings.ToLower(hostname)
	if strings.Contains(hostname, ":") {
		canonicalHost = "[" + canonicalHost + "]"
	}
	if port != "" && !proxyTestIsDefaultPort(scheme, port) {
		canonicalHost += ":" + port
	}
	parsed.Host = canonicalHost
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed, nil
}

func proxyTestHasControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func proxyTestHasEncodedByte(path, encoded string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "%"+encoded)
}

func proxyTestValidHostname(hostname string) bool {
	if hostname == "" || strings.HasPrefix(hostname, ".") || strings.Contains(hostname, "..") {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return true
	}
	if strings.Contains(hostname, ":") || len(hostname) > 253 {
		return false
	}
	if strings.HasSuffix(hostname, ".") {
		hostname = strings.TrimSuffix(hostname, ".")
	}
	if hostname == "" {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func proxyTestCanonicalTargetPort(host string) (string, error) {
	port := ""
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing < 0 {
			return "", errors.New("target URL host invalid")
		}
		rest := host[closing+1:]
		if rest == "" {
			return "", nil
		}
		if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
			return "", errors.New("target URL port invalid")
		}
		port = rest[1:]
	} else {
		colonCount := strings.Count(host, ":")
		if colonCount > 1 {
			return "", errors.New("target URL host invalid")
		}
		if colonCount == 1 {
			separator := strings.LastIndexByte(host, ':')
			if separator == 0 || separator == len(host)-1 {
				return "", errors.New("target URL port invalid")
			}
			port = host[separator+1:]
		}
	}
	if port == "" {
		return "", nil
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return "", errors.New("target URL port invalid")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func proxyTestIsDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

// ---------------------------------------------------------------------------
// Manual-only outbound identity probe (jobs manual_outbound.go).
// ---------------------------------------------------------------------------

type proxyTestOutboundInfo struct {
	IP     string
	Region string
}

type proxyTestOutboundTarget struct {
	URL    string
	Parser string
}

// proxyTestOutboundTargets is a var so tests can pin it to local fixtures;
// production keeps the frozen order ip-api / ipwho.is / api.ip.sb / ipinfo /
// ipify / httpbin.
var proxyTestOutboundTargets = []proxyTestOutboundTarget{
	{URL: "http://ip-api.com/json/?lang=zh-CN", Parser: "ip-api"},
	{URL: "https://ipwho.is/", Parser: "ipwhois"},
	{URL: "https://api.ip.sb/geoip", Parser: "ipsb"},
	{URL: "https://ipinfo.io/json", Parser: "ipinfo"},
	{URL: "https://api.ipify.org?format=json", Parser: "ipify"},
	{URL: "http://httpbin.org/ip", Parser: "httpbin"},
}

func probeProxyTestOutbound(ctx context.Context, rawProxyURL string, timeout time.Duration) (proxyTestOutboundInfo, bool) {
	if timeout <= 0 {
		return proxyTestOutboundInfo{}, false
	}
	for _, target := range proxyTestOutboundTargets {
		if err := ctx.Err(); err != nil {
			return proxyTestOutboundInfo{}, false
		}
		info, ok := requestProxyTestOutbound(ctx, rawProxyURL, target, timeout)
		if ok && info.IP != "" {
			return info, true
		}
	}
	return proxyTestOutboundInfo{}, false
}

func requestProxyTestOutbound(ctx context.Context, rawProxyURL string, target proxyTestOutboundTarget, timeout time.Duration) (proxyTestOutboundInfo, bool) {
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport, err := newProxyTestTransport(rawProxyURL, timeout)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	parsedProxy, err := url.Parse(rawProxyURL)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	applyProxyTestHeaders(request, targetURL, parsedProxy)
	client := upstreamhttp.NewClientWithTransport(transport)
	response, err := client.Do(request)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = upstreamhttp.Drain(response.Body)
		return proxyTestOutboundInfo{}, false
	}
	body, err := upstreamhttp.ReadAndDrainBounded(response.Body, proxyTestMaxResponseBodyBytes)
	if err != nil {
		return proxyTestOutboundInfo{}, false
	}
	return parseProxyTestOutbound(target.Parser, body)
}

// parseProxyTestOutbound mirrors parseManualOutbound.
func parseProxyTestOutbound(parser string, body []byte) (proxyTestOutboundInfo, bool) {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return proxyTestOutboundInfo{}, false
	}
	stringValue := func(key string) string {
		text, ok := value[key].(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(text)
	}
	firstIP := func(raw string) string {
		if index := strings.IndexByte(raw, ','); index >= 0 {
			raw = raw[:index]
		}
		return strings.TrimSpace(raw)
	}
	region := func(country, countryCode string, fallback ...string) string {
		if country = strings.TrimSpace(country); country != "" {
			return country
		}
		if countryCode = strings.TrimSpace(countryCode); countryCode != "" {
			return strings.ToUpper(countryCode)
		}
		for _, candidate := range fallback {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
		return ""
	}
	switch parser {
	case "ip-api":
		if strings.ToLower(stringValue("status")) != "success" {
			return proxyTestOutboundInfo{}, false
		}
		return proxyTestOutboundInfo{IP: stringValue("query"), Region: region(stringValue("country"), stringValue("countryCode"), stringValue("regionName"), stringValue("region"), stringValue("city"))}, stringValue("query") != ""
	case "ipwhois":
		if success, exists := value["success"].(bool); exists && !success {
			return proxyTestOutboundInfo{}, false
		}
		ip := stringValue("ip")
		return proxyTestOutboundInfo{IP: ip, Region: region(stringValue("country"), stringValue("country_code"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipsb":
		ip := stringValue("ip")
		return proxyTestOutboundInfo{IP: ip, Region: region(stringValue("country"), stringValue("country_code"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipinfo":
		ip := stringValue("ip")
		return proxyTestOutboundInfo{IP: ip, Region: region("", stringValue("country"), stringValue("region"), stringValue("city"))}, ip != ""
	case "ipify":
		ip := stringValue("ip")
		return proxyTestOutboundInfo{IP: ip}, ip != ""
	case "httpbin":
		ip := firstIP(stringValue("origin"))
		return proxyTestOutboundInfo{IP: ip}, ip != ""
	default:
		return proxyTestOutboundInfo{}, false
	}
}
