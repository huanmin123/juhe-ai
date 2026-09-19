package operationlog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// sliceLogger 是 producer 测试的告警收集器：worker goroutine 会并发写，读取
// 必须经 snapshot() 加锁拷贝。
type sliceLogger struct {
	mu    sync.Mutex
	warns *[]string
}

func (l *sliceLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.warns = append(*l.warns, msg)
}

func (l *sliceLogger) Error(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.warns = append(*l.warns, msg)
}

func (l *sliceLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *l.warns...)
}

func TestSafeChangeSensitiveRedaction(t *testing.T) {
	change := SafeChange("password", "登录密码", "old-secret", "new-secret", true)
	if !change.Sensitive {
		t.Fatal("sensitive flag missing")
	}
	if change.Before != "已设置" {
		t.Fatalf("sensitive before = %v", change.Before)
	}
	if change.After != "已变更" {
		t.Fatalf("sensitive after = %v", change.After)
	}
	if strings.Contains(change.Before.(string), "secret") || strings.Contains(change.After.(string), "secret") {
		t.Fatal("sensitive values leaked")
	}
}

// TestSafeChangeSensitiveClearedValueShowsUnset pins the BUG-0157 Node
// alignment (operation-log.service.ts:186-187): a sensitive after/before that
// is undefined, null or 空字符串 must show 未设置, never a fixed 已变更/已设置.
func TestSafeChangeSensitiveClearedValueShowsUnset(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		wantLabel string
	}{
		{"nil value", nil, "未设置"},
		{"empty string", "", "未设置"},
		{"replacement value", "new-secret", "已变更"},
		{"non-empty string", " ", "已变更"},
	}
	for _, testCase := range cases {
		after := SafeChange("token", "访问令牌", "old-secret", testCase.value, true).After
		if after != testCase.wantLabel {
			t.Fatalf("%s: sensitive after = %v, want %s", testCase.name, after, testCase.wantLabel)
		}
	}
	for _, testCase := range cases {
		wantBefore := testCase.wantLabel
		if wantBefore == "已变更" {
			wantBefore = "已设置"
		}
		before := SafeChange("token", "访问令牌", testCase.value, "new-secret", true).Before
		if before != wantBefore {
			t.Fatalf("%s: sensitive before = %v, want %s", testCase.name, before, wantBefore)
		}
	}
}

func TestSafeChangeStringTruncation(t *testing.T) {
	long := strings.Repeat("x", 300)
	change := SafeChange("displayName", "用户名称", long, "short", false)
	if !strings.HasSuffix(change.Before.(string), "...") {
		t.Fatal("long before value must be truncated with ellipsis")
	}
	if len(change.Before.(string)) > 206 {
		t.Fatalf("truncation length wrong: %d", len(change.Before.(string)))
	}
	if change.After != "short" {
		t.Fatalf("after = %v", change.After)
	}
}

// TestSafeChangePreservesNativeScalarTypes pins the BUG-0157 normalizeSafeValue
// alignment (operation-log.service.ts:222-229): null, numbers and booleans
// keep their native type instead of being stringified, and only strings are
// ellipsis-truncated.
func TestSafeChangePreservesNativeScalarTypes(t *testing.T) {
	change := SafeChange("enabled", "启用", "启用", false, false)
	if change.After != false {
		t.Fatalf("boolean after must stay native false, got %v (%T)", change.After, change.After)
	}
	change = SafeChange("maxCount", "上限", json.Number("10"), 3, false)
	if change.After != 3 {
		t.Fatalf("integer after must stay native 3, got %v (%T)", change.After, change.After)
	}
	change = SafeChange("ratio", "比例", 0.5, 3.14, false)
	if change.After != 3.14 {
		t.Fatalf("float after must stay native 3.14, got %v (%T)", change.After, change.After)
	}
	change = SafeChange("memo", "备注", nil, nil, false)
	if change.Before != nil || change.After != nil {
		t.Fatalf("nil must stay nil, got before=%v (%T) after=%v (%T)", change.Before, change.Before, change.After, change.After)
	}
	encoded, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	// Node undefined → the JSON field disappears; Go nil omits it likewise.
	if want := `{"field":"memo","label":"备注"}`; string(encoded) != want {
		t.Fatalf("nil change JSON = %s, want %s", encoded, want)
	}
	if encoded, err = json.Marshal(SafeChange("enabled", "启用", "禁用", true, false)); err != nil {
		t.Fatal(err)
	}
	if want := `{"field":"enabled","label":"启用","before":"禁用","after":true}`; string(encoded) != want {
		t.Fatalf("boolean change JSON = %s, want %s", encoded, want)
	}
}

// TestSafeChangeStructuredTruncationMatchesNode pins the remaining BUG-0157
// branch (operation-log.service.ts:233-238): structured values are
// JSON-serialized and hard-cut at 500 characters without an appended ellipsis.
func TestSafeChangeStructuredTruncationMatchesNode(t *testing.T) {
	change := SafeChange("config", "配置", nil, map[string]any{"a": 1}, false)
	if change.After != `{"a":1}` {
		t.Fatalf("short structured after = %v, want compact JSON", change.After)
	}
	long := map[string]any{"v": strings.Repeat("x", 600)}
	change = SafeChange("config", "配置", nil, long, false)
	encoded := change.After.(string)
	if len(encoded) != 500 {
		t.Fatalf("structured truncation length = %d, want 500", len(encoded))
	}
	if strings.HasSuffix(encoded, "...") {
		t.Fatal("structured truncation must not append an ellipsis")
	}
}

// TestSafeChangeStringTruncationValidUTF8 pins the UTF-16 code-unit cut
// (operation-log.service.ts:224 value.length/slice semantics, implemented via
// truncateUTF16Units): multi-byte characters never split mid-rune, so the
// truncated value is always valid UTF-8, and astral runes (emoji) count as two
// units like Node's String.length.
func TestSafeChangeStringTruncationValidUTF8(t *testing.T) {
	// 250 个中文（每个 1 个 UTF-16 unit、3 个 UTF-8 字节）→ 截到 200 units + "..."。
	chinese := strings.Repeat("中", 250)
	truncated := normalizeSafeValue(chinese).(string)
	if !utf8.ValidString(truncated) {
		t.Fatal("truncated Chinese value must stay valid UTF-8")
	}
	if want := strings.Repeat("中", 200) + "..."; truncated != want {
		t.Fatalf("Chinese truncation wrong: units=%d", utf16LengthUnits(truncated))
	}

	// 250 个 emoji（每个 astral rune = 2 个 UTF-16 units）→ 500 units，截到
	// 100 个 emoji + "..."，绝不切出半个 surrogate pair。
	emoji := strings.Repeat("\U0001F600", 250)
	truncated = normalizeSafeValue(emoji).(string)
	if !utf8.ValidString(truncated) {
		t.Fatal("truncated emoji value must stay valid UTF-8")
	}
	if want := strings.Repeat("\U0001F600", 100) + "..."; truncated != want {
		t.Fatalf("emoji truncation wrong: got %d runes", len([]rune(truncated)))
	}

	// 恰好 200 units 不截断；201 units 在 rune 边界截断。
	boundary := strings.Repeat("中", 200)
	if got := normalizeSafeValue(boundary).(string); got != boundary {
		t.Fatal("exactly 200 units must not truncate")
	}
	mixed := strings.Repeat("x", 199) + "\U0001F600"
	truncated = normalizeSafeValue(mixed).(string)
	if !utf8.ValidString(truncated) {
		t.Fatal("mixed truncation must stay valid UTF-8")
	}
	if want := strings.Repeat("x", 199) + "..."; truncated != want {
		t.Fatalf("mixed truncation must cut before the astral rune, got %q", truncated)
	}

	// 结构化 JSON 分支：截断到 500 units 且保持合法 UTF-8。
	long := map[string]any{"v": strings.Repeat("x", 600)}
	encoded := normalizeSafeValue(long).(string)
	if len(encoded) != 500 || !utf8.ValidString(encoded) {
		t.Fatalf("structured truncation must stay 500 valid bytes, got %d", len(encoded))
	}
}

// utf16LengthUnits counts UTF-16 code units for assertions (String.length).
func utf16LengthUnits(value string) int {
	units := 0
	for _, symbol := range value {
		units += utf16.RuneLen(symbol)
	}
	return units
}

func TestProducerPersistsAndSwallowsErrors(t *testing.T) {
	fake := &fakeStore{}
	producer := NewProducer(fake, OwnerLease{}, Config{InstanceID: "test"}, nil)

	producer.Record(Input{
		ActorSystemAccountID: "sysacc_1",
		ActorRole:            "admin",
		Module:               "system_accounts",
		Action:               "create",
		OperationKey:         "system_accounts.create",
		ResourceType:         "system_account",
		Summary:              "创建系统账户",
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.persisted() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fake.persisted() != 1 {
		t.Fatalf("expected 1 persisted entry, got %d", fake.persisted())
	}

	// Errors are swallowed: persist failure never panics or propagates.
	fake.failPersists = true
	producer.Record(Input{ActorSystemAccountID: "x", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.persistAttempts() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeStore struct {
	mu            sync.Mutex
	persistCount  int
	failPersists  bool
	renewAccepted bool
}

func (f *fakeStore) persistAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.persistCount
}

func (f *fakeStore) persisted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.persistCount
}

func (f *fakeStore) EnsureSchema(context.Context) error { return nil }

func (f *fakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, true, nil
}

func (f *fakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewAccepted = true
	return true, nil
}

func (f *fakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error { return nil }

func (f *fakeStore) Persist(ctx context.Context, lease OwnerLease, input Input) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistCount++
	if f.failPersists {
		return false, context.DeadlineExceeded
	}
	return true, nil
}

// gatedStore 阻塞续期，让测试把全部 worker 停在写路径内以确定性地填满队列。
type gatedStore struct {
	*fakeStore
	gate    chan struct{}
	entered atomic.Int32
}

func (g *gatedStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	g.entered.Add(1)
	<-g.gate
	return true, nil
}

// TestProducerRecordDropsWhenQueueSaturated 钉住有界队列契约：全部 worker 被
// 阻塞时，队列恰好接收 producerQueueCapacity 条，再来的记录被丢弃并告警
// （Record 保持 fire-and-forget，绝不阻塞业务事务）。
func TestProducerRecordDropsWhenQueueSaturated(t *testing.T) {
	inner := &fakeStore{}
	gate := make(chan struct{})
	store := &gatedStore{fakeStore: inner, gate: gate}
	var warns []string
	logger := &sliceLogger{warns: &warns}
	producer := NewProducer(store, OwnerLease{}, Config{InstanceID: "test", OwnerLease: 30 * time.Second}, logger)

	// 把每个 worker 停在阻塞续期内，后续不再有出队与填充竞争。
	for i := 0; i < producerWorkers; i++ {
		producer.Record(Input{ID: fmt.Sprintf("inflight-%d", i)})
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.entered.Load() < producerWorkers && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if store.entered.Load() != producerWorkers {
		t.Fatalf("只有 %d 个 worker 进入阻塞续期，期望 %d", store.entered.Load(), producerWorkers)
	}

	for i := 0; i < producerQueueCapacity; i++ {
		producer.Record(Input{ID: fmt.Sprintf("fill-%d", i)})
	}
	producer.Record(Input{ID: "overflow-dropped"})
	if snapshot := logger.snapshot(); len(snapshot) == 0 || !strings.Contains(snapshot[len(snapshot)-1], "F4 操作日志队列已满") {
		t.Fatalf("队列饱和必须对丢弃告警，got %v", snapshot)
	}

	close(gate)
	want := producerWorkers + producerQueueCapacity
	deadline = time.Now().Add(10 * time.Second)
	for store.persistAttempts() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if persisted := store.persisted(); persisted != want {
		t.Fatalf("放行后应恰好落库 %d 条（丢弃的那条不落库），got %d", want, persisted)
	}
}

func (f *fakeStore) List(context.Context, ListOptions) (ListResult, error) {
	return ListResult{}, nil
}

func (f *fakeStore) Detail(context.Context, string, string) (DetailSupplement, bool, error) {
	return DetailSupplement{}, false, nil
}

func (f *fakeStore) CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}

func (f *fakeStore) RetentionDays(context.Context, int) (int, error) { return 365, nil }

func (f *fakeStore) Close() error { return nil }
