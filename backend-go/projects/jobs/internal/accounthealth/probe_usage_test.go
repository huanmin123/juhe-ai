package accounthealth

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

type probeUsageFakeSink struct {
	observations []ProbeUsageObservation
}

func (s *probeUsageFakeSink) RecordProbeUsage(_ context.Context, observation ProbeUsageObservation) error {
	s.observations = append(s.observations, observation)
	return nil
}

func probeUsageRunner(sink ProbeUsageRecorder) *Runner {
	return &Runner{logger: slog.Default(), usageRecorder: sink}
}

// TestProbeTrafficSourceMapping 锚定分类映射，并与 usagewriter 常量对照防漂移。
func TestProbeTrafficSourceMapping(t *testing.T) {
	if probeTrafficSourceForKind("health") != usagewriter.TrafficSourceAccountHealthCheck {
		t.Fatal("scheduled health must map to account_health_check")
	}
	if probeTrafficSourceForKind("cooldown_retest") != usagewriter.TrafficSourceCooldownRetest {
		t.Fatal("cooldown kind must map to cooldown_retest")
	}
	if probeTrafficSourceForReason("request_failure") != usagewriter.TrafficSourceAccountHealthCheck {
		t.Fatal("request_failure must map to account_health_check")
	}
	if probeTrafficSourceForReason("runtime_reset_recovery") != usagewriter.TrafficSourceRuntimeRecoveryProb {
		t.Fatal("runtime recovery/reset must map to runtime_recovery_probe")
	}
	if probeTrafficSourceForReason("cooldown_retest") != usagewriter.TrafficSourceCooldownRetest {
		t.Fatal("explicit cooldown must map to cooldown_retest")
	}
}

func probeUsageOutcome(outcome string) Outcome {
	return Outcome{
		OutcomeID:  "och_test",
		AccountID:  "acc-1",
		Outcome:    outcome,
		ObservedAt: time.Date(2026, 9, 27, 0, 30, 0, 0, time.UTC),
		StatusCode: 200,
	}
}

// TestRecordProbeUsage 只补记真实探针观测：stale / task_failure 不产生使用
// 记录；成功与上游失败都产生且 Success 按结果分类。
func TestRecordProbeUsage(t *testing.T) {
	cases := []struct {
		name      string
		outcome   string
		wantCount int
		wantSucc  bool
	}{
		{"success", OutcomeSuccess, 1, true},
		{"neutral", OutcomeNeutral, 1, false},
		{"upstream failure", OutcomeUpstreamFailed, 1, false},
		{"stale skipped", OutcomeStale, 0, false},
		{"task failure skipped", OutcomeTaskFailed, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &probeUsageFakeSink{}
			runner := probeUsageRunner(sink)
			runner.recordProbeUsage(context.Background(), probeUsageOutcome(tc.outcome), Input{HealthModel: "gpt-test"}, "account_health_check")
			if len(sink.observations) != tc.wantCount {
				t.Fatalf("observations = %d, want %d", len(sink.observations), tc.wantCount)
			}
			if tc.wantCount == 1 && sink.observations[0].Success != tc.wantSucc {
				t.Fatalf("success = %v, want %v", sink.observations[0].Success, tc.wantSucc)
			}
		})
	}
}

// TestRecordProbeUsageNilRecorder 未装配时零行为。
func TestRecordProbeUsageNilRecorder(t *testing.T) {
	runner := &Runner{logger: slog.Default()}
	runner.recordProbeUsage(context.Background(), probeUsageOutcome(OutcomeSuccess), Input{}, "account_health_check")
}

// TestEndpointFamilyForMode 对齐 direct_input_reader 的端点族映射。
func TestEndpointFamilyForMode(t *testing.T) {
	cases := map[string]string{
		"chat_json":             "chat_completions",
		"chat_sse":              "chat_completions",
		"responses_json":        "responses",
		"responses_sse":         "responses",
		"messages_json":         "messages",
		"generate_content_json": "generate_content",
		"generate_content_sse":  "stream_generate_content",
		"interactions_sse":      "interactions",
		"images_json":           "images",
		"unknown_mode":          "",
	}
	for mode, want := range cases {
		if got := endpointFamilyForMode(mode); got != want {
			t.Fatalf("endpointFamily(%q) = %q, want %q", mode, got, want)
		}
	}
}
