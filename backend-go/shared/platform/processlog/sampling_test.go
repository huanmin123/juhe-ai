package processlog

import (
	"strings"
	"testing"
)

func TestLoadStageDetailGateDefaults(t *testing.T) {
	t.Run("standalone defaults to full sampling", func(t *testing.T) {
		gate, err := LoadStageDetailGate(func(string) string { return "" }, false)
		if err != nil {
			t.Fatalf("LoadStageDetailGate unexpected error: %v", err)
		}
		if gate.SamplePermille != DefaultTimingDetailSamplePermilleFull {
			t.Fatalf("standalone default permille = %d, want %d", gate.SamplePermille, DefaultTimingDetailSamplePermilleFull)
		}
		if !gate.TimingDetailSampled("trace_any") {
			t.Fatal("standalone gate must sample every request")
		}
	})
	t.Run("performance defaults to 50 permille", func(t *testing.T) {
		gate, err := LoadStageDetailGate(func(string) string { return "" }, true)
		if err != nil {
			t.Fatalf("LoadStageDetailGate unexpected error: %v", err)
		}
		if gate.SamplePermille != DefaultTimingDetailSamplePermillePerformance {
			t.Fatalf("performance default permille = %d, want %d", gate.SamplePermille, DefaultTimingDetailSamplePermillePerformance)
		}
	})
	t.Run("env override both directions", func(t *testing.T) {
		gate, err := LoadStageDetailGate(func(name string) string {
			if name != "JUHE_AI_GATEWAY_TIMING_DETAIL_SAMPLE_PERMILLE" {
				t.Fatalf("read unexpected env %q", name)
			}
			return "250"
		}, true)
		if err != nil {
			t.Fatalf("LoadStageDetailGate unexpected error: %v", err)
		}
		if gate.SamplePermille != 250 {
			t.Fatalf("permille = %d, want 250", gate.SamplePermille)
		}
	})
	t.Run("non numeric fails startup", func(t *testing.T) {
		if _, err := LoadStageDetailGate(func(string) string { return "half" }, true); err == nil {
			t.Fatal("non numeric permille must fail startup")
		}
	})
	t.Run("out of range fails startup", func(t *testing.T) {
		if _, err := LoadStageDetailGate(func(string) string { return "1001" }, true); err == nil {
			t.Fatal("permille above 1000 must fail startup")
		}
		if _, err := LoadStageDetailGate(func(string) string { return "-1" }, true); err == nil {
			t.Fatal("negative permille must fail startup")
		}
	})
}

func TestTimingDetailSampledBoundaries(t *testing.T) {
	traceID := "trace_1725863412000000000"
	cases := []struct {
		permille int
		want     bool
	}{
		{permille: 0, want: false},
		{permille: 1000, want: true},
		{permille: 1001, want: true},
		{permille: -5, want: false},
	}
	for _, testCase := range cases {
		gate := StageDetailGate{PerformanceMode: true, SamplePermille: testCase.permille}
		if got := gate.TimingDetailSampled(traceID); got != testCase.want {
			t.Fatalf("permille %d sampled = %v, want %v", testCase.permille, got, testCase.want)
		}
	}
	// Outside performance mode the permille boundaries never apply.
	standby := StageDetailGate{PerformanceMode: false, SamplePermille: 0}
	if !standby.TimingDetailSampled(traceID) {
		t.Fatal("standalone gate must ignore permille 0")
	}
}

func TestTimingDetailSampledHashStability(t *testing.T) {
	gate := StageDetailGate{PerformanceMode: true, SamplePermille: 500}
	// The decision is a pure function of the traceId: repeated calls and the
	// empty traceId are stable, and distinct traceIds are not all equal.
	first := gate.TimingDetailSampled("trace_100")
	for range 64 {
		if gate.TimingDetailSampled("trace_100") != first {
			t.Fatal("sampling decision must be stable per traceId")
		}
	}
	// Distinct traceIds (including the empty one and non-ASCII) get stable,
	// individually computed decisions.
	for _, id := range []string{"", "trace_100", "trace_101", "trace_abc", "trace_中文"} {
		gate.TimingDetailSampled(id)
	}
	sampled := 0
	for _, id := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10",
		"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8", "u9", "u10"} {
		if gate.TimingDetailSampled(id) {
			sampled++
		}
	}
	if sampled == 0 || sampled == 20 {
		t.Fatalf("50 permille budget must split the trace population, got %d/20", sampled)
	}
	// Byte-wise equality with the Node contract for ASCII: the FNV-1a over
	// UTF-16 code units must keep ((hash % 1000) < permille) stable across a
	// larger population.
	hits := 0
	for index := range 1000 {
		id := "trace_" + strings.Repeat("a", 3) + strings.Repeat("b", index%7) + strconvUint(index)
		if gate.TimingDetailSampled(id) {
			hits++
		}
	}
	if hits < 400 || hits > 600 {
		t.Fatalf("1000 traceIds under 50%% budget sampled %d, want ~500", hits)
	}
}

func TestShouldWriteStageDetailOrder(t *testing.T) {
	pressureMax := int64(8 * 1024 * 1024)
	sampledTraceID := ""
	unsampled := ""
	for _, candidate := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10"} {
		sampled := uint32(fnv1aUTF16(candidate))%1000 < 500
		if sampled && sampledTraceID == "" {
			sampledTraceID = candidate
		}
		if !sampled && unsampled == "" {
			unsampled = candidate
		}
	}
	if sampledTraceID == "" || unsampled == "" {
		t.Fatal("test fixture: population must split under the 500 permille budget")
	}

	gate := StageDetailGate{PerformanceMode: true, SamplePermille: 500}

	t.Run("outcome whitelist precedes everything", func(t *testing.T) {
		for _, outcome := range []string{"unexpected_failure", "expected_failure", "aborted"} {
			if !gate.ShouldWriteStageDetail(outcome, unsampled, pressureMax, pressureMax) {
				t.Fatalf("outcome %q must always write even under pressure and unsampled", outcome)
			}
		}
	})
	t.Run("standalone mode writes everything", func(t *testing.T) {
		standalone := StageDetailGate{PerformanceMode: false, SamplePermille: 0}
		if !standalone.ShouldWriteStageDetail("success", unsampled, 0, pressureMax) {
			t.Fatal("standalone mode must write unsampled success stages")
		}
	})
	t.Run("pressure gate precedes sampling", func(t *testing.T) {
		if gate.ShouldWriteStageDetail("success", sampledTraceID, pressureMax, pressureMax) {
			t.Fatal("pressure gate must drop even sampled success stages")
		}
		if !gate.ShouldWriteStageDetail("success", sampledTraceID, pressureMax-1, pressureMax) {
			t.Fatal("below the pressure threshold the sampled stage must write")
		}
		if gate.ShouldWriteStageDetail("skipped", unsampled, 0, pressureMax) {
			t.Fatal("unsampled success stages must not write in performance mode")
		}
	})
	t.Run("sampling gate decides the remainder", func(t *testing.T) {
		if !gate.ShouldWriteStageDetail("success", sampledTraceID, 0, pressureMax) {
			t.Fatal("sampled success stages must write")
		}
		if gate.ShouldWriteStageDetail("success", unsampled, 0, pressureMax) {
			t.Fatal("unsampled success stages must drop")
		}
		if !gate.ShouldWriteStageDetail("success", "", 0, 0) {
			t.Fatal("empty traceId without pressure keeps the default sampling path")
		}
	})
}

func strconvUint(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
