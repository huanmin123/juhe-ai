package accountbalance

// 多 Key 余额执行核心测试：全部 Mock（httptest/fake HTTPDoer 模式，禁真实
// 网络），对齐 Node queryMultiKeyAccountBalance / aggregateMultiKeyBalance 的
// 语义锚点：sum/shared 合计、部分失败、口径不一致、全失败、有界并发与共享
// deadline、候选资格放宽与 direct input 读取不再跳过多 Key 行。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const mkSecret = "mk-balance-secret"

// mkRoutedClient 按 Bearer Key 路由固定响应：bodies[key] 缺省时返回 statuses
// 里的状态码（缺省 200 空体）。记录逐 Key 请求、并发峰值与总请求数。
type mkRoutedClient struct {
	mu       sync.Mutex
	bodies   map[string]string
	statuses map[string]int
	delay    time.Duration
	requests int
	current  int
	maxSeen  int
	seenKeys []string
}

func (c *mkRoutedClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.requests++
	c.current++
	if c.current > c.maxSeen {
		c.maxSeen = c.current
	}
	key := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	c.seenKeys = append(c.seenKeys, key)
	body := c.bodies[key]
	status := c.statuses[key]
	delay := c.delay
	c.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	c.mu.Lock()
	c.current--
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func (c *mkRoutedClient) stats() (requests, maxSeen int, keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests, c.maxSeen, append([]string(nil), c.seenKeys...)
}

func mkMultiKeyEnvelope(t *testing.T, keys ...string) CredentialEnvelope {
	t.Helper()
	envelope, err := NewCredentialEnvelope(mkSecret, "api_key", map[string]any{"api_keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func mkInput(envelope CredentialEnvelope) Input {
	now := time.Now().UTC()
	return Input{
		AccountID: "mk-acct", SystemAccountID: "mk-sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://mk-upstream.invalid", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: envelope, Credential: envelope, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
}

func mkQuotaBody(amount string) string {
	return fmt.Sprintf(`{"unit":"USD","remaining":%q,"mode":"quota_limited"}`, amount)
}

// TestMultiKeySumAggregation 全部成功、scope=key、口径一致 → 精确求和。
func TestMultiKeySumAggregation(t *testing.T) {
	client := &mkRoutedClient{bodies: map[string]string{
		"mk-k1": mkQuotaBody("5"),
		"mk-k2": mkQuotaBody("2.5"),
	}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	// 合计走 decimal 精确相加并按 decimalText 去尾零（对齐 Node addDecimalStrings）。
	if snapshot.Status != StatusFresh || snapshot.RemainingUSD != "7.5" {
		t.Fatalf("sum snapshot: %#v", snapshot)
	}
	if snapshot.Scope != ScopeKey || snapshot.Aggregation != AggregationSum {
		t.Fatalf("sum scope/aggregation: %q/%q", snapshot.Scope, snapshot.Aggregation)
	}
	if snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 2 || len(snapshot.KeyBalances) != 2 {
		t.Fatalf("sum counts: %#v", snapshot)
	}
	for index, entry := range snapshot.KeyBalances {
		key := fmt.Sprintf("mk-k%d", index+1)
		if entry.Status != StatusFresh || entry.RemainingUSD == "" || entry.Scope != ScopeKey {
			t.Fatalf("entry %d: %#v", index, entry)
		}
		if entry.KeyFingerprint != BalanceAPIKeyFingerprint(mkSecret, key) {
			t.Fatalf("entry %d fingerprint mismatch: %q", index, entry.KeyFingerprint)
		}
		if entry.MaskedKey != MaskBalanceAPIKey(key) || entry.LastSuccessAt == "" || entry.LastAttemptAt == "" {
			t.Fatalf("entry %d mask/stamps: %#v", index, entry)
		}
	}
	if requests, _, keys := client.stats(); requests != 2 || len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("must query each key once: requests=%d keys=%v", requests, keys)
	}
}

// TestMultiKeySharedAggregation 全部成功、scope=account 且各值相同 → 共享值。
func TestMultiKeySharedAggregation(t *testing.T) {
	client := &mkRoutedClient{bodies: map[string]string{
		"mk-k1": `{"balance":8.5}`,
		"mk-k2": `{"balance":8.50}`,
	}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusFresh || snapshot.RemainingUSD != "8.500000" {
		t.Fatalf("shared snapshot: %#v", snapshot)
	}
	if snapshot.Scope != ScopeAccount || snapshot.Aggregation != AggregationShared || snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 2 {
		t.Fatalf("shared merge: %#v", snapshot)
	}
}

// TestMultiKeyScopeMismatchUnsupported 混合口径（key + account）→ unsupported。
func TestMultiKeyScopeMismatchUnsupported(t *testing.T) {
	client := &mkRoutedClient{bodies: map[string]string{
		"mk-k1": mkQuotaBody("5"),
		"mk-k2": `{"balance":9}`,
	}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusUnsupported || snapshot.ErrorMessage != "多 Key 余额口径不一致，无法安全合计" {
		t.Fatalf("mismatch snapshot: %#v", snapshot)
	}
	if snapshot.RemainingUSD != "" || snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 2 || len(snapshot.KeyBalances) != 2 {
		t.Fatalf("mismatch must keep per-key details without a total: %#v", snapshot)
	}
}

// TestMultiKeySharedValuesDifferUnsupported account 口径但值不同 → 禁止合计。
func TestMultiKeySharedValuesDifferUnsupported(t *testing.T) {
	client := &mkRoutedClient{bodies: map[string]string{
		"mk-k1": `{"balance":8.5}`,
		"mk-k2": `{"balance":9.25}`,
	}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusUnsupported || snapshot.ErrorMessage != "多 Key 余额口径不一致，无法安全合计" {
		t.Fatalf("differing shared values must stay unsupported: %#v", snapshot)
	}
}

// TestMultiKeyPartialFailure 有成功有失败 → failed + 部分失败文案（x=失败数），
// 保留成功 Key 明细。
func TestMultiKeyPartialFailure(t *testing.T) {
	client := &mkRoutedClient{
		bodies:   map[string]string{"mk-k1": mkQuotaBody("5")},
		statuses: map[string]int{"mk-k2": http.StatusUnauthorized},
	}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusFailed || snapshot.ErrorMessage != "多 Key 余额查询部分失败（1/2）" {
		t.Fatalf("partial failure snapshot: %#v", snapshot)
	}
	if snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 1 || len(snapshot.KeyBalances) != 2 {
		t.Fatalf("partial failure counts: %#v", snapshot)
	}
	success := snapshot.KeyBalances[0]
	if success.Status != StatusFresh || success.RemainingUSD != "5.000000" || success.LastSuccessAt == "" {
		t.Fatalf("successful key detail must survive: %#v", success)
	}
	failed := snapshot.KeyBalances[1]
	if failed.Status == StatusFresh || !strings.Contains(failed.ErrorMessage, "HTTP 401") || failed.LastSuccessAt != "" {
		t.Fatalf("failed key detail: %#v", failed)
	}
}

// TestMultiKeyAllFailed 全部失败 → failed + 首个 Key 的代表性错误。
func TestMultiKeyAllFailed(t *testing.T) {
	client := &mkRoutedClient{statuses: map[string]int{"mk-k1": http.StatusInternalServerError, "mk-k2": http.StatusInternalServerError}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusFailed || snapshot.ErrorMessage != "余额上游返回 HTTP 500" {
		t.Fatalf("all-failed snapshot: %#v", snapshot)
	}
	if snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 0 || len(snapshot.KeyBalances) != 2 || snapshot.RemainingUSD != "" {
		t.Fatalf("all-failed counts: %#v", snapshot)
	}
}

// TestMultiKeyBoundedConcurrency 8 个 Key 并发峰值不超过 4 且全部被查询。
func TestMultiKeyBoundedConcurrency(t *testing.T) {
	bodies := map[string]string{}
	for index := 0; index < 8; index++ {
		bodies[fmt.Sprintf("mk-k%d", index+1)] = mkQuotaBody(fmt.Sprintf("%d", index+1))
	}
	client := &mkRoutedClient{bodies: bodies, delay: 20 * time.Millisecond}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2", "mk-k3", "mk-k4", "mk-k5", "mk-k6", "mk-k7", "mk-k8")), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	requests, maxSeen, _ := client.stats()
	if requests != 8 {
		t.Fatalf("every key must be queried once: %d", requests)
	}
	if maxSeen > multiKeyBalanceMaxConcurrent {
		t.Fatalf("concurrency must stay bounded at %d, saw %d", multiKeyBalanceMaxConcurrent, maxSeen)
	}
	// 1+2+...+8 = 36（decimalText 去尾零）
	if result.Snapshot.Status != StatusFresh || result.Snapshot.RemainingUSD != "36" || result.Snapshot.QueriedKeyCount != 8 {
		t.Fatalf("bounded sum snapshot: %#v", result.Snapshot)
	}
}

// TestMultiKeySharedDeadline 共享总 deadline 到期后剩余 Key 不再发起上游请求
// （对齐 Node 的分批 + 超时语义）：4 个请求先行占用完 deadline，其余 Key 记
// "上游余额查询超时"。
func TestMultiKeySharedDeadline(t *testing.T) {
	bodies := map[string]string{}
	for index := 0; index < 8; index++ {
		bodies[fmt.Sprintf("mk-k%d", index+1)] = mkQuotaBody("1")
	}
	client := &mkRoutedClient{bodies: bodies, delay: 150 * time.Millisecond}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2", "mk-k3", "mk-k4", "mk-k5", "mk-k6", "mk-k7", "mk-k8")), QueryOptions{Secret: mkSecret, Client: client, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	requests, _, _ := client.stats()
	if requests != multiKeyBalanceMaxConcurrent {
		t.Fatalf("deadline must stop later waves: requests=%d", requests)
	}
	snapshot := result.Snapshot
	if snapshot.KeyCount != 8 || snapshot.QueriedKeyCount != 4 || snapshot.Status != StatusFailed || snapshot.ErrorMessage != "多 Key 余额查询部分失败（4/8）" {
		t.Fatalf("deadline snapshot: %#v", snapshot)
	}
	timeouts := 0
	for _, entry := range snapshot.KeyBalances {
		if entry.ErrorMessage == "上游余额查询超时" && entry.Status == StatusFailed {
			timeouts++
		}
	}
	if timeouts != 4 {
		t.Fatalf("late keys must record the deadline diagnostic: %d in %#v", timeouts, snapshot.KeyBalances)
	}
}

// TestMultiKeySingleKeyDelegatesSinglePath 单 Key 输入原样委托既有路径，多 Key
// 字段保持零值（序列化不出现）。
func TestMultiKeySingleKeyDelegatesSinglePath(t *testing.T) {
	envelope, err := NewCredentialEnvelope(mkSecret, "api_key", map[string]string{"api_key": "mk-only"})
	if err != nil {
		t.Fatal(err)
	}
	client := &mkRoutedClient{bodies: map[string]string{"mk-only": mkQuotaBody("3.5")}}
	result, err := ExecuteAccountBalanceQuery(context.Background(), mkInput(envelope), QueryOptions{Secret: mkSecret, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.Status != StatusFresh || snapshot.RemainingUSD != "3.500000" {
		t.Fatalf("single-key snapshot: %#v", snapshot)
	}
	if snapshot.KeyCount != 0 || snapshot.QueriedKeyCount != 0 || snapshot.Scope != "" || snapshot.Aggregation != "" || snapshot.KeyBalances != nil {
		t.Fatalf("single-key snapshot must keep multi-key fields zero: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"keyCount", "queriedKeyCount", "keyBalances", "scope", "aggregation"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("single-key JSON must not grow %q: %s", field, encoded)
		}
	}
}

// TestMultiKeyCandidateToInput Carries 多 Key 封套原样进入 Input。
func TestMultiKeyCandidateToInput(t *testing.T) {
	envelope := mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")
	now := time.Now().UTC()
	candidate := Candidate{
		AccountID: "mk-cand", SystemAccountID: "mk-sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BalanceEnabled: true, APIKeyCount: 2, APIKey: envelope, BaseURL: "https://mk-upstream.invalid",
		Config:   QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &now,
	}
	if err := candidateEligible(candidate, TriggerPeriodic); err != nil {
		t.Fatalf("multi-key candidate must stay eligible: %v", err)
	}
	input, err := candidate.ToInput(TriggerPeriodic, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if input.APIKey.Ciphertext != envelope.Ciphertext || input.Credential.Ciphertext != envelope.Ciphertext {
		t.Fatal("multi-key envelope must be carried as-is")
	}
	noKeys := candidate
	noKeys.APIKeyCount = 0
	noKeys.APIKey = CredentialEnvelope{}
	if _, err := noKeys.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "一个 API Key") {
		t.Fatalf("zero-key candidate must fail: %v", err)
	}
}

// TestMultiKeyReaderKeepsMultiKeyRows direct input 读取不再跳过多 Key 凭据行。
func TestMultiKeyReaderKeepsMultiKeyRows(t *testing.T) {
	reader := &PostgresDirectInputReader{secret: mkSecret}
	cipherText, err := EncryptV1Envelope(mkSecret, []byte(`{"api_keys":["mk-k1","mk-k2"],"base_url":"https://mk-upstream.invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	credential, keys, err := reader.decodeCandidateCredential("mk-acct", cipherText)
	if err != nil {
		t.Fatalf("multi-key row must become a candidate: %v", err)
	}
	if len(keys) != 2 || keys[0] != "mk-k1" || keys[1] != "mk-k2" {
		t.Fatalf("effective keys: %v", keys)
	}
	if credential["base_url"] != "https://mk-upstream.invalid" {
		t.Fatalf("credential map: %#v", credential)
	}
	emptyCipher, err := EncryptV1Envelope(mkSecret, []byte(`{"api_key":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.decodeCandidateCredential("mk-acct", emptyCipher); err == nil || !strings.Contains(err.Error(), "缺少有效 API Key") {
		t.Fatalf("zero-key row must be rejected: %v", err)
	}
}

// TestMaskAndFingerprintEquivalence 掩码与指纹和 gateway 读取端等价
// （accountsbalance.MaskBalanceAPIKey / BalanceAPIKeyFingerprint 同源语义）。
func TestMaskAndFingerprintEquivalence(t *testing.T) {
	if masked := MaskBalanceAPIKey("sk-abcdef123456"); masked != "sk-a…3456" {
		t.Fatalf("long key mask: %q", masked)
	}
	if masked := MaskBalanceAPIKey("abc"); masked != "ab…bc" {
		t.Fatalf("short key mask: %q", masked)
	}
	mac := hmac.New(sha256.New, []byte(mkSecret))
	mac.Write([]byte("sk-abcdef123456"))
	if fingerprint := BalanceAPIKeyFingerprint(mkSecret, "sk-abcdef123456"); fingerprint != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("fingerprint must be secret-keyed HMAC-SHA256 hex: %q", fingerprint)
	}
	if fingerprint := BalanceAPIKeyFingerprint(mkSecret, "  "); fingerprint != "" {
		t.Fatalf("blank key must have an empty fingerprint: %q", fingerprint)
	}
}

// TestMultiKeyRunnerManualPersistsMergedSnapshot 端到端：多 Key 手动输入经
// runner 执行分派进入多 Key 入口，合并结果（含逐 Key 明细）写为 Snapshot。
func TestMultiKeyRunnerManualPersistsMergedSnapshot(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\mk-manual.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	client := &mkRoutedClient{bodies: map[string]string{
		"mk-k1": mkQuotaBody("5"),
		"mk-k2": mkQuotaBody("2.5"),
	}}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "mk-owner", CredentialSecret: mkSecret, HTTPClient: client, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	input := mkInput(mkMultiKeyEnvelope(t, "mk-k1", "mk-k2"))
	report, err := runner.RunManual(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("manual report: %#v", report)
	}
	record, found, err := store.LoadSnapshot(ctx, input.AccountID)
	if err != nil || !found {
		t.Fatalf("snapshot load: found=%t err=%v", found, err)
	}
	snapshot := record.Snapshot
	if snapshot.Status != StatusFresh || snapshot.RemainingUSD != "7.5" ||
		snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 2 ||
		snapshot.Aggregation != AggregationSum || snapshot.Scope != ScopeKey || len(snapshot.KeyBalances) != 2 {
		t.Fatalf("merged snapshot must be persisted: %#v", snapshot)
	}
}

func mkPeriodicCandidate(t *testing.T, envelope CredentialEnvelope, version int64) Candidate {
	t.Helper()
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	return Candidate{
		AccountID: "mk-periodic", SystemAccountID: "mk-sys", InputVersion: version, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BalanceEnabled: true, APIKeyCount: 2, APIKey: envelope, BaseURL: "https://mk-upstream.invalid",
		Config:   QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due,
	}
}

func assertDeterministicPartialFailure(t *testing.T, stage string, snapshot Snapshot) {
	t.Helper()
	if snapshot.Status != StatusFailed || snapshot.ErrorMessage != "多 Key 余额查询部分失败（1/2）" {
		t.Fatalf("%s: partial failure must land as-is: %#v", stage, snapshot)
	}
	if snapshot.KeyCount != 2 || snapshot.QueriedKeyCount != 1 || len(snapshot.KeyBalances) != 2 {
		t.Fatalf("%s: per-key details must survive: %#v", stage, snapshot)
	}
	if snapshot.ConsecutiveTransientFails != 0 || snapshot.LastTransientErrorMessage != "" {
		t.Fatalf("%s: deterministic diagnosis must not enter the transient sequence: %#v", stage, snapshot)
	}
	if snapshot.LastAttemptAt == "" {
		t.Fatalf("%s: attempt stamp missing: %#v", stage, snapshot)
	}
	success := snapshot.KeyBalances[0]
	if success.Status != StatusFresh || success.RemainingUSD != "5.000000" || success.LastSuccessAt == "" {
		t.Fatalf("%s: successful key detail: %#v", stage, success)
	}
	failed := snapshot.KeyBalances[1]
	if failed.Status == StatusFresh || !strings.Contains(failed.ErrorMessage, "HTTP 500") {
		t.Fatalf("%s: failed key detail: %#v", stage, failed)
	}
}

// TestMultiKeyPeriodicPartialFailureLandsDeterministicSnapshot M1 回归：多 Key
// 周期部分失败第一次即原样落 failed 快照（逐 Key 明细与文案完整），不保留旧
// 快照、不置 pending、不累计三振计数；连续两次周期失败仍然原样。
func TestMultiKeyPeriodicPartialFailureLandsDeterministicSnapshot(t *testing.T) {
	// 单元层：多 Key failed 结果不得被旧 fresh 快照或 pending 三振分支吞并。
	prior := SnapshotRecord{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "5", LastSuccessAt: time.Now().UTC().Format(time.RFC3339Nano), ConsecutiveTransientFails: 1}}
	partial := Snapshot{Status: StatusFailed, ErrorMessage: "多 Key 余额查询部分失败（1/2）", KeyCount: 2, QueriedKeyCount: 1, KeyBalances: []KeyBalance{
		{KeyFingerprint: "f1", MaskedKey: "m1", Status: StatusFresh, RemainingUSD: "5.000000", Scope: ScopeKey, LastAttemptAt: "a", LastSuccessAt: "a"},
		{KeyFingerprint: "f2", MaskedKey: "m2", Status: StatusFailed, ErrorMessage: "余额上游返回 HTTP 500", LastAttemptAt: "a"},
	}}
	landed := applyQueryResult(QueryResult{Snapshot: partial, ErrorMessage: partial.ErrorMessage}, prior, true, TriggerPeriodic, time.Now().UTC())
	assertDeterministicPartialFailure(t, "unit", landed)

	// 端到端：周期路径第一次部分失败直接原样落库，第二次仍原样（无计数累计）。
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\mk-periodic.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	client := &mkRoutedClient{
		bodies:   map[string]string{"mk-k1": mkQuotaBody("5")},
		statuses: map[string]int{"mk-k2": http.StatusInternalServerError},
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "mk-periodic-owner", CredentialSecret: mkSecret, HTTPClient: client, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	envelope := mkMultiKeyEnvelope(t, "mk-k1", "mk-k2")
	for _, version := range []int64{1, 2} {
		report, err := runner.RunPeriodic(ctx, []Candidate{mkPeriodicCandidate(t, envelope, version)})
		if err != nil {
			t.Fatal(err)
		}
		if report.Executed != 1 || len(report.Errors) != 0 {
			t.Fatalf("periodic run %d report: %#v", version, report)
		}
		record, found, err := store.LoadSnapshot(ctx, "mk-periodic")
		if err != nil || !found {
			t.Fatalf("periodic run %d snapshot load: found=%t err=%v", version, found, err)
		}
		assertDeterministicPartialFailure(t, fmt.Sprintf("periodic-run-%d", version), record.Snapshot)
	}
}

// TestMultiKeyScopeMismatchUnsupportedLandsAsIs M1 回归：多 Key 口径不一致
// （unsupported 聚合）同样按原样落快照，保留逐 Key 明细与文案。
func TestMultiKeyScopeMismatchUnsupportedLandsAsIs(t *testing.T) {
	mismatch := Snapshot{Status: StatusUnsupported, ErrorMessage: "多 Key 余额口径不一致，无法安全合计", KeyCount: 2, QueriedKeyCount: 2, Scope: ScopeAccount, Aggregation: AggregationShared, KeyBalances: []KeyBalance{
		{KeyFingerprint: "f1", MaskedKey: "m1", Status: StatusFresh, RemainingUSD: "5.000000", Scope: ScopeKey, LastAttemptAt: "a", LastSuccessAt: "a"},
		{KeyFingerprint: "f2", MaskedKey: "m2", Status: StatusFresh, RemainingUSD: "9.000000", Scope: ScopeAccount, LastAttemptAt: "a", LastSuccessAt: "a"},
	}}
	landed := applyQueryResult(QueryResult{Snapshot: mismatch, ErrorMessage: mismatch.ErrorMessage}, SnapshotRecord{}, false, TriggerPeriodic, time.Now().UTC())
	if landed.Status != StatusUnsupported || landed.ErrorMessage != "多 Key 余额口径不一致，无法安全合计" {
		t.Fatalf("mismatch must land as-is: %#v", landed)
	}
	if landed.KeyCount != 2 || landed.QueriedKeyCount != 2 || len(landed.KeyBalances) != 2 || landed.Scope != ScopeAccount || landed.Aggregation != AggregationShared {
		t.Fatalf("mismatch details must survive: %#v", landed)
	}
	if landed.ConsecutiveTransientFails != 0 {
		t.Fatalf("mismatch must not enter the transient sequence: %#v", landed)
	}
	// manual 触发同样原样（确定性诊断不做 manual failed 转换）。
	manual := applyQueryResult(QueryResult{Snapshot: mismatch, ErrorMessage: mismatch.ErrorMessage}, SnapshotRecord{}, false, TriggerManual, time.Now().UTC())
	if manual.Status != StatusUnsupported || len(manual.KeyBalances) != 2 {
		t.Fatalf("manual mismatch must land as-is: %#v", manual)
	}
}

// TestSingleKeyTransientSequenceUnchanged 单 Key 临时失败三振回归：无 prior 首
// 振置 pending+count1，第三振落 failed（确定性多 Key 分支不得影响单 Key 路径）。
func TestSingleKeyTransientSequenceUnchanged(t *testing.T) {
	now := time.Now().UTC()
	first := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, SnapshotRecord{}, false, TriggerPeriodic, now)
	if first.Status != StatusPending || first.ConsecutiveTransientFails != 1 || first.LastTransientErrorMessage != "timeout" {
		t.Fatalf("single-key first strike: %#v", first)
	}
	prior := SnapshotRecord{Snapshot: Snapshot{Status: StatusPending, ConsecutiveTransientFails: 2, LastSuccessAt: now.Format(time.RFC3339Nano)}}
	third := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, prior, true, TriggerPeriodic, now)
	if third.Status != StatusFailed || third.ConsecutiveTransientFails != 3 || third.ErrorMessage != "timeout" {
		t.Fatalf("single-key third strike: %#v", third)
	}
	if third.KeyCount != 0 || third.KeyBalances != nil {
		t.Fatalf("single-key strike must stay single-key shaped: %#v", third)
	}
}
