package gatewayusage

// w13d 波次补充测试：磁盘 spool 的错误分支、JSON 有序对象、字节估算、
// RFC3339 与流量来源 helpers。
//
// 本文件登记的不可达语句：
//   - spool.go:149-151（Persist 中 current==nil 回退）：current 要么来自
//     currentCapacityLocked（恒非 nil），要么来自超容量重扫后的重新赋值，
//     恒非 nil。
//   - spool.go:420/423/427（writeFileSync 的 Write/Sync/Close 错误）：临时
//     文件名含 UUID（O_EXCL 独占创建成功），常规文件系统上 Write/Sync 恒
//     成功；Close 错误同样无法稳定触发。
//   - spool.go:217-219（过期 .tmp 清理失败）：临时目录内 Remove 对存在的
//     文件恒成功，错误分支无法稳定触发。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// spool: 错误分支
// ---------------------------------------------------------------------------

func TestW13DSpoolCapacityErrors(t *testing.T) {
	// Directory 是文件 → listInstanceDirectories ReadDir 报错 → Persist 失败。
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: filePath, InstanceID: "inst", MaxItems: 10, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	err := spool.Persist(nil, UsageRecordInput{ID: "id1", CreatedAt: "2023-11-14T22:13:20.123Z"})
	if err == nil {
		t.Fatal("Directory 为文件必须失败")
	}
	if spool.Runtime().PersistFailureCount != 1 {
		t.Fatalf("failure count=%d", spool.Runtime().PersistFailureCount)
	}
	// Windows 上 ReadDir(文件) 以 NotExist 语义降级：目录列表读作空，
	// Persist 失败由 MkdirAll 兜底（平台差异，断言按降级语义固定）。
	// 缺失目录 → 读作空。
	missing := NewUsageRecordSpool(SpoolConfig{
		Directory: filepath.Join(t.TempDir(), "missing"), InstanceID: "inst",
		MaxItems: 10, MaxBytes: 64 * 1024, ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	if directories, listErr := missing.listInstanceDirectories(); listErr != nil || directories != nil {
		t.Fatalf("缺目录必须读作空: %v %v", directories, listErr)
	}
	if files, listErr := missing.listSpoolFiles(8); listErr != nil || files != nil {
		t.Fatalf("缺目录 spool 文件必须为空: %v %v", files, listErr)
	}
}

func TestW13DSpoolCapacityLimit(t *testing.T) {
	directory := t.TempDir()
	spool, _ := newTestSpool(t, true)
	spool.config.Directory = directory
	spool.config.MaxItems = 1
	input := UsageRecordInput{ID: "id1", CreatedAt: "2023-11-14T22:13:20.123Z"}
	if err := spool.Persist(nil, input); err != nil {
		t.Fatal(err)
	}
	// 第二条触发超容量：重扫后仍超 → 容量上限错误。
	err := spool.Persist(nil, UsageRecordInput{ID: "id2", CreatedAt: "2023-11-14T22:13:20.123Z"})
	if err == nil || !strings.Contains(err.Error(), "容量上限") {
		t.Fatalf("err=%v", err)
	}
	// 超容量重扫的 scanCapacity 错误：Directory 替换为文件。
	brokenDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(brokenDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool2, _ := newTestSpool(t, true)
	spool2.config.MaxItems = 1
	if err := spool2.Persist(nil, input); err != nil {
		t.Fatal(err)
	}
	spool2.config.Directory = brokenDirectory
	if err := spool2.Persist(nil, UsageRecordInput{ID: "id2", CreatedAt: "2023-11-14T22:13:20.123Z"}); err == nil {
		t.Fatal("重扫失败必须透传")
	}
}

func TestW13DSpoolMkdirAllError(t *testing.T) {
	directory := t.TempDir()
	// instance 目录位置被文件占用 → MkdirAll 失败。
	if err := os.WriteFile(filepath.Join(directory, "inst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 10, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	if err := spool.Persist(nil, UsageRecordInput{ID: "id1", CreatedAt: "2023-11-14T22:13:20.123Z"}); err == nil {
		t.Fatal("MkdirAll 失败必须透传")
	}
}

func TestW13DSpoolScanCapacityEntries(t *testing.T) {
	directory := t.TempDir()
	instance := filepath.Join(directory, "inst")
	if err := os.MkdirAll(filepath.Join(instance, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 各类文件：json/tmp/corrupt 计数、无关后缀与子目录跳过、过期 tmp 清理。
	files := map[string]string{
		"a.json":    `{}`,
		"b.tmp":     `{}`,
		"c.corrupt": `{}`,
		"d.txt":     `{}`,
		"e.json":    `{}`,
		"stale.tmp": `{}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(instance, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(instance, "stale.tmp")
	// spool 使用 fixedClock（2023-11-14）：过期阈值按该时钟判定。
	past := time.UnixMilli(1700000000000 - 2*60*60_000)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 100, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	capacity, err := spool.scanCapacity()
	if err != nil {
		t.Fatal(err)
	}
	// a.json + b.tmp + c.corrupt + e.json = 4。
	if capacity.items != 4 {
		t.Fatalf("items=%d", capacity.items)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("过期 tmp 必须被清理")
	}
	// 多实例目录收集（非目录条目被过滤）。
	if err := os.MkdirAll(filepath.Join(directory, "inst2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "loose.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	directories, err := spool.listInstanceDirectories()
	if err != nil || len(directories) != 2 {
		t.Fatalf("directories=%v err=%v", directories, err)
	}
	// listSpoolFiles：round-robin 收集 + limit 截断 + 非 json 跳过。
	if err := os.WriteFile(filepath.Join(directory, "inst2", "f.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "inst2", "skip.tmp"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	spoolFiles, listErr := spool.listSpoolFiles(2)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(spoolFiles) != 2 {
		t.Fatalf("files=%v", spoolFiles)
	}
	for _, file := range spoolFiles {
		if !strings.HasSuffix(file, ".json") {
			t.Fatalf("file=%q", file)
		}
	}
	// 空目录 → 无文件。
	empty := NewUsageRecordSpool(SpoolConfig{
		Directory: t.TempDir(), InstanceID: "inst", MaxItems: 10, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	if files, err := empty.listSpoolFiles(8); err != nil || files != nil {
		t.Fatalf("empty files=%v err=%v", files, err)
	}
}

func TestW13DSpoolReplayOnceErrors(t *testing.T) {
	directory := t.TempDir()
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory: directory, InstanceID: "inst", MaxItems: 10, MaxBytes: 64 * 1024,
		ReplayBatchSize: 8, ReplayIntervalMs: 5, Enabled: true,
	}, fixedClock{ms: 1700000000000}, nil)
	// 空目录 → 0。
	if processed, err := spool.RunReplayOnce(nil, &recordingReplay{}); err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	// 损坏与缺 id 文件 → 隔离为 .corrupt 并继续。
	instance := filepath.Join(directory, "inst")
	if err := os.MkdirAll(instance, 0o700); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(instance, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	noIDPath := filepath.Join(instance, "noid.json")
	if err := os.WriteFile(noIDPath, []byte(`{"traceId":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	processed, err := spool.RunReplayOnce(nil, &recordingReplay{})
	if err != nil || processed != 0 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if _, err := os.Stat(badPath + ".corrupt"); err != nil {
		t.Fatalf("corrupt 隔离失败: %v", err)
	}
	if spool.Runtime().ReplayFailureCount != 2 {
		t.Fatalf("replay failures=%d", spool.Runtime().ReplayFailureCount)
	}
	// Replay 端口错误 → 保留文件并返回错误。
	goodPath := filepath.Join(instance, "good.json")
	good := `{"id":"u1","createdAt":"2023-11-14T22:13:20.123Z","traceId":"t"}`
	if err := os.WriteFile(goodPath, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	replayErr := errors.New("队列不可用")
	if _, err := spool.RunReplayOnce(nil, &recordingReplay{err: replayErr}); !errors.Is(err, replayErr) {
		t.Fatalf("err=%v", err)
	}
	// 恢复后重放成功。
	processed, err = spool.RunReplayOnce(nil, &recordingReplay{})
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	if _, err := os.Stat(goodPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重放后文件必须删除")
	}
	// parseUsageRecord 的两个错误分支直接断言。
	if _, err := parseUsageRecord("{bad", "x.json"); err == nil || !strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("err=%v", err)
	}
	if _, err := parseUsageRecord(`{"traceId":"t"}`, "x.json"); err == nil || !strings.Contains(err.Error(), "id/createdAt") {
		t.Fatalf("err=%v", err)
	}
}

func TestW13DSpoolReplayLoopLifecycle(t *testing.T) {
	spool, _ := newTestSpool(t, true)
	// Disabled 时 StartReplay 不启动。
	disabled, _ := newTestSpool(t, false)
	disabled.StartReplay(&recordingReplay{})
	// 正常生命周期：Start → 重入 Start 被忽略 → Stop。
	spool.StartReplay(&recordingReplay{})
	spool.StartReplay(&recordingReplay{})
	spool.StopReplay()
	time.Sleep(20 * time.Millisecond)
	spool.StopReplay()
	// replayWake 的唤醒路径：StopReplay 后 sleepReplayDelay 立即返回。
	spool.replayMu.Lock()
	spool.replayStop = false
	spool.replayMu.Unlock()
	spool.StopReplay()
	started := time.Now()
	spool.sleepReplayDelay(60_000)
	if time.Since(started) > 5*time.Second {
		t.Fatal("wake 必须立即返回")
	}
	// writeFileSync 对不存在目录报错。
	if err := writeFileSync(filepath.Join(t.TempDir(), "missing", "x.json"), []byte("{}")); err == nil {
		t.Fatal("缺失目录必须报错")
	}
}

// ---------------------------------------------------------------------------
// jsonx
// ---------------------------------------------------------------------------

func TestW13DOrderedObjectEdges(t *testing.T) {
	// nil 接收者的各方法。
	var nilObject *OrderedObject
	if nilObject.Get("k") != nil || nilObject.Has("k") || nilObject.Len() != 0 || nilObject.Keys() != nil {
		t.Fatal("nil 接收者必须安全")
	}
	if cloned := nilObject.Clone(); cloned != nil {
		t.Fatal("nil Clone 必须返回 nil")
	}
	encoded, err := nilObject.MarshalJSON()
	if err != nil || string(encoded) != "null" {
		t.Fatalf("nil Marshal=%s err=%v", encoded, err)
	}
	// Set 在零值对象上初始化 values。
	zero := &OrderedObject{}
	zero.Set("a", 1)
	if zero.Len() != 1 {
		t.Fatal("零值 Set 必须初始化")
	}
	// Clone 深拷贝嵌套数组与对象。
	original := NewOrderedObject()
	inner := NewOrderedObject()
	inner.Set("x", 1)
	original.Set("nested", inner)
	original.Set("list", []any{inner, 2})
	cloned := original.Clone()
	cloned.Get("nested").(*OrderedObject).Set("x", 99)
	if inner.Get("x").(int) != 1 {
		t.Fatal("Clone 必须深拷贝")
	}
	clonedList := cloned.Get("list").([]any)
	clonedList[0].(*OrderedObject).Set("x", 77)
	if inner.Get("x").(int) != 1 {
		t.Fatal("数组元素必须深拷贝")
	}
	// MarshalJSON 的 null 值。
	withNil := NewOrderedObject()
	withNil.Set("v", nil)
	encoded, err = withNil.MarshalJSON()
	if err != nil || string(encoded) != `{"v":null}` {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	// UnmarshalJSON：null token、非法 token、截断、坏 value。
	var decoded OrderedObject
	if err := decoded.UnmarshalJSON([]byte(`null`)); err != nil {
		t.Fatalf("null 解码必须安全: %v", err)
	}
	if err := decoded.UnmarshalJSON([]byte(`[1]`)); err == nil {
		t.Fatal("非对象必须报错")
	}
	if err := decoded.UnmarshalJSON([]byte(`{"a"`)); err == nil {
		t.Fatal("截断输入必须报错")
	}
	if err := decoded.UnmarshalJSON([]byte(`{"a" 1}`)); err == nil {
		t.Fatal("坏 value 必须报错")
	}
	// 大整数经 UseNumber 保留。
	if err := decoded.UnmarshalJSON([]byte(`{"n":1234567890123456789}`)); err != nil {
		t.Fatal(err)
	}
	if decoded.Get("n") == nil {
		t.Fatal("n 必须存在")
	}
	// formatFloat 的整型与小数分支。
	if formatFloat(3) != "3" || formatFloat(-2.5) != "-2.5" {
		t.Fatal("formatFloat 错误")
	}
}

// ---------------------------------------------------------------------------
// size
// ---------------------------------------------------------------------------

func TestW13DEstimateJSONLikeBytesEdges(t *testing.T) {
	// 循环引用：slice 与 map。
	circular := make([]any, 1)
	circular[0] = circular
	if EstimateJSONLikeBytes(circular, EstimateJSONLikeBytesOptions{}) < 16 {
		t.Fatal("循环 slice 必须按占位计费")
	}
	circularMap := map[string]any{"self": nil}
	circularMap["self"] = circularMap
	if EstimateJSONLikeBytes(circularMap, EstimateJSONLikeBytesOptions{}) < 16 {
		t.Fatal("循环 map 必须按占位计费")
	}
	// nil 指针与非 nil 指针字段。
	type withPointer struct {
		Ptr *int `json:"ptr"`
	}
	if EstimateJSONLikeBytes(withPointer{}, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatal("nil 指针字段必须可估算")
	}
	value := 5
	if EstimateJSONLikeBytes(withPointer{Ptr: &value}, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatal("指针字段必须可估算")
	}
	// 超长字符串走 len*4 上限。
	long := strings.Repeat("汉", 17*1024)
	if got := estimateStringBytes(long, &jsonLikeEstimateContext{maxBytes: -1}); got <= 0 {
		t.Fatal("长字符串必须估算")
	}
	// sliceStringByUTF8Bytes 的截断边界。
	if got := sliceStringByUTF8Bytes("héllo", 2); got != "h" {
		t.Fatalf("slice=%q", got)
	}
	if got := sliceStringByUTF8Bytes("héllo", 3); got != "hé" {
		t.Fatalf("slice=%q", got)
	}
	if got := sliceStringByUTF8Bytes("abc", 0); got != "" {
		t.Fatalf("slice=%q", got)
	}
	// runeUTF8ByteLength 各分支。
	if runeUTF8ByteLength('a') != 1 || runeUTF8ByteLength(0xC3) != 2 || runeUTF8ByteLength(0xE4) != 3 || runeUTF8ByteLength(0xF0) != 4 {
		t.Fatal("rune 长度错误")
	}
	// boundedStringByteLength。
	if got := boundedStringByteLength("abc", 10); got != 3 {
		t.Fatalf("bounded=%d", got)
	}
	if got := boundedStringByteLength("abcdef", 4); got != 4 {
		t.Fatalf("bounded=%d", got)
	}
	if got := boundedStringByteLength("abc", 0); got != 0 {
		t.Fatalf("bounded=%d", got)
	}
	// 估算上限截断（MaxBytes / MaxNodes）。
	huge := EstimateJSONLikeBytes(NewOrderedObject().Set("k", strings.Repeat("a", 100)), EstimateJSONLikeBytesOptions{MaxBytes: 10})
	if huge > 10 {
		t.Fatalf("maxBytes 截断失败: %d", huge)
	}
	if EstimateJSONLikeBytes([]any{1, 2, 3, 4}, EstimateJSONLikeBytesOptions{MaxNodes: 2}) == 0 {
		t.Fatal("maxNodes 限制必须产出估算")
	}
}

// ---------------------------------------------------------------------------
// rfc3339 / trafficsource / ports
// ---------------------------------------------------------------------------

func TestW13DRFC3339Edges(t *testing.T) {
	// 负 offset 与分数秒。
	parsed, ok := parseRFC3339Instant("2026-01-02T03:04:05.5-08:00")
	if !ok {
		t.Fatal("负 offset 必须可解析")
	}
	if parsed.UTC().Format(timeRFC3339Millis) != "2026-01-02T11:04:05.500Z" {
		t.Fatalf("parsed=%s", parsed.UTC().Format(timeRFC3339Millis))
	}
	// 非法日历日期。
	if _, ok := parseRFC3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 日必须失败")
	}
	// 超长分数秒。
	if _, ok := parseRFC3339Instant("2026-01-02T03:04:05.1234567890Z"); ok {
		t.Fatal("超长分数秒必须失败")
	}
	// requiredRFC3339Instant 的中文错误。
	if _, err := requiredRFC3339Instant("nope", "测试字段"); err == nil || !strings.Contains(err.Error(), "测试字段") {
		t.Fatalf("err=%v", err)
	}
	// 闰年 2 月 29 日合法。
	if _, ok := canonicalizeRFC3339Instant("2024-02-29T00:00:00Z"); !ok {
		t.Fatal("闰年 2 月 29 日必须合法")
	}
}

type w13dStringer struct{}

func (w13dStringer) String() string { return "stringer-value" }

func TestW13DTrafficSourceEdges(t *testing.T) {
	// nil → gateway。
	if normalized, err := NormalizeOpenAIGatewayTrafficSource(nil); err != nil || normalized != TrafficSourceGateway {
		t.Fatalf("nil 必须回落 gateway: (%v, %v)", normalized, err)
	}
	// 非法值错误（Stringer / 数字 / 未知字符串）。
	if _, err := NormalizeOpenAIGatewayTrafficSource(w13dStringer{}); err == nil || !strings.Contains(err.Error(), "stringer-value") {
		t.Fatalf("Stringer 错误=%v", err)
	}
	if _, err := NormalizeOpenAIGatewayTrafficSource(42); err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("数字错误=%v", err)
	}
	if _, err := NormalizeOpenAIGatewayTrafficSource("nope"); err == nil {
		t.Fatal("未知字符串必须报错")
	}
	// 判定 helpers。
	if !IsCooldownRetestTrafficSource(TrafficSourceCooldownRetest) || IsCooldownRetestTrafficSource(TrafficSourceGateway) {
		t.Fatal("cooldown 判定错误")
	}
	if IsAccountProbeTrafficSource(TrafficSourceManualAccountTest) || !IsAccountProbeTrafficSource(TrafficSourceAccountHealthCheck) {
		t.Fatal("probe 判定错误")
	}
	if !IsAccountDiagnosticTrafficSource(TrafficSourceManualAccountTest) || !IsAccountDiagnosticTrafficSource(TrafficSourceRuntimeRecoveryProbe) {
		t.Fatal("diagnostic 判定错误")
	}
	if IsAccountDiagnosticTrafficSource(TrafficSourceHybridScoring) {
		t.Fatal("hybrid 不属于 diagnostic")
	}
	// ClockFunc 适配器。
	if ClockFunc(func() time.Time { return time.UnixMilli(42) }).Now().UnixMilli() != 42 {
		t.Fatal("ClockFunc 错误")
	}
}
