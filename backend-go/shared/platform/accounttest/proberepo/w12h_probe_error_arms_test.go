package proberepo

// w12h 补充 arms：Store 各入口在关闭句柄/缺表下的错误传播矩阵，覆盖
// mutation/reader 中的查询与扫描错误分支。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

// 每个入口在关闭句柄上必须返回错误（驱动层错误传播矩阵）。
func TestW12HStoreClosedHandleMatrix(t *testing.T) {
	h := openTestDB(t)
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	queries := []struct {
		name string
		run  func() error
	}{
		{"FindAccountForTest", func() error {
			_, err := h.store.FindAccountForTest(ctx, "w12h-a")
			return err
		}},
		{"LoadAccountForTest", func() error {
			_, err := h.store.LoadAccountForTest(ctx, "w12h-a")
			return err
		}},
		{"FindAccountForGroup", func() error {
			_, err := h.store.FindAccountForGroup(ctx, "g", "w12h-a", "sys")
			return err
		}},
		{"LoadAccountForGroup", func() error {
			_, err := h.store.LoadAccountForGroup(ctx, "g", "w12h-a", "sys")
			return err
		}},
		{"LoadAccountForGroupFull", func() error {
			_, err := h.store.LoadAccountForGroupFull(ctx, "w12h-a")
			return err
		}},
		{"HasAPIKeyEntry", func() error {
			_, err := h.store.HasAPIKeyEntry(ctx, &accountquality.OpenAIAccountCandidate{ID: "w12h-a"}, "fp", "sk")
			return err
		}},
		{"LoadProbeView", func() error {
			_, err := h.store.LoadProbeView(ctx, accountquality.ProbeRequest{AccountID: "w12h-a"})
			return err
		}},
		{"LoadAccountMetadataByIds", func() error {
			_, err := h.store.LoadAccountMetadataByIds(ctx, []string{"w12h-a"})
			return err
		}},
		{"ValidateCoreTables", func() error {
			return h.store.ValidateCoreTables(ctx)
		}},
		{"ListDueForProbe", func() error {
			_, err := h.store.ListDueForProbe(ctx, 10)
			return err
		}},
		{"RecordKeySuccess", func() error {
			_, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{AccountID: "w12h-a", KeyFingerprint: "fp"})
			return err
		}},
		{"RecordKeyFailure", func() error {
			_, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{AccountID: "w12h-a", KeyFingerprint: "fp"})
			return err
		}},
		{"DeferKeyProbe", func() error {
			_, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{AccountID: "w12h-a", KeyFingerprint: "fp"})
			return err
		}},
	}
	for _, query := range queries {
		if err := query.run(); err == nil {
			t.Fatalf("%s 在关闭句柄上必须失败", query.name)
		}
	}
}

// 缺表（未建 schema）时读写入口的失败面。
func TestW12HStoreMissingSchemaArms(t *testing.T) {
	h := openTestDB(t)
	ctx := context.Background()
	if err := h.store.ValidateCoreTables(ctx); err == nil {
		t.Fatal("未建 schema 时 ValidateCoreTables 必须失败")
	}
	if _, err := h.store.ListDueForProbe(ctx, 10); err == nil {
		t.Fatal("未建 schema 时 ListDueForProbe 必须失败")
	}
	if _, err := h.store.LoadAccountForTest(ctx, "w12h-a"); err == nil {
		t.Fatal("未建 schema 时 LoadAccountForTest 必须失败")
	}
	if _, err := h.store.LoadAccountMetadataByIds(ctx, []string{"w12h-a"}); err == nil {
		t.Fatal("未建 schema 时 LoadAccountMetadataByIds 必须失败")
	}
	if _, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{AccountID: "w12h-a", KeyFingerprint: "fp"}); err == nil {
		t.Fatal("未建 schema 时 RecordKeySuccess 必须失败")
	}
	if _, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{AccountID: "w12h-a", KeyFingerprint: "fp"}); err == nil {
		t.Fatal("未建 schema 时 DeferKeyProbe 必须失败")
	}
	if _, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{AccountID: "w12h-a", KeyFingerprint: "fp"}); err == nil {
		t.Fatal("未建 schema 时 RecordKeyFailure 必须失败")
	}
}

type w12hFailingResult struct {
	affected int64
	err      error
}

func (r w12hFailingResult) LastInsertId() (int64, error) { return 0, r.err }
func (r w12hFailingResult) RowsAffected() (int64, error) { return r.affected, r.err }

func TestW12HMutationHelperArms(t *testing.T) {
	// rowsToResult：RowsAffected 错误、命中、fence 未命中、无 fence 未命中。
	if _, err := rowsToResult(w12hFailingResult{err: errors.New("w12h rows affected")}, true); err == nil {
		t.Fatal("RowsAffected 错误必须传播")
	}
	hit := w12hFailingResult{affected: 1}
	if got, err := rowsToResult(hit, true); err != nil || !got.Changed {
		t.Fatalf("命中行应 Changed: %+v %v", got, err)
	}
	miss := w12hFailingResult{}
	if got, _ := rowsToResult(miss, true); got.Changed {
		t.Fatal("未命中且提供 fence 应返回 stale_probe_state")
	}
	failing := w12hFailingResult{err: errors.New("w12h rows affected")}
	if _, err := rowsToResult(failing, true); err == nil {
		t.Fatal("RowsAffected 错误必须传播")
	}
	skipped, err := rowsToResult(w12hFailingResult{}, false)
	if err != nil || skipped.Changed {
		t.Fatalf("无 fence 未命中应为零值: %+v %v", skipped, err)
	}
	// fenceProvided 未命中 → stale_probe_state。
	unchanged := changedFalse("stale_probe_state")
	if unchanged.Changed {
		t.Fatal("changedFalse 不应命中")
	}
	// normalizeObservedAt。
	if got := normalizeObservedAt("", "fb"); got != "fb" {
		t.Fatalf("空 observed 回落 fallback: %q", got)
	}
	if got := normalizeObservedAt("bad", "fb"); got != "fb" {
		t.Fatalf("非法 observed 回落 fallback: %q", got)
	}
	older := testNow.Add(-time.Hour).UTC().Format(rfc3339Milli)
	if got := normalizeObservedAt(older, nowMillisText()); got != older {
		t.Fatalf("更早 observed 应被采用: %q", got)
	}
	if got := normalizeObservedAt(nowMillisText(), nowMillisText()); got != nowMillisText() {
		t.Fatalf("相同时间戳应原样: %q", got)
	}
	// normalizeTraceID。
	if normalizeTraceID("") != nil {
		t.Fatal("空 trace 必须为 nil")
	}
	long := strings.Repeat("t", 300)
	if got := normalizeTraceID(long); len(got.(string)) != 200 {
		t.Fatalf("超长 trace 必须截断: %d", len(got.(string)))
	}
	if normalizeTraceID(" trace ") != "trace" {
		t.Fatal("trace 应 trim")
	}
	// passive 抖动窗口各档。
	cases := []struct {
		interval, want int64
	}{
		{0, 0}, {1000, 500}, {120_000, 30_000}, {7_200_000, 1_800_000},
		{5 * 86_400_000, 3_600_000}, {30 * 86_400_000, 8 * 3_600_000},
	}
	for _, test := range cases {
		if got := passiveJitterWindowMS(test.interval); got != test.want {
			t.Fatalf("passiveJitterWindowMS(%d)=%d, want %d", test.interval, got, test.want)
		}
	}
	// 延迟下限。
	for i := 0; i < 50; i++ {
		if got := passiveScheduleDelayMS(0); got < 1 {
			t.Fatalf("延迟必须严格正: %d", got)
		}
	}
	// normalizeProbeDeferSeconds。
	if normalizeProbeDeferSeconds(0) != initialProbeBackoffSeconds {
		t.Fatal("下限钳制不符")
	}
	if normalizeProbeDeferSeconds(1 << 30) != maxProbeBackoffSeconds {
		t.Fatal("上限钳制不符")
	}
	if normalizeProbeDeferSeconds(120) != 120 {
		t.Fatal("中间值原样")
	}
}

func TestW12HNewStoreValidationAndListDueClamp(t *testing.T) {
	if _, err := NewStore(Config{}); err == nil {
		t.Fatal("nil DB 必须报错")
	}
	h := openTestDB(t)
	h.seedSchema(t)
	ctx := context.Background()
	// limit<1 钳制为 1（不报错）。
	if _, err := h.store.ListDueForProbe(ctx, 0); err != nil {
		t.Fatalf("limit 钳制不应报错: %v", err)
	}
}
